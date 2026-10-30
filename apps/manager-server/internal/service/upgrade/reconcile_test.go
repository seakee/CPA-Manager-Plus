package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/coverageincomplete"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
)

// copiedVolume is a 1.x source that already went through the copy-in: the v2
// data root holds the migrated store the recorded operation describes.
type copiedVolume struct {
	volume  legacyVolume
	target  string
	options Options
}

func newCopiedVolume(t *testing.T) copiedVolume {
	t.Helper()
	return newCopiedVolumeWith(t, func(*testing.T, legacyVolume) {})
}

// newCopiedVolumeWith seeds the stopped source before the copy-in and then
// copies it in. The seed has to run first: the reconciliation refuses a source
// that is not the volume the recorded copy-in copied.
func newCopiedVolumeWith(t *testing.T, seed func(*testing.T, legacyVolume)) copiedVolume {
	t.Helper()
	volume := newLegacyVolume(t)
	seed(t, volume)
	return copyVolume(t, volume)
}

// copyVolume copies an already prepared 1.x volume into a fresh v2 data root.
func copyVolume(t *testing.T, volume legacyVolume) copiedVolume {
	t.Helper()
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	if _, err := CopyIn(context.Background(), options); err != nil {
		t.Fatalf("copy in: %v", err)
	}
	return copiedVolume{volume: volume, target: targetRoot, options: options}
}

// reconcileOptions is the copy-in options plus what this step needs: the same
// source and target, and no acknowledgement yet.
func (c copiedVolume) reconcileOptions() Options {
	return Options{LegacyRoot: c.volume.root, TargetRoot: c.target, BundleDir: c.options.BundleDir}
}

func (c copiedVolume) databasePath() string { return filepath.Join(c.target, databaseFileName) }

// execDatabase runs one statement at a time against a database file. It opens
// the file directly instead of through the Manager migration, so a test edit
// never rebuilds a derived table and never changes what is being compared.
func execDatabase(t *testing.T, path string, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("execute %q: %v", statement, err)
		}
	}
	if _, err := db.Exec(`pragma wal_checkpoint(truncate)`); err != nil {
		t.Fatalf("checkpoint %s: %v", path, err)
	}
}

// execTarget runs statements against the copied v2 store.
func execTarget(t *testing.T, path string, statements ...string) {
	t.Helper()
	execDatabase(t, path, statements...)
}

// execSource runs statements against the stopped 1.x source. Only fixture
// preparation writes there; the reconciliation never does.
func execSource(t *testing.T, volume legacyVolume, statements ...string) {
	t.Helper()
	execDatabase(t, volume.databasePath(), statements...)
}

func queryTargetInt(t *testing.T, path, query string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	var value int
	if err := db.QueryRow(query).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

func operationRow(t *testing.T, path string) model.UpgradeOperation {
	t.Helper()
	recorded, found, err := recordedOperation(context.Background(), path, true)
	if err != nil || !found {
		if err != nil {
			t.Fatalf("read the recorded operation: %v", err)
		}
		t.Fatal("no recorded operation")
	}
	return recorded
}

// markedDays reads the durable marks, indexed by day.
func markedDays(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = db.Close() }()
	// The bound only has to be a real instant after any mark this store can
	// hold; an extreme value overflows the day arithmetic.
	days, err := coverageincomplete.New(db).DaysIn(context.Background(), 0, 1<<40)
	if err != nil {
		t.Fatalf("read the marked days: %v", err)
	}
	index := make(map[string]string, len(days))
	for _, day := range days {
		index[day.Day] = day.Reason
	}
	return index
}

func mismatchesOf(t *testing.T, report ReconcileReport, domain string) []ReconcileMismatch {
	t.Helper()
	for _, entry := range report.Domains {
		if entry.Domain == domain {
			return entry.Mismatches
		}
	}
	t.Fatalf("report holds no %q domain: %+v", domain, report.Domains)
	return nil
}

