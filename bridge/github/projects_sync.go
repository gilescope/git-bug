package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shurcooL/githubv4"

	"github.com/git-bug/git-bug/bridge/core/auth"
	"github.com/git-bug/git-bug/cache"
)

// ProjectsFileName is the filename git-bug writes project snapshots to,
// placed next to the repo checkout (i.e. in its parent directory — the
// org dir when the layout is <root>/<org>/<repo>/).
//
// The leading dot keeps it out of casual `ls` listings but still lets
// every sibling repo sharing the same owner read the same file — the
// natural scope for GitHub Projects V2, which are owned per user/org
// rather than per repo.
const ProjectsFileName = ".git-bug-projects.json"

// ProjectsSnapshot is the on-disk shape. Wire compatibility matters
// here — the webui reads this format directly — so additions go at the
// end and json tags are stable.
type ProjectsSnapshot struct {
	Owner     string           `json:"owner"`
	FetchedAt time.Time        `json:"fetchedAt"`
	Source    string           `json:"source"` // "github.com"
	Projects  []ProjectRecord  `json:"projects"`
}

type ProjectRecord struct {
	Id          string          `json:"id"`
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	Url         string          `json:"url"`
	Closed      bool            `json:"closed,omitempty"`
	UpdatedAt   time.Time       `json:"updatedAt"`

	Fields []ProjectField `json:"fields,omitempty"`
	Items  []ProjectItem  `json:"items,omitempty"`
}

type ProjectField struct {
	Id      string   `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type"` // e.g. "ProjectV2SingleSelectField"
	Options []string `json:"options,omitempty"`
}

type ProjectItem struct {
	Id        string    `json:"id"`
	Type      string    `json:"type"` // "ISSUE" | "PULL_REQUEST" | "DRAFT_ISSUE" | "REDACTED"
	Archived  bool      `json:"archived,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`

	// Content — one of these is set based on Type.
	ContentUrl   string `json:"contentUrl,omitempty"`
	ContentTitle string `json:"contentTitle,omitempty"`
	ContentState string `json:"contentState,omitempty"`
	ContentRepo  string `json:"contentRepo,omitempty"` // "owner/repo" for issue/PR
	ContentBody  string `json:"contentBody,omitempty"` // draft only

	// Field assignments, keyed by field name. For single-select fields
	// the value is the option name; for text fields, the raw text.
	// Flat map (rather than a slice of structs) keeps UI lookups O(1).
	Values map[string]string `json:"values,omitempty"`
}

// SyncProjects fetches every GitHub Projects V2 owned by the repo's
// configured bridge owner and writes the result to
// <parent-of-repo>/.git-bug-projects.json.
//
// This is scoped to a single repo for lifecycle reasons (same token,
// same config), but the output lives one directory up so it's shared
// across every repo checkout under that owner. Callers that sync
// multiple repos owned by the same owner in one pass are expected to
// dedupe — see projectsSyncCoordinator in import.go.
//
// Returns a non-nil snapshot on success even when the project list is
// empty, so the UI can distinguish "no projects" from "never synced".
// Returns (nil, nil) when the repo has no github bridge — caller
// treats that as a skip.
func SyncProjects(ctx context.Context, repo *cache.RepoCache, repoPath string) (*ProjectsSnapshot, error) {
	cfg, err := readGithubConfig(repo)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}

	creds, err := auth.List(repo,
		auth.WithTarget(target),
		auth.WithKind(auth.KindToken),
		auth.WithMeta(auth.MetaKeyLogin, cfg.login),
	)
	if err != nil {
		return nil, fmt.Errorf("find github token: %w", err)
	}
	if len(creds) == 0 {
		return nil, ErrMissingIdentityToken
	}

	client := buildClient(creds[0].(*auth.Token))

	snap, err := fetchProjectsForOwner(ctx, client, cfg.owner, cfg.project)
	if err != nil {
		return nil, err
	}

	if err := writeProjectsSnapshot(repoPath, snap); err != nil {
		return nil, fmt.Errorf("write projects file: %w", err)
	}
	return snap, nil
}

