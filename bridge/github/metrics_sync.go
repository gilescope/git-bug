package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/oauth2"

	"github.com/git-bug/git-bug/bridge/core/auth"
	"github.com/git-bug/git-bug/cache"
)

// SyncWorkflowMetrics pulls the most recent workflow runs on the repo's
// default branch, resolves their jobs, and appends per-job duration +
// status datapoints into local metric series.
//
// Ingestion is pull-style and idempotent: we look up the highest
// workflow-run id we've already written to the bridge config and skip
// anything at or below it. New runs get recorded; old runs never
// re-ingested. Workflow runs are immutable once complete, so there's
// no update semantics to worry about.
//
// Scope:
//   - default branch only (no PR branches) — keeps the rate-limit budget
//     predictable; PR results would 10x the API calls on a busy repo.
//   - at most `limit` runs per call — typically 20, so the first sync
//     after a long gap won't torch the API budget.
//
// Emitted series (one per (workflow, job, os, arch)):
//   - ci.job.duration{...}  unit: s     value: seconds
//   - ci.job.status{...}    unit: count value: 1 on success, 0 otherwise
//
// Per-point attrs:
//   - runId (GitHub workflow run id)
//   - runUrl (html_url)
//   - commit (head_sha)
func SyncWorkflowMetrics(ctx context.Context, repo *cache.RepoCache, limit int) error {
	return syncMetricsWithOptions(ctx, repo, syncMetricsOptions{maxRuns: limit})
}

// BackfillWorkflowMetrics walks workflow runs back in time until it
// hits one older than `since`, ingesting every completed run along
// the way. Unlike SyncWorkflowMetrics it ignores the lastSeenRunId
// guard — backfill is a deliberate "give me history" call, not the
// idempotent incremental loop. Already-ingested runs are silently
// re-recorded; that's safe because each ingest just appends to the
// series and a duplicate point is a tolerable cost for the much
// simpler "no run-id bookkeeping" implementation.
//
// pageCap bounds the number of REST pages we'll walk in a single
// call so a misconfigured `since` doesn't accidentally pull years
// of history. 20 pages × 100 runs = 2000 runs, plenty for any
// reasonable look-back window.
func BackfillWorkflowMetrics(ctx context.Context, repo *cache.RepoCache, since time.Time) error {
	return syncMetricsWithOptions(ctx, repo, syncMetricsOptions{since: since})
}

type syncMetricsOptions struct {
	// Exactly one of these is meaningful:
	//   maxRuns > 0   → incremental: at most this many recent runs,
	//                    skipping anything at-or-below lastSeenRunId.
	//   since.IsZero==false → backfill: walk pages until a run older
	//                    than `since`, ignoring the lastSeenRunId
	//                    guard so the user can re-pull arbitrarily.
	maxRuns int
	since   time.Time
}

const backfillPageCap = 20 // 100 runs/page × 20 = 2000 runs ceiling

func syncMetricsWithOptions(ctx context.Context, repo *cache.RepoCache, opts syncMetricsOptions) error {
	cfg, err := readGithubConfig(repo)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil // no github bridge, nothing to do
	}
	token, err := lookupToken(repo, cfg.login)
	if err != nil {
		return err
	}
	client := oauth2Client(ctx, token)

	branch, err := fetchDefaultBranch(ctx, client, cfg.owner, cfg.project)
	if err != nil {
		return err
	}

	if !opts.since.IsZero() {
		return backfillByTime(ctx, client, repo, cfg.owner, cfg.project, branch, opts.since)
	}
	return incrementalByCount(ctx, client, repo, cfg.owner, cfg.project, branch, opts.maxRuns)
}

func incrementalByCount(ctx context.Context, client *http.Client, repo *cache.RepoCache, owner, project, branch string, limit int) error {
	if limit <= 0 {
		limit = 20
	}
	lastSeen, _ := readLastSeenRunId(repo)

	runs, err := listWorkflowRuns(ctx, client, owner, project, branch, limit, 1)
	if err != nil {
		return err
	}

	// Runs come back newest-first. Walk in reverse so we write oldest
	// first — lets us persist `lastSeenRunId` incrementally, and if
	// we're interrupted mid-sync we don't skip over anything on the
	// next run.
	var maxId int64 = lastSeen
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if run.Id <= lastSeen {
			continue
		}
		if run.Status != "completed" {
			continue
		}
		if err := ingestRunJobs(ctx, client, repo.Metrics(), owner, project, run); err != nil {
			continue
		}
		if run.Id > maxId {
			maxId = run.Id
		}
	}

	if maxId > lastSeen {
		_ = writeLastSeenRunId(repo, maxId)
	}
	return nil
}

