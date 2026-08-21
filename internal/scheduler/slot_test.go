package scheduler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ajmcquilkin/mini-lambda/internal/runtimeapi"
)

// newPending builds an in-flight invocation with a buffered result channel,
// matching how the engine constructs one before handing it to a slot.
func newPending(requestID string, payload []byte) *pending {
	return &pending{
		inv: &runtimeapi.Invocation{
			RequestID: requestID,
			Payload:   payload,
		},
		result: make(chan outcome, 1),
	}
}

func TestSlotCompleteRejectsMismatchedRequestID(t *testing.T) {
	s := newSlot("tok", "fn")
	p := newPending("req-1", []byte("event"))
	s.deliver(p)

	err := s.Respond("req-wrong", []byte("resp"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown request id")

	// The invocation stays in flight; the correct id still completes it.
	require.NoError(t, s.Respond("req-1", []byte("resp")))
	got := <-p.result
	assert.Equal(t, outcomeResponse, got.kind)
	assert.Equal(t, []byte("resp"), got.payload)
}

func TestSlotCompleteNoInFlight(t *testing.T) {
	s := newSlot("tok", "fn")
	// Nothing delivered yet: completing has no target invocation.
	err := s.Respond("req-1", []byte("resp"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no in-flight invocation")
}

func TestSlotInitErrorNoInFlight(t *testing.T) {
	s := newSlot("tok", "fn")
	// InitError before any delivery is a silent no-op (nil), not an error: the
	// engine's per-invocation timeout reclaims the slot instead.
	require.NoError(t, s.InitError([]byte("boot failed")))
}

func TestSlotInitErrorFailsInFlight(t *testing.T) {
	s := newSlot("tok", "fn")
	p := newPending("req-1", []byte("event"))
	s.deliver(p)

	require.NoError(t, s.InitError([]byte("boot failed")))
	got := <-p.result
	assert.Equal(t, outcomeInitError, got.kind)
	assert.Equal(t, []byte("boot failed"), got.payload)

	// The in-flight pointer is cleared, so a late completion has no target.
	err := s.Respond("req-1", []byte("resp"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no in-flight invocation")
}

func TestSlotNextAfterCloseReturnsClosedSentinel(t *testing.T) {
	s := newSlot("tok", "fn")
	s.close()

	inv, err := s.Next(t.Context())
	assert.Nil(t, inv)
	assert.ErrorIs(t, err, runtimeapi.ErrSlotClosed)

	// close is idempotent.
	assert.NotPanics(t, s.close)
}

func TestSlotNextRespectsContextCancellation(t *testing.T) {
	s := newSlot("tok", "fn")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	inv, err := s.Next(ctx)
	assert.Nil(t, inv)
	assert.ErrorIs(t, err, ctx.Err())
}

func TestSlotNormalHandoff(t *testing.T) {
	s := newSlot("tok", "fn")
	p := newPending("req-1", []byte("event-payload"))

	// A fake RIC: poll Next, echo the payload back as the response. Errors are
	// funneled through a channel so the test goroutine can assert on them
	// without sleeps.
	ricErr := make(chan error, 1)
	go func() {
		inv, err := s.Next(t.Context())
		if err != nil {
			ricErr <- err
			return
		}
		ricErr <- s.Respond(inv.RequestID, append([]byte("echo:"), inv.Payload...))
	}()

	// deliver queues the event for the RIC's blocked Next poll.
	s.deliver(p)

	require.NoError(t, <-ricErr)
	got := <-p.result
	assert.Equal(t, outcomeResponse, got.kind)
	assert.Equal(t, []byte("echo:event-payload"), got.payload)
}

func TestSlotInvocationErrorHandoff(t *testing.T) {
	s := newSlot("tok", "fn")
	p := newPending("req-1", []byte("event"))

	ricErr := make(chan error, 1)
	go func() {
		inv, err := s.Next(t.Context())
		if err != nil {
			ricErr <- err
			return
		}
		ricErr <- s.InvocationError(inv.RequestID, []byte(`{"errorMessage":"boom"}`))
	}()

	s.deliver(p)

	require.NoError(t, <-ricErr)
	got := <-p.result
	// A handler-reported error surfaces as outcomeError (slot stays warm).
	assert.Equal(t, outcomeError, got.kind)
	assert.Equal(t, []byte(`{"errorMessage":"boom"}`), got.payload)
}
