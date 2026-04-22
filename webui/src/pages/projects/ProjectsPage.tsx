import Button from '@mui/material/Button';
import makeStyles from '@mui/styles/makeStyles';
import { useEffect, useState } from 'react';
import { useParams } from 'react-router';

type ProjectsSnapshot = {
  owner: string;
  fetchedAt: string;
  source: string;
  projects: ProjectRecord[] | null;
};

type ProjectRecord = {
  id: string;
  number: number;
  title: string;
  description?: string;
  url: string;
  closed?: boolean;
  updatedAt: string;
  fields?: ProjectField[];
  items?: ProjectItem[];
};

type ProjectField = {
  id: string;
  name: string;
  type: string;
  options?: string[];
};

type ProjectItem = {
  id: string;
  type: string;
  archived?: boolean;
  updatedAt: string;
  contentUrl?: string;
  contentTitle?: string;
  contentState?: string;
  contentRepo?: string;
  contentBody?: string;
  values?: Record<string, string>;
};

const useStyles = makeStyles((theme) => ({
  root: {
    maxWidth: 1400,
    margin: 'auto',
    padding: theme.spacing(2),
  },
  header: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(2),
    marginBottom: theme.spacing(2),
  },
  title: {
    ...theme.typography.h5,
    margin: 0,
  },
  subtitle: {
    color: theme.palette.text.secondary,
    fontSize: '0.85rem',
  },
  empty: {
    color: theme.palette.text.secondary,
    fontStyle: 'italic',
    padding: theme.spacing(4),
    textAlign: 'center',
  },
  error: {
    color: theme.palette.error.main,
    padding: theme.spacing(2),
    fontFamily:
      'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace',
  },
  projectCard: {
    border: `1px solid ${theme.palette.divider}`,
    borderRadius: theme.shape.borderRadius,
    marginBottom: theme.spacing(3),
    background: theme.palette.background.paper,
    overflow: 'hidden',
  },
  projectHead: {
    padding: theme.spacing(1.5, 2),
    borderBottom: `1px solid ${theme.palette.divider}`,
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(1.5),
  },
  projectTitle: {
    ...theme.typography.subtitle1,
    fontWeight: 600,
    margin: 0,
  },
  projectNumber: {
    color: theme.palette.text.secondary,
    fontSize: '0.85rem',
  },
  projectDesc: {
    padding: theme.spacing(1, 2),
    fontSize: '0.85rem',
    color: theme.palette.text.secondary,
    borderBottom: `1px solid ${theme.palette.divider}`,
  },
  closedBadge: {
    background: theme.palette.action.selected,
    color: theme.palette.text.secondary,
    padding: '1px 8px',
    borderRadius: 4,
    fontSize: '0.7rem',
    textTransform: 'uppercase',
  },
  // Horizontal scroll for the kanban band — too many columns is the
  // common case, not too few, so wrapping would make related items
  // drift off each other's eye-line.
  board: {
    display: 'flex',
    gap: theme.spacing(1),
    padding: theme.spacing(1.5),
    overflowX: 'auto',
  },
  column: {
    flex: '0 0 260px',
    background: theme.palette.background.default,
    borderRadius: theme.shape.borderRadius,
    padding: theme.spacing(1),
  },
  columnHead: {
    fontWeight: 600,
    fontSize: '0.8rem',
    marginBottom: theme.spacing(1),
    padding: theme.spacing(0.5, 1),
    borderBottom: `1px solid ${theme.palette.divider}`,
  },
  columnCount: {
    color: theme.palette.text.secondary,
    fontWeight: 400,
    marginLeft: theme.spacing(0.5),
  },
  item: {
    background: theme.palette.background.paper,
    border: `1px solid ${theme.palette.divider}`,
    borderRadius: theme.shape.borderRadius,
    padding: theme.spacing(1),
    marginBottom: theme.spacing(1),
    fontSize: '0.82rem',
  },
  itemTitle: {
    fontWeight: 500,
  },
  itemMeta: {
    color: theme.palette.text.secondary,
    fontSize: '0.75rem',
    marginTop: theme.spacing(0.25),
  },
  itemLink: {
    color: theme.palette.primary.main,
    textDecoration: 'none',
    '&:hover': { textDecoration: 'underline' },
  },
  source: {
    marginLeft: 'auto',
    color: theme.palette.text.disabled,
    fontSize: '0.75rem',
  },
  // Visual distinction for state: open stays neutral, closed/merged get
  // a muted colour so the reader can spot done work at a glance.
  stateOpen: { color: '#2da44e' },
  stateClosed: { color: '#8250df' },
  stateMerged: { color: '#8250df' },
  stateDraft: { color: theme.palette.text.secondary, fontStyle: 'italic' },
}));