// fetchProjectsForOwner walks the owner's projectsV2 connection, then
// per-project tops up any items that overflowed the initial page. The
// second pass uses node(id:) so we don't repeat the expensive
// fields/items join on already-fetched projects.
func fetchProjectsForOwner(ctx context.Context, client *rateLimitHandlerClient, owner, sourceProject string) (*ProjectsSnapshot, error) {
	snap := &ProjectsSnapshot{
		Owner:     owner,
		FetchedAt: time.Now(),
		Source:    "github.com",
	}

	var projectCursor githubv4.String
	for {
		var q projectsPage
		vars := map[string]interface{}{
			"owner":         githubv4.String(owner),
			"projectCursor": githubv4.NewString(projectCursor),
		}
		if err := client.sc.Query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("github projectsV2 query: %w", err)
		}
		noteRateLimit("projectsV2", owner, sourceProject, q.RateLimit)

		// RepositoryOwner is an interface; only one of the two inline
		// fragments is populated for any given query. Pick whichever has
		// nodes — if both are empty the owner just has no projects.
		conn := q.RepositoryOwner.User.ProjectsV2
		if len(conn.Nodes) == 0 && len(q.RepositoryOwner.Organization.ProjectsV2.Nodes) > 0 {
			conn = q.RepositoryOwner.Organization.ProjectsV2
		}

		for _, p := range conn.Nodes {
			rec, err := buildProjectRecord(ctx, client, owner, sourceProject, &p)
			if err != nil {
				return nil, err
			}
			snap.Projects = append(snap.Projects, rec)
		}

		if !bool(conn.PageInfo.HasNextPage) {
			break
		}
		projectCursor = conn.PageInfo.EndCursor
	}
	return snap, nil
}

// buildProjectRecord converts one lightweight projectV2 header into
// the on-disk ProjectRecord, then fetches fields and items in
// separate paginated follow-up queries. Splitting the work avoids
// the "stream error: CANCEL received from peer" failure that GitHub
// triggers on oversized responses when everything is fetched in one
// query.
func buildProjectRecord(ctx context.Context, client *rateLimitHandlerClient, owner, sourceProject string, p *projectV2) (ProjectRecord, error) {
	rec := ProjectRecord{
		Id:          string(p.Id),
		Number:      int(p.Number),
		Title:       string(p.Title),
		Description: string(p.ShortDescription),
		Url:         string(p.Url),
		Closed:      bool(p.Closed),
		UpdatedAt:   p.UpdatedAt.Time,
	}

	// node(id:) expects GraphQL ID! — pass via githubv4.ID so the
	// generated query's variable declaration is $projectId: ID! not
	// $projectId: String!. Stringifying `p.Id` first because it comes
	// back from GitHub typed as githubv4.String (every id scalar does).
	projectId := githubv4.ID(string(p.Id))

	fields, err := fetchProjectFields(ctx, client, owner, sourceProject, projectId)
	if err != nil {
		return rec, err
	}
	rec.Fields = fields

	items, err := fetchProjectItems(ctx, client, owner, sourceProject, projectId)
	if err != nil {
		return rec, err
	}
	rec.Items = items
	return rec, nil
}

// fetchProjectFields walks `node(id:).fields` with pagination. Almost
// every project has <20 fields, but we still paginate so unusual
// projects don't silently truncate.
func fetchProjectFields(ctx context.Context, client *rateLimitHandlerClient, owner, sourceProject string, projectId githubv4.ID) ([]ProjectField, error) {
	var out []ProjectField
	var cursor githubv4.String
	for {
		var q projectFieldsPage
		vars := map[string]interface{}{
			"projectId":   projectId,
			"fieldCursor": githubv4.NewString(cursor),
		}
		if err := client.sc.Query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("github projectsV2 fields page: %w", err)
		}
		noteRateLimit("projectsV2Fields", owner, sourceProject, q.RateLimit)
		for _, f := range q.Node.ProjectV2.Fields.Nodes {
			out = append(out, toProjectField(f))
		}
		if !bool(q.Node.ProjectV2.Fields.PageInfo.HasNextPage) {
			break
		}
		cursor = q.Node.ProjectV2.Fields.PageInfo.EndCursor
	}
	return out, nil
}

