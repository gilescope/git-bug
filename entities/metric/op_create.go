package metric

import (
	"fmt"
	"regexp"
	"time"

	"github.com/git-bug/git-bug/entities/identity"
	"github.com/git-bug/git-bug/entity"
	"github.com/git-bug/git-bug/entity/dag"
)

var _ Operation = &CreateOperation{}

// CreateOperation seals the identity of a series: name, labels, unit,
// source. These are all immutable — anything mutable (e.g. pausing a
// series) goes through a separate op type.
type CreateOperation struct {
	dag.OpBase
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	// Unit is free-form but conventional: "s" (seconds), "ms",
	// "count", "bytes", "percent", "ratio". The UI uses it as an
	// axis suffix; no enforcement here.
	Unit string `json:"unit,omitempty"`
	// Source is a hint about who produced the metric — e.g.
	// "github.com/actions", "junit", "go-test", "user". The UI
	// surfaces this so readers can tell test data from real data
	// at a glance.
	Source string `json:"source,omitempty"`
}

// Common series-name sanity: dotted path, alphanumeric + `_-.`. Blocks
// accidental newline / control chars in git reflogs.
var nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.\-]*$`)

func (op *CreateOperation) Id() entity.Id {
	return dag.IdOperation(op, &op.OpBase)
}

func (op *CreateOperation) Validate() error {
	if err := op.OpBase.Validate(op, CreateOp); err != nil {
		return err
	}
	if op.Name == "" {
		return fmt.Errorf("metric name is empty")
	}
	if !nameRe.MatchString(op.Name) {
		return fmt.Errorf("metric name %q: only [A-Za-z0-9_.-] allowed, must start with a letter", op.Name)
	}
	if len(op.Name) > 256 {
		return fmt.Errorf("metric name too long (>256)")
	}
	for k, v := range op.Labels {
		if !nameRe.MatchString(k) {
			return fmt.Errorf("label key %q: only [A-Za-z0-9_.-] allowed, must start with a letter", k)
		}
		if len(v) > 512 {
			return fmt.Errorf("label %q value too long (>512)", k)
		}
	}
	if len(op.Labels) > 32 {
		return fmt.Errorf("too many labels (>32)")
	}
	if len(op.Unit) > 32 {
		return fmt.Errorf("unit too long (>32)")
	}
	if len(op.Source) > 64 {
		return fmt.Errorf("source too long (>64)")
	}
	return nil
}

func (op *CreateOperation) Apply(snap *Snapshot) {
	if snap.id != "" && snap.id != entity.UnsetId && snap.id != op.Id() {
		return
	}
	snap.id = op.Id()
	snap.Name = op.Name
	snap.Labels = copyStringMap(op.Labels)
	snap.Unit = op.Unit
	snap.Source = op.Source
	snap.Author = op.Author()
	snap.CreateTime = op.Time()
	snap.addActor(op.Author())
	snap.addParticipant(op.Author())
}

func NewCreateOp(author identity.Interface, unixTime int64, name string, labels map[string]string, unit, source string) *CreateOperation {
	return &CreateOperation{
		OpBase: dag.NewOpBase(CreateOp, author, unixTime),
		Name:   name,
		Labels: copyStringMap(labels),
		Unit:   unit,
		Source: source,
	}
}

// Create is a convenience wrapper that builds the entity, appends the
// create op, and returns both. Same shape as bug.Create / board.Create
// so callers can use a consistent idiom.
func Create(author identity.Interface, unixTime int64, name string, labels map[string]string, unit, source string, metadata map[string]string) (*Series, *CreateOperation, error) {
	s := NewSeries()
	op := NewCreateOp(author, unixTime, name, labels, unit, source)
	for k, v := range metadata {
		op.SetMetadata(k, v)
	}
	if err := op.Validate(); err != nil {
		return nil, op, err
	}
	s.Append(op)
	return s, op, nil
}

// copyStringMap returns a defensive copy; ops must never share state
// with callers since the snapshot compiler re-applies them.
func copyStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Ensure time.Time is actually used (imported so the op file stays
// self-contained for future time-related validation).
var _ = time.Time{}
