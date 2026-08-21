package docker

import (
	"errors"
	"fmt"
	"testing"

	"github.com/docker/docker/errdefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWrapNotFound(t *testing.T) {
	underlying := errors.New("no such container: abc123")
	notFound := errdefs.NotFound(underlying)

	tests := []struct {
		name          string
		op            string
		in            error
		wantErrIs     []error // errors.Is must match all of these
		wantNotErrIs  []error // errors.Is must not match any of these
		wantMsgSubstr string
	}{
		{
			name:          "maps docker not-found to ErrContainerNotFound",
			op:            `start container "abc123"`,
			in:            notFound,
			wantErrIs:     []error{ErrContainerNotFound, underlying},
			wantMsgSubstr: `docker: start container "abc123"`,
		},
		{
			name:          "passes other errors through untouched",
			op:            `stop container "def456"`,
			in:            errors.New("boom"),
			wantErrIs:     []error{},
			wantNotErrIs:  []error{ErrContainerNotFound},
			wantMsgSubstr: "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapNotFound(tt.op, tt.in)
			require.Error(t, got)
			assert.Contains(t, got.Error(), tt.wantMsgSubstr)
			for _, target := range tt.wantErrIs {
				assert.ErrorIs(t, got, target)
			}
			for _, target := range tt.wantNotErrIs {
				assert.NotErrorIs(t, got, target)
			}
		})
	}
}

// TestWrapNotFoundRecognizesWrappedNotFound guards the interaction between the
// docker errdefs constructor and the containerd-backed client.IsErrNotFound
// predicate wrapNotFound relies on: a not-found error nested behind extra
// wrapping is still classified as not-found.
func TestWrapNotFoundRecognizesWrappedNotFound(t *testing.T) {
	wrapped := fmt.Errorf("api call: %w", errdefs.NotFound(errors.New("missing")))

	got := wrapNotFound("remove container", wrapped)
	assert.ErrorIs(t, got, ErrContainerNotFound)
}
