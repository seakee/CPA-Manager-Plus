package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/requestbody"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

const cpaUpdateUploadTarget = "/usage-service/runtime/updates/prepare-upload?request_id=web-upload&target_version=7.3.4&expected_active_artifact_id=" + cpaUpdateTestArtifactID

// The fixture exercises the actual Manager-to-Supervisor HTTP transport while
// keeping status independent of the transport behavior under test.
type cpaUpdateUploadRuntime struct {
	*runtimeservice.EmbeddedClient
}

func (cpaUpdateUploadRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	return cpaUpdateReadyStatus(), nil
}

func TestCPAUpdateUploadRejectsInvalidRequestsBeforeCallingRuntime(t *testing.T) {
	client := &cpaUpdateRuntimeStub{status: cpaUpdateReadyStatus()}
	server := newCPAUpdateServer(t, client)
	for _, tc := range []struct {
		name, target  string
		contentTypes  []string
		body          io.Reader
		length        int64
		unauthorized  bool
		want          int
		wantConnClose bool
	}{
		{"auth required", cpaUpdateUploadTarget, []string{"application/gzip"}, strings.NewReader("x"), 1, true, 401, true},
		{"missing content type", cpaUpdateUploadTarget, nil, strings.NewReader("x"), 1, false, 400, true},
		{"wrong type", cpaUpdateUploadTarget, []string{"text/plain"}, strings.NewReader("x"), 1, false, 400, true},
		{"multiple content type headers", cpaUpdateUploadTarget, []string{"application/gzip", "application/octet-stream"}, strings.NewReader("x"), 1, false, 400, true},
		{"malformed content type params", cpaUpdateUploadTarget, []string{"application/gzip; invalid"}, strings.NewReader("x"), 1, false, 400, true},
		{"comma-joined content type", cpaUpdateUploadTarget, []string{"application/gzip, text/plain"}, strings.NewReader("x"), 1, false, 400, true},
		{"empty", cpaUpdateUploadTarget, []string{"application/gzip"}, http.NoBody, 0, false, 400, false},
		{"valid parameterized gzip with empty body", cpaUpdateUploadTarget, []string{"application/x-gzip; charset=binary"}, http.NoBody, 0, false, 400, false},
		{"http NoBody with unknown length", cpaUpdateUploadTarget, []string{"application/gzip"}, http.NoBody, -1, false, 400, false},
		{"negative content length below -1", cpaUpdateUploadTarget, []string{"application/gzip"}, strings.NewReader("x"), -2, false, 400, true},
		{"known oversize", cpaUpdateUploadTarget, []string{"application/gzip"}, strings.NewReader("x"), 64<<20 + 1, false, 413, true},
		{"duplicate query", cpaUpdateUploadTarget + "&request_id=other", []string{"application/gzip"}, strings.NewReader("x"), 1, false, 400, true},
		{"extra query", cpaUpdateUploadTarget + "&extra=1", []string{"application/gzip"}, strings.NewReader("x"), 1, false, 400, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.target, tc.body)
			req.ContentLength = tc.length
			if len(tc.contentTypes) > 0 {
				req.Header["Content-Type"] = append([]string(nil), tc.contentTypes...)
			}
			if !tc.unauthorized {
				req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
			}
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want || client.statusCalls.Load() != 0 {
				t.Fatalf("response = %d %s, Runtime status calls = %d", rec.Code, rec.Body.String(), client.statusCalls.Load())
			}
			gotConnClose := strings.EqualFold(rec.Header().Get("Connection"), "close")
			if gotConnClose != tc.wantConnClose {
				t.Fatalf("Connection header = %q, want close=%v", rec.Header().Get("Connection"), tc.wantConnClose)
			}
		})
	}
}

