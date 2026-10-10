package usagepricing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

var retainedPricingColumns = []string{
	"timestamp_ms", "model", "requested_model", "resolved_model", "service_tier", "failed",
	"input_tokens", "output_tokens", "reasoning_tokens", "cached_tokens", "cache_tokens",
	"cache_read_tokens", "cache_creation_tokens", "normalized_total_input_tokens", "total_tokens", "latency_ms",
	"provider", "auth_index", "source", "source_hash", "account_snapshot", "auth_label_snapshot",
	"auth_file_snapshot", "auth_provider_snapshot", "auth_project_id_snapshot", "auth_account_id_snapshot",
}

var ErrRetainedPricingHistoryIncomplete = errors.New("retained pricing history is incomplete")

// LoadHourlyRowsFromEventsTx bypasses a deficient pricing cache without writing
// to it. A compatible retained projection supplies archived events; raw events
// after its watermark supply the tail, with no overlapping IDs.
// The caller must compare the result with the permanent core snapshot before
// treating it as complete. A projection watermark alone is not proof of data.
func (r *repository) LoadHourlyRowsFromEventsTx(ctx context.Context, tx *sql.Tx, filter HourlyFilter) ([]HourlyRow, error) {
	source, err := retainedEventSourceTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	query, args := hourlyStatementFromEvents(filter, filter.FromMS, filter.ToMS, 0, false, source)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := map[hourlyKey]*HourlyRow{}
	if err := scanAndMergeHourlyRows(rows, grouped); err != nil {
		return nil, err
	}
	return sortedHourlyRows(grouped), nil
}

// LoadAccountRowsFromEventsTx uses the same retained event source and exact
// pricing bands as hourly recovery. The retained projection arm is scoped by
// account_key before entering the pricing CTE so degraded account-history reads
// can use the projection's account index instead of scanning retained history.
// The caller must verify core coverage.
func (r *repository) LoadAccountRowsFromEventsTx(ctx context.Context, tx *sql.Tx, accountKeys []string) ([]AccountRow, error) {
	keys := normalizeValues(accountKeys)
	if len(keys) == 0 {
		return []AccountRow{}, nil
	}
	source, sourceArgs, err := retainedAccountEventSourceTx(ctx, tx, keys)
	if err != nil {
		return nil, err
	}
	grouped := map[accountKey]*AccountRow{}
	if err := mergeAccountRowsFromSourceArgs(ctx, tx, 0, keys, grouped, source, sourceArgs); err != nil {
		return nil, err
	}
	return sortedAccountRows(grouped), nil
}

// VerifyRetainedPricingRebuildSourceTx proves that every raw event removed by
// the supported archive flow is still available from the compatible monitoring
// projection. Structural model-price mutations call this before commit so an
// unrecoverable pricing revision can never become active.
func VerifyRetainedPricingRebuildSourceTx(ctx context.Context, tx *sql.Tx) error {
	_, _, err := retainedPricingRebuildSourceTx(ctx, tx)
	return err
}

