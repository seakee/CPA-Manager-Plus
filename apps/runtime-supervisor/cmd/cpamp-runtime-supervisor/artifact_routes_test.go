package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/protocol"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/readiness"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

type routeTestHarness struct {
	handler       http.Handler
	stageRoot     string
	selectionRoot string
	pinnedID      string
	archive       []byte
	executor      *lifecycle.Executor
	bundledPath   string
	manifestPath  string
}

func newRouteTestHarness(t *testing.T, opts ...lifecycle.AcquisitionOption) *routeTestHarness {
	t.Helper()
	root := t.TempDir()
	supervisorRoot := filepath.Join(root, "supervisor")
	stageRoot := filepath.Join(supervisorRoot, "artifacts", "cpa")
	selectionRoot := filepath.Join(supervisorRoot, "active", "cpa")
	missingBundled := filepath.Join(root, "missing-cpa")
	manifestPath := filepath.Join(root, "cpa-artifact.json")

	binary := []byte("#!/bin/sh\necho s2a-route-cpa\n")
	sum := sha256.Sum256(binary)
	pinnedID := "sha256:" + hex.EncodeToString(sum[:])
	archive := buildRouteTarGzip(t, binary, []byte("MIT License\n"))

	manifestJSON := fmt.Sprintf(
		"{\"schemaVersion\":1,\"engine\":\"cpa\",\"version\":\"8.0.55\",\"artifactId\":%q}\n",
		pinnedID,
	)
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	stageStore, err := runtimeupdate.NewStore(stageRoot)
	if err != nil {
		t.Fatal(err)
	}
	selectionStore, err := selection.NewStore(selectionRoot, stageStore)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectionStore.Load(selection.Bundled(missingBundled, manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	artifactObserver := artifact.NewObserver(selected.ExecutablePath, selected.MetadataPath)
	_ = artifactObserver.RefreshFrom(selected.ExecutablePath, selected.MetadataPath)

	child := cpaprocess.NewManager(func(spec cpaprocess.StartSpec) error {
		meta := manifestPath
		if spec.Executable != missingBundled {
			meta = filepath.Join(filepath.Dir(spec.Executable), "artifact.json")
		}
		return artifactObserver.RefreshFrom(spec.Executable, meta)
	})
	observer, err := readiness.New(child, "127.0.0.1:18399")
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(t.Context(), filepath.Join(supervisorRoot, "operations.sqlite"), journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := lifecycle.NewExecutor(
		journal.Authority{RuntimeIdentity: "runtime-01", RuntimeGeneration: 41},
		store,
		child,
		selected.ExecutablePath,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.Close() })

	if err := executor.EnableActivateUpdate(selected, artifactObserver, selectionStore, observer); err != nil {
		t.Fatal(err)
	}
	protocolHandler, err := protocol.NewHandler(protocol.Config{
		RuntimeIdentity:   "runtime-01",
		RuntimeGeneration: 41,
		Token:             "runtime-token",
		Start:             executor,
		Stop:              executor,
		Restart:           executor,
		Status:            observer,
		Recovery:          executor,
		Artifact:          artifactObserver,
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config{
		runtimeIdentity:     "runtime-01",
		runtimeGeneration:   41,
		token:               "runtime-token",
		journalPath:         filepath.Join(supervisorRoot, "operations.sqlite"),
		cpaExecutable:       missingBundled,
		cpaArtifactManifest: manifestPath,
	}
	rh := &runtimeHandler{Handler: protocolHandler, executor: executor}
	allOpts := append([]lifecycle.AcquisitionOption{lifecycle.WithAcquisitionPlatform("linux", "amd64")}, opts...)
	if err := enableArtifactRoutes(cfg, rh, allOpts...); err != nil {
		t.Fatal(err)
	}

	return &routeTestHarness{
		handler:       rh,
		stageRoot:     stageRoot,
		selectionRoot: selectionRoot,
		pinnedID:      pinnedID,
		archive:       archive,
		executor:      executor,
		bundledPath:   missingBundled,
		manifestPath:  manifestPath,
	}
}

func TestArtifactRoutesRequireConstantTimeBearerAuthorization(t *testing.T) {
	h := newRouteTestHarness(t)
	routes := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: artifactStatusPath},
		{method: http.MethodPost, path: artifactProbePath},
		{method: http.MethodPost, path: artifactAcquirePath},
		{method: http.MethodPut, path: artifactUploadPath},
	}
	authHeaders := []string{
		"",
		"Basic dXNlcjpwYXNz",
		"Bearer",
		"Bearer wrong-token",
		"Bearer runtime-token extra",
	}

	for _, route := range routes {
		for _, auth := range authHeaders {
			req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s with auth %q status = %d, want 401", route.method, route.path, auth, rec.Code)
			}
			if rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("%s %s WWW-Authenticate = %q, want Bearer", route.method, route.path, rec.Header().Get("WWW-Authenticate"))
			}
			var errResp artifactErrorResponse
			if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil || errResp.Code != lifecycle.ArtifactCodeUnauthorized {
				t.Fatalf("%s %s error body = %+v, err=%v", route.method, route.path, errResp, err)
			}
		}
	}
}

