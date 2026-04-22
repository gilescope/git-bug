package http

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/git-bug/git-bug/bridge"
	"github.com/git-bug/git-bug/bridge/core"
	"github.com/git-bug/git-bug/bridge/github"
	"github.com/git-bug/git-bug/cache"
)

// SyncHandler exposes a two-method endpoint (GET/POST) that runs
// `git-bug bridge pull` across registered repositories.
//
//   POST /sync                — start a bulk sync of every repo. 409 if
//                                 one is already running.
//   POST /sync?repo=<name>    — start an ad-hoc sync of just <name>. Runs
//                                 independently of any in-flight bulk run
//                                 and returns 409 only if THAT specific
//                                 repo is already being synced.
//   GET  /sync                — return the current status as JSON.
//
// Sync runs are background goroutines; the HTTP response returns
// immediately. The UI polls GET.
type SyncHandler struct {
	mrc *cache.MultiRepoCache
	// repoPath is (name → filesystem path). Bridge syncs read/write
	// <parent-of-repo>/.git-bug-projects.json after a successful import;
	// we need the working-tree path to find that parent directory.
	repoPath map[string]string

	mu         sync.Mutex
	bulk       SyncStatus
	bulkCancel context.CancelFunc
	// Ad-hoc single-repo syncs currently in flight, keyed by repo name.
	// They run concurrently with any bulk sync and with each other, since
	// each repo is self-contained. The only hard rule is that the same
	// repo can't be synced twice at once.
	adhoc map[string]context.CancelFunc
}

