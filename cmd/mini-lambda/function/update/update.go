package update

import (
	"github.com/spf13/cobra"
)

// NewCmd builds the `function update` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update NAME",
		Short: "Update an existing function",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("function update: not implemented")
			return nil
		},
	}

	cmd.Flags().String("runtime", "", "runtime the function executes on")
	cmd.Flags().String("handler", "", "entrypoint handler for the function")

	return cmd
}
