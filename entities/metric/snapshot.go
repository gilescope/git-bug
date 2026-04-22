package metric

import (
	"sort"
	"time"

	"github.com/git-bug/git-bug/entities/identity"
	"github.com/git-bug/git-bug/entity"
	"github.com/git-bug/git-bug/entity/dag"
)

var _ dag.Snapshot = &Snapshot{}

// Snapshot is the compiled view of a metric series. Points are the
// ground-truth append-only log; any rollup (daily p50, rolling pass
// rate, …) is derived in the cache layer and NOT persisted here —
// the rule is "raw points in the DAG, aggregates in the cache".
//
// Labels are the dimension of the series (e.g. {os: linux, arch: amd64,
// test: "my_crate::tests::it_works"}). They are frozen by the Create
// op — if you want different labels you create a new series.
type Snapshot struct {
	id entity.Id

	// Header fields, all set by the single CreateOperation.
	Name   string
	Labels map[string]string
	Unit   string
	Source string

	// Points is the raw time-series data, one append per RecordOperation.
	// Kept in insertion order; callers that need time order should
	// sort by Time themselves — clock skew across machines means
	// ops can arrive out of wall-clock order.
	Points []Point

	// Retired is true if a RetireOperation has been applied. The UI
	// hides retired series by default; the data is still readable.
	Retired bool

	CreateTime time.Time

	Author       identity.Interface
	Actors       []identity.Interface
	Participants []identity.Interface

	Operations []dag.Operation
}

// Point is a single time-series datapoint. Attrs is for per-sample
// context that isn't part of the series identity — e.g. the CI run
// URL, the host name that reported it, the commit SHA. These don't
// create new series but are useful when drilling down.
type Point struct {
	Time  time.Time         `json:"time"`
	Value float64           `json:"value"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

func (snap *Snapshot) Id() entity.Id {
	if snap.id == "" {
		panic("no id")
	}
	return snap.id
}

func (snap *Snapshot) AllOperations() []dag.Operation { return snap.Operations }

func (snap *Snapshot) AppendOperation(op dag.Operation) {
	snap.Operations = append(snap.Operations, op)
}

// EditTime is the timestamp of the latest operation, used by caches
// that care about recency (e.g. "sort by last updated").
func (snap *Snapshot) EditTime() time.Time {
	if len(snap.Operations) == 0 {
		return time.Unix(0, 0)
	}
	return snap.Operations[len(snap.Operations)-1].Time()
}

// LabelKey is a stable deterministic rendering of (name, sorted labels)
// used by the UI + cache to group series that share identity. Two
// series with the same LabelKey are "the same series" even if they
// live in different entities (a point we'll exercise once monthly
// bucketing lands — two buckets, same LabelKey, graph as one line).
func (snap *Snapshot) LabelKey() string {
	return LabelKey(snap.Name, snap.Labels)
}

// LabelKey formats (name, labels) as `name{k1=v1,k2=v2,...}` with
// keys sorted. Matches the Prometheus convention so if we ever export
// to Prom it's a trivial serializer.
func LabelKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := name + "{"
	for i, k := range keys {
		if i > 0 {
			out += ","
		}
		out += k + "=" + labels[k]
	}
	out += "}"
	return out
}

func (snap *Snapshot) addActor(actor identity.Interface) {
	for _, a := range snap.Actors {
		if actor.Id() == a.Id() {
			return
		}
	}
	snap.Actors = append(snap.Actors, actor)
}

func (snap *Snapshot) addParticipant(p identity.Interface) {
	for _, a := range snap.Participants {
		if p.Id() == a.Id() {
			return
		}
	}
	snap.Participants = append(snap.Participants, p)
}
