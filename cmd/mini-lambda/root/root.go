package root

import (
	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/function"
	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/serve"
)

// NewRootCmd builds the top-level `mini-lambda` command and wires its children.
func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "mini-lambda",
		Short:         "Run and manage AWS-Lambda-style functions locally",
		Long:          "mini-lambda is a local-Lambda CLI and daemon for developing and invoking functions on your machine.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().String("config", "", "path to config file")
	cmd.PersistentFlags().Bool("verbose", false, "enable verbose logging")

	cmd.AddCommand(serve.NewCmd())
	cmd.AddCommand(function.NewCmd())

	return cmd
}
