package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/ajmcquilkin/mini-lambda/internal/apierror"
	"github.com/ajmcquilkin/mini-lambda/internal/model"
	"github.com/ajmcquilkin/mini-lambda/internal/scheduler/schedulermock"
	"github.com/ajmcquilkin/mini-lambda/internal/store"
	"github.com/ajmcquilkin/mini-lambda/internal/store/storemock"
)

func newAPI(t *testing.T) (http.Handler, *storemock.MockStore, *schedulermock.MockScheduler) {
	t.Helper()
	ctrl := gomock.NewController(t)
	st := storemock.NewMockStore(ctrl)
	sched := schedulermock.NewMockScheduler(ctrl)
	return New(st, sched), st, sched
}

// do issues a request carrying the test's context and returns the recorder.
func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, r)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) apierror.Error {
	t.Helper()
	var e apierror.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))
	return e
}

func TestCreateFunction(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		expect     func(st *storemock.MockStore)
		wantStatus int
		wantCode   string
		check      func(t *testing.T, cfg FunctionConfiguration)
	}{
		{
			name: "success",
			body: `{"FunctionName":"hello","Code":{"ImageUri":"img:latest"},"Environment":{"Variables":{"A":"1"}},"MemorySize":256,"Timeout":15}`,
			expect: func(st *storemock.MockStore) {
				st.EXPECT().CreateFunction(gomock.Any(), gomock.Any()).Return(nil)
			},
			wantStatus: http.StatusCreated,
			check: func(t *testing.T, cfg FunctionConfiguration) {
				assert.Equal(t, "hello", cfg.FunctionName)
				assert.Equal(t, "img:latest", cfg.Code.ImageUri)
				assert.Equal(t, 256, cfg.MemorySize)
				assert.Equal(t, 15, cfg.Timeout)
				require.NotNil(t, cfg.Environment)
				assert.Equal(t, "1", cfg.Environment.Variables["A"])
				assert.NotEmpty(t, cfg.FunctionArn)
			},
		},
		{
			name: "defaults applied",
			body: `{"FunctionName":"h","Code":{"ImageUri":"img"}}`,
			expect: func(st *storemock.MockStore) {
				st.EXPECT().CreateFunction(gomock.Any(), gomock.Any()).Return(nil)
			},
			wantStatus: http.StatusCreated,
			check: func(t *testing.T, cfg FunctionConfiguration) {
				assert.Equal(t, DefaultMemorySize, cfg.MemorySize)
				assert.Equal(t, DefaultTimeout, cfg.Timeout)
			},
		},
		{
			name:       "missing name",
			body:       `{"Code":{"ImageUri":"img"}}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   apierror.CodeInvalidParameterValue,
		},
		{
			name:       "missing image",
			body:       `{"FunctionName":"h"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   apierror.CodeInvalidParameterValue,
		},
		{
			name: "conflict",
			body: `{"FunctionName":"dup","Code":{"ImageUri":"img"}}`,
			expect: func(st *storemock.MockStore) {
				st.EXPECT().CreateFunction(gomock.Any(), gomock.Any()).Return(store.ErrConflict)
			},
			wantStatus: http.StatusConflict,
			wantCode:   apierror.CodeResourceConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, st, _ := newAPI(t)
			if tt.expect != nil {
				tt.expect(st)
			}

			rec := do(t, h, http.MethodPost, routeFunctions, tt.body)
			require.Equal(t, tt.wantStatus, rec.Code, "body=%s", rec.Body.String())

			if tt.wantCode != "" {
				e := decodeError(t, rec)
				assert.Equal(t, tt.wantCode, e.Code)
				assert.Equal(t, tt.wantCode, rec.Header().Get("X-Amzn-Errortype"))
				return
			}
			var cfg FunctionConfiguration
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cfg))
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestGetFunction(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		h, st, _ := newAPI(t)
		st.EXPECT().GetFunction(gomock.Any(), "hello").Return(&model.Function{Name: "hello", Image: "img", MemoryMB: 128, TimeoutSec: 5}, nil)

		rec := do(t, h, http.MethodGet, routeFunctions+"/hello", "")
		require.Equal(t, http.StatusOK, rec.Code)
		var cfg FunctionConfiguration
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cfg))
		assert.Equal(t, "hello", cfg.FunctionName)
	})

	t.Run("not found", func(t *testing.T) {
		h, st, _ := newAPI(t)
		st.EXPECT().GetFunction(gomock.Any(), "missing").Return(nil, store.ErrNotFound)

		rec := do(t, h, http.MethodGet, routeFunctions+"/missing", "")
		require.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, apierror.CodeResourceNotFound, decodeError(t, rec).Code)
	})
}

