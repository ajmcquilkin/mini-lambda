package invoke

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/internal/client"
)

// NewCmd builds the `function invoke` command.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invoke NAME",
		Short: "Invoke a function with an event payload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, _ := cmd.Flags().GetString("host")

			payload, err := readPayload(cmd)
			if err != nil {
				return err
			}

			out, err := client.New(host).Invoke(args[0], payload)
			if err != nil {
				return err
			}

			if out.FunctionError != "" {
				// Handler error: emit the raw error payload to stderr and exit
				// non-zero, mirroring the AWS CLI's behavior.
				fmt.Fprintln(cmd.ErrOrStderr(), string(out.Payload))
				return fmt.Errorf("function error: %s", out.FunctionError)
			}

			cmd.OutOrStdout().Write(out.Payload)
			if n := len(out.Payload); n == 0 || out.Payload[n-1] != '\n' {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		},
	}

	cmd.Flags().String("host", defaultHost(), "mini-lambda daemon address (env MINI_LAMBDA_HOST)")
	cmd.Flags().String("payload", "", "JSON event payload (inline)")
	cmd.Flags().StringP("file", "f", "", "read the JSON event payload from a file ('-' for stdin)")

	return cmd
}

func defaultHost() string {
	if h := os.Getenv("MINI_LAMBDA_HOST"); h != "" {
		return h
	}
	return "127.0.0.1:9000"
}

// readPayload resolves the event payload from --payload, then -f/--file (with
// '-' meaning stdin), then bare stdin when neither flag is set.
func readPayload(cmd *cobra.Command) ([]byte, error) {
	if cmd.Flags().Changed("payload") {
		p, _ := cmd.Flags().GetString("payload")
		return []byte(p), nil
	}

	file, _ := cmd.Flags().GetString("file")
	if file != "" && file != "-" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read payload file: %w", err)
		}
		return b, nil
	}

	if file == "-" {
		return io.ReadAll(cmd.InOrStdin())
	}

	// No explicit source: read stdin if it is not an interactive terminal.
	if f, ok := cmd.InOrStdin().(*os.File); ok {
		if info, err := f.Stat(); err == nil && (info.Mode()&os.ModeCharDevice) != 0 {
			return []byte{}, nil
		}
	}
	return io.ReadAll(cmd.InOrStdin())
}
