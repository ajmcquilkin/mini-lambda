// Package daemon assembles and runs the mini-lambda server process: it opens
// the store, migrates it, wires the docker runtime, scheduler, Runtime API
// listener, and public AWS-shaped API, and drives graceful shutdown.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ajmcquilkin/mini-lambda/internal/api"
	"github.com/ajmcquilkin/mini-lambda/internal/apierror"
	"github.com/ajmcquilkin/mini-lambda/internal/runtime"
	"github.com/ajmcquilkin/mini-lambda/internal/runtime/docker"
	"github.com/ajmcquilkin/mini-lambda/internal/runtimeapi"
	"github.com/ajmcquilkin/mini-lambda/internal/scheduler"
	"github.com/ajmcquilkin/mini-lambda/internal/store"
)

// DefaultReachableHostname is the hostname a container uses to reach the
// daemon's Runtime API listener. It resolves to the host gateway via the
// injected ExtraHosts entry.
const DefaultReachableHostname = "host.docker.internal"

// hostGatewayExtraHost maps the reachable hostname onto docker's magic
// host-gateway address inside each container.
const hostGatewayExtraHost = DefaultReachableHostname + ":host-gateway"

// shutdownTimeout bounds the graceful drain of each HTTP server.
const shutdownTimeout = 15 * time.Second

// Config configures the daemon.
type Config struct {
	// Addr is the public API listen address (e.g. "127.0.0.1:9000").
	Addr string
	// RuntimeAddr is the Runtime API listen address. Bind 0.0.0.0 so containers
	// can reach it; a ":0" port lets the OS pick a free port.
	RuntimeAddr string
	// DataDir holds the SQLite state file. It is created if missing.
	DataDir string

	MaxConcurrency         int
	PerFunctionConcurrency int
	IdleTTL                time.Duration

	// ReachableHostname overrides DefaultReachableHostname (mainly for tests).
	ReachableHostname string

	// Logf, if set, receives human-readable lifecycle log lines.
	Logf func(format string, args ...any)
}

type daemon struct {
	cfg   Config
	store store.Store
	rt    runtime.Runtime
	sched *scheduler.Engine
}

func (c Config) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// Run boots the daemon and blocks until ctx is cancelled, then shuts down
// gracefully (scheduler first so containers are killed, then the servers drain).
func Run(ctx context.Context, cfg Config) error {
	if cfg.ReachableHostname == "" {
		cfg.ReachableHostname = DefaultReachableHostname
	}

	st, err := openStore(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	rt, err := docker.New()
	if err != nil {
		return fmt.Errorf("daemon: docker runtime: %w", err)
	}

	// Bind the Runtime API listener first so we know its port before building the
	// scheduler, which bakes "<host>:<port>/<token>" into each container's env.
	runtimeLn, err := net.Listen("tcp", cfg.RuntimeAddr)
	if err != nil {
		return fmt.Errorf("daemon: listen runtime api %q: %w", cfg.RuntimeAddr, err)
	}
	runtimePort := runtimeLn.Addr().(*net.TCPAddr).Port
	reachableHost := fmt.Sprintf("%s:%d", cfg.ReachableHostname, runtimePort)

	sched := scheduler.New(st, rt, scheduler.Config{
		ReachableHost:          reachableHost,
		ExtraHosts:             []string{hostGatewayExtraHost},
		MaxConcurrency:         cfg.MaxConcurrency,
		PerFunctionConcurrency: cfg.PerFunctionConcurrency,
		IdleTTL:                cfg.IdleTTL,
	})

	d := &daemon{cfg: cfg, store: st, rt: rt, sched: sched}

	runtimeSrv := &http.Server{Handler: runtimeapi.New(sched)}
	apiSrv := &http.Server{Addr: cfg.Addr, Handler: d.publicHandler()}

	apiLn, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		runtimeLn.Close()
		return fmt.Errorf("daemon: listen api %q: %w", cfg.Addr, err)
	}

	serveErr := make(chan error, 2)
	go func() { serveErr <- ignoreClosed(runtimeSrv.Serve(runtimeLn)) }()
	go func() { serveErr <- ignoreClosed(apiSrv.Serve(apiLn)) }()

	cfg.logf("mini-lambda daemon listening: api=%s runtime-api=%s (reachable as %s)", cfg.Addr, runtimeLn.Addr(), reachableHost)

	select {
	case <-ctx.Done():
		cfg.logf("shutdown signal received, draining")
	case err := <-serveErr:
		// A server crashed; tear the rest down and report.
		d.shutdown(runtimeSrv, apiSrv)
		return err
	}

	d.shutdown(runtimeSrv, apiSrv)
	return nil
}

