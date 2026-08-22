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
	"strconv"
	"time"

	"github.com/ajmcquilkin/mini-lambda/internal/api"
	"github.com/ajmcquilkin/mini-lambda/internal/apierror"
	"github.com/ajmcquilkin/mini-lambda/internal/model"
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

	// Probe docker up front so a misconfigured/unreachable daemon surfaces as a
	// prominent, actionable startup warning instead of a cryptic error on the
	// first invoke. We still start serving: docker may come up later, and only
	// invocations (not the control plane) depend on it.
	cfg.logf("%s", checkDocker(ctx, rt))

	// Bind the Runtime API listener first so we know its port before building the
	// scheduler, which bakes "<host>:<port>/<token>" into each container's env.
	runtimeLn, err := net.Listen("tcp", cfg.RuntimeAddr)
	if err != nil {
		return fmt.Errorf("daemon: listen runtime api %q: %w", cfg.RuntimeAddr, err)
	}
	reachable := reachableHost(cfg.ReachableHostname, listenerPort(runtimeLn.Addr()))

	sched := scheduler.New(st, rt, scheduler.Config{
		ReachableHost:          reachable,
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

	cfg.logf("%s", startupLine(cfg.Addr, runtimeLn.Addr(), reachable))

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

// functionExister confirms a function exists before its logs are streamed. It
// is the sole store method the logs endpoint needs; satisfied by store.Store.
type functionExister interface {
	GetFunction(ctx context.Context, name string) (*model.Function, error)
}

// containerLister reports the live container IDs backing a function's slots.
// Satisfied by *scheduler.Engine.
type containerLister interface {
	FunctionContainerIDs(fn string) []string
}

// logStreamer opens a container's log stream. Satisfied by runtime.Runtime.
type logStreamer interface {
	Logs(ctx context.Context, id string, follow bool) (io.ReadCloser, error)
}

// logsHandler serves the live-logs endpoint. Its dependencies are the minimal
// consumer-side interfaces the handler actually uses, so it is exercised with
// httptest and small in-package fakes without standing up the whole daemon.
type logsHandler struct {
	fns        functionExister
	containers containerLister
	logs       logStreamer
}

// publicHandler builds the public API handler plus the live-logs endpoint.
func (d *daemon) publicHandler() http.Handler {
	mux := http.NewServeMux()
	// The AWS-shaped API owns everything under "/"; the more specific method+path
	// pattern for logs takes precedence in Go's ServeMux.
	mux.Handle("/", api.New(d.store, d.sched))
	lh := &logsHandler{fns: d.store, containers: d.sched, logs: d.rt}
	mux.HandleFunc("GET /mini-lambda/functions/{name}/logs", lh.handle)
	return mux
}

// handle streams combined logs from a function's live slot containers. Logs are
// live-only: when a slot dies its logs are gone. With follow=false it
// concatenates each live container's current logs; with follow=true it streams
// the first live container (a deliberate minimal choice — multiplexing multiple
// follow streams is out of scope for this round).
func (h *logsHandler) handle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := h.fns.GetFunction(r.Context(), name); err != nil {
		writeAPIError(w, err)
		return
	}

	follow := parseFollow(r.URL.Query().Get("follow"))
	ids := h.containers.FunctionContainerIDs(name)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if len(ids) == 0 {
		return
	}

	flusher, _ := w.(http.Flusher)
	if follow {
		h.stream(r.Context(), w, flusher, ids[0], true)
		return
	}
	for _, id := range ids {
		h.stream(r.Context(), w, flusher, id, false)
	}
}

// stream copies one container's logs to w, flushing as it goes.
func (h *logsHandler) stream(ctx context.Context, w io.Writer, flusher http.Flusher, id string, follow bool) {
	rc, err := h.logs.Logs(ctx, id, follow)
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

// reachableHost builds the "host:port" a container uses to reach the daemon's
// Runtime API listener (the AWS_LAMBDA_RUNTIME_API value the scheduler bakes
// into each container's env).
func reachableHost(hostname string, port int) string {
	return fmt.Sprintf("%s:%d", hostname, port)
}

// dockerPinger is the minimal slice of the docker runtime the startup
// connectivity check needs. *docker.Runtime satisfies it; keeping it here avoids
// widening the frozen runtime.Runtime interface with a Ping method.
type dockerPinger interface {
	Ping(ctx context.Context) error
	Endpoint() string
}

// checkDocker pings docker and returns the startup log line to emit.
func checkDocker(ctx context.Context, p dockerPinger) string {
	return dockerStatusLine(p, p.Ping(ctx))
}

// dockerStatusLine renders the docker-connectivity startup line from a ping
// result. On success it names the resolved endpoint; on failure it surfaces the
// runtime's actionable error (endpoint, probed sockets, "is Docker running?")
// as a warning and notes that serving continues regardless.
func dockerStatusLine(p dockerPinger, pingErr error) string {
	if pingErr != nil {
		return fmt.Sprintf("WARNING: %v — starting anyway; invocations will fail until Docker is reachable", pingErr)
	}
	return fmt.Sprintf("docker daemon reachable at %s", p.Endpoint())
}

// startupLine renders the daemon's "listening" log line. reachable is the
// already-resolved "host:port" string (from reachableHost), not the function.
func startupLine(apiAddr string, runtimeAddr net.Addr, reachable string) string {
	return fmt.Sprintf("mini-lambda daemon listening: api=%s runtime-api=%s (reachable as %s)", apiAddr, runtimeAddr, reachable)
}

// listenerPort extracts the TCP port a listener bound to. It returns 0 for a
// non-TCP address, which the daemon's tcp listeners never produce.
func listenerPort(addr net.Addr) int {
	if tcp, ok := addr.(*net.TCPAddr); ok {
		return tcp.Port
	}
	return 0
}

// writeAPIError mirrors internal/api's error mapping for the logs endpoint via
// the shared apierror.FromError mapping.
func writeAPIError(w http.ResponseWriter, err error) {
	apierror.FromError(err).WriteHTTP(w)
}

// ignoreClosed treats http.ErrServerClosed as a clean shutdown.
func ignoreClosed(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// parseFollow interprets the logs endpoint's ?follow query param with
// strconv.ParseBool semantics; empty or unparseable values are treated as
// false. This replaces a hand-rolled check: "1"/"t"/"T"/"true"/"True"/"TRUE"
// (and their false counterparts) are recognized, but "yes" is no longer truthy.
func parseFollow(v string) bool {
	b, err := strconv.ParseBool(v)
	return err == nil && b
}
