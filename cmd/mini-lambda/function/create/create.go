package create

import (
	"github.com/spf13/cobra"
)

// NewCmd builds the `function create` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a new function",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("function create: not implemented")
			return nil
		},
	}

	cmd.Flags().String("runtime", "", "runtime the function executes on")
	cmd.Flags().String("handler", "", "entrypoint handler for the function")

	return cmd
}