func TestArtifactRoutesRejectUnknownFieldsAndInvalidRequests(t *testing.T) {
	h := newRouteTestHarness(t)

	invalidProbes := []string{
		"",
		`{"unexpected":true}`,
		`{}{}`,
		strings.Repeat(" ", maxArtifactJSONRequestBytes+1),
	}
	for _, body := range invalidProbes {
		req := httptest.NewRequest(http.MethodPost, artifactProbePath, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runtime-token")
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST probe(%q) status = %d, want 400", body, rec.Code)
		}
		assertArtifactErrorCode(t, rec, lifecycle.ArtifactCodeInvalidRequest)
	}

	invalidAcquires := []string{
		"",
		`{}`,
		`{"source":"unknown"}`,
		`{"source":"github","cdnPrefix":"https://cdn.example.com"}`,
		`{"source":"cdn"}`,
		`{"source":"cdn","cdnPrefix":"http://cdn.example.com"}`,
		`{"source":"cdn","cdnPrefix":"https://user@cdn.example.com"}`,
		`{"source":"github","unknownField":1}`,
		`{"source":"github"}` + strings.Repeat(" ", maxArtifactJSONRequestBytes),
	}
	for _, body := range invalidAcquires {
		req := httptest.NewRequest(http.MethodPost, artifactAcquirePath, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runtime-token")
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST acquire(%d bytes) status = %d, want 400", len(body), rec.Code)
		}
		assertArtifactErrorCode(t, rec, lifecycle.ArtifactCodeInvalidRequest)
	}

	// Wrong HTTP method returns 405 with invalid_request.
	req := httptest.NewRequest(http.MethodPost, artifactStatusPath, nil)
	req.Header.Set("Authorization", "Bearer runtime-token")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %s status = %d, want 405", artifactStatusPath, rec.Code)
	}
	assertArtifactErrorCode(t, rec, lifecycle.ArtifactCodeInvalidRequest)
}

func TestArtifactUploadOver64MiBReturns413AndSetsDeadlines(t *testing.T) {
	h := newRouteTestHarness(t)

	// Streaming reader exceeding 64 MiB without Content-Length header.
	oversizeStream := io.LimitReader(zeroReader{}, runtimeupdate.MaxAcquisitionArchiveBytes+1024)
	req := httptest.NewRequest(http.MethodPut, artifactUploadPath, oversizeStream)
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer runtime-token")
	req.Header.Set("Content-Type", "application/gzip")
	writer := &readWriteDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()
	h.handler.ServeHTTP(writer, req)
	after := time.Now()
	if writer.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("PUT oversize upload status = %d, want 413 (body=%s)", writer.Code, writer.Body.String())
	}
	assertArtifactErrorCode(t, writer.ResponseRecorder, lifecycle.ArtifactCodeArchiveTooLarge)
	if len(writer.readDeadlines) != 2 || len(writer.writeDeadlines) != 1 {
		t.Fatalf("upload deadlines read=%d write=%d, want 2 read and 1 active write", len(writer.readDeadlines), len(writer.writeDeadlines))
	}
	if writer.readDeadlines[0].Before(before.Add(uploadArtifactBudgetTimeout)) ||
		writer.readDeadlines[0].After(after.Add(uploadArtifactBudgetTimeout)) {
		t.Fatalf("upload read deadline = %v, want ~%s", writer.readDeadlines[0], uploadArtifactBudgetTimeout)
	}
	if writer.writeDeadlines[0].Before(before.Add(uploadArtifactWriteBudgetTimeout)) ||
		writer.writeDeadlines[0].After(after.Add(uploadArtifactWriteBudgetTimeout)) ||
		!writer.writeDeadlines[0].After(writer.readDeadlines[0]) {
		t.Fatalf("upload write deadline = %v (read=%v), want ~%s (> read budget)", writer.writeDeadlines[0], writer.readDeadlines[0], uploadArtifactWriteBudgetTimeout)
	}
	if writer.readDeadlines[1].IsZero() || writer.readDeadlines[1].Before(before) || writer.readDeadlines[1].After(after) {
		t.Fatalf("unconsumed oversize upload final read deadline = %v, want immediate expiration in [%v, %v]", writer.readDeadlines[1], before, after)
	}
	if got := writer.Header().Get("Connection"); got != "close" {
		t.Fatalf("oversize upload Connection = %q, want close", got)
	}

	entries, err := os.ReadDir(h.stageRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("oversize upload left stage state: %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(h.selectionRoot, "selection.json")); !os.IsNotExist(err) {
		t.Fatalf("oversize upload created selection.json: %v", err)
	}
}

