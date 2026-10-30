package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradeauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// recordUpgradeStage writes the single Operation row into a v2 store, which is
// the shape a copy-in leaves behind: a fully migrated schema, the stage and the
// observations the copy-in records. A `complete` row without them is not a
// finished copy-in and is refused by the admission reader.
func recordUpgradeStage(t *testing.T, databasePath, stage string) {
	t.Helper()
	db, err := sqliterepo.Open(databasePath)
	if err != nil {
		t.Fatalf("open fixture store: %v", err)
	}
	defer func() { _ = db.Close() }()
	operation := model.UpgradeOperation{
		Stage:                   model.UpgradeStage(stage),
		DomainsDone:             []string{model.UpgradeDomainSourceCopy},
		SourceKind:              upgradeservice.SourceKindUpgradedLegacy,
		SourceFingerprintBefore: "source-fingerprint",
		SourceFingerprintAfter:  "source-fingerprint",
		BackupManifestSHA256:    "backup-manifest-sha256",
	}
	if err := upgradeoperation.New(db).Put(context.Background(), operation); err != nil {
		t.Fatalf("record fixture stage %q: %v", stage, err)
	}
}

// TestUnfinishedUpgradeServesOnlyTheUpgradeSurface is the startup boundary of
// the two-stage admission: the recorded stage decides the surface, not the
// schema. The copied store has a complete v2 schema (it classifies as
// native-v2), so only the operation row keeps the product surfaces closed.
func TestUnfinishedUpgradeServesOnlyTheUpgradeSurface(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	if db, err := sqliterepo.Open(databasePath); err != nil {
		t.Fatalf("create copied store: %v", err)
	} else if err := db.Close(); err != nil {
		t.Fatalf("close copied store: %v", err)
	}
	recordUpgradeStage(t, databasePath, string(model.UpgradeStageCopyIn))
	before, found, err := upgradeservice.ReadRecord(context.Background(), databasePath)
	if err != nil || !found {
		t.Fatalf("read fixture operation: found=%t err=%v", found, err)
	}

	process := startManagerServerProcessWithEnvironment(t, ownedPreflightEnvironment(dir, databasePath))
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(process.logs.String(), "serving only the Upgrade surface") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if logs := process.logs.String(); !strings.Contains(logs, "serving only the Upgrade surface") {
		process.stop(t)
		t.Fatalf("an unfinished Upgrade did not take the admitted path:\n%s", logs)
	}

	// The Upgrade authentication leaf is mounted; its method guard proves the
	// leaf itself answers instead of a generic 404.
	if status, _ := waitForManagerResponse(t, process.addr, http.MethodGet, upgradeauth.Path); status != http.StatusMethodNotAllowed {
		process.stop(t)
		t.Fatalf("Upgrade authentication leaf status=%d, want 405", status)
	}

	// Discovery status reads are served. A copied store keeps the v2 WAL mode,
	// which public discovery deliberately refuses to snapshot, so the honest
	// answer for this source is a bounded "unavailable" without a mode. What
	// matters here is that the read is answered by the Upgrade surface and that
	// it never advertises a finished install.
	status, body := waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.Path)
	if status != http.StatusOK && status != http.StatusServiceUnavailable {
		process.stop(t)
		t.Fatalf("upgrade-only discovery status=%d body=%s", status, body)
	}
	for _, forbidden := range []string{`"mode":"fresh"`, `"mode":"upgrade"`, `"mode":"completed"`} {
		if strings.Contains(body, forbidden) {
			process.stop(t)
			t.Fatalf("unfinished Upgrade advertised %s: %s", forbidden, body)
		}
	}

	// Console, gateway and lifecycle routes stay closed, and the process is
	// never ready for writes.
	if health, _ := waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.HealthPath); health != http.StatusServiceUnavailable {
		process.stop(t)
		t.Fatalf("upgrade-only health status=%d, want 503", health)
	}
	for _, probe := range []struct{ method, path string }{
		{method: http.MethodPost, path: "/usage-service/config"},
		{method: http.MethodGet, path: "/usage-service/config"},
		{method: http.MethodGet, path: "/usage-service/dashboard"},
		{method: http.MethodGet, path: "/api/gateway/keys"},
	} {
		if status, _ := waitForManagerResponse(t, process.addr, probe.method, probe.path); status != http.StatusNotFound {
			process.stop(t)
			t.Fatalf("%s %s served status=%d on the Upgrade surface, want 404", probe.method, probe.path, status)
		}
	}
	process.stop(t)

	// The admitted process recorded no progress: copy_in is still the stage.
	after, found, err := upgradeservice.ReadRecord(context.Background(), databasePath)
	if err != nil || !found {
		t.Fatalf("read operation after startup: found=%t err=%v", found, err)
	}
	if after.Stage != before.Stage || after.SourceKind != before.SourceKind || after.UpdatedAtMS != before.UpdatedAtMS {
		t.Fatalf("upgrade-only startup changed the operation: %+v -> %+v", before, after)
	}

	// A completed operation stops restricting the process: the same read-only
	// reader the server injects reports the full surface.
	recordUpgradeStage(t, databasePath, string(model.UpgradeStageComplete))
	stage, err := (legacypreflight.DatabaseUpgradeAdmission{DatabasePath: databasePath}).Admitted(context.Background())
	if err != nil {
		t.Fatalf("read completed admission: %v", err)
	}
	if legacypreflight.UpgradeSurfaceOnly(stage) {
		t.Fatalf("completed operation still restricted startup: %q", stage)
	}
	admission := legacypreflight.Admission{UpgradeStage: stage}
	if surface := admission.Surface(); surface != legacypreflight.SurfaceFull {
		t.Fatalf("completed operation surface = %q, want %q", surface, legacypreflight.SurfaceFull)
	}
	// And no record keeps the historical startup rule: the copied store is a
	// native v2 store, so it is admitted by its schema alone.
	stage, err = (legacypreflight.DatabaseUpgradeAdmission{DatabasePath: filepath.Join(t.TempDir(), "absent.sqlite")}).Admitted(context.Background())
	if err != nil || stage != legacypreflight.UpgradeStageNone {
		t.Fatalf("absent operation = %q/%v, want none", stage, err)
	}
}
