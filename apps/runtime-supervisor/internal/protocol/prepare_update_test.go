package protocol

import (
	"context"
	"errors"
	"io"
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const validPrepareUpdateBody = `{"operationId":"op","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7,"expectedActiveArtifactId":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","targetVersion":"7.3.3"}`

type prepareUpdateFunc func(context.Context, lifecycle.PrepareUpdateRequest) (journal.Operation, error)

func (f prepareUpdateFunc) PrepareUpdate(ctx context.Context, request lifecycle.PrepareUpdateRequest) (journal.Operation, error) {
	return f(ctx, request)
}

func TestPrepareUpdateAuthenticatesBeforeDecodeAndUsesTypedContract(t *testing.T) {
	called := false
	h := prepareUpdateHandler(t, prepareUpdateFunc(func(_ context.Context, request lifecycle.PrepareUpdateRequest) (journal.Operation, error) {
		called = true
		if request.OperationID != "op" || request.ExpectedRuntimeIdentity != "runtime-01" || request.ExpectedRuntimeGeneration != 7 ||
			request.ExpectedActiveArtifactID != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
			request.TargetVersion != "7.3.3" {
			t.Fatalf("typed request = %+v", request)
		}
		return journal.Operation{
			OperationID: "op", OperationType: "prepare_update", RuntimeIdentity: "runtime-01",
			RuntimeGeneration: 7, State: journal.StateSucceeded,
		}, nil
	}))
	unauthorized := httptest.NewRequest(http.MethodPost, prepareUpdatePath, strings.NewReader(validPrepareUpdateBody))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized || called {
		t.Fatalf("unauthorized response=%d called=%v", response.Code, called)
	}
	response = submitPrepareUpdate(t, h, validPrepareUpdateBody)
	if response.Code != http.StatusOK || !called {
		t.Fatalf("prepare response=%d body=%s called=%v", response.Code, response.Body.String(), called)
	}
	var operation operationResponse
	decodeResponse(t, response, &operation)
	if operation.OperationType != "prepare_update" || operation.State != journal.StateSucceeded {
		t.Fatalf("operation = %+v", operation)
	}
}

func TestPrepareUpdateStrictValidationRejectsAliasesAndDownloadAuthority(t *testing.T) {
	h := prepareUpdateHandler(t, prepareUpdateFunc(func(context.Context, lifecycle.PrepareUpdateRequest) (journal.Operation, error) {
		t.Fatal("invalid request reached executor")
		return journal.Operation{}, nil
	}))
	tests := map[string]string{
		"empty": "", "null": "null", "array": "[]", "missing target": strings.Replace(validPrepareUpdateBody, `,"targetVersion":"7.3.3"`, "", 1),
		"latest":            strings.Replace(validPrepareUpdateBody, `"7.3.3"`, `"latest"`, 1),
		"tag alias":         strings.Replace(validPrepareUpdateBody, `"7.3.3"`, `"v7.3.3"`, 1),
		"URL target":        strings.Replace(validPrepareUpdateBody, `"7.3.3"`, `"https://example.test/a"`, 1),
		"path target":       strings.Replace(validPrepareUpdateBody, `"7.3.3"`, `"../7.3.3"`, 1),
		"whitespace target": strings.Replace(validPrepareUpdateBody, `"7.3.3"`, `" 7.3.3"`, 1),
		"uppercase digest":  strings.Replace(validPrepareUpdateBody, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"duplicate target":  strings.Replace(validPrepareUpdateBody, `"targetVersion":"7.3.3"`, `"targetVersion":"7.3.3","targetVersion":"7.3.4"`, 1),
		"trailing":          validPrepareUpdateBody + `{}`,
	}
	for _, field := range []string{"url", "repository", "assetName", "archiveDigest", "executable", "path", "mirror", "action", "params"} {
		tests[field] = strings.TrimSuffix(validPrepareUpdateBody, "}") + `,"` + field + `":"forbidden"}`
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			response := submitPrepareUpdate(t, h, body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertErrorCode(t, response, "invalid_request")
		})
	}
}

func TestPrepareUpdateCapabilityIsAdditive(t *testing.T) {
	h := prepareUpdateHandler(t, prepareUpdateFunc(func(context.Context, lifecycle.PrepareUpdateRequest) (journal.Operation, error) {
		return journal.Operation{}, nil
	}))
	for _, path := range []string{handshakePath, statusPath} {
		var response statusResponse
		decodeResponse(t, request(t, h, http.MethodGet, path, testRuntimeToken), &response)
		if !reflect.DeepEqual(response.Capabilities, []string{CapabilityPrepareUpdate}) {
			t.Fatalf("%s capabilities = %v", path, response.Capabilities)
		}
	}
	readOnly := newTestHandler(t)
	response := submitPrepareUpdate(t, readOnly, validPrepareUpdateBody)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported response = %d", response.Code)
	}
	assertErrorCode(t, response, "unsupported_operation")
}

