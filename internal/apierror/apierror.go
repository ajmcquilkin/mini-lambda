// Package apierror defines AWS-style error envelopes for the public HTTP API.
// Errors serialize to the JSON body {"__type":"<Code>","message":"<msg>"} and
// set the X-Amzn-Errortype header, matching the Lambda data-plane wire format.
package apierror

import (
	"encoding/json"
	"net/http"
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
}

// Error implements the error interface.
func (e *Error) Error() string {
	return e.Code + ": " + e.Message
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
