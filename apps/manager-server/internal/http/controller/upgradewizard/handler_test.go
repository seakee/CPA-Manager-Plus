package upgradewizard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
)

const (
	testPrefix = "/.well-known/cpamp-bootstrap/api/v2"
	canonical  = "/cpamp-preview-abc123/api/v2"
)

func allow(*http.Request) int { return 0 }

func request(t *testing.T, handler http.Handler, method, path string, want int) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Host = "manager.test"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w.Code, w.Body.String()
}

// legacySourceDir writes the smallest 1.x data directory the read-only preflight
// will inspect. The report may be blocked (a synthetic source has no ownership
// fence or CPA classification) — the route contract is a 200 report either way.
func legacySourceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.key"), []byte("legacy-data-key-material"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sqliterepo.Open(filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newHandler(legacyRoot, database string, authorize func(*http.Request) int) Handler {
	return Handler{
		Authorize:  authorize,
		LegacyRoot: func() string { return legacyRoot },
		Database:   func() string { return database },
		Timeout:    10 * time.Second,
	}
}

// TestPreflightRequiresAuthorizationAndServesAReport covers the route gate and
// the pre-cutover read: an unauthenticated caller is refused, a method mismatch
// is refused, and the authorized caller receives a report with every check.
func TestPreflightRequiresAuthorizationAndServesAReport(t *testing.T) {
	root := legacySourceDir(t)
	handler := newHandler(root, "", allow)

	request(t, newHandler(root, "", nil), http.MethodGet, testPrefix+PreflightSuffix, http.StatusUnauthorized)
	denied := newHandler(root, "", func(*http.Request) int { return http.StatusUnauthorized })
	request(t, denied, http.MethodGet, testPrefix+PreflightSuffix, http.StatusUnauthorized)
	request(t, handler, http.MethodPost, testPrefix+PreflightSuffix, http.StatusMethodNotAllowed)
	for _, path := range []string{
		testPrefix + PreflightSuffix + "/",
		testPrefix + "/installation/upgrade/preflightx",
		testPrefix + "/installation/upgrade",
		testPrefix + PreflightSuffix + "?legacy=/etc",
	} {
		request(t, handler, http.MethodGet, path, http.StatusNotFound)
	}

	for _, prefix := range []string{testPrefix, canonical} {
		_, body := request(t, handler, http.MethodGet, prefix+PreflightSuffix, http.StatusOK)
		var report preflightDocument
		if err := json.Unmarshal([]byte(body), &report); err != nil {
			t.Fatalf("decode preflight: %v: %s", err, body)
		}
		if report.ContractVersion != Contract || len(report.Checks) == 0 {
			t.Fatalf("preflight document = %+v", report)
		}
		if report.Ready && len(report.Blocked) != 0 {
			t.Fatalf("a ready report carried blocked codes: %s", body)
		}
		if report.Blocked == nil || report.ParityGaps.AffectedDates == nil {
			t.Fatalf("preflight omitted a list: %s", body)
		}
		for _, secret := range []string{root, "legacy-data-key-material"} {
			if strings.Contains(body, secret) {
				t.Fatalf("preflight leaked %q: %s", secret, body)
			}
		}
		if report.Fingerprint != "" && !strings.HasSuffix(report.Fingerprint, "…") && len(report.Fingerprint) > 12 {
			t.Fatalf("fingerprint is not abbreviated: %q", report.Fingerprint)
		}
	}
}

// TestPreflightMissingLegacyRootIsTyped proves a missing environment is a typed
// code the wizard can name, never a 500 and never a fabricated ready report.
func TestPreflightMissingLegacyRootIsTyped(t *testing.T) {
	cases := []struct {
		name, root, code string
	}{
		{name: "unconfigured", root: "", code: CodeLegacyRootMissing},
		{name: "absent", root: filepath.Join(t.TempDir(), "absent"), code: CodeLegacyRootMissing},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, body := request(t, newHandler(testCase.root, "", allow), http.MethodGet, testPrefix+PreflightSuffix, http.StatusServiceUnavailable)
			if !strings.Contains(body, `"`+testCase.code+`"`) {
				t.Fatalf("code %s missing: %s", testCase.code, body)
			}
		})
	}
	file := filepath.Join(t.TempDir(), "legacy.sqlite")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body := request(t, newHandler(file, "", allow), http.MethodGet, testPrefix+PreflightSuffix, http.StatusServiceUnavailable)
	if !strings.Contains(body, `"`+CodeLegacyRootUnusable+`"`) {
		t.Fatalf("unusable root code missing: %s", body)
	}
}

