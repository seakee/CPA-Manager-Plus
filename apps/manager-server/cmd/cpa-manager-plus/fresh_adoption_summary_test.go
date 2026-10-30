package main

import (
	"encoding/base64"
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
		if item.Outcome == string(freshstore.AdoptionManual) && item.Reason == "" && item.Instruction == "" {
			t.Fatalf("a manual item carries neither reason nor instruction: %+v", item)
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
