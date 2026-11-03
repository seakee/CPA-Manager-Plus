package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// adoption posts the summary read exactly as the browser does: authenticated
// with the Admin credential of a committed installation.
func (f *adoptMigrationFixture) adoption(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	return f.adoptionWith("Bearer " + strategyTestAdmin)
}

// adoptionWith is the same read with an explicit credential, because the route's
// authorization is part of what these tests have to prove.
func (f *adoptMigrationFixture) adoptionWith(auth string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/control/api/v2/installation/adoption", nil)
	if auth != "" {
		request.Header.Set("Authorization", auth)
	}
	response := httptest.NewRecorder()
	f.app.canonicalAPI(response, request)
	return response
}

// installationSession issues the installation session of an app that has not cut
// over yet and returns the token the browser holds as `Fresh <token>`. The
// session survives the cutover; the claim routes do not, so this must be taken
// before the routing checkpoint commits.
func installationSession(t *testing.T, app *freshApplication) string {
	t.Helper()
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := app.authority.Issue(t.Context())
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claim, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handoff.Fragment, "#handoff="))
	if err != nil {
		t.Fatal(err)
	}
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(claim, &issued); err != nil || issued.Token == "" {
		t.Fatalf("the claim is not a token: %v", err)
	}
	if handoff.Resume {
		// A resumed handoff already carries the session: the installation has
		// its Admin credential, so the claim is exchanged for it by the handoff.
		return issued.Token
	}
	session, err := app.authority.Exchange(t.Context(), fresh.ExchangeRequest{
		Identity: record.Operation.Identity, ClaimGeneration: handoff.ClaimGeneration, Token: issued.Token,
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	return session.Token
}

// readAdoption decodes the summary reply, failing on anything the completion
// screen could not render.
func readAdoption(t *testing.T, response *httptest.ResponseRecorder) adoptionSummaryReply {
	t.Helper()
	var reply adoptionSummaryReply
	if response.Code != http.StatusOK {
		t.Fatalf("summary read answered %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatalf("summary read did not answer the recorded shape: %s", response.Body.String())
	}
	return reply
}

// The read route answers with the recorded per-item results and the run markers,
// so a completion screen can show progress without the completion decision.
func TestAdoptionSummaryReportsTheRecordedItems(t *testing.T) {
	fixture := settledAdoption(t)
	record := fixture.reload(t)
	reply := readAdoption(t, fixture.adoption(t))

	if reply.Planned != record.AdoptionSummary.Planned || !reply.Finished || reply.Running {
		t.Fatalf("run markers disagree with the record: %+v", reply)
	}
	if reply.RunID != record.AdoptionSummary.RunID || len(reply.RunID) != 32 {
		t.Fatalf("the reply does not carry the run generation: %q", reply.RunID)
	}
	if len(reply.Items) != len(record.AdoptionSummary.Items) || len(reply.Items) == 0 {
		t.Fatalf("items disagree with the record: %d vs %d", len(reply.Items), len(record.AdoptionSummary.Items))
	}
	migrated, manual, failed := 0, 0, 0
	absent := 0
	for _, item := range record.AdoptionSummary.Items {
		switch item.Outcome {
		case freshstore.AdoptionMigrated:
			migrated++
		case freshstore.AdoptionManual:
			manual++
		case freshstore.AdoptionFailed:
			failed++
		case freshstore.AdoptionAbsentAtSource:
			absent++
		}
	}
	if reply.Counts.Migrated != migrated || reply.Counts.Manual != manual ||
		reply.Counts.Failed != failed || reply.Counts.AbsentAtSource != absent {
		t.Fatalf("counts disagree with the record: %+v", reply.Counts)
	}
	if migrated+manual+failed+absent != len(reply.Items) {
		t.Fatalf("the tallies do not cover every item: %+v", reply.Counts)
	}
	if reply.Retryable != 0 {
		t.Fatalf("a settled run reported %d retryable items", reply.Retryable)
	}
	for _, item := range reply.Items {
		if item.Item == "" || item.Kind == "" || item.Outcome == "" {
			t.Fatalf("item without identity, kind or outcome: %+v", item)
		}
		if item.Outcome == string(freshstore.AdoptionManual) && item.Reason == "" {
			t.Fatalf("a manual item carries no reason: %+v", item)
		}
	}
	// The generation is a fact about the record, not about one read of it.
	if again := readAdoption(t, fixture.adoption(t)); again.RunID != reply.RunID {
		t.Fatalf("the run generation changed between two reads")
	}
}

// A staged adoption, or an installation that is not an adoption, has no summary
// to describe and is refused with the bounded code rather than an empty list that
// looks like a finished run.
func TestAdoptionSummaryRefusesWithoutACommittedAdoption(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)
	record := fixture.reload(t)
	if record.Operation.Strategy.Stage != "staged" {
		t.Fatalf("the fixture is not staged: %s", record.Operation.Strategy.Stage)
	}
	response := fixture.adoption(t)
	if response.Code != http.StatusNotFound {
		t.Fatalf("a staged adoption answered %d: %s", response.Code, response.Body.String())
	}
	var refusal struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(response.Body.Bytes(), &refusal) != nil || refusal.Code != adoptionSummaryUnavailable {
		t.Fatalf("the refusal did not name the missing summary: %s", response.Body.String())
	}

	// Committing the same operation makes the same route answer. The record's
	// strategy is what decides, never the route's own history.
	if committed := fixture.strategy(t, record); committed.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", committed.Code, committed.Body.String())
	}
	fixture.wait()
	readAdoption(t, fixture.adoption(t))
}