func TestReconcileCleanCopyRecordsTheStageAndMarksNothing(t *testing.T) {
	copied := newCopiedVolume(t)
	sourceBefore := fingerprintTree(t, copied.volume.root)

	report, err := Reconcile(context.Background(), copied.reconcileOptions())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// The copy of the 1.x database is scratch: it never survives the step.
	assertNoScratch(t, copied.target)
	if len(report.MismatchDays) != 0 || len(report.ParityGapDays) != 0 || len(report.IncompleteDays) != 0 {
		t.Fatalf("clean reconcile reported %+v, want no gaps", report.ReconcileEvidence)
	}
	if !report.Recorded || report.Stage != model.UpgradeStageReconcile {
		t.Fatalf("report = %+v, want recorded at %q", report, model.UpgradeStageReconcile)
	}
	if len(report.Domains) != 4 {
		t.Fatalf("report domains = %d, want the four compared domains", len(report.Domains))
	}
	// Every domain was applicable, so every operation domain is recorded; a
	// domain that could not be compared must never appear here.
	wantDomains := []string{
		model.UpgradeDomainRetainedProjection,
		model.UpgradeDomainPricing,
		model.UpgradeDomainCoverage,
	}
	for _, domain := range wantDomains {
		if !slicesContain(report.DomainsDone, domain) {
			t.Fatalf("domains done = %v, want %s", report.DomainsDone, domain)
		}
	}
	for _, domain := range report.DomainsDone {
		if domain == model.UpgradeDomainSourceCopy || slicesContain(wantDomains, domain) {
			continue
		}
		t.Fatalf("domains done = %v, want only copy-in and the compared domains", report.DomainsDone)
	}
	if got := markedDays(t, copied.databasePath()); len(got) != 0 {
		t.Fatalf("marked days = %v, want none", got)
	}
	assertTreeUnchanged(t, "1.x source", sourceBefore, fingerprintTree(t, copied.volume.root))

	again, err := Reconcile(context.Background(), copied.reconcileOptions())
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if again.NewlyMarkedDays != 0 || again.Stage != model.UpgradeStageReconcile {
		t.Fatalf("second reconcile = %+v, want the same state and no new mark", again)
	}
	if len(again.MismatchDays) != 0 || len(again.Domains) != len(report.Domains) {
		t.Fatalf("second reconcile report = %+v, want the first report", again.ReconcileEvidence)
	}
}

func TestReconcileBlocksOnADisagreeingDayAndMarksNothing(t *testing.T) {
	copied := newCopiedVolume(t)
	execTarget(t, copied.databasePath(),
		`update usage_events set total_tokens = total_tokens + 1 where event_hash = 'legacy-event-1'`)

	report, err := Reconcile(context.Background(), copied.reconcileOptions())
	if !errors.Is(err, ErrReconcileMismatch) {
		t.Fatalf("reconcile error = %v, want the typed mismatch", err)
	}
	if report.Recorded {
		t.Fatal("blocked reconcile recorded the operation")
	}
	mismatches := mismatchesOf(t, report, ReconcileDomainRetained)
	if len(mismatches) != 1 || mismatches[0].Field != mismatchFieldTokens || mismatches[0].Day != "1970-01-01" {
		t.Fatalf("retained mismatches = %+v, want one total_tokens mismatch on 1970-01-01", mismatches)
	}
	if mismatches[0].Source == mismatches[0].Target {
		t.Fatalf("mismatch = %+v, want two different values", mismatches[0])
	}
	if len(report.UnacknowledgedDays) != 1 || report.UnacknowledgedDays[0] != "1970-01-01" {
		t.Fatalf("unacknowledged days = %v, want the disagreeing day", report.UnacknowledgedDays)
	}
	if got := markedDays(t, copied.databasePath()); len(got) != 0 {
		t.Fatalf("marked days = %v, want none after a blocked run", got)
	}
	if recorded := operationRow(t, copied.databasePath()); recorded.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("stage = %q, want %q", recorded.Stage, model.UpgradeStageCopyIn)
	}
	// A failed comparison removes its copy of the 1.x database as well.
	assertNoScratch(t, copied.target)
}

