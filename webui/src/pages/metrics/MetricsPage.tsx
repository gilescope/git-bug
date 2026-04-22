import Button from '@mui/material/Button';
import Chip from '@mui/material/Chip';
import MenuItem from '@mui/material/MenuItem';
import Select from '@mui/material/Select';
import TextField from '@mui/material/TextField';
import makeStyles from '@mui/styles/makeStyles';
import { useEffect, useMemo, useState } from 'react';
import { useParams } from 'react-router';
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';

type ListEntry = {
  id: string;
  name: string;
  labels?: Record<string, string>;
  labelKey: string;
  unit?: string;
  source?: string;
  pointCount: number;
  lastTime?: string;
  retired?: boolean;
};

type ListResponse = {
  series: ListEntry[];
};

type Point = {
  time: string; // RFC3339
  value: number;
  attrs?: Record<string, string>;
};

type Detail = {
  id: string;
  name: string;
  labels?: Record<string, string>;
  labelKey: string;
  unit?: string;
  source?: string;
  retired?: boolean;
  points: Point[];
};

type Range = '7d' | '30d' | '90d' | '365d' | 'all';

const rangeOffsets: Record<Range, number | null> = {
  '7d': 7,
  '30d': 30,
  '90d': 90,
  '365d': 365,
  all: null,
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
    flexWrap: 'wrap',
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
  searchRow: {
    display: 'flex',
    gap: theme.spacing(1.5),
    alignItems: 'center',
    marginBottom: theme.spacing(2),
    flexWrap: 'wrap',
  },
  search: {
    minWidth: 260,
  },
  card: {
    border: `1px solid ${theme.palette.divider}`,
    borderRadius: theme.shape.borderRadius,
    padding: theme.spacing(1.5),
    marginBottom: theme.spacing(2),
    background: theme.palette.background.paper,
  },
  cardHead: {
    display: 'flex',
    alignItems: 'center',
    gap: theme.spacing(1),
    flexWrap: 'wrap',
    marginBottom: theme.spacing(1),
  },
  name: {
    fontWeight: 600,
  },
  labels: {
    color: theme.palette.text.secondary,
    fontSize: '0.78rem',
    fontFamily:
      'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace',
  },
  meta: {
    color: theme.palette.text.disabled,
    fontSize: '0.75rem',
    marginLeft: 'auto',
  },
  chart: {
    width: '100%',
    height: 220,
  },
  loadingChart: {
    color: theme.palette.text.disabled,
    fontStyle: 'italic',
    padding: theme.spacing(1),
  },
}));

/** MetricsPage lists all locally-known metric series for a repo and
 *  draws a small line chart per series, scoped by the selected time
 *  range. Rendered read-only: recording a point is a CLI concern. */
export default function MetricsPage() {
  const { repoName } = useParams<{ repoName: string }>();
  const classes = useStyles();

  const [entries, setEntries] = useState<ListEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [match, setMatch] = useState('');
  const [range, setRange] = useState<Range>('30d');
  // reloadTick is bumped on focus / Refresh; its value is part of the
  // useEffect deps so every increment forces a re-fetch. We don't
  // care about the numeric value, only that it changed.
  const [reloadTick, setReloadTick] = useState(0);

  useEffect(() => {
    if (!repoName) return;
    let cancelled = false;
    fetch(`/metrics/${encodeURIComponent(repoName)}`)
      .then(async (r) => {
        if (!r.ok) throw new Error(await r.text());
        return r.json() as Promise<ListResponse>;
      })
      .then((j) => {
        if (!cancelled) setEntries(j.series ?? []);
      })
      .catch((e: Error) => {
        if (!cancelled) setError(e.message || 'request failed');
      });
    return () => {
      cancelled = true;
    };
  }, [repoName, reloadTick]);

  // Re-fetch when the tab regains focus. Common flow: user hits Sync
  // in the header (which fires off a background bulk sync), switches
  // to another tab while it runs, then comes back. Without this the
  // metrics list silently reflects pre-sync state until a hard
  // reload — the "only one datapoint" surprise the user just hit.
  useEffect(() => {
    const onFocus = () => setReloadTick((t) => t + 1);
    window.addEventListener('focus', onFocus);
    return () => window.removeEventListener('focus', onFocus);
  }, []);

  const filtered = useMemo(() => {
    if (!entries) return [];
    const q = match.trim().toLowerCase();
    if (!q) return entries;
    return entries.filter((e) => e.labelKey.toLowerCase().includes(q));
  }, [entries, match]);

  if (error) return <div className={classes.error}>⚠ {error}</div>;
  if (!entries) return <div className={classes.empty}>Loading metrics…</div>;

  return (
    <div className={classes.root}>
      <div className={classes.header}>
        <h2 className={classes.title}>Metrics</h2>
        <span className={classes.subtitle}>
          {entries.length} series
          {entries.length !== filtered.length && ` · ${filtered.length} shown`}
        </span>
        <Button
          variant="outlined"
          size="small"
          onClick={() => setReloadTick((t) => t + 1)}
        >
          Refresh
        </Button>
      </div>

      <div className={classes.searchRow}>
        <TextField
          className={classes.search}
          label="Filter"
          placeholder="substring of name{k=v,...}"
          size="small"
          value={match}
          onChange={(e) => setMatch(e.target.value)}
        />
        <Select
          size="small"
          value={range}
          onChange={(e) => setRange(e.target.value as Range)}
        >
          <MenuItem value="7d">Last 7 days</MenuItem>
          <MenuItem value="30d">Last 30 days</MenuItem>
          <MenuItem value="90d">Last 90 days</MenuItem>
          <MenuItem value="365d">Last 365 days</MenuItem>
          <MenuItem value="all">All time</MenuItem>
        </Select>
      </div>

      {filtered.length === 0 && (
        <div className={classes.empty}>
          No series match. Record one from the CLI:
          <br />
          <code style={{ display: 'inline-block', marginTop: 8 }}>
            git-bug metric record ci.job.duration 12.3 --label job=build
          </code>
        </div>
      )}

      {filtered.map((e) => (
        <SeriesCard
          key={e.id}
          entry={e}
          range={range}
          repoName={repoName!}
          classes={classes}
        />
      ))}
    </div>
  );
}

