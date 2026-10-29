package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

// rebindCall posts one rebind request through the canonical API.
func rebindCall(t *testing.T, app *freshApplication, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/control/api/v2/external/rebind", bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.canonicalAPI(response, request)
	return response
}

// rebindBody renders one well-formed rebind request.
func rebindBody(baseURL, key string) string {
	body := `{"requestId":"` + strings.Repeat("a", 32) + `","managementKey":"` + key + `"`
	if baseURL != "" {
		body += `,"baseUrl":"` + baseURL + `"`
	}
	return body + `}`
}

// TestExternalRebindRotatesTheCommittedCredential proves the admin route replaces
// the credential of the address this installation already holds: the candidate is
// proven first, the stored value stays a protected envelope, the record revision
// moves so a concurrent write conflicts, and the installation identity — the
// address — is unchanged, so the key-usage history stays attributable to the same
// CPA. The Connect External target generation is pinned to the installation and
// does not move for a credential rotation.
func TestExternalRebindRotatesTheCommittedCredential(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "")
	before, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// The user rotated the management credential on their own CPA.
	rotated := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	cpaServer.management = rotated
	// The stored credential is now refused by the CPA, which is the blocker the
	// rebind exists to resolve: resolving the authority never contacts the CPA, so
	// the refusal itself is proven by the credential the reply does not carry.
	cpaServer.reject.Store(false)

	response := rebindCall(t, app, rebindBody("", rotated))
	if response.Code != http.StatusOK {
		t.Fatalf("rebind = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), rotated) {
		t.Fatal("the rebind reply echoed the credential")
	}
	after, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Connection == nil || after.Connection.CPABaseURL != before.Connection.CPABaseURL {
		t.Fatalf("the address changed: %+v", after.Connection)
	}
	if after.Operation.Strategy.TargetIdentity != before.Operation.Strategy.TargetIdentity {
		t.Fatalf("the identity changed: %q", after.Operation.Strategy.TargetIdentity)
	}
	if after.Connection.ManagementKey == rotated {
		t.Fatal("the credential was stored in plaintext")
	}
	if after.Connection.ManagementKey == before.Connection.ManagementKey {
		t.Fatal("the stored envelope was not replaced")
	}
	// The Connect External checkpoint pins its target generation to the
	// installation, which a credential rotation does not change; the record
	// revision is what moves, and it is the CAS every route holds.
	if after.Operation.Revision != before.Operation.Revision+1 {
		t.Fatalf("revision = %d, want %d", after.Operation.Revision, before.Operation.Revision+1)
	}
	if after.Operation.Strategy.TargetGeneration != before.Operation.Strategy.TargetGeneration {
		t.Fatalf("the installation generation moved to %d", after.Operation.Strategy.TargetGeneration)
	}
	// The rebound installation resolves, and the credential it carries is the
	// replacement: the previous one is gone.
	_, key, err := app.keyApply.ExternalTarget(t.Context(), after.Operation.Strategy.TargetGeneration)
	if err != nil {
		t.Fatalf("the rebound target does not resolve: %v", err)
	}
	if key != rotated {
		t.Fatal("the resolved target still carries the replaced credential")
	}
	// The legacy compatibility row carries the new envelope too, never plaintext.
	setup, found, err := app.runtimeSettings.LoadManagerConfig(t.Context())
	if err != nil || !found {
		t.Fatalf("manager config = found %v, %v", found, err)
	}
	if setup.CPAConnection.ManagementKey != rotated {
		t.Fatal("the compatibility row does not decrypt to the rebound credential")
	}
}

