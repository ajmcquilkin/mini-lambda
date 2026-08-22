package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
