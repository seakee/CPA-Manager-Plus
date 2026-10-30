// Package identityusage reads Canonical-Key usage from the canonical identity
// shadow projection. Aggregation reads only counts and token sums; hashes never
// leave this adapter.
package identityusage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/coverageincomplete"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

const (
	stateName = "canonical_identity_v1"
	// collectionStartKey is the settings row recording the first durable
	// collection instant of this installation.
	collectionStartKey = "fresh_usage_collection_started_at_ms"
	// maxIdentityPlaceholders bounds one batched aggregation statement.
	maxIdentityPlaceholders = 200
)

type repository struct{ db *sql.DB }

// New returns the SQLite adapter of the identity usage read port.
func New(db *sql.DB) ports.Repository { return &repository{db: db} }

func (r *repository) Binding(ctx context.Context, id identity.APIKeyID) (ports.Binding, error) {
	if err := id.Validate(); err != nil {
		return ports.Binding{}, err
	}
	// One Canonical Key owns exactly one source hash. The lookup returns that
	// hash's attribution window; a hash owned by another active identity is a
	// conflict, never a silent relay to that identity.
	var binding ports.Binding
	var conflict bool
	err := r.db.QueryRowContext(ctx, `
		select coalesce(min(b.first_seen_at_ms), 0),
		       exists (
		         select 1 from `+sqliterepo.GatewayAPIKeySourceBindingsTable+` o
		         where o.api_key_hash = k.secret_hash and o.retired_at_ms is null
		           and o.api_key_id <> k.api_key_id
		       )
		from fresh_client_api_keys k
		left join `+sqliterepo.GatewayAPIKeySourceBindingsTable+` b on b.api_key_id = k.api_key_id
		where k.api_key_id = ?
		group by k.api_key_id`, string(id)).Scan(&binding.FirstSeenAtMS, &conflict)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.Binding{}, ports.ErrBindingNotFound
	}
	if err != nil {
		return ports.Binding{}, fmt.Errorf("read canonical key binding: %w", err)
	}
	binding.APIKeyID = id
	if conflict {
		return ports.Binding{}, ports.ErrBindingConflict
	}
	if binding.FirstSeenAtMS <= 0 {
		return ports.Binding{}, ports.ErrBindingNotFound
	}
	return binding, nil
}

func (r *repository) ProjectionState(ctx context.Context) (ports.ProjectionState, error) {
	var state ports.ProjectionState
	err := r.db.QueryRowContext(ctx, `select status, last_processed_event_id, target_event_id,
		(select coalesce(max(id), 0) from usage_events)
		from `+sqliterepo.GatewayUsageIdentityProjectionStateTable+` where state_name = ?`, stateName).Scan(
		&state.Status, &state.LastProcessedEventID, &state.TargetEventID, &state.LatestEventID)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ProjectionState{}, errors.New("canonical identity projection state is missing")
	}
	if err != nil {
		return ports.ProjectionState{}, fmt.Errorf("read canonical identity projection state: %w", err)
	}
	return state, nil
}