// An installation with no strategy at all is refused the same way: the route
// never guesses that an empty record is an adoption.
func TestAdoptionSummaryRefusesANonAdoption(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	app := newFreshApplication(opened, config.Config{}, nil)
	// The session has to be taken while the installation is still claimable:
	// both claim routes close with the cutover, and the session is what the
	// browser keeps afterwards.
	session := installationSession(t, app)
	record, err := app.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Update(ctx, record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: r.Operation.Identity.OperationID, Revision: 1,
			Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/control/api/v2/installation/adoption", nil)
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app.canonicalAPI(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), adoptionSummaryUnavailable) {
		t.Fatalf("a non-adoption answered %d: %s", response.Code, response.Body.String())
	}
	// The browser's own credential reaches the same answer: the session branch
	// admits the read, and the route's projection refuses it because no adoption
	// was ever committed.
	request = httptest.NewRequest(http.MethodGet, "/control/api/v2/installation/adoption", nil)
	request.Header.Set("Authorization", "Fresh "+session)
	response = httptest.NewRecorder()
	app.canonicalAPI(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), adoptionSummaryUnavailable) {
		t.Fatalf("a non-adoption answered its session %d: %s", response.Code, response.Body.String())
	}
}

// `running` is the live flag, not a recorded value: a poll taken while the copy
// is in flight says so, and the same route says otherwise once it settles.
func TestAdoptionSummaryReportsTheRunInFlight(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)
	release, entered := blockCredentialDownloads(t, fixture)
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	<-entered
	inFlight := readAdoption(t, fixture.adoption(t))
	if !inFlight.Running || inFlight.Finished {
		t.Fatalf("an in-flight run was reported as settled: %+v", inFlight)
	}
	if len(inFlight.RunID) != 32 {
		t.Fatalf("the in-flight run carries no generation: %+v", inFlight)
	}
	release()
	fixture.wait()
	if settled := readAdoption(t, fixture.adoption(t)); settled.Running || !settled.Finished {
		t.Fatalf("a settled run was reported as in flight: %+v", settled)
	}
	if settled := readAdoption(t, fixture.adoption(t)); settled.RunID != inFlight.RunID {
		t.Fatalf("the settled run changed generation without a new run")
	}
}

// A failure that wrote nothing is retryable, and the reply says so both per item
// and in the count the completion refusal reports.
func TestAdoptionSummaryMarksRetryableItems(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)
	fixture.source.downloadFailures["codex-source.json"] = true
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()
	reply := readAdoption(t, fixture.adoption(t))
	if reply.Retryable != 1 || reply.Counts.Failed != 1 {
		t.Fatalf("the retryable item was not reported: %+v", reply)
	}
	retryable := 0
	for _, item := range reply.Items {
		if !item.Retryable {
			continue
		}
		retryable++
		if item.Outcome != string(freshstore.AdoptionFailed) || item.Reason != "download_failed" {
			t.Fatalf("a retryable item reported the wrong outcome: %+v", item)
		}
	}
	if retryable != 1 {
		t.Fatalf("per-item retryable flags disagree with the count: %d", retryable)
	}
}

