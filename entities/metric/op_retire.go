package metric

import (
	"github.com/git-bug/git-bug/entities/identity"
	"github.com/git-bug/git-bug/entity"
	"github.com/git-bug/git-bug/entity/dag"
)

var _ Operation = &RetireOperation{}

// RetireOperation tombstones a series. The data stays readable — the
// UI just hides retired series by default. Useful for stale test
// names that got renamed or CI jobs that are gone.
type RetireOperation struct {
	dag.OpBase
}

func (op *RetireOperation) Id() entity.Id {
	return dag.IdOperation(op, &op.OpBase)
}

func (op *RetireOperation) Validate() error {
	return op.OpBase.Validate(op, RetireOp)
}

func (op *RetireOperation) Apply(snap *Snapshot) {
	snap.Retired = true
	snap.addActor(op.Author())
	snap.addParticipant(op.Author())
}

func NewRetireOp(author identity.Interface, unixTime int64) *RetireOperation {
	return &RetireOperation{OpBase: dag.NewOpBase(RetireOp, author, unixTime)}
}

// Retire marks the series inactive.
func Retire(s ReadWrite, author identity.Interface, unixTime int64, metadata map[string]string) (*RetireOperation, error) {
	op := NewRetireOp(author, unixTime)
	for k, v := range metadata {
		op.SetMetadata(k, v)
	}
	if err := op.Validate(); err != nil {
		return nil, err
	}
	s.Append(op)
	return op, nil
}
