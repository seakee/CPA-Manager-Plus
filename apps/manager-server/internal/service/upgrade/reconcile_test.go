package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

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
	volume := newLegacyVolume(t)
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

// execTarget runs one statement against the copied v2 store. It opens the file
// directly instead of through the Manager migration, so a test edit never
// rebuilds a derived table and never changes what is being compared.
func execTarget(t *testing.T, path string, statements ...string) {
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
	if len(report.MismatchDays) != 0 || len(report.ParityGapDays) != 0 || len(report.IncompleteDays) != 0 {
		t.Fatalf("clean reconcile reported %+v, want no gaps", report.ReconcileEvidence)
	}
	if !report.Recorded || report.Stage != model.UpgradeStageReconcile {
		t.Fatalf("report = %+v, want recorded at %q", report, model.UpgradeStageReconcile)
	}
	for _, domain := range reconciledDomains {
		if !slicesContain(report.DomainsDone, domain) {
			t.Fatalf("domains done = %v, want %s", report.DomainsDone, domain)
		}
	}
	if len(report.Domains) != 4 {
		t.Fatalf("report domains = %d, want the four compared domains", len(report.Domains))
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
