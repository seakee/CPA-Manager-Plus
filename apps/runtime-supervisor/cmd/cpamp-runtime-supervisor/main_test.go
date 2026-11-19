package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/protocol"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

func TestLoadConfig(t *testing.T) {
	values := map[string]string{
		"CPAMP_RUNTIME_IDENTITY": " runtime-01 ",
		"CPAMP_RUNTIME_TOKEN":    "runtime-token",
	}
	cfg, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(7))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.addr != defaultRuntimeAddr || cfg.runtimeIdentity != "runtime-01" || cfg.runtimeGeneration != 7 || cfg.token != "runtime-token" {
		t.Fatalf("config = %#v", cfg)
	}

	values["CPAMP_RUNTIME_ADDR"] = ":28318"
	values["CPAMP_RUNTIME_GENERATION"] = "99"
	cfg, err = loadConfig(func(key string) string { return values[key] }, fixedGeneration(8))
	if err != nil {
		t.Fatalf("loadConfig() custom address error = %v", err)
	}
	if cfg.addr != ":28318" {
		t.Fatalf("address = %q", cfg.addr)
	}
	if cfg.runtimeGeneration != 8 {
		t.Fatalf("runtime generation = %d, want source value 8", cfg.runtimeGeneration)
	}
}

func TestLoadConfigRejectsInvalidRequiredValues(t *testing.T) {
	valid := map[string]string{
		"CPAMP_RUNTIME_IDENTITY": "runtime-01",
		"CPAMP_RUNTIME_TOKEN":    "runtime-token",
	}
	tests := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{name: "identity", key: "CPAMP_RUNTIME_IDENTITY", want: "CPAMP_RUNTIME_IDENTITY is required"},
		{name: "token", key: "CPAMP_RUNTIME_TOKEN", want: "CPAMP_RUNTIME_TOKEN is required"},
		{name: "token whitespace", key: "CPAMP_RUNTIME_TOKEN", value: "token value", want: "CPAMP_RUNTIME_TOKEN must not contain whitespace"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := make(map[string]string, len(valid))
			for key, value := range valid {
				values[key] = value
			}
			values[test.key] = test.value
			_, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(1))
			if err == nil || err.Error() != test.want {
				t.Fatalf("loadConfig() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadConfigParsesPairedNonRootCPAIdentity(t *testing.T) {
	values := validConfigValues()
	values["CPAMP_RUNTIME_CPA_UID"] = "10001"
	values["CPAMP_RUNTIME_CPA_GID"] = "10002"
	cfg, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(1))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.cpaIdentity == nil || *cfg.cpaIdentity != (cpaprocess.ChildIdentity{UID: 10001, GID: 10002}) {
		t.Fatalf("CPA identity = %+v", cfg.cpaIdentity)
	}
}

func TestLoadConfigRejectsIncompleteRootOrMalformedCPAIdentity(t *testing.T) {
	for _, test := range []struct {
		uid  string
		gid  string
		want string
	}{
		{uid: "10001", want: "configured together"},
		{gid: "10001", want: "configured together"},
		{uid: "0", gid: "10001", want: "UID must be a non-zero uint32"},
		{uid: "10001", gid: "0", want: "GID must be a non-zero uint32"},
		{uid: "root", gid: "10001", want: "UID must be a non-zero uint32"},
		{uid: "10001", gid: "-1", want: "GID must be a non-zero uint32"},
	} {
		values := validConfigValues()
		values["CPAMP_RUNTIME_CPA_UID"] = test.uid
		values["CPAMP_RUNTIME_CPA_GID"] = test.gid
		_, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(1))
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("loadConfig(uid=%q gid=%q) error = %v, want %q", test.uid, test.gid, err, test.want)
		}
	}
}

func TestLoadConfigResamplesZeroGeneration(t *testing.T) {
	values := validConfigValues()
	generations := []uint64{0, 42}
	var calls int
	cfg, err := loadConfig(func(key string) string { return values[key] }, func() (uint64, error) {
		generation := generations[calls]
		calls++
		return generation, nil
	})
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.runtimeGeneration != 42 || calls != 2 {
		t.Fatalf("runtime generation = %d after %d calls, want 42 after 2 calls", cfg.runtimeGeneration, calls)
	}
}

func TestLoadConfigFailsWhenGenerationSourceFails(t *testing.T) {
	values := validConfigValues()
	sourceErr := errors.New("entropy unavailable")
	cfg, err := loadConfig(func(key string) string { return values[key] }, func() (uint64, error) {
		return 0, sourceErr
	})
	if !errors.Is(err, sourceErr) || !strings.Contains(err.Error(), "generate runtime generation") {
		t.Fatalf("loadConfig() error = %v, want wrapped generation error", err)
	}
	if !reflect.DeepEqual(cfg, config{}) {
		t.Fatalf("config = %#v, want zero value", cfg)
	}
}

func TestRuntimeGenerationIsStableForSupervisorIncarnation(t *testing.T) {
	cfg := loadTestConfig(t, fixedGeneration(41))
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatalf("newRuntimeHandler() error = %v", err)
	}

	requests := []struct {
		path      string
		wantState string
	}{
		{path: "/v1/runtime/handshake"},
		{path: "/v1/runtime/status", wantState: "unknown"},
		{path: "/v1/runtime/status", wantState: "unknown"},
	}
	for _, request := range requests {
		response := requestRuntime(t, handler, request.path)
		if response.RuntimeGeneration != 41 || response.RuntimeIdentity != "runtime-01" || response.ProtocolVersion != "v1" {
			t.Fatalf("%s protocol metadata = %#v", request.path, response)
		}
		if response.State != request.wantState || response.CPAObservedVersion != "" || len(response.Capabilities) != 0 {
			t.Fatalf("%s response = %#v", request.path, response)
		}
	}
}

