package upgradewizard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// actionVerification is a whole verification observation: every field the verdict
// rests on. It is the value the completion gate writes beside the stage, on the
// source fingerprint the fixture row carries.
func actionVerification(domains ...string) model.UpgradeVerification {
	if len(domains) == 0 {
		domains = []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig, model.UpgradeDomainIdentity}
	}
	return model.UpgradeVerification{
		TargetMode:          model.UpgradeTargetExternal,
		SourceFingerprint:   "fingerprint-after-secret-value",
		Domains:             domains,
		BindingIdentity:     "https://cpa.example",
		BindingGeneration:   "https://cpa.example",
		BindingObservedAtMS: 1_700_000_000_000,
		ObservedAtMS:        1_700_000_000_001,
	}
}

// recordCompletedOperation leaves the row a finished operation holds: the stage
// and the verification, written by the gate in one statement, beside the External
// binding that verification names.
func recordCompletedOperation(t *testing.T, database string, domains ...string) {
	t.Helper()
	ctx := context.Background()
	db, err := managersqlite.Open(database)
	if err != nil {
		t.Fatalf("open the fixture store: %v", err)
	}
	defer func() { _ = db.Close() }()
	repository := upgradeoperation.New(db)
	if _, _, err := repository.Get(ctx); err != nil {
		t.Fatalf("read the fixture operation: %v", err)
	}
	if _, err := repository.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageVerify); err != nil {
		t.Fatalf("record the fixture position: %v", err)
	}
	for _, domain := range []string{model.UpgradeDomainIdentity, model.UpgradeDomainCPAConfig} {
		if _, err := repository.Advance(ctx, domain, model.UpgradeStageVerify); err != nil {
			t.Fatalf("record the fixture domain %q: %v", domain, err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the fixture completion: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	verification := actionVerification(domains...)
	if err := upgradeoperation.RecordExternalBindingTx(ctx, tx, upgradeoperation.ExternalBinding{
		BaseURL:      "https://cpa.example",
		Identity:     verification.BindingIdentity,
		Generation:   verification.BindingGeneration,
		ObservedAtMS: verification.BindingObservedAtMS,
	}); err != nil {
		t.Fatalf("record the fixture binding: %v", err)
	}
	if _, err := upgradeoperation.RecordCompletionTx(ctx, tx, verification); err != nil {
		t.Fatalf("record the fixture verdict: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit the fixture verdict: %v", err)
	}
}

// actionRequest sends one request to the wizard with the browser provenance the
// surface checks. It returns the recorder so a test can assert on the headers too.
func actionRequest(t *testing.T, handler http.Handler, method, path, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Host = "manager.test"
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

// TestVerifyActionRefusesEveryVerbExceptPost keeps the reads pure: the action is
// POST only, so a prefetch, a link or a bookmark of its address records nothing.
func TestVerifyActionRefusesEveryVerbExceptPost(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{Stage: model.UpgradeStageCopyIn})
	handler := newHandler(legacySourceDir(t), database)

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		w := actionRequest(t, handler, method, testPrefix+VerifySuffix, bearer(testAdminKey))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d, want 405: %s", method, VerifySuffix, w.Code, w.Body.String())
		}
		if allow := w.Header().Get("Allow"); allow != http.MethodPost {
			t.Fatalf("%s %s Allow = %q, want POST", method, VerifySuffix, allow)
		}
		// The verb verdict never advanced anything.
		if strings.Contains(w.Body.String(), `"complete":true`) {
			t.Fatalf("%s answered with a verdict: %s", method, w.Body.String())
		}
	}
	// The two reads refuse the action's verb as well.
	for _, suffix := range []string{StatusSuffix, PreflightSuffix} {
		w := actionRequest(t, handler, http.MethodPost, testPrefix+suffix, bearer(testAdminKey))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want 405", suffix, w.Code)
		}
	}
}

// TestVerifyActionAuthenticatesBeforeItAnswersAnything is the order contract: an
// unknown route inside the surface, an unknown method and the action itself all
// answer the credential first, so a caller without one cannot map the surface.
func TestVerifyActionAuthenticatesBeforeItAnswersAnything(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{Stage: model.UpgradeStageCopyIn})
	handler := newHandler(legacySourceDir(t), database)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodPost, testPrefix + VerifySuffix},
		{http.MethodGet, testPrefix + VerifySuffix},
		{http.MethodPost, testPrefix + "/installation/upgrade/unknown"},
		{http.MethodGet, testPrefix + "/installation/upgrade/unknown"},
	}
	for _, probe := range cases {
		w := actionRequest(t, handler, probe.method, probe.path, "")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without a credential = %d, want 401", probe.method, probe.path, w.Code)
		}
		if !strings.Contains(w.Body.String(), `"`+CodeAdminUnauthorized+`"`) {
			t.Fatalf("%s %s did not name the refusal: %s", probe.method, probe.path, w.Body.String())
		}
		// A session-looking header is not a credential either.
		w = actionRequest(t, handler, probe.method, probe.path, "Fresh installation-session-token")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s with a Fresh session = %d, want 401", probe.method, probe.path, w.Code)
		}
		w = actionRequest(t, handler, probe.method, probe.path, bearer(foreignKey))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s with a foreign key = %d, want 401", probe.method, probe.path, w.Code)
		}
	}
	// With the credential, an unknown route is a 404 and the action is not.
	if w := actionRequest(t, handler, http.MethodPost, testPrefix+"/installation/upgrade/unknown", bearer(testAdminKey)); w.Code != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// TestVerifyActionRefusesCrossSiteBrowsers keeps the action under the surface's
