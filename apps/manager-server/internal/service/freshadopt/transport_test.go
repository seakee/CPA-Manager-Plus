package freshadopt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// Both endpoints must be the management safe-read transport itself, not a
// second client with a similar comment: no proxy, no redirect following and a
// five-second budget per call, which is what the External and adoption reads are
// qualified on.
func TestEndpointsReuseTheManagementSafeReadTransport(t *testing.T) {
	redirected := make(chan struct{}, 1)
	// A destination that answers every management read with a redirect. The
	// safe-read transport returns the 3xx itself instead of following it, so the
	// redirect target must never be reached.
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		select {
		case redirected <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(target.Close)
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirecting.Close)

	source, err := NewSource(t.Context(), Endpoint{BaseURL: redirecting.URL, ManagementKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.ListCredentialFiles(t.Context()); err == nil {
		t.Fatal("a redirecting source was followed")
	}
	select {
	case <-redirected:
		t.Fatal("the redirect target was reached")
	default:
	}

	// The per-call budget is the safe-read client's five seconds, not a copy with
	// a different number in it.
	shared, closeShared, err := cpa.NewDirectClient(t.Context(), redirecting.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer closeShared()
	if client := source.w.client; client.Timeout != shared.Timeout || client.Timeout != 5*time.Second {
		t.Fatalf("source per-call timeout = %s, shared = %s", client.Timeout, shared.Timeout)
	}
	direct, ok := source.w.client.Transport.(*http.Transport)
	if !ok || direct.Proxy != nil {
		t.Fatal("the source transport is not the proxyless safe-read transport")
	}
	if source.w.client.CheckRedirect == nil {
		t.Fatal("the source transport follows redirects")
	}

	// A slow answer is bounded by that budget rather than by the call.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	deadlineSource, err := NewSource(ctx, Endpoint{BaseURL: slow.URL, ManagementKey: "key"})
	if err != nil {
		t.Fatal(err)
	}
	defer deadlineSource.Close()
	started := time.Now()
	if _, err := deadlineSource.ListCredentialFiles(ctx); err == nil {
		t.Fatal("a hung source was accepted")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("a hung source was not bounded: %s", elapsed)
	}
}