func TestSeparateSupervisorInitializationsUseIndependentGenerations(t *testing.T) {
	first := loadTestConfig(t, fixedGeneration(41))
	second := loadTestConfig(t, fixedGeneration(42))
	if first.runtimeGeneration != 41 || second.runtimeGeneration != 42 {
		t.Fatalf("runtime generations = %d and %d, want 41 and 42", first.runtimeGeneration, second.runtimeGeneration)
	}
}

func TestHTTPServerHasBoundedTimeouts(t *testing.T) {
	server := newHTTPServer(http.NewServeMux())
	if server.ReadHeaderTimeout != 5*time.Second ||
		server.ReadTimeout != 10*time.Second ||
		server.WriteTimeout != 10*time.Second ||
		server.IdleTimeout != 60*time.Second ||
		server.MaxHeaderBytes != 16<<10 {
		t.Fatalf("HTTP server limits = %#v", server)
	}
}

func TestPrepareUpdateTransportBudgetIsDedicatedAndOrdered(t *testing.T) {
	server := newHTTPServer(http.NewServeMux())
	if server.WriteTimeout != 10*time.Second {
		t.Fatalf("ordinary Runtime WriteTimeout = %s, want 10s", server.WriteTimeout)
	}
	if prepareUpdateResponseTimeout <= lifecycle.PrepareUpdateExecutionTimeout+
		lifecycle.PrepareUpdateTerminalPersistenceTimeout ||
		prepareUpdateResponseTimeout <= runtimeupdate.ReleaseClientTimeout {
		t.Fatalf("prepare response budget = %s, execution=%s persistence=%s release=%s", prepareUpdateResponseTimeout,
			lifecycle.PrepareUpdateExecutionTimeout, lifecycle.PrepareUpdateTerminalPersistenceTimeout, runtimeupdate.ReleaseClientTimeout)
	}
	if prepareUpdateUploadReadTimeout <= server.ReadTimeout || prepareUpdateUploadReadTimeout >= prepareUpdateResponseTimeout {
		t.Fatalf("upload read budget=%s ordinary read=%s prepare response=%s",
			prepareUpdateUploadReadTimeout, server.ReadTimeout, prepareUpdateResponseTimeout)
	}
	if activateUpdateResponseTimeout <= lifecycle.ActivationExecutionTimeout+
		lifecycle.ActivationTerminalPersistenceTimeout ||
		lifecycle.ActivationCandidateReadinessTimeout+lifecycle.ActivationRollbackReadinessTimeout >=
			lifecycle.ActivationExecutionTimeout {
		t.Fatalf("activate response budget=%s execution=%s persistence=%s candidate=%s rollback=%s",
			activateUpdateResponseTimeout, lifecycle.ActivationExecutionTimeout,
			lifecycle.ActivationTerminalPersistenceTimeout, lifecycle.ActivationCandidateReadinessTimeout,
			lifecycle.ActivationRollbackReadinessTimeout)
	}
	const managerArtifactUploadTimeout = 15 * time.Minute
	if uploadArtifactBudgetTimeout <= server.ReadTimeout ||
		uploadArtifactBudgetTimeout != lifecycle.AcquireArtifactExecutionTimeout ||
		uploadArtifactWriteBudgetTimeout <= uploadArtifactBudgetTimeout ||
		uploadArtifactWriteBudgetTimeout <= lifecycle.AcquireArtifactExecutionTimeout ||
		uploadArtifactWriteBudgetTimeout >= managerArtifactUploadTimeout {
		t.Fatalf("artifact upload budgets read=%s exec=%s write=%s manager=%s",
			uploadArtifactBudgetTimeout, lifecycle.AcquireArtifactExecutionTimeout,
			uploadArtifactWriteBudgetTimeout, managerArtifactUploadTimeout)
	}
}

const testActiveArtifactID = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testPrepareUploadQuery(operationID string) string {
	return "operationId=" + operationID +
		"&expectedRuntimeIdentity=runtime-01" +
		"&expectedRuntimeGeneration=7" +
		"&expectedActiveArtifactId=" + testActiveArtifactID +
		"&targetVersion=7.3.3"
}

type startExecutorFunc func(context.Context, lifecycle.StartRequest) (journal.Operation, error)

func (fn startExecutorFunc) Start(ctx context.Context, req lifecycle.StartRequest) (journal.Operation, error) {
	return fn(ctx, req)
}

type prepareExecutorFunc func(context.Context, lifecycle.PrepareUpdateRequest) (journal.Operation, error)

func (fn prepareExecutorFunc) PrepareUpdate(ctx context.Context, req lifecycle.PrepareUpdateRequest) (journal.Operation, error) {
	return fn(ctx, req)
}

type activateExecutorFunc func(context.Context, lifecycle.ActivateUpdateRequest) (journal.Operation, error)

func (fn activateExecutorFunc) ActivateUpdate(ctx context.Context, req lifecycle.ActivateUpdateRequest) (journal.Operation, error) {
	return fn(ctx, req)
}

type uploadExecutorFunc func(context.Context, lifecycle.PrepareUpdateUploadRequest) (journal.Operation, error)

func (fn uploadExecutorFunc) PrepareUpdateUpload(ctx context.Context, req lifecycle.PrepareUpdateUploadRequest) (journal.Operation, error) {
	return fn(ctx, req)
}

