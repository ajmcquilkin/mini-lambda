package delete

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/internal/client"
)

// NewCmd builds the `function rm` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rm NAME",
		Aliases: []string{"delete"},
		Short:   "Delete a function",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, _ := cmd.Flags().GetString("host")
			if err := client.New(host).DeleteFunction(args[0]); err != nil {
				return err
			}
			cmd.Printf("deleted %s\n", args[0])
			return nil
		},
	}

	cmd.Flags().String("host", defaultHost(), "mini-lambda daemon address (env MINI_LAMBDA_HOST)")

	return cmd
}

func defaultHost() string {
	if h := os.Getenv("MINI_LAMBDA_HOST"); h != "" {
		return h
	}
	return "127.0.0.1:9000"
}
