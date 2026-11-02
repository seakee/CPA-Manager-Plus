// Package coverageincomplete persists the UTC days a v2 Upgrade permanently
// marked incomplete.
//
// A day lands here only when the operator acknowledged that the 1.x source can
// never be read again for it: a raw delete before v1.14.6 removed the event
// without a retained projection row, or a reconciled measure disagreed and the
// operator accepted the discrepancy. The mark is durable evidence, never a
// repair: no reader reconstructs the missing numbers and no writer deletes a
// mark, so a marked day stays a lower bound for the life of the store.
package coverageincomplete

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// TableName is the marking table. It is created by the incremental migration
// and is deliberately not a derived table: a revision rebuild must never drop
// it.
const TableName = "usage_coverage_incomplete_days"

// Reasons. A stored reason is a stable code; the UI owns its wording.
const (
	// ReasonProjectionLostBeforeV1146 marks a day whose raw delete before
	// v1.14.6 removed the event while the retained projection was missing or
	// stale, so the day can never be reconstructed from the source.
	ReasonProjectionLostBeforeV1146 = "projection_lost_before_v1_14_6"
	// ReasonReconcileMismatch marks a day whose reconciled measures disagreed
	// between the 1.x source and the copied-in store.
	ReasonReconcileMismatch = "reconcile_mismatch"
	// ReasonDerivedRollupLost marks a day whose derived rollup the store can no
	// longer show: the rollup table is there, but it holds no row the day's
	// source rows landed in. The rows are derivable from the retained event log,
	// so the loss is a gap the operator may acknowledge instead of a store that
	// cannot be trusted at all; nothing in this release rebuilds them.
	ReasonDerivedRollupLost = "derived_rollup_lost"
)

// SourceUpgradeReconcile is the only writer of a mark today.
const SourceUpgradeReconcile = "upgrade_reconcile"

// dayLayout is the stored day format. Days sort chronologically as text, so a
// range read is a plain string comparison and needs no parsing.
const dayLayout = "2006-01-02"

const utcDayMS = int64(24 * 60 * 60 * 1000)

// The day indexes the stored text form can hold. Below the first, a rendered
// day would carry a negative year; above the last, a five-digit one, and both
// sort wrongly against every stored YYYY-MM-DD day.
const (
	minStoredDayIndex = int64(-719162) // 0001-01-01
	maxStoredDayIndex = int64(2932896) // 9999-12-31
)

// Day is one marked UTC day.
type Day struct {
	Day    string `json:"day"`
	Reason string `json:"reason"`
}

// Queryer is the narrow read surface a caller inside its own transaction
// provides, so a coverage read can see the marks in the same snapshot as the
// numbers they describe.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Repository reads and writes the marking table.
type Repository struct{ db *sql.DB }

func New(db *sql.DB) *Repository { return &Repository{db: db} }

// ParseDay validates one operator-supplied day and returns it normalized.
// Anything but a calendar day in YYYY-MM-DD form is refused, so a typo can never
// silently acknowledge a different day.
func ParseDay(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("an incomplete day must be a calendar day in %s form", dayLayout)
	}
	parsed, err := time.Parse(dayLayout, trimmed)
	if err != nil {
		return "", fmt.Errorf("%q is not a calendar day in %s form", trimmed, dayLayout)
	}
	return parsed.UTC().Format(dayLayout), nil
}

// Label renders the UTC day index (milliseconds divided by one day) that the
// schema's integer day columns carry.
func Label(utcDay int64) string {
	return time.UnixMilli(utcDay * utcDayMS).UTC().Format(dayLayout)
}