func TestUpdateTransportDeadlinesAreRouteSpecific(t *testing.T) {
	const (
		preparePath      = "/v1/runtime/operations/prepare-update"
		activatePath     = "/v1/runtime/operations/activate-update"
		validStartBody   = `{"operationId":"op-start","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7}`
		validPrepareBody = `{"operationId":"op-prep","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7,"expectedActiveArtifactId":"` + testActiveArtifactID + `","targetVersion":"7.3.3"}`
		validActBody     = `{"operationId":"op-act","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7,"expectedActiveArtifactId":"` + testActiveArtifactID + `","targetVersion":"7.3.3"}`
	)

	protocolHandler, err := protocol.NewHandler(protocol.Config{
		RuntimeIdentity:   "runtime-01",
		RuntimeGeneration: 7,
		Token:             "runtime-token",
		Start: startExecutorFunc(func(_ context.Context, req lifecycle.StartRequest) (journal.Operation, error) {
			return journal.Operation{
				OperationID:       req.OperationID,
				OperationType:     "start",
				RuntimeIdentity:   req.ExpectedRuntimeIdentity,
				RuntimeGeneration: req.ExpectedRuntimeGeneration,
				State:             journal.StateSucceeded,
			}, nil
		}),
		PrepareUpdate: prepareExecutorFunc(func(_ context.Context, req lifecycle.PrepareUpdateRequest) (journal.Operation, error) {
			return journal.Operation{
				OperationID:       req.OperationID,
				OperationType:     "prepare_update",
				RuntimeIdentity:   req.ExpectedRuntimeIdentity,
				RuntimeGeneration: req.ExpectedRuntimeGeneration,
				State:             journal.StateSucceeded,
			}, nil
		}),
		ActivateUpdate: activateExecutorFunc(func(_ context.Context, req lifecycle.ActivateUpdateRequest) (journal.Operation, error) {
			return journal.Operation{
				OperationID:       req.OperationID,
				OperationType:     "activate_update",
				RuntimeIdentity:   req.ExpectedRuntimeIdentity,
				RuntimeGeneration: req.ExpectedRuntimeGeneration,
				State:             journal.StateSucceeded,
			}, nil
		}),
		PrepareUpdateUpload: uploadExecutorFunc(func(_ context.Context, req lifecycle.PrepareUpdateUploadRequest) (journal.Operation, error) {
			if req.OperationID == "op-replay" {
				return journal.Operation{
					OperationID:       req.OperationID,
					RuntimeIdentity:   req.ExpectedRuntimeIdentity,
					RuntimeGeneration: req.ExpectedRuntimeGeneration,
					State:             journal.StateSucceeded,
				}, nil
			}
			if _, err := io.ReadAll(req.Archive); err != nil {
				return journal.Operation{}, err
			}
			return journal.Operation{
				OperationID:       req.OperationID,
				RuntimeIdentity:   req.ExpectedRuntimeIdentity,
				RuntimeGeneration: req.ExpectedRuntimeGeneration,
				State:             journal.StateSucceeded,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, method, path, auth, body string
		wantStatus                     int
		write                          time.Duration
	}{
		{name: "ordinary lifecycle", method: http.MethodPost, path: "/v1/runtime/operations/start", auth: "Bearer runtime-token", body: validStartBody, wantStatus: http.StatusOK},
		{name: "prepare update missing auth", method: http.MethodPost, path: preparePath, body: validPrepareBody, wantStatus: http.StatusUnauthorized},
		{name: "prepare update wrong bearer token", method: http.MethodPost, path: preparePath, auth: "Bearer wrong-token", body: validPrepareBody, wantStatus: http.StatusUnauthorized},
		{name: "prepare update invalid body", method: http.MethodPost, path: preparePath, auth: "Bearer runtime-token", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "prepare update authenticated", method: http.MethodPost, path: preparePath, auth: "Bearer runtime-token", body: validPrepareBody, wantStatus: http.StatusOK, write: prepareUpdateResponseTimeout},
		{name: "activate update missing auth", method: http.MethodPost, path: activatePath, body: validActBody, wantStatus: http.StatusUnauthorized},
		{name: "activate update wrong bearer token", method: http.MethodPost, path: activatePath, auth: "Bearer wrong-token", body: validActBody, wantStatus: http.StatusUnauthorized},
		{name: "activate update invalid body", method: http.MethodPost, path: activatePath, auth: "Bearer runtime-token", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "activate update authenticated", method: http.MethodPost, path: activatePath, auth: "Bearer runtime-token", body: validActBody, wantStatus: http.StatusOK, write: activateUpdateResponseTimeout},
		{name: "upload wrong method", method: http.MethodGet, path: protocol.PrepareUpdateUploadPath, auth: "Bearer runtime-token", wantStatus: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &deadlineResponseWriter{ResponseRecorder: httptest.NewRecorder()}
			var bodyReader io.Reader
			if test.body != "" {
				bodyReader = strings.NewReader(test.body)
			}
			request := httptest.NewRequest(test.method, test.path, bodyReader)
			if test.auth != "" {
				request.Header.Set("Authorization", test.auth)
			}
			before := time.Now()
			protocolHandler.ServeHTTP(writer, request)
			after := time.Now()
			if writer.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", writer.Code, test.wantStatus, writer.Body.String())
			}
			if test.wantStatus == http.StatusUnauthorized && test.body != "" {
				if len(writer.reads) != 1 || writer.reads[0].IsZero() || writer.reads[0].After(after.Add(time.Second)) {
					t.Fatalf("expected immediate read deadline expiration on unconsumed body, got %v", writer.reads)
				}
			} else if len(writer.reads) != 0 {
				t.Fatalf("unexpected read deadline changes: %v", writer.reads)
			}
			if test.write == 0 {
				if len(writer.writes) != 0 {
					t.Fatalf("unexpected write deadline changes: %v", writer.writes)
				}
			} else {
				if len(writer.writes) != 1 || writer.writes[0].Before(before.Add(test.write)) || writer.writes[0].After(after.Add(test.write)) {
					t.Fatalf("write deadlines = %v, want single active budget %s", writer.writes, test.write)
				}
			}
		})
	}

	validUploadPath := protocol.PrepareUpdateUploadPath + "?" + testPrepareUploadQuery("op-upload-1")

	for _, rejected := range []struct {
		name        string
		path        string
		token       string
		contentType string
		length      int64
		noBody      bool
		wantStatus  int
	}{
		{name: "missing bearer token", path: validUploadPath, length: 4, wantStatus: http.StatusUnauthorized},
		{name: "wrong bearer token", path: validUploadPath, token: "wrong-token", length: 4, wantStatus: http.StatusUnauthorized},
		{name: "invalid query", path: protocol.PrepareUpdateUploadPath + "?operationId=only", token: "runtime-token", length: 4, wantStatus: http.StatusBadRequest},
		{name: "invalid content type", path: validUploadPath, token: "runtime-token", contentType: "application/json", length: 4, wantStatus: http.StatusBadRequest},
		{name: "zero content length", path: validUploadPath, token: "runtime-token", contentType: "application/octet-stream", length: 0, wantStatus: http.StatusBadRequest},
		{name: "negative content length below -1", path: validUploadPath, token: "runtime-token", contentType: "application/octet-stream", length: -2, wantStatus: http.StatusBadRequest},
		{name: "http.NoBody with unknown length", path: validUploadPath, token: "runtime-token", contentType: "application/octet-stream", length: -1, noBody: true, wantStatus: http.StatusBadRequest},
		{name: "oversize content length", path: validUploadPath, token: "runtime-token", contentType: "application/octet-stream", length: runtimeupdate.MaxAcquisitionArchiveBytes + 1, wantStatus: http.StatusRequestEntityTooLarge},
	} {
		t.Run("rejected before budget/"+rejected.name, func(t *testing.T) {
			writer := &deadlineResponseWriter{ResponseRecorder: httptest.NewRecorder()}
			var req *http.Request
			if rejected.noBody {
				req = httptest.NewRequest(http.MethodPost, rejected.path, nil)
			} else {
				req = httptest.NewRequest(http.MethodPost, rejected.path, strings.NewReader("data"))
			}
			req.ContentLength = rejected.length
			if rejected.token != "" {
				req.Header.Set("Authorization", "Bearer "+rejected.token)
			}
			if rejected.contentType != "" {
				req.Header.Set("Content-Type", rejected.contentType)
			}
			protocolHandler.ServeHTTP(writer, req)
			after := time.Now()
			if writer.Code != rejected.wantStatus {
				t.Fatalf("status = %d, want %d", writer.Code, rejected.wantStatus)
			}
			if len(writer.writes) != 0 {
				t.Fatalf("unexpected write deadline changes on rejected upload: writes=%v", writer.writes)
			}
			if !rejected.noBody {
				if got := writer.Header().Get("Connection"); got != "close" {
					t.Fatalf("Connection header = %q, want close for unconsumed body", got)
				}
				if len(writer.reads) != 1 || writer.reads[0].IsZero() || writer.reads[0].After(after.Add(time.Second)) {
					t.Fatalf("expected immediate read deadline expiration on rejected upload, got %v", writer.reads)
				}
			} else if len(writer.reads) != 0 {
				t.Fatalf("unexpected read deadline changes on NoBody upload: reads=%v", writer.reads)
			}
		})
	}

	t.Run("authenticated valid upload keeps write deadline active and restores read deadline on EOF", func(t *testing.T) {
		writer := &deadlineResponseWriter{ResponseRecorder: httptest.NewRecorder()}
		req := httptest.NewRequest(http.MethodPost, validUploadPath, strings.NewReader("ab"))
		req.Header.Set("Authorization", "Bearer runtime-token")
		req.Header.Set("Content-Type", "application/octet-stream")
		before := time.Now()
		protocolHandler.ServeHTTP(writer, req)
		after := time.Now()
		if writer.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", writer.Code, writer.Body.String())
		}
		if len(writer.reads) != 2 || len(writer.writes) != 1 {
			t.Fatalf("deadline transitions reads=%v writes=%v, want [budget, zero] for read and [budget] for write", writer.reads, writer.writes)
		}
		if writer.reads[0].Before(before.Add(prepareUpdateUploadReadTimeout)) || writer.reads[0].After(after.Add(prepareUpdateUploadReadTimeout)) {
			t.Fatalf("upload read deadline = %s, want budget %s", writer.reads[0], prepareUpdateUploadReadTimeout)
		}
		if writer.writes[0].Before(before.Add(prepareUpdateResponseTimeout)) || writer.writes[0].After(after.Add(prepareUpdateResponseTimeout)) {
			t.Fatalf("upload write deadline = %s, want budget %s", writer.writes[0], prepareUpdateResponseTimeout)
		}
		if !writer.reads[1].IsZero() {
			t.Fatalf("restored read deadline = %v, want zero time after EOF", writer.reads[1])
		}
		if got := writer.Header().Get("Connection"); got != "" {
			t.Fatalf("Connection header = %q, want empty after full body consumption", got)
		}
	})

	t.Run("authenticated unconsumed upload expires read deadline immediately", func(t *testing.T) {
		writer := &deadlineResponseWriter{ResponseRecorder: httptest.NewRecorder()}
		replayPath := protocol.PrepareUpdateUploadPath + "?" + testPrepareUploadQuery("op-replay")
		req := httptest.NewRequest(http.MethodPost, replayPath, strings.NewReader("unconsumed"))
		req.Header.Set("Authorization", "Bearer runtime-token")
		req.Header.Set("Content-Type", "application/octet-stream")
		before := time.Now()
		protocolHandler.ServeHTTP(writer, req)
		after := time.Now()
		if writer.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", writer.Code, writer.Body.String())
		}
		if len(writer.reads) != 2 || len(writer.writes) != 1 {
			t.Fatalf("deadline transitions reads=%v writes=%v, want 2 reads and 1 active write", writer.reads, writer.writes)
		}
		if writer.reads[1].IsZero() || writer.reads[1].Before(before) || writer.reads[1].After(after) {
			t.Fatalf("unconsumed final read deadline = %v, want immediate expiration in [%v, %v]", writer.reads[1], before, after)
		}
		if got := writer.Header().Get("Connection"); got != "close" {
			t.Fatalf("Connection header = %q, want close for unconsumed body", got)
		}
	})
}

