package logs

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/internal/cli"
	"github.com/ajmcquilkin/mini-lambda/internal/client"
)

// NewCmd builds the `function logs` command. It streams the live logs of a
// function's running slot containers. Logs are ephemeral: only currently-live
// slots produce output, and it disappears when a slot is destroyed.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs NAME",
		Short: "Show live logs for a function",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := cli.ResolveHost(cmd)
			follow, _ := cmd.Flags().GetBool("follow")

			rc, err := client.New(host).Logs(args[0], follow)
			if err != nil {
				return err
			}
			defer rc.Close()

			_, err = io.Copy(cmd.OutOrStdout(), rc)
			return err
		},
	}

	cli.AddHostFlag(cmd)
	cmd.Flags().BoolP("follow", "f", false, "stream logs as they are produced")

	return cmd
}
