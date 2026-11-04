package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	bootstrapcontract "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/contract/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradeauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// admissionAdminKey is the copied store's administrator credential: a real
// copy-in carries the source's credential, which is what makes the administrator
// entry the right public projection for an unfinished operation.
const admissionAdminKey = "cpamp_Admission_Key_0001"

// seedCopiedStore creates the store a copy-in leaves behind: a fully migrated v2
// database in WAL mode that already carries the administrator credential.
func seedCopiedStore(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sqliterepo.Open(databasePath)
	if err != nil {
		t.Fatalf("create copied store: %v", err)
	}
	credential, err := security.NewAdminCredential(admissionAdminKey, "admission-test")
	if err != nil {
		_ = db.Close()
		t.Fatalf("build the administrator credential: %v", err)
	}
	if err := setting.New(db).SaveAdminCredential(t.Context(), credential); err != nil {
		_ = db.Close()
		t.Fatalf("store the administrator credential: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close copied store: %v", err)
	}
}

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
	repository := upgradeoperation.New(db)
	ctx := context.Background()
	// Put is a compare-and-set: a fixture that replaces an existing row has to
	// read it first, exactly like every other writer.
	operation, found, err := repository.Get(ctx)
	if err != nil {
		t.Fatalf("read fixture operation: %v", err)
	}
	if !found {
		operation = model.UpgradeOperation{
			DomainsDone: []string{model.UpgradeDomainSourceCopy},
		}
	}
	priorStage := operation.Stage
	operation.Stage = model.UpgradeStage(stage)
	operation.SourceKind = upgradeservice.SourceKindUpgradedLegacy
	operation.SourceFingerprintBefore = "source-fingerprint"
	operation.SourceFingerprintAfter = "source-fingerprint"
	operation.BackupManifestSHA256 = "backup-manifest-sha256"
	if operation.Stage == model.UpgradeStageComplete {
		// The complete verdict is the gate's record, and it is never a bare
		// stage: this fixture writes the same whole observation the gate would, so
		// the startup reader sees the row a finished operation actually holds.
		// The observations are recorded at the stage the row sat at first, because
		// `complete` itself is the gate's to write.
		recordStage := priorStage
		if recordStage == "" {
			recordStage = model.UpgradeStagePreflight
		}
		operation.Stage = recordStage
		if err := repository.Put(ctx, operation); err != nil {
			t.Fatalf("record fixture observations: %v", err)
		}
		stored, found, err := repository.Get(ctx)
		if err != nil || !found {
			t.Fatalf("read fixture observations: found=%t err=%v", found, err)
		}
		for _, domain := range []string{model.UpgradeDomainIdentity, model.UpgradeDomainCPAConfig} {
			if !stored.HasDomain(domain) {
				advanced, err := repository.Advance(ctx, domain, stored.Stage)
				if err != nil {
					t.Fatalf("record fixture domain %q: %v", domain, err)
				}
				stored = advanced
			}
		}
		verification := model.UpgradeVerification{
			TargetMode:          model.UpgradeTargetExternal,
			SourceFingerprint:   stored.SourceFingerprintAfter,
			Domains:             fixtureDomains(stored.DomainsDone),
			BindingIdentity:     "https://cpa.example",
			BindingGeneration:   "https://cpa.example",
			BindingObservedAtMS: 1_700_000_000_000,
			ObservedAtMS:        1_700_000_000_001,
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin fixture completion: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := upgradeoperation.RecordExternalBindingTx(ctx, tx, upgradeoperation.ExternalBinding{
			BaseURL:      "https://cpa.example",
			Identity:     verification.BindingIdentity,
			Generation:   verification.BindingGeneration,
			ObservedAtMS: verification.BindingObservedAtMS,
		}); err != nil {
			t.Fatalf("record fixture binding: %v", err)
		}
		if _, err := upgradeoperation.RecordCompletionTx(ctx, tx, verification); err != nil {
			t.Fatalf("record fixture completion: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit fixture completion: %v", err)
		}
		return
	}
	if err := repository.Put(ctx, operation); err != nil {
		t.Fatalf("record fixture stage %q: %v", stage, err)
	}
}

// fixtureDomains is the domain list a completion fixture names: the recorded
// domains, never an invented set. The copy-in domain stands in when the fixture
// created no operation beforehand.
func fixtureDomains(domains []string) []string {
	if len(domains) == 0 {
		return []string{model.UpgradeDomainSourceCopy}
	}
	return append([]string{}, domains...)
}