func TestArtifactUploadEmptyOrInvalidLengthsRejectedBeforeDeadlinesOrOperationMutation(t *testing.T) {
	h := newRouteTestHarness(
		t,
		lifecycle.WithAcquisitionCDNAddrPolicy(nil),
		lifecycle.WithAcquisitionCDNLookupIP(func(_ context.Context, host string) ([]net.IPAddr, error) {
			if host == "api.github.com" {
				return []net.IPAddr{{IP: net.ParseIP("140.82.121.6")}}, nil
			}
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}),
	)

	// Seed a retained operation on state.currentOp and wait for it to settle.
	acqReq := httptest.NewRequest(http.MethodPost, artifactAcquirePath, strings.NewReader(`{"source":"cdn","cdnPrefix":"https://cdn.example.com/mirror"}`))
	acqReq.Header.Set("Authorization", "Bearer runtime-token")
	acqRec := httptest.NewRecorder()
	h.handler.ServeHTTP(acqRec, acqReq)
	if acqRec.Code != http.StatusAccepted {
		t.Fatalf("seed POST acquire = %d (%s), want 202", acqRec.Code, acqRec.Body.String())
	}
	var seededOpID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stRec := httptest.NewRecorder()
		stReq := httptest.NewRequest(http.MethodGet, artifactStatusPath, nil)
		stReq.Header.Set("Authorization", "Bearer runtime-token")
		h.handler.ServeHTTP(stRec, stReq)
		var st lifecycle.ArtifactStatusResponse
		if err := json.NewDecoder(stRec.Body).Decode(&st); err == nil && st.Operation != nil && st.Operation.Phase == lifecycle.ArtifactPhaseFailed {
			seededOpID = st.Operation.ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if seededOpID == "" {
		t.Fatal("seeded acquire operation did not settle to failed")
	}

	for _, tc := range []struct {
		name   string
		body   io.Reader
		length int64
	}{
		{name: "ContentLength 0 with empty reader", body: strings.NewReader(""), length: 0},
		{name: "ContentLength 0 with nil body", body: nil, length: 0},
		{name: "ContentLength -2", body: strings.NewReader("ab"), length: -2},
		{name: "http.NoBody with ContentLength -1", body: nil, length: -1},
		{name: "http.NoBody with positive ContentLength", body: nil, length: 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, artifactUploadPath, tc.body)
			req.ContentLength = tc.length
			req.Header.Set("Authorization", "Bearer runtime-token")
			req.Header.Set("Content-Type", "application/gzip")
			writer := &readWriteDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			h.handler.ServeHTTP(writer, req)
			after := time.Now()
			if writer.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", writer.Code, writer.Body.String())
			}
			assertArtifactErrorCode(t, writer.ResponseRecorder, lifecycle.ArtifactCodeInvalidRequest)
			if len(writer.writeDeadlines) != 0 {
				t.Fatalf("unexpected write deadlines=%v", writer.writeDeadlines)
			}
			if tc.body != nil {
				if len(writer.readDeadlines) != 1 || writer.readDeadlines[0].IsZero() || writer.readDeadlines[0].After(after.Add(time.Second)) {
					t.Fatalf("expected immediate read deadline expiration for unconsumed body, got %v", writer.readDeadlines)
				}
			} else if len(writer.readDeadlines) != 0 {
				t.Fatalf("unexpected read deadlines for nil/NoBody=%v", writer.readDeadlines)
			}
			stRec := httptest.NewRecorder()
			stReq := httptest.NewRequest(http.MethodGet, artifactStatusPath, nil)
			stReq.Header.Set("Authorization", "Bearer runtime-token")
			h.handler.ServeHTTP(stRec, stReq)
			var st lifecycle.ArtifactStatusResponse
			if err := json.NewDecoder(stRec.Body).Decode(&st); err != nil {
				t.Fatal(err)
			}
			if st.Operation == nil || st.Operation.ID != seededOpID || st.Operation.Source != lifecycle.ArtifactSourceCDN {
				t.Fatalf("rejected empty upload mutated currentOp: got %+v, want %q", st.Operation, seededOpID)
			}
		})
	}
}