/** ProjectsPage shows the GitHub Projects V2 snapshot cached for the
 *  current repo's owner. The snapshot lives in
 *  <parent-of-repo>/.git-bug-projects.json — refreshed by the Sync
 *  button or via POST /projects/<repo>. */
export default function ProjectsPage() {
  const { repoName } = useParams<{ repoName: string }>();
  const classes = useStyles();
  const [snap, setSnap] = useState<ProjectsSnapshot | null>(null);
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!repoName) return;
    let cancelled = false;
    setLoading(true);
    fetch(`/projects/${encodeURIComponent(repoName)}`)
      .then(async (r) => {
        if (!r.ok) throw new Error(await r.text());
        return r.json();
      })
      .then((j: ProjectsSnapshot | null) => {
        if (!cancelled) setSnap(j);
      })
      .catch((e: Error) => {
        if (!cancelled) setError(e.message || 'request failed');
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [repoName]);

  const triggerSync = async () => {
    if (!repoName) return;
    setSyncing(true);
    setError(null);
    try {
      const r = await fetch(
        `/projects/${encodeURIComponent(repoName)}`,
        { method: 'POST' }
      );
      if (!r.ok) throw new Error(await r.text());
      setSnap(await r.json());
    } catch (e) {
      setError((e as Error).message || 'sync failed');
    } finally {
      setSyncing(false);
    }
  };

  if (loading) return <div className={classes.empty}>Loading projects…</div>;

  return (
    <div className={classes.root}>
      <div className={classes.header}>
        <h2 className={classes.title}>Projects</h2>
        <span className={classes.subtitle}>
          {snap?.owner ? `owner: ${snap.owner}` : 'never synced'}
          {snap?.fetchedAt && ` · fetched ${new Date(snap.fetchedAt).toLocaleString()}`}
        </span>
        <span className={classes.source}>source: github.com</span>
        <Button
          variant="outlined"
          size="small"
          onClick={triggerSync}
          disabled={syncing}
        >
          {syncing ? 'Syncing…' : 'Sync now'}
        </Button>
      </div>

      {error && <div className={classes.error}>⚠ {error}</div>}

      {!snap && !error && (
        <div className={classes.empty}>
          No snapshot yet for this repo&rsquo;s owner. Click &ldquo;Sync now&rdquo; or run the
          main Sync from the header — a file will be written to the repo&rsquo;s
          parent directory (<code>.git-bug-projects.json</code>) and the list will
          populate here.
        </div>
      )}

      {snap && (!snap.projects || snap.projects.length === 0) && !error && (
        <div className={classes.empty}>
          No projects for <code>{snap.owner}</code>.
        </div>
      )}

      {snap?.projects?.map((p) => (
        <ProjectCard key={p.id} project={p} classes={classes} />
      ))}
    </div>
  );
}

