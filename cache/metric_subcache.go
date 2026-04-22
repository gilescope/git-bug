package cache

import (
	"time"

	"github.com/git-bug/git-bug/entities/identity"
	"github.com/git-bug/git-bug/entities/metric"
	"github.com/git-bug/git-bug/entity"
	"github.com/git-bug/git-bug/repository"
)

type RepoCacheMetric struct {
	*SubCache[*metric.Series, *MetricExcerpt, *MetricCache]
}

func NewRepoCacheMetric(repo repository.ClockedRepo,
	resolvers func() entity.Resolvers,
	getUserIdentity getUserIdentityFunc) *RepoCacheMetric {

	makeCached := func(s *metric.Series, entityUpdated func(id entity.Id) error) *MetricCache {
		return NewMetricCache(s, repo, getUserIdentity, entityUpdated)
	}

	// indexData feeds bleve — the strings here become searchable via
	// the full-text search bar. Name, label key/values, unit, source
	// is what a reader would type to find a metric.
	makeIndexData := func(c *MetricCache) []string {
		snap := c.Snapshot()
		out := []string{snap.Name, snap.Unit, snap.Source}
		for k, v := range snap.Labels {
			out = append(out, k, v, k+"="+v)
		}
		return out
	}

	actions := Actions[*metric.Series]{
		ReadWithResolver:    metric.ReadWithResolver,
		ReadAllWithResolver: metric.ReadAllWithResolver,
		Remove:              metric.Remove,
		RemoveAll:           metric.RemoveAll,
		MergeAll:            metric.MergeAll,
	}

	sc := NewSubCache[*metric.Series, *MetricExcerpt, *MetricCache](
		repo, resolvers, getUserIdentity,
		makeCached, NewMetricExcerpt, makeIndexData, actions,
		metric.Typename, metric.Namespace,
		formatVersion, defaultMaxLoadedBugs,
	)

	return &RepoCacheMetric{SubCache: sc}
}

// New creates a new metric series. Returns the cached wrapper + the
// create op. Caller typically follows up with c.Record(...) to add
// the first datapoint.
func (c *RepoCacheMetric) New(name string, labels map[string]string, unit, source string) (*MetricCache, *metric.CreateOperation, error) {
	author, err := c.getUserIdentity()
	if err != nil {
		return nil, nil, err
	}
	return c.NewRaw(author, time.Now().Unix(), name, labels, unit, source, nil)
}

func (c *RepoCacheMetric) NewRaw(author identity.Interface, unixTime int64, name string, labels map[string]string, unit, source string, metadata map[string]string) (*MetricCache, *metric.CreateOperation, error) {
	s, op, err := metric.Create(author, unixTime, name, labels, unit, source, metadata)
	if err != nil {
		return nil, nil, err
	}
	if err := s.Commit(c.repo); err != nil {
		return nil, nil, err
	}
	cached, err := c.add(s)
	if err != nil {
		return nil, nil, err
	}
	return cached, op, nil
}

// FindByLabelKey looks up a series by its canonical name{k=v,...}
// representation. Used by ingesters to dedupe: "have I already
// created the series for this (name, labels)?"
func (c *RepoCacheMetric) FindByLabelKey(key string) (*MetricCache, error) {
	excerpt, err := c.ResolveExcerptMatcher(func(e *MetricExcerpt) bool {
		return e.LabelKey == key
	})
	if err != nil {
		return nil, err
	}
	return c.Resolve(excerpt.Id())
}

// GetOrCreate looks up an existing series by label key, creating a
// new one if it doesn't exist. The typical ingester call site —
// pulling CI timings off GitHub and sticking them under a stable
// series name wants this idempotent behaviour.
func (c *RepoCacheMetric) GetOrCreate(name string, labels map[string]string, unit, source string) (*MetricCache, error) {
	key := metric.LabelKey(name, labels)
	existing, err := c.FindByLabelKey(key)
	if err == nil {
		return existing, nil
	}
	// Not-found is the common case we handle; anything else bubbles.
	if !entity.IsErrNotFound(err) {
		return nil, err
	}
	created, _, err := c.New(name, labels, unit, source)
	return created, err
}
