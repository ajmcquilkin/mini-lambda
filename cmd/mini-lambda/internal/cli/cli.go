// Package cli holds the small helpers shared by the mini-lambda CLI's
// function subcommands: daemon host resolution, --env parsing, and rendering
// of a FunctionConfiguration. It lives under cmd/mini-lambda/internal so the
// Go toolchain forbids importing it outside cmd/mini-lambda/...
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/internal/api"
)

// HostFlag is the name of the shared --host flag.
const HostFlag = "host"

// hostFlagUsage is the usage string shown for --host across every subcommand.
const hostFlagUsage = "mini-lambda daemon address (env MINI_LAMBDA_HOST)"

// defaultHostAddr is the built-in fallback daemon address.
const defaultHostAddr = "127.0.0.1:9000"

// DefaultHost returns the daemon address to use when --host is not provided:
// the MINI_LAMBDA_HOST environment variable when set, otherwise the built-in
// default. This is used as the --host flag's default value.
func DefaultHost() string {
	if h := os.Getenv("MINI_LAMBDA_HOST"); h != "" {
		return h
	}
	return defaultHostAddr
}

// AddHostFlag registers the shared --host flag on cmd, defaulting to
// DefaultHost() so an explicit flag wins over MINI_LAMBDA_HOST, which in turn
// wins over the built-in default.
func AddHostFlag(cmd *cobra.Command) {
	cmd.Flags().String(HostFlag, DefaultHost(), hostFlagUsage)
}

// ResolveHost returns the effective daemon address for cmd, applying the
// precedence: explicit --host value > MINI_LAMBDA_HOST > default 127.0.0.1:9000.
func ResolveHost(cmd *cobra.Command) string {
	if cmd.Flags().Changed(HostFlag) {
		v, _ := cmd.Flags().GetString(HostFlag)
		return v
	}
	return DefaultHost()
}

// ParseEnv converts repeated K=V --env pairs into a map. Each pair must contain
// an '=' and a non-empty key; otherwise an error is returned. Later pairs
// override earlier ones with the same key. An empty input yields an empty
// (non-nil) map.
func ParseEnv(pairs []string) (map[string]string, error) {
	env := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --env %q: expected K=V", p)
		}
		env[k] = v
	}
	return env, nil
}

// RenderFunction prints a single FunctionConfiguration to cmd's stdout. When
// output is "json" it emits indented JSON; otherwise it writes an aligned
// human-readable table. Environment variables are printed in sorted key order.
func RenderFunction(cmd *cobra.Command, output string, cfg *api.FunctionConfiguration) error {
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
