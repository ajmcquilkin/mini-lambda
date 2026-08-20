package serve

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// NewCmd builds the `serve` command that runs the mini-lambda daemon.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the mini-lambda daemon",
		Long:  "serve starts the mini-lambda daemon that hosts and invokes functions locally.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("serve: not implemented")
			return nil
		},
	}

	cmd.Flags().String("addr", "127.0.0.1:8080", "address the daemon listens on")

	// Per-command flag binding is kept local; there is no shared config package yet.
	v := viper.New()
	_ = v.BindPFlag("addr", cmd.Flags().Lookup("addr"))

	return cmd
}
