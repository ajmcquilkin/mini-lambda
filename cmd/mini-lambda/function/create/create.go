package create

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/cmd/mini-lambda/internal/cli"
	"github.com/ajmcquilkin/mini-lambda/internal/api"
	"github.com/ajmcquilkin/mini-lambda/internal/client"
)

// NewCmd builds the `function create` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create a new function",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host := cli.ResolveHost(cmd)
			image, _ := cmd.Flags().GetString("image")
			envPairs, _ := cmd.Flags().GetStringArray("env")
			memory, _ := cmd.Flags().GetInt("memory")
			timeout, _ := cmd.Flags().GetInt("timeout")
			output, _ := cmd.Flags().GetString("output")

			if image == "" {
				return fmt.Errorf("--image is required")
			}
			env, err := cli.ParseEnv(envPairs)
			if err != nil {
				return err
			}

			req := api.CreateFunctionRequest{
				FunctionName: args[0],
				Code:         api.Code{ImageUri: image},
				MemorySize:   memory,
				Timeout:      timeout,
			}
			if len(env) > 0 {
				req.Environment = &api.Environment{Variables: env}
			}

			cfg, err := client.New(host).CreateFunction(req)
			if err != nil {
				return err
			}
			return cli.RenderFunction(cmd, output, cfg)
		},
	}

	cli.AddHostFlag(cmd)
	cmd.Flags().String("image", "", "container image URI for the function (required)")
	cmd.Flags().StringArray("env", nil, "environment variable K=V (repeatable)")
	cmd.Flags().Int("memory", api.DefaultMemorySize, "memory size in MB")
	cmd.Flags().Int("timeout", api.DefaultTimeout, "timeout in seconds")
	cmd.Flags().String("output", "table", "output format (table|json)")

	return cmd
}
