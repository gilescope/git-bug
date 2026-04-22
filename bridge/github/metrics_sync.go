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
	if limit <= 0 {
		limit = 20
	}

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

	lastSeen, _ := readLastSeenRunId(repo)

	runs, err := listWorkflowRuns(ctx, client, cfg.owner, cfg.project, branch, limit)
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
		// Only completed runs have meaningful duration. In-progress
		// runs are skipped — we'll pick them up on the next sync once
		// status transitions to "completed".
		if run.Status != "completed" {
			continue
		}
		if err := ingestRunJobs(ctx, client, repo.Metrics(), cfg.owner, cfg.project, run); err != nil {
			// Per-run failure shouldn't stop the bulk; log via caller
			// by returning at the end. For now, just carry on.
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
type workflowRun struct {
	Id          int64     `json:"id"`
	Name        string    `json:"name"`
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

func listWorkflowRuns(ctx context.Context, client *http.Client, owner, project, branch string, limit int) ([]workflowRun, error) {
	per := limit
	if per > 100 {
		per = 100
	}
	u := fmt.Sprintf("%s/repos/%s/%s/actions/runs?branch=%s&per_page=%d",
		githubV3Url, url.PathEscape(owner), url.PathEscape(project),
		url.QueryEscape(branch), per)

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

	workflowName := run.Name
	if workflowName == "" {
		workflowName = "workflow-" + strconv.FormatInt(run.WorkflowId, 10)
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
			"workflow": workflowName,
			"job":      job.Name,
			"os":       os,
			"arch":     arch,
		}
		attrs := map[string]string{
			"runId":   strconv.FormatInt(run.Id, 10),
			"runUrl":  run.HtmlUrl,
			"jobUrl":  job.HtmlUrl,
			"commit":  run.HeadSha,
			"branch":  run.HeadBranch,
			"concl":   job.Conclusion,
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
// git-bug.bridge.github.metrics.lastRunId. Keeping it out of git
// history: it's per-checkout state, not something we want to share
// over a push/pull bridge. Fresh clones re-ingest from zero, which
// takes one burst but costs ~1 graphql-point per call and stabilizes.
const confKeyLastRunId = "metrics.lastRunId"

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
