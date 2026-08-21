package runtimeapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSlot is a hand-rolled Slot for exercising the HTTP surface. The scheduler
// provides the real implementation; here we only need to observe what the
// handlers do.
type fakeSlot struct {
	inv         *Invocation
	respondReq  string
	respondBody []byte
	errReq      string
	errBody     []byte
	initBody    []byte
	failReq     bool // Respond/InvocationError should report a bad request id
}

func (f *fakeSlot) Next(context.Context) (*Invocation, error) { return f.inv, nil }

func (f *fakeSlot) Respond(requestID string, payload []byte) error {
	if f.failReq {
		return assertErr
	}
	f.respondReq, f.respondBody = requestID, payload
	return nil
}

func (f *fakeSlot) InvocationError(requestID string, payload []byte) error {
	f.errReq, f.errBody = requestID, payload
	return nil
}

func (f *fakeSlot) InitError(payload []byte) error {
	f.initBody = payload
	return nil
}

var assertErr = &badReq{}

type badReq struct{}

func (*badReq) Error() string { return "bad request id" }

type fakeRegistry map[string]Slot

func (r fakeRegistry) LookupSlot(token string) (Slot, bool) {
	s, ok := r[token]
	return s, ok
}

const token = "tok123"

func serve(t *testing.T, reg Registry, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, r)
	rec := httptest.NewRecorder()
	New(reg).ServeHTTP(rec, req)
	return rec
}

func TestNextPropagatesHeadersAndBody(t *testing.T) {
	slot := &fakeSlot{inv: &Invocation{
		RequestID:          "req-1",
		DeadlineMs:         1700000000000,
		InvokedFunctionArn: "arn:aws:lambda:local:000000000000:function:hello",
		TraceID:            "trace-xyz",
		Payload:            []byte(`{"event":true}`),
	}}
	reg := fakeRegistry{token: slot}

	rec := serve(t, reg, http.MethodGet, "/"+token+"/2018-06-01/runtime/invocation/next", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `{"event":true}`, rec.Body.String())
	assert.Equal(t, "req-1", rec.Header().Get(HeaderRequestID))
	assert.Equal(t, "1700000000000", rec.Header().Get(HeaderDeadlineMs))
	assert.Equal(t, "arn:aws:lambda:local:000000000000:function:hello", rec.Header().Get(HeaderARN))
	assert.Equal(t, "trace-xyz", rec.Header().Get(HeaderTraceID))
}

func TestNextOmitsEmptyTraceID(t *testing.T) {
	slot := &fakeSlot{inv: &Invocation{RequestID: "r", DeadlineMs: 1, InvokedFunctionArn: "arn"}}
	rec := serve(t, fakeRegistry{token: slot}, http.MethodGet, "/"+token+"/2018-06-01/runtime/invocation/next", "")
	require.Equal(t, http.StatusOK, rec.Code)
	_, ok := rec.Header()[HeaderTraceID]
	assert.False(t, ok, "empty trace id must not be sent")
}

func TestResponseAndErrorRouting(t *testing.T) {
	slot := &fakeSlot{}
	reg := fakeRegistry{token: slot}

	rec := serve(t, reg, http.MethodPost, "/"+token+"/2018-06-01/runtime/invocation/req-1/response", `{"ok":1}`)
	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "req-1", slot.respondReq)
	assert.Equal(t, `{"ok":1}`, string(slot.respondBody))

	rec = serve(t, reg, http.MethodPost, "/"+token+"/2018-06-01/runtime/invocation/req-2/error", `{"errorMessage":"x"}`)
	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "req-2", slot.errReq)
	assert.Equal(t, `{"errorMessage":"x"}`, string(slot.errBody))

	rec = serve(t, reg, http.MethodPost, "/"+token+"/2018-06-01/runtime/init/error", `{"errorMessage":"init"}`)
	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, `{"errorMessage":"init"}`, string(slot.initBody))
}

func TestUnknownTokenForbidden(t *testing.T) {
	rec := serve(t, fakeRegistry{}, http.MethodGet, "/nope/2018-06-01/runtime/invocation/next", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestUnknownTokenForbiddenAllRoutes asserts every runtime-API route funnels an
// unknown token through the shared slotFor prologue: identical 403 status,
// error envelope, and Content-Type.
func TestUnknownTokenForbiddenAllRoutes(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"next", http.MethodGet, "/nope/2018-06-01/runtime/invocation/next", ""},
		{"response", http.MethodPost, "/nope/2018-06-01/runtime/invocation/req-1/response", `{}`},
		{"invocationError", http.MethodPost, "/nope/2018-06-01/runtime/invocation/req-1/error", `{}`},
		{"initError", http.MethodPost, "/nope/2018-06-01/runtime/init/error", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, fakeRegistry{}, tc.method, tc.path, tc.body)
			require.Equal(t, http.StatusForbidden, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			assert.JSONEq(t, `{"errorMessage":"unknown runtime token","errorType":"Runtime.UnknownSlot"}`, rec.Body.String())
		})
	}
}

func TestBadRequestIDRejected(t *testing.T) {
	slot := &fakeSlot{failReq: true}
	rec := serve(t, fakeRegistry{token: slot}, http.MethodPost, "/"+token+"/2018-06-01/runtime/invocation/wrong/response", `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