// one origin rule: no cookie, no cross-site provenance.
func TestVerifyActionRefusesCrossSiteBrowsers(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{Stage: model.UpgradeStageCopyIn})
	handler := newHandler(legacySourceDir(t), database)
	handler.PublicOrigin = "https://manager.test"

	send := func(origin, site string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, testPrefix+VerifySuffix, nil)
		r.Host = "manager.test"
		r.Header.Set("Authorization", bearer(testAdminKey))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, rejected := range [][2]string{
		{"https://attacker.test", ""},
		{"http://manager.test", ""},
		{"https://manager.test", "cross-site"},
		{"https://manager.test", "same-site"},
	} {
		w := send(rejected[0], rejected[1])
		if w.Code != http.StatusForbidden {
			t.Fatalf("origin=%q site=%q status = %d, want 403: %s", rejected[0], rejected[1], w.Code, w.Body.String())
		}
	}
	// A cookie alone is no authority: the surface never accepts one.
	r := httptest.NewRequest(http.MethodPost, testPrefix+VerifySuffix, nil)
	r.Host = "manager.test"
	r.Header.Set("Cookie", "cmp_admin="+testAdminKey)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cookie-only action status = %d, want 401: %s", w.Code, w.Body.String())
	}
}

// TestVerifyActionRefusesACallerSuppliedInput keeps the verdict out of the
// request: the action takes no body, so no caller can name the source, the target
// or the authority.
func TestVerifyActionRefusesACallerSuppliedInput(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{Stage: model.UpgradeStageCopyIn})
	handler := newHandler(legacySourceDir(t), database)

	body := strings.NewReader(`{"legacyRoot":"/etc","targetRoot":"/","stage":"complete"}`)
	r := httptest.NewRequest(http.MethodPost, testPrefix+VerifySuffix, body)
	r.Host = "manager.test"
	r.Header.Set("Authorization", bearer(testAdminKey))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"`+CodeRequestInvalid+`"`) {
		t.Fatalf("action with a body = %d %s, want 400 %s", w.Code, w.Body.String(), CodeRequestInvalid)
	}
}

// TestVerifyActionReportsTheRecordedVerdict proves the action reports what the
// gate recorded and repeats it without writing: a completed operation keeps its
// revision and its timestamp, and the evidence it rests on never reaches the
// browser.
func TestVerifyActionReportsTheRecordedVerdict(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{Stage: model.UpgradeStageCopyIn})
	recordCompletedOperation(t, database, model.UpgradeDomainSourceCopy, model.UpgradeDomainIdentity, model.UpgradeDomainCPAConfig)
	before := readRecordedOperation(t, database)

	w := actionRequest(t, handlerFor(t, database), http.MethodPost, testPrefix+VerifySuffix, bearer(testAdminKey))
	if w.Code != http.StatusOK {
		t.Fatalf("action status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var document verifyDocument
	if err := json.Unmarshal(w.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode the action document: %v; body: %s", err, w.Body.String())
	}
	if !document.Recorded || !document.Complete || document.Stage != StageComplete {
		t.Fatalf("document = %+v, want a recorded completion", document)
	}
	if !document.AlreadyVerified {
		t.Fatalf("document = %+v, want the recorded verdict reported as already recorded", document)
	}
	if document.NextAction != ActionComplete {
		t.Fatalf("next action = %q, want %q", document.NextAction, ActionComplete)
	}
	if document.TargetMode != model.UpgradeTargetExternal {
		t.Fatalf("target mode = %q, want external", document.TargetMode)
	}
	// No observation of the record reaches the browser.
	for _, forbidden := range []string{"sha256:", before.SourceFingerprintAfter, "https://cpa.example", before.SourceRoot} {
		if forbidden != "" && strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("the action leaked %q: %s", forbidden, w.Body.String())
		}
	}
	// The repeat wrote nothing.
	after := readRecordedOperation(t, database)
	if after.Revision != before.Revision || after.UpdatedAtMS != before.UpdatedAtMS {
		t.Fatalf("the action wrote a completed row: %d/%d -> %d/%d",
			before.Revision, before.UpdatedAtMS, after.Revision, after.UpdatedAtMS)
	}
}

// TestVerifyActionReportsATypedRefusal keeps the gate's refusals typed on the
// wire and free of the record: the page reads a code, never a path or a digest.
func TestVerifyActionReportsATypedRefusal(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{
		Stage:       model.UpgradeStageCPAImport,
		DomainsDone: []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainPricing},
	})
	before := readRecordedOperation(t, database)

	w := actionRequest(t, handlerFor(t, database), http.MethodPost, testPrefix+VerifySuffix, bearer(testAdminKey))
	if w.Code != http.StatusConflict {
		t.Fatalf("action status = %d, want 409: %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode the refusal: %v; body: %s", err, w.Body.String())
	}
	if envelope.Error.Code != string(upgradeservice.UpgradeErrorVerifyConnectionUnproven) {
		t.Fatalf("refusal code = %q, want the connection-unproven code", envelope.Error.Code)
	}
	for _, forbidden := range []string{filepath.Dir(database), "sha256:", "management"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("the refusal leaked %q: %s", forbidden, w.Body.String())
		}
	}
	after := readRecordedOperation(t, database)
	if after.Stage != before.Stage || after.Revision != before.Revision || after.UpdatedAtMS != before.UpdatedAtMS {
		t.Fatalf("a refused action wrote the row: %+v -> %+v", before, after)
	}
}

// TestVerifyActionHoldsTheStoreFence refuses a store another writer owns instead
// of sharing it with a verdict: the action answers its own typed code, and the
// row is untouched.
func TestVerifyActionHoldsTheStoreFence(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{
		Stage:       model.UpgradeStageCPAImport,
		DomainsDone: []string{model.UpgradeDomainSourceCopy},
	})
	before := readRecordedOperation(t, database)
	held, err := processlock.Acquire(database)
	if err != nil {
		t.Fatalf("take the v2 data-root fence: %v", err)
	}
	defer func() { _ = held.Close() }()

	w := actionRequest(t, handlerFor(t, database), http.MethodPost, testPrefix+VerifySuffix, bearer(testAdminKey))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("action status = %d, want 503: %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode the refusal: %v; body: %s", err, w.Body.String())
	}
	if envelope.Error.Code != string(upgradeservice.UpgradeErrorVerifyV2Running) {
		t.Fatalf("refusal code = %q, want the held-fence code", envelope.Error.Code)
	}
	after := readRecordedOperation(t, database)
	if after.Stage != before.Stage || after.Revision != before.Revision || after.UpdatedAtMS != before.UpdatedAtMS {
		t.Fatalf("a refused action wrote the row: %+v -> %+v", before, after)
	}
}

// TestReadsStayPureReads proves GET and HEAD never write: the recorded position,
// its revision and its timestamp are unchanged by a read.
func TestReadsStayPureReads(t *testing.T) {
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{
		Stage:       model.UpgradeStageCPAImport,
		DomainsDone: []string{model.UpgradeDomainSourceCopy},
	})
	before := readRecordedOperation(t, database)
	handler := handlerFor(t, database)
	actionRequest(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey))
	actionRequest(t, handler, http.MethodHead, testPrefix+StatusSuffix, bearer(testAdminKey))
	actionRequest(t, handler, http.MethodGet, testPrefix+PreflightSuffix, bearer(testAdminKey))
	after := readRecordedOperation(t, database)
	if after.Revision != before.Revision || after.UpdatedAtMS != before.UpdatedAtMS || after.Stage != before.Stage {
		t.Fatalf("a read wrote the operation: %+v -> %+v", before, after)
	}
}

// TestVerifiedRowOffersTheGateAgain pins the wizard's mapping of a recorded
// verify position: the Managed waiting row is a step the operator may run again,
// never a completed operation.
func TestVerifiedRowOffersTheGateAgain(t *testing.T) {
	waiting := string(upgradeservice.UpgradeErrorVerifyWaitingForRepin)
	if got := nextActionForRecordedStage(StageVerify, waiting, nil, nil); got != ActionVerify {
		t.Fatalf("next action for a waiting row = %q, want %q", got, ActionVerify)
	}
	if got := nextActionForRecordedStage(StageVerify, "", nil, nil); got != ActionComplete {
		t.Fatalf("next action for a verified row without a reason = %q, want %q", got, ActionComplete)
	}
	// The result projection carries the same decision through the route.
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, database, model.UpgradeOperation{
		Stage:       model.UpgradeStageVerify,
		DomainsDone: []string{model.UpgradeDomainSourceCopy},
		ErrorCode:   waiting,
	})
	_, body := request(t, handlerFor(t, database), http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatal(err)
	}
	if status.NextAction != ActionVerify {
		t.Fatalf("status next action = %q, want %q", status.NextAction, ActionVerify)
	}
	if status.ErrorCode != waiting {
		t.Fatalf("status reason = %q, want %q", status.ErrorCode, waiting)
	}
}

// handlerFor builds the wizard over a real legacy mount and a Manager store.
func handlerFor(t *testing.T, database string) *Handler {
	t.Helper()
	return newHandler(legacySourceDir(t), database)
}

// readRecordedOperation reads the row the store holds, so a test can prove a
// route wrote nothing.
func readRecordedOperation(t *testing.T, database string) model.UpgradeOperation {
	t.Helper()
	db, err := managersqlite.Open(database)
	if err != nil {
		t.Fatalf("open the fixture store: %v", err)
	}
	defer func() { _ = db.Close() }()
	operation, found, err := upgradeoperation.New(db).Get(context.Background())
	if err != nil || !found {
		t.Fatalf("read the fixture operation: found=%t err=%v", found, err)
	}
	return operation
}
