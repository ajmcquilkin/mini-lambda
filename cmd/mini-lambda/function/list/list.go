package list

import (
	"github.com/spf13/cobra"
)

// NewCmd builds the `function list` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List functions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("function list: not implemented")
			return nil
		},
	}

	cmd.Flags().String("output", "table", "output format (table|json)")

	return cmd
}
