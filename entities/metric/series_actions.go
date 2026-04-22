package metric

import (
	"github.com/git-bug/git-bug/entities/identity"
	"github.com/git-bug/git-bug/entity"
	"github.com/git-bug/git-bug/entity/dag"
	"github.com/git-bug/git-bug/repository"
)

// These are the series-level actions the cache layer needs, mirroring
// bug_actions.go / board_actions.go. Thin wrappers so callers don't
// have to know about the dag.Definition sausage factory.

func Fetch(repo repository.Repo, remote string) (string, error) {
	return dag.Fetch(def, repo, remote)
}

func Push(repo repository.Repo, remote string) (string, error) {
	return dag.Push(def, repo, remote)
}

func Pull(repo repository.ClockedRepo, resolvers entity.Resolvers, remote string, mergeAuthor identity.Interface) error {
	return dag.Pull(def, wrapper, repo, resolvers, remote, mergeAuthor)
}

func MergeAll(repo repository.ClockedRepo, resolvers entity.Resolvers, remote string, mergeAuthor identity.Interface) <-chan entity.MergeResult {
	return dag.MergeAll(def, wrapper, repo, resolvers, remote, mergeAuthor)
}

func Remove(repo repository.ClockedRepo, id entity.Id) error {
	return dag.Remove(def, repo, id)
}

func RemoveAll(repo repository.ClockedRepo) error {
	return dag.RemoveAll(def, repo)
}

// ListLocalIds returns the ids of every metric series known locally.
// Used by the cache builder to enumerate what's on disk.
func ListLocalIds(repo repository.Repo) ([]entity.Id, error) {
	return dag.ListLocalIds(def, repo)
}
