import Button from '@mui/material/Button';
import Checkbox from '@mui/material/Checkbox';
import Chip from '@mui/material/Chip';
import FormControlLabel from '@mui/material/FormControlLabel';
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

// ChartRow is the per-point shape we feed to recharts: numeric x/y
// plus the metadata we want available in dot renderers and the
// tooltip. Kept narrow on purpose — every field here is rendered.
//
// vLine and vOff split `v` along the trend axis: vLine is set when
// the point should pull the trend line (pass / fail / timed_out),
// vOff is set otherwise (cancelled, skipped, neutral, action_required).
// recharts skips null values when drawing a line, so feeding two
// separate `<Line>` series with these keys lets us draw a clean
// trend line through the meaningful points while still showing the
// off-trend dots as visible markers — without them yanking the line
// through arbitrary durations (a cancelled run can finish in 2 s).
type ChartRow = {
  t: number;
  v: number;
  vLine: number | null;
  vOff: number | null;
  concl?: string;
  jobUrl?: string;
};

// isLineWorthy decides which conclusions count toward the trend line.
// Real outcomes (success / failure / timed_out) do; runs that didn't
// produce a meaningful duration (cancelled, skipped, neutral, anything
// requiring user action) don't. Unknown conclusions default to "yes"
// because user-recorded series have no concl at all and need to draw.
function isLineWorthy(concl?: string): boolean {
  switch (concl) {
    case 'cancelled':
    case 'skipped':
    case 'neutral':
    case 'action_required':
      return false;
    default:
      return true;
  }
}

// isCiSeries flags series populated by the GitHub Actions ingester.
// Used to decide whether to apply the workflow-aware title format.
function isCiSeries(name: string): boolean {
  return name === 'ci.job.duration' || name === 'ci.job.status';
}

// formatSeriesTitle turns a raw series header into a human-readable
// chart title. For ci.* series with a `workflow` label that looks
// like a file path, we lead with the workflow basename so the chart
// reads "<workflow.yml> ci job duration" instead of the dotted
// metric name; for everything else, we just humanise the metric
// name (replace dots with spaces). Keep it pure — easier to test
// and the renderer can call it on every entry without caching.
function formatSeriesTitle(entry: ListEntry): string {
  const human = entry.name.replace(/\./g, ' ');
  if (isCiSeries(entry.name) && entry.labels?.workflow) {
    return `${workflowBasename(entry.labels.workflow)} ${human}`;
  }
  return human;
}

