package metriccmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/git-bug/git-bug/commands/execenv"
)

type showOptions struct {
	outputFormat string
	since        string
	until        string
}

func newMetricShowCommand(env *execenv.Env) *cobra.Command {
	opts := showOptions{outputFormat: "default"}

	cmd := &cobra.Command{
		Use:     "show ID",
		Short:   "Show a metric series and its points",
		Args:    cobra.ExactArgs(1),
		PreRunE: execenv.LoadBackend(env),
		RunE: execenv.CloseBackend(env, func(cmd *cobra.Command, args []string) error {
			return runShow(env, opts, args)
		}),
	}

	flags := cmd.Flags()
	flags.SortFlags = false
	flags.StringVarP(&opts.outputFormat, "format", "f", "default", "default | json | csv")
	flags.StringVar(&opts.since, "since", "", "Only include points on/after RFC3339 or unix seconds")
	flags.StringVar(&opts.until, "until", "", "Only include points strictly before RFC3339 or unix seconds")

	return cmd
}

func runShow(env *execenv.Env, opts showOptions, args []string) error {
	prefix := args[0]
	c, err := env.Backend.Metrics().ResolvePrefix(prefix)
	if err != nil {
		return err
	}
	snap := c.Snapshot()

	var since, until time.Time
	var sinceSet, untilSet bool
	if opts.since != "" {
		since, err = parseAtTime(opts.since)
		if err != nil {
			return err
		}
		sinceSet = true
	}
	if opts.until != "" {
		until, err = parseAtTime(opts.until)
		if err != nil {
			return err
		}
		untilSet = true
	}

	// Filter up-front — the snapshot's Points slice stays untouched
	// (we never want to mutate a snapshot, it's a shared value).
	pts := snap.Points
	if sinceSet || untilSet {
		filtered := make([]int, 0, len(pts))
		for i, p := range pts {
			if sinceSet && p.Time.Before(since) {
				continue
			}
			if untilSet && !p.Time.Before(until) {
				continue
			}
			filtered = append(filtered, i)
		}
		out := make([]struct {
			Time  time.Time         `json:"time"`
			Value float64           `json:"value"`
			Attrs map[string]string `json:"attrs,omitempty"`
		}, 0, len(filtered))
		for _, i := range filtered {
			out = append(out, struct {
				Time  time.Time         `json:"time"`
				Value float64           `json:"value"`
				Attrs map[string]string `json:"attrs,omitempty"`
			}{pts[i].Time, pts[i].Value, pts[i].Attrs})
		}
		return emit(env, snap.LabelKey(), snap.Unit, snap.Source, snap.Retired, out, opts.outputFormat)
	}

	// No filtering — pass snapshot points through directly.
	out := make([]struct {
		Time  time.Time         `json:"time"`
		Value float64           `json:"value"`
		Attrs map[string]string `json:"attrs,omitempty"`
	}, 0, len(pts))
	for _, p := range pts {
		out = append(out, struct {
			Time  time.Time         `json:"time"`
			Value float64           `json:"value"`
			Attrs map[string]string `json:"attrs,omitempty"`
		}{p.Time, p.Value, p.Attrs})
	}
	return emit(env, snap.LabelKey(), snap.Unit, snap.Source, snap.Retired, out, opts.outputFormat)
}

func emit(env *execenv.Env, labelKey, unit, source string, retired bool, points []struct {
	Time  time.Time         `json:"time"`
	Value float64           `json:"value"`
	Attrs map[string]string `json:"attrs,omitempty"`
}, format string) error {
	switch format {
	case "default":
		tag := ""
		if retired {
			tag = " (retired)"
		}
		env.Out.Printf("# %s  unit=%s  source=%s%s\n", labelKey, unit, source, tag)
		for _, p := range points {
			env.Out.Printf("%s\t%v", p.Time.UTC().Format(time.RFC3339), p.Value)
			for k, v := range p.Attrs {
				env.Out.Printf("\t%s=%s", k, v)
			}
			env.Out.Println()
		}
	case "csv":
		env.Out.Printf("time,value\n")
		for _, p := range points {
			env.Out.Printf("%s,%v\n", p.Time.UTC().Format(time.RFC3339), p.Value)
		}
	case "json":
		return env.Out.PrintJSON(map[string]interface{}{
			"labelKey": labelKey,
			"unit":     unit,
			"source":   source,
			"retired":  retired,
			"points":   points,
		})
	default:
		return fmt.Errorf("unknown format %q", format)
	}
	return nil
}
