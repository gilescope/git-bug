package github

import "github.com/shurcooL/githubv4"

// projectsPage fetches one page of projects for a given owner (user or
// organization), headers-only — id, title, description, url, timestamps.
// Fields and items are fetched per-project in follow-up queries
// (projectFieldsPage, projectItemsPage) to keep any single request far
// below GitHub's query-cost and response-size limits. Combining all
// three into one request works for toy projects but GitHub's server
// silently resets the HTTP/2 stream ("stream error: CANCEL received
// from peer") once the response gets large enough.
type projectsPage struct {
	RepositoryOwner struct {
		Typename githubv4.String `graphql:"__typename"`
		User     struct {
			ProjectsV2 projectV2Connection `graphql:"projectsV2(first: 20, after: $projectCursor)"`
		} `graphql:"... on User"`
		Organization struct {
			ProjectsV2 projectV2Connection `graphql:"projectsV2(first: 20, after: $projectCursor)"`
		} `graphql:"... on Organization"`
	} `graphql:"repositoryOwner(login: $owner)"`
	RateLimit rateLimit
}

type projectV2Connection struct {
	Nodes    []projectV2
	PageInfo pageInfo
}

// projectV2 is the lightweight header row of a project. Fields and
// items are intentionally omitted — those come from projectFieldsPage
// and projectItemsPage. Keeping this tight lets us scan the owner's
// project inventory in a single cheap query.
type projectV2 struct {
	Id               githubv4.String
	Number           githubv4.Int
	Title            githubv4.String
	ShortDescription githubv4.String
	Closed           githubv4.Boolean
	Url              githubv4.String
	UpdatedAt        githubv4.DateTime
}

// projectFieldsPage returns fields (and single-select options) for one
// project. Limited to 20 fields per page — GitHub Projects V2 rarely
// defines more than a handful of custom fields, but we still paginate
// so outliers don't silently truncate.
type projectFieldsPage struct {
	Node struct {
		Typename  githubv4.String `graphql:"__typename"`
		ProjectV2 struct {
			Fields projectV2FieldConnection `graphql:"fields(first: 20, after: $fieldCursor)"`
		} `graphql:"... on ProjectV2"`
	} `graphql:"node(id: $projectId)"`
	RateLimit rateLimit
}

// projectV2FieldConnection lists the fields defined on a project. We
// care mostly about the Status single-select (to derive columns), but
// we keep any user-defined single-select so a richer UI can render
// colour badges later. Other field types (number, date, iteration, …)
// are captured as "common" entries — their values still surface as
// text in fieldValues.
type projectV2FieldConnection struct {
	Nodes    []projectV2Field
	PageInfo pageInfo
}

type projectV2Field struct {
	Typename githubv4.String `graphql:"__typename"`
	// Common is an inline fragment on the ProjectV2FieldCommon interface:
	// every union member (ProjectV2Field, ProjectV2IterationField,
	// ProjectV2SingleSelectField) implements it, so id+name come back
	// for all of them without per-type branching. GraphQL forbids direct
	// selections on a union, so *every* sub-selection here must be an
	// inline fragment — no bare field lists.
	Common struct {
		Id   githubv4.String
		Name githubv4.String
	} `graphql:"... on ProjectV2FieldCommon"`
	SingleSelectField projectV2SingleSelectFieldFields `graphql:"... on ProjectV2SingleSelectField"`
}

// projectV2SingleSelectFieldFields holds the single-select-only bits
// (options) that ProjectV2FieldCommon doesn't expose. Id/Name come
// via the Common fragment above, so we intentionally don't re-select
// them here.
type projectV2SingleSelectFieldFields struct {
	Options []projectV2FieldOption
}

type projectV2FieldOption struct {
	Id   githubv4.String
	Name githubv4.String
}

type projectV2ItemConnection struct {
	Nodes    []projectV2Item
	PageInfo pageInfo
}