func TestReconcileMarksAnAcknowledgedMismatchOnce(t *testing.T) {
	copied := newCopiedVolume(t)
	execTarget(t, copied.databasePath(),
		`update usage_events set total_tokens = total_tokens + 1 where event_hash = 'legacy-event-1'`)
	options := copied.reconcileOptions()
	options.Acknowledged = []string{"1970-01-01"}

	report, err := Reconcile(context.Background(), options)
	if err != nil {
		t.Fatalf("acknowledged reconcile: %v", err)
	}
	if len(report.IncompleteDays) != 1 || report.IncompleteDays[0].Reason != coverageincomplete.ReasonReconcileMismatch {
		t.Fatalf("incomplete days = %+v, want the disagreeing day as %q",
			report.IncompleteDays, coverageincomplete.ReasonReconcileMismatch)
	}
	if report.NewlyMarkedDays != 1 || report.Stage != model.UpgradeStageReconcile {
		t.Fatalf("report = %+v, want one new mark at %q", report, model.UpgradeStageReconcile)
	}
	if got := markedDays(t, copied.databasePath()); len(got) != 1 ||
		got["1970-01-01"] != coverageincomplete.ReasonReconcileMismatch {
		t.Fatalf("marked days = %v, want one %q mark", got, coverageincomplete.ReasonReconcileMismatch)
	}

	again, err := Reconcile(context.Background(), options)
	if err != nil {
		t.Fatalf("second acknowledged reconcile: %v", err)
	}
	if again.NewlyMarkedDays != 0 {
		t.Fatalf("second run marked %d more days, want 0", again.NewlyMarkedDays)
	}
	if len(again.IncompleteDays) != 1 {
		t.Fatalf("second run reported %+v, want the same accepted day", again.IncompleteDays)
	}
	if got := queryTargetInt(t, copied.databasePath(),
		`select count(*) from usage_coverage_incomplete_days`); got != 1 {
		t.Fatalf("marked rows = %d, want 1", got)
	}
}

func TestReconcileRequiresAcknowledgementForAParityGap(t *testing.T) {
	volume := newLegacyVolume(t)
	// The gap has to exist in the source before the copy-in: reconciliation
	// refuses a source whose fingerprint is not the one the copy-in recorded.
	seedParityGap(t, volume)
	targetRoot := canonicalTempDir(t)
	options := copyInOptions(volume, targetRoot)
	options.Lock = volume.lock(t)
	if _, err := CopyIn(context.Background(), options); err != nil {
		t.Fatalf("copy in: %v", err)
	}
	copied := copiedVolume{volume: volume, target: targetRoot, options: options}

	report, err := Reconcile(context.Background(), copied.reconcileOptions())
	if !errors.Is(err, ErrReconcileMismatch) {
		t.Fatalf("reconcile error = %v, want the typed mismatch", err)
	}
	if len(report.ParityGapDays) != 1 || report.ParityGapDays[0] != "1970-01-01" {
		t.Fatalf("parity gap days = %v, want 1970-01-01", report.ParityGapDays)
	}
	if len(report.MismatchDays) != 0 {
		t.Fatalf("mismatch days = %v, want none: a lost projection is not a measured disagreement", report.MismatchDays)
	}
	if got := markedDays(t, copied.databasePath()); len(got) != 0 {
		t.Fatalf("marked days = %v, want none before acknowledgement", got)
	}

	gapOptions := copied.reconcileOptions()
	gapOptions.AcknowledgeParityGaps = true
	acknowledged, err := Reconcile(context.Background(), gapOptions)
	if err != nil {
		t.Fatalf("reconcile with parity gaps acknowledged: %v", err)
	}
	if len(acknowledged.IncompleteDays) != 1 ||
		acknowledged.IncompleteDays[0].Reason != coverageincomplete.ReasonProjectionLostBeforeV1146 {
		t.Fatalf("incomplete days = %+v, want the gap day as %q",
			acknowledged.IncompleteDays, coverageincomplete.ReasonProjectionLostBeforeV1146)
	}
	if got := markedDays(t, copied.databasePath()); len(got) != 1 ||
		got["1970-01-01"] != coverageincomplete.ReasonProjectionLostBeforeV1146 {
		t.Fatalf("marked days = %v, want one %q mark", got, coverageincomplete.ReasonProjectionLostBeforeV1146)
	}
}

