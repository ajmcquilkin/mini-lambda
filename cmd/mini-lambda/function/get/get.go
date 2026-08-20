package get

import (
	"github.com/spf13/cobra"
)

// NewCmd builds the `function get` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get NAME",
		Short: "Show details for a function",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("function get: not implemented")
			return nil
		},
	}

	cmd.Flags().String("output", "table", "output format (table|json)")

	return cmd
}
