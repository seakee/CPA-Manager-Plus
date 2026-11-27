package httpqueue

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientPopReadsUsageQueue(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/usage-queue" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("count") != "25" {
			t.Fatalf("count = %q", r.URL.Query().Get("count"))
		}
		if r.Header.Get("Authorization") != "Bearer management-key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"timestamp":"2026-05-06T00:00:00Z","model":"gpt-test"},
			"{\"timestamp\":\"2026-05-06T00:00:01Z\",\"model\":\"gpt-string\"}",
			null
		]`))
	}))
	t.Cleanup(upstream.Close)

	items, err := New(upstream.URL, "management-key").Pop(context.Background(), 25)
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2: %#v", len(items), items)
	}
	if !strings.Contains(items[0], `"model":"gpt-test"`) {
		t.Fatalf("object item = %s", items[0])
	}
	if !strings.Contains(items[1], `"model":"gpt-string"`) {
		t.Fatalf("string item = %s", items[1])
	}
}

func TestClientPopClassifiesUnsupportedEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(upstream.Close)

	_, err := New(upstream.URL, "management-key").Pop(context.Background(), 10)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestClientPopKeepsAuthErrorsDistinct(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	t.Cleanup(upstream.Close)

	_, err := New(upstream.URL, "management-key").Pop(context.Background(), 10)
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %T %v, want StatusError", err, err)
	}
	if statusErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", statusErr.StatusCode)
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatalf("auth error must not be classified as unsupported")
	}
}

func TestClientPopClassifiesPrePopVsUncertainRemovalErrors(t *testing.T) {
	// Closed local port -> TCP dial failure (pre-pop, not uncertain).
	closedSrv := httptest.NewServer(http.NewServeMux())
	closedURL := closedSrv.URL
	closedSrv.Close()
	_, dialErr := New(closedURL, "management-key").Pop(context.Background(), 10)
	if dialErr == nil || IsUncertainPop(dialErr) {
		t.Fatalf("dial error = %v (uncertain=%v), want non-uncertain dial error", dialErr, IsUncertainPop(dialErr))
	}

	// Truncated 200 JSON body -> uncertain removal.
	truncSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":1`))
	}))
	defer truncSrv.Close()
	_, truncErr := New(truncSrv.URL, "management-key").Pop(context.Background(), 10)
	if !IsUncertainPop(truncErr) {
		t.Fatalf("truncated body err = %v, want IsUncertainPop=true", truncErr)
	}

	// Dropped connection after receiving request -> uncertain removal.
	dropSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("missing hijacker")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}))
	defer dropSrv.Close()
	_, dropErr := New(dropSrv.URL, "management-key").Pop(context.Background(), 10)
	if !IsUncertainPop(dropErr) {
		t.Fatalf("dropped reply err = %v, want IsUncertainPop=true", dropErr)
	}

	// 500 response -> uncertain removal.
	err500Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "oops", http.StatusInternalServerError)
	}))
	defer err500Srv.Close()
	_, err500 := New(err500Srv.URL, "management-key").Pop(context.Background(), 10)
	if !IsUncertainPop(err500) {
		t.Fatalf("500 err = %v, want IsUncertainPop=true", err500)
	}
}