func TestPrepareUpdateMapsStableFenceAndReleaseErrors(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{err: lifecycle.ErrActiveArtifactUnavailable, status: http.StatusConflict, code: "active_artifact_unavailable"},
		{err: lifecycle.ErrActiveArtifactMismatch, status: http.StatusConflict, code: "active_artifact_mismatch"},
		{err: lifecycle.ErrUnsupportedStaging, status: http.StatusBadRequest, code: "unsupported_staging_platform"},
		{err: lifecycle.ErrReleaseMetadataInvalid, status: http.StatusBadGateway, code: "release_metadata_invalid"},
	}
	for _, test := range tests {
		h := prepareUpdateHandler(t, prepareUpdateFunc(func(context.Context, lifecycle.PrepareUpdateRequest) (journal.Operation, error) {
			return journal.Operation{}, test.err
		}))
		response := submitPrepareUpdate(t, h, validPrepareUpdateBody)
		if response.Code != test.status {
			t.Fatalf("error %v status=%d body=%s", test.err, response.Code, response.Body.String())
		}
		assertErrorCode(t, response, test.code)
	}
}

func TestPrepareUpdateFailedReplayUsesCommonOperationResponse(t *testing.T) {
	h := prepareUpdateHandler(t, prepareUpdateFunc(func(context.Context, lifecycle.PrepareUpdateRequest) (journal.Operation, error) {
		return journal.Operation{
			OperationID: "op", OperationType: "prepare_update", RuntimeIdentity: "runtime-01",
			RuntimeGeneration: 6, State: journal.StateFailed, FailureCode: "release_asset_invalid",
		}, nil
	}))
	response := submitPrepareUpdate(t, h, validPrepareUpdateBody)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var got operationResponse
	decodeResponse(t, response, &got)
	if got.Error == nil || got.Error.Code != "release_asset_invalid" {
		t.Fatalf("operation response = %+v", got)
	}
}

