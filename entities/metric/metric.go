package metric

import (
	"fmt"

	"github.com/git-bug/git-bug/entities/identity"
	"github.com/git-bug/git-bug/entity"
	"github.com/git-bug/git-bug/entity/dag"
	"github.com/git-bug/git-bug/repository"
)

var _ ReadOnly = &Series{}
var _ ReadWrite = &Series{}
var _ entity.Interface = &Series{}

// formatVersion bumps when the on-disk operation schema changes
// incompatibly. 1 = original (Create/Record/Retire/SetMetadata).
const formatVersion = 1

const Typename = "metric"
const Namespace = "metrics"

var def = dag.Definition{
	Typename:             Typename,
	Namespace:            Namespace,
	OperationUnmarshaler: operationUnmarshaler,
	FormatVersion:        formatVersion,
}

var ClockLoader = dag.ClockLoader(def)

type ReadOnly dag.ReadOnly[*Snapshot, Operation]
type ReadWrite dag.ReadWrite[*Snapshot, Operation]

// Series is a single time-series metric. The series identity (name,
// labels, unit, source) is sealed by the CreateOperation; all
// subsequent points are RecordOperations appended to the same entity.
type Series struct {
	*dag.Entity
}

func NewSeries() *Series { return &Series{Entity: dag.New(def)} }

func wrapper(e *dag.Entity) *Series { return &Series{Entity: e} }

func simpleResolvers(repo repository.ClockedRepo) entity.Resolvers {
	return entity.Resolvers{
		&identity.Identity{}: identity.NewSimpleResolver(repo),
	}
}

func Read(repo repository.ClockedRepo, id entity.Id) (*Series, error) {
	return ReadWithResolver(repo, simpleResolvers(repo), id)
}

func ReadWithResolver(repo repository.ClockedRepo, resolvers entity.Resolvers, id entity.Id) (*Series, error) {
	return dag.Read(def, wrapper, repo, resolvers, id)
}

func ReadAll(repo repository.ClockedRepo) <-chan entity.StreamedEntity[*Series] {
	return dag.ReadAll(def, wrapper, repo, simpleResolvers(repo))
}

func ReadAllWithResolver(repo repository.ClockedRepo, resolvers entity.Resolvers) <-chan entity.StreamedEntity[*Series] {
	return dag.ReadAll(def, wrapper, repo, resolvers)
}

// Validate mirrors the board/bug convention: first op must be a
// Create, no subsequent Create ops are allowed.
func (s *Series) Validate() error {
	if err := s.Entity.Validate(); err != nil {
		return err
	}
	first := s.FirstOp()
	if first == nil || first.Type() != CreateOp {
		return fmt.Errorf("first operation should be Create")
	}
	for i, op := range s.Entity.Operations() {
		if i == 0 {
			continue
		}
		if op.Type() == CreateOp {
			return fmt.Errorf("only one Create op allowed")
		}
	}
	return nil
}

func (s *Series) Append(op Operation) { s.Entity.Append(op) }

func (s *Series) Operations() []Operation {
	src := s.Entity.Operations()
	out := make([]Operation, len(src))
	for i, op := range src {
		out[i] = op.(Operation)
	}
	return out
}

func (s *Series) Snapshot() *Snapshot {
	snap := &Snapshot{id: s.Id()}
	for _, op := range s.Operations() {
		op.Apply(snap)
		snap.Operations = append(snap.Operations, op)
	}
	return snap
}

func (s *Series) FirstOp() Operation {
	if fo := s.Entity.FirstOp(); fo != nil {
		return fo.(Operation)
	}
	return nil
}

func (s *Series) LastOp() Operation {
	if lo := s.Entity.LastOp(); lo != nil {
		return lo.(Operation)
	}
	return nil
}
