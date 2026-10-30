package upgradewizard

import (
	"context"
	"database/sql"
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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

const (
	testPrefix = "/.well-known/cpamp-bootstrap/api/v2"
	canonical  = "/cpamp-preview-abc123/api/v2"
	// testAdminKey is the source administrator credential the bootstrap Upgrade
	// entry verifies; the wizard routes must accept exactly this key.
	testAdminKey = "legacy-manager-admin-credential"
	// foreignKey is a well-formed credential that is not the source's.
	foreignKey = "another-manager-admin-credential"
)

func bearer(key string) string { return "Bearer " + key }

func request(t *testing.T, handler http.Handler, method, path, authorization string, want int) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Host = "manager.test"
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w.Code, w.Body.String()
}

// legacySourceDir writes the smallest 1.x data directory the read-only preflight
// will inspect, carrying the administrator credential the Upgrade entry verifies.
// The report may be blocked (a synthetic source has no pinned version) — the
// route contract is a 200 report either way.
func legacySourceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.key"), []byte("legacy-data-key-material"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential, err := security.NewAdminCredential(testAdminKey, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`pragma journal_mode = delete;
create table settings (key text primary key, value text not null);
create table usage_events (id integer primary key autoincrement, event_hash text not null unique, timestamp_ms integer not null);`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into settings (key, value) values ('admin_credential_v1', ?)`, string(raw)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage.sqlite.manager.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newHandler(legacyRoot, database string) Handler {
	return Handler{
		LegacyRoot: func() string { return legacyRoot },
		Database:   func() string { return database },
		Timeout:    10 * time.Second,
	}
}

// TestRoutesRefuseCallersWithoutTheSourceCredential is the negative identity
// contract: mounting the handler authenticates every request, so no absence of a
// credential — and no other scheme — can read as allow.
func TestRoutesRefuseCallersWithoutTheSourceCredential(t *testing.T) {
	handler := newHandler(legacySourceDir(t), "")
	for name, authorization := range map[string]string{
		"absent":           "",
		"fresh-session":    "Fresh installation-session-token",
		"other-scheme":     "Basic b3BlcmF0b3I6cGFzcw==",
		"no-value":         "Bearer ",
		"padded":           bearer(" " + testAdminKey),
		"foreign-key":      bearer(foreignKey),
		"bare-key":         testAdminKey,
		"key-as-scheme":    bearer(testAdminKey) + " extra",
		"empty-key-bearer": "Bearer",
	} {
		t.Run(name, func(t *testing.T) {
			for _, prefix := range []string{testPrefix, canonical} {
				for _, resource := range []string{PreflightSuffix, StatusSuffix} {
					code, body := request(t, handler, http.MethodGet, prefix+resource, authorization, http.StatusUnauthorized)
					if code != http.StatusUnauthorized || !strings.Contains(body, `"`+CodeAdminUnauthorized+`"`) {
						t.Fatalf("%s%s answered %d: %s", prefix, resource, code, body)
					}
				}
			}
		})
	}
	// Two Authorization values are two credentials; neither is a decision.
	r := httptest.NewRequest(http.MethodGet, testPrefix+PreflightSuffix, nil)
	r.Header.Add("Authorization", bearer(testAdminKey))
	r.Header.Add("Authorization", bearer(testAdminKey))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate Authorization = %d", w.Code)
	}
	// The credential the entry page verified is accepted on both routes.
	request(t, handler, http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusOK)
}