// The browser reads the progress with its Fresh installation session, not with
// an Admin credential, so the session branch is the one the completion screen
// actually uses. This drives each of its answers.
func TestAdoptionSummaryAuthorizesTheInstallationSession(t *testing.T) {
	fixture := settledAdoption(t)
	session := fixture.session
	record := fixture.reload(t)
	if record.Operation.ProductComplete {
		t.Fatal("the fixture completed before the session read was taken")
	}
	reply := readAdoption(t, fixture.adoptionWith("Fresh "+session))
	if reply.Planned != record.AdoptionSummary.Planned ||
		len(reply.Items) != len(record.AdoptionSummary.Items) {
		t.Fatalf("the session read a different summary: %+v", reply)
	}
	// No credential, an invented session and the Admin key sent as one are all
	// refused: the branch admits exactly the claim that committed the adoption.
	for _, auth := range []string{"", "Fresh " + strings.Repeat("a", 43), "Bearer " + session} {
		if response := fixture.adoptionWith(auth); response.Code != http.StatusUnauthorized {
			t.Fatalf("%q was not refused: %d %s", auth, response.Code, response.Body.String())
		}
	}
	// Completion ends the claim's reach. The summary it read while the
	// installation was pending is not readable through it afterwards, because a
	// completed installation reads through its Admin credential only.
	if _, err := fixture.app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if response := fixture.adoptionWith("Fresh " + session); response.Code != http.StatusUnauthorized {
		t.Fatalf("a completed installation served its summary to the claim: %d %s", response.Code, response.Body.String())
	}
}

// The reply is progress only: it carries no digest, no source Key, no management
// secret and no credential byte. The record never holds a value, and the route
// proves it by emitting none of the fixture's own secrets.
func TestAdoptionSummaryLeaksNoSecretOrDigest(t *testing.T) {
	fixture := settledAdoption(t)
	body := fixture.adoption(t).Body.String()
	for _, secret := range []string{
		migrationSourceKey, migrationCredentialToken, migrationProviderKeyValue,
		migrationSourceClientValue, fixture.runtime.stagedKey,
	} {
		if secret == "" {
			continue
		}
		if strings.Contains(body, secret) {
			t.Fatalf("the summary leaked a secret: %s", body)
		}
	}
	for _, field := range []string{"sourceSha256", "destinationSha256", "secret-key", "managementKey", "access_token", "instruction"} {
		if strings.Contains(body, field) {
			t.Fatalf("the summary carried %q: %s", field, body)
		}
	}
}

// The summary route is the read face of text the Manager did not author: a
// credential file name is the source CPA's metadata, and the type string is the
// destination's. Both travel through the migration record, and neither may be
// echoed to the browser as free text — the reply is a projection of typed codes,
// closed-vocabulary references and generic labels.
func TestAdoptionSummaryRedactsUntrustedMetadata(t *testing.T) {
	// A Client Key and a credential token in the shapes CPA issues, placed where
	// the run reads names and types rather than values.
	const (
		untrustedKey   = "sk-proj-m3taDatakeyValue0011223344556677"
		untrustedToken = "sk-ant-credentialTokenValue889900112233"
	)
	fixture := newAdoptMigrationFixture(t)
	name := untrustedKey + ".json"
	fixture.source.credentials[name] = []byte(`{"type":"claude","access_token":"` + untrustedToken + `"}`)
	fixture.source.types[name] = "claude"
	// The destination classifies the copied file under the token, which makes the
	// type string the instruction's own interpolation: arbitrary upstream text.
	fixture.destination.types[name] = untrustedToken
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()

	// The fixture really did put both values into recorded metadata: the reply's
	// silence below is the boundary's doing, not an accident of the run.
	record := fixture.reload(t)
	named, diverged := false, false
	for _, item := range record.AdoptionSummary.Items {
		named = named || strings.Contains(item.Item, untrustedKey)
		diverged = diverged ||
			(item.Reason == "type_diverged" && strings.Contains(item.Instruction, untrustedToken))
	}
	if !named || !diverged {
		t.Fatalf("the run did not record the metadata these assertions are about: %+v", record.AdoptionSummary.Items)
	}

	body := fixture.adoption(t).Body.String()
	if strings.Contains(body, untrustedKey) {
		t.Errorf("the summary echoed the Client Key from a credential name: %s", body)
	}
	if strings.Contains(body, untrustedToken) || strings.Contains(body, "classifies it as") {
		t.Errorf("the summary carried the destination's type metadata: %s", body)
	}
	// The item is still reported, under a generic label with its bounded reason.
	reply := readAdoptionJSON(t, fixture.adoption(t))
	shown := false
	for _, item := range reply.Items {
		if item.Kind == string(freshstore.AdoptionKindCredential) && item.Reason == "type_diverged" {
			shown = true
			if !strings.HasPrefix(item.Item, adoptionCredentialLabel+" #") {
				t.Errorf("a credential item was not projected structurally: %+v", item)
			}
		}
	}
	if !shown {
		t.Fatalf("the diverged credential was not reported at all: %+v", reply.Items)
	}
}

