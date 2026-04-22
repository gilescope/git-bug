// Package metriccmd is the `git-bug metric` command tree: record, list,
// show. Entry point mirrors bugcmd so root.go can hook it in with a
// single addCmdWithGroup call.
package metriccmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/git-bug/git-bug/cache"
	"github.com/git-bug/git-bug/commands/execenv"
)

const entityGroup = "metric"

type metricOptions struct {
	match        string
	outputFormat string
}

func NewMetricCommand(env *execenv.Env) *cobra.Command {
	options := metricOptions{outputFormat: "default"}

	cmd := &cobra.Command{
		Use:     "metric",
		Short:   "List metric series",
		Long:    "List locally known metric series. Each series is a (name, labels) pair with a time-series of values attached.",
		PreRunE: execenv.LoadBackend(env),
		RunE: execenv.CloseBackend(env, func(cmd *cobra.Command, args []string) error {
			return runMetricList(env, options)
		}),
	}

	flags := cmd.Flags()
	flags.SortFlags = false
	flags.StringVarP(&options.match, "match", "m", "",
		"Substring match against the canonical label key name{k=v,...}")
	flags.StringVarP(&options.outputFormat, "format", "f", "default",
		"Output format: default, json, id")

	cmd.AddCommand(newMetricRecordCommand(env))
	cmd.AddCommand(newMetricShowCommand(env))
	cmd.AddCommand(newMetricRetireCommand(env))

	return cmd
}

func runMetricList(env *execenv.Env, opts metricOptions) error {
	allIds := env.Backend.Metrics().AllIds()
	rows := make([]*cache.MetricExcerpt, 0, len(allIds))
	for _, id := range allIds {
		e, err := env.Backend.Metrics().ResolveExcerpt(id)
		if err != nil {
			return err
		}
		if opts.match != "" && !strings.Contains(e.LabelKey, opts.match) {
			continue
		}
		rows = append(rows, e)
	}
	// Most-recently-updated first — matches what you'd want when
	// scanning a dashboard: stale metrics drop off the top.
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].LastTimeUnix > rows[j].LastTimeUnix
	})

	switch opts.outputFormat {
	case "id":
		for _, r := range rows {
			env.Out.Println(r.Id().String())
		}
		return nil
	case "json":
		out := make([]map[string]interface{}, 0, len(rows))
		for _, r := range rows {
			out = append(out, map[string]interface{}{
				"id":         r.Id().String(),
				"name":       r.Name,
				"labels":     r.Labels,
				"labelKey":   r.LabelKey,
				"unit":       r.Unit,
				"source":     r.Source,
				"pointCount": r.PointCount,
				"lastTime":   time.Unix(r.LastTimeUnix, 0).UTC().Format(time.RFC3339),
				"retired":    r.Retired,
			})
		}
		return env.Out.PrintJSON(out)
	case "default":
		for _, r := range rows {
			tag := ""
			if r.Retired {
				tag = " (retired)"
			}
			last := "never"
			if r.LastTimeUnix > 0 {
				last = time.Unix(r.LastTimeUnix, 0).UTC().Format(time.RFC3339)
			}
			env.Out.Printf("%s\t%s\t%d pts\tlast=%s%s\n",
				r.Id().Human(), r.LabelKey, r.PointCount, last, tag)
		}
		return nil
	default:
		return fmt.Errorf("unknown format %q", opts.outputFormat)
	}
}

// parseLabelFlag turns "k=v" into a (k, v) pair. Returns an error on
// missing '=' so the CLI fails loudly instead of silently dropping
// the label. Values may contain '=' (everything after the first).
func parseLabelFlag(s string) (string, string, error) {
	idx := strings.Index(s, "=")
	if idx <= 0 {
		return "", "", fmt.Errorf("label %q: expected key=value", s)
	}
	return s[:idx], s[idx+1:], nil
}

// parseLabels turns a slice of k=v strings into a map, erroring on
// duplicates so a user-supplied -l twice isn't silently ignored.
func parseLabels(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, s := range raw {
		k, v, err := parseLabelFlag(s)
		if err != nil {
			return nil, err
		}
		if _, seen := out[k]; seen {
			return nil, fmt.Errorf("label %q specified more than once", k)
		}
		out[k] = v
	}
	return out, nil
}

// pickJSON is a tiny helper so subcommands can marshal a value to
// JSON without pulling in encoding/json themselves.
func pickJSON(v interface{}) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
