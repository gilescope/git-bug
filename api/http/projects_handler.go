package http

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/git-bug/git-bug/bridge/github"
	"github.com/git-bug/git-bug/cache"
)

// ProjectsHandler serves the GitHub Projects V2 snapshot for a repo.
//
//	GET /projects/{repo}            → read the cached snapshot from disk
//	POST /projects/{repo}/sync      → re-fetch from GitHub and rewrite it
//
// The snapshot lives in <parent-of-repo>/.git-bug-projects.json so two
// repos under the same owner share one file (GitHub Projects V2 are
// owned per user/org, not per repo — that's the natural scope).
//
// Concurrency: a sync takes 1–30 s against GitHub. We coalesce concurrent
// sync requests per repo via inflight so the second tab-flip during the
// same sync doesn't double-fetch.
type ProjectsHandler struct {
	mrc *cache.MultiRepoCache
	// repoPath maps registered-repo-name → filesystem path. Needed so the
	// handler can resolve <parent-of-repo>/.git-bug-projects.json without
	// groveling inside the gogit internals. Populated at wiring time in
	// commands/webui.go.
	repoPath map[string]string

	mu       sync.Mutex
	inflight map[string]*syncRun
}

type syncRun struct {
	done chan struct{}
	snap *github.ProjectsSnapshot
	err  error
}

func NewProjectsHandler(mrc *cache.MultiRepoCache, repoPath map[string]string) *ProjectsHandler {
	return &ProjectsHandler{
		mrc:      mrc,
		repoPath: repoPath,
		inflight: map[string]*syncRun{},
	}
}

func (h *ProjectsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	repoName := vars["repo"]

	path, ok := h.repoPath[repoName]
	if !ok {
		http.Error(w, "unknown repo: "+repoName, http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.serveRead(w, path)
	case http.MethodPost:
		h.serveSync(w, r, repoName, path)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveRead answers GET: read whatever the last sync wrote. Returns an
// empty body (null JSON) if nothing has been written yet, so the UI can
// distinguish "not synced" from "synced but empty".
func (h *ProjectsHandler) serveRead(w http.ResponseWriter, repoPath string) {
	snap, err := github.ReadProjectsSnapshot(repoPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

// serveSync answers POST: re-fetch from GitHub, rewrite the file, return
// the fresh snapshot. Concurrent requests for the same repo share the
// same inflight run.
func (h *ProjectsHandler) serveSync(w http.ResponseWriter, r *http.Request, repoName, repoPath string) {
	repo, err := h.mrc.ResolveRepo(repoName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	run := h.claim(repoName)
	if run.done == nil {
		// We're the owner of this run — actually perform the sync.
		run.done = make(chan struct{})
		h.storeRun(repoName, run)
		go func() {
			// A large project with hundreds of items needs ~20 paginated
			// item queries at ~1–2 s each, plus a fields query and the
			// header list. 60 s cut off real-world runs mid-pagination;
			// 5 min gives plenty of headroom while still capping a stuck
			// fetch. The outer goroutine is detached from the request so
			// the client can disconnect without cancelling mid-sync —
			// the result still lands in the file for next time.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			snap, err := github.SyncProjects(ctx, repo, repoPath)
			h.mu.Lock()
			run.snap = snap
			run.err = err
			h.mu.Unlock()
			close(run.done)
			// Clear inflight so the next sync starts a fresh run.
			h.mu.Lock()
			if h.inflight[repoName] == run {
				delete(h.inflight, repoName)
			}
			h.mu.Unlock()
		}()
	}

	select {
	case <-run.done:
	case <-r.Context().Done():
		http.Error(w, "client cancelled", 499)
		return
	}

	if run.err != nil {
		http.Error(w, run.err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(run.snap)
}

// claim returns the existing inflight run for a repo, or a fresh
// placeholder the caller should populate. The zero `done` channel is
// the signal to the caller "you own this run; please fill it in."
func (h *ProjectsHandler) claim(repoName string) *syncRun {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.inflight[repoName]; ok {
		return existing
	}
	return &syncRun{}
}

func (h *ProjectsHandler) storeRun(repoName string, run *syncRun) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inflight[repoName] = run
}