// The summary is read while the public route is still pending, so the fixed
// bootstrap carrier has to know the resource: a resource it does not know is
// answered 404 by the carrier itself, which is what made the browser's progress
// read fail with a plain-text page instead of the recorded summary.
func TestAdoptionSummaryIsAServedResourceOnTheFixedCarrier(t *testing.T) {
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	app := newFreshApplication(opened, config.Config{}, nil)

	request := func(resource string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodGet,
			bootstrap.FixedBootstrapAPIPath+resource,
			nil,
		)
		req.Header.Set("Authorization", "Fresh "+strings.Repeat("a", 43))
		response := httptest.NewRecorder()
		app.fixed.ServeHTTP(response, req)
		return response
	}
	if response := request("installation/adoption"); response.Code != http.StatusUnauthorized {
		t.Fatalf("the fixed carrier does not serve the summary: %d %s", response.Code, response.Body.String())
	}
	if response := request("installation/unknown"); response.Code != http.StatusNotFound {
		t.Fatalf("an unknown resource was not refused: %d", response.Code)
	}
}

// summaryItemJSON and summaryJSON decode the reply as it reaches the browser.
// They are deliberately separate from the reply type the route builds: these
// assertions are about what was written to the wire, and they have to be the same
// assertions against any version of the projection.
type summaryItemJSON struct {
	ID        string `json:"id"`
	Item      string `json:"item"`
	Kind      string `json:"kind"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason"`
	Retryable bool   `json:"retryable"`
	Left      int    `json:"left"`
}

type summaryJSON struct {
	Items     []summaryItemJSON `json:"items"`
	RunID     string            `json:"runId"`
	Planned   int               `json:"planned"`
	Finished  bool              `json:"finished"`
	Running   bool              `json:"running"`
	Retryable int               `json:"retryable"`
}

func readAdoptionJSON(t *testing.T, response *httptest.ResponseRecorder) summaryJSON {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("summary read answered %d: %s", response.Code, response.Body.String())
	}
	var reply summaryJSON
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatalf("summary read did not answer the recorded shape: %s", response.Body.String())
	}
	return reply
}

// noProtectedFragment fails when any window of at least eight bytes of a
// protected value reaches the reply. Eight is the shortest value this Manager
// treats as a credential, so a shorter window cannot identify one; a longer one
// must never be emitted.
func noProtectedFragment(t *testing.T, body string, value string) {
	t.Helper()
	for start := 0; start+8 <= len(value); start++ {
		if fragment := value[start : start+8]; strings.Contains(body, fragment) {
			t.Fatalf("the summary carried an eight-byte fragment of a protected value: %q", fragment)
		}
	}
}

