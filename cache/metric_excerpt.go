package cache

import (
	"encoding/gob"
	"sort"
	"time"

	"github.com/git-bug/git-bug/entity"
	"github.com/git-bug/git-bug/util/lamport"
)

func init() {
	gob.Register(MetricExcerpt{})
}

var _ Excerpt = &MetricExcerpt{}

// MetricExcerpt is the light-weight summary of a metric series kept
// in the excerpt cache. It lets the UI / CLI list and filter series
// without loading every datapoint off disk.
//
// We deliberately don't pre-aggregate values here — the full point
// list is cheap to load on demand, and any aggregate (p50 per day,
// rolling pass rate) depends on a time-range the caller hasn't
// declared yet. Aggregates live in the UI tier, above this cache.
type MetricExcerpt struct {
	id entity.Id

	CreateLamportTime lamport.Time
	EditLamportTime   lamport.Time
	CreateUnixTime    int64
	EditUnixTime      int64

	Name   string
	Unit   string
	Source string
	// Labels stored as a sorted []string of "k=v" pairs so gob
	// serialization is stable — maps don't serialize in a fixed order.
	Labels  []string
	LabelKey string

	PointCount int
	// LastTimeUnix is the wall-clock of the most recent point, so
	// "show me stale series" queries don't have to load points.
	LastTimeUnix int64

	Retired bool

	Actors []entity.Id

	CreateMetadata map[string]string
}

func NewMetricExcerpt(c *MetricCache) *MetricExcerpt {
	snap := c.Snapshot()

	actors := make([]entity.Id, 0, len(snap.Actors))
	for _, a := range snap.Actors {
		actors = append(actors, a.Id())
	}

	labels := make([]string, 0, len(snap.Labels))
	for k, v := range snap.Labels {
		labels = append(labels, k+"="+v)
	}
	sort.Strings(labels)

	var last int64
	if len(snap.Points) > 0 {
		// Points arrive in insertion order, not wall-clock order — scan
		// to find the true most-recent event-time.
		last = snap.Points[0].Time.Unix()
		for _, p := range snap.Points {
			if t := p.Time.Unix(); t > last {
				last = t
			}
		}
	}

	return &MetricExcerpt{
		id:                c.Id(),
		CreateLamportTime: c.CreateLamportTime(),
		EditLamportTime:   c.EditLamportTime(),
		CreateUnixTime:    c.FirstOp().Time().Unix(),
		EditUnixTime:      snap.EditTime().Unix(),
		Name:              snap.Name,
		Unit:              snap.Unit,
		Source:            snap.Source,
		Labels:            labels,
		LabelKey:          snap.LabelKey(),
		PointCount:        len(snap.Points),
		LastTimeUnix:      last,
		Retired:           snap.Retired,
		Actors:            actors,
		CreateMetadata:    c.FirstOp().AllMetadata(),
	}
}

func (m *MetricExcerpt) Id() entity.Id       { return m.id }
func (m *MetricExcerpt) setId(id entity.Id)  { m.id = id }
func (m *MetricExcerpt) CreateTime() time.Time { return time.Unix(m.CreateUnixTime, 0) }
func (m *MetricExcerpt) EditTime() time.Time   { return time.Unix(m.EditUnixTime, 0) }