func (r *repository) Windows(ctx context.Context, ids []identity.APIKeyID, fromMS, toMS int64) (map[identity.APIKeyID]ports.Window, error) {
	result := map[identity.APIKeyID]ports.Window{}
	if len(ids) == 0 {
		return result, nil
	}
	if toMS <= fromMS {
		return nil, errors.New("invalid identity usage window")
	}
	// One statement can only carry a bounded number of placeholders, so large
	// key lists are aggregated in batches and merged. Never truncate: a key
	// dropped here would be reported as an exact zero by its caller.
	for start := 0; start < len(ids); start += maxIdentityPlaceholders {
		end := start + maxIdentityPlaceholders
		if end > len(ids) {
			end = len(ids)
		}
		if err := r.windowsBatch(ctx, result, ids[start:end], fromMS, toMS); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (r *repository) windowsBatch(ctx context.Context, result map[identity.APIKeyID]ports.Window, ids []identity.APIKeyID, fromMS, toMS int64) error {
	args, placeholders := identityArgs(ids)
	mappedSource := ` select p.api_key_id, count(*) as calls,
			coalesce(sum(case when u.failed <> 0 then 1 else 0 end), 0) as failures,
			coalesce(sum(u.input_tokens), 0) as input_tokens,
			coalesce(sum(u.output_tokens), 0) as output_tokens,
			coalesce(sum(u.total_tokens), 0) as total_tokens,
			max(u.timestamp_ms) as last_used_at_ms
		from ` + sqliterepo.GatewayUsageIdentityProjectionTable + ` p
		join usage_events u on u.id = p.usage_event_id
		where p.api_key_state = 'mapped' and p.api_key_id in (` + placeholders + `)`
	totalsArgs := append(append([]any{}, args...), fromMS, toMS)
	rows, err := r.db.QueryContext(ctx, mappedSource+`
		and u.timestamp_ms >= ? and u.timestamp_ms < ?
		group by p.api_key_id`, totalsArgs...)
	if err != nil {
		return fmt.Errorf("aggregate canonical key usage: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rawID string
		var lastUsed sql.NullInt64
		var window ports.Window
		if err := rows.Scan(&rawID, &window.Totals.Calls, &window.Totals.Failure,
			&window.Totals.InputTokens, &window.Totals.OutputTokens, &window.Totals.TotalTokens,
			&lastUsed); err != nil {
			return fmt.Errorf("scan canonical key usage: %w", err)
		}
		if lastUsed.Valid && lastUsed.Int64 > 0 {
			window.Totals.LastUsedAtMS = &lastUsed.Int64
		}
		result[identity.APIKeyID(rawID)] = window
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate canonical key usage: %w", err)
	}
	rows.Close()

	// Events whose source hash could not be attributed are positive proof that
	// the window is incomplete, even when the key currently has no calls.
	unmappedRows, err := r.db.QueryContext(ctx, `
		select b.api_key_id, count(distinct p.usage_event_id)
		from `+sqliterepo.GatewayUsageIdentityProjectionTable+` p
		join `+sqliterepo.GatewayAPIKeySourceBindingsTable+` b on b.api_key_hash = p.api_key_source_hash
		where p.api_key_state <> 'mapped' and b.api_key_id in (`+placeholders+`)
		  and p.evidence_timestamp_ms >= ? and p.evidence_timestamp_ms < ?
		group by b.api_key_id`, totalsArgs...)
	if err != nil {
		return fmt.Errorf("count unattributed canonical key usage: %w", err)
	}
	defer unmappedRows.Close()
	for unmappedRows.Next() {
		var rawID string
		var count int64
		if err := unmappedRows.Scan(&rawID, &count); err != nil {
			return fmt.Errorf("scan unattributed canonical key usage: %w", err)
		}
		window, ok := result[identity.APIKeyID(rawID)]
		if !ok {
			window = ports.Window{}
		}
		window.UnmappedEvents = count
		result[identity.APIKeyID(rawID)] = window
	}
	return unmappedRows.Err()
}

func (r *repository) Series(ctx context.Context, id identity.APIKeyID, fromMS, toMS, bucketMS int64) ([]ports.Bucket, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	if toMS <= fromMS || bucketMS <= 0 {
		return nil, errors.New("invalid identity usage series window")
	}
	rows, err := r.db.QueryContext(ctx, `
		select (u.timestamp_ms / ?) * ?, count(*),
			coalesce(sum(case when u.failed <> 0 then 1 else 0 end), 0),
			coalesce(sum(u.total_tokens), 0)
		from `+sqliterepo.GatewayUsageIdentityProjectionTable+` p
		join usage_events u on u.id = p.usage_event_id
		where p.api_key_state = 'mapped' and p.api_key_id = ?
		  and u.timestamp_ms >= ? and u.timestamp_ms < ?
		group by 1 order by 1`, bucketMS, bucketMS, string(id), fromMS, toMS)
	if err != nil {
		return nil, fmt.Errorf("aggregate canonical key usage series: %w", err)
	}
	defer rows.Close()
	buckets := make([]ports.Bucket, 0)
	for rows.Next() {
		var bucket ports.Bucket
		if err := rows.Scan(&bucket.BucketMS, &bucket.Calls, &bucket.Failure, &bucket.TotalTokens); err != nil {
			return nil, fmt.Errorf("scan canonical key usage series: %w", err)
		}
		buckets = append(buckets, bucket)
	}
	return buckets, rows.Err()
}

func (r *repository) RawDeletedIn(ctx context.Context, fromMS, toMS int64) (bool, error) {
	if toMS <= fromMS {
		return false, nil
	}
	var deleted bool
	err := r.db.QueryRowContext(ctx, `select exists (
		select 1 from usage_archive_deleted_coverage_daily
		where deleted_event_count > 0 and max_timestamp_ms >= ? and min_timestamp_ms < ?
		union all
		select 1 from usage_archive_event_refs
		where raw_deleted_at_ms is not null and timestamp_ms >= ? and timestamp_ms < ?
	)`, fromMS, toMS, fromMS, toMS).Scan(&deleted)
	if err != nil {
		return false, fmt.Errorf("inspect archived raw usage coverage: %w", err)
	}
	return deleted, nil
}

func (r *repository) IncompleteDaysIn(ctx context.Context, fromMS, toMS int64) ([]ports.IncompleteDay, error) {
	days := []ports.IncompleteDay{}
	if toMS <= fromMS {
		return days, nil
	}
	marks, err := coverageincomplete.DaysIn(ctx, r.db, fromMS, toMS)
	if err != nil {
		return nil, fmt.Errorf("read the incomplete coverage days: %w", err)
	}
	for _, mark := range marks {
		days = append(days, ports.IncompleteDay{Day: mark.Day, Reason: mark.Reason})
	}
	return days, nil
}

func (r *repository) CollectionStartedAtMS(ctx context.Context) (int64, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `select value from settings where key = ?`, collectionStartKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read usage collection start: %w", err)
	}
	var startedAtMS int64
	if _, err := fmt.Sscan(raw, &startedAtMS); err != nil || startedAtMS <= 0 {
		return 0, nil
	}
	return startedAtMS, nil
}

func (r *repository) MarkCollectionStarted(ctx context.Context, nowMS int64) error {
	if nowMS <= 0 {
		return errors.New("invalid usage collection start")
	}
	_, err := r.db.ExecContext(ctx, `insert into settings(key, value, updated_at_ms) values(?, ?, ?)
		on conflict(key) do nothing`, collectionStartKey, fmt.Sprintf("%d", nowMS), nowMS)
	if err != nil {
		return fmt.Errorf("record usage collection start: %w", err)
	}
	return nil
}

// identityArgs renders one bounded batch. Callers split larger lists before
// calling it; truncating here would silently report later keys as empty.
func identityArgs(ids []identity.APIKeyID) ([]any, string) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = string(id)
	}
	return args, strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
}