func backfillByTime(ctx context.Context, client *http.Client, repo *cache.RepoCache, owner, project, branch string, since time.Time) error {
	for page := 1; page <= backfillPageCap; page++ {
		runs, err := listWorkflowRuns(ctx, client, owner, project, branch, 100, page)
		if err != nil {
			return err
		}
		if len(runs) == 0 {
			return nil // walked the whole branch history
		}
		for _, run := range runs {
			// Done as soon as we see a run older than the cutoff —
			// list returns newest-first by created_at, so subsequent
			// pages would only carry yet-older runs.
			if run.CreatedAt.Before(since) {
				return nil
			}
			if run.Status != "completed" {
				continue
			}
			if err := ingestRunJobs(ctx, client, repo.Metrics(), owner, project, run); err != nil {
				continue
			}
		}
	}
	return nil
}

// lookupToken is the same auth dance as checks.go / projects_sync.go —
// find the token stored under the bridge config's default login.
func lookupToken(repo *cache.RepoCache, login string) (*auth.Token, error) {
	creds, err := auth.List(repo,
		auth.WithTarget(target),
		auth.WithKind(auth.KindToken),
		auth.WithMeta(auth.MetaKeyLogin, login),
	)
	if err != nil {
		return nil, fmt.Errorf("find github token: %w", err)
	}
	if len(creds) == 0 {
		return nil, ErrMissingIdentityToken
	}
	return creds[0].(*auth.Token), nil
}

// oauth2Client builds a Bearer-authenticated http.Client. Separate
// from rateLimitHandlerClient because we're using REST, not GraphQL.
func oauth2Client(ctx context.Context, token *auth.Token) *http.Client {
	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token.Value})
	c := oauth2.NewClient(ctx, src)
	c.Timeout = defaultTimeout
	return c
}

// fetchDefaultBranch asks REST for the repo metadata and returns the
// default branch name. Cached once per call; callers at higher levels
// (bulk sync) could memoize across repos sharing an owner.
func fetchDefaultBranch(ctx context.Context, client *http.Client, owner, project string) (string, error) {
	u := fmt.Sprintf("%s/repos/%s/%s", githubV3Url, url.PathEscape(owner), url.PathEscape(project))
	var resp struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := restGET(ctx, client, u, &resp); err != nil {
		return "", fmt.Errorf("github repo metadata: %w", err)
	}
	if resp.DefaultBranch == "" {
		return "main", nil // sensible fallback
	}
	return resp.DefaultBranch, nil
}

// workflowRun is the subset of GitHub's workflow-run schema we care
// about. Full schema is huge and mostly irrelevant for metrics.
//
// `Path` is the workflow file path (e.g. `.github/workflows/ci.yml`)
// and is the STABLE identifier across runs. `Name` is the rendered
// display name, which can be dynamic — some workflows use
// `name: ${{ github.event.head_commit.message }}`, producing a new
// string every run — so we don't use it for series identity.
type workflowRun struct {
	Id          int64     `json:"id"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	HeadSha     string    `json:"head_sha"`
	HeadBranch  string    `json:"head_branch"`
	Status      string    `json:"status"`     // queued | in_progress | completed
	Conclusion  string    `json:"conclusion"` // success | failure | cancelled | ...
	WorkflowId  int64     `json:"workflow_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	RunStartedAt time.Time `json:"run_started_at"`
	HtmlUrl     string    `json:"html_url"`
}

func listWorkflowRuns(ctx context.Context, client *http.Client, owner, project, branch string, limit, page int) ([]workflowRun, error) {
	per := limit
	if per > 100 {
		per = 100
	}
	if page <= 0 {
		page = 1
	}
	u := fmt.Sprintf("%s/repos/%s/%s/actions/runs?branch=%s&per_page=%d&page=%d",
		githubV3Url, url.PathEscape(owner), url.PathEscape(project),
		url.QueryEscape(branch), per, page)

	var resp struct {
		WorkflowRuns []workflowRun `json:"workflow_runs"`
	}
	if err := restGET(ctx, client, u, &resp); err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	if len(resp.WorkflowRuns) > limit {
		resp.WorkflowRuns = resp.WorkflowRuns[:limit]
	}
	return resp.WorkflowRuns, nil
}