// TestExternalRebindRefusesAnotherAddress proves a rebind re-points a credential
// and never the installation: an address the checkpoint does not name is refused
// before the user's CPA is contacted at all.
func TestExternalRebindRefusesAnotherAddress(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "")
	probes := cpaServer.probes.Load()
	response := rebindCall(t, app, rebindBody("https://other.example", externalAuthorityManagement))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "external_rebind_refused") {
		t.Fatalf("rebind to another address = %d %s", response.Code, response.Body.String())
	}
	if cpaServer.probes.Load() != probes {
		t.Fatal("another address was contacted")
	}
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if record.Operation.Strategy.TargetGeneration != 1 {
		t.Fatalf("a refused rebind moved the generation to %d", record.Operation.Strategy.TargetGeneration)
	}
}

// TestExternalRebindRefusesUnusableAndRejectedCredentials proves neither an
// unusable shape nor a refusal by the user's CPA is persisted: the installation
// keeps the credential it had, and the answer is the typed blocker the page acts
// on.
func TestExternalRebindRefusesUnusableAndRejectedCredentials(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "")
	before, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// A shape CPA itself could never accept is refused before any read.
	probes := cpaServer.probes.Load()
	response := rebindCall(t, app, rebindBody("", " leading-space"))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "external_rebind_refused") {
		t.Fatalf("rebind with an unusable key = %d %s", response.Code, response.Body.String())
	}
	if cpaServer.probes.Load() != probes {
		t.Fatal("an unusable credential was sent to the CPA")
	}
	// The refused credential did reach the CPA, which is what distinguishes this
	// case from the shape refusal above.
	response = rebindCall(t, app, rebindBody("", strings.Repeat("8", 64)))
	if response.Code != http.StatusConflict || cpaServer.probes.Load() == probes {
		t.Fatalf("a well-shaped refusal never reached the CPA: %d %s", response.Code, response.Body.String())
	}
	// A credential the CPA refuses is the typed rejection, and nothing is stored.
	response = rebindCall(t, app, rebindBody("", strings.Repeat("9", 64)))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), freshconfig.ReasonManagementRejected) {
		t.Fatalf("rebind with a rejected key = %d %s", response.Code, response.Body.String())
	}
	after, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Connection.ManagementKey != before.Connection.ManagementKey ||
		after.Operation.Strategy.TargetGeneration != before.Operation.Strategy.TargetGeneration {
		t.Fatal("a refused rebind changed the installation")
	}
}

// TestExternalRebindRefusesAnUnsupportedVersion proves a CPA that does not prove
// the v8 contract cannot be rebound to: the credential would authenticate, but
// this Manager could not write anything with it.
func TestExternalRebindRefusesAnUnsupportedVersion(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "")
	cpaServer.version = "v7.2.130"
	before, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rotated := strings.Repeat("7", 64)
	cpaServer.management = rotated
	response := rebindCall(t, app, rebindBody("", rotated))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), freshconfig.ReasonVersionUnsupported) {
		t.Fatalf("rebind on an unsupported CPA = %d %s", response.Code, response.Body.String())
	}
	after, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Connection.ManagementKey != before.Connection.ManagementKey {
		t.Fatal("an unsupported version was still persisted")
	}
}

// TestExternalRebindExplainsAForbiddenAnswer proves a CPA that answers 403 —
// what it does to an address banned for too many failed authentications, or one
// whose remote management is disabled — is reported with the same rejection code
// and a detail that keeps the same credential worth retrying. The ban is
// temporary, so asking for a new key first would send the administrator after a
// credential that was never the problem.
func TestExternalRebindExplainsAForbiddenAnswer(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "")
	before, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rotated := strings.Repeat("6", 64)
	cpaServer.forbid.Store(true)
	response := rebindCall(t, app, rebindBody("", rotated))
	if response.Code != http.StatusConflict {
		t.Fatalf("rebind against a refusing client = %d %s", response.Code, response.Body.String())
	}
	var answer struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode refusal: %v (%s)", err, response.Body.String())
	}
	if answer.Code != freshconfig.ReasonManagementRejected {
		t.Fatalf("code = %q, want the rejection code the 401 uses", answer.Code)
	}
	if !strings.Contains(answer.Detail, "ban") {
		t.Fatalf("detail = %q, want the ban window named", answer.Detail)
	}
	if strings.Contains(response.Body.String(), rotated) {
		t.Fatal("the refusal echoed the candidate credential")
	}
	after, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Connection.ManagementKey != before.Connection.ManagementKey {
		t.Fatal("a refused rebind persisted the candidate")
	}
	// The probe, the statistics read and the key read all report the same code
	// for the same answer, so the page has one blocker to explain.
	if _, _, err = app.keyApply.ExternalTarget(t.Context(), 1); err != nil {
		t.Fatalf("authority on a refusing client = %v", err)
	}
}

