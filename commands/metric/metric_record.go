package metriccmd

import (
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/git-bug/git-bug/commands/execenv"
)

type recordOptions struct {
	name   string
	labels []string
	attrs  []string
	unit   string
	source string
	at     string // RFC3339 timestamp; empty ⇒ now
}

func newMetricRecordCommand(env *execenv.Env) *cobra.Command {
	opts := recordOptions{source: "user"}

	cmd := &cobra.Command{
		Use:   "record NAME VALUE",
		Short: "Record a datapoint on a metric series",
		Long: `Record a datapoint.

The series is identified by NAME plus --label/-l pairs. If no series
with that identity exists yet, it's created implicitly. Per-sample
context (commit sha, host, CI run URL) goes in --attr pairs; these
don't create new series but show up when drilling down.`,
		Example: `  # CI run duration for a specific job on a commit
  git-bug metric record ci.job.duration 123.4 \
      --label job=rust-check --label workflow=main --label os=linux \
      --attr commit=abc123 --attr run=https://... \
      --unit s --source github.com/actions
`,
		Args:    cobra.ExactArgs(2),
		PreRunE: execenv.LoadBackendEnsureUser(env),
		RunE: execenv.CloseBackend(env, func(cmd *cobra.Command, args []string) error {
			return runRecord(env, opts, args)
		}),
	}

	flags := cmd.Flags()
	flags.SortFlags = false
	flags.StringSliceVarP(&opts.labels, "label", "l", nil,
		"Label pair k=v (repeatable). Labels are part of the series identity.")
	flags.StringSliceVarP(&opts.attrs, "attr", "a", nil,
		"Per-sample attribute k=v (repeatable). Attrs are NOT part of the series identity.")
	flags.StringVarP(&opts.unit, "unit", "u", "",
		"Unit hint: s, ms, count, bytes, percent, ratio, ...")
	flags.StringVarP(&opts.source, "source", "s", opts.source,
		"Source label: user, junit, go-test, github.com/actions, ...")
	flags.StringVar(&opts.at, "at", "",
		"Event time as RFC3339, or a unix-seconds integer. Default: now.")

	return cmd
}

func runRecord(env *execenv.Env, opts recordOptions, args []string) error {
	name := args[0]
	val, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		return fmt.Errorf("VALUE %q: %w", args[1], err)
	}

	labels, err := parseLabels(opts.labels)
	if err != nil {
		return err
	}
	attrs, err := parseLabels(opts.attrs)
	if err != nil {
		return err
	}

	eventTime, err := parseAtTime(opts.at)
	if err != nil {
		return err
	}

	series, err := env.Backend.Metrics().GetOrCreate(name, labels, opts.unit, opts.source)
	if err != nil {
		return err
	}

	op, err := series.Record(eventTime, val, attrs)
	if err != nil {
		return err
	}

	env.Out.Printf("recorded %s  value=%v  at=%s  op=%s\n",
		series.Snapshot().LabelKey(),
		val,
		eventTime.UTC().Format(time.RFC3339),
		op.Id().Human(),
	)
	return nil
}

// parseAtTime accepts RFC3339 or a bare unix-seconds integer so
// callers can pipe either form into --at. Empty string returns the
// current wall-clock time.
func parseAtTime(s string) (time.Time, error) {
	if s == "" {
		return time.Now(), nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--at %q: expected RFC3339 or unix seconds", s)
	}
	return t, nil
}