// A credential file name is source metadata and can be, or contain, a credential
// whose shape the project's scrubber does not recognise. The reply therefore
// never carries the name at all: every credential item is a Manager-owned label
// with a stable ordinal, whether the value appears whole, as a prefix, a middle
// or a suffix fragment, across two values' start offset, under an overlong cut,
// or as the marker's own literal text.
func TestAdoptionSummaryProjectsCredentialItemsStructurally(t *testing.T) {
	const (
		middleValue  = "middleValue0123456789abcdefghij"
		suffixValue  = "suffixValue0123456789abcdefghijkl"
		overlapValue = "01234567overlapValueabcdefghij"
		markerName   = "[redacted]-literalValue0123456789"
	)
	// Two real source client keys: one used whole as a name, one only as a middle
	// fragment. The rest are ordinary names carrying the fragments above.
	wholeKey := "sk-wholeValue0123456789abcdef"
	fixture := newAdoptMigrationFixtureWithClientKeys(t, migrationSourceClientValue, wholeKey)
	overlong := strings.Repeat("p", 100) + "overlongValue0123456789abcdefgh" + strings.Repeat("q", 200) + ".json"
	names := []string{
		migrationSourceClientValue + ".json",
		wholeKey + ".json",
		"prefix-" + middleValue[10:] + ".json",
		"trailer-" + suffixValue[len(suffixValue)-16:] + ".json",
		// Two values whose matches start at different offsets in the same text:
		// the shorter one is a prefix of the longer one and the longer one starts
		// where the shorter ends.
		overlapValue[:16] + overlapValue + ".json",
		markerName + ".json",
		overlong,
	}
	for _, name := range names {
		fixture.source.credentials[name] = []byte(`{"type":"claude","access_token":"x"}`)
		fixture.source.types[name] = "claude"
	}
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()

	// The record really does hold the names, so the reply's silence below is the
	// projection's doing rather than an accident of the run. An overlong name is
	// recorded as a bounded prefix, which is still identity text.
	record := fixture.reload(t)
	for _, name := range names {
		needle := name
		if len(needle) > 120 {
			needle = needle[:120]
		}
		found := false
		for _, item := range record.AdoptionSummary.Items {
			if strings.Contains(item.Item, needle) {
				found = true
			}
		}
		if !found {
			t.Fatalf("the run recorded no identity for %q", name)
		}
	}

	response := fixture.adoption(t)
	body := response.Body.String()
	reply := readAdoptionJSON(t, response)
	// Every credential item is the generic label and a contiguous ordinal, and no
	// credential text carries any part of a name.
	ordinal := 0
	labels := 0
	for _, item := range reply.Items {
		if item.Kind != string(freshstore.AdoptionKindCredential) {
			continue
		}
		if item.Left > 0 {
			if item.Item != adoptionCredentialOverflowLabel {
				t.Fatalf("the overflow marker was not projected structurally: %+v", item)
			}
			continue
		}
		ordinal++
		if want := adoptionCredentialLabel + " #" + strconv.Itoa(ordinal); item.Item != want {
			t.Fatalf("credential item text = %q, want %q", item.Item, want)
		}
		labels++
	}
	expected := 0
	for _, item := range record.AdoptionSummary.Items {
		if item.Kind == freshstore.AdoptionKindCredential && item.Left == 0 {
			expected++
		}
	}
	if labels != expected {
		t.Fatalf("the reply projected %d credential labels for %d recorded credentials", labels, expected)
	}
	if expected < len(names) {
		t.Fatalf("the fixture recorded %d credentials for %d seeded names", expected, len(names))
	}
	// Canonical non-credential references still identify their item: they come
	// from the Manager's own tables, not from the source.
	if !strings.Contains(body, "config:routing.strategy") || !strings.Contains(body, "section:api-keys.claude") {
		t.Fatalf("the reply dropped the canonical references: %s", body)
	}
	// Protected values and every recorded name are absent, whole and in
	// fragments: whole value, prefix, middle-only, suffix-only, two values whose
	// matches start at different offsets, the overlong cut and the marker's own
	// literal text.
	for _, value := range []string{
		migrationSourceClientValue, wholeKey,
		middleValue[10:], suffixValue[len(suffixValue)-16:],
		overlapValue, overlapValue[:16], markerName, overlong,
	} {
		noProtectedFragment(t, body, value)
	}
	for _, field := range []string{"…#sha256:", "credential-name-sha256", "credential:"} {
		if strings.Contains(body, field) {
			t.Fatalf("the reply carried the record's identity text %q: %s", field, body)
		}
	}
	// The identity is stable across reads, unique per item and derived only from
	// public text: it never confirms a source name offline.
	seen := map[string]bool{}
	for _, item := range reply.Items {
		if len(item.ID) != 64 {
			t.Fatalf("item %q carries no stable identity: %+v", item.Item, item)
		}
		if seen[item.ID] {
			t.Fatalf("two items share one identity: %+v", reply.Items)
		}
		seen[item.ID] = true
	}
	again := readAdoptionJSON(t, fixture.adoption(t))
	for index := range reply.Items {
		if again.Items[index].ID != reply.Items[index].ID {
			t.Fatalf("item %d changed identity between two reads", index)
		}
	}
}