// retainedPricingRebuildSourceTx returns the exact event source and target ID
// used only for full pricing rebuilds. Live raw rows are authoritative when
// present; the projection contributes only events whose raw rows were deleted.
// This keeps the rebuild source duplicate-free while preserving original event
// IDs for the existing checkpoint state machine.
func retainedPricingRebuildSourceTx(ctx context.Context, tx *sql.Tx) (string, int64, error) {
	var hasDeletedRaw bool
	if err := tx.QueryRowContext(ctx, `select exists (
		select 1 from usage_archive_event_refs where raw_deleted_at_ms is not null
	)`).Scan(&hasDeletedRaw); err != nil {
		return "", 0, err
	}
	if !hasDeletedRaw {
		latestID, err := latestEventID(ctx, tx)
		return "usage_events", latestID, err
	}

	var schemaVersion int
	var revision, status string
	var coverageID int64
	if err := tx.QueryRowContext(ctx, `select schema_version, structure_revision, status, coverage_event_id
		from usage_monitoring_rollup_state where rollup_name = 'projection_v1'`).Scan(
		&schemaVersion, &revision, &status, &coverageID,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, fmt.Errorf("%w: retained usage projection state is unavailable", ErrRetainedPricingHistoryIncomplete)
		}
		return "", 0, err
	}
	if schemaVersion != 1 ||
		revision != usageidentity.MonitoringProjectionStructureRevision() ||
		status == "clearing" {
		return "", 0, fmt.Errorf("%w: retained usage projection is incompatible or rebuilding", ErrRetainedPricingHistoryIncomplete)
	}

	var incomplete bool
	if err := tx.QueryRowContext(ctx, `select exists (
		select 1
		from usage_archive_event_refs archived
		left join usage_monitoring_event_projection_v1 p
			on p.event_id = archived.raw_event_id
		where archived.raw_deleted_at_ms is not null
			and not exists (
				select 1 from usage_events live where live.event_hash = archived.event_hash
			)
			and (archived.raw_event_id > ? or p.event_id is null)
	)`, coverageID).Scan(&incomplete); err != nil {
		return "", 0, err
	}
	if incomplete {
		return "", 0, fmt.Errorf("%w: retained usage projection is missing a deleted pricing event", ErrRetainedPricingHistoryIncomplete)
	}

	var latestID int64
	if err := tx.QueryRowContext(ctx, `select coalesce(max(event_id), 0) from (
		select id as event_id from usage_events
		union all
		select archived.raw_event_id as event_id
		from usage_archive_event_refs archived
		where archived.raw_deleted_at_ms is not null
			and not exists (
				select 1 from usage_events live where live.event_hash = archived.event_hash
			)
	)`).Scan(&latestID); err != nil {
		return "", 0, err
	}

	source := fmt.Sprintf(`(select e.id, e.%s from usage_events e
		union all
		select p.event_id as id, p.%s
		from usage_archive_event_refs archived
		join %s p on p.event_id = archived.raw_event_id
		where archived.raw_deleted_at_ms is not null
			and not exists (
				select 1 from usage_events live where live.event_hash = archived.event_hash
			))`,
		strings.Join(retainedPricingColumns, ", e."),
		strings.Join(retainedPricingColumns, ", p."),
		usageprojection.EventTable,
	)
	return source, latestID, nil
}

func retainedEventSourceTx(ctx context.Context, tx *sql.Tx) (string, error) {
	coverageID, compatible, err := retainedProjectionCoverageTx(ctx, tx)
	if err != nil {
		return "", err
	}
	if !compatible || coverageID <= 0 {
		return "usage_events", nil
	}
	return fmt.Sprintf(`(select p.event_id as id, p.%s from %s p where p.event_id <= %d
		union all select e.id, e.%s from usage_events e where e.id > %d)`,
		strings.Join(retainedPricingColumns, ", p."), usageprojection.EventTable, coverageID,
		strings.Join(retainedPricingColumns, ", e."), coverageID,
	), nil
}

func retainedAccountEventSourceTx(ctx context.Context, tx *sql.Tx, accountKeys []string) (string, []any, error) {
	coverageID, compatible, err := retainedProjectionCoverageTx(ctx, tx)
	if err != nil {
		return "", nil, err
	}
	if !compatible || coverageID <= 0 {
		return "usage_events", nil, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(accountKeys)), ",")
	args := make([]any, 0, len(accountKeys))
	for _, key := range accountKeys {
		args = append(args, key)
	}
	return fmt.Sprintf(`(select p.event_id as id, p.%s from %s p
		where p.account_key in (%s) and p.event_id <= %d
		union all select e.id, e.%s from usage_events e where e.id > %d)`,
		strings.Join(retainedPricingColumns, ", p."), usageprojection.EventTable, placeholders, coverageID,
		strings.Join(retainedPricingColumns, ", e."), coverageID,
	), args, nil
}

func retainedProjectionCoverageTx(ctx context.Context, tx *sql.Tx) (int64, bool, error) {
	var schemaVersion int
	var revision, status string
	var coverageID int64
	err := tx.QueryRowContext(ctx, `select schema_version, structure_revision, status, coverage_event_id
		from usage_monitoring_rollup_state where rollup_name = 'projection_v1'`).Scan(
		&schemaVersion, &revision, &status, &coverageID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	compatible := schemaVersion == 1 &&
		revision == usageidentity.MonitoringProjectionStructureRevision() &&
		status != "clearing"
	return coverageID, compatible, nil
}