func TestArtifactUploadStalledOpenSocketsCloseAndShutdownPromptly(t *testing.T) {
	h := newRouteTestHarness(t)

	// Upload valid archive first so subsequent uploads enter serveUpload, extend budgets,
	// and return 409 artifact_already_active without consuming the request stream.
	initReq := httptest.NewRequest(http.MethodPut, artifactUploadPath, bytes.NewReader(h.archive))
	initReq.Header.Set("Authorization", "Bearer runtime-token")
	initReq.Header.Set("Content-Type", "application/gzip")
	initRec := &readWriteDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	beforeInit := time.Now()
	h.handler.ServeHTTP(initRec, initReq)
	afterInit := time.Now()
	if initRec.Code != http.StatusOK {
		t.Fatalf("initial PUT upload = %d (%s)", initRec.Code, initRec.Body.String())
	}
	if len(initRec.readDeadlines) != 2 || len(initRec.writeDeadlines) != 1 ||
		!initRec.readDeadlines[1].IsZero() ||
		initRec.writeDeadlines[0].Before(beforeInit.Add(uploadArtifactWriteBudgetTimeout)) ||
		initRec.writeDeadlines[0].After(afterInit.Add(uploadArtifactWriteBudgetTimeout)) {
		t.Fatalf("consumed upload deadlines read=%v write=%v", initRec.readDeadlines, initRec.writeDeadlines)
	}

	// Also start a second harness for chunked >64 MiB 413 over real TCP with open socket.
	oversizeHarness := newRouteTestHarness(t)

	runOpenSocketServerChecks := func(t *testing.T, handler http.Handler, cases []struct {
		name       string
		send       func(conn net.Conn) error
		wantStatus int
	}) {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := newHTTPServer(handler)
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

		parentT := t
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				start := time.Now()
				conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				// Leave client socket open until after server.Shutdown completes.
				parentT.Cleanup(func() { _ = conn.Close() })
				if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if err := tc.send(conn); err != nil {
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
					t.Fatalf("open-socket upload delayed response for %s; want < 1s", elapsed)
				}
				var extra [1]byte
				if _, eofErr := br.Read(extra[:]); !errors.Is(eofErr, io.EOF) {
					t.Fatalf("server kept unconsumed connection open instead of aborting 256 KiB drain: err = %v", eofErr)
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

	t.Run("409 already active open sockets", func(t *testing.T) {
		runOpenSocketServerChecks(t, h.handler, []struct {
			name       string
			send       func(conn net.Conn) error
			wantStatus int
		}{
			{
				name: "Content-Length<=256KiB",
				send: func(conn net.Conn) error {
					_, err := fmt.Fprintf(
						conn,
						"PUT %s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nContent-Type: application/gzip\r\nContent-Length: 4096\r\n\r\na",
						artifactUploadPath,
					)
					return err
				},
				wantStatus: http.StatusConflict,
			},
			{
				name: "Transfer-Encoding chunked",
				send: func(conn net.Conn) error {
					_, err := fmt.Fprintf(
						conn,
						"PUT %s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nContent-Type: application/gzip\r\nTransfer-Encoding: chunked\r\n\r\n1\r\na\r\n",
						artifactUploadPath,
					)
					return err
				},
				wantStatus: http.StatusConflict,
			},
		})
	})

	t.Run("413 chunked overflow open socket", func(t *testing.T) {
		runOpenSocketServerChecks(t, oversizeHarness.handler, []struct {
			name       string
			send       func(conn net.Conn) error
			wantStatus int
		}{
			{
				name: "Transfer-Encoding chunked over 64MiB without closing chunk",
				send: func(conn net.Conn) error {
					const chunkLen = runtimeupdate.MaxAcquisitionArchiveBytes + 4096
					header := fmt.Sprintf(
						"PUT %s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer runtime-token\r\nContent-Type: application/gzip\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n",
						artifactUploadPath,
						chunkLen,
					)
					if _, err := io.WriteString(conn, header); err != nil {
						return err
					}
					// Stream 64 MiB + 1024 bytes asynchronously while leaving 3072 bytes + trailing chunk framing unsent on the open socket.
					go func() {
						_, _ = io.CopyN(conn, zeroReader{}, runtimeupdate.MaxAcquisitionArchiveBytes+1024)
					}()
					return nil
				},
				wantStatus: http.StatusRequestEntityTooLarge,
			},
		})
	})
}

func TestArtifactRoutesUploadAndIdempotentAcquireAndProtocolCoexistence(t *testing.T) {
	h := newRouteTestHarness(t)

	// Initial GET /v1/runtime/artifact shows active=null, required=true.
	statusRec := httptest.NewRecorder()
	statusReq := httptest.NewRequest(http.MethodGet, artifactStatusPath, nil)
	statusReq.Header.Set("Authorization", "Bearer runtime-token")
	h.handler.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, %s", statusRec.Code, statusRec.Body.String())
	}
	var before lifecycle.ArtifactStatusResponse
	if err := json.NewDecoder(statusRec.Body).Decode(&before); err != nil {
		t.Fatal(err)
	}
	if before.Active != nil || !before.Required || before.Pinned.Version != "8.0.55" || string(before.Pinned.ArtifactID) != h.pinnedID {
		t.Fatalf("initial status = %+v", before)
	}

	// Synchronous PUT /v1/runtime/artifact/upload succeeds with 200.
	uploadReq := httptest.NewRequest(http.MethodPut, artifactUploadPath, bytes.NewReader(h.archive))
	uploadReq.Header.Set("Authorization", "Bearer runtime-token")
	uploadReq.Header.Set("Content-Type", "application/gzip")
	uploadRec := httptest.NewRecorder()
	h.handler.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("PUT upload = %d, %s", uploadRec.Code, uploadRec.Body.String())
	}
	var uploadResp lifecycle.ArtifactOperationResponse
	if err := json.NewDecoder(uploadRec.Body).Decode(&uploadResp); err != nil {
		t.Fatal(err)
	}
	if uploadResp.Operation.Source != lifecycle.ArtifactSourceUpload ||
		uploadResp.Operation.Phase != lifecycle.ArtifactPhaseSucceeded ||
		uploadResp.Operation.Code != "" ||
		uploadResp.Operation.ReceivedBytes != int64(len(h.archive)) {
		t.Fatalf("upload operation = %+v", uploadResp.Operation)
	}

	// Subsequent POST /v1/runtime/artifact/acquire returns 200 (idempotent).
	acqReq := httptest.NewRequest(http.MethodPost, artifactAcquirePath, strings.NewReader(`{"source":"github"}`))
	acqReq.Header.Set("Authorization", "Bearer runtime-token")
	acqRec := httptest.NewRecorder()
	h.handler.ServeHTTP(acqRec, acqReq)
	if acqRec.Code != http.StatusOK {
		t.Fatalf("idempotent POST acquire = %d, %s", acqRec.Code, acqRec.Body.String())
	}

	// Second PUT /v1/runtime/artifact/upload returns 409 artifact_already_active.
	secondUploadReq := httptest.NewRequest(http.MethodPut, artifactUploadPath, bytes.NewReader(h.archive))
	secondUploadReq.Header.Set("Authorization", "Bearer runtime-token")
	secondUploadReq.Header.Set("Content-Type", "application/gzip")
	secondUploadRec := httptest.NewRecorder()
	h.handler.ServeHTTP(secondUploadRec, secondUploadReq)
	if secondUploadRec.Code != http.StatusConflict {
		t.Fatalf("second PUT upload = %d, want 409 (%s)", secondUploadRec.Code, secondUploadRec.Body.String())
	}
	assertArtifactErrorCode(t, secondUploadRec, lifecycle.ArtifactCodeArtifactAlreadyActive)

	// Post-commit failure maps to 500 recovery_required.
	recoveryHarness := newRouteTestHarness(t, lifecycle.WithAcquisitionPostCommitHooks(
		func() error { return fmt.Errorf("post-commit refresh failure") },
		nil,
	))
	recReq := httptest.NewRequest(http.MethodPut, artifactUploadPath, bytes.NewReader(recoveryHarness.archive))
	recReq.Header.Set("Authorization", "Bearer runtime-token")
	recReq.Header.Set("Content-Type", "application/gzip")
	recResp := httptest.NewRecorder()
	recoveryHarness.handler.ServeHTTP(recResp, recReq)
	if recResp.Code != http.StatusInternalServerError {
		t.Fatalf("post-commit failure PUT upload = %d, want 500 (%s)", recResp.Code, recResp.Body.String())
	}
	assertArtifactErrorCode(t, recResp, lifecycle.ArtifactCodeRecoveryRequired)

	// Runtime Protocol /v1/runtime/status now reports the acquired ActiveGatewayArtifact.
	rtStatus := requestRuntime(t, h.handler, "/v1/runtime/status")
	if rtStatus.ActiveGatewayArtifact == nil || rtStatus.ActiveGatewayArtifact.ArtifactID != h.pinnedID ||
		rtStatus.ActiveGatewayArtifact.Version != "8.0.55" {
		t.Fatalf("runtime status after upload = %+v", rtStatus)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type readWriteDeadlineRecorder struct {
	*httptest.ResponseRecorder
	readDeadlines  []time.Time
	writeDeadlines []time.Time
}

func (r *readWriteDeadlineRecorder) SetReadDeadline(d time.Time) error {
	r.readDeadlines = append(r.readDeadlines, d)
	return nil
}

func (r *readWriteDeadlineRecorder) SetWriteDeadline(d time.Time) error {
	r.writeDeadlines = append(r.writeDeadlines, d)
	return nil
}

func assertArtifactErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var got artifactErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode error response %q: %v", rec.Body.String(), err)
	}
	if got.Code != want {
		t.Fatalf("error code = %q, want %q", got.Code, want)
	}
}

