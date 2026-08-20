package logs

import (
	"github.com/spf13/cobra"
)

// NewCmd builds the `function logs` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs NAME",
		Short: "Show logs for a function",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("function logs: not implemented")
			return nil
		},
	}

	cmd.Flags().Bool("follow", false, "stream logs as they are produced")

	return cmd
}
