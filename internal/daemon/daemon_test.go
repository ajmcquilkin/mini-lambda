package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ajmcquilkin/mini-lambda/internal/model"
	"github.com/ajmcquilkin/mini-lambda/internal/store"
)

func TestParseFollow(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"1", true},
		{"t", true},
		{"T", true},
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"0", false},
		{"f", false},
		{"false", false},
		{"False", false},
		{"FALSE", false},
		{"", false},
		// Behavior change vs the old hand-rolled isTrue: "yes" is no longer truthy.
		{"yes", false},
		{"no", false},
		{"nope", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, parseFollow(tt.in))
		})
	}
}

func TestReachableHost(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		port     int
		want     string
	}{
		{"default host", "host.docker.internal", 9001, "host.docker.internal:9001"},
		{"loopback", "127.0.0.1", 0, "127.0.0.1:0"},
		{"custom", "gateway", 65535, "gateway:65535"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, reachableHost(tt.hostname, tt.port))
		})
	}
}

func TestListenerPort(t *testing.T) {
	tests := []struct {
		name string
		addr net.Addr
		want int
	}{
		{"tcp addr", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4321}, 4321},
		{"non-tcp addr", &net.UnixAddr{Name: "/tmp/x.sock", Net: "unix"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, listenerPort(tt.addr))
		})
	}
}

func TestStartupLine(t *testing.T) {
	reachable := reachableHost("host.docker.internal", 9001)
	runtimeAddr := &net.TCPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 9001}

	got := startupLine("127.0.0.1:9000", runtimeAddr, reachable)

	// The resolved host string must appear verbatim; the old bug logged the
	// reachableHost function value ("%!s(func...)") instead.
	assert.Contains(t, got, reachable)
	assert.NotContains(t, got, "%!")
	assert.Equal(t, "mini-lambda daemon listening: api=127.0.0.1:9000 runtime-api=0.0.0.0:9001 (reachable as host.docker.internal:9001)", got)
}

// samplePortFile is a representative readiness document reused across the
// contract tests.
var samplePortFile = portFile{
	API:              "127.0.0.1:54321",
	Runtime:          "[::]:9001",
	RuntimeReachable: "host.docker.internal:9001",
	PID:              4242,
}

func TestReadyLine(t *testing.T) {
	// The READY line is a machine-parseable API: its exact shape is contractual.
	// v2 adds runtime_reachable (the container-dialable host:port) and pid.
	got := readyLine(samplePortFile)
	assert.Equal(t, "MINI_LAMBDA_READY api=127.0.0.1:54321 runtime=[::]:9001 runtime_reachable=host.docker.internal:9001 pid=4242", got)
	assert.True(t, strings.HasPrefix(got, "MINI_LAMBDA_READY "))
	assert.NotContains(t, got, "%!")
}

func TestReadyLineUsesResolvedEphemeralPort(t *testing.T) {
	// Binding ":0" must surface the OS-chosen port through the resolved
	// listener address the READY line is built from — never the flag string.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	resolved := ln.Addr().String()
	port := listenerPort(ln.Addr())
	require.NotZero(t, port, "OS must assign a concrete port for :0")

	line := readyLine(portFile{API: resolved, Runtime: resolved, RuntimeReachable: "host.docker.internal:" + strconv.Itoa(port), PID: 1})
	// The api field carries the resolved (non-zero) port, not the ":0" flag.
	assert.Contains(t, line, "api="+resolved)
	assert.NotContains(t, line, "api=127.0.0.1:0 ")
}

func TestShutdownLine(t *testing.T) {
	// The shutdown line is a machine-parseable API too: exact strings matter.
	assert.Equal(t, "MINI_LAMBDA_SHUTDOWN complete", shutdownLine(false))
	assert.Equal(t, "MINI_LAMBDA_SHUTDOWN forced", shutdownLine(true))
	assert.True(t, strings.HasPrefix(shutdownLine(false), "MINI_LAMBDA_SHUTDOWN "))
}

func TestWritePortFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")

	require.NoError(t, writePortFile(path, defaultPortFileMode, samplePortFile))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var pf portFile
	require.NoError(t, json.Unmarshal(data, &pf))
	assert.Equal(t, samplePortFile, pf)
	// Exact JSON shape (field names + order) is part of the contract for tooling.
	assert.JSONEq(t, `{"api":"127.0.0.1:54321","runtime":"[::]:9001","runtime_reachable":"host.docker.internal:9001","pid":4242}`, string(data))
}