func buildRouteTarGzip(t *testing.T, binary, license []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{name: "cli-proxy-api", data: binary},
		{name: "LICENSE", data: license},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name:     entry.name,
			Typeflag: tar.TypeReg,
			Mode:     0o755,
			Size:     int64(len(entry.data)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEnableArtifactRoutesNoOpWhenExecutorNil(t *testing.T) {
	cfg := loadTestConfig(t, fixedGeneration(41))
	rh, err := newRuntimeHandler(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := enableArtifactRoutes(cfg, rh); err != nil {
		t.Fatalf("enableArtifactRoutes(read-only) error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, artifactStatusPath, nil)
	req.Header.Set("Authorization", "Bearer runtime-token")
	rec := httptest.NewRecorder()
	rh.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("read-only GET artifact status = %d, want 409", rec.Code)
	}
	assertArtifactErrorCode(t, rec, lifecycle.ArtifactCodeUnsupportedPlatform)
}

func TestRunStartsAndAnswersStatusWithMinimalAdoptionEnvAndPrefixedManifestVersion(t *testing.T) {
	for _, tc := range []struct {
		name             string
		manifestMode     string
		goos             string
		goarch           string
		wantArtifactCode string
		wantHTTPStatus   int
	}{
		{
			name:             "v-prefixed version manifest from adoption harness returns 500 install_failed on artifact routes",
			manifestMode:     "v-prefixed",
			goos:             "linux",
			goarch:           "amd64",
			wantArtifactCode: lifecycle.ArtifactCodeInstallFailed,
			wantHTTPStatus:   http.StatusInternalServerError,
		},
		{
			name:             "configured missing manifest file returns 500 install_failed on artifact routes",
			manifestMode:     "missing-file",
			goos:             "linux",
			goarch:           "amd64",
			wantArtifactCode: lifecycle.ArtifactCodeInstallFailed,
			wantHTTPStatus:   http.StatusInternalServerError,
		},
		{
			name:             "unset manifest path returns 409 unsupported_platform on artifact routes",
			manifestMode:     "unset",
			goos:             "linux",
			goarch:           "amd64",
			wantArtifactCode: lifecycle.ArtifactCodeUnsupportedPlatform,
			wantHTTPStatus:   http.StatusConflict,
		},
		{
			name:             "unsupported platform darwin/amd64 returns 409 unsupported_platform on GET, probe, acquire, and upload",
			manifestMode:     "canonical",
			goos:             "darwin",
			goarch:           "amd64",
			wantArtifactCode: lifecycle.ArtifactCodeUnsupportedPlatform,
			wantHTTPStatus:   http.StatusConflict,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			targetRoot := t.TempDir()
			cpaPath := filepath.Join(targetRoot, "cli-proxy-api")
			binary := []byte("#!/bin/sh\necho adoption-cpa\n")
			if err := os.WriteFile(cpaPath, binary, 0o755); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(binary)
			artifactID := "sha256:" + hex.EncodeToString(sum[:])
			archive := buildRouteTarGzip(t, binary, []byte("MIT License\n"))

			var manifestPath string
			switch tc.manifestMode {
			case "canonical":
				manifestPath = filepath.Join(targetRoot, "artifact.json")
				content := fmt.Sprintf(`{"schemaVersion":1,"engine":"cpa","version":"8.0.55","artifactId":%q}`, artifactID)
				if err := os.WriteFile(manifestPath, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			case "v-prefixed":
				manifestPath = filepath.Join(targetRoot, "artifact.json")
				content := fmt.Sprintf(`{"schemaVersion":1,"engine":"cpa","version":"v8.0.55","artifactId":%q}`, artifactID)
				if err := os.WriteFile(manifestPath, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-file":
				manifestPath = filepath.Join(targetRoot, "missing-artifact.json")
			case "unset":
				manifestPath = ""
			}
			env := map[string]string{
				"CPAMP_RUNTIME_ADDR":          "127.0.0.1:0",
				"CPAMP_RUNTIME_IDENTITY":      "isolated-adopt-runtime",
				"CPAMP_RUNTIME_TOKEN":         "isolated-runtime-transport-token",
				"CPAMP_RUNTIME_JOURNAL_PATH":  filepath.Join(targetRoot, "operations.sqlite"),
				"CPAMP_CPA_EXECUTABLE":        cpaPath,
				"CPAMP_CPA_ARTIFACT_MANIFEST": manifestPath,
				"CPAMP_RUNTIME_CPA_ADDR":      "127.0.0.1:18399",
			}
			cfg, err := loadConfig(func(key string) string { return env[key] }, fixedGeneration(77))
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}

			rh, err := newRuntimeHandler(t.Context(), cfg)
			if err != nil {
				t.Fatalf("newRuntimeHandler() error = %v", err)
			}
			t.Cleanup(func() { _ = rh.Close() })

			if err := enableArtifactRoutes(cfg, rh, lifecycle.WithAcquisitionPlatform(tc.goos, tc.goarch)); err != nil {
				t.Fatalf("enableArtifactRoutes() error = %v", err)
			}

			// /v1/runtime/status stays 200 in every configuration.
			req := httptest.NewRequest(http.MethodGet, "/v1/runtime/status", nil)
			req.Header.Set("Authorization", "Bearer isolated-runtime-transport-token")
			req.Header.Set(protocol.ArtifactObservationHeader, protocol.ArtifactObservationFeature)
			rec := httptest.NewRecorder()
			rh.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /v1/runtime/status = %d (%s), want 200", rec.Code, rec.Body.String())
			}
			var status runtimeResponse
			if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
				t.Fatalf("decode /v1/runtime/status: %v", err)
			}
			if status.RuntimeIdentity != "isolated-adopt-runtime" || status.ActiveGatewayArtifact == nil || status.ActiveGatewayArtifact.ArtifactID != artifactID {
				t.Fatalf("/v1/runtime/status = %+v, want identity isolated-adopt-runtime and artifact %s", status, artifactID)
			}

			// All four artifact routes return the expected error class (409 unsupported_platform vs 500 install_failed).
			for _, route := range []struct {
				method      string
				path        string
				contentType string
				body        []byte
			}{
				{method: http.MethodGet, path: artifactStatusPath},
				{method: http.MethodPost, path: artifactProbePath, contentType: "application/json", body: []byte(`{}`)},
				{method: http.MethodPost, path: artifactAcquirePath, contentType: "application/json", body: []byte(`{"source":"github"}`)},
				{method: http.MethodPut, path: artifactUploadPath, contentType: "application/gzip", body: archive},
			} {
				artReq := httptest.NewRequest(route.method, route.path, bytes.NewReader(route.body))
				artReq.Header.Set("Authorization", "Bearer isolated-runtime-transport-token")
				if route.contentType != "" {
					artReq.Header.Set("Content-Type", route.contentType)
				}
				artRec := httptest.NewRecorder()
				rh.ServeHTTP(artRec, artReq)
				if artRec.Code != tc.wantHTTPStatus {
					t.Fatalf("%s %s status = %d (%s), want %d", route.method, route.path, artRec.Code, artRec.Body.String(), tc.wantHTTPStatus)
				}
				assertArtifactErrorCode(t, artRec, tc.wantArtifactCode)
			}
		})
	}
}

func TestArtifactRoutesUnconsumedEarlyRejectionClosesOpenHTTP11SocketPromptly(t *testing.T) {
	h := newRouteTestHarness(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer(h.handler)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	raw := fmt.Sprintf(
		"PUT %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/gzip\r\nContent-Length: 4096\r\n\r\npartial",
		artifactUploadPath,
		ln.Addr().String(),
	)
	started := time.Now()
	if _, err := conn.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(conn)
	parseReq, _ := http.NewRequest(http.MethodPut, "http://127.0.0.1"+artifactUploadPath, nil)
	resp, err := http.ReadResponse(br, parseReq)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var extra [1]byte
	n, closeErr := br.Read(extra[:])
	elapsed := time.Since(started)
	if resp.StatusCode != http.StatusUnauthorized || !resp.Close {
		t.Fatalf("resp = (%d, close=%v), want (401, true)", resp.StatusCode, resp.Close)
	}
	if elapsed >= time.Second {
		t.Fatalf("unconsumed artifact socket close took %v, want immediate close well below 10s ReadTimeout", elapsed)
	}
	if n != 0 || closeErr == nil || errors.Is(closeErr, os.ErrDeadlineExceeded) {
		t.Fatalf("post-response read = (%d, %v), want immediate EOF/close", n, closeErr)
	}
}

func TestArtifactResponseWriterImplicitWriteCommitsHeaderAndClosesWhenUnconsumed(t *testing.T) {
	t.Run("unconsumed_body_sets_connection_close_and_expires_read_deadline_once", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, artifactUploadPath, strings.NewReader("unconsumed-archive-bytes"))
		recorder := &readWriteDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		wrapped := &artifactResponseWriter{
			ResponseWriter: recorder,
			body:           trackArtifactRequestBody(req),
		}
		before := time.Now()
		n, err := wrapped.Write([]byte("part1-"))
		after := time.Now()
		if err != nil || n != len("part1-") {
			t.Fatalf("first Write = (%d, %v), want (%d, nil)", n, err, len("part1-"))
		}
		if !wrapped.wroteHeader {
			t.Fatal("expected wroteHeader=true after implicit 200 Write")
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", recorder.Code)
		}
		if got := recorder.Header().Get("Connection"); got != "close" {
			t.Fatalf("Connection = %q, want close", got)
		}
		if len(recorder.readDeadlines) != 1 || recorder.readDeadlines[0].IsZero() ||
			recorder.readDeadlines[0].Before(before) || recorder.readDeadlines[0].After(after) {
			t.Fatalf("readDeadlines = %v, want 1 immediate deadline in [%v, %v]", recorder.readDeadlines, before, after)
		}
		if _, err := wrapped.Write([]byte("part2")); err != nil {
			t.Fatalf("second Write error = %v", err)
		}
		if len(recorder.readDeadlines) != 1 {
			t.Fatalf("readDeadlines after second Write = %v, want 1", recorder.readDeadlines)
		}
		if got := recorder.Body.String(); got != "part1-part2" {
			t.Fatalf("body = %q, want part1-part2", got)
		}
	})

	t.Run("consumed_body_commits_200_without_connection_close_or_expired_deadline", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, artifactProbePath, strings.NewReader(`{}`))
		recorder := &readWriteDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		wrapped := &artifactResponseWriter{
			ResponseWriter: recorder,
			body:           trackArtifactRequestBody(req),
		}
		if _, err := io.ReadAll(req.Body); err != nil {
			t.Fatalf("ReadAll(req.Body): %v", err)
		}
		if _, err := wrapped.Write([]byte("ok")); err != nil {
			t.Fatalf("Write error = %v", err)
		}
		if !wrapped.wroteHeader || recorder.Code != http.StatusOK {
			t.Fatalf("wroteHeader=%t status=%d, want true and 200", wrapped.wroteHeader, recorder.Code)
		}
		if got := recorder.Header().Get("Connection"); got != "" {
			t.Fatalf("Connection = %q, want empty", got)
		}
		if len(recorder.readDeadlines) != 0 {
			t.Fatalf("readDeadlines = %v, want none", recorder.readDeadlines)
		}
	})
}