func prepareUpdateHandler(t *testing.T, executor PrepareUpdateExecutor) http.Handler {
	t.Helper()
	h, err := NewHandler(Config{
		RuntimeIdentity: "runtime-01", RuntimeGeneration: 7, Token: testRuntimeToken, PrepareUpdate: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func submitPrepareUpdate(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, prepareUpdatePath, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testRuntimeToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func TestPrepareUpdateAcceptsValidCDNAndRejectsUnsafeCDNPrefix(t *testing.T) {
	var got lifecycle.PrepareUpdateRequest
	h := prepareUpdateHandler(t, prepareUpdateFunc(func(_ context.Context, req lifecycle.PrepareUpdateRequest) (journal.Operation, error) {
		got = req
		return journal.Operation{
			OperationID: req.OperationID, OperationType: "prepare_update",
			RuntimeIdentity: req.ExpectedRuntimeIdentity, RuntimeGeneration: req.ExpectedRuntimeGeneration,
			State: journal.StateSucceeded,
		}, nil
	}))
	good := `{"operationId":"op","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7,"expectedActiveArtifactId":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","targetVersion":"7.3.4","sourceMode":"cdn","cdnPrefix":"https://cdn.example.com/mirror/"}`
	resp := submitPrepareUpdate(t, h, good)
	if resp.Code != http.StatusOK || got.SourceMode != "cdn" || got.CDNPrefix != "https://cdn.example.com/mirror/" {
		t.Fatalf("valid CDN status=%d got=%+v body=%s", resp.Code, got, resp.Body.String())
	}
	for _, bad := range []string{
		`{"operationId":"op","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7,"expectedActiveArtifactId":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","targetVersion":"7.3.4","sourceMode":"cdn","cdnPrefix":"http://127.0.0.1/"}`,
		`{"operationId":"op","expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":7,"expectedActiveArtifactId":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","targetVersion":"7.3.4","sourceMode":"github","cdnPrefix":"https://cdn.example.com/"}`,
	} {
		if resp := submitPrepareUpdate(t, h, bad); resp.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d", bad, resp.Code)
		}
	}
}

const prepareUploadQuery = "?operationId=manager-op&expectedRuntimeIdentity=runtime-01&expectedRuntimeGeneration=7&targetVersion=7.3.4&expectedActiveArtifactId="

func TestPrepareUpdateUploadRejectsKnownInvalidSizesBeforeExecution(t *testing.T) {
	h := newTestHandler(t) // no executor; size errors must take precedence
	path := PrepareUpdateUploadPath + prepareUploadQuery + "sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name   string
		length int64
		noBody bool
		status int
		code   string
	}{
		{name: "zero content length", length: 0, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "negative content length below -1", length: -2, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "http.NoBody with unknown length", length: -1, noBody: true, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "oversize content length", length: runtimeupdate.MaxAcquisitionArchiveBytes + 1, status: http.StatusRequestEntityTooLarge, code: "archive_too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, nil)
			request.ContentLength = test.length
			if !test.noBody && test.length != 0 {
				request.Body = unreadableBody{t: t}
			}
			request.Header.Set("Authorization", "Bearer "+testRuntimeToken)
			response := &uploadDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			h.ServeHTTP(response, request)
			after := time.Now()
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertErrorCode(t, response.ResponseRecorder, test.code)
			if len(response.writeDeadlines) != 0 {
				t.Fatalf("unexpected write deadlines=%v", response.writeDeadlines)
			}
			wantImmediateReadExpire := !test.noBody && test.length != 0
			if wantImmediateReadExpire {
				if len(response.readDeadlines) != 1 || response.readDeadlines[0].IsZero() || response.readDeadlines[0].After(after.Add(time.Second)) {
					t.Fatalf("expected 1 immediate read deadline expiration for unconsumed body, got %v", response.readDeadlines)
				}
			} else if len(response.readDeadlines) != 0 {
				t.Fatalf("unexpected read deadlines=%v", response.readDeadlines)
			}
		})
	}
}

func TestPrepareUpdateUploadRetainsChunkedOverflowFailure(t *testing.T) {
	dir := t.TempDir()
	store, err := journal.Open(t.Context(), filepath.Join(dir, "operations.sqlite"), journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	closeRuntime := store.Close
	t.Cleanup(func() { _ = closeRuntime() })
	executable := filepath.Join(dir, "active-cpa")
	if err := os.WriteFile(executable, []byte("test artifact; never executed"), 0o600); err != nil {
		t.Fatal(err)
	}
	observer := artifact.NewObserver(executable, "")
	_ = observer.Refresh()
	active := observer.Observation()
	if active == nil {
		t.Fatal("missing active artifact observation")
	}
	executor, err := lifecycle.NewExecutor(journal.Authority{RuntimeIdentity: "runtime-01", RuntimeGeneration: 7},
		store, cpaprocess.NewManager(nil), executable)
	if err != nil {
		t.Fatal(err)
	}
	closeRuntime = executor.Close
	preparer := &prepareUploadVerifier{}
	if err := executor.EnablePrepareUpdate(observer, preparer); err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(Config{
		RuntimeIdentity: "runtime-01", RuntimeGeneration: 7, Token: testRuntimeToken,
		PrepareUpdateUpload: executor, ObserveUpdate: executor,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := PrepareUpdateUploadPath + prepareUploadQuery + string(active.ArtifactID)
	submit := func(body io.Reader) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, body)
		r.ContentLength = -1
		r.TransferEncoding = []string{"chunked"}
		r.Header.Set("Authorization", "Bearer "+testRuntimeToken)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, r)
		var got operationResponse
		decodeResponse(t, response, &got)
		if response.Code != http.StatusOK || got.State != journal.StateFailed || got.Error == nil || got.Error.Code != "archive_too_large" {
			t.Fatalf("overflow response = %d %+v", response.Code, got)
		}
	}
	body := &io.LimitedReader{R: zeroUploadBody{}, N: runtimeupdate.MaxAcquisitionArchiveBytes + 2}
	submit(body)
	if body.N != 1 {
		t.Fatalf("overflow consumed %d bytes, want limit plus one", runtimeupdate.MaxAcquisitionArchiveBytes+2-body.N)
	}
	observation := request(t, h, http.MethodGet, validObserveUpdateTarget(), testRuntimeToken)
	var retained observedOperationResponse
	decodeResponse(t, observation, &retained)
	if observation.Code != http.StatusOK || retained.State != journal.StateFailed || retained.Error == nil || retained.Error.Code != "archive_too_large" {
		t.Fatalf("durable overflow observation = %d %+v", observation.Code, retained)
	}
	replay := strings.NewReader("new body must not replace retained evidence")
	unread := replay.Len()
	submit(replay)
	if preparer.stages != 1 || replay.Len() != unread {
		t.Fatalf("replay restaged upload: stage calls=%d unread bytes=%d", preparer.stages, replay.Len())
	}
}

type zeroUploadBody struct{}

func (zeroUploadBody) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type prepareUploadVerifier struct{ stages int }

func (*prepareUploadVerifier) Resolve(_ context.Context, version string) (runtimeupdate.Release, error) {
	return runtimeupdate.Release{
		Version: version, ArchiveSize: 1, ArchiveDigest: "sha256:" + strings.Repeat("a", 64),
	}, nil
}

func (*prepareUploadVerifier) Stage(context.Context, runtimeupdate.Release) (runtimeupdate.Metadata, error) {
	return runtimeupdate.Metadata{}, runtimeupdate.ErrStaging
}

func (p *prepareUploadVerifier) StageWithOptions(ctx context.Context, release runtimeupdate.Release, opts runtimeupdate.StageOptions) (runtimeupdate.Metadata, error) {
	p.stages++
	return runtimeupdate.Metadata{}, runtimeupdate.VerifyUploadStream(ctx, release, opts.UploadReader, opts.UploadLength, io.Discard, opts.Progress)
}

type prepareUploadFunc func(context.Context, lifecycle.PrepareUpdateUploadRequest) (journal.Operation, error)

func (f prepareUploadFunc) PrepareUpdateUpload(ctx context.Context, req lifecycle.PrepareUpdateUploadRequest) (journal.Operation, error) {
	return f(ctx, req)
}

type uploadDeadlineRecorder struct {
	*httptest.ResponseRecorder
	readDeadlines  []time.Time
	writeDeadlines []time.Time
	writeErr       error
}

func (r *uploadDeadlineRecorder) SetReadDeadline(d time.Time) error {
	r.readDeadlines = append(r.readDeadlines, d)
	return nil
}

func (r *uploadDeadlineRecorder) SetWriteDeadline(d time.Time) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	r.writeDeadlines = append(r.writeDeadlines, d)
	return nil
}

