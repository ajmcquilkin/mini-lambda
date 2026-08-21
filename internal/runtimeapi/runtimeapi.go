// Package runtimeapi implements the AWS Lambda Runtime API surface that the
// in-container Runtime Interface Client (RIC) speaks to. It is exactly AWS's
// runtime API (the 2018-06-01 contract) with a per-slot token prefixed onto
// every path: one shared listener multiplexes all slots, and the token in the
// URL selects which slot's mailbox a request belongs to.
//
// A container's bootstrap builds request URLs by string-interpolating
// AWS_LAMBDA_RUNTIME_API into
// http://$AWS_LAMBDA_RUNTIME_API/2018-06-01/runtime/..., so injecting a value
// of "host:port/<token>" transparently carries the token through every call.
//
// This package owns only the HTTP contract; the scheduler owns slot lifecycle
// and implements the Registry/Slot seam defined here.
package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// ErrSlotClosed is returned by Slot.Next when the slot has been destroyed while
// a poll was blocked on it.
var ErrSlotClosed = errors.New("runtimeapi: slot closed")

// API version prefix used by the Lambda Runtime API.
const runtimeAPIVersion = "/2018-06-01/runtime"

// Runtime API response headers carried on the "next" response.
const (
	HeaderRequestID  = "Lambda-Runtime-Aws-Request-Id"
	HeaderDeadlineMs = "Lambda-Runtime-Deadline-Ms"
	HeaderARN        = "Lambda-Runtime-Invoked-Function-Arn"
	HeaderTraceID    = "Lambda-Runtime-Trace-Id"
)

// Invocation is one unit of work delivered to a slot's RIC via the "next"
// long-poll. Its fields map directly onto the Runtime API response headers and
// body.
type Invocation struct {
	RequestID          string
	DeadlineMs         int64
	InvokedFunctionArn string
	TraceID            string
	Payload            []byte
}

// Slot is the per-token mailbox the Runtime API talks to. The scheduler
// implements it; the HTTP handlers only ever touch a slot through this seam.
type Slot interface {
	// Next blocks until an invocation is ready for this slot, the context is
	// cancelled, or the slot is destroyed. On destruction it returns
	// ErrSlotClosed.
	Next(ctx context.Context) (*Invocation, error)

	// Respond delivers a successful handler response body for requestID.
	Respond(requestID string, payload []byte) error

	// InvocationError delivers a handler-reported error body for requestID.
	InvocationError(requestID string, payload []byte) error

	// InitError delivers a bootstrap/init failure body: the slot's current
	// invocation fails and the slot must be destroyed.
	InitError(payload []byte) error
}

// Registry resolves a per-slot token to its Slot mailbox.
type Registry interface {
	// LookupSlot returns the slot registered under token, or false.
	LookupSlot(token string) (Slot, bool)
}

// Server is the HTTP handler serving the token-prefixed Runtime API.
type Server struct {
	reg Registry
	mux *http.ServeMux
}

// New returns a Server that resolves tokens against reg.
func New(reg Registry) *Server {
	s := &Server{reg: reg, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /{token}"+runtimeAPIVersion+"/invocation/next", s.handleNext)
	s.mux.HandleFunc("POST /{token}"+runtimeAPIVersion+"/invocation/{requestId}/response", s.handleResponse)
	s.mux.HandleFunc("POST /{token}"+runtimeAPIVersion+"/invocation/{requestId}/error", s.handleInvocationError)
	s.mux.HandleFunc("POST /{token}"+runtimeAPIVersion+"/init/error", s.handleInitError)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// slotFor resolves the slot for the request's path token. On an unknown token
// it writes the 403 Runtime.UnknownSlot envelope and returns ok=false, so
// callers can simply `return` on a false result.
func (s *Server) slotFor(w http.ResponseWriter, r *http.Request) (Slot, bool) {
	slot, ok := s.reg.LookupSlot(r.PathValue("token"))
	if !ok {
		writeRuntimeError(w, http.StatusForbidden, "Runtime.UnknownSlot", "unknown runtime token")
		return nil, false
	}
	return slot, true
}

func (s *Server) handleNext(w http.ResponseWriter, r *http.Request) {
	slot, ok := s.slotFor(w, r)
	if !ok {
		return
	}

	inv, err := slot.Next(r.Context())
	if err != nil {
		// The slot went away (shutdown) or the caller's context ended; the RIC
		// treats a non-2xx as retryable. 500 is the AWS-documented signal that
		// the runtime should back off and retry the poll.
		writeRuntimeError(w, http.StatusInternalServerError, "Runtime.NextFailed", err.Error())
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set(HeaderRequestID, inv.RequestID)
	h.Set(HeaderDeadlineMs, strconv.FormatInt(inv.DeadlineMs, 10))
	h.Set(HeaderARN, inv.InvokedFunctionArn)
	if inv.TraceID != "" {
		h.Set(HeaderTraceID, inv.TraceID)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(inv.Payload)
}

func (s *Server) handleResponse(w http.ResponseWriter, r *http.Request) {
	slot, ok := s.slotFor(w, r)
	if !ok {
		return
	}
	body := readBody(w, r)
	if body == nil {
		return
	}
	if err := slot.Respond(r.PathValue("requestId"), body); err != nil {
		writeRuntimeError(w, http.StatusBadRequest, "Runtime.InvalidRequestID", err.Error())
		return
	}
	writeAccepted(w)
}

func (s *Server) handleInvocationError(w http.ResponseWriter, r *http.Request) {
	slot, ok := s.slotFor(w, r)
	if !ok {
		return
	}
	body := readBody(w, r)
	if body == nil {
		return
	}
	if err := slot.InvocationError(r.PathValue("requestId"), body); err != nil {
		writeRuntimeError(w, http.StatusBadRequest, "Runtime.InvalidRequestID", err.Error())
		return
	}
	writeAccepted(w)
}

func (s *Server) handleInitError(w http.ResponseWriter, r *http.Request) {
	slot, ok := s.slotFor(w, r)
	if !ok {
		return
	}
	body := readBody(w, r)
	if body == nil {
		return
	}
	_ = slot.InitError(body)
	writeAccepted(w)
}

// readBody reads the entire request body, writing a 400 and returning nil on
// failure. An empty body is valid and returns a non-nil empty slice.
func readBody(w http.ResponseWriter, r *http.Request) []byte {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		writeRuntimeError(w, http.StatusBadRequest, "Runtime.BodyReadError", err.Error())
		return nil
	}
	if b == nil {
		b = []byte{}
	}
	return b
}

// writeAccepted mirrors the Runtime API's 202 {"status":"OK"} acknowledgement.
func writeAccepted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"OK"}`))
}

// writeRuntimeError writes the Runtime API's error envelope
// {"errorMessage":...,"errorType":...}.
func writeRuntimeError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"errorMessage": msg,
		"errorType":    errType,
	})
}
