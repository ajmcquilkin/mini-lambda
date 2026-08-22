package docker

import (
	"errors"
	"fmt"
	"testing"

	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrapDockerErr(t *testing.T) {
	underlying := errors.New("no such container: abc123")
	notFound := errdefs.NotFound(underlying)
	// A real connection failure as produced by the docker client, so the
	// client.IsErrConnectionFailed predicate wrapDockerErr relies on matches.
	connFailed := client.ErrorConnectionFailed("unix:///var/run/docker.sock")

	host := "unix:///Users/dev/.docker/run/docker.sock"
	tried := []string{"DOCKER_HOST unset", "/var/run/docker.sock", "/Users/dev/.docker/run/docker.sock"}

	tests := []struct {
		name             string
		op               string
		in               error
		wantErrIs        []error // errors.Is must match all of these
		wantNotErrIs     []error // errors.Is must not match any of these
		wantMsgSubstrs   []string
		wantNoMsgSubstrs []string
	}{
		{
			name:           "maps docker not-found to ErrContainerNotFound",
			op:             `start container "abc123"`,
			in:             notFound,
			wantErrIs:      []error{ErrContainerNotFound, underlying},
			wantNotErrIs:   []error{ErrDockerUnavailable},
			wantMsgSubstrs: []string{`docker: start container "abc123"`},
		},
		{
			name:         "maps connection failure to ErrDockerUnavailable with host, probes and hint",
			op:           `inspect image "echo-python:latest"`,
			in:           connFailed,
			wantErrIs:    []error{ErrDockerUnavailable, connFailed},
			wantNotErrIs: []error{ErrContainerNotFound},
			wantMsgSubstrs: []string{
				"cannot reach the docker daemon",
				host,
				"is Docker running?",
				"/var/run/docker.sock",
				"/Users/dev/.docker/run/docker.sock",
			},
			// The clean message must not carry the inner op wrap prefix.
			wantNoMsgSubstrs: []string{"inspect image"},
		},
		{
			name:           "passes other errors through with op context",
			op:             `stop container "def456"`,
			in:             errors.New("boom"),
			wantNotErrIs:   []error{ErrContainerNotFound, ErrDockerUnavailable},
			wantMsgSubstrs: []string{"boom", `docker: stop container "def456"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapDockerErr(tt.op, host, tried, tt.in)
			require.Error(t, got)
			for _, s := range tt.wantMsgSubstrs {
				assert.Contains(t, got.Error(), s)
			}
			for _, s := range tt.wantNoMsgSubstrs {
				assert.NotContains(t, got.Error(), s)
			}
			for _, target := range tt.wantErrIs {
				assert.ErrorIs(t, got, target)
			}
			for _, target := range tt.wantNotErrIs {
				assert.NotErrorIs(t, got, target)
			}
		})
	}
}

// TestWrapDockerErrRecognizesWrappedNotFound guards the interaction between the
// docker errdefs constructor and the containerd-backed client.IsErrNotFound
// predicate wrapDockerErr relies on: a not-found error nested behind extra
// wrapping is still classified as not-found.
func TestWrapDockerErrRecognizesWrappedNotFound(t *testing.T) {
	wrapped := fmt.Errorf("api call: %w", errdefs.NotFound(errors.New("missing")))

	got := wrapDockerErr("remove container", "unix:///var/run/docker.sock", nil, wrapped)
	assert.ErrorIs(t, got, ErrContainerNotFound)
}