// fetchProjectItems walks `node(id:).items` with pagination.
func fetchProjectItems(ctx context.Context, client *rateLimitHandlerClient, owner, sourceProject string, projectId githubv4.ID) ([]ProjectItem, error) {
	var out []ProjectItem
	var cursor githubv4.String
	for {
		var q projectItemsPage
		vars := map[string]interface{}{
			"projectId":  projectId,
			"itemCursor": githubv4.NewString(cursor),
		}
		if err := client.sc.Query(ctx, &q, vars); err != nil {
			return nil, fmt.Errorf("github projectsV2 items page: %w", err)
		}
		noteRateLimit("projectsV2Items", owner, sourceProject, q.RateLimit)
		for _, it := range q.Node.ProjectV2.Items.Nodes {
			out = append(out, toProjectItem(it))
		}
		if !bool(q.Node.ProjectV2.Items.PageInfo.HasNextPage) {
			break
		}
		cursor = q.Node.ProjectV2.Items.PageInfo.EndCursor
	}
	return out, nil
}

func toProjectField(f projectV2Field) ProjectField {
	// id+name come from the ProjectV2FieldCommon interface fragment —
	// it's implemented by every concrete field type, so no per-type
	// branching needed here. We only branch to pull options out of the
	// single-select variant.
	out := ProjectField{
		Type: string(f.Typename),
		Id:   string(f.Common.Id),
		Name: string(f.Common.Name),
	}
	if string(f.Typename) == "ProjectV2SingleSelectField" {
		for _, o := range f.SingleSelectField.Options {
			out.Options = append(out.Options, string(o.Name))
		}
	}
	return out
}

func toProjectItem(it projectV2Item) ProjectItem {
	out := ProjectItem{
		Id:        string(it.Id),
		Type:      string(it.Type),
		Archived:  bool(it.IsArchived),
		UpdatedAt: it.UpdatedAt.Time,
	}

	switch string(it.Content.Typename) {
	case "Issue":
		c := it.Content.Issue
		out.ContentUrl = string(c.Url)
		out.ContentTitle = string(c.Title)
		out.ContentState = string(c.State)
		out.ContentRepo = string(c.Repository.NameWithOwner)
	case "PullRequest":
		c := it.Content.PullRequest
		out.ContentUrl = string(c.Url)
		out.ContentTitle = string(c.Title)
		out.ContentState = string(c.State)
		out.ContentRepo = string(c.Repository.NameWithOwner)
	case "DraftIssue":
		c := it.Content.DraftIssue
		out.ContentTitle = string(c.Title)
		out.ContentBody = string(c.Body)
	}

	for _, v := range it.FieldValues.Nodes {
		switch string(v.Typename) {
		case "ProjectV2ItemFieldSingleSelectValue":
			name := string(v.SingleSelectValue.Field.SingleSelect.Name)
			if name == "" {
				continue
			}
			if out.Values == nil {
				out.Values = map[string]string{}
			}
			out.Values[name] = string(v.SingleSelectValue.Name)
		case "ProjectV2ItemFieldTextValue":
			name := string(v.TextValue.Field.Common.Name)
			if name == "" {
				continue
			}
			if out.Values == nil {
				out.Values = map[string]string{}
			}
			out.Values[name] = string(v.TextValue.Text)
		}
	}
	return out
}

// writeProjectsSnapshot writes atomically (temp file + rename) so
// reads by the webui never see a partial file. Placed in the parent
// directory of the repo — so two sibling repos under the same owner
// share one file, matching GitHub's owner-scoped project model.
//
// repoPath is the filesystem path to the repo checkout; we resolve
// `filepath.Dir(repoPath)` as the target directory.
func writeProjectsSnapshot(repoPath string, snap *ProjectsSnapshot) error {
	dir := filepath.Dir(repoPath)
	if dir == "" || dir == "." {
		return fmt.Errorf("cannot resolve parent dir for %q", repoPath)
	}
	target := filepath.Join(dir, ProjectsFileName)

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".git-bug-projects-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, target)
}

// ReadProjectsSnapshot reads the snapshot written by SyncProjects.
// Used by the HTTP handler to serve /projects/<repo>. Returns nil,nil
// when the file doesn't exist — the caller renders "never synced"
// instead of a 500.
func ReadProjectsSnapshot(repoPath string) (*ProjectsSnapshot, error) {
	dir := filepath.Dir(repoPath)
	target := filepath.Join(dir, ProjectsFileName)
	data, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var snap ProjectsSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("parse %s: %w", target, err)
	}
	return &snap, nil
}