// SyncStatus is the shape returned by GET /sync. It is intentionally flat
// and JSON-friendly; the server merges bulk and ad-hoc state into a single
// view for the UI.
type SyncStatus struct {
	// Running is true if anything is syncing (bulk or ad-hoc).
	Running bool `json:"running"`
	// BulkRunning is true only for the all-repos run. The UI uses this to
	// gate the "Sync all" button without blocking "Sync this".
	BulkRunning bool      `json:"bulkRunning"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	FinishedAt  time.Time `json:"finishedAt,omitempty"`
	// Active is the union of repos currently being synced by the bulk pool
	// plus any ad-hoc syncs. The UI uses membership to disable "Sync this"
	// for the repo in question.
	Active []string          `json:"active"`
	Done   int               `json:"done"`
	Total  int               `json:"total"`
	Errors map[string]string `json:"errors,omitempty"`
	// Cumulative counters. Bulk counters reset at each bulk start; ad-hoc
	// counters add into the same fields so the UI sees progress from both.
	ImportedBugs       int `json:"importedBugs"`
	ImportedIdentities int `json:"importedIdentities"`
	// Classification of the bulk run: "initial" have no lastImportTime
	// recorded and will pull everything, "catchup" pull only since the last
	// successful import, "noBridge" have no bridge configured and will
	// no-op. Set at bulk-run start and left stable.
	InitialSyncs  int `json:"initialSyncs"`
	CatchupSyncs  int `json:"catchupSyncs"`
	NoBridgeRepos int `json:"noBridgeRepos"`
	// Last observed GitHub GraphQL rate-limit state (populated by the
	// github bridge whenever it issues a top-level query). Useful for the
	// UI to warn before clicking "Sync all" when the budget is low.
	RateLimit *RateLimitInfo `json:"rateLimit,omitempty"`
}

type RateLimitInfo struct {
	Remaining int       `json:"remaining"`
	Limit     int       `json:"limit"`
	ResetAt   time.Time `json:"resetAt,omitempty"`
}

// Parallelism is split by sync kind. Catchup pulls are cheap (often a single
// GraphQL point if nothing is new) so we fan out, but stay under GitHub's
// undocumented ~20-concurrent-requests secondary rate limit per token.
// Initial pulls fetch every issue/PR/comment and torch the 5000/hr budget
// in minutes, so we keep those serialized more tightly. Two independent
// pools run side by side — a slow initial doesn't block the catchup queue.
const (
	catchupConcurrency = 20
	initialConcurrency = 2
)

func NewSyncHandler(mrc *cache.MultiRepoCache, repoPath map[string]string) *SyncHandler {
	return &SyncHandler{
		mrc:      mrc,
		repoPath: repoPath,
		bulk:     SyncStatus{Active: []string{}, Errors: map[string]string{}},
		adhoc:    map[string]context.CancelFunc{},
	}
}

func (h *SyncHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.writeStatus(w)
	case http.MethodPost:
		h.start(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// snapshot builds a defensive copy of the wire-format status, merging bulk
// state with the ad-hoc repo set. Caller must not hold h.mu.
func (h *SyncHandler) snapshot() SyncStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	snap := h.bulk
	errs := make(map[string]string, len(h.bulk.Errors))
	for k, v := range h.bulk.Errors {
		errs[k] = v
	}
	snap.Errors = errs
	active := make([]string, 0, len(h.bulk.Active)+len(h.adhoc))
	active = append(active, h.bulk.Active...)
	for name := range h.adhoc {
		active = append(active, name)
	}
	snap.Active = active
	snap.BulkRunning = h.bulk.Running
	snap.Running = h.bulk.Running || len(h.adhoc) > 0
	if _, remaining, limit, resetAt := github.LastRateLimit(); limit > 0 {
		snap.RateLimit = &RateLimitInfo{
			Remaining: remaining,
			Limit:     limit,
			ResetAt:   resetAt,
		}
	}
	return snap
}

func (h *SyncHandler) writeStatus(w http.ResponseWriter) {
	snap := h.snapshot()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func (h *SyncHandler) start(w http.ResponseWriter, r *http.Request) {
	repoName := r.URL.Query().Get("repo")
	// ?backfillDays=N runs a backfill instead of an incremental sync.
	// Implies a single-repo target: backfilling every repo at once would
	// burn an enormous amount of API budget for little user benefit.
	backfillDays := 0
	if v := r.URL.Query().Get("backfillDays"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			backfillDays = n
		}
	}
	if repoName == "" {
		if backfillDays > 0 {
			http.Error(w, "?backfillDays requires ?repo=<name>", http.StatusBadRequest)
			return
		}
		h.startBulk(w)
		return
	}
	if backfillDays > 0 {
		h.startBackfill(w, repoName, backfillDays)
		return
	}
	h.startAdhoc(w, repoName)
}

// startBackfill triggers a one-shot CI metrics backfill for a single
// repo over the last `days` days. Doesn't run any of the bug import
// machinery — that's what the regular sync path is for. Backfill is
// metrics-only by design: the user explicitly asked for history, and
// the bug import is already idempotent on every sync anyway.
func (h *SyncHandler) startBackfill(w http.ResponseWriter, repoName string, days int) {
	rc, err := h.mrc.ResolveRepo(repoName)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	h.mu.Lock()
	if _, already := h.adhoc[repoName]; already {
		h.mu.Unlock()
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "this repo is already being synced"})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.adhoc[repoName] = cancel
	h.mu.Unlock()

	go func() {
		since := time.Now().AddDate(0, 0, -days)
		err := github.BackfillWorkflowMetrics(ctx, rc, since)
		h.mu.Lock()
		delete(h.adhoc, repoName)
		if err != nil {
			h.bulk.Errors[repoName] = "backfill: " + err.Error()
		} else {
			delete(h.bulk.Errors, repoName)
		}
		h.mu.Unlock()
	}()

	w.WriteHeader(http.StatusAccepted)
	h.writeStatus(w)
}

func (h *SyncHandler) startBulk(w http.ResponseWriter) {
	repos := h.mrc.AllRepos()

	h.mu.Lock()
	if h.bulk.Running {
		h.mu.Unlock()
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "bulk sync already running"})
		return
	}
	initial, catchup, noBridge := classifySyncs(repos)
	ctx, cancel := context.WithCancel(context.Background())
	h.bulkCancel = cancel
	h.bulk = SyncStatus{
		Running:       true,
		StartedAt:     time.Now(),
		Total:         len(repos),
		Active:        []string{},
		Errors:        map[string]string{},
		InitialSyncs:  initial,
		CatchupSyncs:  catchup,
		NoBridgeRepos: noBridge,
	}
	h.mu.Unlock()

	go h.run(ctx, repos)

	w.WriteHeader(http.StatusAccepted)
	h.writeStatus(w)
}

func (h *SyncHandler) startAdhoc(w http.ResponseWriter, repoName string) {
	rc, err := h.mrc.ResolveRepo(repoName)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	h.mu.Lock()
	if _, already := h.adhoc[repoName]; already {
		h.mu.Unlock()
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "this repo is already being synced"})
		return
	}
	for _, n := range h.bulk.Active {
		if n == repoName {
			h.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "this repo is currently being synced by the bulk run"})
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.adhoc[repoName] = cancel
	h.mu.Unlock()

	go func() {
		bugs, ids, err := syncOneRepo(ctx, rc, h.repoPath[repoName])
		h.mu.Lock()
		delete(h.adhoc, repoName)
		h.bulk.ImportedBugs += bugs
		h.bulk.ImportedIdentities += ids
		if err != nil {
			h.bulk.Errors[repoName] = err.Error()
		} else {
			// Clear any stale error from a previous failed run on this repo
			// — the new success supersedes it.
			delete(h.bulk.Errors, repoName)
		}
		h.mu.Unlock()
	}()

	w.WriteHeader(http.StatusAccepted)
	h.writeStatus(w)
}

func (h *SyncHandler) run(ctx context.Context, repos []*cache.RepoCache) {
	// Deterministic order by name so the UI shows a predictable progression.
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name() < repos[j].Name() })

	// Partition up-front so the two pools don't need to classify on the
	// hot path. noBridge repos are trivial no-ops — count them as Done
	// immediately and skip worker dispatch entirely.
	var catchup, initial []*cache.RepoCache
	for _, r := range repos {
		switch classifyRepo(r) {
		case syncCatchup:
			catchup = append(catchup, r)
		case syncInitial:
			initial = append(initial, r)
		default:
			h.mu.Lock()
			h.bulk.Done++
			h.mu.Unlock()
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.runPool(ctx, catchup, catchupConcurrency) }()
	go func() { defer wg.Done(); h.runPool(ctx, initial, initialConcurrency) }()
	wg.Wait()
	h.finish()
}

// runPool drains `repos` through `workers` parallel workers. Caller is
// responsible for overall finish() — runPool only handles its slice.
func (h *SyncHandler) runPool(ctx context.Context, repos []*cache.RepoCache, workers int) {
	if len(repos) == 0 {
		return
	}
	jobs := make(chan *cache.RepoCache)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for repo := range jobs {
				if ctx.Err() != nil {
					return
				}
				h.markActive(repo.Name(), true)
				bugs, ids, err := syncOneRepo(ctx, repo, h.repoPath[repo.Name()])
				h.markActive(repo.Name(), false)

				h.mu.Lock()
				if err != nil {
					h.bulk.Errors[repo.Name()] = err.Error()
				}
				h.bulk.Done++
				h.bulk.ImportedBugs += bugs
				h.bulk.ImportedIdentities += ids
				h.mu.Unlock()
			}
		}()
	}

	for _, repo := range repos {
		select {
		case jobs <- repo:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		}
	}
	close(jobs)
	wg.Wait()
}

func (h *SyncHandler) markActive(name string, entering bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entering {
		h.bulk.Active = append(h.bulk.Active, name)
		return
	}
	out := h.bulk.Active[:0]
	for _, n := range h.bulk.Active {
		if n != name {
			out = append(out, n)
		}
	}
	h.bulk.Active = out
}

func (h *SyncHandler) finish() {
	h.mu.Lock()
	h.bulk.Running = false
	h.bulk.FinishedAt = time.Now()
	h.bulk.Active = []string{}
	h.mu.Unlock()
}

// classifySyncs buckets each repo by what its sync will actually do:
//   - initial: bridge configured but no lastImportTime → full pull
//   - catchup: bridge configured with lastImportTime → incremental
//   - noBridge: no bridge configured → syncOneRepo no-ops
func classifySyncs(repos []*cache.RepoCache) (initial, catchup, noBridge int) {
	for _, repo := range repos {
		switch classifyRepo(repo) {
		case syncInitial:
			initial++
		case syncCatchup:
			catchup++
		default:
			noBridge++
		}
	}
	return
}

type syncKind int

const (
	syncNoBridge syncKind = iota
	syncInitial
	syncCatchup
)

func classifyRepo(repo *cache.RepoCache) syncKind {
	keys, err := repo.LocalConfig().ReadAll("git-bug.bridge.")
	if err != nil || len(keys) == 0 {
		return syncNoBridge
	}
	for k := range keys {
		if strings.HasSuffix(k, ".lastImportTime") {
			return syncCatchup
		}
	}
	return syncInitial
}

// syncOneRepo runs the equivalent of `git-bug bridge pull` on a single repo.
// Repos without any bridge configured are treated as no-ops, not errors.
//
// After a successful bridge pull, the handler also refreshes the GitHub
// Projects V2 snapshot when repoPath is known — projects are owner-scoped
// rather than per-repo, so the snapshot is written to
// <parent-of-repo>/.git-bug-projects.json and shared between sibling
// checkouts. An error here does NOT fail the sync (projects are an
// optional enhancement); it surfaces as a returned error string but the
// bug/identity counts still apply.
func syncOneRepo(ctx context.Context, repo *cache.RepoCache, repoPath string) (int, int, error) {
	b, err := bridge.DefaultBridge(repo)
	if err != nil {
		return 0, 0, nil
	}

	events, err := b.ImportAll(ctx)
	if err != nil {
		return 0, 0, err
	}

	bugs := 0
	ids := 0
	for result := range events {
		switch result.Event {
		case core.ImportEventBug:
			bugs++
		case core.ImportEventIdentity:
			ids++
		case core.ImportEventError:
			if result.Err != context.Canceled {
				return bugs, ids, result.Err
			}
		}
	}

	// Projects sync is github-only and best-effort. An auth/permission
	// failure here would otherwise poison the whole bulk-sync result;
	// log-swallow instead so partial progress is preserved.
	if repoPath != "" {
		if _, perr := github.SyncProjects(ctx, repo, repoPath); perr != nil {
			return bugs, ids, nil
		}
	}

	// CI metrics ingestion: pull the last N workflow runs on the
	// default branch and record per-job timings as metric series.
	// Idempotent (tracked by last-seen run id in local config), so
	// the second call after a fresh sync is mostly a no-op. Errors
	// are swallowed — missing metrics shouldn't fail the sync.
	_ = github.SyncWorkflowMetrics(ctx, repo, workflowRunsPerSync)

	return bugs, ids, nil
}

// workflowRunsPerSync caps how many workflow runs we pull per call.
// 20 is generous enough that a CI-heavy trunk branch stays caught up
// between syncs (typical rate: ~10 runs/hr at peak), and small enough
// that one sync doesn't burn through the API budget. Tracked runs
// live in local config as the highest-seen run id, so increasing this
// only affects the first sync after a long gap.
const workflowRunsPerSync = 20