// An earlier build persisted an overlong credential as the unsalted SHA-256 of
// its whole name, and this build persists the same file with a readable prefix
// and that digest. Neither record may reach the browser, and a resume must
// recognise both rather than append a third identity for the same file.
func TestAdoptionSummaryProjectsALegacyRecordAndResumesIt(t *testing.T) {
	// ASCII only, so the executor's 120-byte cut and this test's prefix agree.
	const fragment = "legacy-overlong-value-0123456789ab"
	name := strings.Repeat("p", 100) + fragment + strings.Repeat("q", 200) + ".json"
	fixture := newAdoptMigrationFixtureWithClientKeys(t, migrationSourceClientValue)
	fixture.source.credentials[name] = []byte(`{"type":"claude","access_token":"x"}`)
	fixture.source.types[name] = "claude"

	raw := sha256.Sum256([]byte(name))
	legacyDigest := hex.EncodeToString(raw[:])
	legacyItem := freshstore.AdoptionItem{
		Item: "credential-name-sha256:" + legacyDigest, Kind: freshstore.AdoptionKindCredential,
		Outcome: freshstore.AdoptionManual, Reason: "adoption_item_name_too_long",
		Instruction: "Copy the credential file into the destination CPA's auth directory by hand.",
	}
	// The shape this build writes, seeded as a record an earlier run of this
	// build already settled.
	truncatedItem := freshstore.AdoptionItem{
		Item: "credential:" + name[:120] + "…#sha256:" + legacyDigest, Kind: freshstore.AdoptionKindCredential,
		Outcome: freshstore.AdoptionManual, Reason: "adoption_item_name_too_long",
		Instruction: "Copy the credential file into the destination CPA's auth directory by hand.",
	}
	seed := freshstore.AdoptionSummary{Items: []freshstore.AdoptionItem{legacyItem, truncatedItem}, Planned: 2}
	record := fixture.reload(t)
	record, err := fixture.app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		copied := seed
		r.AdoptionSummary = &copied
		return nil
	})
	if err != nil {
		t.Fatalf("seed the legacy record: %v", err)
	}

	if response := fixture.strategy(t, record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()

	// The resume recognised both identities and appended nothing for this file.
	settled := fixture.reload(t)
	if len(settled.AdoptionSummary.Items) < 2 ||
		settled.AdoptionSummary.Items[0] != legacyItem || settled.AdoptionSummary.Items[1] != truncatedItem {
		t.Fatalf("the resume rewrote the seeded record: %+v", settled.AdoptionSummary.Items)
	}
	tooLong := 0
	appended := false
	for _, item := range settled.AdoptionSummary.Items {
		if item.Reason == "adoption_item_name_too_long" {
			tooLong++
		}
		if item.Item == "credential:"+name[:120]+"…#sha256:"+legacyDigest {
			appended = true
		}
	}
	if tooLong != 2 {
		t.Fatalf("the resume re-recorded an overlong name: %d manual items", tooLong)
	}
	if count := strings.Count(strings.Join(itemNames(settled.AdoptionSummary), "\x00"), "credential-name-sha256:"); count != 1 {
		t.Fatalf("the resume duplicated the legacy identity: %+v", settled.AdoptionSummary.Items)
	}
	if !appended {
		t.Fatal("the seeded truncated identity disappeared from the record")
	}

	response := fixture.adoption(t)
	body := response.Body.String()
	for _, forbidden := range []string{legacyDigest, "…#sha256:", "credential-name-sha256", strings.Repeat("p", 100), strings.Repeat("q", 8)} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the reply carried %q from the legacy record", forbidden)
		}
	}
	for _, value := range []string{migrationSourceClientValue, fragment} {
		noProtectedFragment(t, body, value)
	}
	reply := readAdoptionJSON(t, response)
	if len(reply.RunID) != 32 {
		t.Fatalf("the resumed run did not persist its generation: %q", reply.RunID)
	}
	for _, item := range reply.Items {
		if item.Kind == string(freshstore.AdoptionKindCredential) && item.Left == 0 &&
			!strings.HasPrefix(item.Item, adoptionCredentialLabel+" #") {
			t.Fatalf("a credential item was not projected structurally: %+v", item)
		}
		if strings.Contains(item.Reason, "sha256") {
			t.Fatalf("the reply carried a legacy identity as a reason: %+v", item)
		}
	}
}

// itemNames returns the record's identities, for assertions that count one
// identity without depending on the reply's projection.
func itemNames(summary *freshstore.AdoptionSummary) []string {
	if summary == nil {
		return nil
	}
	names := make([]string, 0, len(summary.Items))
	for _, item := range summary.Items {
		names = append(names, item.Item)
	}
	return names
}

