package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
)

type observeUpdateFunc func(context.Context, lifecycle.ObserveUpdateOperationRequest) (journal.Operation, error)

func (f observeUpdateFunc) ObserveUpdateOperation(ctx context.Context, request lifecycle.ObserveUpdateOperationRequest) (journal.Operation, error) {
	return f(ctx, request)
}

func TestObserveUpdateOperationAuthenticatesAndReturnsOnlyDurableEvidence(t *testing.T) {
	created := time.Date(2026, 9, 18, 1, 2, 3, 0, time.UTC)
	updated := created.Add(time.Second)
	completed := updated
	called := 0
	h := observeUpdateHandler(t, observeUpdateFunc(func(_ context.Context, request lifecycle.ObserveUpdateOperationRequest) (journal.Operation, error) {
		called++
		if request.OperationID != "manager-op" || request.ExpectedRuntimeIdentity != "runtime-01" ||
			request.ExpectedRuntimeGeneration != 7 || request.OperationType != lifecycle.UpdateOperationPrepare ||
			request.TargetVersion != "7.3.4" {
			t.Fatalf("request = %+v", request)
		}
		return journal.Operation{
			OperationID: "manager-op", OperationType: lifecycle.UpdateOperationPrepare,
			RuntimeIdentity: "runtime-01", RuntimeGeneration: 6, State: journal.StateFailed,
			FailureCode: "staging_failed", CreatedAt: created, UpdatedAt: updated, CompletedAt: &completed,
		}, nil
	}))

	unauthorized := httptest.NewRequest(http.MethodGet, validObserveUpdateTarget(), nil)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized || called != 0 {
		t.Fatalf("unauthorized response=%d called=%d", response.Code, called)
	}

	response = submitObserveUpdate(t, h, validObserveUpdateTarget(), nil)
	if response.Code != http.StatusOK || called != 1 {
		t.Fatalf("response=%d body=%s called=%d", response.Code, response.Body.String(), called)
	}
	assertJSONHeaders(t, response)
	var got observedOperationResponse
	decodeResponse(t, response, &got)
	if got.OperationID != "manager-op" || got.OperationType != lifecycle.UpdateOperationPrepare ||
		got.RuntimeGeneration != 6 || got.State != journal.StateFailed || got.Error == nil ||
		got.Error.Code != "staging_failed" || !got.CreatedAt.Equal(created) || !got.UpdatedAt.Equal(updated) ||
		got.CompletedAt == nil || !got.CompletedAt.Equal(completed) {
		t.Fatalf("response = %+v", got)
	}
	for _, forbidden := range []string{"requestFingerprint", "TombstonedAt", testRuntimeToken, "/runtime/"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestObserveUpdateOperationStrictQueryAndEmptyBody(t *testing.T) {
	h := observeUpdateHandler(t, observeUpdateFunc(func(context.Context, lifecycle.ObserveUpdateOperationRequest) (journal.Operation, error) {
		t.Fatal("invalid observation reached journal")
		return journal.Operation{}, nil
	}))
	valid := validObserveUpdateTarget()
	tests := map[string]struct {
		target string
		body   *strings.Reader
	}{
		"missing":         {target: strings.Replace(valid, "&targetVersion=7.3.4", "", 1)},
		"unknown":         {target: valid + "&rawOperationId=forbidden"},
		"duplicate":       {target: valid + "&targetVersion=7.3.4"},
		"wrong phase":     {target: strings.Replace(valid, "prepare_update", "restart", 1)},
		"version alias":   {target: strings.Replace(valid, "7.3.4", "v7.3.4", 1)},
		"zero generation": {target: strings.Replace(valid, "expectedRuntimeGeneration=7", "expectedRuntimeGeneration=0", 1)},
		"malformed query": {target: observeUpdateOperationPath + "?operationId=%zz"},
		"non-empty body":  {target: valid, body: strings.NewReader("{}")},
		"raw token query": {target: valid + "&token=" + testRuntimeToken},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			response := submitObserveUpdate(t, h, test.target, test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertErrorCode(t, response, "invalid_request")
			if strings.Contains(response.Body.String(), testRuntimeToken) {
				t.Fatal("response leaked token")
			}
		})
	}
}

func TestObserveUpdateOperationMapsStableErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{journal.ErrOperationNotFound, http.StatusNotFound, "operation_not_found"},
		{journal.ErrOperationIDConflict, http.StatusConflict, "operation_id_conflict"},
		{journal.ErrRuntimeIdentityMismatch, http.StatusConflict, "runtime_identity_mismatch"},
		{journal.ErrStaleRuntimeGeneration, http.StatusConflict, "stale_runtime_generation"},
		{lifecycle.ErrPersistenceUnavailable, http.StatusServiceUnavailable, "operation_persistence_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			h := observeUpdateHandler(t, observeUpdateFunc(func(context.Context, lifecycle.ObserveUpdateOperationRequest) (journal.Operation, error) {
				return journal.Operation{}, test.err
			}))
			response := submitObserveUpdate(t, h, validObserveUpdateTarget(), nil)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertErrorCode(t, response, test.code)
		})
	}

	h := observeUpdateHandler(t, observeUpdateFunc(func(context.Context, lifecycle.ObserveUpdateOperationRequest) (journal.Operation, error) {
		return journal.Operation{}, errors.New("sqlite path /private/runtime token=secret")
	}))
	response := submitObserveUpdate(t, h, validObserveUpdateTarget(), nil)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "sqlite") || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("internal response leaked details: %d %s", response.Code, response.Body.String())
	}
}