// workflowBasename strips the ".github/workflows/" prefix and any
// leading directory; if the path doesn't look like a real workflow
// path we return it unchanged so something is always shown.
function workflowBasename(path: string): string {
  const trimmed = path.replace(/^\.github\/workflows\//, '');
  const slash = trimmed.lastIndexOf('/');
  return slash >= 0 ? trimmed.slice(slash + 1) : trimmed;
}

// hasConcl reports whether any row carries a `concl` attr — the
// signal we use to decide whether to render the pass/fail legend.
// User-recorded series (e.g. test durations from a JUnit importer)
// won't have it, so the legend would be misleading.
function hasConcl(rows: ChartRow[]): boolean {
  for (const r of rows) {
    if (r.concl) return true;
  }
  return false;
}

// BackfillMenu is the "pull more history" affordance. It POSTs to
// /sync?repo=<name>&backfillDays=N which kicks off a one-shot
// time-bounded ingest. Returns 202 (request accepted) immediately;
// the actual fetch runs in the background and can take a minute or
// two for an active repo with many workflows. We poll /sync until
// the repo drops out of `active` to know when to refresh.
function BackfillMenu({
  repoName,
  busy,
  onBusy,
  onDone,
  onError,
}: {
  repoName: string;
  busy: boolean;
  onBusy: (b: boolean) => void;
  onDone: () => void;
  onError: (msg: string) => void;
}) {
  const [days, setDays] = useState(14);
  const trigger = async () => {
    onBusy(true);
    try {
      const r = await fetch(
        `/sync?repo=${encodeURIComponent(repoName)}&backfillDays=${days}`,
        { method: 'POST' }
      );
      if (!r.ok && r.status !== 202) {
        onError(await r.text());
        onBusy(false);
        return;
      }
      // Poll until this repo leaves the `active` list, then declare
      // done. 5 s cadence is friendly to the server and quick enough
      // that a user staring at the page sees feedback.
      const start = Date.now();
      const tick = async () => {
        try {
          const s = await (await fetch('/sync')).json();
          const active: string[] = s.active || [];
          if (!active.includes(repoName)) {
            onBusy(false);
            onDone();
            return;
          }
          if (Date.now() - start > 10 * 60 * 1000) {
            onBusy(false);
            onError('timed out waiting for backfill (still running on server)');
            return;
          }
          window.setTimeout(tick, 5000);
        } catch {
          window.setTimeout(tick, 5000);
        }
      };
      window.setTimeout(tick, 2000);
    } catch (e) {
      onError((e as Error).message || 'request failed');
      onBusy(false);
    }
  };
  return (
    <span style={{ display: 'inline-flex', gap: 4, alignItems: 'center' }}>
      <Select
        size="small"
        value={days}
        onChange={(e) => setDays(Number(e.target.value))}
        disabled={busy}
      >
        <MenuItem value={7}>Backfill 7d</MenuItem>
        <MenuItem value={14}>Backfill 14d</MenuItem>
        <MenuItem value={30}>Backfill 30d</MenuItem>
        <MenuItem value={90}>Backfill 90d</MenuItem>
      </Select>
      <Button variant="outlined" size="small" onClick={trigger} disabled={busy}>
        {busy ? 'Backfilling…' : 'Run'}
      </Button>
    </span>
  );
}

// dotColorFor maps a workflow-job conclusion to a chart colour.
// Conventions follow the existing pass/fail palette used in
// PrDiff (#2da44e green, #cf222e red); cancelled / skipped get a
// muted grey so they don't compete with the real signal. Unknown
// concl (e.g. user-recorded series with no `concl` attr) falls
// back to the neutral line colour so the dot is still visible.
function dotColorFor(concl?: string): string {
  switch (concl) {
    case 'success':
      return '#2da44e';
    case 'failure':
    case 'timed_out':
      return '#cf222e';
    case 'cancelled':
    case 'skipped':
    case 'neutral':
      return '#8c959f';
    case 'action_required':
      return '#bf8700';
    default:
      return '#57606a';
  }
}

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
  legend: {
    display: 'flex',
    gap: theme.spacing(1),
    marginTop: theme.spacing(0.5),
    paddingLeft: theme.spacing(7),
    fontSize: '0.7rem',
    color: theme.palette.text.secondary,
    alignItems: 'center',
  },
  legendDot: {
    width: 8,
    height: 8,
    borderRadius: '50%',
    display: 'inline-block',
    marginRight: 4,
    verticalAlign: 'middle',
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
  // ci.job.status is now visualised as the dot colour on the
  // matching ci.job.duration chart, so listing it as its own series
  // would just clutter the page. Hidden by default, opt-in via the
  // "show status series" checkbox for users who want raw access.
  const [showStatusSeries, setShowStatusSeries] = useState(false);
  // Cancelled / skipped / neutral / action_required runs report
  // arbitrary durations that aren't useful for trend analysis.
  // Hidden by default; enable to see when those runs happened.
  const [showOffTrend, setShowOffTrend] = useState(false);
  const [backfillBusy, setBackfillBusy] = useState(false);
  const [backfillMsg, setBackfillMsg] = useState<string | null>(null);
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
    let xs = entries;
    if (!showStatusSeries) {
      // ci.job.status is visualised via dot colour on ci.job.duration;
      // listing it separately is duplication. The toggle keeps it
      // accessible for anyone who wants the raw number.
      xs = xs.filter((e) => e.name !== 'ci.job.status');
    }
    const q = match.trim().toLowerCase();
    if (q) xs = xs.filter((e) => e.labelKey.toLowerCase().includes(q));
    return xs;
  }, [entries, match, showStatusSeries]);

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
        <BackfillMenu
          repoName={repoName!}
          busy={backfillBusy}
          onBusy={setBackfillBusy}
          onDone={() => {
            setBackfillMsg('Backfill complete — refreshing.');
            setReloadTick((t) => t + 1);
            window.setTimeout(() => setBackfillMsg(null), 4000);
          }}
          onError={(msg) => {
            setBackfillMsg(`Backfill failed: ${msg}`);
            window.setTimeout(() => setBackfillMsg(null), 8000);
          }}
        />
        {backfillMsg && (
          <span className={classes.subtitle} style={{ marginLeft: 8 }}>
            {backfillMsg}
          </span>
        )}
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
        <FormControlLabel
          control={
            <Checkbox
              size="small"
              checked={showStatusSeries}
              onChange={(e) => setShowStatusSeries(e.target.checked)}
            />
          }
          label="show status series"
          slotProps={{ typography: { fontSize: '0.85rem' } }}
        />
        <FormControlLabel
          control={
            <Checkbox
              size="small"
              checked={showOffTrend}
              onChange={(e) => setShowOffTrend(e.target.checked)}
            />
          }
          label="show cancelled / skipped"
          slotProps={{ typography: { fontSize: '0.85rem' } }}
        />
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
          showOffTrend={showOffTrend}
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
  showOffTrend,
  classes,
}: {
  entry: ListEntry;
  range: Range;
  repoName: string;
  showOffTrend: boolean;
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
  // We also carry `concl` + `jobUrl` onto the row so the per-dot
  // renderer can colour by pass/fail and wire the click-through.
  // dropped counts how many raw points the showOffTrend filter
  // skipped, so the empty-state copy can say "all N points are
  // cancelled/skipped" instead of the misleading "no points in
  // range" that suggests an empty time window.
  const { data, dropped } = useMemo<{ data: ChartRow[]; dropped: number }>(() => {
    if (!detail) return { data: [], dropped: 0 };
    const rows: ChartRow[] = [];
    let skipped = 0;
    for (const p of detail.points) {
      const concl = p.attrs?.concl;
      const include = isLineWorthy(concl);
      if (!include && !showOffTrend) {
        skipped++;
        continue;
      }
      const v = p.value;
      rows.push({
        t: Date.parse(p.time),
        v,
        vLine: include ? v : null,
        vOff: include ? null : v,
        concl,
        jobUrl: p.attrs?.jobUrl,
      });
    }
    return { data: rows, dropped: skipped };
  }, [detail, showOffTrend]);

  // Title gets a human-friendly rendering: for ci.* series with a
  // workflow file path label, lead with the workflow basename so the
  // chart reads "<workflow.yml> ci job duration" instead of the raw
  // dotted metric name. The workflow chip is then redundant; we
  // suppress it from the chip row so the user doesn't see the same
  // value twice.
  const title = formatSeriesTitle(entry);
  const titleHasWorkflow = !!(entry.labels?.workflow && isCiSeries(entry.name));

  return (
    <div className={classes.card}>
      <div className={classes.cardHead}>
        <span className={classes.name}>{title}</span>
        {entry.labels &&
          Object.entries(entry.labels)
            .filter(([k]) => !(titleHasWorkflow && k === 'workflow'))
            .map(([k, v]) => (
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
      {!loading && data.length === 0 && dropped > 0 && (
        <div className={classes.loadingChart}>
          {dropped} {dropped === 1 ? 'point' : 'points'} in range, all
          cancelled / skipped — toggle &ldquo;show cancelled / skipped&rdquo; to see them.
        </div>
      )}
      {!loading && data.length === 0 && dropped === 0 && (
        <div className={classes.loadingChart}>no points in range</div>
      )}
      {!loading && data.length > 0 && hasConcl(data) && (
        <div className={classes.legend}>
          <span><span className={classes.legendDot} style={{ background: dotColorFor('success') }} />pass</span>
          <span><span className={classes.legendDot} style={{ background: dotColorFor('failure') }} />fail</span>
          {showOffTrend && (
            <span><span className={classes.legendDot} style={{ background: dotColorFor('cancelled') }} />cancelled / skipped</span>
          )}
          <span style={{ marginLeft: 8, fontStyle: 'italic' }}>(click a dot to open the run on github)</span>
        </div>
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
                // The chart has two <Line>s sharing each row (vLine
                // and vOff) — exactly one is non-null per row, so
                // filtering nulls collapses the tooltip back to a
                // single entry per timestamp without us having to
                // know which series fed it.
                formatter={(v, _name, item) => {
                  if (v == null) return null as any;
                  const row: ChartRow | undefined = item?.payload;
                  const suffix = row?.concl ? ` (${row.concl})` : '';
                  return [formatValue(Number(v), entry.unit) + suffix, ''];
                }}
              />
              <Line
                type="monotone"
                dataKey="vLine"
                // Neutral stroke — the colour signal lives on the dots,
                // so a coloured line would double-encode the wrong thing
                // (what colour for a line connecting a pass and a fail?).
                stroke="#8c959f"
                strokeWidth={1.5}
                // connectNulls=false (recharts default) — the line
                // breaks across cancelled/skipped points so they don't
                // pull the trend through a meaningless duration.
                dot={<StatusDot />}
                activeDot={<StatusDot active />}
                isAnimationActive={false}
              />
              {/*
                Off-trend line: cancelled / skipped points carry vOff
                instead of vLine. stroke="none" so this layer draws
                only the markers — no segment connects them — which
                is what lets the user still see "this run happened"
                without it warping the trend.
              */}
              <Line
                type="monotone"
                dataKey="vOff"
                stroke="none"
                dot={<StatusDot />}
                activeDot={<StatusDot active />}
                isAnimationActive={false}
                legendType="none"
              />
            </LineChart>
          </ResponsiveContainer>
        </div>
      )}
    </div>
  );
}

// StatusDot is a per-point renderer for recharts <Line dot={...}>.
// recharts passes cx/cy/payload via cloneElement, hence the loose
// `props: any` — recharts' types for these are notoriously incomplete
// and trying to thread a tighter shape through fights the library
// without buying us anything. The render contract is documented at
// https://recharts.org/en-US/api/Line#dot.
function StatusDot(props: any) {
  const { cx, cy, payload, active } = props;
  if (cx == null || cy == null) return null;
  const row: ChartRow = payload;
  const fill = dotColorFor(row?.concl);
  // active=true is recharts' "you're hovered" hook; bump radius so
  // the user has obvious feedback. jobUrl makes the dot actionable
  // when set — the wrapping <a> works in SVG via xlink, and recharts
  // handles the click before the line layer steals it.
  const r = active ? 5 : 3;
  const circle = (
    <circle
      cx={cx}
      cy={cy}
      r={r}
      fill={fill}
      stroke="#fff"
      strokeWidth={1}
      style={{ cursor: row?.jobUrl ? 'pointer' : 'default' }}
    />
  );
  if (row?.jobUrl) {
    return (
      <a
        href={row.jobUrl}
        target="_blank"
        rel="noreferrer"
      >
        {circle}
      </a>
    );
  }
  return circle;
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