func TestPrepareUpdateUploadContentTypeDeadlinesAndUnconsumedClose(t *testing.T) {
	path := PrepareUpdateUploadPath + prepareUploadQuery + "sha256:" + strings.Repeat("a", 64)

	h, err := NewHandler(Config{
		RuntimeIdentity:   "runtime-01",
		RuntimeGeneration: 7,
		Token:             testRuntimeToken,
		PrepareUpdateUpload: prepareUploadFunc(func(_ context.Context, req lifecycle.PrepareUpdateUploadRequest) (journal.Operation, error) {
			if req.OperationID == "replay-op" {
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

	for _, rejectedCT := range [][]string{
		{"application/json"},
		{"multipart/form-data; boundary=xyz"},
		{"text/plain"},
		{"invalid/mime/type"},
		{"application/octet-stream", "application/gzip"},
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.ContentLength = 8
		req.Body = unreadableBody{t: t}
		req.Header.Set("Authorization", "Bearer "+testRuntimeToken)
		req.Header["Content-Type"] = rejectedCT
		rec := &uploadDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		h.ServeHTTP(rec, req)
		after := time.Now()
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("Content-Type %v: status = %d, want 400", rejectedCT, rec.Code)
		}
		assertErrorCode(t, rec.ResponseRecorder, "invalid_request")
		if len(rec.writeDeadlines) != 0 || len(rec.readDeadlines) != 1 || rec.readDeadlines[0].IsZero() || rec.readDeadlines[0].After(after.Add(time.Second)) {
			t.Fatalf("Content-Type %v: unexpected deadlines read=%v write=%v", rejectedCT, rec.readDeadlines, rec.writeDeadlines)
		}
		if got := rec.Header().Get("Connection"); got != "close" {
			t.Fatalf("Content-Type %v: Connection = %q, want close", rejectedCT, got)
		}
	}

	for _, allowedCT := range []string{
		"",
		"application/octet-stream",
		"application/gzip",
		"application/x-gzip; charset=binary",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("archive"))
		req.Header.Set("Authorization", "Bearer "+testRuntimeToken)
		if allowedCT != "" {
			req.Header.Set("Content-Type", allowedCT)
		}
		rec := &uploadDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		before := time.Now()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("Content-Type %q: status = %d body = %s", allowedCT, rec.Code, rec.Body.String())
		}
		if len(rec.readDeadlines) != 2 || len(rec.writeDeadlines) != 1 {
			t.Fatalf("Content-Type %q: deadlines read=%v write=%v, want 2 read and 1 active write", allowedCT, rec.readDeadlines, rec.writeDeadlines)
		}
		if rec.readDeadlines[0].Before(before.Add(PrepareUpdateUploadReadTimeout)) ||
			rec.writeDeadlines[0].Before(before.Add(PrepareUpdateResponseTimeout)) ||
			!rec.readDeadlines[1].IsZero() {
			t.Fatalf("Content-Type %q: unexpected deadline values read=%v write=%v", allowedCT, rec.readDeadlines, rec.writeDeadlines)
		}
		if got := rec.Header().Get("Connection"); got != "" {
			t.Fatalf("Content-Type %q: Connection = %q, want empty on consumed stream", allowedCT, got)
		}
	}

	replayPath := strings.Replace(path, "operationId=manager-op", "operationId=replay-op", 1)
	replayReq := httptest.NewRequest(http.MethodPost, replayPath, strings.NewReader("unconsumed replay stream"))
	replayReq.Header.Set("Authorization", "Bearer "+testRuntimeToken)
	replayRec := &uploadDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	beforeReplay := time.Now()
	h.ServeHTTP(replayRec, replayReq)
	afterReplay := time.Now()
	if replayRec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", replayRec.Code)
	}
	if len(replayRec.readDeadlines) != 2 || len(replayRec.writeDeadlines) != 1 {
		t.Fatalf("replay deadlines read=%v write=%v, want 2 read and 1 active write", replayRec.readDeadlines, replayRec.writeDeadlines)
	}
	if replayRec.readDeadlines[1].IsZero() || replayRec.readDeadlines[1].Before(beforeReplay) || replayRec.readDeadlines[1].After(afterReplay) {
		t.Fatalf("unconsumed replay final read deadline = %v, want immediate expiration in [%v, %v]", replayRec.readDeadlines[1], beforeReplay, afterReplay)
	}
	if got := replayRec.Header().Get("Connection"); got != "close" {
		t.Fatalf("replay Connection = %q, want close for unconsumed stream", got)
	}
}

func TestPrepareUpdateWriteDeadlineRequiresValidAuthAndReturnsJSONOnDeadlineError(t *testing.T) {
	called := 0
	h := prepareUpdateHandler(t, prepareUpdateFunc(func(_ context.Context, req lifecycle.PrepareUpdateRequest) (journal.Operation, error) {
		called++
		return journal.Operation{
			OperationID:       req.OperationID,
			OperationType:     "prepare_update",
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
		{name: "missing auth", body: validPrepareUpdateBody, wantStatus: http.StatusUnauthorized},
		{name: "wrong bearer token", auth: "Bearer wrong-token", body: validPrepareUpdateBody, wantStatus: http.StatusUnauthorized},
		{name: "invalid JSON body", auth: "Bearer " + testRuntimeToken, body: `{"operationId":"op"}`, wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, prepareUpdatePath, strings.NewReader(tc.body))
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

	// Deadline failure returns protocol JSON internal_error rather than text/plain.
	failReq := httptest.NewRequest(http.MethodPost, prepareUpdatePath, strings.NewReader(validPrepareUpdateBody))
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

	// Valid authenticated prepare-update extends write deadline and keeps it active through response flush.
	okReq := httptest.NewRequest(http.MethodPost, prepareUpdatePath, strings.NewReader(validPrepareUpdateBody))
	okReq.Header.Set("Authorization", "Bearer "+testRuntimeToken)
	okRec := &uploadDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()
	h.ServeHTTP(okRec, okReq)
	after := time.Now()
	if okRec.Code != http.StatusOK || called != 1 {
		t.Fatalf("valid prepare status = %d, called = %d", okRec.Code, called)
	}
	if len(okRec.readDeadlines) != 0 || len(okRec.writeDeadlines) != 1 {
		t.Fatalf("valid prepare deadlines read=%v write=%v, want 0 read and 1 active write", okRec.readDeadlines, okRec.writeDeadlines)
	}
	if okRec.writeDeadlines[0].Before(before.Add(PrepareUpdateResponseTimeout)) || okRec.writeDeadlines[0].After(after.Add(PrepareUpdateResponseTimeout)) {
		t.Fatalf("valid prepare write deadline = %v, want ~%s", okRec.writeDeadlines[0], PrepareUpdateResponseTimeout)
	}
}

func TestCloseUnconsumedResponseWriterClosesOpenHTTP11SocketPromptly(t *testing.T) {
	h := newTestHandler(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:     h,
		ReadTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	raw := fmt.Sprintf(
		"POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\npartial",
		prepareUpdatePath,
		ln.Addr().String(),
	)
	started := time.Now()
	if _, err := conn.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(conn)
	parseReq, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1"+prepareUpdatePath, nil)
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
		t.Fatalf("unconsumed socket close took %v, want immediate close well below 5s ReadTimeout", elapsed)
	}
	if n != 0 || closeErr == nil || errors.Is(closeErr, os.ErrDeadlineExceeded) {
		t.Fatalf("post-response read = (%d, %v), want immediate EOF/close", n, closeErr)
	}
}