func TestObserveUpdateOperationCapabilityIsAdditive(t *testing.T) {
	h := observeUpdateHandler(t, observeUpdateFunc(func(context.Context, lifecycle.ObserveUpdateOperationRequest) (journal.Operation, error) {
		return journal.Operation{}, journal.ErrOperationNotFound
	}))
	response := request(t, h, http.MethodGet, handshakePath, testRuntimeToken)
	var got handshakeResponse
	decodeResponse(t, response, &got)
	if !reflect.DeepEqual(got.Capabilities, []string{CapabilityObserveUpdateOperation}) {
		t.Fatalf("capabilities = %v", got.Capabilities)
	}
}

func observeUpdateHandler(t *testing.T, observer UpdateOperationObserver) http.Handler {
	t.Helper()
	h, err := NewHandler(Config{
		RuntimeIdentity: "runtime-01", RuntimeGeneration: 7, Token: testRuntimeToken, ObserveUpdate: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func validObserveUpdateTarget() string {
	query := url.Values{
		"operationId":               {"manager-op"},
		"expectedRuntimeIdentity":   {"runtime-01"},
		"expectedRuntimeGeneration": {"7"},
		"operationType":             {lifecycle.UpdateOperationPrepare},
		"targetVersion":             {"7.3.4"},
	}
	return observeUpdateOperationPath + "?" + query.Encode()
}

func submitObserveUpdate(t *testing.T, handler http.Handler, target string, body *strings.Reader) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(http.MethodGet, target, nil)
	} else {
		request = httptest.NewRequest(http.MethodGet, target, body)
	}
	request.Header.Set("Authorization", "Bearer "+testRuntimeToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type prepareProgressFunc func(string) (lifecycle.PrepareProgress, bool)

func (f prepareProgressFunc) ObservePrepareProgress(operationID string) (lifecycle.PrepareProgress, bool) {
	return f(operationID)
}

func TestObserveUpdateOperationIncludesTransientPrepareProgressWhileRunning(t *testing.T) {
	h, err := NewHandler(Config{
		RuntimeIdentity:   "runtime-01",
		RuntimeGeneration: 7,
		Token:             testRuntimeToken,
		ObserveUpdate: observeUpdateFunc(func(context.Context, lifecycle.ObserveUpdateOperationRequest) (journal.Operation, error) {
			return journal.Operation{
				OperationID: "manager-op", OperationType: lifecycle.UpdateOperationPrepare,
				RuntimeIdentity: "runtime-01", RuntimeGeneration: 7, State: journal.StateRunning,
			}, nil
		}),
		ObservePrepareProgress: prepareProgressFunc(func(id string) (lifecycle.PrepareProgress, bool) {
			if id != "manager-op" {
				return lifecycle.PrepareProgress{}, false
			}
			return lifecycle.PrepareProgress{Phase: "downloading", ReceivedBytes: 512, TotalBytes: 1024}, true
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := submitObserveUpdate(t, h, validObserveUpdateTarget(), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var got observedOperationResponse
	decodeResponse(t, response, &got)
	if got.Phase != "downloading" || got.ReceivedBytes != 512 || got.TotalBytes != 1024 {
		t.Fatalf("progress response = %+v", got)
	}
}

func TestObserveUpdateOperationOmitsTransientProgressForTerminalStatesAndActivateUpdate(t *testing.T) {
	created := time.Date(2026, 9, 18, 2, 3, 4, 0, time.UTC)
	updated := created.Add(time.Second)
	completed := updated
	tests := []struct {
		name          string
		operationType string
		state         journal.State
		failureCode   string
		completedAt   *time.Time
	}{
		{
			name:          "prepare_update succeeded",
			operationType: lifecycle.UpdateOperationPrepare,
			state:         journal.StateSucceeded,
			completedAt:   &completed,
		},
		{
			name:          "prepare_update failed",
			operationType: lifecycle.UpdateOperationPrepare,
			state:         journal.StateFailed,
			failureCode:   "staging_failed",
			completedAt:   &completed,
		},
		{
			name:          "activate_update accepted",
			operationType: lifecycle.UpdateOperationActivate,
			state:         journal.StateAccepted,
		},
		{
			name:          "activate_update running",
			operationType: lifecycle.UpdateOperationActivate,
			state:         journal.StateRunning,
		},
		{
			name:          "activate_update succeeded",
			operationType: lifecycle.UpdateOperationActivate,
			state:         journal.StateSucceeded,
			completedAt:   &completed,
		},
		{
			name:          "activate_update failed",
			operationType: lifecycle.UpdateOperationActivate,
			state:         journal.StateFailed,
			failureCode:   "activation_failed",
			completedAt:   &completed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, err := NewHandler(Config{
				RuntimeIdentity:   "runtime-01",
				RuntimeGeneration: 7,
				Token:             testRuntimeToken,
				ObserveUpdate: observeUpdateFunc(func(_ context.Context, request lifecycle.ObserveUpdateOperationRequest) (journal.Operation, error) {
					if request.OperationType != tc.operationType {
						t.Fatalf("request.OperationType = %q, want %q", request.OperationType, tc.operationType)
					}
					return journal.Operation{
						OperationID:       "manager-op",
						OperationType:     tc.operationType,
						RuntimeIdentity:   "runtime-01",
						RuntimeGeneration: 7,
						State:             tc.state,
						FailureCode:       tc.failureCode,
						CreatedAt:         created,
						UpdatedAt:         updated,
						CompletedAt:       tc.completedAt,
					}, nil
				}),
				ObservePrepareProgress: prepareProgressFunc(func(id string) (lifecycle.PrepareProgress, bool) {
					if id != "manager-op" {
						return lifecycle.PrepareProgress{}, false
					}
					return lifecycle.PrepareProgress{
						Phase:         "downloading",
						ReceivedBytes: 512,
						TotalBytes:    1024,
					}, true
				}),
			})
			if err != nil {
				t.Fatal(err)
			}

			query := url.Values{
				"operationId":               {"manager-op"},
				"expectedRuntimeIdentity":   {"runtime-01"},
				"expectedRuntimeGeneration": {"7"},
				"operationType":             {tc.operationType},
				"targetVersion":             {"7.3.4"},
			}
			response := submitObserveUpdate(t, h, observeUpdateOperationPath+"?"+query.Encode(), nil)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertJSONHeaders(t, response)

			rawBody := response.Body.String()
			var raw map[string]any
			if err := json.Unmarshal([]byte(rawBody), &raw); err != nil {
				t.Fatalf("unmarshal raw response: %v", err)
			}
			for _, omittedKey := range []string{"phase", "receivedBytes", "totalBytes"} {
				if _, exists := raw[omittedKey]; exists {
					t.Fatalf("response included transient field %q: %s", omittedKey, rawBody)
				}
			}

			var got observedOperationResponse
			decodeResponse(t, response, &got)
			if got.OperationID != "manager-op" || got.OperationType != tc.operationType || got.State != tc.state {
				t.Fatalf("response = %+v", got)
			}
			if got.Phase != "" || got.ReceivedBytes != 0 || got.TotalBytes != 0 {
				t.Fatalf("transient progress leaked into decoded response: %+v", got)
			}
			if tc.failureCode == "" && got.Error != nil {
				t.Fatalf("unexpected error in response: %+v", got.Error)
			}
			if tc.failureCode != "" && (got.Error == nil || got.Error.Code != tc.failureCode) {
				t.Fatalf("error = %+v, want code %q", got.Error, tc.failureCode)
			}
		})
	}
}
