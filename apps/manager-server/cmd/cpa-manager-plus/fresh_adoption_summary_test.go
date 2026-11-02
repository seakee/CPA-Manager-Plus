package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	release()
	fixture.wait()
	if settled := readAdoption(t, fixture.adoption(t)); settled.Running || !settled.Finished {
		t.Fatalf("a settled run was reported as in flight: %+v", settled)
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
	for _, field := range []string{"sourceSha256", "destinationSha256", "secret-key", "managementKey", "access_token"} {
		if strings.Contains(body, field) {
			t.Fatalf("the summary carried %q: %s", field, body)
		}
	}
}

// The summary route is the read face of text the Manager did not author: a
// credential file name is the source CPA's metadata, and the type string is the
// destination's. Both travel through the migration record, and neither may be
// echoed to the browser as free text — the reply is a projection of typed codes
// and scrubbed identifiers, not the record's prose.
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

// A Client Key used verbatim as a credential file name is the one credential
// shape the project's scrubber cannot see: a bare digest or a UUID has no prefix
// to recognise. The reply is therefore redacted by value as well, from the source
// keys the migration itself read.
func TestAdoptionSummaryRedactsTheSourceClientKeysByValue(t *testing.T) {
	const (
		hexKey  = "d41d8cd98f00b204e9800998ecf8427e"
		uuidKey = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	)
	keys := []string{migrationSourceClientValue, hexKey, uuidKey}
	// The source presents all three shapes, and the destination was staged from
	// that inventory, so verification compares the same list.
	fixture := newAdoptMigrationFixtureWithClientKeys(t, keys...)
	for _, key := range keys {
		name := key + ".json"
		fixture.source.credentials[name] = []byte(`{"type":"claude","access_token":"value-` + key + `"}`)
		fixture.source.types[name] = "claude"
	}
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()

	// The record really does hold the names, so the reply's silence below is the
	// boundary's doing rather than an accident of the run.
	record := fixture.reload(t)
	named := 0
	for _, item := range record.AdoptionSummary.Items {
		for _, key := range keys {
			if strings.Contains(item.Item, key) {
				named++
			}
		}
	}
	if named != len(keys) {
		t.Fatalf("the run recorded %d of %d client-key names: %+v", named, len(keys), record.AdoptionSummary.Items)
	}

	body := fixture.adoption(t).Body.String()
	for _, key := range keys {
		if strings.Contains(body, key) {
			t.Errorf("the summary echoed the source Client Key %q: %s", key, body)
		}
	}
	if !strings.Contains(body, "[redacted]") {
		t.Errorf("the summary did not mark the redaction: %s", body)
	}
}

// An overlong credential name is recorded as a bounded prefix, so a Client Key
// can straddle that cut: the whole value never reaches the record, and a
// whole-value match would leave the readable fragment behind. The reply must
// carry no fragment of a source value either.
func TestAdoptionSummaryRedactsATruncatedSourceValue(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	fixture := newAdoptMigrationFixtureWithClientKeys(t, migrationSourceClientValue, key)
	// The record keeps 120 bytes of the name: 100 readable bytes and the first
	// 20 of the key.
	name := strings.Repeat("p", 100) + key + strings.Repeat("q", 200) + ".json"
	fixture.source.credentials[name] = []byte(`{"type":"claude","access_token":"x"}`)
	fixture.source.types[name] = "claude"
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()

	// The record really does hold the fragment, so the reply's silence below is
	// the boundary's doing rather than an accident of the run.
	record := fixture.reload(t)
	truncated := false
	for _, item := range record.AdoptionSummary.Items {
		if strings.Contains(item.Item, key[:20]) {
			truncated = true
		}
	}
	if !truncated {
		t.Fatalf("the run recorded no truncated value to redact: %+v", record.AdoptionSummary.Items)
	}

	body := fixture.adoption(t).Body.String()
	// No window of the value long enough to be a credential may reach the
	// browser: a truncation leaves exactly such a window.
	for start := 0; start+minRedactedValue <= len(key); start++ {
		if fragment := key[start : start+minRedactedValue]; strings.Contains(body, fragment) {
			t.Fatalf("the summary carried a fragment of the source Client Key: %s", body)
		}
	}
	// The readable part of the name stays: redaction must not make the item
	// unrecognisable.
	if !strings.Contains(body, strings.Repeat("p", 100)) {
		t.Fatalf("the summary dropped the readable part of the name: %s", body)
	}
}

