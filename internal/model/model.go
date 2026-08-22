// Package model holds the shared domain types used across mini-lambda's
// store, runtime, scheduler, and public API layers. It has no dependencies
// beyond the standard library and google/uuid.
package model

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Function is the persisted configuration of a Lambda function. Name is the
// primary key.
type Function struct {
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Env        map[string]string `json:"env,omitempty"`
	MemoryMB   int               `json:"memoryMB"`
	TimeoutSec int               `json:"timeoutSec"`
	CreatedAt  time.Time         `json:"createdAt"`
	UpdatedAt  time.Time         `json:"updatedAt"`
}

// InvokeRequest is a request to invoke a function by name with a raw payload.
type InvokeRequest struct {
	FunctionName string `json:"functionName"`
	Payload      []byte `json:"payload"`
}

// InvokeResult is the outcome of an invocation. FunctionError is empty on
// success and carries the handler-reported error string otherwise.
type InvokeResult struct {
	Payload       []byte `json:"payload"`
	FunctionError string `json:"functionError,omitempty"`
	StatusCode    int    `json:"statusCode"`
}

// InvocationContext is the fabricated Lambda runtime metadata handed to a
// container for a single invocation.
type InvocationContext struct {
	// RequestID is a fresh UUID identifying this invocation.
	RequestID string
	// DeadlineMs is the invocation deadline as epoch milliseconds
	// (now + function timeout).
	DeadlineMs int64
	// InvokedFunctionArn is the synthetic local ARN for the function.
	InvokedFunctionArn string
	// TraceID mirrors X-Ray's _X_AMZN_TRACE_ID. It may be empty (no-op).
	TraceID string
}

// FunctionARN returns the synthetic local ARN for a function name.
func FunctionARN(name string) string {
	return fmt.Sprintf("arn:aws:lambda:local:000000000000:function:%s", name)
}

// DeadlineMs computes an invocation deadline (now + timeoutSec) as epoch
// milliseconds. It is pure so the deadline math can be tested exactly against an
// injected clock rather than a real time.Now() tolerance window.
func DeadlineMs(now time.Time, timeoutSec int) int64 {
	return now.Add(time.Duration(timeoutSec) * time.Second).UnixMilli()
}

// NewInvocationContextAt fabricates an InvocationContext for a single invocation
// of fn using the supplied clock, filling RequestID, DeadlineMs, and
// InvokedFunctionArn. TraceID is left empty. Callers with an injectable clock
// (e.g. the scheduler Engine) use this so the deadline honors that clock.
func NewInvocationContextAt(now time.Time, fn *Function) InvocationContext {
	return InvocationContext{
		RequestID:          uuid.NewString(),
		DeadlineMs:         DeadlineMs(now, fn.TimeoutSec),
		InvokedFunctionArn: FunctionARN(fn.Name),
	}
}

// NewInvocationContext is a thin wrapper over NewInvocationContextAt using the
// wall clock, for callers without their own clock.
func NewInvocationContext(fn *Function) InvocationContext {
	return NewInvocationContextAt(time.Now(), fn)
}