// TestExternalRebindIsAdminOnlyAndRefusesStaleGenerations proves the route is an
// admin write: a request built against a generation the installation has left is
// refused instead of reaching the CPA, and an unauthorized caller is rejected
// before anything else happens.
func TestExternalRebindIsAdminOnlyAndRefusesStaleGenerations(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "")
	// An unauthenticated caller never reaches the authority.
	request := httptest.NewRequest(http.MethodPost, "/control/api/v2/external/rebind", bytes.NewBufferString(rebindBody("", externalAuthorityManagement)))
	request.Header.Set("Content-Type", "application/json")
	unauthorized := httptest.NewRecorder()
	app.canonicalAPI(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated rebind = %d", unauthorized.Code)
	}
	// A rebind against a generation the record no longer holds is a mismatch.
	probes := cpaServer.probes.Load()
	rebound, err := app.keyApply.Rebind(t.Context(), app.store.(externalRebindStore), app.protector, 99,
		"", externalAuthorityManagement)
	if !errors.Is(err, freshkeyapply.ErrGeneration) || rebound.Record.Operation.Revision != 0 {
		t.Fatalf("stale generation rebind = %+v %v", rebound, err)
	}
	if cpaServer.probes.Load() != probes {
		t.Fatal("a stale request reached the CPA")
	}
	// A rebind to the credential already stored is a re-verify: the CPA answers
	// the stored key, so the blocker clears without a rotation being claimed.
	response := rebindCall(t, app, rebindBody("", externalAuthorityManagement))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "reverified") {
		t.Fatalf("re-verify with the stored credential = %d %s", response.Code, response.Body.String())
	}
}

// TestExternalRebindRewritesBothPersistedRows proves the rebind moves both
// persisted connection rows: the compatibility mirrors a legacy reader uses carry
// the new credential, so nothing keeps authenticating with the replaced one.
func TestExternalRebindRewritesBothPersistedRows(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "")
	rotated := strings.Repeat("5", 64)
	cpaServer.management = rotated
	if response := rebindCall(t, app, rebindBody("", rotated)); response.Code != http.StatusOK {
		t.Fatalf("rebind = %d %s", response.Code, response.Body.String())
	}
	manager, found, err := app.runtimeSettings.LoadManagerConfig(t.Context())
	if err != nil || !found {
		t.Fatalf("manager config = found %v, %v", found, err)
	}
	if manager.CPAConnection.ManagementKey != rotated {
		t.Fatal("the manager configuration mirror kept the replaced credential")
	}
	setup, found, err := app.runtimeSettings.LoadSetup(t.Context())
	if err != nil || !found {
		t.Fatalf("setup row = found %v, %v", found, err)
	}
	if setup.ManagementKey != rotated {
		t.Fatal("the setup mirror kept the replaced credential")
	}
}

// TestExternalRebindNeedsACommittedExternalInstallation proves a record that is
// not a committed Connect External installation has no rebind: the route answers
// its own refusal instead of writing to a profile it does not own.
func TestExternalRebindNeedsACommittedExternalInstallation(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	t.Cleanup(closeApp)
	response := rebindCall(t, app, rebindBody("", externalAuthorityManagement))
	if response.Code != http.StatusConflict {
		t.Fatalf("rebind without an External installation = %d %s", response.Code, response.Body.String())
	}
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if record.Connection != nil {
		t.Fatal("a refused rebind created a connection")
	}
}
