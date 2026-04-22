package metriccmd

import (
	"github.com/spf13/cobra"

	"github.com/git-bug/git-bug/commands/execenv"
)

func newMetricRetireCommand(env *execenv.Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "retire ID",
		Short:   "Mark a metric series inactive",
		Long:    "Tombstone a series so it hides from default UI listings. Data stays readable via --show-retired filters and the raw entity.",
		Args:    cobra.ExactArgs(1),
		PreRunE: execenv.LoadBackendEnsureUser(env),
		RunE: execenv.CloseBackend(env, func(cmd *cobra.Command, args []string) error {
			c, err := env.Backend.Metrics().ResolvePrefix(args[0])
			if err != nil {
				return err
			}
			op, err := c.Retire()
			if err != nil {
				return err
			}
			env.Out.Printf("retired %s  op=%s\n", c.Snapshot().LabelKey(), op.Id().Human())
			return nil
		}),
	}
	return cmd
}
