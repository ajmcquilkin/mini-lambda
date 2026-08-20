package delete

import (
	"github.com/spf13/cobra"
)

// NewCmd builds the `function delete` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete NAME",
		Short: "Delete a function",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("function delete: not implemented")
			return nil
		},
	}

	cmd.Flags().Bool("force", false, "delete without confirmation")

	return cmd
}