func TestReconcileReportsADayOnlyOneSideHolds(t *testing.T) {
	copied := newCopiedVolume(t)
	execTarget(t, copied.databasePath(), `insert into usage_events
		(event_hash, timestamp_ms, timestamp, model, requested_model, created_at_ms)
		values ('target-only-event', 86400000, '86400000', 'gpt-test', 'gpt-test', 86400000)`)

	report, err := Reconcile(context.Background(), copied.reconcileOptions())
	if !errors.Is(err, ErrReconcileMismatch) {
		t.Fatalf("reconcile error = %v, want the typed mismatch", err)
	}
	mismatches := mismatchesOf(t, report, ReconcileDomainRetained)
	if len(mismatches) != 1 || mismatches[0].Field != mismatchFieldPresence ||
		mismatches[0].Day != "1970-01-02" {
		t.Fatalf("retained mismatches = %+v, want one presence mismatch on 1970-01-02", mismatches)
	}
	if mismatches[0].Source != presenceValue(false) || mismatches[0].Target != presenceValue(true) {
		t.Fatalf("presence mismatch = %+v, want absent in the source only", mismatches[0])
	}
	if got := markedDays(t, copied.databasePath()); len(got) != 0 {
		t.Fatalf("marked days = %v, want none", got)
	}
}

func TestReconcileRefusesAStoreThatCanNoLongerEnterTheStep(t *testing.T) {
	copied := newCopiedVolume(t)
	// A store the operation already carried past "reconcile": comparing it would
	// mark days for numbers that have grown since the copy.
	db, err := managersqlite.Open(copied.databasePath())
	if err != nil {
		t.Fatalf("open the copied store: %v", err)
	}
	if _, err := upgradeoperation.New(db).Advance(context.Background(),
		model.UpgradeDomainCPAConfig, model.UpgradeStageCPAImport); err != nil {
		_ = db.Close()
		t.Fatalf("advance past the reconcile step: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the copied store: %v", err)
	}
	before := fingerprintTree(t, copied.target)

	report, err := Reconcile(context.Background(), copied.reconcileOptions())
	if err == nil {
		t.Fatalf("reconcile = %+v, want a refusal for a closed stage", report)
	}
	if report.Recorded {
		t.Fatal("a refused reconcile recorded the operation")
	}
	if got := markedDays(t, copied.databasePath()); len(got) != 0 {
		t.Fatalf("marked days = %v, want none", got)
	}
	if recorded := operationRow(t, copied.databasePath()); recorded.Stage != model.UpgradeStageCPAImport {
		t.Fatalf("stage = %q, want the untouched %q", recorded.Stage, model.UpgradeStageCPAImport)
	}
	// The scratch area is the only thing this step may leave behind, and it is
	// created after the gate; the store itself must be byte-identical.
	if after := fingerprintTree(t, copied.target); !reflect.DeepEqual(before, after) {
		assertTreeUnchanged(t, "v2 data root", before, after)
	}
}

func TestReconcileRefusesAnAcknowledgementThatIsNotADay(t *testing.T) {
	copied := newCopiedVolume(t)
	options := copied.reconcileOptions()
	options.Acknowledged = []string{"1970-13-40"}

	if _, err := Reconcile(context.Background(), options); !errors.Is(err, ErrUsage) {
		t.Fatalf("reconcile error = %v, want the usage error", err)
	}
	if got := markedDays(t, copied.databasePath()); len(got) != 0 {
		t.Fatalf("marked days = %v, want none", got)
	}
}

func TestReconcileRefusesASourceThatIsNotTheCopiedOne(t *testing.T) {
	copied := newCopiedVolume(t)
	sourceDatabase := copied.volume.databasePath()
	db, err := sql.Open("sqlite", sourceDatabase)
	if err != nil {
		t.Fatalf("open the 1.x source: %v", err)
	}
	if _, err := db.Exec(`insert into settings (key, value, updated_at_ms) values ('drifted', '1', 1)`); err != nil {
		_ = db.Close()
		t.Fatalf("drift the 1.x source: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the 1.x source: %v", err)
	}

	if _, err := Reconcile(context.Background(), copied.reconcileOptions()); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("reconcile error = %v, want the source-changed error", err)
	}
	if got := markedDays(t, copied.databasePath()); len(got) != 0 {
		t.Fatalf("marked days = %v, want none", got)
	}
}

