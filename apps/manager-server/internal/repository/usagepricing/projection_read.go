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
// pricing bands as hourly recovery. The caller must verify core coverage.
func (r *repository) LoadAccountRowsFromEventsTx(ctx context.Context, tx *sql.Tx, accountKeys []string) ([]AccountRow, error) {
	keys := normalizeValues(accountKeys)
	if len(keys) == 0 {
		return []AccountRow{}, nil
	}
	source, err := retainedEventSourceTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	grouped := map[accountKey]*AccountRow{}
	if err := mergeAccountRowsFromSource(ctx, tx, 0, keys, grouped, source); err != nil {
		return nil, err
	}
	return sortedAccountRows(grouped), nil
}

func retainedEventSourceTx(ctx context.Context, tx *sql.Tx) (string, error) {
	source := "usage_events"
	var schemaVersion int
	var revision, status string
	var coverageID int64
	err := tx.QueryRowContext(ctx, `select schema_version, structure_revision, status, coverage_event_id
		from usage_monitoring_rollup_state where rollup_name = 'projection_v1'`).Scan(
		&schemaVersion, &revision, &status, &coverageID,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil && schemaVersion == 1 && coverageID > 0 &&
		revision == usageidentity.MonitoringProjectionStructureRevision() && status != "clearing" {
		// These are the inputs used by the existing pricing-band query. Do not
		// synthesize request details or trust an obsolete projection identity.
		columns := []string{
			"timestamp_ms", "model", "requested_model", "resolved_model", "service_tier", "failed",
			"input_tokens", "output_tokens", "reasoning_tokens", "cached_tokens", "cache_tokens",
			"cache_read_tokens", "cache_creation_tokens", "normalized_total_input_tokens", "total_tokens", "latency_ms",
			"provider", "auth_index", "source", "source_hash", "account_snapshot", "auth_label_snapshot",
			"auth_file_snapshot", "auth_provider_snapshot", "auth_project_id_snapshot", "auth_account_id_snapshot",
		}
		source = fmt.Sprintf(`(select p.event_id as id, p.%s from %s p where p.event_id <= %d
			union all select e.id, e.%s from usage_events e where e.id > %d)`,
			strings.Join(columns, ", p."), usageprojection.EventTable, coverageID,
			strings.Join(columns, ", e."), coverageID,
		)
	}
	return source, nil
}