function ProjectCard({
  project,
  classes,
}: {
  project: ProjectRecord;
  classes: ReturnType<typeof useStyles>;
}) {
  const columns = deriveColumns(project);
  const bucketed = bucketItems(project.items ?? [], columns);

  return (
    <div className={classes.projectCard}>
      <div className={classes.projectHead}>
        <h3 className={classes.projectTitle}>{project.title}</h3>
        <span className={classes.projectNumber}>#{project.number}</span>
        {project.closed && <span className={classes.closedBadge}>closed</span>}
        <span style={{ marginLeft: 'auto' }}>
          <a
            href={project.url}
            target="_blank"
            rel="noreferrer"
            className={classes.itemLink}
          >
            open on github ↗
          </a>
        </span>
      </div>
      {project.description && (
        <div className={classes.projectDesc}>{project.description}</div>
      )}
      <div className={classes.board}>
        {columns.map((col) => (
          <div key={col} className={classes.column}>
            <div className={classes.columnHead}>
              {col}
              <span className={classes.columnCount}>
                ({bucketed[col]?.length ?? 0})
              </span>
            </div>
            {(bucketed[col] ?? []).map((it) => (
              <ItemCard key={it.id} item={it} classes={classes} />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}

function ItemCard({
  item,
  classes,
}: {
  item: ProjectItem;
  classes: ReturnType<typeof useStyles>;
}) {
  const stateCls = item.contentState
    ? item.contentState === 'OPEN'
      ? classes.stateOpen
      : item.contentState === 'MERGED'
      ? classes.stateMerged
      : classes.stateClosed
    : classes.stateDraft;

  const title = item.contentTitle || '(untitled)';
  const isDraft = item.type === 'DRAFT_ISSUE';
  return (
    <div className={classes.item}>
      <div className={classes.itemTitle}>
        {item.contentUrl ? (
          <a
            href={item.contentUrl}
            target="_blank"
            rel="noreferrer"
            className={classes.itemLink}
          >
            {title}
          </a>
        ) : (
          <span>{title}</span>
        )}
      </div>
      <div className={classes.itemMeta}>
        <span className={stateCls}>
          {isDraft ? 'draft' : item.contentState?.toLowerCase() || 'unknown'}
        </span>
        {item.contentRepo && <span> · {item.contentRepo}</span>}
      </div>
    </div>
  );
}

// deriveColumns picks column names in priority order:
//   1. A single-select field called "Status" (case-insensitive) — this is
//      what GitHub Projects uses by default.
//   2. Any other single-select field if no Status exists.
//   3. Fallback: a single "All" column.
// We also add "(No status)" at the end when any items lack the chosen
// field — otherwise they'd vanish from the view.
function deriveColumns(p: ProjectRecord): string[] {
  const fields = p.fields ?? [];
  const selects = fields.filter((f) => f.type === 'ProjectV2SingleSelectField');
  const status =
    selects.find((f) => f.name.toLowerCase() === 'status') ?? selects[0];
  if (!status || !status.options || status.options.length === 0) {
    return ['All'];
  }
  const cols = [...status.options];
  const needsNoStatus = (p.items ?? []).some((it) => {
    const v = it.values?.[status.name];
    return !v;
  });
  if (needsNoStatus) cols.push('(No status)');
  return cols;
}

function bucketItems(
  items: ProjectItem[],
  columns: string[]
): Record<string, ProjectItem[]> {
  const out: Record<string, ProjectItem[]> = {};
  for (const c of columns) out[c] = [];
  // Single-column fallback: everything into "All".
  if (columns.length === 1 && columns[0] === 'All') {
    out['All'] = items.filter((it) => !it.archived);
    return out;
  }
  // Find which field name drives column assignment — mirror the
  // deriveColumns logic exactly so status/fallback stay in sync.
  const pickField = (it: ProjectItem): string | undefined => {
    if (!it.values) return undefined;
    for (const c of columns) {
      if (c === '(No status)') continue;
      // Find any values[key] that matches c — we don't know the exact
      // field name here, so scan. Cheap because values are O(5).
      for (const k of Object.keys(it.values)) {
        if (it.values[k] === c) return c;
      }
    }
    return undefined;
  };

  for (const it of items) {
    if (it.archived) continue;
    const bucket = pickField(it) ?? '(No status)';
    if (!out[bucket]) out[bucket] = [];
    out[bucket].push(it);
  }
  return out;
}
