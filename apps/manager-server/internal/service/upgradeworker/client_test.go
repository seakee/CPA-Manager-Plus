package upgradeworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	upgrade "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

func seedPreparedTargetDatabase(t *testing.T, dbPath, adminKey, fingerprintAfter string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(dbPath + suffix)
	}
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	cred, err := security.NewAdminCredential(adminKey, "legacy")
	if err != nil {
		_ = db.Close()
		t.Fatalf("create admin credential: %v", err)
	}
	if err := setting.New(db).SaveAdminCredential(context.Background(), cred); err != nil {
		_ = db.Close()
		t.Fatalf("save admin credential: %v", err)
	}
	if err := upgradeoperation.New(db).Put(context.Background(), model.UpgradeOperation{
		Stage:                   model.UpgradeStageReconcile,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "unknown",
		SourceFingerprintBefore: strings.Repeat("1", 64),
		SourceFingerprintAfter:  fingerprintAfter,
		BackupManifestSHA256:    strings.Repeat("2", 64),
		ManualItems:             []string{},
	}); err != nil {
		_ = db.Close()
		t.Fatalf("put upgrade operation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite db: %v", err)
	}
}

func TestClientFallbackAuthenticateAndRevisionUsesSingleSnapshotAndPrefersSnapshotRevision(t *testing.T) {
	const (
		origin = "http://manager.test:8380"
		key1   = "cpamp_FirstTargetAdminSecret0123456789"
		key2   = "cpamp_ReplacedTargetAdminSecret987654321"
	)
	rev1 := strings.Repeat("a", 64)
	rev2 := strings.Repeat("b", 64)
	staleCachedRev := strings.Repeat("f", 64)

	targetDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve target dir: %v", err)
	}
	dbPath := filepath.Join(targetDir, "usage.sqlite")
	seedPreparedTargetDatabase(t, dbPath, key1, rev1)

	var snapshotOpens atomic.Int32
	verifier := NewPreparedCredentialVerifier(dbPath)
	verifier.openSnapshot = func(ctx context.Context, path, scratch string) (*upgrade.Snapshot, error) {
		snapshotOpens.Add(1)
		return upgrade.OpenSnapshot(ctx, path, scratch)
	}

	client := NewClient(filepath.Join(t.TempDir(), "down.sock"), origin)
	client.FallbackAuthenticateAndRevision = verifier.AuthenticateAndRevision

	doEntry := func(authKey, expectedRev string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, origin+EntryPath, nil)
		req.Header.Set("Authorization", "Bearer "+authKey)
		req.Header.Set("Origin", origin)
		if expectedRev != "" {
			req.Header.Set("X-CPAMP-Upgrade-Source", expectedRev)
		}
		rec := httptest.NewRecorder()
		client.ServeHTTP(rec, req)
		return rec
	}

	// 1. Single snapshot verifies key1 and returns rev1 from that snapshot, ignoring staleCachedRev.
	rec1 := doEntry(key1, "")
	if rec1.Code != http.StatusOK {
		t.Fatalf("entry with key1 = %d: %s", rec1.Code, rec1.Body.String())
	}
	if opens := snapshotOpens.Load(); opens != 1 {
		t.Fatalf("snapshot opens for single fallback entry auth = %d, want 1", opens)
	}
	var doc1 struct {
		SourceRevision string `json:"source_revision"`
	}
	if err := json.Unmarshal(rec1.Body.Bytes(), &doc1); err != nil {
		t.Fatalf("decode entry response: %v", err)
	}
	if doc1.SourceRevision != rev1 {
		t.Fatalf("source_revision = %q, want %q (not stale cached %q)", doc1.SourceRevision, rev1, staleCachedRev)
	}

	// 2. Replace target store with (key2, rev2); key1 is rejected, staleCachedRev is refused, and key2 returns rev2.
	seedPreparedTargetDatabase(t, dbPath, key2, rev2)

	if recOldKey := doEntry(key1, ""); recOldKey.Code != http.StatusUnauthorized {
		t.Fatalf("replaced store with old key1 = %d, want 401", recOldKey.Code)
	}
	if recStaleHeader := doEntry(key2, staleCachedRev); recStaleHeader.Code != http.StatusServiceUnavailable {
		t.Fatalf("replaced store with stale X-CPAMP-Upgrade-Source = %d, want 503", recStaleHeader.Code)
	}

	snapshotOpens.Store(0)
	rec2 := doEntry(key2, "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("replaced store with key2 = %d: %s", rec2.Code, rec2.Body.String())
	}
	if opens := snapshotOpens.Load(); opens != 1 {
		t.Fatalf("snapshot opens for replaced store entry auth = %d, want 1", opens)
	}
	var doc2 struct {
		SourceRevision string `json:"source_revision"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &doc2); err != nil {
		t.Fatalf("decode second entry response: %v", err)
	}
	if doc2.SourceRevision != rev2 {
		t.Fatalf("replaced store source_revision = %q, want %q", doc2.SourceRevision, rev2)
	}
	if recMatch := doEntry(key2, rev2); recMatch.Code != http.StatusOK {
		t.Fatalf("replaced store with matching X-CPAMP-Upgrade-Source = %d, want 200", recMatch.Code)
	}

	// 3. VerifyHeader honors FallbackAuthenticateAndRevision when worker socket is unreachable.
	okValid, err := client.VerifyHeader(context.Background(), "Bearer "+key2)
	if err != nil || !okValid {
		t.Fatalf("VerifyHeader(key2) = (%v, %v), want (true, nil)", okValid, err)
	}
	okWrong, err := client.VerifyHeader(context.Background(), "Bearer "+key1)
	if err != nil || okWrong {
		t.Fatalf("VerifyHeader(key1) = (%v, %v), want (false, nil)", okWrong, err)
	}
}

func TestClientFallbackAuthenticateWithRevisionCallbackAndMissingRevisionFailClosed(t *testing.T) {
	const (
		origin = "http://manager.test:8380"
		key    = "cpamp_LegacyFallbackCallback0123456789ab"
	)
	rev := strings.Repeat("c", 64)
	otherRev := strings.Repeat("d", 64)

	targetDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve target dir: %v", err)
	}
	dbPath := filepath.Join(targetDir, "usage.sqlite")
	seedPreparedTargetDatabase(t, dbPath, key, rev)

	verifier := NewPreparedCredentialVerifier(dbPath)
	client := NewClient(filepath.Join(t.TempDir(), "down.sock"), origin)
	client.FallbackAuthenticate = verifier.Authenticate
	client.FallbackRevision = verifier.RecordedRevision

	doEntry := func(c *Client, expectedRev string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, origin+EntryPath, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Origin", origin)
		if expectedRev != "" {
			req.Header.Set("X-CPAMP-Upgrade-Source", expectedRev)
		}
		rec := httptest.NewRecorder()
		c.ServeHTTP(rec, req)
		return rec
	}

	rec := doEntry(client, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("FallbackAuthenticate + FallbackRevision initial sign-in = %d: %s", rec.Code, rec.Body.String())
	}
	var doc struct {
		SourceRevision string `json:"source_revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode entry response: %v", err)
	}
	if doc.SourceRevision != rev {
		t.Fatalf("source_revision = %q, want %q", doc.SourceRevision, rev)
	}
	if recMatch := doEntry(client, rev); recMatch.Code != http.StatusOK {
		t.Fatalf("matching fallback renewal = %d, want 200", recMatch.Code)
	}
	if recMismatch := doEntry(client, otherRev); recMismatch.Code != http.StatusServiceUnavailable {
		t.Fatalf("mismatched fallback renewal = %d, want 503", recMismatch.Code)
	}

	// FallbackAuthenticate without FallbackRevision or FallbackAuthenticateAndRevision fails closed.
	noRevClient := NewClient(filepath.Join(t.TempDir(), "down2.sock"), origin)
	noRevClient.FallbackAuthenticate = verifier.Authenticate
	if recNoRev := doEntry(noRevClient, ""); recNoRev.Code != http.StatusServiceUnavailable {
		t.Fatalf("FallbackAuthenticate without FallbackRevision = %d, want 503", recNoRev.Code)
	}
}