func TestArtifactBaselineRoutes(t *testing.T) {
	for _, bundled := range []bool{true, false} {
		name := "slim upload"
		if bundled {
			name = "bundled adoption"
		}
		t.Run(name, func(t *testing.T) {
			h := newRouteTestHarness(t)
			request := func(method, path, token string, body []byte) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(method, path, bytes.NewReader(body))
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				h.handler.ServeHTTP(w, r)
				return w
			}
			check := func(result *httptest.ResponseRecorder, want int, code string) {
				t.Helper()
				if result.Code != want || (code != "" && !strings.Contains(result.Body.String(), `"code":"`+code+`"`)) {
					t.Fatalf("got %d %s; want %d/%s", result.Code, result.Body.String(), want, code)
				}
			}
			check(request(http.MethodPut, artifactUploadPath, "runtime-token", h.archive), http.StatusOK, "")
			binary := []byte("#!/bin/sh\necho next-route-baseline\n")
			id := fmt.Sprintf("sha256:%x", sha256.Sum256(binary))
			manifest := fmt.Sprintf(`{"schemaVersion":1,"engine":"cpa","version":"8.0.56","artifactId":%q}`, id)
			if err := os.WriteFile(h.manifestPath, []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			if bundled {
				if err := os.WriteFile(h.bundledPath, binary, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.executor.EnableArtifactAcquisition(h.manifestPath, lifecycle.WithAcquisitionPlatform("linux", "amd64"), lifecycle.WithBundledArtifactExecutable(h.bundledPath)); err != nil {
				t.Fatal(err)
			}
			if bundled {
				body := []byte(fmt.Sprintf(`{"expectedActiveArtifactId":%q}`, h.pinnedID))
				check(request(http.MethodPost, artifactAdoptBaselinePath, "", body), http.StatusUnauthorized, lifecycle.ArtifactCodeUnauthorized)
				check(request(http.MethodGet, artifactAdoptBaselinePath, "runtime-token", nil), http.StatusMethodNotAllowed, lifecycle.ArtifactCodeInvalidRequest)
				check(request(http.MethodPost, artifactAdoptBaselinePath, "runtime-token", []byte(`{"expected_active_artifact_id":"invalid"}`)), http.StatusBadRequest, lifecycle.ArtifactCodeInvalidRequest)
				check(request(http.MethodPost, artifactAdoptBaselinePath, "runtime-token", []byte(fmt.Sprintf(`{"expectedActiveArtifactId":%q}`, id))), http.StatusConflict, lifecycle.ArtifactCodeArtifactConflict)
				res := request(http.MethodPost, artifactAdoptBaselinePath, "runtime-token", body)
				check(res, http.StatusOK, "")
				var status lifecycle.ArtifactStatusResponse
				if err := json.Unmarshal(res.Body.Bytes(), &status); err != nil || !status.BundledAvailable || status.AdoptBaselineAvailable || status.Active == nil || string(status.Active.ArtifactID) != id {
					t.Fatalf("adoption status = %+v, %v", status, err)
				}
			} else {
				archive := buildRouteTarGzip(t, binary, []byte("MIT License\n"))
				check(request(http.MethodPut, artifactUploadPath+"?allow_baseline_upgrade=true", "runtime-token", archive), http.StatusBadRequest, lifecycle.ArtifactCodeInvalidRequest)
				check(request(http.MethodPut, artifactUploadPath+"?allow_baseline_upgrade=1&allow_baseline_upgrade=1", "runtime-token", archive), http.StatusBadRequest, lifecycle.ArtifactCodeInvalidRequest)
				check(request(http.MethodPut, artifactUploadPath+"?allow_baseline_upgrade=1", "runtime-token", archive), http.StatusOK, "")
			}
		})
	}
}