func recordOperation(t *testing.T, database, stage string, domains []string, reconciliation string) {
	t.Helper()
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	operation := model.UpgradeOperation{
		Stage:                   model.UpgradeStage(stage),
		DomainsDone:             domains,
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "1.14.4",
		SourceFingerprintBefore: "fingerprint-before-secret-value",
		SourceFingerprintAfter:  "fingerprint-after-secret-value",
		BackupManifestSHA256:    "backup-manifest-secret-value",
		ManualItems:             []string{"reimport_cpa_keys"},
		ErrorCode:               "upgrade_reconcile_blocked",
		ErrorDetail:             "retained projection missing for 2026-10-01",
	}
	if reconciliation != "" {
		operation.Reconciliation = json.RawMessage(reconciliation)
	}
	if err := upgradeoperation.New(db).Put(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
}

// TestStatusProjectionOmitsSecretsAndToleratesUnknownStages is the read
// projection contract: stages map onto the wizard's next action, a stage this
// build does not know is still described, and no fingerprint, digest, path or
// credential reaches the response.
func TestStatusProjectionOmitsSecretsAndToleratesUnknownStages(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, string(model.UpgradeStageReconcile),
		[]string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
		`{"mismatchDays":["2026-10-02","2026-10-01","2026-10-01","not-a-day"],"raw":{"secret":"x"}}`)
	handler := newHandler("", database, allow)

	_, body := request(t, handler, http.MethodGet, canonical+StatusSuffix, http.StatusOK)
	for _, secret := range []string{
		"fingerprint-before-secret-value", "fingerprint-after-secret-value",
		"backup-manifest-secret-value", database, `"raw"`, "not-a-day",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("status leaked %q: %s", secret, body)
		}
	}
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Recorded || !status.StageKnown || status.Stage != string(model.UpgradeStageReconcile) {
		t.Fatalf("status = %+v", status)
	}
	if status.NextAction != ActionReconcile {
		t.Fatalf("next action = %q", status.NextAction)
	}
	if len(status.DomainsDone) != 2 || status.DomainsDone[0] != model.UpgradeDomainSourceCopy {
		t.Fatalf("domains = %v", status.DomainsDone)
	}
	if got := string(status.Reconciliation); got != `{"mismatchDays":["2026-10-01","2026-10-02"]}` {
		t.Fatalf("reconciliation = %s", got)
	}
	if status.SourceKind != "upgraded-legacy" || status.SourceVersion != "1.14.4" {
		t.Fatalf("source = %+v", status)
	}
	if len(status.ManualItems) != 1 || status.ErrorCode != "upgrade_reconcile_blocked" {
		t.Fatalf("operation detail = %+v", status)
	}
}

// TestStatusStageToNextAction is the stage table the wizard renders against.
func TestStatusStageToNextAction(t *testing.T) {
	cases := []struct {
		stage      string
		action     string
		stageKnown bool
	}{
		{stage: "", action: ActionPreflight, stageKnown: true},
		{stage: StagePreflight, action: ActionPreflight, stageKnown: true},
		{stage: StageCopyIn, action: ActionCopyIn, stageKnown: true},
		{stage: StageReconcile, action: ActionReconcile, stageKnown: true},
		{stage: StageCPAImport, action: ActionReconcile, stageKnown: true},
		{stage: StageExternal, action: ActionReconcile, stageKnown: true},
		{stage: StageVerify, action: ActionReconcile, stageKnown: true},
		{stage: StageFailed, action: ActionReconcile, stageKnown: true},
		{stage: StageComplete, action: ActionComplete, stageKnown: true},
		{stage: StageRolledBack, action: ActionRolledBack, stageKnown: true},
		{stage: "quantum", action: "", stageKnown: false},
	}
	for _, testCase := range cases {
		if got := nextAction(testCase.stage); got != testCase.action {
			t.Fatalf("nextAction(%q) = %q, want %q", testCase.stage, got, testCase.action)
		}
		if got := knownStage(testCase.stage); got != testCase.stageKnown {
			t.Fatalf("knownStage(%q) = %t, want %t", testCase.stage, got, testCase.stageKnown)
		}
	}
}

// TestStatusUnrecordedAndUnreadable proves the pre-operation state is a normal
// empty projection, and that an unreadable store is a typed code.
func TestStatusUnrecordedAndUnreadable(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, body := request(t, newHandler("", database, allow), http.MethodGet, testPrefix+StatusSuffix, http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if status.Recorded || status.Stage != "" || status.NextAction != ActionPreflight {
		t.Fatalf("unrecorded projection = %+v", status)
	}
	if status.DomainsDone == nil || status.ManualItems == nil {
		t.Fatalf("unrecorded projection omitted a list: %s", body)
	}
	request(t, newHandler("", "", allow), http.MethodGet, testPrefix+StatusSuffix, http.StatusServiceUnavailable)
	broken := filepath.Join(t.TempDir(), "usage.sqlite")
	if err := os.WriteFile(broken, []byte("not sqlite at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body = request(t, newHandler("", broken, allow), http.MethodGet, testPrefix+StatusSuffix, http.StatusServiceUnavailable)
	if !strings.Contains(body, `"`+CodeOperationUnread+`"`) {
		t.Fatalf("unreadable store code missing: %s", body)
	}
}

// TestStatusRolledBackIsAnObservation proves a stage written by a build that
// knows rollback is readable here even though this build's model validator
// would reject it.
func TestStatusRolledBackIsAnObservation(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into upgrade_operations (id, stage, created_at_ms, updated_at_ms) values (1, 'rolled_back', 1, 2)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, body := request(t, newHandler("", database, allow), http.MethodGet, canonical+StatusSuffix, http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Recorded || status.Stage != StageRolledBack || !status.StageKnown || status.NextAction != ActionRolledBack {
		t.Fatalf("rolled_back projection = %+v", status)
	}
}

// TestScrubbersKeepHostPathsOut proves the two text scrubbing rules used on
// operator-visible strings.
func TestScrubbersKeepHostPathsOut(t *testing.T) {
	scrub := newScrubber("/srv/cpamp/legacy")
	if got := scrub.detail("source /srv/cpamp/legacy/usage.sqlite is busy"); got != "source <source>/usage.sqlite is busy" {
		t.Fatalf("detail = %q", got)
	}
	if got := scrubError("open /srv/cpamp/data-key: permission denied"); strings.Contains(got, "/srv") {
		t.Fatalf("error detail leaked a path: %q", got)
	}
	if got := scrubError("retained projection missing for 2026-10-01"); got != "retained projection missing for 2026-10-01" {
		t.Fatalf("error detail = %q", got)
	}
	if got := abbreviate("0123456789abcdef"); got != "0123456789ab…" {
		t.Fatalf("abbreviate = %q", got)
	}
}
