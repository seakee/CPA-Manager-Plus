package protocol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
)

const validStartBody = `{"operationId":"op","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7}`

func TestFreshStartExactArtifactEnvelopeIsStrictAndStartOnly(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	h := startHandler(t, startFunc(func(_ context.Context, request lifecycle.StartRequest) (journal.Operation, error) {
		if string(request.ExpectedActiveArtifactID) != id {
			t.Fatalf("artifact fence lost at protocol: %+v", request)
		}
		return journal.Operation{OperationID: request.OperationID}, nil
	}))
	body := strings.TrimSuffix(validStartBody, "}") + `,"expectedActiveArtifactId":"` + id + `"}`
	if response := submitStart(t, h, body); response.Code != http.StatusOK {
		t.Fatalf("artifact-bound Start rejected: %s", response.Body.String())
	}
	for name, invalid := range map[string]string{
		"invalid artifact":   strings.Replace(body, id, "version-8", 1),
		"null artifact":      strings.Replace(body, `"`+id+`"`, "null", 1),
		"case alias":         strings.Replace(body, "expectedActiveArtifactId", "ExpectedActiveArtifactId", 1),
		"duplicate artifact": strings.TrimSuffix(body, "}") + `,"expectedActiveArtifactId":"` + id + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := submitStart(t, h, invalid)
			if response.Code != 400 {
				t.Fatalf("accepted invalid artifact: %s", response.Body.String())
			}
			assertErrorCode(t, response, "invalid_request")
		})
	}
	for _, path := range []string{stopPath, restartPath} {
		for _, value := range []string{`"` + id + `"`, `""`} {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(strings.TrimSuffix(validStartBody, "}")+`,"expectedActiveArtifactId":`+value+`}`))
			request.Header.Set("Authorization", "Bearer "+testRuntimeToken)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != 400 {
				t.Fatalf("%s accepted Start payload", path)
			}
			assertErrorCode(t, response, "invalid_request")
		}
	}
}

func TestStartAuthenticatesBeforeReadingBodyOrCallingExecutor(t *testing.T) {
	h := startHandler(t, startFunc(func(context.Context, lifecycle.StartRequest) (journal.Operation, error) {
		t.Fatal("unauthorized request reached execution boundary")
		return journal.Operation{}, nil
	}))
	req := httptest.NewRequest(http.MethodPost, startPath, nil)
	req.Body = unreadableBody{t}
	req.Header.Set("Authorization", "Bearer wrong-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("unauthorized response: %d, %s", w.Code, w.Body.String())
	}
	assertErrorCode(t, w, "unauthorized")
}

func TestStartStrictRequestValidation(t *testing.T) {
	h := startHandler(t, startFunc(func(context.Context, lifecycle.StartRequest) (journal.Operation, error) {
		t.Fatal("invalid request reached journal/process executor")
		return journal.Operation{}, nil
	}))
	tests := map[string]string{
		"empty": "", "null": "null", "array": "[]", "string": `"request"`,
		"empty object": `{}`, "truncated": `{"operationId":`,
		"empty ID":           strings.Replace(validStartBody, `"op"`, `""`, 1),
		"long ID":            strings.Replace(validStartBody, `"op"`, `"`+strings.Repeat("x", 129)+`"`, 1),
		"UTF-8 byte limit":   strings.Replace(validStartBody, `"op"`, `"`+strings.Repeat("字", 43)+`"`, 1),
		"invalid UTF-8":      strings.Replace(validStartBody, "op\"", string([]byte{0xff})+"\"", 1),
		"missing identity":   `{"operationId":"op","expectedRuntimeGeneration":7}`,
		"blank identity":     strings.Replace(validStartBody, `"runtime-01"`, `"  "`, 1),
		"null identity":      strings.Replace(validStartBody, `"runtime-01"`, `null`, 1),
		"missing generation": `{"operationId":"op","expectedRuntimeIdentity":"runtime-01"}`,
		"trailing object":    validStartBody + `{}`, "trailing null": validStartBody + `null`,
		"trailing garbage": validStartBody + `x`,
		"oversized body":   strings.Repeat(" ", maxMutationBody) + validStartBody,
		"case alias":       strings.Replace(validStartBody, "operationId", "OperationId", 1),
		"duplicate ID":     strings.Replace(validStartBody, `"operationId":"op"`, `"operationId":"op","operationId":"other"`, 1),
	}
	for _, generation := range []string{"0", "-1", "1.5", "18446744073709551616", `"7"`, "null", "true"} {
		tests["generation "+generation] = strings.Replace(validStartBody, ":7", ":"+generation, 1)
	}
	for _, field := range []string{"executable", "argv", "args", "env", "workingDirectory", "shellCommand", "action", "params"} {
		tests[field] = strings.TrimSuffix(validStartBody, "}") + `,"` + field + `":"not-allowed"}`
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			w := submitStart(t, h, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			assertErrorCode(t, w, "invalid_request")
		})
	}
}

func TestStartPreservesOpaqueIDAndFullWidthGeneration(t *testing.T) {
	id := strings.Repeat("字", 42) + " a"
	h := startHandler(t, startFunc(func(_ context.Context, request lifecycle.StartRequest) (journal.Operation, error) {
		if request.OperationID != id || request.ExpectedRuntimeGeneration != ^uint64(0) {
			t.Fatalf("typed request = %+v", request)
		}
		return journal.Operation{OperationID: id}, nil
	}))
	body := fmt.Sprintf(`{"operationId":%q,"expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":18446744073709551615}`, id)
	if w := submitStart(t, h, body); w.Code != http.StatusOK {
		t.Fatalf("valid opaque request = %d, %s", w.Code, w.Body.String())
	}
}