// projectV2Item is a tuple of (type, content, fieldValues). `type` is
// REDACTED/ISSUE/PULL_REQUEST/DRAFT_ISSUE — we rely on it to decide
// which content fragment actually has data.
type projectV2Item struct {
	Id        githubv4.String
	Type      githubv4.String `graphql:"type"`
	UpdatedAt githubv4.DateTime
	IsArchived githubv4.Boolean

	Content projectV2ItemContent
	// 8 fieldValues per item is enough to capture Status + a handful of
	// custom fields on common project layouts. Bumping this is the main
	// knob to turn if the UI needs to surface more field assignments;
	// cost grows linearly with the product of items × fieldValues.
	FieldValues projectV2FieldValueConnection `graphql:"fieldValues(first: 8)"`
}

type projectV2ItemContent struct {
	Typename    githubv4.String `graphql:"__typename"`
	Issue       projectV2IssueContent       `graphql:"... on Issue"`
	PullRequest projectV2PullRequestContent `graphql:"... on PullRequest"`
	DraftIssue  projectV2DraftContent       `graphql:"... on DraftIssue"`
}

// projectV2IssueContent / projectV2PullRequestContent share the same
// shape — GitHub's GraphQL schema doesn't give us a common fragment
// covering both, so we duplicate. Keep NameWithOwner on Repository
// so the webui can distinguish items from different repos within the
// same project.
type projectV2IssueContent struct {
	Id         githubv4.String
	Number     githubv4.Int
	Title      githubv4.String
	Url        githubv4.String
	State      githubv4.String
	Repository struct {
		NameWithOwner githubv4.String
	}
}

type projectV2PullRequestContent struct {
	Id         githubv4.String
	Number     githubv4.Int
	Title      githubv4.String
	Url        githubv4.String
	State      githubv4.String
	Repository struct {
		NameWithOwner githubv4.String
	}
}

type projectV2DraftContent struct {
	Id    githubv4.String
	Title githubv4.String
	Body  githubv4.String
}

type projectV2FieldValueConnection struct {
	Nodes []projectV2FieldValue
}

// projectV2FieldValue covers only the field-value shapes we render.
// Text, Number, Date, Iteration values can be added later if the UI
// needs them — for now the Status single-select drives column
// assignment and that's all we persist.
type projectV2FieldValue struct {
	Typename           githubv4.String `graphql:"__typename"`
	SingleSelectValue  projectV2SingleSelectValue  `graphql:"... on ProjectV2ItemFieldSingleSelectValue"`
	TextValue          projectV2TextValue          `graphql:"... on ProjectV2ItemFieldTextValue"`
}

type projectV2SingleSelectValue struct {
	Field struct {
		SingleSelect struct {
			Id   githubv4.String
			Name githubv4.String
		} `graphql:"... on ProjectV2SingleSelectField"`
	}
	OptionId githubv4.String
	Name     githubv4.String
}

type projectV2TextValue struct {
	// `field` returns ProjectV2FieldConfiguration (a union) — only inline
	// fragments allowed. ProjectV2FieldCommon is an interface that every
	// concrete field type implements, which covers id+name for free.
	Field struct {
		Common struct {
			Id   githubv4.String
			Name githubv4.String
		} `graphql:"... on ProjectV2FieldCommon"`
	}
	Text githubv4.String
}

// projectItemsPage walks one project's items in 25-at-a-time pages,
// addressing the project via node(id:) so we don't need to traverse
// the owner's whole project list on each call.
//
// Page size is deliberately modest: items × fieldValues dominates
// response size and cost, and GitHub's HTTP/2 layer will unilaterally
// CANCEL the stream on responses that get too large (not a friendly
// error, just a silent reset — been there).
type projectItemsPage struct {
	Node struct {
		Typename  githubv4.String          `graphql:"__typename"`
		ProjectV2 struct {
			Items projectV2ItemConnection `graphql:"items(first: 25, after: $itemCursor)"`
		} `graphql:"... on ProjectV2"`
	} `graphql:"node(id: $projectId)"`
	RateLimit rateLimit
}
