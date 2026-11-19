package protocol

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testRuntimeToken = "runtime-token"

func TestHandshake(t *testing.T) {
	h := newTestHandler(t)
	response := request(t, h, http.MethodGet, handshakePath, testRuntimeToken)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertJSONHeaders(t, response)
	var got map[string]any
	decodeResponse(t, response, &got)
	want := map[string]any{
		"protocolVersion":   "v1",
		"runtimeIdentity":   "runtime-01",
		"runtimeGeneration": float64(7),
		"capabilities":      []any{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("handshake = %#v, want %#v", got, want)
	}
}

func TestStatusDoesNotInventCPAObservation(t *testing.T) {
	h := newTestHandler(t)
	response := request(t, h, http.MethodGet, statusPath, testRuntimeToken)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertJSONHeaders(t, response)
	var got map[string]any
	decodeResponse(t, response, &got)
	want := map[string]any{
		"protocolVersion":    "v1",
		"runtimeIdentity":    "runtime-01",
		"runtimeGeneration":  float64(7),
		"state":              "unknown",
		"cpaObservedVersion": "",
		"capabilities":       []any{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime status = %#v, want %#v", got, want)
	}
	if _, ok := got["runtimeMode"]; ok {
		t.Fatal("runtime status included Manager-owned runtimeMode")
	}
}

func TestEndpointsRequireBearerToken(t *testing.T) {
	h := newTestHandler(t)
	tests := []struct {
		name          string
		target        string
		authorization string
	}{
		{name: "missing", target: handshakePath},
		{name: "wrong token", target: handshakePath, authorization: "Bearer wrong-token"},
		{name: "wrong scheme", target: handshakePath, authorization: "Basic " + testRuntimeToken},
		{name: "extra value", target: handshakePath, authorization: "Bearer " + testRuntimeToken + " extra"},
		{name: "query token", target: statusPath + "?token=" + testRuntimeToken},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.target, nil)
			if test.authorization != "" {
				req.Header.Set("Authorization", test.authorization)
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			assertJSONHeaders(t, response)
			if got := response.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
			if strings.Contains(response.Body.String(), testRuntimeToken) {
				t.Fatal("response exposed runtime token")
			}
			assertErrorCode(t, response, "unauthorized")
		})
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, handshakePath, nil)
	req.Header.Set("Authorization", "bearer "+testRuntimeToken)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestEndpointMethodAndPathErrorsAreJSON(t *testing.T) {
	h := newTestHandler(t)

	methodResponse := request(t, h, http.MethodPost, statusPath, testRuntimeToken)
	if methodResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d", methodResponse.Code)
	}
	assertJSONHeaders(t, methodResponse)
	if got := methodResponse.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q", got)
	}
	assertErrorCode(t, methodResponse, "method_not_allowed")

	notFoundResponse := request(t, h, http.MethodGet, "/v1/runtime/unknown", testRuntimeToken)
	if notFoundResponse.Code != http.StatusNotFound {
		t.Fatalf("not found status = %d", notFoundResponse.Code)
	}
	assertJSONHeaders(t, notFoundResponse)
	assertErrorCode(t, notFoundResponse, "not_found")
}

func TestNewHandlerValidatesProtocolIdentity(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		want   string
	}{
		{
			name:   "missing identity",
			config: Config{RuntimeGeneration: 1, Token: testRuntimeToken},
			want:   "runtime identity is required",
		},
		{
			name:   "zero generation",
			config: Config{RuntimeIdentity: "runtime-01", Token: testRuntimeToken},
			want:   "runtime generation must be greater than zero",
		},
		{
			name:   "missing token",
			config: Config{RuntimeIdentity: "runtime-01", RuntimeGeneration: 1},
			want:   "runtime token is required",
		},
		{
			name:   "token whitespace",
			config: Config{RuntimeIdentity: "runtime-01", RuntimeGeneration: 1, Token: "token value"},
			want:   "runtime token must not contain whitespace",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewHandler(test.config)
			if err == nil || err.Error() != test.want {
				t.Fatalf("NewHandler() error = %v, want %q", err, test.want)
			}
		})
	}
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := NewHandler(Config{
		RuntimeIdentity:   " runtime-01 ",
		RuntimeGeneration: 7,
		Token:             testRuntimeToken,
	})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return h
}

func request(t *testing.T, h http.Handler, method string, target string, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	return response
}

func assertJSONHeaders(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func assertErrorCode(t *testing.T, response *httptest.ResponseRecorder, want string) {
	t.Helper()
	var got errorResponse
	decodeResponse(t, response, &got)
	if got.Error.Code != want || got.Error.Message == "" {
		t.Fatalf("error = %#v, want code %q", got.Error, want)
	}
}

func TestCloseUnconsumedResponseWriterImplicitWriteCommitsHeaderAndClosesWhenUnconsumed(t *testing.T) {
	t.Run("unconsumed_body_sets_connection_close_and_expires_read_deadline_once", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, startPath, strings.NewReader(`{"operationId":"op-1"}`))
		recorder := &uploadDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		wrapped := &closeUnconsumedResponseWriter{
			ResponseWriter: recorder,
			body:           trackRequestBody(req),
		}
		before := time.Now()
		n, err := wrapped.Write([]byte("first-"))
		after := time.Now()
		if err != nil || n != len("first-") {
			t.Fatalf("first Write = (%d, %v), want (%d, nil)", n, err, len("first-"))
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
		if _, err := wrapped.Write([]byte("second")); err != nil {
			t.Fatalf("second Write error = %v", err)
		}
		if len(recorder.readDeadlines) != 1 {
			t.Fatalf("readDeadlines after second Write = %v, want 1", recorder.readDeadlines)
		}
		if got := recorder.Body.String(); got != "first-second" {
			t.Fatalf("body = %q, want first-second", got)
		}
	})

	t.Run("consumed_body_commits_200_without_connection_close_or_expired_deadline", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, startPath, strings.NewReader(`{}`))
		recorder := &uploadDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		wrapped := &closeUnconsumedResponseWriter{
			ResponseWriter: recorder,
			body:           trackRequestBody(req),
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
