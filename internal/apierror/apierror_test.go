package apierror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ajmcquilkin/mini-lambda/internal/store"
)

func TestFromError(t *testing.T) {
	t.Run("nil maps to nil", func(t *testing.T) {
		assert.Nil(t, FromError(nil))
	})

	existing := Throttled("slow down")

	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantStatus int
		wantMsg    string
		wantSame   *Error // when set, FromError must return this exact pointer
		wantCause  error  // when set, errors.Is(result, wantCause) must hold
	}{
		{
			name:       "existing apierror passes through",
			err:        existing,
			wantCode:   CodeTooManyRequests,
			wantStatus: http.StatusTooManyRequests,
			wantMsg:    "slow down",
			wantSame:   existing,
		},
		{
			name:       "wrapped apierror passes through via errors.As",
			err:        fmt.Errorf("api: %w", existing),
			wantCode:   CodeTooManyRequests,
			wantStatus: http.StatusTooManyRequests,
			wantMsg:    "slow down",
			wantSame:   existing,
		},
		{
			name:       "store.ErrNotFound maps to NotFound",
			err:        store.ErrNotFound,
			wantCode:   CodeResourceNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    store.ErrNotFound.Error(),
			wantCause:  store.ErrNotFound,
		},
		{
			name:       "wrapped store.ErrNotFound maps to NotFound",
			err:        fmt.Errorf("get function %q: %w", "fn", store.ErrNotFound),
			wantCode:   CodeResourceNotFound,
			wantStatus: http.StatusNotFound,
			wantMsg:    `get function "fn": ` + store.ErrNotFound.Error(),
			wantCause:  store.ErrNotFound,
		},
		{
			name:       "store.ErrConflict maps to Conflict",
			err:        store.ErrConflict,
			wantCode:   CodeResourceConflict,
			wantStatus: http.StatusConflict,
			wantMsg:    store.ErrConflict.Error(),
			wantCause:  store.ErrConflict,
		},
		{
			name:       "wrapped store.ErrConflict maps to Conflict",
			err:        fmt.Errorf("create function: %w", store.ErrConflict),
			wantCode:   CodeResourceConflict,
			wantStatus: http.StatusConflict,
			wantMsg:    "create function: " + store.ErrConflict.Error(),
			wantCause:  store.ErrConflict,
		},
		{
			name:       "unknown error maps to Internal",
			err:        errors.New("boom"),
			wantCode:   CodeService,
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromError(tt.err)
			require.NotNil(t, got)
			assert.Equal(t, tt.wantCode, got.Code)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantMsg, got.Message)

			if tt.wantSame != nil {
				assert.Same(t, tt.wantSame, got, "existing envelope must pass through unchanged")
			}
			if tt.wantCause != nil {
				assert.ErrorIs(t, got, tt.wantCause, "mapping must remain unwrappable to the sentinel")
			}
		})
	}
}

func TestWriteHTTP(t *testing.T) {
	tests := []struct {
		name       string
		err        *Error
		wantStatus int
		wantType   string
		wantMsg    string
	}{
		{
			name:       "not found",
			err:        NotFound("missing"),
			wantStatus: http.StatusNotFound,
			wantType:   CodeResourceNotFound,
			wantMsg:    "missing",
		},
		{
			name:       "conflict",
			err:        Conflict("exists"),
			wantStatus: http.StatusConflict,
			wantType:   CodeResourceConflict,
			wantMsg:    "exists",
		},
		{
			name:       "internal",
			err:        Internal("boom"),
			wantStatus: http.StatusInternalServerError,
			wantType:   CodeService,
			wantMsg:    "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.err.WriteHTTP(rec)

			res := rec.Result()
			defer res.Body.Close()

			assert.Equal(t, tt.wantStatus, res.StatusCode)
			assert.Equal(t, tt.wantType, res.Header.Get("X-Amzn-Errortype"))
			assert.Equal(t, "application/x-amz-json-1.1", res.Header.Get("Content-Type"))

			var body struct {
				Type    string `json:"__type"`
				Message string `json:"message"`
			}
			require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
			assert.Equal(t, tt.wantType, body.Type)
			assert.Equal(t, tt.wantMsg, body.Message)
		})
	}
}
