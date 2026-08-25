package serve

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ajmcquilkin/mini-lambda/internal/daemon"
)

// Flag defaults.
const (
	defaultAddr                   = "127.0.0.1:9000"
	defaultRuntimeAddr            = "0.0.0.0:0"
	defaultMaxConcurrency         = 32
	defaultPerFunctionConcurrency = 4
	defaultIdleTTL                = 5 * time.Minute
	defaultShutdownTimeout        = 20 * time.Second
)

// NewCmd builds the `serve` command that runs the mini-lambda daemon.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the mini-lambda daemon",
		Long:  "serve starts the mini-lambda daemon that hosts and invokes functions locally.",
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, _ := cmd.Flags().GetString("addr")
			runtimeAddr, _ := cmd.Flags().GetString("runtime-addr")
			dataDir, _ := cmd.Flags().GetString("data")
			maxConc, _ := cmd.Flags().GetInt("max-concurrency")
			perFn, _ := cmd.Flags().GetInt("per-function-concurrency")
			idleTTL, _ := cmd.Flags().GetDuration("idle-ttl")
			portFile, _ := cmd.Flags().GetString("port-file")
			shutdownTimeout, _ := cmd.Flags().GetDuration("shutdown-timeout")
			reapOrphans, _ := cmd.Flags().GetBool("reap-orphans")

			resolvedData, err := resolveDataDir(dataDir)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return daemon.Run(ctx, daemon.Config{
				Addr:                   addr,
				RuntimeAddr:            runtimeAddr,
				DataDir:                resolvedData,
				MaxConcurrency:         maxConc,
				PerFunctionConcurrency: perFn,
				IdleTTL:                idleTTL,
				PortFile:               portFile,
				ShutdownTimeout:        shutdownTimeout,
				ReapOrphans:            reapOrphans,
				Logf: func(format string, a ...any) {
					cmd.Printf(format+"\n", a...)
				},
			})
		},
	}

	cmd.Flags().String("addr", defaultAddr, "public API listen address")
	cmd.Flags().String("runtime-addr", defaultRuntimeAddr, "Lambda Runtime API listen address (bind 0.0.0.0 so containers can reach it; :0 picks a free port)")
	cmd.Flags().String("data", "", "data directory for the state database (default ~/.mini-lambda)")
	cmd.Flags().Int("max-concurrency", defaultMaxConcurrency, "daemon-wide max concurrent slots")
	cmd.Flags().Int("per-function-concurrency", defaultPerFunctionConcurrency, "per-function max concurrent slots")
	cmd.Flags().Duration("idle-ttl", defaultIdleTTL, "idle slot time-to-live before reaping")
	cmd.Flags().String("port-file", "", "atomically write {\"api\":...,\"runtime\":...} resolved listen addresses to this path at readiness (removed on shutdown)")
	cmd.Flags().Duration("shutdown-timeout", defaultShutdownTimeout, "max time to drain in-flight invocations on SIGTERM/SIGINT before force-stopping containers")
	cmd.Flags().Bool("reap-orphans", true, "on startup, remove managed containers whose owning daemon has died")

	return cmd
}

// resolveDataDir expands an empty value to ~/.mini-lambda and otherwise returns
// the provided path unchanged.
func resolveDataDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".mini-lambda"), nil
}
