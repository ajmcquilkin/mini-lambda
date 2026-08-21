// Package apierror defines AWS-style error envelopes for the public HTTP API.
// Errors serialize to the JSON body {"__type":"<Code>","message":"<msg>"} and
// set the X-Amzn-Errortype header, matching the Lambda data-plane wire format.
package apierror

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ajmcquilkin/mini-lambda/internal/store"
)

// AWS error codes (the "__type" / X-Amzn-Errortype value).
const (
	CodeResourceConflict      = "ResourceConflictException"
	CodeResourceNotFound      = "ResourceNotFoundException"
	CodeTooManyRequests       = "TooManyRequestsException"
	CodeInvalidParameterValue = "InvalidParameterValueException"
	CodeService               = "ServiceException"
)

// Throttle reasons surfaced in the TooManyRequestsException body.
const (
	ReasonReservedConcurrencyLimitExceeded = "ReservedFunctionConcurrentInvocationLimitExceeded"
)

// Error is an AWS-style API error carrying an error code, HTTP status, and
// message. It implements the error interface and knows how to write itself as
// an HTTP response.
type Error struct {
	// Code is the AWS error code emitted as "__type" and X-Amzn-Errortype.
	Code string `json:"__type"`
	// Message is the human-readable error message.
	Message string `json:"message"`
	// Status is the HTTP status code (not serialized in the body).
	Status int `json:"-"`
	// Reason is an optional throttle reason (e.g. for TooManyRequestsException).
	Reason string `json:"Reason,omitempty"`

	// cause is the underlying error this envelope was derived from, if any. It
	// is exposed via Unwrap so errors.Is/As keep working through the mapping. It
	// is never serialized to the wire.
	cause error
}

// Error implements the error interface.
func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

// Unwrap returns the underlying cause, so errors.Is/As can see through an
// envelope produced by FromError to the sentinel it was mapped from.
func (e *Error) Unwrap() error {
	return e.cause
}

// WriteHTTP writes the error as an AWS-style JSON response, setting the
// Content-Type and X-Amzn-Errortype headers and the HTTP status.
func (e *Error) WriteHTTP(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("X-Amzn-Errortype", e.Code)
	w.WriteHeader(e.Status)
	// Best effort: the status/headers are already committed.
	_ = json.NewEncoder(w).Encode(e)
}

// Conflict returns a 409 ResourceConflictException (e.g. creating a function
// whose name already exists).
func Conflict(msg string) *Error {
	return &Error{Code: CodeResourceConflict, Message: msg, Status: http.StatusConflict}
}

// NotFound returns a 404 ResourceNotFoundException.
func NotFound(msg string) *Error {
	return &Error{Code: CodeResourceNotFound, Message: msg, Status: http.StatusNotFound}
}

// Throttled returns a 429 TooManyRequestsException carrying the reserved
// concurrency limit reason.
func Throttled(msg string) *Error {
	return &Error{
		Code:    CodeTooManyRequests,
		Message: msg,
		Status:  http.StatusTooManyRequests,
		Reason:  ReasonReservedConcurrencyLimitExceeded,
	}
}

// InvalidParameter returns a 400 InvalidParameterValueException.
func InvalidParameter(msg string) *Error {
	return &Error{Code: CodeInvalidParameterValue, Message: msg, Status: http.StatusBadRequest}
}

// Internal returns a 500 ServiceException.
func Internal(msg string) *Error {
	return &Error{Code: CodeService, Message: msg, Status: http.StatusInternalServerError}
}

// FromError maps an arbitrary error to its AWS-style envelope. It is the single
// source of truth for the error->wire mapping shared by the public API and the
// daemon:
//
//   - nil            -> nil
//   - *apierror.Error -> itself (via errors.As; already-shaped errors pass through)
//   - store.ErrNotFound -> NotFound (404 ResourceNotFoundException)
//   - store.ErrConflict -> Conflict (409 ResourceConflictException)
//   - anything else  -> Internal (500 ServiceException)
//
// For the sentinel and default cases the original error is carried as the
// envelope's cause (see Unwrap), so errors.Is/As keep working through the
// mapping. The rendered message preserves err.Error() to match prior behavior.
func FromError(err error) *Error {
	if err == nil {
		return nil
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return withCause(NotFound(err.Error()), err)
	case errors.Is(err, store.ErrConflict):
		return withCause(Conflict(err.Error()), err)
	default:
		return withCause(Internal(err.Error()), err)
	}
}

// withCause attaches the originating error to an envelope so Unwrap exposes it.
func withCause(e *Error, cause error) *Error {
	e.cause = cause
	return e
}