// TestPreflightServesAReport covers the read contract on both prefixes: the
// method and path gates, and a report with every check and no source detail.
func TestPreflightServesAReport(t *testing.T) {
	root := legacySourceDir(t)
	handler := newHandler(root, "")

	request(t, handler, http.MethodPost, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusMethodNotAllowed)
	for _, path := range []string{
		testPrefix + PreflightSuffix + "/",
		testPrefix + "/installation/upgrade/preflightx",
		testPrefix + "/installation/upgrade",
		testPrefix + PreflightSuffix + "?legacy=/etc",
	} {
		request(t, handler, http.MethodGet, path, bearer(testAdminKey), http.StatusNotFound)
	}

	for _, prefix := range []string{testPrefix, canonical} {
		_, body := request(t, handler, http.MethodGet, prefix+PreflightSuffix, bearer(testAdminKey), http.StatusOK)
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
		for _, secret := range []string{root, "legacy-data-key-material", testAdminKey} {
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
// code the wizard can name, never a 500 and never a fabricated ready report. The
// caller still has to present a credential first.
func TestPreflightMissingLegacyRootIsTyped(t *testing.T) {
	cases := []struct {
		name, root, code string
	}{
		{name: "unconfigured", root: "", code: CodeLegacyRootMissing},
		{name: "absent", root: filepath.Join(t.TempDir(), "absent"), code: CodeLegacyRootMissing},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, body := request(t, newHandler(testCase.root, ""), http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
			if !strings.Contains(body, `"`+testCase.code+`"`) {
				t.Fatalf("code %s missing: %s", testCase.code, body)
			}
		})
	}
	file := filepath.Join(t.TempDir(), "legacy.sqlite")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body := request(t, newHandler(file, ""), http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
	if !strings.Contains(body, `"`+CodeLegacyRootUnusable+`"`) {
		t.Fatalf("unusable root code missing: %s", body)
	}
	// A source without the recorded credential proves nothing: a well-formed key
	// is not accepted because the root exists.
	_, body = request(t, newHandler(t.TempDir(), ""), http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
	if !strings.Contains(body, `"`+CodeSourceLockUnavailable+`"`) {
		t.Fatalf("unreadable source code missing: %s", body)
	}
}

func recordOperation(t *testing.T, database string, operation model.UpgradeOperation) {
	t.Helper()
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if operation.SourceKind == "" {
		operation.SourceKind = "upgraded-legacy"
	}
	if operation.SourceVersion == "" {
		operation.SourceVersion = "1.14.4"
	}
	operation.SourceFingerprintBefore = "fingerprint-before-secret-value"
	operation.SourceFingerprintAfter = "fingerprint-after-secret-value"
	operation.BackupManifestSHA256 = "backup-manifest-secret-value"
	if err := upgradeoperation.New(db).Put(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
}

// TestStatusProjectionOmitsSecretsAndToleratesUnknownStages is the read
// projection contract: stages map onto the wizard's next action, a stage this
// build does not know is still described, and no fingerprint, digest, path or
// credential reaches the response.
func TestStatusProjectionOmitsSecretsAndToleratesUnknownStages(t *testing.T) {
	root := legacySourceDir(t)
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{
		Stage:          model.UpgradeStageReconcile,
		DomainsDone:    []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
		Reconciliation: json.RawMessage(`{"mismatchDays":["2026-10-02","2026-10-01","2026-10-01","not-a-day"],"raw":{"secret":"x"}}`),
		ManualItems:    []string{"reimport_cpa_keys"},
		ErrorCode:      "upgrade_reconcile_blocked",
		ErrorDetail:    "retained projection missing for 2026-10-01",
	})
	handler := newHandler(root, database)

	_, body := request(t, handler, http.MethodGet, canonical+StatusSuffix, bearer(testAdminKey), http.StatusOK)
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
	if len(status.ManualItems) != 1 || status.ManualItems[0] != "reimport_cpa_keys" || status.ErrorCode != "upgrade_reconcile_blocked" {
		t.Fatalf("operation detail = %+v", status)
	}
}

// TestStatusScrubsRecordedTextByContent proves the scrubber reads the text, not
// its shape: a credential carries no path, so a path-shaped test cannot see it,
// and a multi-line detail is evidence that must survive line by line.
func TestStatusScrubsRecordedTextByContent(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	const (
		bearerSecret = "eyJhbGciOiJIUzI1NiJ9.bGVnYWN5.c2lnbmF0dXJl"
		issuedSecret = "sk-live-424242424242"
		apiKeySecret = "cpaKeyFromHeader"
		urlSecret    = "hunter2password"
	)
	recordOperation(t, database, model.UpgradeOperation{
		Stage:       model.UpgradeStageReconcile,
		DomainsDone: []string{model.UpgradeDomainSourceCopy},
		ManualItems: []string{
			"reimport_cpa_keys",
			"call the gateway with Bearer " + bearerSecret,
			"export X_API_KEY=" + apiKeySecret,
		},
		ErrorCode: "upgrade_reconcile_blocked",
		ErrorDetail: "first line keeps its words\n" +
			"upstream answered 401 for Authorization: Bearer " + bearerSecret + "\n" +
			"retry with sk-live-424242424242 instead\n" +
			"rejected x-api-key: " + apiKeySecret + "\n" +
			"endpoint https://operator:" + urlSecret + "@cpa.internal/v0/management answered\n" +
			"leftover /srv/cpamp/data/usage.sqlite is unreadable",
	})
	handler := newHandler(legacySourceDir(t), database)
	_, body := request(t, handler, http.MethodGet, canonical+StatusSuffix, bearer(testAdminKey), http.StatusOK)

	for _, secret := range []string{bearerSecret, issuedSecret, apiKeySecret, urlSecret, "/srv/cpamp"} {
		if strings.Contains(body, secret) {
			t.Fatalf("status leaked %q: %s", secret, body)
		}
	}
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	// The detail is scrubbed line by line instead of being dropped whole.
	for _, fragment := range []string{"first line keeps its words", "upstream answered 401", "retry with", "rejected x-api-key", "endpoint https://", "@cpa.internal/v0/management answered", "leftover"} {
		if !strings.Contains(status.ErrorDetail, fragment) {
			t.Fatalf("detail lost %q: %q", fragment, status.ErrorDetail)
		}
	}
	if lines := strings.Split(status.ErrorDetail, "\n"); len(lines) != 6 {
		t.Fatalf("detail lines = %d, want 6: %q", len(lines), status.ErrorDetail)
	}
	if !strings.Contains(status.ErrorDetail, redactedText) || !strings.Contains(status.ErrorDetail, sourceText) {
		t.Fatalf("detail carries no redaction marker: %q", status.ErrorDetail)
	}
	// The same scrubber applies to the operation's manual items.
	if len(status.ManualItems) != 3 || status.ManualItems[0] != "reimport_cpa_keys" {
		t.Fatalf("manual items = %v", status.ManualItems)
	}
	for _, item := range status.ManualItems {
		for _, secret := range []string{bearerSecret, apiKeySecret} {
			if strings.Contains(item, secret) {
				t.Fatalf("manual item leaked %q: %q", secret, item)
			}
		}
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
	root := legacySourceDir(t)
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, body := request(t, newHandler(root, database), http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
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
	request(t, newHandler(root, ""), http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
	broken := filepath.Join(t.TempDir(), "usage.sqlite")
	if err := os.WriteFile(broken, []byte("not sqlite at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body = request(t, newHandler(root, broken), http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusServiceUnavailable)
	if !strings.Contains(body, `"`+CodeOperationUnread+`"`) {
		t.Fatalf("unreadable store code missing: %s", body)
	}
}

// TestStatusRolledBackIsAnObservation proves a stage written by a build that
// knows rollback is readable here even though this build's model validator
// would reject it.
func TestStatusRolledBackIsAnObservation(t *testing.T) {
	root := legacySourceDir(t)
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
	_, body := request(t, newHandler(root, database), http.MethodGet, canonical+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Recorded || status.Stage != StageRolledBack || !status.StageKnown || status.NextAction != ActionRolledBack {
		t.Fatalf("rolled_back projection = %+v", status)
	}
}

// TestScrubbersKeepHostPathsOut proves the text scrubbing rules used on
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
	// A credential is redacted by its own content, with no path in sight.
	for _, testCase := range []struct{ in, secret string }{
		{in: "request failed with Authorization: Bearer abc.def.ghi", secret: "abc.def.ghi"},
		{in: "upstream key sk-live-0123456789 expired", secret: "sk-live-0123456789"},
		{in: "header x-api-key: cpaManagedSecret rejected", secret: "cpaManagedSecret"},
		{in: "header X-Api-Key=cpaManagedSecret rejected", secret: "cpaManagedSecret"},
		{in: "url https://admin:pw-12345@gateway.internal/x refused", secret: "pw-12345"},
	} {
		got := scrubError(testCase.in)
		if strings.Contains(got, testCase.secret) {
			t.Fatalf("%q leaked %q", testCase.in, testCase.secret)
		}
		if !strings.Contains(got, redactedText) {
			t.Fatalf("%q has no redaction marker: %q", testCase.in, got)
		}
		if strings.Contains(got, "\n") {
			t.Fatalf("%q gained a line break: %q", testCase.in, got)
		}
	}
	// A remote URL is not a host path: only its credentials are redacted.
	if got := scrubError("gateway https://gateway.internal/v0/management refused"); got != "gateway https://gateway.internal/v0/management refused" {
		t.Fatalf("remote URL was rewritten: %q", got)
	}
	// Multi-line detail survives line by line.
	if got := scrubError("first\nsecond Bearer leaked-token\nthird"); got != "first\nsecond Bearer "+redactedText+"\nthird" {
		t.Fatalf("multi-line detail = %q", got)
	}
	// A NUL byte is not text and never reaches the operator.
	if got := scrubError("broken\x00value"); strings.Contains(got, "\x00") {
		t.Fatalf("NUL survived: %q", got)
	}
	if got := abbreviate("0123456789abcdef"); got != "0123456789ab…" {
		t.Fatalf("abbreviate = %q", got)
	}
}