// workflowJob is the per-job data inside a run. Runners carry the
// os/arch labels we need for flaky-test triage ("was it just on the
// linux-arm64 runner?"). Steps are available but excluded — step-
// level noise dwarfs signal for macro-level metrics.
type workflowJob struct {
	Id          int64     `json:"id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	RunnerName  string    `json:"runner_name"`
	Labels      []string  `json:"labels"`
	HtmlUrl     string    `json:"html_url"`
}

func listJobsForRun(ctx context.Context, client *http.Client, owner, project string, runId int64) ([]workflowJob, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d/jobs?per_page=100",
		githubV3Url, url.PathEscape(owner), url.PathEscape(project), runId)
	var resp struct {
		Jobs []workflowJob `json:"jobs"`
	}
	if err := restGET(ctx, client, u, &resp); err != nil {
		return nil, fmt.Errorf("list jobs for run %d: %w", runId, err)
	}
	return resp.Jobs, nil
}

// ingestRunJobs walks jobs for one workflow run and emits
// duration/status datapoints per (workflow, job, os, arch) combo.
func ingestRunJobs(ctx context.Context, client *http.Client, metrics *cache.RepoCacheMetric, owner, project string, run workflowRun) error {
	jobs, err := listJobsForRun(ctx, client, owner, project, run.Id)
	if err != nil {
		return err
	}

	// Stable identity: use the workflow *file path* (or numeric id as
	// a fallback) — these don't change across runs. Display name goes
	// on attrs so the UI can still show a human label.
	workflowKey := run.Path
	if workflowKey == "" {
		workflowKey = "workflow-" + strconv.FormatInt(run.WorkflowId, 10)
	}

	for _, job := range jobs {
		if job.Status != "completed" {
			continue
		}
		// os/arch come out of the runner labels — GitHub tags the
		// first as the platform ("ubuntu-latest", "windows-2022",
		// "macos-14-arm64"). We try to tease out a stable token so
		// aggregation groups sensibly across upgrades.
		os, arch := deriveOSArch(job.Labels)

		labels := map[string]string{
			"workflow": workflowKey,
			"job":      job.Name,
			"os":       os,
			"arch":     arch,
		}
		attrs := map[string]string{
			"runId":        strconv.FormatInt(run.Id, 10),
			"runUrl":       run.HtmlUrl,
			"jobUrl":       job.HtmlUrl,
			"commit":       run.HeadSha,
			"branch":       run.HeadBranch,
			"concl":        job.Conclusion,
			"workflowName": run.Name,
		}

		dur := job.CompletedAt.Sub(job.StartedAt).Seconds()
		if dur < 0 {
			dur = 0
		}
		if err := recordSeries(metrics, "ci.job.duration", "s", labels, job.CompletedAt, dur, attrs); err != nil {
			return err
		}

		statusValue := 0.0
		if job.Conclusion == "success" {
			statusValue = 1.0
		}
		if err := recordSeries(metrics, "ci.job.status", "count", labels, job.CompletedAt, statusValue, attrs); err != nil {
			return err
		}
	}
	return nil
}

// recordSeries is GetOrCreate + Record. Common enough to warrant a
// thin helper so ingestion loops stay legible.
func recordSeries(metrics *cache.RepoCacheMetric, name, unit string, labels map[string]string, eventTime time.Time, value float64, attrs map[string]string) error {
	s, err := metrics.GetOrCreate(name, labels, unit, "github.com/actions")
	if err != nil {
		return err
	}
	_, err = s.Record(eventTime, value, attrs)
	return err
}

// deriveOSArch picks a canonical (os, arch) pair from a runner's
// labels list. GitHub's label scheme isn't fully documented but the
// common values are well known.
func deriveOSArch(labels []string) (string, string) {
	os := ""
	arch := "x86_64"
	for _, lbl := range labels {
		switch lbl {
		case "ubuntu-latest", "ubuntu-22.04", "ubuntu-24.04", "ubuntu-20.04":
			os = "linux"
		case "windows-latest", "windows-2022", "windows-2019":
			os = "windows"
		case "macos-latest", "macos-14", "macos-13":
			os = "macos"
			arch = "arm64"
		case "macos-12", "macos-11":
			os = "macos"
		case "macos-14-arm64", "macos-13-arm64":
			os = "macos"
			arch = "arm64"
		}
	}
	if os == "" {
		os = "unknown"
	}
	return os, arch
}

// restGET does a JSON GET with context + error-body surfacing.
func restGET(ctx context.Context, client *http.Client, u string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// lastSeenRunId lives in git-bug's local config under
// git-bug.bridge.github.metrics.lastRunId.v3. Keeping it out of git
// history: it's per-checkout state, not something we want to share
// over a push/pull bridge. Fresh clones re-ingest from zero, which
// takes one burst but costs ~1 graphql-point per call and stabilizes.
//
// Version bumps:
//   v1 → v2: label fix — `workflow` went from dynamic display name
//            to stable workflow file path.
//   v2 → v3: auto-commit fix — Record op used to stage without
//            committing, so ingested points evaporated on cache
//            re-resolve; the v2 sync wrote excerpts but no actual
//            git objects.
// Each bump forces a one-off re-walk of the most recent runs so the
// fix takes effect without requiring a manual wipe.
const confKeyLastRunId = "metrics.lastRunId.v3"

func readLastSeenRunId(repo *cache.RepoCache) (int64, error) {
	kv, err := repo.LocalConfig().ReadAll("git-bug.bridge.github." + confKeyLastRunId)
	if err != nil || len(kv) == 0 {
		return 0, nil
	}
	v := kv["git-bug.bridge.github."+confKeyLastRunId]
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, nil
	}
	return n, nil
}

func writeLastSeenRunId(repo *cache.RepoCache, id int64) error {
	return repo.LocalConfig().StoreString(
		"git-bug.bridge.github."+confKeyLastRunId,
		strconv.FormatInt(id, 10),
	)
}
