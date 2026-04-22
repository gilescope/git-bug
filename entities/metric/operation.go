package metric

import (
	"encoding/json"
	"fmt"

	"github.com/git-bug/git-bug/entity"
	"github.com/git-bug/git-bug/entity/dag"
)

// OperationType numbers are persisted in git; reorder and you break
// every repo that already recorded a metric. Appending new variants
// is fine; renumbering existing ones is not.
type OperationType dag.OperationType

const (
	_ dag.OperationType = iota
	CreateOp
	RecordOp
	SetMetadataOp
	RetireOp
)

// Operation is the dag.Operation subtype for metric series. Snapshot
// is the thing the dag framework compiles all ops into.
type Operation interface {
	dag.Operation
	Apply(snapshot *Snapshot)
}

// operationUnmarshaler is called by the dag framework when loading
// ops from git. Every OperationType listed in the const block above
// needs a branch here or loading panics.
func operationUnmarshaler(raw json.RawMessage, _ entity.Resolvers) (dag.Operation, error) {
	var t struct {
		OperationType dag.OperationType `json:"type"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}

	var op dag.Operation
	switch t.OperationType {
	case CreateOp:
		op = &CreateOperation{}
	case RecordOp:
		op = &RecordOperation{}
	case SetMetadataOp:
		op = &dag.SetMetadataOperation[*Snapshot]{}
	case RetireOp:
		op = &RetireOperation{}
	default:
		return nil, fmt.Errorf("unknown metric op type %v", t.OperationType)
	}

	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, err
	}
	return op, nil
}
