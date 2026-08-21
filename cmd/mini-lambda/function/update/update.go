package update

import (
	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/internal/cli"
	"github.com/ajmcquilkin/mini-lambda/internal/api"
	"github.com/ajmcquilkin/mini-lambda/internal/client"
)

// NewCmd builds the `function update` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update NAME",
		Short: "Update an existing function's configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := cli.ResolveHost(cmd)
			output, _ := cmd.Flags().GetString("output")

			var req api.UpdateFunctionConfigurationRequest
			if cmd.Flags().Changed("image") {
				image, _ := cmd.Flags().GetString("image")
				req.Code = &api.Code{ImageUri: image}
			}
			if cmd.Flags().Changed("env") {
				pairs, _ := cmd.Flags().GetStringArray("env")
				env, err := cli.ParseEnv(pairs)
				if err != nil {
					return err
				}
				req.Environment = &api.Environment{Variables: env}
			}
			if cmd.Flags().Changed("memory") {
				memory, _ := cmd.Flags().GetInt("memory")
				req.MemorySize = &memory
			}
			if cmd.Flags().Changed("timeout") {
				timeout, _ := cmd.Flags().GetInt("timeout")
				req.Timeout = &timeout
			}

			cfg, err := client.New(host).UpdateFunctionConfiguration(args[0], req)
			if err != nil {
				return err
			}
			return cli.RenderFunction(cmd, output, cfg)
		},
	}

	cli.AddHostFlag(cmd)
	cmd.Flags().String("image", "", "container image URI for the function")
	cmd.Flags().StringArray("env", nil, "environment variable K=V (repeatable; replaces the set)")
	cmd.Flags().Int("memory", api.DefaultMemorySize, "memory size in MB")
	cmd.Flags().Int("timeout", api.DefaultTimeout, "timeout in seconds")
	cmd.Flags().String("output", "table", "output format (table|json)")

	return cmd
}