func TestWritePortFileMode(t *testing.T) {
	// os.CreateTemp makes the temp 0600; the write must chmod it so the file is
	// published with the requested (default world-readable) mode.
	dir := t.TempDir()
	def := filepath.Join(dir, "default.json")
	require.NoError(t, writePortFile(def, defaultPortFileMode, samplePortFile))
	info, err := os.Stat(def)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "default port file must be world-readable (0644)")

	// A tighter mode is honored too (proving the chmod is real, not incidental).
	tight := filepath.Join(dir, "tight.json")
	require.NoError(t, writePortFile(tight, 0o600, samplePortFile))
	info, err = os.Stat(tight)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestWritePortFileAtomicRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")

	require.NoError(t, writePortFile(path, defaultPortFileMode, samplePortFile))

	// The temp files must be renamed away, not left littering the dir.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "ports.json", entries[0].Name())
}

func TestRemovePortFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	require.NoError(t, writePortFile(path, defaultPortFileMode, samplePortFile))

	removePortFile(path, func(string, ...any) {})
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err))

	// Removing a missing file is a silent no-op (no log line).
	logged := false
	removePortFile(path, func(string, ...any) { logged = true })
	assert.False(t, logged)
}

func TestHealthzBody(t *testing.T) {
	assert.JSONEq(t, `{"status":"ok","docker":"ok"}`, string(healthzBody(true)))
	assert.JSONEq(t, `{"status":"ok","docker":"unreachable"}`, string(healthzBody(false)))
}

func serveHealthz(t *testing.T, h *healthzHandler) *http.Response {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.handle)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(t.Context())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Result()
}

func TestHealthzAlwaysOKRegardlessOfDocker(t *testing.T) {
	tests := []struct {
		name       string
		pinger     dockerPinger
		wantDocker string
	}{
		{"docker reachable", fakePinger{}, "ok"},
		{"docker unreachable", fakePinger{pingErr: errors.New("down")}, "unreachable"},
		{"no pinger wired", nil, "unreachable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := serveHealthz(t, &healthzHandler{pinger: tt.pinger})
			defer res.Body.Close()

			// The daemon being up is the whole signal: 200 no matter what docker does.
			assert.Equal(t, http.StatusOK, res.StatusCode)
			assert.Equal(t, "application/json", res.Header.Get("Content-Type"))

			body, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			assert.JSONEq(t, `{"status":"ok","docker":"`+tt.wantDocker+`"}`, string(body))
		})
	}
}

// fakePinger is a hand-rolled dockerPinger: Ping returns pingErr and Endpoint
// returns endpoint, so the startup connectivity line can be exercised without a
// real docker daemon.
type fakePinger struct {
	pingErr  error
	endpoint string
}

func (f fakePinger) Ping(context.Context) error { return f.pingErr }
func (f fakePinger) Endpoint() string           { return f.endpoint }

func TestCheckDocker_ReachableNamesEndpoint(t *testing.T) {
	p := fakePinger{endpoint: "unix:///Users/dev/.docker/run/docker.sock"}

	got := checkDocker(t.Context(), p)

	assert.Contains(t, got, "reachable")
	assert.Contains(t, got, p.endpoint)
	assert.NotContains(t, got, "WARNING")
}

func TestCheckDocker_UnreachableWarnsButKeepsServing(t *testing.T) {
	// The runtime's error already carries the actionable detail; the daemon must
	// surface it as a warning and make clear it keeps serving.
	p := fakePinger{pingErr: errors.New("cannot reach the docker daemon at unix:///var/run/docker.sock — is Docker running?")}

	got := checkDocker(t.Context(), p)

	assert.Contains(t, got, "WARNING")
	assert.Contains(t, got, "is Docker running?")
	assert.Contains(t, got, "starting anyway")
}

// fakeFns is a hand-rolled functionExister: it reports whether a function
// exists via the error it returns from GetFunction.
type fakeFns struct {
	err error
}

func (f fakeFns) GetFunction(_ context.Context, name string) (*model.Function, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &model.Function{Name: name}, nil
}

// fakeContainers is a hand-rolled containerLister.
type fakeContainers struct {
	ids []string
}

func (f fakeContainers) FunctionContainerIDs(string) []string { return f.ids }

