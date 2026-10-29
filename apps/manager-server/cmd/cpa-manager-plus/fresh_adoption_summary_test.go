package main

import (
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
	request := httptest.NewRequest(http.MethodGet, "/control/api/v2/installation/adoption", nil)
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	f.app.canonicalAPI(response, request)
	return response
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