// A record written before run generations existed is still projected: its
// `runId` is empty on the wire, its credential identities are structural, and the
// projection does not invent a generation the record never carried.
func TestAdoptionSummaryProjectsALegacyRecordWithoutARunGeneration(t *testing.T) {
	digest := strings.Repeat("a", 64)
	reply := adoptionSummaryFrom(&freshstore.AdoptionSummary{
		Items: []freshstore.AdoptionItem{{
			Item: "credential-name-sha256:" + digest, Kind: freshstore.AdoptionKindCredential,
			Outcome: freshstore.AdoptionManual, Reason: "adoption_item_name_too_long",
		}},
		Planned: 1, Finished: true,
	})
	if reply.RunID != "" {
		t.Fatalf("a legacy record invented a run generation: %q", reply.RunID)
	}
	if len(reply.Items) != 1 || reply.Items[0].Item != adoptionCredentialLabel+" #1" {
		t.Fatalf("the legacy credential was not projected structurally: %+v", reply.Items)
	}
	encoded, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	if !strings.Contains(body, `"runId":""`) {
		t.Fatalf("the reply omitted the legacy run identity: %s", body)
	}
	if strings.Contains(body, digest) || strings.Contains(body, "credential-name-sha256") {
		t.Fatalf("the reply carried the legacy identity: %s", body)
	}
	if reply.Items[0].Reason != "adoption_item_name_too_long" {
		t.Fatalf("the projection dropped a bounded reason: %+v", reply.Items[0])
	}
}

// A reason is text the record carries from its producer, and a failed
// destination write can quote CPA's own rejection. The reply keeps this
// Manager's own codes and drops everything else, so no upstream text can reach
// the browser through the one free-text field the projection still has.
func TestAdoptionSummaryReasonsAreBoundedToOwnCodes(t *testing.T) {
	const upstream = "cpa said: upstream-value-0123456789abcdef"
	summary := &freshstore.AdoptionSummary{Items: []freshstore.AdoptionItem{
		{Item: "config:routing.strategy", Kind: freshstore.AdoptionKindConfigLeaf, Outcome: freshstore.AdoptionFailed, Reason: upstream},
		{Item: "credential:claude.json", Kind: freshstore.AdoptionKindCredential, Outcome: freshstore.AdoptionManual, Reason: "type_diverged"},
		{Item: "rule:default", Kind: freshstore.AdoptionKindConfigFamily, Outcome: freshstore.AdoptionManual, Reason: "reconfigure_after_adoption"},
		{Item: "section:attacker-named-section", Kind: freshstore.AdoptionKindProviderSection, Outcome: freshstore.AdoptionManual, Reason: "reconfigure_after_adoption"},
	}}
	reply := adoptionSummaryFrom(summary)
	if reply.Items[0].Reason != "" {
		t.Fatalf("an upstream reason reached the reply: %q", reply.Items[0].Reason)
	}
	if reply.Items[1].Reason != "type_diverged" || reply.Items[2].Reason != "reconfigure_after_adoption" {
		t.Fatalf("a bounded code was dropped: %+v", reply.Items)
	}
	// A canonical Manager reference stays; an identity outside the closed
	// vocabulary is not echoed and keeps an ordinal-based label instead.
	if reply.Items[0].Item != "config:routing.strategy" {
		t.Fatalf("a canonical leaf lost its reference: %q", reply.Items[0].Item)
	}
	if reply.Items[2].Item != adoptionConfigurationLabel+" #3" {
		t.Fatalf("an unknown configuration identity was echoed: %q", reply.Items[2].Item)
	}
	if reply.Items[3].Item != adoptionProviderLabel+" #4" {
		t.Fatalf("an unknown provider identity was echoed: %q", reply.Items[3].Item)
	}
	// The item whose reason was dropped is still reported: the outcome is the
	// fact the operator acts on.
	if reply.Items[0].Outcome != string(freshstore.AdoptionFailed) {
		t.Fatalf("the item lost its outcome with its reason: %+v", reply.Items[0])
	}
	encoded, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "upstream-value") {
		t.Fatalf("the projection emitted the upstream reason: %s", encoded)
	}
	// A non-credential identity outside the closed vocabulary is not echoed
	// either, and keeps a stable ordinal-based label instead.
	for _, item := range reply.Items {
		if strings.Contains(item.Item, "upstream") {
			t.Fatalf("the projection emitted an unknown identity: %+v", item)
		}
	}
}