// fakeLogs is a hand-rolled logStreamer that serves canned per-id log content
// and records the (id, follow) pair of every call so tests can assert which
// containers were streamed and how.
type fakeLogs struct {
	byID map[string]string
	err  error

	gotIDs     []string
	gotFollows []bool
}

func (f *fakeLogs) Logs(_ context.Context, id string, follow bool) (io.ReadCloser, error) {
	f.gotIDs = append(f.gotIDs, id)
	f.gotFollows = append(f.gotFollows, follow)
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.byID[id])), nil
}

func serveLogs(t *testing.T, h *logsHandler, target string) *http.Response {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mini-lambda/functions/{name}/logs", h.handle)

	req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(t.Context())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Result()
}

func TestLogsHandler_FunctionNotFound(t *testing.T) {
	logs := &fakeLogs{}
	h := &logsHandler{
		fns:        fakeFns{err: store.ErrNotFound},
		containers: fakeContainers{ids: []string{"a"}},
		logs:       logs,
	}

	res := serveLogs(t, h, "/mini-lambda/functions/missing/logs")
	defer res.Body.Close()

	assert.Equal(t, http.StatusNotFound, res.StatusCode)
	assert.Empty(t, logs.gotIDs, "logs must not be streamed for a missing function")
}

func TestLogsHandler_NoContainers(t *testing.T) {
	logs := &fakeLogs{}
	h := &logsHandler{
		fns:        fakeFns{},
		containers: fakeContainers{ids: nil},
		logs:       logs,
	}

	res := serveLogs(t, h, "/mini-lambda/functions/fn/logs")
	defer res.Body.Close()

	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "text/plain; charset=utf-8", res.Header.Get("Content-Type"))

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	assert.Empty(t, body)
	assert.Empty(t, logs.gotIDs)
}

func TestLogsHandler_NoFollowConcatenatesAllContainers(t *testing.T) {
	logs := &fakeLogs{byID: map[string]string{"a": "AAA", "b": "BBB"}}
	h := &logsHandler{
		fns:        fakeFns{},
		containers: fakeContainers{ids: []string{"a", "b"}},
		logs:       logs,
	}

	res := serveLogs(t, h, "/mini-lambda/functions/fn/logs?follow=false")
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "AAABBB", string(body))
	assert.Equal(t, []string{"a", "b"}, logs.gotIDs)
	assert.Equal(t, []bool{false, false}, logs.gotFollows)
}

func TestLogsHandler_FollowStreamsFirstContainerOnly(t *testing.T) {
	logs := &fakeLogs{byID: map[string]string{"a": "AAA", "b": "BBB"}}
	h := &logsHandler{
		fns:        fakeFns{},
		containers: fakeContainers{ids: []string{"a", "b"}},
		logs:       logs,
	}

	res := serveLogs(t, h, "/mini-lambda/functions/fn/logs?follow=true")
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	assert.Equal(t, "AAA", string(body))
	assert.Equal(t, []string{"a"}, logs.gotIDs)
	assert.Equal(t, []bool{true}, logs.gotFollows)
}

func TestLogsHandler_YesIsNotFollow(t *testing.T) {
	// "yes" used to be truthy under isTrue; with strconv.ParseBool it is not, so
	// the request must fall through to the non-follow (concatenate-all) path.
	logs := &fakeLogs{byID: map[string]string{"a": "AAA", "b": "BBB"}}
	h := &logsHandler{
		fns:        fakeFns{},
		containers: fakeContainers{ids: []string{"a", "b"}},
		logs:       logs,
	}

	res := serveLogs(t, h, "/mini-lambda/functions/fn/logs?follow=yes")
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	assert.Equal(t, "AAABBB", string(body))
	assert.Equal(t, []bool{false, false}, logs.gotFollows)
}

func TestLogsHandler_LogStreamErrorIsReported(t *testing.T) {
	logs := &fakeLogs{err: errors.New("boom")}
	h := &logsHandler{
		fns:        fakeFns{},
		containers: fakeContainers{ids: []string{"a"}},
		logs:       logs,
	}

	res := serveLogs(t, h, "/mini-lambda/functions/fn/logs")
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)

	// The header is already 200 by the time the stream fails, so the error is
	// surfaced inline in the response body rather than as a status code.
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Contains(t, string(body), "mini-lambda: logs for a unavailable: boom")
}
