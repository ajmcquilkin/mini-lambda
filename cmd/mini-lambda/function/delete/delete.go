package delete

import (
	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/internal/cli"
	"github.com/ajmcquilkin/mini-lambda/internal/client"
)

// NewCmd builds the `function rm` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rm NAME",
		Aliases: []string{"delete"},
		Short:   "Delete a function",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := cli.ResolveHost(cmd)
			if err := client.New(host).DeleteFunction(args[0]); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}

	cli.AddHostFlag(cmd)

	return cmd
}