// shutdown stops the scheduler (killing containers) then drains both servers.
func (d *daemon) shutdown(runtimeSrv, apiSrv *http.Server) {
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// Stop the public API first so no new invocations arrive, then reap slots,
	// then drop the Runtime API listener the (now dead) RICs were polling.
	_ = apiSrv.Shutdown(sctx)
	if err := d.sched.Shutdown(sctx); err != nil {
		d.cfg.logf("scheduler shutdown: %v", err)
	}
	_ = runtimeSrv.Shutdown(sctx)
}

// publicHandler builds the public API handler plus the live-logs endpoint.
func (d *daemon) publicHandler() http.Handler {
	mux := http.NewServeMux()
	// The AWS-shaped API owns everything under "/"; the more specific method+path
	// pattern for logs takes precedence in Go's ServeMux.
	mux.Handle("/", api.New(d.store, d.sched))
	mux.HandleFunc("GET /mini-lambda/functions/{name}/logs", d.handleLogs)
	return mux
}

// handleLogs streams combined logs from a function's live slot containers.
// Logs are live-only: when a slot dies its logs are gone. With follow=false it
// concatenates each live container's current logs; with follow=true it streams
// the first live container (a deliberate minimal choice — multiplexing multiple
// follow streams is out of scope for this round).
func (d *daemon) handleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := d.store.GetFunction(r.Context(), name); err != nil {
		writeAPIError(w, err)
		return
	}

	follow := isTrue(r.URL.Query().Get("follow"))
	ids := d.sched.FunctionContainerIDs(name)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if len(ids) == 0 {
		return
	}

	flusher, _ := w.(http.Flusher)
	if follow {
		d.streamLogs(r.Context(), w, flusher, ids[0], true)
		return
	}
	for _, id := range ids {
		d.streamLogs(r.Context(), w, flusher, id, false)
	}
}

// streamLogs copies one container's logs to w, flushing as it goes.
func (d *daemon) streamLogs(ctx context.Context, w io.Writer, flusher http.Flusher, id string, follow bool) {
	rc, err := d.rt.Logs(ctx, id, follow)
	if err != nil {
		fmt.Fprintf(w, "mini-lambda: logs for %s unavailable: %v\n", id, err)
		return
	}
	defer rc.Close()

	buf := make([]byte, 32*1024)
	for {
		n, rerr := rc.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// openStore opens (creating the data dir if needed) and migrates the store.
func openStore(ctx context.Context, dataDir string) (store.Store, error) {
	if dataDir == "" {
		return nil, errors.New("daemon: empty data dir")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("daemon: create data dir %q: %w", dataDir, err)
	}
	st, err := store.Open(filepath.Join(dataDir, "state.db"))
	if err != nil {
		return nil, fmt.Errorf("daemon: open store: %w", err)
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, fmt.Errorf("daemon: migrate: %w", err)
	}
	return st, nil
}

// writeAPIError mirrors internal/api's error mapping for the logs endpoint.
func writeAPIError(w http.ResponseWriter, err error) {
	var apiErr *apierror.Error
	switch {
	case errors.As(err, &apiErr):
		apiErr.WriteHTTP(w)
	case errors.Is(err, store.ErrNotFound):
		apierror.NotFound(err.Error()).WriteHTTP(w)
	default:
		apierror.Internal(err.Error()).WriteHTTP(w)
	}
}

// ignoreClosed treats http.ErrServerClosed as a clean shutdown.
func ignoreClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func isTrue(v string) bool {
	switch v {
	case "1", "true", "TRUE", "True", "yes":
		return true
	default:
		return false
	}
}