function SeriesCard({
  entry,
  range,
  repoName,
  classes,
}: {
  entry: ListEntry;
  range: Range;
  repoName: string;
  classes: ReturnType<typeof useStyles>;
}) {
  const [detail, setDetail] = useState<Detail | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    const params = new URLSearchParams();
    params.set('id', entry.id);
    const offset = rangeOffsets[range];
    if (offset !== null) {
      const since = new Date(Date.now() - offset * 86_400_000).toISOString();
      params.set('since', since);
    }
    fetch(`/metrics/${encodeURIComponent(repoName)}?${params.toString()}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('fetch failed'))))
      .then((j: Detail) => {
        if (!cancelled) setDetail(j);
      })
      .catch(() => {
        if (!cancelled) setDetail(null);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [entry.id, range, repoName]);

  // Recharts wants numeric x — epoch ms is the standard choice; we
  // format back to a date label inside the Tooltip / XAxis formatter.
  const data = useMemo(() => {
    if (!detail) return [];
    return detail.points.map((p) => ({ t: Date.parse(p.time), v: p.value }));
  }, [detail]);

  return (
    <div className={classes.card}>
      <div className={classes.cardHead}>
        <span className={classes.name}>{entry.name}</span>
        {entry.labels &&
          Object.entries(entry.labels).map(([k, v]) => (
            <Chip
              key={k}
              size="small"
              label={`${k}=${v}`}
              variant="outlined"
              style={{ height: 20, fontSize: '0.7rem' }}
            />
          ))}
        {entry.retired && <Chip size="small" label="retired" color="default" />}
        <span className={classes.meta}>
          unit: {entry.unit || '—'} · source: {entry.source || '—'} ·{' '}
          {detail ? detail.points.length : entry.pointCount} pts
        </span>
      </div>
      {loading && <div className={classes.loadingChart}>loading points…</div>}
      {!loading && data.length === 0 && (
        <div className={classes.loadingChart}>no points in range</div>
      )}
      {!loading && data.length > 0 && (
        <div className={classes.chart}>
          <ResponsiveContainer>
            <LineChart
              data={data}
              margin={{ top: 4, right: 16, left: 0, bottom: 8 }}
            >
              <CartesianGrid strokeDasharray="3 3" opacity={0.3} />
              <XAxis
                dataKey="t"
                type="number"
                domain={['dataMin', 'dataMax']}
                tickFormatter={(v) => formatAxisTime(v, range)}
                fontSize={11}
              />
              <YAxis
                fontSize={11}
                tickFormatter={(v) => formatValue(v, entry.unit)}
                width={56}
              />
              <Tooltip
                labelFormatter={(v) => new Date(v as number).toISOString()}
                formatter={(v) => formatValue(Number(v), entry.unit)}
              />
              <Line
                type="monotone"
                dataKey="v"
                stroke="#2da44e"
                strokeWidth={2}
                dot={false}
                isAnimationActive={false}
              />
            </LineChart>
          </ResponsiveContainer>
        </div>
      )}
    </div>
  );
}

// Axis label: coarse for long ranges, fine for short ones. Saves
// tick density without needing to hand-tune it per metric.
function formatAxisTime(v: number, range: Range): string {
  const d = new Date(v);
  if (range === '7d') {
    return d.toLocaleString(undefined, {
      month: 'short',
      day: 'numeric',
      hour: 'numeric',
    });
  }
  return d.toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
  });
}

// formatValue appends a unit hint to values so the tooltip reads
// naturally. Conservative — if we don't recognise the unit we drop
// it rather than render garbage.
function formatValue(v: number, unit?: string): string {
  if (Number.isNaN(v)) return '—';
  const n =
    Math.abs(v) >= 100 ? v.toFixed(0) : Math.abs(v) >= 1 ? v.toFixed(2) : v.toFixed(4);
  if (!unit) return n;
  switch (unit) {
    case 's':
      return `${n} s`;
    case 'ms':
      return `${n} ms`;
    case 'count':
    case 'bytes':
    case 'percent':
    case 'ratio':
      return `${n} ${unit}`;
    default:
      return `${n} ${unit}`;
  }
}

