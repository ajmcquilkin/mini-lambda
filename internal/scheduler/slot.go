package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ajmcquilkin/mini-lambda/internal/runtimeapi"
)

// outcomeKind classifies how a single invocation finished.
type outcomeKind int

const (
	// outcomeResponse: the handler returned successfully; the slot stays warm.
	outcomeResponse outcomeKind = iota
	// outcomeError: the handler reported an error. The invocation surfaces the
	// error payload but the sandbox is kept warm, matching AWS.
	outcomeError
	// outcomeInitError: the runtime bootstrap failed. The slot is dead and must
	// be destroyed; the next invocation cold-starts.
	outcomeInitError
)

// outcome is the terminal result of one invocation delivered by the RIC.
type outcome struct {
	kind    outcomeKind
	payload []byte
}

// pending is a single in-flight invocation awaiting its RIC response.
type pending struct {
	inv    *runtimeapi.Invocation
	result chan outcome // buffered(1); receives exactly one outcome
}

// slot is one container serving one invocation at a time. It is the concrete
// runtimeapi.Slot: the Runtime API HTTP handlers drive it by token, while the
// engine owns its lifecycle. A slot alternates between idle (its RIC is blocked
// on the "next" long-poll) and busy (an invocation has been delivered).
type slot struct {
	token       string
	fnName      string
	containerID string

	// work hands the next invocation's payload/headers to the RIC's blocked
	// "next" poll. Buffered(1) so delivery never blocks on the RIC not yet
	// having looped back to poll.
	work chan *runtimeapi.Invocation

	mu      sync.Mutex
	current *pending // the invocation the RIC is currently working, if any

	done      chan struct{} // closed when the slot is destroyed
	closeOnce sync.Once

	// lastUsed is when the slot last returned to idle; read/written by the engine
	// under its own lock for idle reaping.
	lastUsed time.Time
}

// newSlot builds a slot ready to accept its first delivery.
func newSlot(token, fnName string) *slot {
	return &slot{
		token:  token,
		fnName: fnName,
		work:   make(chan *runtimeapi.Invocation, 1),
		done:   make(chan struct{}),
	}
}

// deliver hands p to the slot's RIC. It records p as the in-flight invocation
// (so a subsequent init-error before the first poll can still fail it) and
// queues the event for the "next" poll.
func (s *slot) deliver(p *pending) {
	s.mu.Lock()
	s.current = p
	s.mu.Unlock()
	s.work <- p.inv
}

// Next blocks until an invocation is ready, the slot is destroyed, or ctx ends.
func (s *slot) Next(ctx context.Context) (*runtimeapi.Invocation, error) {
	select {
	case inv := <-s.work:
		return inv, nil
	case <-s.done:
		return nil, runtimeapi.ErrSlotClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Respond delivers a successful handler response for requestID.
func (s *slot) Respond(requestID string, payload []byte) error {
	return s.complete(requestID, outcome{kind: outcomeResponse, payload: payload})
}

// InvocationError delivers a handler-reported error for requestID.
func (s *slot) InvocationError(requestID string, payload []byte) error {
	return s.complete(requestID, outcome{kind: outcomeError, payload: payload})
}

// InitError fails the current invocation (if any) with an init failure. The
// engine observing outcomeInitError destroys the slot.
func (s *slot) InitError(payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		// Init failed before any invocation was delivered; the engine's
		// per-invocation timeout will reclaim the slot.
		return nil
	}
	s.current.result <- outcome{kind: outcomeInitError, payload: payload}
	s.current = nil
	return nil
}

// complete matches requestID against the in-flight invocation and delivers o.
func (s *slot) complete(requestID string, o outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return errors.New("runtimeapi: no in-flight invocation")
	}
	if requestID != "" && requestID != s.current.inv.RequestID {
		return fmt.Errorf("runtimeapi: unknown request id %q", requestID)
	}
	s.current.result <- o // buffered(1): never blocks
	s.current = nil
	return nil
}

// close unblocks any goroutine parked in Next and marks the slot destroyed. It
// is safe to call multiple times.
func (s *slot) close() {
	s.closeOnce.Do(func() { close(s.done) })
}
