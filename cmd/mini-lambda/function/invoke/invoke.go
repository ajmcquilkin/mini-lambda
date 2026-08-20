package invoke

import (
	"github.com/spf13/cobra"
)

// NewCmd builds the `function invoke` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invoke NAME",
		Short: "Invoke a function",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("function invoke: not implemented")
			return nil
		},
	}

	cmd.Flags().String("payload", "", "JSON payload passed to the function")

	return cmd
}
