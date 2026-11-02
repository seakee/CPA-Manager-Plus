package coverageincomplete

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

// openMarkDatabase opens a migrated Manager store: the marking table is created
// by the incremental migration, so a real store is the fixture a reader meets.
func openMarkDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := managersqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open the marking store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func dayStartMS(year int, month time.Month, day int) int64 {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC).UnixMilli()
}

// TestMarkKeepsTheFirstReasonAndInstant proves a day is marked once: a repeated
// reconciliation can neither rewrite the evidence nor duplicate it, which is
// what makes the recording idempotent.
func TestMarkKeepsTheFirstReasonAndInstant(t *testing.T) {
	ctx := context.Background()
	db := openMarkDatabase(t)
	repository := New(db)

	inserted, err := repository.Mark(ctx, []Day{
		{Day: " 2026-03-01 ", Reason: ReasonReconcileMismatch},
	}, 1_000)
	if err != nil {
		t.Fatalf("mark the day: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want the one new mark", inserted)
	}
	again, err := repository.Mark(ctx, []Day{
		{Day: "2026-03-01", Reason: ReasonProjectionLostBeforeV1146},
	}, 2_000)
	if err != nil {
		t.Fatalf("mark the day again: %v", err)
	}
	if again != 0 {
		t.Fatalf("inserted = %d, want no second mark for a marked day", again)
	}
	days, err := repository.DaysIn(ctx, 1, 1<<42)
	if err != nil {
		t.Fatalf("read the marks: %v", err)
	}
	if len(days) != 1 || days[0].Day != "2026-03-01" || days[0].Reason != ReasonReconcileMismatch {
		t.Fatalf("marks = %+v, want the normalized day under its first reason", days)
	}
	var recordedAtMS int64
	if err := db.QueryRowContext(ctx, `select recorded_at_ms from `+TableName+` where day = ?`,
		"2026-03-01").Scan(&recordedAtMS); err != nil {
		t.Fatalf("read the recorded instant: %v", err)
	}
	if recordedAtMS != 1_000 {
		t.Fatalf("recorded_at_ms = %d, want the first instant 1000", recordedAtMS)
	}
}

// TestMarkRefusesAnInvalidDayOrAMissingReason keeps the table's evidence
// trustworthy: a typo cannot mark another day, and a mark with no reason is not
// a record of anything.
func TestMarkRefusesAnInvalidDayOrAMissingReason(t *testing.T) {
	ctx := context.Background()
	db := openMarkDatabase(t)
	repository := New(db)
	cases := []struct {
		name string
		day  Day
	}{
		{name: "not a calendar day", day: Day{Day: "2026-02-30", Reason: ReasonReconcileMismatch}},
		{name: "not a day at all", day: Day{Day: "2026-03", Reason: ReasonReconcileMismatch}},
		{name: "empty day", day: Day{Day: "  ", Reason: ReasonReconcileMismatch}},
		{name: "no reason", day: Day{Day: "2026-03-01", Reason: " "}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := repository.Mark(ctx, []Day{testCase.day}, 1_000); err == nil {
				t.Fatalf("mark %+v was accepted", testCase.day)
			}
		})
	}
	days, err := repository.DaysIn(ctx, 1, 1<<42)
	if err != nil {
		t.Fatalf("read the marks: %v", err)
	}
	if len(days) != 0 {
		t.Fatalf("marks = %+v, want nothing written by a refused mark", days)
	}
}

