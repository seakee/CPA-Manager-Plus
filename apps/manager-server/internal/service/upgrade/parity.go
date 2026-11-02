package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

// projectionState is the trust decision for the retained projection. A
// projection row is only evidence when the rollup state that produced it is
// current: right schema version, the pinned structure revision, and not being
// cleared. A watermark from any other state proves worker progress, not that a
// retained row still means what it says.
type projectionState struct {
	usable        bool
	reason        string
	watermark     int64
	revision      string
	expectedRev   string
	schemaVersion int
	status        string
}

// detectParityGaps ports the read-only detection queries for the pre-v1.14.6
// raw-delete loss: an archived event whose raw row is gone, but whose retained
// projection is missing, carries a different timestamp, cannot be trusted
// because the projection state is unusable, or sits past the projection
// watermark. It also cross-checks the per-day deleted coverage table against the
// archive refs in both directions. It reports counts and affected UTC dates; it
// repairs nothing and blocks nothing.
func detectParityGaps(ctx context.Context, db *sql.DB, schema sourceSchema) (ParityGaps, error) {
	gaps := ParityGaps{AffectedDates: []string{}}
	// The parity question only exists for a source that retained the archive
	// refs and the deleted-coverage rollup. A source without them has no
	// retained history to compare, so the answer is "not applicable" and never
	// a clean zero, which would read as "no gaps found".
	if !schema.tables["usage_archive_event_refs"] {
		gaps.Reason = "usage_archive_event_refs is missing"
		return gaps, nil
	}
	if !schema.tables["usage_archive_deleted_coverage_daily"] {
		gaps.Reason = "usage_archive_deleted_coverage_daily is missing"
		return gaps, nil
	}
	if !schema.tables["usage_events"] {
		gaps.Reason = "usage_events is missing"
		return gaps, nil
	}
	gaps.Applicable = true
	state, err := projectionWatermark(ctx, db, schema)
	if err != nil {
		return gaps, err
	}
	gaps.ProjectionStateUsable = state.usable
	gaps.ProjectionStateReason = state.reason

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
		case !state.usable:
			// Same timestamp, but the projection state cannot vouch for the row:
			// a stale projection is indistinguishable from a healthy one without
			// the revision and status the rollup recorded.
			gaps.StaleProjection++
			affected[day] = true
		}
		if state.usable && rawEventID > state.watermark {
			gaps.PastWatermark++
			affected[day] = true
		}
	}
	if err := rows.Err(); err != nil {
		return gaps, err
	}

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

	gaps.AffectedDates = make([]string, 0, len(affected))
	for day := range affected {
		gaps.AffectedDates = append(gaps.AffectedDates, day)
	}
	sort.Strings(gaps.AffectedDates)
	return gaps, nil
}

// projectionWatermark reads the projection_v1 rollup state and decides whether
// its watermark can be trusted: schema version 1, the pinned projection
// structure revision, a present status that is not "clearing", and a
// non-negative coverage id.
func projectionWatermark(ctx context.Context, db *sql.DB, schema sourceSchema) (projectionState, error) {
	state := projectionState{expectedRev: usageidentity.MonitoringProjectionStructureRevision()}
	if !schema.tables["usage_monitoring_rollup_state"] {
		state.reason = "usage_monitoring_rollup_state is missing"
		return state, nil
	}
	err := db.QueryRowContext(ctx, `select schema_version, structure_revision, status, coverage_event_id
		from usage_monitoring_rollup_state where rollup_name = 'projection_v1'`).
		Scan(&state.schemaVersion, &state.revision, &state.status, &state.watermark)
	if errors.Is(err, sql.ErrNoRows) {
		state.reason = "projection_v1 rollup state is missing"
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.usable, state.reason = stateTrust(state)
	if !state.usable {
		state.watermark = 0
	}
	return state, nil
}

func stateTrust(state projectionState) (bool, string) {
	if state.schemaVersion != 1 {
		return false, fmt.Sprintf("schema_version=%d expected=1", state.schemaVersion)
	}
	if state.revision != state.expectedRev {
		return false, fmt.Sprintf("structure_revision=%q expected=%q", state.revision, state.expectedRev)
	}
	if state.status == "" {
		return false, "status is empty"
	}
	if state.status == "clearing" {
		return false, "status=clearing"
	}
	if state.watermark < 0 {
		return false, fmt.Sprintf("coverage_event_id=%d is negative", state.watermark)
	}
	return true, ""
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