// Execer is the write surface a caller inside its own transaction provides, so
// a mark can commit together with the evidence that justifies it.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Mark inserts the days that are not marked yet, all or nothing. A day is
// marked once: an existing row keeps its first reason and recorded instant, so
// a repeated reconciliation can neither rewrite the evidence nor duplicate it.
// It returns how many rows this call actually inserted.
func (r *Repository) Mark(ctx context.Context, days []Day, recordedAtMS int64) (int, error) {
	if len(days) == 0 {
		return 0, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	inserted, err := MarkTx(ctx, tx, days, recordedAtMS)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// MarkTx writes the days inside the caller's transaction, so the marks the
// reconciliation concludes are durable exactly when the evidence that explains
// them is: an interrupted run leaves neither. Inserting is idempotent, so a
// caller that retries the whole recording writes the same marks again instead
// of duplicating or rewriting them.
func MarkTx(ctx context.Context, execer Execer, days []Day, recordedAtMS int64) (int, error) {
	if len(days) == 0 {
		return 0, nil
	}
	if recordedAtMS <= 0 {
		return 0, fmt.Errorf("a coverage mark needs the instant it was recorded")
	}
	inserted := 0
	for _, day := range days {
		normalized, err := ParseDay(day.Day)
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(day.Reason) == "" {
			return 0, fmt.Errorf("the mark for %s names no reason", normalized)
		}
		result, err := execer.ExecContext(ctx, `insert into `+TableName+` (day, reason, source, recorded_at_ms)
			values (?, ?, ?, ?)
			on conflict(day) do nothing`, normalized, day.Reason, SourceUpgradeReconcile, recordedAtMS)
		if err != nil {
			return 0, fmt.Errorf("record the incomplete day %s: %w", normalized, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		inserted += int(affected)
	}
	return inserted, nil
}

// DaysIn returns the marks whose UTC day overlaps [fromTimestampMS,
// toTimestampMS), ordered by day. A store that never ran a reconciliation has no
// marking table at all, which is reported as no marks rather than as an error:
// nothing was ever marked, so nothing is incomplete.
func (r *Repository) DaysIn(ctx context.Context, fromTimestampMS, toTimestampMS int64) ([]Day, error) {
	return DaysIn(ctx, r.db, fromTimestampMS, toTimestampMS)
}

// DaysIn reads the marks inside a caller's transaction or snapshot.
func DaysIn(ctx context.Context, q Queryer, fromTimestampMS, toTimestampMS int64) ([]Day, error) {
	days := []Day{}
	if toTimestampMS <= fromTimestampMS {
		return days, nil
	}
	lower, upper, hasUpper := dayBounds(fromTimestampMS, toTimestampMS)
	if lower == "" {
		return days, nil
	}
	present, err := tablePresent(ctx, q)
	if err != nil || !present {
		return days, err
	}
	query := `select day, reason from ` + TableName + ` where day >= ?`
	args := []any{lower}
	if hasUpper {
		query += ` and day < ?`
		args = append(args, upper)
	}
	rows, err := q.QueryContext(ctx, query+` order by day`, args...)
	if err != nil {
		return nil, fmt.Errorf("read the incomplete coverage days: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var day Day
		if err := rows.Scan(&day.Day, &day.Reason); err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	return days, rows.Err()
}

// dayBounds renders [fromTimestampMS, toTimestampMS) as the inclusive lower day
// label and the exclusive upper one. The upper label is dropped (hasUpper
// false) once the window reaches past the last day the stored text form can
// hold: no mark can sit above it, and adding a day to a timestamp near MaxInt64
// would wrap into a negative day that sorts before every mark, hiding all of
// them. The lower label is clamped to the first day the form can hold, which is
// below every mark, so a pre-year-1 window still means "from the beginning". A
// lower label of "" means no stored day can fall inside the window.
func dayBounds(fromTimestampMS, toTimestampMS int64) (string, string, bool) {
	lower := floorDayIndex(fromTimestampMS)
	if lower > maxStoredDayIndex {
		return "", "", false
	}
	if lower < minStoredDayIndex {
		lower = minStoredDayIndex
	}
	upper := ceilDayIndex(toTimestampMS)
	if upper <= lower {
		return "", "", false
	}
	if upper > maxStoredDayIndex {
		return Label(lower), "", false
	}
	return Label(lower), Label(upper), true
}

// floorDayIndex is the index of the UTC day a timestamp falls in, rounding
// towards negative infinity so a timestamp before the epoch keeps belonging to
// the day it was in.
func floorDayIndex(timestampMS int64) int64 {
	day := timestampMS / utcDayMS
	if timestampMS%utcDayMS != 0 && timestampMS < 0 {
		day--
	}
	return day
}

// ceilDayIndex is the index of the first UTC day at or after a timestamp: the
// exclusive upper bound of a day range. It never adds to the timestamp, so a
// value near MaxInt64 cannot overflow.
func ceilDayIndex(timestampMS int64) int64 {
	day := timestampMS / utcDayMS
	if timestampMS%utcDayMS > 0 {
		day++
	}
	return day
}

func tablePresent(ctx context.Context, q Queryer) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, `select count(*) from sqlite_master
		where type = 'table' and name = ?`, TableName).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect the incomplete coverage table: %w", err)
	}
	return count == 1, nil
}
