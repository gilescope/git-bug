package http

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/git-bug/git-bug/cache"
	"github.com/git-bug/git-bug/entities/metric"
)

// MetricsHandler serves metric series + points from the repo's git-bug
// cache. Two URL shapes:
//
//   GET /metrics/{repo}                         list all series (light)
//   GET /metrics/{repo}/{seriesIdPrefix}        one series with its points
//
// Listing returns only excerpt data (name, labels, point count, last
// time). Detail returns the full point log, optionally filtered by
// ?since= / ?until= / ?match= so the UI can scope a graph without
// pulling a multi-year history over the wire.
type MetricsHandler struct {
	mrc *cache.MultiRepoCache
}

func NewMetricsHandler(mrc *cache.MultiRepoCache) *MetricsHandler {
	return &MetricsHandler{mrc: mrc}
}

func (h *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	vars := mux.Vars(r)
	repoName := vars["repo"]
	repo, err := h.mrc.ResolveRepo(repoName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	q := r.URL.Query()

	// ?id=<prefix> switches from list mode to detail. Kept on the
	// query string because repo names contain slashes — putting the
	// id in the path would make mux routing ambiguous.
	if id := strings.TrimSpace(firstQuery(q, "id")); id != "" {
		h.serveOne(w, repo, id, q)
		return
	}
	h.serveList(w, repo, q)
}

// listEntry mirrors the excerpt wire shape. Flat so the frontend can
// render a table without extra nesting.
type listEntry struct {
	Id         string            `json:"id"`
	Name       string            `json:"name"`
	Labels     map[string]string `json:"labels,omitempty"`
	LabelKey   string            `json:"labelKey"`
	Unit       string            `json:"unit,omitempty"`
	Source     string            `json:"source,omitempty"`
	PointCount int               `json:"pointCount"`
	LastTime   string            `json:"lastTime,omitempty"`
	Retired    bool              `json:"retired,omitempty"`
}

type listResponse struct {
	Series []listEntry `json:"series"`
}

func (h *MetricsHandler) serveList(w http.ResponseWriter, repo *cache.RepoCache, q map[string][]string) {
	match := firstQuery(q, "match")
	showRetired := firstQuery(q, "retired") == "1"

	ids := repo.Metrics().AllIds()
	out := make([]listEntry, 0, len(ids))
	for _, id := range ids {
		ex, err := repo.Metrics().ResolveExcerpt(id)
		if err != nil {
			continue
		}
		if !showRetired && ex.Retired {
			continue
		}
		if match != "" && !strings.Contains(ex.LabelKey, match) {
			continue
		}
		out = append(out, excerptToListEntry(ex))
	}
	// Newest data first — lets the UI show "recent activity" first by
	// default without the client having to sort.
	sort.Slice(out, func(i, j int) bool { return out[i].LastTime > out[j].LastTime })

	writeJSON(w, listResponse{Series: out})
}

func excerptToListEntry(ex *cache.MetricExcerpt) listEntry {
	labels := map[string]string{}
	for _, kv := range ex.Labels {
		if i := strings.Index(kv, "="); i > 0 {
			labels[kv[:i]] = kv[i+1:]
		}
	}
	last := ""
	if ex.LastTimeUnix > 0 {
		last = time.Unix(ex.LastTimeUnix, 0).UTC().Format(time.RFC3339)
	}
	return listEntry{
		Id:         ex.Id().String(),
		Name:       ex.Name,
		Labels:     labels,
		LabelKey:   ex.LabelKey,
		Unit:       ex.Unit,
		Source:     ex.Source,
		PointCount: ex.PointCount,
		LastTime:   last,
		Retired:    ex.Retired,
	}
}

// detailResponse is the shape for GET /metrics/{repo}/{id}.
type detailResponse struct {
	Id       string            `json:"id"`
	Name     string            `json:"name"`
	Labels   map[string]string `json:"labels,omitempty"`
	LabelKey string            `json:"labelKey"`
	Unit     string            `json:"unit,omitempty"`
	Source   string            `json:"source,omitempty"`
	Retired  bool              `json:"retired,omitempty"`
	Points   []pointJSON       `json:"points"`
}

type pointJSON struct {
	Time  string            `json:"time"`
	Value float64           `json:"value"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

func (h *MetricsHandler) serveOne(w http.ResponseWriter, repo *cache.RepoCache, idPrefix string, q map[string][]string) {
	c, err := repo.Metrics().ResolvePrefix(idPrefix)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	snap := c.Snapshot()

	since, untilErr := parseTimeQuery(q, "since")
	until, sinceErr := parseTimeQuery(q, "until")
	if untilErr != nil || sinceErr != nil {
		http.Error(w, "since/until must be RFC3339 or unix seconds", http.StatusBadRequest)
		return
	}

	pts := make([]pointJSON, 0, len(snap.Points))
	for _, p := range snap.Points {
		if since != nil && p.Time.Before(*since) {
			continue
		}
		if until != nil && !p.Time.Before(*until) {
			continue
		}
		pts = append(pts, pointJSON{
			Time:  p.Time.UTC().Format(time.RFC3339),
			Value: p.Value,
			Attrs: p.Attrs,
		})
	}
	// Sort ascending by time so the UI can plot left-to-right without
	// re-sorting. Points can arrive out of wall-clock order because of
	// clock skew between contributors.
	sort.Slice(pts, func(i, j int) bool { return pts[i].Time < pts[j].Time })

	writeJSON(w, detailResponse{
		Id:       c.Id().String(),
		Name:     snap.Name,
		Labels:   snap.Labels,
		LabelKey: metric.LabelKey(snap.Name, snap.Labels),
		Unit:     snap.Unit,
		Source:   snap.Source,
		Retired:  snap.Retired,
		Points:   pts,
	})
}

func firstQuery(q map[string][]string, key string) string {
	v, ok := q[key]
	if !ok || len(v) == 0 {
		return ""
	}
	return v[0]
}

// parseTimeQuery turns a ?since=/?until= value (RFC3339 or unix
// seconds) into *time.Time. Empty ⇒ nil (no filter). Returns an
// error for malformed input so the HTTP layer can 400 cleanly
// instead of silently dropping the filter.
func parseTimeQuery(q map[string][]string, key string) (*time.Time, error) {
	s := firstQuery(q, key)
	if s == "" {
		return nil, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		t := time.Unix(n, 0)
		return &t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
