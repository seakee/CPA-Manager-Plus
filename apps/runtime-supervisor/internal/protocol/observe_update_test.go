package protocol

import (
	"context"
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
