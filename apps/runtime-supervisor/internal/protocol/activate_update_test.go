package protocol

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const validActivateUpdateBody = `{"operationId":"op","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7,"expectedActiveArtifactId":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","targetVersion":"7.3.4"}`

type activateUpdateFunc func(context.Context, lifecycle.ActivateUpdateRequest) (journal.Operation, error)

func (f activateUpdateFunc) ActivateUpdate(ctx context.Context, request lifecycle.ActivateUpdateRequest) (journal.Operation, error) {
	return f(ctx, request)
}

func TestActivateUpdateAuthenticatesBeforeStrictTypedDecode(t *testing.T) {
	called := 0
	h := activateUpdateHandler(t, activateUpdateFunc(func(_ context.Context, request lifecycle.ActivateUpdateRequest) (journal.Operation, error) {
		called++
		if request.TargetVersion != "7.3.4" || request.OperationID != "op" {
			t.Fatalf("request = %+v", request)
		}
		return journal.Operation{
			OperationID: "op", OperationType: "activate_update", RuntimeIdentity: "runtime-01",
			RuntimeGeneration: 7, State: journal.StateSucceeded,
		}, nil
	}))
	unauthorized := httptest.NewRequest(http.MethodPost, activateUpdatePath, strings.NewReader(`{"invalid":`))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized || called != 0 {
		t.Fatalf("unauthorized response=%d called=%d", response.Code, called)
	}
	response = submitActivateUpdate(t, h, validActivateUpdateBody)
	if response.Code != http.StatusOK || called != 1 || !strings.Contains(response.Body.String(), `"operationType":"activate_update"`) {
		t.Fatalf("response=%d body=%s called=%d", response.Code, response.Body.String(), called)
	}
}