// TestUnfinishedUpgradeServesOnlyTheUpgradeSurface is the startup boundary of
// the two-stage admission: the recorded stage decides the surface, not the
// schema. The copied store has a complete v2 schema (it classifies as
// native-v2), so only the operation row keeps the product surfaces closed.
func TestUnfinishedUpgradeServesOnlyTheUpgradeSurface(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	seedCopiedStore(t, databasePath)
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

	// The Upgrade authentication leaf is mounted; its own credential verdict
	// proves the leaf itself answers instead of a generic 404. The credential is
	// answered before the verb, so an unauthenticated caller sees 401 here even
	// with the wrong method — it cannot learn that this path takes POST.
	if status, _ := waitForManagerResponse(t, process.addr, http.MethodGet, upgradeauth.Path); status != http.StatusUnauthorized {
		process.stop(t)
		t.Fatalf("Upgrade authentication leaf status=%d, want 401", status)
	}

	// Discovery status reads project the inspection this process was admitted on.
	// A copied store keeps the v2 WAL journal, which the rollback-mode discovery
	// snapshot refuses — a fact about the snapshot, not about the source — so a
	// gate that re-inspected the store would answer "observation unavailable" and
	// the browser could never reach the administrator entry that opens the one
	// surface able to finish the operation. The answer must be the bounded
	// administrator-entry projection, and it must still never advertise a
	// finished install.
	status, body := waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.Path)
	if status != http.StatusOK {
		process.stop(t)
		t.Fatalf("upgrade-only discovery status=%d body=%s, want 200", status, body)
	}
	var discovery bootstrapcontract.DiscoveryDTO
	if err := json.Unmarshal([]byte(body), &discovery); err != nil {
		process.stop(t)
		t.Fatalf("decode discovery: %v; body=%s", err, body)
	}
	if discovery.Status != bootstrap.PublicUnsupportedUpgrade ||
		discovery.Mode == nil || *discovery.Mode != bootstrap.UnsupportedUpgrade ||
		discovery.AuthEntry != bootstrap.AuthEntryAdminSession ||
		discovery.Reason != bootstrap.ReasonQualificationUnavailable ||
		!discovery.AuthenticationRequired {
		process.stop(t)
		t.Fatalf("upgrade-only discovery = %+v, want the administrator entry", discovery)
	}
	if err := discovery.Validate(); err != nil {
		process.stop(t)
		t.Fatalf("upgrade-only discovery is not a valid public projection: %v", err)
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

// TestCompletedUpgradeStartsTheNormalProduct walks the other side of the
// admission through the real process: a store whose operation the completion gate
// recorded as complete no longer takes the Upgrade-only branch. The server reaches
// the writable product bootstrap instead, which is the path every install takes
// after admission — and the Upgrade surface is not what answers it.
func TestCompletedUpgradeStartsTheNormalProduct(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	if db, err := sqliterepo.Open(databasePath); err != nil {
		t.Fatalf("create copied store: %v", err)
	} else if err := db.Close(); err != nil {
		t.Fatalf("close copied store: %v", err)
	}
	// The gate continues a recorded operation, so the fixture records the
	// connect position first and then the verdict, with the evidence it rests on.
	recordUpgradeStage(t, databasePath, string(model.UpgradeStageExternalConnect))
	recordUpgradeStage(t, databasePath, string(model.UpgradeStageComplete))
	recorded, found, err := upgradeservice.ReadRecord(context.Background(), databasePath)
	if err != nil || !found {
		t.Fatalf("read fixture operation: found=%t err=%v", found, err)
	}
	if recorded.Stage != model.UpgradeStageComplete {
		t.Fatalf("fixture stage = %q, want complete", recorded.Stage)
	}
	if _, err := recorded.VerificationEvidence(); err != nil {
		t.Fatalf("the completed fixture carries no verification: %v", err)
	}

	process := startManagerServerProcessWithEnvironment(t, ownedPreflightEnvironment(dir, databasePath))
	// The writable bootstrap's own verdict is the marker. The Upgrade-only branch
	// is the one log line that must not appear: a completed operation is never
	// held on that surface again.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		logs := process.logs.String()
		if strings.Contains(logs, "bootstrap manager server:") || strings.Contains(logs, "serving only the Upgrade surface") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	logs := process.logs.String()
	if strings.Contains(logs, "serving only the Upgrade surface") {
		t.Fatalf("a completed Upgrade still served the Upgrade surface:\n%s", logs)
	}
	// The product bootstrap replaces the Upgrade surface. With no host-controlled
	// initialize run yet it stops on exactly that step, which is the same answer
	// every admitted store gets; the point here is that the Upgrade surface is no
	// longer the branch startup took.
	if !strings.Contains(logs, "bootstrap manager server:") {
		t.Fatalf("a completed Upgrade did not enter the product bootstrap:\n%s", logs)
	}
	// The process leaves the upgrade-only branch, so it ends after the bootstrap
	// refusal; its exit belongs to that verdict, not to this test.
	select {
	case <-process.done:
	case <-time.After(20 * time.Second):
		process.stop(t)
		t.Fatal("the product bootstrap did not return after a completed Upgrade")
	}
}