func TestOperationRevisionAndAuthenticateIdentity(t *testing.T) {
	validOp := model.UpgradeOperation{
		Stage:                   model.UpgradeStageReconcile,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "unknown",
		SourceFingerprintBefore: strings.Repeat("1", 64),
		SourceFingerprintAfter:  "  " + strings.Repeat("A", 64) + " ",
		BackupManifestSHA256:    strings.Repeat("B", 64),
		ManualItems:             []string{},
	}
	sum := sha256.Sum256([]byte("cpamp-upgrade-operation-v1:" + strings.Repeat("a", 64) + ":" + strings.Repeat("b", 64)))
	wantOpRev := hex.EncodeToString(sum[:])
	if got := OperationRevision(validOp); got != wantOpRev {
		t.Fatalf("OperationRevision(validOp) = %q, want %q", got, wantOpRev)
	}

	for _, invalid := range []model.UpgradeOperation{
		{},
		{Stage: model.UpgradeStagePreflight, DomainsDone: []string{model.UpgradeDomainSourceCopy}, SourceKind: "upgraded-legacy", SourceFingerprintBefore: strings.Repeat("1", 64), SourceFingerprintAfter: strings.Repeat("a", 64), BackupManifestSHA256: strings.Repeat("b", 64)},
		{Stage: model.UpgradeStageRolledBack, DomainsDone: []string{model.UpgradeDomainSourceCopy}, SourceKind: "upgraded-legacy", SourceFingerprintBefore: strings.Repeat("1", 64), SourceFingerprintAfter: strings.Repeat("a", 64), BackupManifestSHA256: strings.Repeat("b", 64)},
		{Stage: model.UpgradeStageReconcile, DomainsDone: []string{model.UpgradeDomainSourceCopy}, SourceKind: "upgraded-legacy", SourceFingerprintBefore: strings.Repeat("1", 64), SourceFingerprintAfter: "not-hex", BackupManifestSHA256: strings.Repeat("b", 64)},
		{Stage: model.UpgradeStageReconcile, DomainsDone: []string{model.UpgradeDomainSourceCopy}, SourceKind: "upgraded-legacy", SourceFingerprintBefore: strings.Repeat("1", 64), SourceFingerprintAfter: strings.Repeat("a", 64), BackupManifestSHA256: ""},
	} {
		if got := OperationRevision(invalid); got != "" {
			t.Fatalf("OperationRevision(invalid=%+v) = %q, want empty", invalid, got)
		}
	}

	const (
		origin   = "http://manager.test:8380"
		adminKey = "cpamp_IdentityVerifierSecret0123456789"
	)
	targetDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve target dir: %v", err)
	}
	dbPath := filepath.Join(targetDir, "usage.sqlite")
	afterRev := strings.Repeat("c", 64)
	seedPreparedTargetDatabase(t, dbPath, adminKey, afterRev)
	expectedOpRev := OperationRevision(model.UpgradeOperation{
		Stage:                   model.UpgradeStageReconcile,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
		SourceKind:              "upgraded-legacy",
		SourceFingerprintBefore: strings.Repeat("1", 64),
		SourceFingerprintAfter:  afterRev,
		BackupManifestSHA256:    strings.Repeat("2", 64),
	})

	var snapshotOpens atomic.Int32
	verifier := NewPreparedCredentialVerifier(dbPath)
	verifier.openSnapshot = func(ctx context.Context, path, scratch string) (*upgrade.Snapshot, error) {
		snapshotOpens.Add(1)
		return upgrade.OpenSnapshot(ctx, path, scratch)
	}

	srcRev, opRev, err := verifier.AuthenticateIdentity(context.Background(), "Bearer "+adminKey)
	if err != nil {
		t.Fatalf("AuthenticateIdentity(Bearer): %v", err)
	}
	if snapshotOpens.Load() != 1 {
		t.Fatalf("snapshot opens = %d, want 1", snapshotOpens.Load())
	}
	if srcRev != afterRev || opRev != expectedOpRev {
		t.Fatalf("AuthenticateIdentity = (%q, %q), want (%q, %q)", srcRev, opRev, afterRev, expectedOpRev)
	}

	rawSrcRev, rawOpRev, err := verifier.AuthenticateIdentity(context.Background(), adminKey)
	if err != nil || rawSrcRev != afterRev || rawOpRev != expectedOpRev {
		t.Fatalf("AuthenticateIdentity(raw) = (%q, %q, %v), want (%q, %q, nil)", rawSrcRev, rawOpRev, err, afterRev, expectedOpRev)
	}

	for _, badCred := range []string{"", "Basic " + adminKey, "Bearer wrong", "wrong"} {
		if _, _, err := verifier.AuthenticateIdentity(context.Background(), badCred); !errors.Is(err, upgrade.ErrAdminUnauthorized) {
			t.Fatalf("AuthenticateIdentity(%q) err = %v, want ErrAdminUnauthorized", badCred, err)
		}
	}

	doEntry := func(c *Client, expectedRev string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, origin+EntryPath, nil)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		req.Header.Set("Origin", origin)
		if expectedRev != "" {
			req.Header.Set("X-CPAMP-Upgrade-Source", expectedRev)
		}
		rec := httptest.NewRecorder()
		c.ServeHTTP(rec, req)
		return rec
	}

	// FallbackAuthenticateIdentity takes precedence over legacy callbacks and emits operation_revision.
	client := NewClient(filepath.Join(t.TempDir(), "down.sock"), origin)
	client.FallbackAuthenticateIdentity = verifier.AuthenticateIdentity
	client.FallbackAuthenticateAndRevision = func(context.Context, string) (string, error) {
		t.Fatal("FallbackAuthenticateAndRevision should not be called when FallbackAuthenticateIdentity is set")
		return "", nil
	}
	rec := doEntry(client, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("entry with FallbackAuthenticateIdentity = %d: %s", rec.Code, rec.Body.String())
	}
	var doc struct {
		SourceRevision    string `json:"source_revision"`
		OperationRevision string `json:"operation_revision"`
		Status            string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode entry response: %v", err)
	}
	if doc.SourceRevision != afterRev || doc.OperationRevision != expectedOpRev || doc.Status != "observation_unavailable" {
		t.Fatalf("entry doc = %+v, want source=%q operation=%q status=observation_unavailable", doc, afterRev, expectedOpRev)
	}

	// VerifyHeader honors FallbackAuthenticateIdentity when worker socket is unreachable.
	if ok, err := client.VerifyHeader(context.Background(), "Bearer "+adminKey); err != nil || !ok {
		t.Fatalf("VerifyHeader(valid) = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := client.VerifyHeader(context.Background(), "Bearer wrong"); err != nil || ok {
		t.Fatalf("VerifyHeader(wrong) = (%v, %v), want (false, nil)", ok, err)
	}

	// Legacy FallbackAuthenticateAndRevision fills operation_revision == source_revision.
	legacyClient := NewClient(filepath.Join(t.TempDir(), "down2.sock"), origin)
	legacyClient.FallbackAuthenticateAndRevision = verifier.AuthenticateAndRevision
	legacyRec := doEntry(legacyClient, "")
	if legacyRec.Code != http.StatusOK {
		t.Fatalf("legacy entry = %d: %s", legacyRec.Code, legacyRec.Body.String())
	}
	var legacyDoc struct {
		SourceRevision    string `json:"source_revision"`
		OperationRevision string `json:"operation_revision"`
	}
	if err := json.Unmarshal(legacyRec.Body.Bytes(), &legacyDoc); err != nil {
		t.Fatalf("decode legacy entry: %v", err)
	}
	if legacyDoc.SourceRevision != afterRev || legacyDoc.OperationRevision != afterRev {
		t.Fatalf("legacy fallback doc = %+v, want both=%q", legacyDoc, afterRev)
	}

	// Invalid or empty operation_revision from a custom callback omits operation_revision field.
	noOpRevClient := NewClient(filepath.Join(t.TempDir(), "down3.sock"), origin)
	noOpRevClient.FallbackAuthenticateIdentity = func(context.Context, string) (string, string, error) {
		return afterRev, "", nil
	}
	noOpRec := doEntry(noOpRevClient, "")
	if noOpRec.Code != http.StatusOK {
		t.Fatalf("entry without operation_revision = %d: %s", noOpRec.Code, noOpRec.Body.String())
	}
	var rawDoc map[string]any
	if err := json.Unmarshal(noOpRec.Body.Bytes(), &rawDoc); err != nil {
		t.Fatalf("decode raw entry: %v", err)
	}
	if _, exists := rawDoc["operation_revision"]; exists {
		t.Fatalf("expected operation_revision omitted when empty, got %v", rawDoc)
	}
}
