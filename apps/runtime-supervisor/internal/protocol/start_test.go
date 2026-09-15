package protocol

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
)

func TestStartMutationSuccess(t *testing.T) {
	var calls int
	h := newStartTestHandler(t, func(_ context.Context, request lifecycle.StartRequest) (journal.Operation, error) {
		calls++
		if request.OperationID != "op-start" || request.ExpectedRuntimeIdentity != "runtime-01" || request.ExpectedRuntimeGeneration != 7 {
			t.Fatalf("request = %+v", request)
		}
		return journal.Operation{
			OperationID:       request.OperationID,
			OperationType:     "start",
			RuntimeIdentity:   "runtime-01",
			RuntimeGeneration: 7,
			State:             journal.StateSucceeded,
		}, nil
	})
	response := startRequestFor(t, h, testRuntimeToken, `{"operationId":"op-start","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7}`)
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
	var got operationResponse
	decodeResponse(t, response, &got)
	if got.OperationID != "op-start" || got.OperationType != "start" || got.RuntimeIdentity != "runtime-01" || got.RuntimeGeneration != 7 || got.State != journal.StateSucceeded || got.Error != nil {
		t.Fatalf("operation response = %+v", got)
	}
}

func TestStartMutationRequiresAuthBeforeExecutor(t *testing.T) {
	var calls int
	h := newStartTestHandler(t, func(context.Context, lifecycle.StartRequest) (journal.Operation, error) {
		calls++
		return journal.Operation{}, nil
	})
	response := startRequestFor(t, h, "wrong-token", `{}`)
	if response.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
	assertErrorCode(t, response, "unauthorized")
}

func TestStartMutationUnsupportedWhenExecutorMissing(t *testing.T) {
	h := newTestHandler(t)
	response := startRequestFor(t, h, testRuntimeToken, `{}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	assertErrorCode(t, response, "unsupported_operation")
}

func TestStartMutationStrictJSON(t *testing.T) {
	var calls int
	h := newStartTestHandler(t, func(context.Context, lifecycle.StartRequest) (journal.Operation, error) {
		calls++
		return journal.Operation{}, nil
	})
	for name, body := range map[string]string{
		"malformed":    `{"operationId":`,
		"unknown field": `{"operationId":"op","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7,"action":"shell"}`,
		"trailing":     `{"operationId":"op","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := startRequestFor(t, h, testRuntimeToken, body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertErrorCode(t, response, "invalid_request")
		})
	}
	if calls != 0 {
		t.Fatalf("strict JSON errors invoked Start %d times", calls)
	}
}

func TestStartMutationMapsStableErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "invalid", err: lifecycle.ErrInvalidRequest, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "identity", err: journal.ErrRuntimeIdentityMismatch, status: http.StatusConflict, code: "runtime_identity_mismatch"},
		{name: "generation", err: journal.ErrStaleRuntimeGeneration, status: http.StatusConflict, code: "stale_runtime_generation"},
		{name: "id conflict", err: journal.ErrOperationIDConflict, status: http.StatusConflict, code: "operation_id_conflict"},
		{name: "state", err: lifecycle.ErrOperationStateConflict, status: http.StatusConflict, code: "operation_state_conflict"},
		{name: "persistence", err: lifecycle.ErrPersistenceUnavailable, status: http.StatusServiceUnavailable, code: "operation_persistence_unavailable"},
		{name: "spawn", err: lifecycle.ErrProcessStartFailed, status: http.StatusInternalServerError, code: "internal_error"},
		{name: "result persistence", err: lifecycle.ErrResultPersistenceAfterStart, status: http.StatusInternalServerError, code: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newStartTestHandler(t, func(context.Context, lifecycle.StartRequest) (journal.Operation, error) {
				return journal.Operation{}, test.err
			})
			response := startRequestFor(t, h, testRuntimeToken, `{"operationId":"op","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7}`)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertErrorCode(t, response, test.code)
		})
	}
}

func TestStartMutationFailedReplayReturnsOperationEvidence(t *testing.T) {
	h := newStartTestHandler(t, func(context.Context, lifecycle.StartRequest) (journal.Operation, error) {
		return journal.Operation{
			OperationID:       "op-failed",
			OperationType:     "start",
			RuntimeIdentity:   "runtime-01",
			RuntimeGeneration: 3,
			State:             journal.StateFailed,
			FailureCode:       "internal_error",
		}, nil
	})
	response := startRequestFor(t, h, testRuntimeToken, `{"operationId":"op-failed","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var got operationResponse
	decodeResponse(t, response, &got)
	if got.State != journal.StateFailed || got.RuntimeGeneration != 3 || got.Error == nil || got.Error.Code != "internal_error" {
		t.Fatalf("operation response = %+v", got)
	}
}

func TestStartMutationMethodIsPost(t *testing.T) {
	h := newStartTestHandler(t, func(context.Context, lifecycle.StartRequest) (journal.Operation, error) {
		return journal.Operation{}, errors.New("must not run")
	})
	req := httptest.NewRequest(http.MethodGet, startPath, nil)
	req.Header.Set("Authorization", "Bearer "+testRuntimeToken)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("status=%d allow=%q", response.Code, response.Header().Get("Allow"))
	}
	assertErrorCode(t, response, "method_not_allowed")
}

func newStartTestHandler(t *testing.T, start StartExecutor) http.Handler {
	t.Helper()
	h, err := NewHandler(Config{
		RuntimeIdentity:   "runtime-01",
		RuntimeGeneration: 7,
		Token:             testRuntimeToken,
		Start:             start,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func startRequestFor(t *testing.T, h http.Handler, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, startPath, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	return response
}