// seedParityGap records a day whose deleted coverage claims events the archive
// refs no longer prove: one of the two inputs the parity detection compares.
// The rows carry no archive run, so the copy-in bundle keeps verifying; the gap
// is a 1.x volume that already lost the evidence, not an archive it can prove.
func seedParityGap(t *testing.T, volume legacyVolume) {
	t.Helper()
	db, err := sql.Open("sqlite", volume.databasePath())
	if err != nil {
		t.Fatalf("open the 1.x source: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`insert into usage_archive_deleted_coverage_daily (
		utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms
	) values (0, 3, 1000, 1000)`); err != nil {
		t.Fatalf("seed the parity gap: %v", err)
	}
	if _, err := db.Exec(`pragma wal_checkpoint(truncate)`); err != nil {
		t.Fatalf("checkpoint the 1.x source: %v", err)
	}
}

// The seeded usage domains live on one UTC day with two hours, so a mutation can
// move a row inside the day without moving it out of it.
var (
	seededDayMS   = time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	seededHourMS  = time.Date(2026, time.March, 1, 5, 0, 0, 0, time.UTC).UnixMilli()
	seededDayName = "2026-03-01"
	// seededDayIndex is the utc_day key the deleted coverage table stores.
	seededDayIndex = seededDayMS / utcDayMilliseconds
)

func seedSourcePricing(t *testing.T, volume legacyVolume) {
	t.Helper()
	execSource(t, volume,
		fmt.Sprintf(`insert into usage_pricing_hourly_rollups_v1 (
			structure_revision, bucket_ms, model, billing_model, pricing_model, service_tier,
			context_threshold_tokens, failed, calls, input_tokens, total_tokens, updated_at_ms)
			values ('v1', %d, 'gpt-test', 'gpt-test', 'gpt-test', 'default', 0, 0, 2, 400000, 400000, 1)`,
			seededHourMS),
		fmt.Sprintf(`insert into usage_pricing_hourly_rollups_v1 (
			structure_revision, bucket_ms, model, billing_model, pricing_model, service_tier,
			context_threshold_tokens, failed, calls, input_tokens, total_tokens, updated_at_ms)
			values ('v1', %d, 'gpt-test', 'gpt-test', 'gpt-test', 'default', 0, 1, 5, 100000, 100000, 1)`,
			seededHourMS),
		fmt.Sprintf(`insert into usage_pricing_hourly_rollups_v1 (
			structure_revision, bucket_ms, model, billing_model, pricing_model, service_tier,
			context_threshold_tokens, failed, calls, input_tokens, total_tokens, updated_at_ms)
			values ('v1', %d, 'gpt-test', 'gpt-test', 'gpt-test', 'default', 0, 0, 3, 200000, 200000, 1)`,
			seededHourMS+time.Hour.Milliseconds()),
	)
}

func seedSourceCoverage(t *testing.T, volume legacyVolume) {
	t.Helper()
	execSource(t, volume, fmt.Sprintf(`insert into usage_archive_deleted_coverage_daily (
		utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms
	) values (%d, 4, %d, %d)`, seededDayIndex, seededDayMS+1000, seededDayMS+2000))
}

func seedSourceHistory(t *testing.T, volume legacyVolume) {
	t.Helper()
	execSource(t, volume, fmt.Sprintf(`insert into usage_monitoring_account_daily_rollups_v1 (
		structure_revision, bucket_ms, account_snapshot, auth_label_snapshot, provider,
		auth_provider_snapshot, auth_index, source, source_hash, auth_file_snapshot,
		api_key_hash, executor_type, model, billing_model, pricing_model, service_tier,
		context_threshold_tokens, failed, calls, total_tokens, last_seen_ms, updated_at_ms)
		values ('v1', %d, 'acct-1', 'label-1', 'openai', 'openai-auth', '0', 'source-a', 'hash-a',
			'file-a', 'key-a', 'codex', 'gpt-test', 'gpt-test', 'gpt-test', 'default',
			0, 0, 2, 100, %d, 1)`, seededDayMS, seededDayMS))
}

// seedSourceRetention adds two events in the same hour and model under two API
// keys with different token totals, so a key swap inside the hour changes what
// each key totals without changing the hour.
func seedSourceRetention(t *testing.T, volume legacyVolume) {
	t.Helper()
	execSource(t, volume,
		fmt.Sprintf(`insert into usage_events (
			event_hash, timestamp_ms, timestamp, model, requested_model, api_key_hash, total_tokens, created_at_ms)
			values ('legacy-key-a', %d, '%d', 'gpt-test', 'gpt-test', 'key-a', 10, %d)`,
			seededHourMS+1000, seededHourMS+1000, seededHourMS),
		fmt.Sprintf(`insert into usage_events (
			event_hash, timestamp_ms, timestamp, model, requested_model, api_key_hash, total_tokens, created_at_ms)
			values ('legacy-key-b', %d, '%d', 'gpt-test', 'gpt-test', 'key-b', 20, %d)`,
			seededHourMS+2000, seededHourMS+2000, seededHourMS),
	)
}

// assertNoScratch proves the reconciliation copy of the 1.x database is gone.
func assertNoScratch(t *testing.T, targetRoot string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(targetRoot, reconcileScratchDirectory)); !os.IsNotExist(err) {
		t.Fatalf("the reconciliation scratch area survived: %v", err)
	}
}

// TestReconcileBlocksWhenTheStoreLosesASourceTable reproduces the review's H1:
// the store dropped a priced table while the source holds rows of it. That is
// lost data, so nothing may be certified, marked or recorded.
func TestReconcileBlocksWhenTheStoreLosesASourceTable(t *testing.T) {
	copied := newCopiedVolumeWith(t, seedSourcePricing)
	execTarget(t, copied.databasePath(), `drop table usage_pricing_hourly_rollups_v1`)

	report, err := Reconcile(context.Background(), copied.reconcileOptions())
	if !errors.Is(err, ErrReconcileTargetIncomplete) {
		t.Fatalf("reconcile error = %v, want the typed incomplete target", err)
	}
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorReconcileTargetIncomplete {
		t.Fatalf("error code = %q (%t), want %q", code, ok, UpgradeErrorReconcileTargetIncomplete)
	}
	if message := err.Error(); !strings.Contains(message, `"pricing"`) ||
		!strings.Contains(message, "usage_pricing_hourly_rollups_v1") {
		t.Fatalf("error %q does not name the domain and the table", message)
	}
	if report.Recorded {
		t.Fatal("a blocked reconcile recorded the operation")
	}
	if len(report.DomainsDone) != 0 {
		t.Fatalf("domains done = %v, want none: nothing was compared", report.DomainsDone)
	}
	pricing := mismatchesOf(t, report, ReconcileDomainPricing)
	if len(pricing) != 0 {
		t.Fatalf("pricing mismatches = %+v, want none: a lost table is not a disagreement", pricing)
	}
	for _, domain := range report.Domains {
		if domain.Domain != ReconcileDomainPricing {
			continue
		}
		if domain.Applicable || domain.Table != "usage_pricing_hourly_rollups_v1" || domain.SourceDays != 1 {
			t.Fatalf("pricing report = %+v, want an inapplicable domain with one source day", domain)
		}
	}
	if got := markedDays(t, copied.databasePath()); len(got) != 0 {
		t.Fatalf("marked days = %v, want none", got)
	}
	if recorded := operationRow(t, copied.databasePath()); recorded.Stage != model.UpgradeStageCopyIn ||
		slicesContain(recorded.DomainsDone, model.UpgradeDomainPricing) {
		t.Fatalf("recorded operation = %+v, want the untouched copy-in stage", recorded)
	}
	assertNoScratch(t, copied.target)
}

// TestReconcileDoesNotRecordADomainTheSourceDoesNotHold keeps the other half of
// H1: a domain the 1.x volume never had was not compared, so this step must not
// claim it.
func TestReconcileDoesNotRecordADomainTheSourceDoesNotHold(t *testing.T) {
	volume := newLegacyVolume(t)
	execSource(t, volume, `drop table usage_archive_deleted_coverage_daily`)
	copied := copyVolume(t, volume)

	report, err := Reconcile(context.Background(), copied.reconcileOptions())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if slicesContain(report.DomainsDone, model.UpgradeDomainCoverage) {
		t.Fatalf("domains done = %v, want no coverage: the source holds no coverage table", report.DomainsDone)
	}
	if !slicesContain(report.DomainsDone, model.UpgradeDomainPricing) ||
		!slicesContain(report.DomainsDone, model.UpgradeDomainRetainedProjection) {
		t.Fatalf("domains done = %v, want the compared domains", report.DomainsDone)
	}
	for _, domain := range report.Domains {
		if domain.Domain != ReconcileDomainCoverage {
			continue
		}
		if domain.Applicable || domain.SourceDays != 0 {
			t.Fatalf("coverage report = %+v, want an inapplicable domain with no source day", domain)
		}
	}
	assertNoScratch(t, copied.target)
}

// wantedMismatch is one mismatch a mutation must produce: the column that
// disagrees and a substring of the row it disagrees on.
type wantedMismatch struct {
	field     string
	dimension string
}

// TestReconcileReportsEveryPermutationInsideADay reproduces the review's M1:
// every one of these mutations was certified clean by the day totals.
func TestReconcileReportsEveryPermutationInsideADay(t *testing.T) {
	cases := []struct {
		name   string
		seed   func(*testing.T, legacyVolume)
		mutate []string
		domain string
		want   []wantedMismatch
	}{
		{
			name:   "pricing hour bucket moved inside the day",
			seed:   seedSourcePricing,
			mutate: []string{`update usage_pricing_hourly_rollups_v1 set bucket_ms = bucket_ms + 3600000 where failed = 1`},
			domain: ReconcileDomainPricing,
			want: []wantedMismatch{
				{mismatchFieldBucketMS, seededHourName()},
				{mismatchFieldBucketMS, utcHourLabel(seededHourMS + time.Hour.Milliseconds())},
			},
		},
		{
			name: "pricing calls swapped between the failed rows",
			seed: seedSourcePricing,
			mutate: []string{
				fmt.Sprintf(`update usage_pricing_hourly_rollups_v1 set calls = 5 where failed = 0 and bucket_ms = %d`, seededHourMS),
				fmt.Sprintf(`update usage_pricing_hourly_rollups_v1 set calls = 2 where failed = 1 and bucket_ms = %d`, seededHourMS),
			},
			domain: ReconcileDomainPricing,
			want: []wantedMismatch{
				{mismatchFieldCalls, "failed=0"},
				{mismatchFieldCalls, "failed=1"},
			},
		},
		{
			name:   "coverage edges rewritten",
			seed:   seedSourceCoverage,
			mutate: []string{`update usage_archive_deleted_coverage_daily set min_timestamp_ms = 500, max_timestamp_ms = 2500`},
			domain: ReconcileDomainCoverage,
			want: []wantedMismatch{
				{mismatchFieldMinTimestamp, ""},
				{mismatchFieldMaxTimestamp, ""},
			},
		},
		{
			name: "history identity rewritten",
			seed: seedSourceHistory,
			mutate: []string{`update usage_monitoring_account_daily_rollups_v1
				set provider = 'other', api_key_hash = 'key-z', model = 'gpt-other'`},
			domain: ReconcileDomainHistory,
			want: []wantedMismatch{
				{mismatchFieldProvider, "other"},
				{mismatchFieldAPIKeyHash, "key-z"},
				{mismatchFieldModel, "gpt-other"},
			},
		},
		{
			name: "retained api_key_hash swapped between events",
			seed: seedSourceRetention,
			mutate: []string{`update usage_events
				set api_key_hash = case api_key_hash when 'key-a' then 'key-b' else 'key-a' end
				where event_hash in ('legacy-key-a', 'legacy-key-b')`},
			domain: ReconcileDomainRetained,
			want: []wantedMismatch{
				{mismatchFieldTokens, "key=key-a"},
				{mismatchFieldTokens, "key=key-b"},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			copied := newCopiedVolumeWith(t, testCase.seed)
			execTarget(t, copied.databasePath(), testCase.mutate...)

			report, err := Reconcile(context.Background(), copied.reconcileOptions())
			if !errors.Is(err, ErrReconcileMismatch) {
				t.Fatalf("reconcile error = %v, want the typed mismatch", err)
			}
			if report.Recorded {
				t.Fatal("a mismatched reconcile recorded the operation")
			}
			mismatches := mismatchesOf(t, report, testCase.domain)
			for _, wanted := range testCase.want {
				if !holdsMismatch(mismatches, wanted) {
					t.Fatalf("no %q mismatch naming %q in %+v", wanted.field, wanted.dimension, mismatches)
				}
			}
			for _, mismatch := range mismatches {
				if mismatch.Day == "" || mismatch.Field == "" || mismatch.Source == "" || mismatch.Target == "" {
					t.Fatalf("mismatch = %+v, want a day, a field and both values", mismatch)
				}
			}
			if got := markedDays(t, copied.databasePath()); len(got) != 0 {
				t.Fatalf("marked days = %v, want none", got)
			}
			assertNoScratch(t, copied.target)
		})
	}
}

// holdsMismatch reports whether one mismatch carries the wanted column on the
// wanted row of the seeded day, with two different values.
func holdsMismatch(mismatches []ReconcileMismatch, wanted wantedMismatch) bool {
	for _, mismatch := range mismatches {
		if mismatch.Day != seededDayName || mismatch.Field != wanted.field ||
			mismatch.Source == mismatch.Target {
			continue
		}
		if strings.Contains(mismatch.Dimension, wanted.dimension) {
			return true
		}
	}
	return false
}

// seededHourName is the hour label the seeded bucket is reported under.
func seededHourName() string { return utcHourLabel(seededHourMS) }

// TestReconcileTreatsAOneMicroCostDifferenceAsAMismatch reproduces the review's
// M2 directly: identical inputs produce identical integer micro sums, so one
// micro is a disagreement and no relative tolerance may hide it.
func TestReconcileTreatsAOneMicroCostDifferenceAsAMismatch(t *testing.T) {
	key := pricingCellKey{
		hourMS: seededHourMS, model: "gpt-test", billing: "gpt-test", pricing: "gpt-test", tier: "default",
	}
	tables := sourceSchema{tables: map[string]bool{"usage_pricing_hourly_rollups_v1": true}}
	source := sideObservation{tables: tables, pricing: map[pricingCellKey]pricingCell{
		key: {calls: 1, tokens: 400000, input: 400000, costMicros: 3_000_000_000},
	}}
	target := sideObservation{tables: tables, pricing: map[pricingCellKey]pricingCell{
		key: {calls: 1, tokens: 400000, input: 400000, costMicros: 3_000_000_001},
	}}

	report := comparePricingDomain(source, target)
	if len(report.Mismatches) != 1 {
		t.Fatalf("mismatches = %+v, want exactly the one-micro cost difference", report.Mismatches)
	}
	mismatch := report.Mismatches[0]
	if mismatch.Field != mismatchFieldCost || mismatch.Day != seededDayName {
		t.Fatalf("mismatch = %+v, want the cost on %s", mismatch, seededDayName)
	}
	if mismatch.Source != "3000000000" || mismatch.Target != "3000000001" {
		t.Fatalf("mismatch = %+v, want the exact micro totals", mismatch)
	}
}

// TestReconcileSeesAPriceChangeOfOneMicroPerMillion reproduces the review's M2
// end to end: a $3,000 day whose price moved by 0.000001 dollars per million
// tokens was inside the old relative tolerance.
func TestReconcileSeesAPriceChangeOfOneMicroPerMillion(t *testing.T) {
	copied := newCopiedVolumeWith(t, func(t *testing.T, volume legacyVolume) {
		seedSourcePricing(t, volume)
		execSource(t, volume,
			`insert into model_prices (model, prompt_per_1m, completion_per_1m, cache_per_1m, updated_at_ms)
				values ('gpt-test', 3.0, 0, 0, 1)`,
			fmt.Sprintf(`update usage_pricing_hourly_rollups_v1
				set input_tokens = 1000000000, total_tokens = 1000000000
				where failed = 0 and bucket_ms = %d`, seededHourMS),
		)
	})
	execTarget(t, copied.databasePath(),
		`update model_prices set prompt_per_1m = 3.000001 where model = 'gpt-test'`)

	report, err := Reconcile(context.Background(), copied.reconcileOptions())
	if !errors.Is(err, ErrReconcileMismatch) {
		t.Fatalf("reconcile error = %v, want the typed mismatch", err)
	}
	costs := []ReconcileMismatch{}
	for _, mismatch := range mismatchesOf(t, report, ReconcileDomainPricing) {
		if mismatch.Field == mismatchFieldCost {
			costs = append(costs, mismatch)
		}
	}
	if len(costs) != 1 {
		t.Fatalf("cost mismatches = %+v, want the one priced day", costs)
	}
	if costs[0].Source != "3000000000" || costs[0].Target != "3000001000" {
		t.Fatalf("cost mismatch = %+v, want the 1000-micro difference", costs[0])
	}
	if got := markedDays(t, copied.databasePath()); len(got) != 0 {
		t.Fatalf("marked days = %v, want none", got)
	}
	assertNoScratch(t, copied.target)
}
