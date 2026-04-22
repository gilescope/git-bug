package metric

import (
	"fmt"
	"math"
	"time"

	"github.com/git-bug/git-bug/entities/identity"
	"github.com/git-bug/git-bug/entity"
	"github.com/git-bug/git-bug/entity/dag"
)

var _ Operation = &RecordOperation{}

// RecordOperation appends a single datapoint. Multiple record ops on
// the same series accumulate — the snapshot's Points list is the
// ordered log of everything ever recorded.
//
// EventTime is stored separately from OpBase.Time() (which is when
// the record was authored) because for CI-style data the "measured
// at" time and the "recorded at" time can differ by hours — the CI
// run finished at 14:05, but git-bug records the point at 14:07
// during sync. EventTime lets the UI graph by the former.
type RecordOperation struct {
	dag.OpBase
	EventTime int64             `json:"event_time"` // unix seconds
	Value     float64           `json:"value"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

func (op *RecordOperation) Id() entity.Id {
	return dag.IdOperation(op, &op.OpBase)
}

func (op *RecordOperation) Validate() error {
	if err := op.OpBase.Validate(op, RecordOp); err != nil {
		return err
	}
	if op.EventTime <= 0 {
		return fmt.Errorf("record: event_time must be > 0")
	}
	if math.IsNaN(op.Value) || math.IsInf(op.Value, 0) {
		// NaN/Inf break JSON encoding in the HTTP layer and break
		// any sensible chart renderer — reject at the source.
		return fmt.Errorf("record: value must be a finite number")
	}
	for k, v := range op.Attrs {
		if !nameRe.MatchString(k) {
			return fmt.Errorf("attr key %q: only [A-Za-z0-9_.-] allowed, must start with a letter", k)
		}
		if len(v) > 512 {
			return fmt.Errorf("attr %q value too long (>512)", k)
		}
	}
	if len(op.Attrs) > 16 {
		return fmt.Errorf("too many attrs (>16)")
	}
	return nil
}

func (op *RecordOperation) Apply(snap *Snapshot) {
	snap.addActor(op.Author())
	snap.addParticipant(op.Author())
	snap.Points = append(snap.Points, Point{
		Time:  time.Unix(op.EventTime, 0).UTC(),
		Value: op.Value,
		Attrs: copyStringMap(op.Attrs),
	})
}

func NewRecordOp(author identity.Interface, unixTime int64, eventTime int64, value float64, attrs map[string]string) *RecordOperation {
	return &RecordOperation{
		OpBase:    dag.NewOpBase(RecordOp, author, unixTime),
		EventTime: eventTime,
		Value:     value,
		Attrs:     copyStringMap(attrs),
	}
}

// Record is a convenience function: validate + append in one call.
// Returns the new op so callers can inspect its generated id.
func Record(s ReadWrite, author identity.Interface, unixTime int64, eventTime int64, value float64, attrs map[string]string, metadata map[string]string) (*RecordOperation, error) {
	op := NewRecordOp(author, unixTime, eventTime, value, attrs)
	for k, v := range metadata {
		op.SetMetadata(k, v)
	}
	if err := op.Validate(); err != nil {
		return nil, err
	}
	s.Append(op)
	return op, nil
}
