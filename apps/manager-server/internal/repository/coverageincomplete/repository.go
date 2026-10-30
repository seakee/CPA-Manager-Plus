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
)

// SourceUpgradeReconcile is the only writer of a mark today.
const SourceUpgradeReconcile = "upgrade_reconcile"

// dayLayout is the stored day format. Days sort chronologically as text, so a
// range read is a plain string comparison and needs no parsing.
const dayLayout = "2006-01-02"

const utcDayMS = int64(24 * 60 * 60 * 1000)

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

// Mark inserts the days that are not marked yet, all or nothing. A day is
// marked once: an existing row keeps its first reason and recorded instant, so
// a repeated reconciliation can neither rewrite the evidence nor duplicate it.
// It returns how many rows this call actually inserted.
func (r *Repository) Mark(ctx context.Context, days []Day, recordedAtMS int64) (int, error) {
	if len(days) == 0 {
		return 0, nil
	}
	if recordedAtMS <= 0 {
		return 0, fmt.Errorf("a coverage mark needs the instant it was recorded")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	inserted := 0
	for _, day := range days {
		normalized, err := ParseDay(day.Day)
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(day.Reason) == "" {
			return 0, fmt.Errorf("the mark for %s names no reason", normalized)
		}
		result, err := tx.ExecContext(ctx, `insert into `+TableName+` (day, reason, source, recorded_at_ms)
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
	if err := tx.Commit(); err != nil {
		return 0, err
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
	present, err := tablePresent(ctx, q)
	if err != nil || !present {
		return days, err
	}
	rows, err := q.QueryContext(ctx, `select day, reason from `+TableName+`
		where day >= ? and day < ? order by day`,
		Label(fromTimestampMS/utcDayMS), Label((toTimestampMS+utcDayMS-1)/utcDayMS))
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

func tablePresent(ctx context.Context, q Queryer) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, `select count(*) from sqlite_master
		where type = 'table' and name = ?`, TableName).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect the incomplete coverage table: %w", err)
	}
	return count == 1, nil
}