// TestMarkTxRequiresTheInstantItWasRecorded keeps a caller from recording a day
// with no provenance instant.
func TestMarkTxRequiresTheInstantItWasRecorded(t *testing.T) {
	ctx := context.Background()
	db := openMarkDatabase(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := MarkTx(ctx, tx, []Day{{Day: "2026-03-01", Reason: ReasonReconcileMismatch}}, 0); err == nil {
		t.Fatal("a mark with no recorded instant was accepted")
	}
}

// TestDaysInReturnsTheSortedMarksInsideTheRange proves the window is
// half-open and chronological: a mark before the window and one at its end are
// not reported, and the marks inside it come back in day order.
func TestDaysInReturnsTheSortedMarksInsideTheRange(t *testing.T) {
	ctx := context.Background()
	db := openMarkDatabase(t)
	repository := New(db)
	if _, err := repository.Mark(ctx, []Day{
		{Day: "2026-03-03", Reason: ReasonDerivedRollupLost},
		{Day: "2026-03-01", Reason: ReasonReconcileMismatch},
		{Day: "2026-03-02", Reason: ReasonProjectionLostBeforeV1146},
	}, 1_000); err != nil {
		t.Fatalf("mark the days: %v", err)
	}

	days, err := repository.DaysIn(ctx, dayStartMS(2026, time.March, 2), dayStartMS(2026, time.March, 3))
	if err != nil {
		t.Fatalf("read the window: %v", err)
	}
	if len(days) != 1 || days[0].Day != "2026-03-02" {
		t.Fatalf("marks = %+v, want the single day inside the window", days)
	}
	all, err := repository.DaysIn(ctx, dayStartMS(2026, time.March, 1), dayStartMS(2026, time.March, 4))
	if err != nil {
		t.Fatalf("read every day: %v", err)
	}
	if len(all) != 3 || all[0].Day != "2026-03-01" || all[1].Day != "2026-03-02" || all[2].Day != "2026-03-03" {
		t.Fatalf("marks = %+v, want the three days in order", all)
	}
	empty, err := repository.DaysIn(ctx, dayStartMS(2026, time.March, 3), dayStartMS(2026, time.March, 3))
	if err != nil {
		t.Fatalf("read an empty window: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("marks = %+v, want an empty window to report nothing", empty)
	}
}

// TestDaysInWithoutTheTableReportsNoMarks covers a store that never ran an
// upgrade: the marking table does not exist, and that is "nothing was marked",
// not a read error.
func TestDaysInWithoutTheTableReportsNoMarks(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "bare.sqlite"))
	if err != nil {
		t.Fatalf("open a bare store: %v", err)
	}
	defer func() { _ = db.Close() }()

	days, err := New(db).DaysIn(ctx, dayStartMS(2026, time.March, 1), dayStartMS(2026, time.March, 2))
	if err != nil {
		t.Fatalf("read a store with no marking table: %v", err)
	}
	if len(days) != 0 {
		t.Fatalf("marks = %+v, want none", days)
	}
}

// TestParseDayNormalizesAndValidates covers the operator-facing parse: the day
// a mark is stored under is exactly the calendar day the operator named.
func TestParseDayNormalizesAndValidates(t *testing.T) {
	for _, value := range []string{"2026-03-01", " 2026-03-01 "} {
		day, err := ParseDay(value)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		if day != "2026-03-01" {
			t.Fatalf("parse %q = %q, want the calendar day", value, day)
		}
	}
	for _, value := range []string{"", "  ", "2026-13-01", "2026-02-30", "tomorrow", "2026/03/01", "2026-03-01T00:00:00Z"} {
		if _, err := ParseDay(value); err == nil {
			t.Fatalf("parse %q was accepted", value)
		}
	}
}

// TestDaysInKeepsMarksWithAnUpperBoundAtTheLimitOfInt64 reproduces review L4:
// the upper bound added a day to the window end, so DaysIn(1, MaxInt64)
// wrapped into a negative day, compared every mark against a day before the
// epoch, and reported none.
func TestDaysInKeepsMarksWithAnUpperBoundAtTheLimitOfInt64(t *testing.T) {
	ctx := context.Background()
	db := openMarkDatabase(t)
	repository := New(db)
	if _, err := repository.Mark(ctx, []Day{
		{Day: "2026-03-01", Reason: ReasonReconcileMismatch},
	}, 1_000); err != nil {
		t.Fatalf("mark the day: %v", err)
	}
	for _, upper := range []int64{1 << 42, math.MaxInt64 - utcDayMS, math.MaxInt64} {
		days, err := repository.DaysIn(ctx, 1, upper)
		if err != nil {
			t.Fatalf("read the window ending at %d: %v", upper, err)
		}
		if len(days) != 1 || days[0].Day != "2026-03-01" {
			t.Fatalf("marks in the window ending at %d = %+v, want the marked day", upper, days)
		}
	}
	// A window that starts after the last day the stored form can hold has no
	// mark in it, and says so instead of reading a wrapped label.
	days, err := repository.DaysIn(ctx, 1<<60, math.MaxInt64)
	if err != nil {
		t.Fatalf("read a window past the last stored day: %v", err)
	}
	if len(days) != 0 {
		t.Fatalf("marks = %+v, want none", days)
	}
	// A window that ends at the start of the seeded day excludes it: the upper
	// bound stays exclusive even when it is the largest day label.
	excluded, err := repository.DaysIn(ctx, 1, dayStartMS(2026, time.March, 1))
	if err != nil {
		t.Fatalf("read the window before the mark: %v", err)
	}
	if len(excluded) != 0 {
		t.Fatalf("marks = %+v, want the exclusive end to drop the day", excluded)
	}
}
