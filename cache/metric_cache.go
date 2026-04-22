package cache

import (
	"time"

	"github.com/git-bug/git-bug/entities/identity"
	"github.com/git-bug/git-bug/entities/metric"
	"github.com/git-bug/git-bug/entity"
	"github.com/git-bug/git-bug/repository"
)

// MetricCache wraps a metric.Series with caching + locking + change
// notifications, mirroring BugCache / BoardCache.
type MetricCache struct {
	CachedEntityBase[*metric.Snapshot, metric.Operation]
}

func NewMetricCache(s *metric.Series, repo repository.ClockedRepo, getUserIdentity getUserIdentityFunc, entityUpdated func(id entity.Id) error) *MetricCache {
	return &MetricCache{
		CachedEntityBase: CachedEntityBase[*metric.Snapshot, metric.Operation]{
			repo:            repo,
			entityUpdated:   entityUpdated,
			getUserIdentity: getUserIdentity,
			entity:          newWithSnapshot[*metric.Snapshot, metric.Operation](s),
		},
	}
}

// Record appends a datapoint. eventTime is when the measurement was
// taken (not when we recorded it) — this is the value the UI graphs
// against, since for CI imports the "recorded at" time is just when
// sync happened to run.
func (c *MetricCache) Record(eventTime time.Time, value float64, attrs map[string]string) (*metric.RecordOperation, error) {
	author, err := c.getUserIdentity()
	if err != nil {
		return nil, err
	}
	return c.RecordRaw(author, time.Now().Unix(), eventTime.Unix(), value, attrs, nil)
}

func (c *MetricCache) RecordRaw(author identity.Interface, unixTime, eventTime int64, value float64, attrs map[string]string, metadata map[string]string) (*metric.RecordOperation, error) {
	c.mu.Lock()
	op, err := metric.Record(c.entity, author, unixTime, eventTime, value, attrs, metadata)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	// Auto-commit per point: metric series are write-once-per-point by
	// nature (no one batches a few samples then flushes), and forgetting
	// to commit leaves a staged op that silently vanishes when the
	// cache re-resolves the series on a later read — the "one point in
	// the excerpt, zero points from the detail endpoint" surprise.
	// CommitAsNeeded so a no-op second call is harmless.
	if err := c.entity.CommitAsNeeded(c.repo); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()
	return op, c.notifyUpdated()
}

// Retire marks the series inactive. Data stays readable.
func (c *MetricCache) Retire() (*metric.RetireOperation, error) {
	author, err := c.getUserIdentity()
	if err != nil {
		return nil, err
	}
	return c.RetireRaw(author, time.Now().Unix(), nil)
}

func (c *MetricCache) RetireRaw(author identity.Interface, unixTime int64, metadata map[string]string) (*metric.RetireOperation, error) {
	c.mu.Lock()
	op, err := metric.Retire(c.entity, author, unixTime, metadata)
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if err := c.entity.CommitAsNeeded(c.repo); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	c.mu.Unlock()
	return op, c.notifyUpdated()
}
