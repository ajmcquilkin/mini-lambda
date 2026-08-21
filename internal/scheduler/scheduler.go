// Package scheduler defines the invocation scheduler interface. It acquires a
// concurrency slot, drives the runtime to execute an invocation, and enforces
// capacity limits. This package is interface-only; a concrete implementation is
// provided in a later round.
package scheduler

import (
	"context"

	"github.com/ajmcquilkin/mini-lambda/internal/model"
)

//go:generate mockgen -destination=schedulermock/mock_scheduler.go -package=schedulermock github.com/ajmcquilkin/mini-lambda/internal/scheduler Scheduler

// Scheduler executes function invocations under a bounded concurrency limit.
type Scheduler interface {
	// Invoke acquires a slot and runs fnName with payload, returning the result.
	// When at capacity it returns an *apierror.Error throttle
	// (TooManyRequestsException).
	Invoke(ctx context.Context, fnName string, payload []byte) (*model.InvokeResult, error)

	// Shutdown drains in-flight invocations and releases resources.
	Shutdown(ctx context.Context) error
}
