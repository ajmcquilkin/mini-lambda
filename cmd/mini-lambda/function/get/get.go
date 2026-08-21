package get

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/internal/api"
	"github.com/ajmcquilkin/mini-lambda/internal/client"
)

// NewCmd builds the `function get` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get NAME",
		Short: "Show details for a function",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, _ := cmd.Flags().GetString("host")
			output, _ := cmd.Flags().GetString("output")

			cfg, err := client.New(host).GetFunction(args[0])
			if err != nil {
				return err
			}
			return renderFunction(cmd, output, cfg)
		},
	}

	cmd.Flags().String("host", defaultHost(), "mini-lambda daemon address (env MINI_LAMBDA_HOST)")
	cmd.Flags().String("output", "table", "output format (table|json)")

	return cmd
}

func defaultHost() string {
	if h := os.Getenv("MINI_LAMBDA_HOST"); h != "" {
		return h
	}
	return "127.0.0.1:9000"
}

func renderFunction(cmd *cobra.Command, output string, cfg *api.FunctionConfiguration) error {
	if output == "json" {
		b, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		cmd.Println(string(b))
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintf(w, "NAME\t%s\n", cfg.FunctionName)
	fmt.Fprintf(w, "ARN\t%s\n", cfg.FunctionArn)
	fmt.Fprintf(w, "IMAGE\t%s\n", cfg.Code.ImageUri)
	fmt.Fprintf(w, "MEMORY\t%dMB\n", cfg.MemorySize)
	fmt.Fprintf(w, "TIMEOUT\t%ds\n", cfg.Timeout)
	if cfg.Environment != nil && len(cfg.Environment.Variables) > 0 {
		keys := make([]string, 0, len(cfg.Environment.Variables))
		for k := range cfg.Environment.Variables {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "ENV\t%s=%s\n", k, cfg.Environment.Variables[k])
		}
	}
	fmt.Fprintf(w, "LAST MODIFIED\t%s\n", cfg.LastModified.Format(time.RFC3339))
	return w.Flush()
}