func TestPrepareUpdateUploadSurvivesOrdinaryServerTimeouts(t *testing.T) {
	const ordinaryTimeout = 50 * time.Millisecond
	firstByteRead := make(chan struct{})
	protocolHandler, err := protocol.NewHandler(protocol.Config{
		RuntimeIdentity:   "runtime-01",
		RuntimeGeneration: 7,
		Token:             "runtime-token",
		PrepareUpdateUpload: uploadExecutorFunc(func(_ context.Context, req lifecycle.PrepareUpdateUploadRequest) (journal.Operation, error) {
			var first [1]byte
			if _, err := io.ReadFull(req.Archive, first[:]); err != nil {
				return journal.Operation{}, err
			}
			close(firstByteRead)
			rest, err := io.ReadAll(req.Archive)
			if err != nil {
				return journal.Operation{}, err
			}
			if string(first[:])+string(rest) != "ab" {
				return journal.Operation{}, lifecycle.ErrInvalidRequest
			}
			return journal.Operation{
				OperationID:       req.OperationID,
				RuntimeIdentity:   req.ExpectedRuntimeIdentity,
				RuntimeGeneration: req.ExpectedRuntimeGeneration,
				State:             journal.StateSucceeded,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	server.Config = newHTTPServer(protocolHandler)
	server.Config.ReadTimeout = ordinaryTimeout
	server.Config.WriteTimeout = ordinaryTimeout
	server.Start()
	defer server.Close()

	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	target := protocol.PrepareUpdateUploadPath + "?" + testPrepareUploadQuery("op-slow-upload")
	if _, err := fmt.Fprintf(
		conn,
		"POST %s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nContent-Type: application/octet-stream\r\nContent-Length: 2\r\nConnection: close\r\n\r\na",
		target,
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstByteRead:
	case <-time.After(2 * time.Second):
		t.Fatal("authenticated upload did not reach executor")
	}
	// Both ordinary server deadlines expire before the client finishes streaming its archive.
	time.Sleep(3 * ordinaryTimeout)
	if _, err := io.WriteString(conn, "b"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read delayed upload response: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"state":"succeeded"`) {
		t.Fatalf("delayed upload = %d, %q, %v", response.StatusCode, body, err)
	}
}

func TestPrepareUpdateStalledUnconsumedBodiesClosePromptly(t *testing.T) {
	protocolHandler, err := protocol.NewHandler(protocol.Config{
		RuntimeIdentity:   "runtime-01",
		RuntimeGeneration: 7,
		Token:             "runtime-token",
		PrepareUpdateUpload: uploadExecutorFunc(func(_ context.Context, req lifecycle.PrepareUpdateUploadRequest) (journal.Operation, error) {
			switch req.OperationID {
			case "op-conflict", "op-conflict-chunked":
				return journal.Operation{}, journal.ErrOperationIDConflict
			case "op-replay", "op-replay-chunked":
				return journal.Operation{
					OperationID:       req.OperationID,
					RuntimeIdentity:   req.ExpectedRuntimeIdentity,
					RuntimeGeneration: req.ExpectedRuntimeGeneration,
					State:             journal.StateSucceeded,
				}, nil
			case "op-stream-err", "op-stream-err-chunked":
				var one [1]byte
				_, _ = req.Archive.Read(one[:])
				return journal.Operation{
					OperationID:       req.OperationID,
					RuntimeIdentity:   req.ExpectedRuntimeIdentity,
					RuntimeGeneration: req.ExpectedRuntimeGeneration,
					State:             journal.StateFailed,
					FailureCode:       "download_failed",
				}, nil
			default:
				return journal.Operation{}, lifecycle.ErrInvalidRequest
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newHTTPServer(protocolHandler)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	var serverStopped bool
	t.Cleanup(func() {
		if !serverStopped {
			_ = server.Close()
			<-serveDone
		}
	})

	for _, tc := range []struct {
		name           string
		rawRequest     string
		wantStatus     int
		keepSocketOpen bool
	}{
		{
			name: "unauthenticated stalled upload",
			rawRequest: fmt.Sprintf(
				"POST %s?%s HTTP/1.1\r\nHost: localhost\r\nContent-Length: 4096\r\n\r\na",
				protocol.PrepareUpdateUploadPath,
				testPrepareUploadQuery("op-unauth"),
			),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "sync invalid content-type stalled upload",
			rawRequest: fmt.Sprintf(
				"POST %s?%s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nContent-Type: text/plain\r\nContent-Length: 4096\r\n\r\na",
				protocol.PrepareUpdateUploadPath,
				testPrepareUploadQuery("op-bad-ct"),
			),
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "sync oversize content-length stalled upload",
			rawRequest: fmt.Sprintf(
				"POST %s?%s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nContent-Length: %d\r\n\r\na",
				protocol.PrepareUpdateUploadPath,
				testPrepareUploadQuery("op-oversize"),
				runtimeupdate.MaxAcquisitionArchiveBytes+1,
			),
			wantStatus:     http.StatusRequestEntityTooLarge,
			keepSocketOpen: true,
		},
		{
			name: "precondition 409 conflict stalled Content-Length<=256KiB open socket",
			rawRequest: fmt.Sprintf(
				"POST %s?%s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nContent-Length: 4096\r\n\r\na",
				protocol.PrepareUpdateUploadPath,
				testPrepareUploadQuery("op-conflict"),
			),
			wantStatus:     http.StatusConflict,
			keepSocketOpen: true,
		},
		{
			name: "precondition 409 conflict stalled chunked open socket",
			rawRequest: fmt.Sprintf(
				"POST %s?%s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nTransfer-Encoding: chunked\r\n\r\n1\r\na\r\n",
				protocol.PrepareUpdateUploadPath,
				testPrepareUploadQuery("op-conflict-chunked"),
			),
			wantStatus:     http.StatusConflict,
			keepSocketOpen: true,
		},
		{
			name: "idempotent 200 replay stalled Content-Length<=256KiB open socket",
			rawRequest: fmt.Sprintf(
				"POST %s?%s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nContent-Length: 4096\r\n\r\na",
				protocol.PrepareUpdateUploadPath,
				testPrepareUploadQuery("op-replay"),
			),
			wantStatus:     http.StatusOK,
			keepSocketOpen: true,
		},
		{
			name: "idempotent 200 replay stalled chunked open socket",
			rawRequest: fmt.Sprintf(
				"POST %s?%s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nTransfer-Encoding: chunked\r\n\r\n1\r\na\r\n",
				protocol.PrepareUpdateUploadPath,
				testPrepareUploadQuery("op-replay-chunked"),
			),
			wantStatus:     http.StatusOK,
			keepSocketOpen: true,
		},
		{
			name: "partial read stream error stalled Content-Length<=256KiB open socket",
			rawRequest: fmt.Sprintf(
				"POST %s?%s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nContent-Length: 4096\r\n\r\nabcd",
				protocol.PrepareUpdateUploadPath,
				testPrepareUploadQuery("op-stream-err"),
			),
			wantStatus:     http.StatusOK,
			keepSocketOpen: true,
		},
		{
			name: "partial read stream error stalled chunked open socket",
			rawRequest: fmt.Sprintf(
				"POST %s?%s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nTransfer-Encoding: chunked\r\n\r\n4\r\nabcd\r\n",
				protocol.PrepareUpdateUploadPath,
				testPrepareUploadQuery("op-stream-err-chunked"),
			),
			wantStatus:     http.StatusOK,
			keepSocketOpen: true,
		},
		{
			name:       "unauthenticated non-upload prepare stalled body",
			rawRequest: "POST /v1/runtime/operations/prepare-update HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n{",
			wantStatus: http.StatusUnauthorized,
		},
	} {
		parentT := t
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if tc.keepSocketOpen {
				// Keep client socket open past server.Shutdown to prove net/http aborted its 256 KiB post-handler drain.
				parentT.Cleanup(func() { _ = conn.Close() })
			} else {
				defer conn.Close()
			}
			if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(conn, tc.rawRequest); err != nil {
				t.Fatal(err)
			}
			br := bufio.NewReader(conn)
			resp, err := http.ReadResponse(br, nil)
			if err != nil {
				t.Fatalf("ReadResponse() error = %v", err)
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			elapsed := time.Since(start)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if !resp.Close && !strings.EqualFold(resp.Header.Get("Connection"), "close") {
				t.Fatalf("response did not close unconsumed connection: Close=%v Connection=%q", resp.Close, resp.Header.Get("Connection"))
			}
			if elapsed >= time.Second {
				t.Fatalf("stalled body delayed response for %s; want < 1s", elapsed)
			}
			if tc.keepSocketOpen {
				var extra [1]byte
				if _, eofErr := br.Read(extra[:]); !errors.Is(eofErr, io.EOF) {
					t.Fatalf("server kept unconsumed connection open instead of aborting 256 KiB drain: err = %v", eofErr)
				}
			}
		})
	}

	shutdownStart := time.Now()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("server.Shutdown() with client sockets still open error = %v", err)
	}
	serverStopped = true
	if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("server.Serve() error = %v", err)
	}
	if shutdownElapsed := time.Since(shutdownStart); shutdownElapsed >= time.Second {
		t.Fatalf("server.Shutdown() took %s; want < 1s", shutdownElapsed)
	}
}

func TestArtifactStatusRouteDoesNotBlockOnSlowCDNLookup(t *testing.T) {
	lookupEntered := make(chan struct{})
	releaseLookup := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() {
		select {
		case <-releaseLookup:
		default:
			close(releaseLookup)
		}
	})

	h := newRouteTestHarness(
		t,
		lifecycle.WithAcquisitionCDNAddrPolicy(nil),
		lifecycle.WithAcquisitionCDNLookupIP(func(ctx context.Context, host string) ([]net.IPAddr, error) {
			if host == "api.github.com" {
				once.Do(func() { close(lookupEntered) })
				select {
				case <-releaseLookup:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return []net.IPAddr{{IP: net.ParseIP("140.82.121.6")}}, nil
			}
			return []net.IPAddr{{IP: net.ParseIP("198.18.0.113")}}, nil
		}),
	)

	acquireReq := httptest.NewRequest(http.MethodPost, artifactAcquirePath, strings.NewReader(`{"source":"cdn","cdnPrefix":"https://cdn.example.com/mirror"}`))
	acquireReq.Header.Set("Authorization", "Bearer runtime-token")
	acquireReq.Header.Set("Content-Type", "application/json")
	acquireRec := httptest.NewRecorder()
	h.handler.ServeHTTP(acquireRec, acquireReq)
	if acquireRec.Code != http.StatusAccepted {
		t.Fatalf("POST %s = %d (%s), want 202", artifactAcquirePath, acquireRec.Code, acquireRec.Body.String())
	}

	select {
	case <-lookupEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("CDN acquire did not reach api.github.com lookup")
	}

	statusDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		statusReq := httptest.NewRequest(http.MethodGet, artifactStatusPath, nil)
		statusReq.Header.Set("Authorization", "Bearer runtime-token")
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, statusReq)
		statusDone <- rec
	}()

	select {
	case rec := <-statusDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d (%s), want 200", artifactStatusPath, rec.Code, rec.Body.String())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("GET /v1/runtime/artifact blocked on slow cdnLookupIP")
	}
	close(releaseLookup)
}

type deadlineResponseWriter struct {
	*httptest.ResponseRecorder
	read, write   time.Time
	reads, writes []time.Time
}

func (w *deadlineResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.write = deadline
	w.writes = append(w.writes, deadline)
	return nil
}

func (w *deadlineResponseWriter) SetReadDeadline(deadline time.Time) error {
	w.read = deadline
	w.reads = append(w.reads, deadline)
	return nil
}

func TestServeStopsCleanlyWhenContextIsCanceled(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serve(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
	}()

	response, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("GET running server: %v", err)
	}
	_ = response.Body.Close()
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve() did not stop after context cancellation")
	}
}

func TestSupervisorShutdownClosesLifecycleAdmissionBeforeDraining(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAccepted := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		cancel()
		releaseAccepted()
		_ = listener.Close()
	})

	journalPath := filepath.Join(t.TempDir(), "operations.sqlite")
	store, err := journal.Open(t.Context(), journalPath, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	trackedJournal := &shutdownJournal{Store: store}
	child := &shutdownProcess{entered: make(chan struct{}), release: release}
	executor, err := lifecycle.NewExecutor(
		journal.Authority{RuntimeIdentity: "runtime-01", RuntimeGeneration: 41},
		trackedJournal,
		child,
		"supervisor-local-cpa",
	)
	if err != nil {
		t.Fatal(err)
	}
	queued := &queuedStartExecutor{
		next:        executor,
		operationID: "queued-start",
		waiting:     make(chan struct{}),
	}
	protocolHandler, err := protocol.NewHandler(protocol.Config{
		RuntimeIdentity:   "runtime-01",
		RuntimeGeneration: 41,
		Token:             "runtime-token",
		Start:             queued,
		Stop:              executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &runtimeHandler{Handler: protocolHandler, executor: executor}
	shutdown := &shutdownAdmissionObserver{runtimeHandler: runtime, closed: make(chan struct{})}
	serveResult := make(chan error, 1)
	go func() {
		serveErr := serve(ctx, listener, shutdown)
		serveResult <- errors.Join(serveErr, runtime.Close())
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	submit := func(operationID string) <-chan shutdownHTTPResult {
		result := make(chan shutdownHTTPResult, 1)
		go func() {
			req, err := http.NewRequest(
				http.MethodPost,
				"http://"+listener.Addr().String()+"/v1/runtime/operations/start",
				strings.NewReader(startBody(operationID, 41)),
			)
			if err != nil {
				result <- shutdownHTTPResult{err: err}
				return
			}
			req.Header.Set("Authorization", "Bearer runtime-token")
			response, err := client.Do(req)
			if err != nil {
				result <- shutdownHTTPResult{err: err}
				return
			}
			body, readErr := io.ReadAll(response.Body)
			result <- shutdownHTTPResult{
				status: response.StatusCode,
				body:   string(body),
				err:    errors.Join(readErr, response.Body.Close()),
			}
		}()
		return result
	}

	acceptedResult := submit("accepted-start")
	select {
	case <-child.entered:
	case <-time.After(time.Second):
		t.Fatal("accepted Start did not reach its process side effect")
	}
	queuedResult := submit("queued-start")
	select {
	case <-queued.waiting:
	case <-time.After(time.Second):
		t.Fatal("second Start did not enter the handler and wait on the lifecycle gate")
	}

	cancel()
	select {
	case <-shutdown.closed:
	case <-time.After(time.Second):
		t.Fatal("Supervisor shutdown did not close lifecycle admission")
	}
	if child.startCount() != 1 {
		t.Fatalf("process starts before drain = %d, want 1", child.startCount())
	}
	if trackedJournal.closeCount() != 0 {
		t.Fatalf("journal closed while accepted Start was still executing")
	}
	if resolves, begins := trackedJournal.submissionCounts(); resolves != 1 || begins != 1 {
		t.Fatalf("journal submissions before drain = Resolve %d, Begin %d; want 1, 1", resolves, begins)
	}
	select {
	case err := <-serveResult:
		t.Fatalf("server shutdown completed before accepted Start drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	reader, err := journal.Open(t.Context(), journalPath, journal.Options{})
	if err != nil {
		t.Fatalf("open journal during accepted execution: %v", err)
	}
	accepted, acceptedErr := reader.Get(t.Context(), "runtime-01", "accepted-start")
	_, queuedErr := reader.Get(t.Context(), "runtime-01", "queued-start")
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if acceptedErr != nil || accepted.State != journal.StateRunning {
		t.Fatalf("accepted durable execution = %+v, %v", accepted, acceptedErr)
	}
	if !errors.Is(queuedErr, journal.ErrOperationNotFound) {
		t.Fatalf("queued Start wrote durable intent before drain: %v", queuedErr)
	}

	releaseAccepted()
	acceptedHTTP := <-acceptedResult
	if acceptedHTTP.err != nil || acceptedHTTP.status != http.StatusOK || !strings.Contains(acceptedHTTP.body, `"state":"succeeded"`) {
		t.Fatalf("accepted Start response = %d, %s, %v", acceptedHTTP.status, acceptedHTTP.body, acceptedHTTP.err)
	}
	queuedHTTP := <-queuedResult
	if queuedHTTP.err != nil || queuedHTTP.status != http.StatusServiceUnavailable ||
		!strings.Contains(queuedHTTP.body, "operation_persistence_unavailable") {
		t.Fatalf("queued Start response = %d, %s, %v", queuedHTTP.status, queuedHTTP.body, queuedHTTP.err)
	}
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("Supervisor shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server shutdown did not finish after accepted Start drained")
	}
	if child.startCount() != 1 {
		t.Fatalf("shutdown admitted an additional process Start: %d", child.startCount())
	}
	if trackedJournal.closeCount() != 1 {
		t.Fatalf("journal close calls = %d, want 1", trackedJournal.closeCount())
	}
	if resolves, begins := trackedJournal.submissionCounts(); resolves != 1 || begins != 1 {
		t.Fatalf("queued Start reached journal = Resolve %d, Begin %d; want 1, 1", resolves, begins)
	}
	if err := runtime.Close(); err != nil || trackedJournal.closeCount() != 1 {
		t.Fatalf("repeated runtime Close = %v, journal close calls %d", err, trackedJournal.closeCount())
	}

	reopened, err := journal.Open(t.Context(), journalPath, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	accepted, acceptedErr = reopened.Get(t.Context(), "runtime-01", "accepted-start")
	_, queuedErr = reopened.Get(t.Context(), "runtime-01", "queued-start")
	if acceptedErr != nil || accepted.State != journal.StateSucceeded {
		t.Fatalf("drained durable execution = %+v, %v", accepted, acceptedErr)
	}
	if !errors.Is(queuedErr, journal.ErrOperationNotFound) {
		t.Fatalf("queued Start wrote durable intent during shutdown: %v", queuedErr)
	}
}

func TestServeReportsListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	err = serve(context.Background(), listener, http.NewServeMux())
	if err == nil || !strings.Contains(err.Error(), "closed network connection") {
		t.Fatalf("serve() error = %v", err)
	}
}

func TestServeForcesCloseAfterShutdownTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- serveWithShutdownTimeout(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(requestStarted)
			<-releaseRequest
			w.WriteHeader(http.StatusNoContent)
		}), 10*time.Millisecond)
	}()
	requestResult := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			_ = response.Body.Close()
		}
		requestResult <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("request did not reach handler")
	}
	cancel()
	select {
	case err := <-serveResult:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("serveWithShutdownTimeout() error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveWithShutdownTimeout() remained blocked after shutdown timeout")
	}
	close(releaseRequest)
	select {
	case <-requestResult:
	case <-time.After(time.Second):
		t.Fatal("forced-close request did not finish")
	}
}

type runtimeResponse struct {
	ProtocolVersion       string                   `json:"protocolVersion"`
	RuntimeIdentity       string                   `json:"runtimeIdentity"`
	RuntimeGeneration     uint64                   `json:"runtimeGeneration"`
	State                 string                   `json:"state"`
	CPAObservedVersion    string                   `json:"cpaObservedVersion"`
	ActiveGatewayArtifact *runtimeArtifactResponse `json:"activeGatewayArtifact"`
	Capabilities          []string                 `json:"capabilities"`
	Recovery              *runtimeRecoveryResponse `json:"recovery"`
}

type runtimeArtifactResponse struct {
	Engine     string `json:"engine"`
	ArtifactID string `json:"artifactId"`
	Version    string `json:"version"`
}

type runtimeRecoveryResponse struct {
	State             string `json:"state"`
	AttemptsRemaining int    `json:"attemptsRemaining"`
}

func validConfigValues() map[string]string {
	return map[string]string{
		"CPAMP_RUNTIME_IDENTITY": "runtime-01",
		"CPAMP_RUNTIME_TOKEN":    "runtime-token",
	}
}

func fixedGeneration(generation uint64) generationSource {
	return func() (uint64, error) {
		return generation, nil
	}
}

func loadTestConfig(t *testing.T, source generationSource) config {
	t.Helper()
	values := validConfigValues()
	cfg, err := loadConfig(func(key string) string { return values[key] }, source)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	return cfg
}

func requestRuntime(t *testing.T, handler http.Handler, path string) runtimeResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer runtime-token")
	if path == "/v1/runtime/status" {
		req.Header.Set(protocol.ArtifactObservationHeader, protocol.ArtifactObservationFeature)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", path, recorder.Code, recorder.Body.String())
	}
	var response runtimeResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode GET %s response: %v", path, err)
	}
	return response
}

type shutdownAdmissionObserver struct {
	*runtimeHandler
	once   sync.Once
	closed chan struct{}
}

func (h *shutdownAdmissionObserver) CloseAdmission() {
	h.runtimeHandler.CloseAdmission()
	h.once.Do(func() { close(h.closed) })
}

type queuedStartExecutor struct {
	next        protocol.StartExecutor
	operationID string
	waiting     chan struct{}
	once        sync.Once
}

func (e *queuedStartExecutor) Start(ctx context.Context, request lifecycle.StartRequest) (journal.Operation, error) {
	if request.OperationID != e.operationID {
		return e.next.Start(ctx, request)
	}
	type result struct {
		operation journal.Operation
		err       error
	}
	completed := make(chan result, 1)
	go func() {
		operation, err := e.next.Start(ctx, request)
		completed <- result{operation: operation, err: err}
	}()
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	select {
	case got := <-completed:
		return got.operation, got.err
	case <-timer.C:
		e.once.Do(func() { close(e.waiting) })
		got := <-completed
		return got.operation, got.err
	}
}

type shutdownJournal struct {
	*journal.Store
	mu           sync.Mutex
	resolveCalls int
	beginCalls   int
	closeCalls   int
}

func (j *shutdownJournal) Resolve(ctx context.Context, authority journal.Authority, intent journal.Intent) (journal.Operation, bool, error) {
	j.mu.Lock()
	j.resolveCalls++
	j.mu.Unlock()
	return j.Store.Resolve(ctx, authority, intent)
}

func (j *shutdownJournal) Begin(ctx context.Context, authority journal.Authority, intent journal.Intent) (journal.Operation, bool, error) {
	j.mu.Lock()
	j.beginCalls++
	j.mu.Unlock()
	return j.Store.Begin(ctx, authority, intent)
}

func (j *shutdownJournal) Close() error {
	j.mu.Lock()
	j.closeCalls++
	j.mu.Unlock()
	return j.Store.Close()
}

func (j *shutdownJournal) closeCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.closeCalls
}

func (j *shutdownJournal) submissionCounts() (int, int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.resolveCalls, j.beginCalls
}

type shutdownProcess struct {
	mu          sync.Mutex
	observation cpaprocess.Observation
	starts      int
	entered     chan struct{}
	release     <-chan struct{}
	once        sync.Once
}

func (p *shutdownProcess) Observe() cpaprocess.Observation {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.observation.State == "" {
		return cpaprocess.Observation{State: cpaprocess.StateNotStarted}
	}
	return p.observation
}

func (p *shutdownProcess) Start(ctx context.Context, _ cpaprocess.StartSpec) (cpaprocess.Observation, error) {
	p.mu.Lock()
	p.starts++
	p.mu.Unlock()
	p.once.Do(func() { close(p.entered) })
	select {
	case <-p.release:
	case <-ctx.Done():
		return p.Observe(), ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.observation = cpaprocess.Observation{State: cpaprocess.StateRunning, PID: 123}
	return p.observation, nil
}

func (p *shutdownProcess) PrepareStop() (cpaprocess.StopTarget, error) {
	return nil, cpaprocess.ErrStateConflict
}

func (p *shutdownProcess) ExitEvents() <-chan cpaprocess.ExitEvent { return nil }

func (p *shutdownProcess) startCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.starts
}

type shutdownHTTPResult struct {
	status int
	body   string
	err    error
}