func TestListFunctions(t *testing.T) {
	h, st, _ := newAPI(t)
	st.EXPECT().ListFunctions(gomock.Any()).Return([]*model.Function{
		{Name: "a", Image: "img"},
		{Name: "b", Image: "img"},
	}, nil)

	rec := do(t, h, http.MethodGet, routeFunctions, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var resp ListFunctionsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Len(t, resp.Functions, 2)
}

func TestUpdateFunctionConfiguration(t *testing.T) {
	t.Run("partial update", func(t *testing.T) {
		h, st, _ := newAPI(t)
		gomock.InOrder(
			st.EXPECT().GetFunction(gomock.Any(), "c").
				Return(&model.Function{Name: "c", Image: "img", MemoryMB: 128, TimeoutSec: 5}, nil),
			st.EXPECT().UpdateFunctionConfiguration(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, fn *model.Function) error {
					assert.Equal(t, 1024, fn.MemoryMB)
					assert.Equal(t, 5, fn.TimeoutSec) // unchanged
					assert.Equal(t, "V", fn.Env["K"])
					return nil
				}),
		)

		rec := do(t, h, http.MethodPut, routeFunctions+"/c/configuration", `{"MemorySize":1024,"Environment":{"Variables":{"K":"V"}}}`)
		require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
		var cfg FunctionConfiguration
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cfg))
		assert.Equal(t, 1024, cfg.MemorySize)
		assert.Equal(t, 5, cfg.Timeout)
	})

	t.Run("not found", func(t *testing.T) {
		h, st, _ := newAPI(t)
		st.EXPECT().GetFunction(gomock.Any(), "nope").Return(nil, store.ErrNotFound)

		rec := do(t, h, http.MethodPut, routeFunctions+"/nope/configuration", `{"MemorySize":10}`)
		require.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestDeleteFunction(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h, st, _ := newAPI(t)
		st.EXPECT().DeleteFunction(gomock.Any(), "d").Return(nil)

		rec := do(t, h, http.MethodDelete, routeFunctions+"/d", "")
		require.Equal(t, http.StatusNoContent, rec.Code)
		assert.Zero(t, rec.Body.Len())
	})

	t.Run("not found", func(t *testing.T) {
		h, st, _ := newAPI(t)
		st.EXPECT().DeleteFunction(gomock.Any(), "d").Return(store.ErrNotFound)

		rec := do(t, h, http.MethodDelete, routeFunctions+"/d", "")
		require.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestInvoke(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h, _, sched := newAPI(t)
		sched.EXPECT().Invoke(gomock.Any(), "fn", []byte(`{"x":1}`)).
			Return(&model.InvokeResult{Payload: []byte(`{"ok":true}`), StatusCode: 200}, nil)

		rec := do(t, h, http.MethodPost, routeFunctions+"/fn/invocations", `{"x":1}`)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, `{"ok":true}`, rec.Body.String())
		assert.Empty(t, rec.Header().Get("X-Amz-Function-Error"))
	})

	t.Run("handler error", func(t *testing.T) {
		h, _, sched := newAPI(t)
		sched.EXPECT().Invoke(gomock.Any(), "fn", gomock.Any()).
			Return(&model.InvokeResult{Payload: []byte(`{"errorMessage":"boom"}`), FunctionError: "boom", StatusCode: 200}, nil)

		rec := do(t, h, http.MethodPost, routeFunctions+"/fn/invocations", `{}`)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "Unhandled", rec.Header().Get("X-Amz-Function-Error"))
		assert.Equal(t, `{"errorMessage":"boom"}`, rec.Body.String())
	})

	t.Run("throttle", func(t *testing.T) {
		h, _, sched := newAPI(t)
		sched.EXPECT().Invoke(gomock.Any(), "fn", gomock.Any()).Return(nil, apierror.Throttled("at capacity"))

		rec := do(t, h, http.MethodPost, routeFunctions+"/fn/invocations", `{}`)
		require.Equal(t, http.StatusTooManyRequests, rec.Code)
		assert.Equal(t, apierror.CodeTooManyRequests, decodeError(t, rec).Code)
	})

	t.Run("unknown function", func(t *testing.T) {
		h, _, sched := newAPI(t)
		sched.EXPECT().Invoke(gomock.Any(), "ghost", gomock.Any()).Return(nil, store.ErrNotFound)

		rec := do(t, h, http.MethodPost, routeFunctions+"/ghost/invocations", `{}`)
		require.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, apierror.CodeResourceNotFound, decodeError(t, rec).Code)
	})
}