func TestStartReadOnlyModeIsUnsupported(t *testing.T) {
	h := newTestHandler(t)
	w := submitStart(t, h, validStartBody)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("read-only Start = %d", w.Code)
	}
	assertErrorCode(t, w, "unsupported_operation")
	method := request(t, h, http.MethodGet, startPath, testRuntimeToken)
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("Start method = %d, Allow %q", method.Code, method.Header().Get("Allow"))
	}
	for _, path := range []string{"/v1/runtime/operations/op"} {
		if w := request(t, h, http.MethodPost, path, testRuntimeToken); w.Code != http.StatusNotFound {
			t.Fatalf("out-of-scope endpoint %s is exposed: %d", path, w.Code)
		}
	}
}

func TestStartCapabilitiesAndResponseDoNotClaimReadiness(t *testing.T) {
	for _, state := range []journal.State{journal.StateAccepted, journal.StateRunning, journal.StateSucceeded, journal.StateFailed} {
		t.Run(string(state), func(t *testing.T) {
			h := startHandler(t, startFunc(func(_ context.Context, r lifecycle.StartRequest) (journal.Operation, error) {
				if r.ExpectedRuntimeGeneration != 7 {
					t.Fatalf("request generation = %d", r.ExpectedRuntimeGeneration)
				}
				operation := journal.Operation{OperationID: r.OperationID, OperationType: "start", RuntimeIdentity: "runtime-01", RuntimeGeneration: 6, State: state}
				if state == journal.StateFailed {
					operation.FailureCode = "process_start_failed"
				}
				return operation, nil
			}))
			w := submitStart(t, h, validStartBody)
			if w.Code != http.StatusOK {
				t.Fatalf("Start response = %d, %s", w.Code, w.Body.String())
			}
			assertJSONHeaders(t, w)
			var got operationResponse
			decodeResponse(t, w, &got)
			if got.OperationID != "op" || got.OperationType != "start" || got.RuntimeIdentity != "runtime-01" ||
				got.RuntimeGeneration != 6 || got.State != state {
				t.Fatalf("operation response = %+v", got)
			}
			if state == journal.StateFailed && (got.Error == nil || got.Error.Code != "process_start_failed") {
				t.Fatalf("replay lost structured failure: %+v", got)
			}
			if strings.Contains(w.Body.String(), "Fingerprint") || strings.Contains(w.Body.String(), "ready") {
				t.Fatalf("response leaked journal fields or invented readiness: %s", w.Body.String())
			}
			for _, path := range []string{handshakePath, statusPath} {
				var status statusResponse
				decodeResponse(t, request(t, h, http.MethodGet, path, testRuntimeToken), &status)
				if status.RuntimeGeneration != 7 || status.CPAObservedVersion != "" || !reflect.DeepEqual(status.Capabilities, []string{"start"}) ||
					(path == statusPath && status.State != "unknown") {
					t.Fatalf("%s invented runtime observation or lost capability: %+v", path, status)
				}
			}
		})
	}
}

func TestStartMapsErrorsWithoutExposingExecutionDetails(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{lifecycle.ErrInvalidRequest, 400, "invalid_request"},
		{journal.ErrRuntimeIdentityMismatch, 409, "runtime_identity_mismatch"},
		{journal.ErrStaleRuntimeGeneration, 409, "stale_runtime_generation"},
		{journal.ErrOperationIDConflict, 409, "operation_id_conflict"},
		{journal.ErrOperationStateConflict, 409, "operation_state_conflict"},
		{lifecycle.ErrPersistenceUnavailable, 503, "operation_persistence_unavailable"},
		{errors.Join(lifecycle.ErrPersistenceUnavailable, journal.ErrOperationStateConflict), 503, "operation_persistence_unavailable"},
		{errors.Join(lifecycle.ErrExecutionFailed, journal.ErrOperationStateConflict), 500, "internal_error"},
		{errors.New("secret-executable-or-token"), 500, "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.err.Error(), func(t *testing.T) {
			h := startHandler(t, startFunc(func(context.Context, lifecycle.StartRequest) (journal.Operation, error) {
				return journal.Operation{}, test.err
			}))
			w := submitStart(t, h, validStartBody)
			if w.Code != test.status {
				t.Fatalf("error response = %d, %s", w.Code, w.Body.String())
			}
			assertErrorCode(t, w, test.code)
			assertJSONHeaders(t, w)
			if strings.Contains(w.Body.String(), "secret-executable-or-token") {
				t.Fatal("raw execution error escaped to HTTP")
			}
		})
	}
}

type startFunc func(context.Context, lifecycle.StartRequest) (journal.Operation, error)

func (f startFunc) Start(ctx context.Context, r lifecycle.StartRequest) (journal.Operation, error) {
	return f(ctx, r)
}

func startHandler(t *testing.T, executor StartExecutor) http.Handler {
	t.Helper()
	h, err := NewHandler(Config{RuntimeIdentity: "runtime-01", RuntimeGeneration: 7, Token: testRuntimeToken, Start: executor})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func submitStart(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, startPath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testRuntimeToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

type unreadableBody struct{ t *testing.T }

func (b unreadableBody) Read([]byte) (int, error) {
	b.t.Fatal("unauthorized request body was read")
	return 0, errors.New("unexpected read")
}

func (unreadableBody) Close() error { return nil }
