package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	bootstrapcontract "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/contract/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// TestWALStoreWithoutAdmissionStillProjectsUnavailable is the negative half of
// the UpgradeOnly projection. Only a caller that already holds the admission may
// project its inspection, so the fenced discovery path is unchanged: a v2 WAL
// store that no admission granted still fails closed instead of borrowing the
// admitted projection. Legacy and recovery sources keep exactly the answer they
// had.
func TestWALStoreWithoutAdmissionStillProjectsUnavailable(t *testing.T) {
	t.Setenv(legacyRootEnvironment, "")
	database := upgradeSurfaceManagerStore(t, "manager-store-administrator-key", false)
	assertWALStore(t, database)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(database), legacypreflight.Options{}, 5*time.Second)
	if h.readOnlyDiscovery == nil {
		t.Fatal("the fixed discovery path was not mounted")
	}
	server := httptest.NewServer(h)
	defer server.Close()

	response, err := server.Client().Get(server.URL + bootstrapdiscovery.Path)
	if err != nil {
		t.Fatalf("read discovery: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read discovery body: %v", err)
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("discovery status = %d, want 503; body = %s", response.StatusCode, body)
	}
	var discovery bootstrapcontract.DiscoveryDTO
	if err := json.Unmarshal(body, &discovery); err != nil {
		t.Fatalf("decode discovery: %v; body = %s", err, body)
	}
	if discovery.Status != bootstrap.PublicUnavailable ||
		discovery.AuthEntry != bootstrap.AuthEntryUnavailable ||
		discovery.Reason != bootstrap.ReasonObservationUnavailable ||
		discovery.Mode != nil {
		t.Fatalf("discovery = %+v, want the fail-closed observation", discovery)
	}
}

// assertWALStore proves the fixture is the shape the rollback-mode discovery
// snapshot refuses: the SQLite header's write/read format bytes are 2/2.
func assertWALStore(t *testing.T, database string) {
	t.Helper()
	file, err := os.Open(database)
	if err != nil {
		t.Fatalf("open the fixture store: %v", err)
	}
	defer func() { _ = file.Close() }()
	header := make([]byte, 20)
	if _, err := io.ReadFull(file, header); err != nil {
		t.Fatalf("read the fixture header: %v", err)
	}
	if header[18] != 2 || header[19] != 2 {
		t.Fatalf("the fixture store is not in WAL mode: header=%v", header[18:20])
	}
}