func TestCPAUpdateUploadStreamsAndMapsRuntimeRejections(t *testing.T) {
	const maxBytes = 64 << 20
	for _, tc := range []struct {
		name     string
		upstream int
		wantCode string
		chunked  bool
		want     int
	}{
		{"success", 200, "", false, 200},
		{"invalid request", 400, "invalid_request", false, 400},
		{"upstream oversize", 413, "archive_too_large", false, 413},
		{"chunked overflow recorded", 200, "archive_too_large", true, 502},
		{"chunked overflow not recorded", 200, "", true, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received atomic.Int64
			supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.Method != http.MethodPost || r.URL.Path != "/v1/runtime/operations/prepare-update/upload" ||
					q.Get("expectedRuntimeIdentity") != "runtime-http-test" || q.Get("expectedRuntimeGeneration") != "1" ||
					q.Get("expectedActiveArtifactId") != cpaUpdateTestArtifactID || q.Get("targetVersion") != "7.3.4" ||
					r.Header.Get("Authorization") != "Bearer private-runtime-token" || r.Header.Get("Content-Type") != "application/octet-stream" {
					t.Errorf("invalid upstream request: %s %s", r.Method, r.URL)
				}
				if tc.upstream != http.StatusOK {
					w.WriteHeader(tc.upstream)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": tc.wantCode, "message": "upstream detail"}})
					return
				}
				n, err := io.Copy(io.Discard, r.Body)
				if err != nil {
					t.Errorf("read upload: %v", err)
				}
				received.Store(n)
				result := map[string]any{
					"operationId": q.Get("operationId"), "operationType": string(model.RuntimeOperationPrepareUpdate),
					"runtimeIdentity": "runtime-http-test", "runtimeGeneration": 1, "state": "succeeded",
				}
				if tc.wantCode != "" {
					result["state"] = "failed"
					result["error"] = map[string]string{"code": tc.wantCode, "message": "upload failed"}
				}
				_ = json.NewEncoder(w).Encode(result)
			}))
			t.Cleanup(supervisor.Close)
			server := newCPAUpdateServer(t, cpaUpdateUploadRuntime{runtimeservice.NewEmbeddedClient(supervisor.URL, "private-runtime-token")})
			var body io.Reader = strings.NewReader("archive")
			if tc.chunked {
				body = io.LimitReader(zeroUploadBytes{}, maxBytes+128)
			}
			req := httptest.NewRequest(http.MethodPost, cpaUpdateUploadTarget, body)
			if tc.chunked {
				req.ContentLength = -1
			}
			req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
			req.Header.Set("Content-Type", "application/gzip")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want || rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "upstream detail") {
				t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
			}
			wantBody := `"state":"succeeded"`
			switch tc.want {
			case 400:
				wantBody = `"code":"invalid_request"`
			case 413:
				wantBody = `"code":"archive_too_large"`
			case 502:
				wantBody = `"failure_code":"archive_too_large"`
			}
			if !strings.Contains(rec.Body.String(), wantBody) {
				t.Fatalf("missing %s in %s", wantBody, rec.Body.String())
			}
			if tc.chunked && received.Load() != maxBytes+1 {
				t.Fatalf("upstream read %d bytes, want exactly one overflow sentinel", received.Load())
			}
		})
	}
}

type zeroUploadBytes struct{}

