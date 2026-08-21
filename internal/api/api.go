package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/ajmcquilkin/mini-lambda/internal/apierror"
	"github.com/ajmcquilkin/mini-lambda/internal/model"
	"github.com/ajmcquilkin/mini-lambda/internal/scheduler"
	"github.com/ajmcquilkin/mini-lambda/internal/store"
)

// Route path constants (the AWS Lambda data-plane API version prefix).
const (
	routeFunctions     = "/2015-03-31/functions"
	routeFunction      = "/2015-03-31/functions/{name}"
	routeConfiguration = "/2015-03-31/functions/{name}/configuration"
	routeInvocations   = "/2015-03-31/functions/{name}/invocations"
)

// server holds the collaborators the handlers close over.
type server struct {
	store store.Store
	sched scheduler.Scheduler
}

// New returns an http.Handler exposing the AWS-shaped public API backed by the
// given store and scheduler.
func New(st store.Store, sched scheduler.Scheduler) http.Handler {
	s := &server{store: st, sched: sched}

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+routeFunctions, s.createFunction)
	mux.HandleFunc("GET "+routeFunctions, s.listFunctions)
	mux.HandleFunc("GET "+routeFunction, s.getFunction)
	mux.HandleFunc("PUT "+routeConfiguration, s.updateFunctionConfiguration)
	mux.HandleFunc("DELETE "+routeFunction, s.deleteFunction)
	mux.HandleFunc("POST "+routeInvocations, s.invoke)
	return mux
}

func (s *server) createFunction(w http.ResponseWriter, r *http.Request) {
	var req CreateFunctionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierror.InvalidParameter("invalid request body: " + err.Error()).WriteHTTP(w)
		return
	}
	if req.FunctionName == "" {
		apierror.InvalidParameter("FunctionName is required").WriteHTTP(w)
		return
	}
	if req.Code.ImageUri == "" {
		apierror.InvalidParameter("Code.ImageUri is required").WriteHTTP(w)
		return
	}

	now := time.Now().UTC()
	fn := &model.Function{
		Name:       req.FunctionName,
		Image:      req.Code.ImageUri,
		MemoryMB:   orDefault(req.MemorySize, DefaultMemorySize),
		TimeoutSec: orDefault(req.Timeout, DefaultTimeout),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if req.Environment != nil && len(req.Environment.Variables) > 0 {
		fn.Env = req.Environment.Variables
	}

	if err := s.store.CreateFunction(r.Context(), fn); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, NewFunctionConfiguration(fn))
}

func (s *server) listFunctions(w http.ResponseWriter, r *http.Request) {
	fns, err := s.store.ListFunctions(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	resp := ListFunctionsResponse{Functions: make([]FunctionConfiguration, 0, len(fns))}
	for _, fn := range fns {
		resp.Functions = append(resp.Functions, NewFunctionConfiguration(fn))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) getFunction(w http.ResponseWriter, r *http.Request) {
	fn, err := s.store.GetFunction(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, NewFunctionConfiguration(fn))
}

func (s *server) updateFunctionConfiguration(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	var req UpdateFunctionConfigurationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierror.InvalidParameter("invalid request body: " + err.Error()).WriteHTTP(w)
		return
	}

	fn, err := s.store.GetFunction(r.Context(), name)
	if err != nil {
		writeError(w, err)
		return
	}

	if req.Code != nil && req.Code.ImageUri != "" {
		fn.Image = req.Code.ImageUri
	}
	if req.Environment != nil {
		fn.Env = req.Environment.Variables
	}
	if req.MemorySize != nil {
		fn.MemoryMB = *req.MemorySize
	}
	if req.Timeout != nil {
		fn.TimeoutSec = *req.Timeout
	}
	fn.UpdatedAt = time.Now().UTC()

	if err := s.store.UpdateFunctionConfiguration(r.Context(), fn); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, NewFunctionConfiguration(fn))
}

func (s *server) deleteFunction(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteFunction(r.Context(), r.PathValue("name")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) invoke(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		apierror.InvalidParameter("failed to read invocation payload: " + err.Error()).WriteHTTP(w)
		return
	}

	res, err := s.sched.Invoke(r.Context(), name, payload)
	if err != nil {
		// Scheduler throttles surface as *apierror.Error (429) and pass through
		// as their own envelope; an unknown function surfaces as ErrNotFound.
		writeError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if res.FunctionError != "" {
		// Mirror AWS: a handler-reported error is HTTP 200 with the raw error
		// payload and the X-Amz-Function-Error header set.
		w.Header().Set("X-Amz-Function-Error", "Unhandled")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.Payload)
}

// writeJSON serializes v as an application/json response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError maps an error to an AWS-style envelope. *apierror.Error values are
// written verbatim; store sentinels map to their canonical codes; anything else
// becomes a 500 ServiceException.
func writeError(w http.ResponseWriter, err error) {
	apierror.FromError(err).WriteHTTP(w)
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
