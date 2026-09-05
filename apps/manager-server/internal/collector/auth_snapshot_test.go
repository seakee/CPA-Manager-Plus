package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestAuthSnapshotResolverCacheRequiresEveryRequestedAuthIndex(t *testing.T) {
	resolver := newAuthSnapshotResolver()
	resolver.snapshots = map[string]authSnapshot{
		"auth-1": {Account: "a@example.com", Provider: "codex"},
	}

	if !resolver.hasAllLocked(map[string]struct{}{"auth-1": {}}) {
		t.Fatal("expected cache hit for known auth index")
	}
	if resolver.hasAllLocked(map[string]struct{}{"auth-1": {}, "auth-2": {}}) {
		t.Fatal("cache with missing auth index must force a refresh")
	}
}

func TestAuthSnapshotResolverTreatsDuplicateAuthIndexAsAmbiguous(t *testing.T) {
	resolver := newAuthSnapshotResolver()
	resolver.snapshots = map[string]authSnapshot{
		"auth-1": {Account: "alice@example.com", Provider: "codex"},
	}
	resolver.ambiguous = map[string]struct{}{"auth-1": {}}

	if resolver.hasAllLocked(map[string]struct{}{"auth-1": {}}) != true {
		t.Fatal("an ambiguous auth index should be considered resolved in the cache")
	}
	if snapshots := resolver.lookupLocked(map[string]struct{}{"auth-1": {}}); len(snapshots) != 0 {
		t.Fatalf("ambiguous auth index returned a snapshot: %#v", snapshots)
	}
}

func TestAuthSnapshotResolverFetchMarksDuplicateAuthIndexAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[
			{"auth_index":"auth-1","provider":"codex","account":"alice@example.com"},
			{"auth_index":"auth-1","provider":"codex","account":"bob@example.com"}
		]}`))
	}))
	t.Cleanup(server.Close)

	resolver := newAuthSnapshotResolver()
	resolver.client = server.Client()
	snapshots, ambiguous, err := resolver.fetch(context.Background(), server.URL, "management-key")
	if err != nil {
		t.Fatalf("fetch snapshots: %v", err)
	}
	if _, ok := snapshots["auth-1"]; ok {
		t.Fatalf("duplicate auth index retained a snapshot: %#v", snapshots["auth-1"])
	}
	if _, ok := ambiguous["auth-1"]; !ok {
		t.Fatalf("duplicate auth index was not marked ambiguous: %#v", ambiguous)
	}
}

func TestAuthSnapshotResolverPreservesNonCodexAccountLabelFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[
			{"auth_index":"auth-1","provider":"openai","account":"account-id"}
		]}`))
	}))
	t.Cleanup(server.Close)

	resolver := newAuthSnapshotResolver()
	resolver.client = server.Client()
	snapshots, _, err := resolver.fetch(context.Background(), server.URL, "management-key")
	if err != nil {
		t.Fatalf("fetch snapshots: %v", err)
	}
	snapshot, ok := snapshots["auth-1"]
	if !ok {
		t.Fatalf("missing auth snapshot: %#v", snapshots)
	}
	if snapshot.Account != "account-id" || snapshot.Label != "account-id" {
		t.Fatalf("non-Codex account/label fallback = account:%q label:%q, want account-id", snapshot.Account, snapshot.Label)
	}
}

func TestCodexAuthSnapshotUsesExplicitAccountIDProvenance(t *testing.T) {
	marked := usageidentity.CodexAccountIDSnapshot("account-a")
	if got := usageidentity.CodexAccountIDFromSnapshot(marked); got != "account-a" {
		t.Fatalf("marked account id = %q", got)
	}
	if got := usageidentity.CodexAccountIDFromSnapshot("generic-project"); got != "" {
		t.Fatalf("generic project unexpectedly trusted as Codex account id: %q", got)
	}
}
