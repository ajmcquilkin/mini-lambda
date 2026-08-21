package get

import (
	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/internal/cli"
	"github.com/ajmcquilkin/mini-lambda/internal/client"
)

// NewCmd builds the `function get` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get NAME",
		Short: "Show details for a function",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := cli.ResolveHost(cmd)
			output, _ := cmd.Flags().GetString("output")

			cfg, err := client.New(host).GetFunction(args[0])
			if err != nil {
				return err
			}
			return cli.RenderFunction(cmd, output, cfg)
		},
	}

	cli.AddHostFlag(cmd)
	cmd.Flags().String("output", "table", "output format (table|json)")

	return cmd
}
