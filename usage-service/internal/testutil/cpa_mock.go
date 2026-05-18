package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type ObservedRequest struct {
	Method        string
	Path          string
	Query         string
	Authorization string
	Body          string
}

type CPAMock struct {
	server *httptest.Server

	ManagementKey          string
	UsageStatisticsEnabled bool
	RetentionSeconds       int

	mu       sync.Mutex
	requests []ObservedRequest
}

func NewCPAMock(t testing.TB) *CPAMock {
	t.Helper()
	mock := &CPAMock{
		ManagementKey:    "management-key",
		RetentionSeconds: 60,
	}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.ServeHTTP))
	t.Cleanup(mock.Close)
	return mock
}

func (m *CPAMock) URL() string {
	return m.server.URL
}

func (m *CPAMock) Close() {
	if m.server != nil {
		m.server.Close()
	}
}

func (m *CPAMock) Requests() []ObservedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ObservedRequest, len(m.requests))
	copy(out, m.requests)
	return out
}

func (m *CPAMock) LastRequest(path string) (ObservedRequest, bool) {
	requests := m.Requests()
	for i := len(requests) - 1; i >= 0; i-- {
		if requests[i].Path == path {
			return requests[i], true
		}
	}
	return ObservedRequest{}, false
}

func (m *CPAMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.record(ObservedRequest{
		Method:        r.Method,
		Path:          r.URL.Path,
		Query:         r.URL.RawQuery,
		Authorization: r.Header.Get("Authorization"),
		Body:          string(body),
	})

	switch {
	case r.URL.Path == "/v0/management/config" && r.Method == http.MethodGet:
		if !m.hasManagementAuth(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		writeJSON(w, map[string]any{
			"usage-statistics-enabled":            m.UsageStatisticsEnabled,
			"redis-usage-queue-retention-seconds": m.RetentionSeconds,
		})
	case r.URL.Path == "/v0/management/usage-statistics-enabled" && r.Method == http.MethodPut:
		if !m.hasManagementAuth(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var payload struct {
			Value bool `json:"value"`
		}
		_ = json.Unmarshal(body, &payload)
		m.mu.Lock()
		m.UsageStatisticsEnabled = payload.Value
		m.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	case r.URL.Path == "/v0/management/usage-queue" && r.Method == http.MethodGet:
		if !m.hasManagementAuth(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		writeJSON(w, []string{})
	case strings.HasPrefix(r.URL.Path, "/v0/management/"):
		if !m.hasManagementAuth(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		writeJSON(w, map[string]any{
			"ok":     true,
			"method": r.Method,
			"path":   r.URL.Path,
		})
	case r.URL.Path == "/v1/models" || r.URL.Path == "/models":
		writeJSON(w, map[string]any{
			"data": []map[string]string{{"id": "gpt-test"}},
		})
	default:
		http.NotFound(w, r)
	}
}

func (m *CPAMock) hasManagementAuth(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer "+m.ManagementKey
}

func (m *CPAMock) record(req ObservedRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
