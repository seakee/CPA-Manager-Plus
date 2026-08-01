package usagerollup

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const AccountHistoryCheckpointName = "account_history"
const DashboardHourlyCheckpointName = "dashboard_hourly"

type Repository interface {
	CatchUpAccountHistory(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)
	CatchUpDashboardHourly(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)
	Checkpoint(ctx context.Context, name string) (Checkpoint, error)
	LatestEventID(ctx context.Context) (int64, error)
	AccountHistoryRows(ctx context.Context, accountKeys []string) ([]AccountHistoryRow, error)
	DashboardHourlyRows(ctx context.Context, fromMS, toMS int64) ([]DashboardHourlyRow, error)
	DashboardHourlyModelRows(ctx context.Context, fromMS, toMS int64) ([]DashboardHourlyRow, error)
	DashboardDailyRows(ctx context.Context, fromMS, toMS int64) ([]DashboardHourlyRow, error)
}

type Checkpoint struct {
	Name                string
	LastEventID         int64
	UpdatedAtMS         int64
	LastError           string
	LastRunStartedAtMS  int64
	LastRunFinishedAtMS int64
}

type CatchUpResult struct {
	Processed   int
	LastEventID int64
	Pending     bool
}

type AccountHistoryRow struct {
	usage.LongContextTokens
	AccountKey           string
	AccountSnapshot      string
	AuthLabelSnapshot    string
	AuthProviderSnapshot string
	AuthIndex            string
	Source               string
	SourceHash           string
	Model                string
	BillingModel         string
	ServiceTier          string
	Calls                int64
	SuccessCalls         int64
	FailureCalls         int64
	InputTokens          int64
	OutputTokens         int64
	ReasoningTokens      int64
	CachedTokens         int64
	CacheReadTokens      int64
	CacheCreationTokens  int64
	TotalTokens          int64
	FirstSeenMS          int64
	LastSeenMS           int64
	UpdatedAtMS          int64
}

type repository struct {
	db          *sql.DB
	catchUpGate chan struct{}
}

func New(db *sql.DB) Repository {
	return &repository{
		db:          db,
		catchUpGate: make(chan struct{}, 1),
	}
}

type eventRow struct {
	ID                   int64
	TimestampMS          int64
	AccountSnapshot      string
	AuthLabelSnapshot    string
	AuthProviderSnapshot string
	AuthIndex            string
	Source               string
	SourceHash           string
	Model                string
	BillingModel         string
	ServiceTier          string
	Failed               bool
	InputTokens          int64
	OutputTokens         int64
	ReasoningTokens      int64
	CachedTokens         int64
	CacheTokens          int64
	CacheReadTokens      int64
	CacheCreationTokens  int64
	TotalTokens          int64
}

