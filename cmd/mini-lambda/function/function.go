package function

import (
	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/function/create"
	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/function/delete"
	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/function/get"
	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/function/invoke"
	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/function/list"
	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/function/logs"
	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/function/update"
)

// NewCmd builds the `function` parent command and wires its leaf children.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "function",
		Short: "Manage mini-lambda functions",
		Long:  "function groups the subcommands used to create, inspect, invoke, and manage functions.",
	}

	cmd.AddCommand(create.NewCmd())
	cmd.AddCommand(list.NewCmd())
	cmd.AddCommand(get.NewCmd())
	cmd.AddCommand(update.NewCmd())
	cmd.AddCommand(delete.NewCmd())
	cmd.AddCommand(invoke.NewCmd())
	cmd.AddCommand(logs.NewCmd())

	return cmd
}