func (zeroUploadBytes) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestCPAUpdateUploadEarlySupervisorResponseStopsReadingBodyWithoutRace(t *testing.T) {
	for _, tc := range []struct {
		name       string
		upstream   int
		wantStatus int
		wantSubstr string
	}{
		{
			name:       "409 conflict without draining upload body",
			upstream:   http.StatusConflict,
			wantStatus: http.StatusConflict,
			wantSubstr: `"code":"operation_state_conflict"`,
		},
		{
			name:       "200 replay without draining upload body",
			upstream:   http.StatusOK,
			wantStatus: http.StatusOK,
			wantSubstr: `"state":"succeeded"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstRead := make(chan struct{})
			stopStream := make(chan struct{})
			defer close(stopStream)

			supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				<-firstRead
				w.Header().Set("Content-Type", "application/json")
				if tc.upstream == http.StatusConflict {
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]string{"code": "operation_state_conflict", "message": "busy"},
					})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"operationId":       r.URL.Query().Get("operationId"),
					"operationType":     string(model.RuntimeOperationPrepareUpdate),
					"runtimeIdentity":   "runtime-http-test",
					"runtimeGeneration": 1,
					"state":             "succeeded",
				})
			}))
			t.Cleanup(supervisor.Close)

			server := newCPAUpdateServer(t, cpaUpdateUploadRuntime{
				runtimeservice.NewEmbeddedClient(supervisor.URL, "private-runtime-token"),
			})
			req := httptest.NewRequest(
				http.MethodPost,
				cpaUpdateUploadTarget,
				&slowUploadStreamReader{firstRead: firstRead, stop: stopStream},
			)
			req.ContentLength = 1 << 20
			req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
			req.Header.Set("Content-Type", "application/gzip")
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantSubstr) {
				t.Fatalf("response = %d %s, want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantSubstr)
			}
		})
	}
}

type slowUploadStreamReader struct {
	firstRead chan struct{}
	stop      <-chan struct{}
	notified  bool
}

func (r *slowUploadStreamReader) Read(p []byte) (int, error) {
	if !r.notified {
		r.notified = true
		close(r.firstRead)
	}
	select {
	case <-r.stop:
		return 0, io.EOF
	case <-time.After(time.Millisecond):
		if len(p) == 0 {
			return 0, nil
		}
		p[0] = 'x'
		return 1, nil
	}
}

func TestCPAUpdateUploadBlockingInboundReaderReturnsPromptlyOnEarlySupervisorResponse(t *testing.T) {
	for _, tc := range []struct {
		name           string
		supervisorCode int
		supervisorBody string
		wantStatus     int
		wantSubstr     string
	}{
		{
			name:           "409 conflict while inbound reader blocks after first chunk",
			supervisorCode: http.StatusConflict,
			supervisorBody: `{"error":{"code":"operation_state_conflict","message":"busy"}}`,
			wantStatus:     http.StatusConflict,
			wantSubstr:     `"operation_state_conflict"`,
		},
		{
			name:           "413 too large while inbound reader blocks after first chunk",
			supervisorCode: http.StatusRequestEntityTooLarge,
			supervisorBody: `{"error":{"code":"archive_too_large","message":"too large"}}`,
			wantStatus:     http.StatusRequestEntityTooLarge,
			wantSubstr:     `"archive_too_large"`,
		},
		{
			name:           "200 replay while inbound reader blocks after first chunk",
			supervisorCode: http.StatusOK,
			supervisorBody: `{"operationId":"%OPERATION_ID%","operationType":"prepare_update","runtimeIdentity":"runtime-http-test","runtimeGeneration":1,"state":"succeeded"}`,
			wantStatus:     http.StatusOK,
			wantSubstr:     `"state":"succeeded"`,
		},
	} {
		t.Run(tc.name+" recorder", func(t *testing.T) {
			secondReadEntered := make(chan struct{})
			stopStream := make(chan struct{})
			defer close(stopStream)

			const firstChunk = "first-upload-chunk"
			supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buf := make([]byte, len(firstChunk))
				if _, err := io.ReadFull(r.Body, buf); err != nil {
					t.Errorf("read first chunk: %v", err)
				}
				<-secondReadEntered
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.supervisorCode)
				body := strings.ReplaceAll(tc.supervisorBody, "%OPERATION_ID%", r.URL.Query().Get("operationId"))
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(supervisor.Close)

			server := newCPAUpdateServer(t, cpaUpdateUploadRuntime{
				runtimeservice.NewEmbeddedClient(supervisor.URL, "private-runtime-token"),
			})
			req := httptest.NewRequest(
				http.MethodPost,
				cpaUpdateUploadTarget,
				&blockingUploadStreamReader{
					firstChunk:        []byte(firstChunk),
					secondReadEntered: secondReadEntered,
					stop:              stopStream,
				},
			)
			req.ContentLength = 1 << 20
			req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
			req.Header.Set("Content-Type", "application/gzip")

			started := time.Now()
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
				t.Fatalf("ServeHTTP took %v, want prompt millisecond return", elapsed)
			}
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantSubstr) {
				t.Fatalf("response = %d %s, want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantSubstr)
			}
		})

		t.Run(tc.name+" live_http_server", func(t *testing.T) {
			secondReadEntered := make(chan struct{})
			stopStream := make(chan struct{})
			defer close(stopStream)

			const firstChunk = "first-upload-chunk"
			supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				buf := make([]byte, len(firstChunk))
				if _, err := io.ReadFull(r.Body, buf); err != nil {
					t.Errorf("read first chunk: %v", err)
				}
				<-secondReadEntered
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.supervisorCode)
				body := strings.ReplaceAll(tc.supervisorBody, "%OPERATION_ID%", r.URL.Query().Get("operationId"))
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(supervisor.Close)

			server := newCPAUpdateServer(t, cpaUpdateUploadRuntime{
				runtimeservice.NewEmbeddedClient(supervisor.URL, "private-runtime-token"),
			})
			liveManager := httptest.NewServer(server.Handler())
			t.Cleanup(liveManager.Close)

			req, err := http.NewRequestWithContext(
				t.Context(),
				http.MethodPost,
				liveManager.URL+cpaUpdateUploadTarget,
				&blockingUploadStreamReader{
					firstChunk:        []byte(firstChunk),
					secondReadEntered: secondReadEntered,
					stop:              stopStream,
				},
			)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.ContentLength = 1 << 20
			req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
			req.Header.Set("Content-Type", "application/gzip")

			started := time.Now()
			resp, err := liveManager.Client().Do(req)
			if err != nil {
				t.Fatalf("Do(prepare-upload): %v", err)
			}
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
				t.Fatalf("live HTTP upload took %v, want prompt millisecond return", elapsed)
			}
			if resp.StatusCode != tc.wantStatus || !strings.Contains(string(respBody), tc.wantSubstr) {
				t.Fatalf("response = %d %s, want %d containing %s", resp.StatusCode, string(respBody), tc.wantStatus, tc.wantSubstr)
			}
		})
	}
}

type blockingUploadStreamReader struct {
	firstChunk        []byte
	secondReadEntered chan struct{}
	stop              <-chan struct{}
	reads             atomic.Int32
}

func (r *blockingUploadStreamReader) Read(p []byte) (int, error) {
	switch r.reads.Add(1) {
	case 1:
		n := copy(p, r.firstChunk)
		return n, nil
	case 2:
		close(r.secondReadEntered)
		<-r.stop
		return 0, io.EOF
	default:
		<-r.stop
		return 0, io.EOF
	}
}

func TestCPAUpdateUploadEarlyResponsesCloseHTTP11TCPWithoutStallOrRequestBleed(t *testing.T) {
	const (
		largeUnreadBodyLength = 512 << 10
		oversizeArchiveLength = (64 << 20) + 1
	)

	t.Run("pre_runtime_early_rejections_over_tcp", func(t *testing.T) {
		for _, tc := range []struct {
			name          string
			target        string
			contentType   string
			contentLength int64
			authorized    bool
			extraHeaders  string
			nilService    bool
			wantStatus    int
			wantSubstr    string
		}{
			{
				name:          "413 oversize 64MiB plus 1",
				target:        cpaUpdateUploadTarget,
				contentType:   "application/gzip",
				contentLength: oversizeArchiveLength,
				authorized:    true,
				wantStatus:    http.StatusRequestEntityTooLarge,
				wantSubstr:    `"code":"archive_too_large"`,
			},
			{
				name:          "400 invalid query with unread body over 256KiB",
				target:        cpaUpdateUploadTarget + "&extra=1",
				contentType:   "application/gzip",
				contentLength: largeUnreadBodyLength,
				authorized:    true,
				wantStatus:    http.StatusBadRequest,
				wantSubstr:    `"code":"invalid_request"`,
			},
			{
				name:          "400 invalid content type with unread body over 256KiB",
				target:        cpaUpdateUploadTarget,
				contentType:   "text/plain",
				contentLength: largeUnreadBodyLength,
				authorized:    true,
				wantStatus:    http.StatusBadRequest,
				wantSubstr:    `"code":"invalid_request"`,
			},
			{
				name:          "403 cross-site admin rejection with unread body over 256KiB",
				target:        cpaUpdateUploadTarget,
				contentType:   "application/gzip",
				contentLength: largeUnreadBodyLength,
				authorized:    true,
				extraHeaders:  "Sec-Fetch-Site: cross-site\r\nSec-Fetch-Dest: empty\r\n",
				wantStatus:    http.StatusForbidden,
				wantSubstr:    "cross-site admin request rejected",
			},
			{
				name:          "403 cross-site admin rejection with unread body under 256KiB",
				target:        cpaUpdateUploadTarget,
				contentType:   "application/gzip",
				contentLength: 4096,
				authorized:    true,
				extraHeaders:  "Sec-Fetch-Site: cross-site\r\nSec-Fetch-Dest: empty\r\n",
				wantStatus:    http.StatusForbidden,
				wantSubstr:    "cross-site admin request rejected",
			},
			{
				name:          "307 non-canonical ServeMux path with unread body over 256KiB",
				target:        strings.Replace(cpaUpdateUploadTarget, "/runtime/updates/", "/runtime//updates/", 1),
				contentType:   "application/gzip",
				contentLength: largeUnreadBodyLength,
				authorized:    true,
				wantStatus:    http.StatusTemporaryRedirect,
				wantSubstr:    "",
			},
			{
				name:          "401 unauthorized with unread body",
				target:        cpaUpdateUploadTarget,
				contentType:   "application/gzip",
				contentLength: 4096,
				authorized:    false,
				wantStatus:    http.StatusUnauthorized,
				wantSubstr:    "invalid admin key",
			},
			{
				name:          "503 service unavailable with unread body",
				target:        cpaUpdateUploadTarget,
				contentType:   "application/gzip",
				contentLength: 4096,
				authorized:    true,
				nilService:    true,
				wantStatus:    http.StatusServiceUnavailable,
				wantSubstr:    "CPA update state unavailable",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				client := &cpaUpdateRuntimeStub{status: cpaUpdateReadyStatus()}
				server := newCPAUpdateServer(t, client)
				if tc.nilService {
					server.AppContext().CPAUpdateService = nil
				}
				liveManager := httptest.NewServer(server.Handler())
				t.Cleanup(liveManager.Close)

				conn, err := net.Dial("tcp", liveManager.Listener.Addr().String())
				if err != nil {
					t.Fatalf("dial live manager: %v", err)
				}
				t.Cleanup(func() { _ = conn.Close() })

				authHeader := ""
				if tc.authorized {
					authHeader = "Authorization: Bearer " + testutil.AdminKey + "\r\n"
				}
				// Embed an HTTP/1.1 request prefix inside the unread upload stream while
				// withholding the declared Content-Length remainder: the server must close
				// the TCP socket immediately without a 250ms drain stall and must never
				// interpret unread body bytes as a follow-up request.
				unreadPrefix := fmt.Sprintf(
					"GET /health HTTP/1.1\r\nHost: %s\r\n\r\n",
					liveManager.Listener.Addr().String(),
				)
				rawReq := fmt.Sprintf(
					"POST %s HTTP/1.1\r\nHost: %s\r\n%s%sContent-Type: %s\r\nContent-Length: %d\r\n\r\n%s",
					tc.target,
					liveManager.Listener.Addr().String(),
					authHeader,
					tc.extraHeaders,
					tc.contentType,
					tc.contentLength,
					unreadPrefix,
				)

				started := time.Now()
				if _, err := conn.Write([]byte(rawReq)); err != nil {
					t.Fatalf("write raw HTTP/1.1 request: %v", err)
				}
				assertPromptTCPCloseWithoutSecondResponse(
					t,
					conn,
					started,
					tc.wantStatus,
					tc.wantSubstr,
				)
			})
		}
	})

	t.Run("chunked_early_supervisor_responses_over_tcp", func(t *testing.T) {
		for _, tc := range []struct {
			name           string
			supervisorCode int
			supervisorBody string
			wantStatus     int
			wantSubstr     string
		}{
			{
				name:           "chunked 409 conflict",
				supervisorCode: http.StatusConflict,
				supervisorBody: `{"error":{"code":"operation_state_conflict","message":"busy"}}`,
				wantStatus:     http.StatusConflict,
				wantSubstr:     `"code":"operation_state_conflict"`,
			},
			{
				name:           "chunked 200 replay",
				supervisorCode: http.StatusOK,
				supervisorBody: `{"operationId":"%OPERATION_ID%","operationType":"prepare_update","runtimeIdentity":"runtime-http-test","runtimeGeneration":1,"state":"succeeded"}`,
				wantStatus:     http.StatusOK,
				wantSubstr:     `"state":"succeeded"`,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				const firstChunk = "chunked-upload-head"
				supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					buf := make([]byte, len(firstChunk))
					if _, err := io.ReadFull(r.Body, buf); err != nil {
						t.Errorf("read first chunk: %v", err)
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Connection", "close")
					_ = http.NewResponseController(w).SetReadDeadline(time.Unix(1, 0))
					w.WriteHeader(tc.supervisorCode)
					body := strings.ReplaceAll(tc.supervisorBody, "%OPERATION_ID%", r.URL.Query().Get("operationId"))
					_, _ = w.Write([]byte(body))
				}))
				t.Cleanup(supervisor.Close)

				server := newCPAUpdateServer(t, cpaUpdateUploadRuntime{
					runtimeservice.NewEmbeddedClient(supervisor.URL, "private-runtime-token"),
				})
				liveManager := httptest.NewServer(server.Handler())
				t.Cleanup(liveManager.Close)

				conn, err := net.Dial("tcp", liveManager.Listener.Addr().String())
				if err != nil {
					t.Fatalf("dial live manager: %v", err)
				}
				t.Cleanup(func() { _ = conn.Close() })

				secondChunkRequestLike := fmt.Sprintf("GET /health HTTP/1.1\r\nHost: %s\r\n\r\n", liveManager.Listener.Addr().String())
				rawReq := fmt.Sprintf(
					"POST %s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/gzip\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n%x\r\n%s\r\n",
					cpaUpdateUploadTarget,
					liveManager.Listener.Addr().String(),
					testutil.AdminKey,
					len(firstChunk),
					firstChunk,
					len(secondChunkRequestLike),
					secondChunkRequestLike,
				)

				started := time.Now()
				if _, err := conn.Write([]byte(rawReq)); err != nil {
					t.Fatalf("write chunked HTTP/1.1 request: %v", err)
				}
				assertPromptTCPCloseWithoutSecondResponse(
					t,
					conn,
					started,
					tc.wantStatus,
					tc.wantSubstr,
				)
			})
		}
	})
	t.Run("slow_unread_body_under_256kib_exceeding_drain_timeout_over_tcp", func(t *testing.T) {
		client := &cpaUpdateRuntimeStub{status: cpaUpdateReadyStatus()}
		server := newCPAUpdateServer(t, client)
		server.WrapHandler(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r = r.WithContext(requestbody.WithEarlyCloseDrainTimeout(r.Context(), 40*time.Millisecond))
				next.ServeHTTP(w, r)
			})
		})
		liveManager := httptest.NewServer(server.Handler())
		t.Cleanup(liveManager.Close)

		conn, err := net.Dial("tcp", liveManager.Listener.Addr().String())
		if err != nil {
			t.Fatalf("dial live manager: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		nonCanonicalTarget := strings.Replace(cpaUpdateUploadTarget, "/runtime/updates/", "/runtime//updates/", 1)
		unreadPrefix := fmt.Sprintf(
			"GET /health HTTP/1.1\r\nHost: %s\r\n\r\n",
			liveManager.Listener.Addr().String(),
		)
		rawReq := fmt.Sprintf(
			"POST %s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/gzip\r\nContent-Length: 4096\r\n\r\n%s",
			nonCanonicalTarget,
			liveManager.Listener.Addr().String(),
			testutil.AdminKey,
			unreadPrefix,
		)

		started := time.Now()
		if _, err := conn.Write([]byte(rawReq)); err != nil {
			t.Fatalf("write raw HTTP/1.1 request: %v", err)
		}
		assertPromptTCPCloseWithoutSecondResponse(
			t,
			conn,
			started,
			http.StatusTemporaryRedirect,
			"",
		)
	})
}

func assertPromptTCPCloseWithoutSecondResponse(
	t *testing.T,
	conn net.Conn,
	started time.Time,
	wantStatus int,
	wantSubstr string,
) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var rawWire bytes.Buffer
	br := bufio.NewReader(io.TeeReader(conn, &rawWire))
	parseReq, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1"+cpaUpdateUploadTarget, nil)
	resp, err := http.ReadResponse(br, parseReq)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	var extra [1]byte
	n, closeErr := br.Read(extra[:])
	elapsed := time.Since(started)

	if elapsed >= 200*time.Millisecond {
		t.Fatalf("early response + TCP close took %v, want prompt close under 200ms without 250ms drain stall", elapsed)
	}
	if resp.StatusCode != wantStatus || !strings.Contains(string(respBody), wantSubstr) {
		t.Fatalf("response = %d %s, want %d containing %s", resp.StatusCode, string(respBody), wantStatus, wantSubstr)
	}
	if !resp.Close || !strings.Contains(strings.ToLower(rawWire.String()), "\r\nconnection: close\r\n") {
		t.Fatalf("response Close=%v wire=%q, want Connection: close on wire", resp.Close, rawWire.String())
	}
	if n != 0 || closeErr == nil || errors.Is(closeErr, os.ErrDeadlineExceeded) {
		t.Fatalf("post-response TCP stream read = (%d, %v), want immediate socket close without second response", n, closeErr)
	}
}