func (r *repository) CatchUpAccountHistory(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error) {
	if limit <= 0 {
		limit = 1000
	}
	if nowMS <= 0 {
		return CatchUpResult{}, errors.New("nowMS must be greater than 0")
	}
	if err := r.acquireCatchUp(ctx); err != nil {
		return CatchUpResult{}, err
	}
	defer r.releaseCatchUp()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CatchUpResult{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	checkpoint, err := checkpointInTx(ctx, tx, AccountHistoryCheckpointName)
	if err != nil {
		return CatchUpResult{}, err
	}
	events, err := eventsAfterCheckpoint(ctx, tx, checkpoint.LastEventID, limit)
	if err != nil {
		return CatchUpResult{}, err
	}
	latestID, err := latestEventIDInTx(ctx, tx)
	if err != nil {
		return CatchUpResult{}, err
	}
	if len(events) == 0 {
		if err := upsertCheckpoint(ctx, tx, AccountHistoryCheckpointName, checkpoint.LastEventID, nowMS, nowMS, nowMS, ""); err != nil {
			return CatchUpResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CatchUpResult{}, err
		}
		return CatchUpResult{LastEventID: checkpoint.LastEventID, Pending: latestID > checkpoint.LastEventID}, nil
	}

	rollups := aggregateAccountHistory(events, nowMS)
	if err := upsertAccountRollups(ctx, tx, rollups); err != nil {
		return CatchUpResult{}, err
	}
	lastEventID := events[len(events)-1].ID
	if err := upsertCheckpoint(ctx, tx, AccountHistoryCheckpointName, lastEventID, nowMS, nowMS, nowMS, ""); err != nil {
		return CatchUpResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CatchUpResult{}, err
	}
	return CatchUpResult{
		Processed:   len(events),
		LastEventID: lastEventID,
		Pending:     latestID > lastEventID,
	}, nil
}

func (r *repository) acquireCatchUp(ctx context.Context) error {
	select {
	case r.catchUpGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *repository) releaseCatchUp() {
	select {
	case <-r.catchUpGate:
	default:
	}
}

func (r *repository) Checkpoint(ctx context.Context, name string) (Checkpoint, error) {
	if strings.TrimSpace(name) == "" {
		name = AccountHistoryCheckpointName
	}
	return checkpointQuery(ctx, r.db, name)
}

func (r *repository) LatestEventID(ctx context.Context) (int64, error) {
	var id int64
	if err := r.db.QueryRowContext(ctx, `select coalesce(max(id), 0) from usage_events`).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (r *repository) AccountHistoryRows(ctx context.Context, accountKeys []string) ([]AccountHistoryRow, error) {
	keys := normalizeAccountKeys(accountKeys)
	if len(keys) == 0 {
		return []AccountHistoryRow{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		args = append(args, key)
	}
	rows, err := r.db.QueryContext(ctx, `select
	account_key,
	coalesce(account_snapshot, ''),
	coalesce(auth_label_snapshot, ''),
	coalesce(auth_provider_snapshot, ''),
	coalesce(auth_index, ''),
	coalesce(source, ''),
	coalesce(source_hash, ''),
	model,
	billing_model,
	service_tier,
	calls,
	success_calls,
	failure_calls,
	input_tokens,
	output_tokens,
	reasoning_tokens,
	cached_tokens,
	cache_read_tokens,
	cache_creation_tokens,
	long_input_tokens,
	long_output_tokens,
	long_cached_tokens,
	long_cache_read_tokens,
	long_cache_creation_tokens,
	total_tokens,
	first_seen_ms,
	last_seen_ms,
	updated_at_ms
from usage_account_model_rollups
where account_key in (`+placeholders+`)
order by account_key, last_seen_ms desc`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]AccountHistoryRow, 0)
	for rows.Next() {
		var row AccountHistoryRow
		if err := rows.Scan(
			&row.AccountKey,
			&row.AccountSnapshot,
			&row.AuthLabelSnapshot,
			&row.AuthProviderSnapshot,
			&row.AuthIndex,
			&row.Source,
			&row.SourceHash,
			&row.Model,
			&row.BillingModel,
			&row.ServiceTier,
			&row.Calls,
			&row.SuccessCalls,
			&row.FailureCalls,
			&row.InputTokens,
			&row.OutputTokens,
			&row.ReasoningTokens,
			&row.CachedTokens,
			&row.CacheReadTokens,
			&row.CacheCreationTokens,
			&row.LongInputTokens,
			&row.LongOutputTokens,
			&row.LongCachedTokens,
			&row.LongCacheReadTokens,
			&row.LongCacheCreationTokens,
			&row.TotalTokens,
			&row.FirstSeenMS,
			&row.LastSeenMS,
			&row.UpdatedAtMS,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func checkpointQuery(ctx context.Context, db *sql.DB, name string) (Checkpoint, error) {
	var cp Checkpoint
	var lastError sql.NullString
	var started, finished sql.NullInt64
	err := db.QueryRowContext(ctx, `select
	name, last_event_id, updated_at_ms, last_error, last_run_started_at_ms, last_run_finished_at_ms
from usage_rollup_checkpoints
where name = ?`, name).Scan(
		&cp.Name,
		&cp.LastEventID,
		&cp.UpdatedAtMS,
		&lastError,
		&started,
		&finished,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{Name: name}, nil
	}
	if err != nil {
		return Checkpoint{}, err
	}
	cp.LastError = lastError.String
	cp.LastRunStartedAtMS = started.Int64
	cp.LastRunFinishedAtMS = finished.Int64
	return cp, nil
}

func checkpointInTx(ctx context.Context, tx *sql.Tx, name string) (Checkpoint, error) {
	var cp Checkpoint
	var lastError sql.NullString
	var started, finished sql.NullInt64
	err := tx.QueryRowContext(ctx, `select
	name, last_event_id, updated_at_ms, last_error, last_run_started_at_ms, last_run_finished_at_ms
from usage_rollup_checkpoints
where name = ?`, name).Scan(
		&cp.Name,
		&cp.LastEventID,
		&cp.UpdatedAtMS,
		&lastError,
		&started,
		&finished,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{Name: name}, nil
	}
	if err != nil {
		return Checkpoint{}, err
	}
	cp.LastError = lastError.String
	cp.LastRunStartedAtMS = started.Int64
	cp.LastRunFinishedAtMS = finished.Int64
	return cp, nil
}

func latestEventIDInTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `select coalesce(max(id), 0) from usage_events`).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func eventsAfterCheckpoint(ctx context.Context, tx *sql.Tx, lastEventID int64, limit int) ([]eventRow, error) {
	rows, err := tx.QueryContext(ctx, `select
	id,
	timestamp_ms,
	coalesce(account_snapshot, ''),
	coalesce(auth_label_snapshot, ''),
	coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
	coalesce(auth_index, ''),
	coalesce(source, ''),
	coalesce(source_hash, ''),
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	failed,
	coalesce(normalized_total_input_tokens, input_tokens, 0),
	coalesce(output_tokens, 0),
	coalesce(reasoning_tokens, 0),
	coalesce(cached_tokens, 0),
	coalesce(cache_tokens, 0),
	coalesce(cache_read_tokens, 0),
	coalesce(cache_creation_tokens, 0),
	coalesce(total_tokens, 0)
from usage_events
where id > ?
order by id
limit ?`, lastEventID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]eventRow, 0, limit)
	for rows.Next() {
		var row eventRow
		var failed int
		if err := rows.Scan(
			&row.ID,
			&row.TimestampMS,
			&row.AccountSnapshot,
			&row.AuthLabelSnapshot,
			&row.AuthProviderSnapshot,
			&row.AuthIndex,
			&row.Source,
			&row.SourceHash,
			&row.Model,
			&row.BillingModel,
			&row.ServiceTier,
			&failed,
			&row.InputTokens,
			&row.OutputTokens,
			&row.ReasoningTokens,
			&row.CachedTokens,
			&row.CacheTokens,
			&row.CacheReadTokens,
			&row.CacheCreationTokens,
			&row.TotalTokens,
		); err != nil {
			return nil, err
		}
		row.Failed = failed != 0
		row.CachedTokens = usage.CompatibleCachedTokens(
			row.CachedTokens,
			row.CacheTokens,
			row.CacheReadTokens,
			row.CacheCreationTokens,
		)
		events = append(events, row)
	}
	return events, rows.Err()
}

type accountRollupKey struct {
	AccountKey   string
	BillingModel string
	ServiceTier  string
}

func aggregateAccountHistory(events []eventRow, nowMS int64) []AccountHistoryRow {
	grouped := map[accountRollupKey]*AccountHistoryRow{}
	for _, event := range events {
		accountKey := accountGroupKey(event.AccountSnapshot, event.AuthLabelSnapshot, event.Source, event.AuthIndex)
		billingModel := strings.TrimSpace(event.BillingModel)
		if billingModel == "" {
			billingModel = strings.TrimSpace(event.Model)
		}
		if billingModel == "" {
			billingModel = "-"
		}
		serviceTier := strings.TrimSpace(event.ServiceTier)
		key := accountRollupKey{
			AccountKey:   accountKey,
			BillingModel: billingModel,
			ServiceTier:  serviceTier,
		}
		row := grouped[key]
		if row == nil {
			modelName := strings.TrimSpace(event.Model)
			if modelName == "" {
				modelName = billingModel
			}
			row = &AccountHistoryRow{
				AccountKey:           accountKey,
				AccountSnapshot:      event.AccountSnapshot,
				AuthLabelSnapshot:    event.AuthLabelSnapshot,
				AuthProviderSnapshot: event.AuthProviderSnapshot,
				AuthIndex:            event.AuthIndex,
				Source:               event.Source,
				SourceHash:           event.SourceHash,
				Model:                modelName,
				BillingModel:         billingModel,
				ServiceTier:          serviceTier,
				FirstSeenMS:          event.TimestampMS,
				LastSeenMS:           event.TimestampMS,
				UpdatedAtMS:          nowMS,
			}
			grouped[key] = row
		}
		fillSnapshotFields(row, event)
		row.Calls++
		if event.Failed {
			row.FailureCalls++
		} else {
			row.SuccessCalls++
		}
		row.InputTokens += event.InputTokens
		row.OutputTokens += event.OutputTokens
		row.ReasoningTokens += event.ReasoningTokens
		row.CachedTokens += event.CachedTokens
		row.CacheReadTokens += event.CacheReadTokens
		row.CacheCreationTokens += event.CacheCreationTokens
		row.AddIfLongContext(event.InputTokens, event.OutputTokens, event.CachedTokens, event.CacheReadTokens, event.CacheCreationTokens)
		row.TotalTokens += event.TotalTokens
		if event.TimestampMS < row.FirstSeenMS {
			row.FirstSeenMS = event.TimestampMS
		}
		if event.TimestampMS > row.LastSeenMS {
			row.LastSeenMS = event.TimestampMS
		}
	}
	result := make([]AccountHistoryRow, 0, len(grouped))
	for _, row := range grouped {
		result = append(result, *row)
	}
	return result
}

func fillSnapshotFields(row *AccountHistoryRow, event eventRow) {
	if row.AccountSnapshot == "" {
		row.AccountSnapshot = event.AccountSnapshot
	}
	if row.AuthLabelSnapshot == "" {
		row.AuthLabelSnapshot = event.AuthLabelSnapshot
	}
	if row.AuthProviderSnapshot == "" {
		row.AuthProviderSnapshot = event.AuthProviderSnapshot
	}
	if row.AuthIndex == "" {
		row.AuthIndex = event.AuthIndex
	}
	if row.Source == "" {
		row.Source = event.Source
	}
	if row.SourceHash == "" {
		row.SourceHash = event.SourceHash
	}
}

func upsertAccountRollups(ctx context.Context, tx *sql.Tx, rows []AccountHistoryRow) error {
	if len(rows) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `insert into usage_account_model_rollups (
	account_key,
	account_snapshot,
	auth_label_snapshot,
	auth_provider_snapshot,
	auth_index,
	source,
	source_hash,
	model,
	billing_model,
	service_tier,
	calls,
	success_calls,
	failure_calls,
	input_tokens,
	output_tokens,
	reasoning_tokens,
	cached_tokens,
	cache_read_tokens,
	cache_creation_tokens,
	long_input_tokens,
	long_output_tokens,
	long_cached_tokens,
	long_cache_read_tokens,
	long_cache_creation_tokens,
	total_tokens,
	first_seen_ms,
	last_seen_ms,
	updated_at_ms
) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(account_key, billing_model, service_tier) do update set
	account_snapshot = coalesce(nullif(excluded.account_snapshot, ''), usage_account_model_rollups.account_snapshot),
	auth_label_snapshot = coalesce(nullif(excluded.auth_label_snapshot, ''), usage_account_model_rollups.auth_label_snapshot),
	auth_provider_snapshot = coalesce(nullif(excluded.auth_provider_snapshot, ''), usage_account_model_rollups.auth_provider_snapshot),
	auth_index = coalesce(nullif(excluded.auth_index, ''), usage_account_model_rollups.auth_index),
	source = coalesce(nullif(excluded.source, ''), usage_account_model_rollups.source),
	source_hash = coalesce(nullif(excluded.source_hash, ''), usage_account_model_rollups.source_hash),
	model = coalesce(nullif(excluded.model, ''), usage_account_model_rollups.model),
	calls = usage_account_model_rollups.calls + excluded.calls,
	success_calls = usage_account_model_rollups.success_calls + excluded.success_calls,
	failure_calls = usage_account_model_rollups.failure_calls + excluded.failure_calls,
	input_tokens = usage_account_model_rollups.input_tokens + excluded.input_tokens,
	output_tokens = usage_account_model_rollups.output_tokens + excluded.output_tokens,
	reasoning_tokens = usage_account_model_rollups.reasoning_tokens + excluded.reasoning_tokens,
	cached_tokens = usage_account_model_rollups.cached_tokens + excluded.cached_tokens,
	cache_read_tokens = usage_account_model_rollups.cache_read_tokens + excluded.cache_read_tokens,
	cache_creation_tokens = usage_account_model_rollups.cache_creation_tokens + excluded.cache_creation_tokens,
	long_input_tokens = usage_account_model_rollups.long_input_tokens + excluded.long_input_tokens,
	long_output_tokens = usage_account_model_rollups.long_output_tokens + excluded.long_output_tokens,
	long_cached_tokens = usage_account_model_rollups.long_cached_tokens + excluded.long_cached_tokens,
	long_cache_read_tokens = usage_account_model_rollups.long_cache_read_tokens + excluded.long_cache_read_tokens,
	long_cache_creation_tokens = usage_account_model_rollups.long_cache_creation_tokens + excluded.long_cache_creation_tokens,
	total_tokens = usage_account_model_rollups.total_tokens + excluded.total_tokens,
	first_seen_ms = min(usage_account_model_rollups.first_seen_ms, excluded.first_seen_ms),
	last_seen_ms = max(usage_account_model_rollups.last_seen_ms, excluded.last_seen_ms),
	updated_at_ms = excluded.updated_at_ms`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, row := range rows {
		if _, err := stmt.ExecContext(
			ctx,
			row.AccountKey,
			nullString(row.AccountSnapshot),
			nullString(row.AuthLabelSnapshot),
			nullString(row.AuthProviderSnapshot),
			nullString(row.AuthIndex),
			nullString(row.Source),
			nullString(row.SourceHash),
			row.Model,
			row.BillingModel,
			row.ServiceTier,
			row.Calls,
			row.SuccessCalls,
			row.FailureCalls,
			row.InputTokens,
			row.OutputTokens,
			row.ReasoningTokens,
			row.CachedTokens,
			row.CacheReadTokens,
			row.CacheCreationTokens,
			row.LongInputTokens,
			row.LongOutputTokens,
			row.LongCachedTokens,
			row.LongCacheReadTokens,
			row.LongCacheCreationTokens,
			row.TotalTokens,
			row.FirstSeenMS,
			row.LastSeenMS,
			row.UpdatedAtMS,
		); err != nil {
			return err
		}
	}
	return nil
}

func upsertCheckpoint(ctx context.Context, tx *sql.Tx, name string, lastEventID int64, updatedAtMS int64, startedAtMS int64, finishedAtMS int64, lastError string) error {
	_, err := tx.ExecContext(ctx, `insert into usage_rollup_checkpoints (
	name, last_event_id, updated_at_ms, last_error, last_run_started_at_ms, last_run_finished_at_ms
) values (?, ?, ?, ?, ?, ?)
on conflict(name) do update set
	last_event_id = excluded.last_event_id,
	updated_at_ms = excluded.updated_at_ms,
	last_error = excluded.last_error,
	last_run_started_at_ms = excluded.last_run_started_at_ms,
	last_run_finished_at_ms = excluded.last_run_finished_at_ms`,
		name,
		lastEventID,
		updatedAtMS,
		nullString(lastError),
		nullPositiveInt64(startedAtMS),
		nullPositiveInt64(finishedAtMS),
	)
	return err
}

func accountGroupKey(accountSnapshot, authLabelSnapshot, source, authIndex string) string {
	if strings.TrimSpace(accountSnapshot) != "" {
		return accountSnapshot
	}
	if strings.TrimSpace(authLabelSnapshot) != "" {
		return authLabelSnapshot
	}
	if strings.TrimSpace(source) != "" {
		return source
	}
	if strings.TrimSpace(authIndex) != "" {
		return authIndex
	}
	return "-"
}

func normalizeAccountKeys(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.TrimSpace(value)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, key)
	}
	return result
}

func nullString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullPositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func AccountKey(accountSnapshot, authLabelSnapshot, source, authIndex string) string {
	return accountGroupKey(accountSnapshot, authLabelSnapshot, source, authIndex)
}
