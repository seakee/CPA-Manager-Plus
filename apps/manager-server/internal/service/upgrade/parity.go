package upgrade

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// detectParityGaps ports the read-only detection queries for the pre-v1.14.6
// raw-delete loss: an archived event whose raw row is gone, but whose retained
// projection is missing, carries a different timestamp, or sits past the
// projection watermark. It also cross-checks the per-day deleted coverage table
// against the archive refs in both directions. It reports counts and affected
// UTC dates; it repairs nothing and blocks nothing.
func detectParityGaps(ctx context.Context, db *sql.DB, schema sourceSchema) (ParityGaps, error) {
	gaps := ParityGaps{AffectedDates: []string{}}
	if !schema.tables["usage_archive_event_refs"] || !schema.tables["usage_events"] {
		return gaps, nil
	}
	usable, watermark, err := projectionWatermark(ctx, db, schema)
	if err != nil {
		return gaps, err
	}
	gaps.ProjectionStateUsable = usable

	projectionJoin := ""
	projectionColumns := "0, 0"
	if schema.tables["usage_monitoring_event_projection_v1"] {
		projectionJoin = "left join usage_monitoring_event_projection_v1 p on p.event_id = r.raw_event_id"
		projectionColumns = "case when p.event_id is null then 0 else 1 end, coalesce(p.timestamp_ms, 0)"
	}
	rows, err := db.QueryContext(ctx, `select r.raw_event_id, r.timestamp_ms, `+projectionColumns+`
		from usage_archive_event_refs r `+projectionJoin+`
		where r.raw_deleted_at_ms is not null
			and not exists (select 1 from usage_events e where e.event_hash = r.event_hash)`)
	if err != nil {
		return gaps, err
	}
	defer rows.Close()
	affected := map[string]bool{}
	for rows.Next() {
		var rawEventID, timestampMS int64
		var hasProjection int
		var projectionTimestampMS int64
		if err := rows.Scan(&rawEventID, &timestampMS, &hasProjection, &projectionTimestampMS); err != nil {
			return gaps, err
		}
		gaps.DeletedEventRefs++
		day := utcDayLabel(timestampMS / 86_400_000)
		switch {
		case hasProjection == 0:
			gaps.MissingProjection++
			affected[day] = true
		case projectionTimestampMS != timestampMS:
			gaps.MismatchedProjection++
			affected[day] = true
		}
		if gaps.ProjectionStateUsable && rawEventID > watermark {
			gaps.PastWatermark++
			affected[day] = true
		}
	}
	if err := rows.Err(); err != nil {
		return gaps, err
	}

	if schema.tables["usage_archive_deleted_coverage_daily"] {
		coverage, err := readDeletedCoverageDaily(ctx, db)
		if err != nil {
			return gaps, err
		}
		refs, err := countDeletedRefsPerDay(ctx, db)
		if err != nil {
			return gaps, err
		}
		days := make(map[int64]bool, len(coverage)+len(refs))
		for day := range coverage {
			days[day] = true
		}
		for day := range refs {
			days[day] = true
		}
		for day := range days {
			if coverage[day] != refs[day] {
				gaps.CoverageDaysWithGaps++
				affected[utcDayLabel(day)] = true
			}
		}
	}

	gaps.AffectedDates = make([]string, 0, len(affected))
	for day := range affected {
		gaps.AffectedDates = append(gaps.AffectedDates, day)
	}
	sort.Strings(gaps.AffectedDates)
	return gaps, nil
}

// projectionWatermark reads the projection_v1 rollup state. Usable means a
// watermark can be trusted for the past-watermark comparison: schema version 1,
// present, and not being cleared. The projection revision is not re-derived
// here; a mismatch is left to the reconciliation step.
func projectionWatermark(ctx context.Context, db *sql.DB, schema sourceSchema) (bool, int64, error) {
	if !schema.tables["usage_monitoring_rollup_state"] {
		return false, 0, nil
	}
	var schemaVersion int
	var status string
	var coverageEventID int64
	err := db.QueryRowContext(ctx, `select schema_version, status, coverage_event_id
		from usage_monitoring_rollup_state where rollup_name = 'projection_v1'`).
		Scan(&schemaVersion, &status, &coverageEventID)
	if err == sql.ErrNoRows {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	usable := schemaVersion == 1 && status != "" && status != "clearing" && coverageEventID >= 0
	if !usable {
		return false, 0, nil
	}
	return true, coverageEventID, nil
}

func readDeletedCoverageDaily(ctx context.Context, db *sql.DB) (map[int64]int, error) {
	rows, err := db.QueryContext(ctx,
		`select utc_day, deleted_event_count from usage_archive_deleted_coverage_daily`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	coverage := map[int64]int{}
	for rows.Next() {
		var day int64
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		coverage[day] = count
	}
	return coverage, rows.Err()
}

func countDeletedRefsPerDay(ctx context.Context, db *sql.DB) (map[int64]int, error) {
	rows, err := db.QueryContext(ctx, `select timestamp_ms / 86400000 as utc_day, count(*)
		from usage_archive_event_refs where raw_deleted_at_ms is not null group by utc_day`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := map[int64]int{}
	for rows.Next() {
		var day int64
		var count int
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		refs[day] = count
	}
	return refs, rows.Err()
}

func utcDayLabel(day int64) string {
	return time.UnixMilli(day * 86_400_000).UTC().Format("2006-01-02")
}