func TestActivateUpdateStrictValidationRejectsAliasesAndExecutionAuthority(t *testing.T) {
	h := activateUpdateHandler(t, activateUpdateFunc(func(context.Context, lifecycle.ActivateUpdateRequest) (journal.Operation, error) {
		t.Fatal("invalid request reached executor")
		return journal.Operation{}, nil
	}))
	tests := map[string]string{
		"empty": "", "null": "null", "array": "[]",
		"latest":           strings.Replace(validActivateUpdateBody, `"7.3.4"`, `"latest"`, 1),
		"tag alias":        strings.Replace(validActivateUpdateBody, `"7.3.4"`, `"v7.3.4"`, 1),
		"URL target":       strings.Replace(validActivateUpdateBody, `"7.3.4"`, `"https://example.test/a"`, 1),
		"uppercase digest": strings.Replace(validActivateUpdateBody, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"duplicate target": strings.Replace(validActivateUpdateBody, `"targetVersion":"7.3.4"`, `"targetVersion":"7.3.4","targetVersion":"7.3.5"`, 1),
		"trailing":         validActivateUpdateBody + `{}`,
	}
	for _, field := range []string{"targetExecutable", "stagedPath", "targetDigest", "archiveUrl", "rollbackPath", "command", "args", "env"} {
		tests[field] = strings.TrimSuffix(validActivateUpdateBody, "}") + `,"` + field + `":"forbidden"}`
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			response := submitActivateUpdate(t, h, body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
				t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestActivateUpdateCapabilityIsAdditiveAndErrorsAreStable(t *testing.T) {
	executor := activateUpdateFunc(func(context.Context, lifecycle.ActivateUpdateRequest) (journal.Operation, error) {
		return journal.Operation{}, nil
	})
	h := activateUpdateHandler(t, executor)
	for _, path := range []string{handshakePath, statusPath} {
		response := activateAuthenticatedRequest(t, h, http.MethodGet, path, "")
		var decoded struct {
			Capabilities []string `json:"capabilities"`
		}
		decodeResponse(t, response, &decoded)
		if !reflect.DeepEqual(decoded.Capabilities, []string{CapabilityActivateUpdate}) {
			t.Fatalf("%s capabilities = %v", path, decoded.Capabilities)
		}
	}

	for name, test := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"missing":        {lifecycle.ErrTargetStageUnavailable, http.StatusConflict, "target_stage_unavailable"},
		"corrupt":        {runtimeupdate.ErrStageConflict, http.StatusInternalServerError, "internal_error"},
		"mapped corrupt": {lifecycle.ErrTargetStageCorrupt, http.StatusConflict, "target_stage_corrupt"},
		"state":          {journal.ErrOperationStateConflict, http.StatusConflict, "operation_state_conflict"},
		"unsupported":    {lifecycle.ErrUnsupportedActivation, http.StatusBadRequest, "unsupported_operation"},
		"selection":      {selection.ErrSelectionPublicationAmbiguous, http.StatusInternalServerError, "internal_error"},
	} {
		t.Run(name, func(t *testing.T) {
			h := activateUpdateHandler(t, activateUpdateFunc(func(context.Context, lifecycle.ActivateUpdateRequest) (journal.Operation, error) {
				return journal.Operation{}, test.err
			}))
			response := submitActivateUpdate(t, h, validActivateUpdateBody)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestActivateUpdateFailedReplayUsesCommonOperationResponse(t *testing.T) {
	h := activateUpdateHandler(t, activateUpdateFunc(func(context.Context, lifecycle.ActivateUpdateRequest) (journal.Operation, error) {
		return journal.Operation{
			OperationID: "op", OperationType: "activate_update", RuntimeIdentity: "runtime-01",
			RuntimeGeneration: 7, State: journal.StateFailed, FailureCode: "activation_readiness_failed",
		}, nil
	}))
	response := submitActivateUpdate(t, h, validActivateUpdateBody)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"failed"`) ||
		!strings.Contains(response.Body.String(), `"code":"activation_readiness_failed"`) {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
}

func activateUpdateHandler(t *testing.T, executor ActivateUpdateExecutor) http.Handler {
	t.Helper()
	h, err := NewHandler(Config{
		RuntimeIdentity: "runtime-01", RuntimeGeneration: 7, Token: testRuntimeToken, ActivateUpdate: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func submitActivateUpdate(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	return activateAuthenticatedRequest(t, h, http.MethodPost, activateUpdatePath, body)
}

func activateAuthenticatedRequest(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testRuntimeToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func TestActivateUpdateWriteDeadlineRequiresValidAuthAndReturnsJSONOnDeadlineError(t *testing.T) {
	called := 0
	h := activateUpdateHandler(t, activateUpdateFunc(func(_ context.Context, req lifecycle.ActivateUpdateRequest) (journal.Operation, error) {
		called++
		return journal.Operation{
			OperationID:       req.OperationID,
			OperationType:     "activate_update",
			RuntimeIdentity:   req.ExpectedRuntimeIdentity,
			RuntimeGeneration: req.ExpectedRuntimeGeneration,
			State:             journal.StateSucceeded,
		}, nil
	}))

	for _, tc := range []struct {
		name       string
		auth       string
		body       string
		wantStatus int
	}{
		{name: "missing auth", body: validActivateUpdateBody, wantStatus: http.StatusUnauthorized},
		{name: "wrong bearer token", auth: "Bearer wrong-token", body: validActivateUpdateBody, wantStatus: http.StatusUnauthorized},
		{name: "invalid JSON body", auth: "Bearer " + testRuntimeToken, body: `{"operationId":"op"}`, wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, activateUpdatePath, strings.NewReader(tc.body))
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rec := &uploadDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			h.ServeHTTP(rec, req)
			after := time.Now()
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if len(rec.writeDeadlines) != 0 {
				t.Fatalf("unexpected write deadline extension on %s: writes=%v", tc.name, rec.writeDeadlines)
			}
			if tc.wantStatus == http.StatusUnauthorized {
				if len(rec.readDeadlines) != 1 || rec.readDeadlines[0].IsZero() || rec.readDeadlines[0].After(after.Add(time.Second)) {
					t.Fatalf("expected immediate read deadline expiration on unconsumed %s: reads=%v", tc.name, rec.readDeadlines)
				}
			} else if len(rec.readDeadlines) != 0 {
				t.Fatalf("unexpected read deadline change on consumed %s: reads=%v", tc.name, rec.readDeadlines)
			}
		})
	}

	// Unconfigured activate-update does not extend write deadline either.
	readOnly := newTestHandler(t)
	unconfiguredReq := httptest.NewRequest(http.MethodPost, activateUpdatePath, strings.NewReader(validActivateUpdateBody))
	unconfiguredReq.Header.Set("Authorization", "Bearer "+testRuntimeToken)
	unconfiguredRec := &uploadDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	readOnly.ServeHTTP(unconfiguredRec, unconfiguredReq)
	if unconfiguredRec.Code != http.StatusBadRequest || len(unconfiguredRec.writeDeadlines) != 0 {
		t.Fatalf("unconfigured activate status=%d writes=%v", unconfiguredRec.Code, unconfiguredRec.writeDeadlines)
	}

	// Deadline failure returns protocol JSON internal_error rather than text/plain.
	failReq := httptest.NewRequest(http.MethodPost, activateUpdatePath, strings.NewReader(validActivateUpdateBody))
	failReq.Header.Set("Authorization", "Bearer "+testRuntimeToken)
	failRec := &uploadDeadlineRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		writeErr:         errors.New("deadline unavailable"),
	}
	h.ServeHTTP(failRec, failReq)
	if failRec.Code != http.StatusInternalServerError || called != 0 {
		t.Fatalf("deadline error status = %d, called = %d; want 500, 0", failRec.Code, called)
	}
	assertJSONHeaders(t, failRec.ResponseRecorder)
	assertErrorCode(t, failRec.ResponseRecorder, "internal_error")

	// Valid authenticated activate-update extends write deadline and keeps it active through response flush.
	okReq := httptest.NewRequest(http.MethodPost, activateUpdatePath, strings.NewReader(validActivateUpdateBody))
	okReq.Header.Set("Authorization", "Bearer "+testRuntimeToken)
	okRec := &uploadDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()
	h.ServeHTTP(okRec, okReq)
	after := time.Now()
	if okRec.Code != http.StatusOK || called != 1 {
		t.Fatalf("valid activate status = %d, called = %d", okRec.Code, called)
	}
	if len(okRec.readDeadlines) != 0 || len(okRec.writeDeadlines) != 1 {
		t.Fatalf("valid activate deadlines read=%v write=%v, want 0 read and 1 active write", okRec.readDeadlines, okRec.writeDeadlines)
	}
	if okRec.writeDeadlines[0].Before(before.Add(ActivateUpdateResponseTimeout)) || okRec.writeDeadlines[0].After(after.Add(ActivateUpdateResponseTimeout)) {
		t.Fatalf("valid activate write deadline = %v, want ~%s", okRec.writeDeadlines[0], ActivateUpdateResponseTimeout)
	}
}