// Two source Client Keys may share their opening bytes. The boundary must
// replace the value the name actually holds whole, not let a longer value's
// shared prefix consume it and leave the rest of the shorter value behind.
func TestAdoptionSummaryRedactsValuesThatShareAPrefix(t *testing.T) {
	const (
		// The shorter value is the one used as a file name; the longer one only
		// shares its opening bytes.
		key   = "01234567aaaaaaaaaaaaaaaaaaaaaaaa"
		other = "01234567bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	fixture := newAdoptMigrationFixtureWithClientKeys(t, migrationSourceClientValue, key, other)
	name := key + ".json"
	fixture.source.credentials[name] = []byte(`{"type":"claude","access_token":"x"}`)
	fixture.source.types[name] = "claude"
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()

	// The record really does hold the name, so the reply's silence below is the
	// boundary's doing rather than an accident of the run.
	record := fixture.reload(t)
	named := false
	for _, item := range record.AdoptionSummary.Items {
		if strings.Contains(item.Item, key) {
			named = true
		}
	}
	if !named {
		t.Fatalf("the run recorded no name to redact: %+v", record.AdoptionSummary.Items)
	}

	body := fixture.adoption(t).Body.String()
	for start := 0; start+minRedactedValue <= len(key); start++ {
		if fragment := key[start : start+minRedactedValue]; strings.Contains(body, fragment) {
			t.Fatalf("the summary carried a fragment of the source Client Key: %s", body)
		}
	}
	if !strings.Contains(body, "[redacted]") {
		t.Errorf("the summary did not mark the redaction: %s", body)
	}
}

// The scan chooses the longest value match at each position, so the order the
// values were read in cannot decide which of them is replaced.
func TestAdoptionRedactorPicksTheLongestMatchAtEachPosition(t *testing.T) {
	const (
		shorter = "01234567aaaaaaaaaaaaaaaaaaaaaaaa"
		longer  = "01234567bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	for _, secrets := range [][]string{{longer, shorter}, {shorter, longer}} {
		redact := adoptionRedactor(secrets)
		for _, value := range []string{shorter, longer} {
			if got := redact("credential:" + value + ".json"); got != "credential:[redacted].json" {
				t.Fatalf("redact(%q) with %v = %q", value, secrets, got)
			}
		}
	}
}

// An overlong name's record identity carries the SHA-256 of the unscrubbed name.
// The reply must not: a reader who knows the rest of a file name could otherwise
// confirm a guessed credential offline from the item text.
func TestAdoptionSummaryOverlongNameIsNotAnOfflineOracle(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	fixture := newAdoptMigrationFixtureWithClientKeys(t, migrationSourceClientValue, key)
	// The record keeps 120 bytes of the name: 100 readable bytes and the first 20
	// of the key. Only a candidate for the whole name can confirm the digest.
	name := strings.Repeat("p", 100) + key + strings.Repeat("q", 200) + ".json"
	fixture.source.credentials[name] = []byte(`{"type":"claude","access_token":"x"}`)
	fixture.source.types[name] = "claude"
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()

	raw := sha256.Sum256([]byte(name))
	candidate := hex.EncodeToString(raw[:])
	// The record really does keep the digest, so the reply's silence below is the
	// boundary's doing rather than an accident of the run.
	recorded := false
	for _, recordedItem := range fixture.reload(t).AdoptionSummary.Items {
		if strings.Contains(recordedItem.Item, candidate) {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("the record kept no digest of the overlong name")
	}

	body := fixture.adoption(t).Body.String()
	if strings.Contains(body, candidate) {
		t.Fatalf("the summary confirms the candidate file name: %s", body)
	}
	if strings.Contains(body, overlongNameDigestMarker) {
		t.Fatalf("the summary carries the record's name digest marker: %s", body)
	}

	reply := readAdoptionJSON(t, fixture.adoption(t))
	index := -1
	for at := range reply.Items {
		if strings.Contains(reply.Items[at].Item, strings.Repeat("p", 100)) {
			index = at
		}
	}
	if index < 0 {
		t.Fatalf("the reply dropped the readable name: %+v", reply.Items)
	}
	item := reply.Items[index]
	if item.ID == candidate {
		t.Fatalf("the reply's identity confirms the candidate file name: %+v", item)
	}
	if len(item.ID) != 64 {
		t.Fatalf("the item carries no stable identity: %+v", item)
	}
	// The identity is the record's, so a second read states the same item.
	if again := readAdoptionJSON(t, fixture.adoption(t)); again.Items[index].ID != item.ID {
		t.Fatalf("the item identity changed between two reads")
	}
}

// The reply's identity for an item must not turn a candidate credential into
// something confirmable offline. The record's own name is not what the digest
// covers, and the identity still keeps a record's items apart and stable.
func TestAdoptionSummaryIdentityIsNotADigestOfTheRecordName(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	fixture := newAdoptMigrationFixtureWithClientKeys(t, migrationSourceClientValue, key)
	name := key + ".json"
	fixture.source.credentials[name] = []byte(`{"type":"claude","access_token":"x"}`)
	fixture.source.types[name] = "claude"
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()

	reply := readAdoptionJSON(t, fixture.adoption(t))
	// The old identity was exactly this digest of the unredacted name, which is
	// what made a guessed key confirmable from the reply.
	raw := sha256.Sum256([]byte("credential:" + key + ".json"))
	candidate := hex.EncodeToString(raw[:])
	identities := map[string]bool{}
	for _, item := range reply.Items {
		if item.ID == candidate {
			t.Fatalf("the reply's identity confirms the record's own name: %+v", item)
		}
		if strings.Contains(item.Item, key) {
			t.Fatalf("the reply showed the source Client Key: %+v", item)
		}
		if identities[item.ID] {
			t.Fatalf("two items share one identity: %+v", reply.Items)
		}
		identities[item.ID] = true
	}
	// The identity is a fact about the record, not about one read of it.
	again := readAdoptionJSON(t, fixture.adoption(t))
	for index := range reply.Items {
		if again.Items[index].ID != reply.Items[index].ID {
			t.Fatalf("item %d changed identity between two reads", index)
		}
	}
}

// A source value can begin with the marker's own text. The prefix rule must
// terminate and must not rewrite a marker the scrubber already wrote.
func TestAdoptionRedactorDoesNotRewrapItsOwnMarker(t *testing.T) {
	redact := adoptionRedactor([]string{"redactedValue123", "x-redacted-value"})
	for value, want := range map[string]string{
		"credential:redactedValue123.json": "credential:[redacted].json",
		"credential:[redacted].json":       "credential:[redacted].json",
		"redactedValue123-backup.json":     "[redacted]-backup.json",
	} {
		if got := redact(value); got != want {
			t.Fatalf("redact(%q) = %q, want %q", value, got, want)
		}
	}
}

// Two token-shaped file names scrub to the same text. The browser binds an
// acknowledgement to the record's item, not to text that happens to be equal, so
// the reply carries a stable identity that keeps them distinct.
func TestAdoptionSummaryKeepsScrubbedNamesDistinct(t *testing.T) {
	const (
		first  = "sk-proj-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		second = "sk-proj-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	fixture := newAdoptMigrationFixture(t)
	for _, key := range []string{first, second} {
		name := key + ".json"
		fixture.source.credentials[name] = []byte(`{"type":"claude","access_token":"shared-token-"` + key + `"}`)
		fixture.source.types[name] = "claude"
	}
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()

	reply := readAdoptionJSON(t, fixture.adoption(t))
	byItem := map[string][]string{}
	for _, item := range reply.Items {
		if len(item.ID) != 64 {
			t.Fatalf("item %q carries no stable identity: %+v", item.Item, item)
		}
		byItem[item.Item] = append(byItem[item.Item], item.ID)
	}
	collided := 0
	for _, named := range byItem {
		if len(named) < 2 {
			continue
		}
		collided++
		distinct := map[string]bool{}
		for _, id := range named {
			if distinct[id] {
				t.Fatalf("two items share one identity: %v", named)
			}
			distinct[id] = true
		}
	}
	if collided == 0 {
		t.Fatalf("the fixtures did not collapse into one name: %+v", byItem)
	}
}

// A credential whose name cannot be an item identity keeps enough of the name to
// be actionable. The record also keeps a digest of the whole name for its own
// identity; the reply keeps the prefix and the ellipsis, not the digest.
func TestAdoptionSummaryKeepsAnOverlongNameReadable(t *testing.T) {
	overlong := strings.Repeat("overlong-credential-name-", 12) + ".json"
	fixture := newAdoptMigrationFixture(t)
	fixture.source.credentials[overlong] = []byte(`{"type":"claude","access_token":"overlong"}`)
	fixture.source.types[overlong] = "claude"
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()

	reply := readAdoptionJSON(t, fixture.adoption(t))
	var item *summaryItemJSON
	for index := range reply.Items {
		if reply.Items[index].Reason == "adoption_item_name_too_long" {
			item = &reply.Items[index]
		}
	}
	if item == nil {
		t.Fatalf("the overlong name was not recorded as manual work: %+v", reply.Items)
	}
	if len(item.Item) > freshstore.MaxAdoptionItemName() {
		t.Fatalf("the item identity exceeds the store's bound: %d", len(item.Item))
	}
	if !strings.HasPrefix(item.Item, "credential:"+overlong[:100]) {
		t.Fatalf("the item identity kept no readable prefix: %q", item.Item)
	}
	if !strings.HasSuffix(item.Item, "…") {
		t.Fatalf("the cut identity lost its ellipsis: %q", item.Item)
	}
	// The record's digest of the whole name is a digest of the unscrubbed name,
	// so the reply must not carry it.
	if strings.Contains(item.Item, overlongNameDigestMarker) {
		t.Fatalf("the reply carries the record's name digest: %q", item.Item)
	}
	// The identity is the record's, so a second read states the same item.
	if again := readAdoptionJSON(t, fixture.adoption(t)); again.Items[0].ID != reply.Items[0].ID {
		t.Fatalf("the item identity changed between two reads")
	}
}
