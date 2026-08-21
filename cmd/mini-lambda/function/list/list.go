package list

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/internal/api"
	"github.com/ajmcquilkin/mini-lambda/internal/client"
)

// NewCmd builds the `function ls` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List functions",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			host, _ := cmd.Flags().GetString("host")
			output, _ := cmd.Flags().GetString("output")

			fns, err := client.New(host).ListFunctions()
			if err != nil {
				return err
			}

			if output == "json" {
				b, err := json.MarshalIndent(api.ListFunctionsResponse{Functions: fns}, "", "  ")
				if err != nil {
					return err
				}
				cmd.Println(string(b))
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tIMAGE\tMEMORY\tTIMEOUT\tLAST MODIFIED")
			for _, fn := range fns {
				fmt.Fprintf(w, "%s\t%s\t%dMB\t%ds\t%s\n",
					fn.FunctionName, fn.Code.ImageUri, fn.MemorySize, fn.Timeout,
					fn.LastModified.Format(time.RFC3339))
			}
			return w.Flush()
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
