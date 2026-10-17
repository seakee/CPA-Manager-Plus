package freshstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const OperationKey = "fresh_operation_v1"
const AuthorityKey = "fresh_authority_v1"
const adoptionKey = "fresh_adoption_v1"
const adminKey = "admin_credential_v1"

type repository struct{ db *sql.DB }

func New(db *sql.DB) Store { return &repository{db} }

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func load(ctx context.Context, q queryer) (Record, error) {
	var r Record
	var legacy int
	if err := q.QueryRowContext(ctx, "select count(*) from settings where key in ('setup','bootstrap_state_v1','runtime_desired_v1')").Scan(&legacy); err != nil || legacy != 0 {
		return Record{}, ErrNotFresh
	}
	for _, item := range []struct {
		k        string
		v        any
		optional bool
	}{{OperationKey, &r.Operation, false}, {AuthorityKey, &r.Authority, false}, {adminKey, &r.Admin, true}, {adoptionKey, &r.AdoptionCiphertext, true}} {
		var data string
		err := q.QueryRowContext(ctx, "select value from settings where key=?", item.k).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			if item.optional {
				continue
			}
			return Record{}, ErrNotFresh
		}
		if err != nil {
			return Record{}, err
		}
		if err = json.Unmarshal([]byte(data), item.v); err != nil {
			return Record{}, ErrNotFresh
		}
	}
	var connectionData string
	if err := q.QueryRowContext(ctx, "select value from settings where key='manager_config_v1'").Scan(&connectionData); err == nil {
		var cfg model.ManagerConfig
		if json.Unmarshal([]byte(connectionData), &cfg) != nil || cfg.Collector.Enabled == nil || *cfg.Collector.Enabled {
			return Record{}, ErrNotFresh
		}
		r.Connection = &cfg.CPAConnection
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Record{}, err
	}
	if err := valid(r); err != nil {
		return Record{}, err
	}
	return r, nil
}
func valid(r Record) error {
	if r.Operation.Validate() != nil || r.Operation.AdminInitialized != (r.Admin != nil) || r.Operation.AdminInitialized != r.Authority.ClaimClosed {
		return ErrNotFresh
	}
	if r.Admin != nil && !security.IsValidAdminCredential(*r.Admin) {
		return ErrNotFresh
	}
	if r.Connection != nil && (r.Connection.CPABaseURL == "" || !security.IsValidProtectedEnvelope(r.Connection.ManagementKey)) {
		return ErrNotFresh
	}
	if r.Operation.Strategy.Strategy == bootstrap.ConnectExternal && r.Operation.Strategy.Stage == "committed" {
		if r.Connection == nil {
			return ErrNotFresh
		}
		// The strategy service persists its normalized URL and hashes those
		// exact bytes. Check the same durable fact without service/network reads.
		digest := sha256.Sum256([]byte(r.Connection.CPABaseURL))
		if r.Operation.Strategy.TargetIdentity != "external:sha256:"+hex.EncodeToString(digest[:]) {
			return ErrNotFresh
		}
	}
	if r.Operation.Strategy.Strategy == bootstrap.AdoptExisting {
		if !security.IsValidProtectedEnvelope(r.AdoptionCiphertext) {
			return ErrNotFresh
		}
		if r.Operation.Strategy.Stage == "committed" {
			if r.Connection == nil {
				return ErrNotFresh
			}
			digest := sha256.Sum256([]byte(r.Connection.CPABaseURL))
			if r.Operation.Strategy.DestinationConnectionIdentity != "external:sha256:"+hex.EncodeToString(digest[:]) {
				return ErrNotFresh
			}
		} else if r.Connection != nil {
			return ErrNotFresh
		}
	} else if r.AdoptionCiphertext != "" {
		return ErrNotFresh
	}
	return nil
}
func (r *repository) Load(ctx context.Context) (Record, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	v, err := load(ctx, tx)
	if err != nil {
		return Record{}, err
	}
	return v, tx.Commit()
}
func write(ctx context.Context, tx *sql.Tx, k string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at_ms=excluded.updated_at_ms", k, string(data), time.Now().UnixMilli())
	return err
}

// ValidatePristine rejects all product state before the first ownership write.
func ValidatePristine(ctx context.Context, q queryer) error {
	// Inventory every table, including retained identity ledgers and future
	// product tables. Only these exact Migrate-created metadata seeds may have
	// rows. A table omitted by a hand-maintained product list must not become a
	// way to admit archived history or account state as a new installation.
	type seed struct {
		count   int
		allowed string
		args    []any
	}
	seeds := map[string]seed{
		"settings":                                {3, `(key='usage_account_history_identity_format_version' and value=?) or (key='usage_monitoring_model_format_version' and value=?) or (key='usage_dashboard_hourly_format_version' and value='3')`, []any{usageidentity.AccountHistoryStructureRevision(), usageidentity.ModelFormatVersion}},
		"gateway_source_binding_revision":         {1, `id=1 and revision=0`, nil},
		"gateway_usage_identity_projection_state": {1, `state_name='canonical_identity_v1' and schema_version=1 and status='pending' and last_processed_event_id=0 and target_event_id=0 and processed_events=0 and binding_revision=-1 and last_run_started_at_ms is null and updated_at_ms=0 and finished_at_ms is null and last_error is null`, nil},
		"usage_data_migrations":                   {3, `((name in ('usage_cache_accounting_v1','usage_cache_accounting_v2') and status='completed') or (name='quota_snapshot_lifecycle_v1' and status='pending')) and last_event_id=0 and target_event_id=0 and processed_rows=0 and changed_rows=0 and applied_rows=0 and started_at_ms is null and updated_at_ms=0 and finished_at_ms is null and last_error is null`, nil},
		"usage_hourly_aggregate_state":            {1, `aggregate_name='hourly_core' and schema_version=3 and structure_revision='schema-3:model-1' and status='ready' and backfill_last_event_id=0 and coverage_event_id=0 and target_event_id=0 and processed_events=0 and min_bucket_ms is null and max_bucket_ms is null and last_run_started_at_ms is null and updated_at_ms=0 and finished_at_ms is null and last_error is null`, nil},
		"usage_monitoring_rollup_state":           {4, `schema_version=1 and status='ready' and backfill_last_event_id=0 and coverage_event_id=0 and target_event_id=0 and processed_events=0 and last_run_started_at_ms is null and updated_at_ms=0 and finished_at_ms is null and last_error is null and ((rollup_name in ('codex_legacy_identity_v1','stats_v1') and structure_revision='') or (rollup_name='metadata_v1' and structure_revision=?) or (rollup_name='projection_v1' and structure_revision=?))`, []any{usageidentity.ModelFormatVersion, usageidentity.MonitoringProjectionStructureRevision()}},
		"usage_monitoring_search_index_state":     {1, `id=1 and ready=0 and updated_at_ms=0`, nil},
		"usage_pricing_rollup_state":              {1, `rollup_name='pricing_v1' and schema_version=1 and structure_revision='' and status='pending' and backfill_last_event_id=0 and coverage_event_id=0 and target_event_id=0 and processed_events=0 and min_bucket_ms is null and max_bucket_ms is null and last_run_started_at_ms is null and updated_at_ms=0 and finished_at_ms is null and last_error is null`, nil},
		// Empty FTS5 shadow metadata emitted by the pinned SQLite Migrate. Any
		// index payload beyond the empty initialization blocks is source state.
		"usage_monitoring_event_search_v1_config": {1, `k='version' and v=4`, nil},
		"usage_monitoring_event_search_v1_data":   {2, `(id=1 and length(block)=0) or (id=10 and hex(block)='00000000000000')`, nil},
	}
	rows, err := q.QueryContext(ctx, `select name from sqlite_schema where type='table' order by name`)
	if err != nil {
		return ErrNotFresh
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			_ = rows.Close()
			return ErrNotFresh
		}
		tables = append(tables, name)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return ErrNotFresh
	}
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		var count int
		if err = q.QueryRowContext(ctx, "select count(*) from "+quoted).Scan(&count); err != nil {
			return ErrNotFresh
		}
		initial, ok := seeds[table]
		if !ok {
			if count != 0 {
				return ErrNotFresh
			}
			continue
		}
		if count != initial.count {
			return ErrNotFresh
		}
		if err = q.QueryRowContext(ctx, "select count(*) from "+quoted+" where ("+initial.allowed+") is not true", initial.args...).Scan(&count); err != nil || count != 0 {
			return ErrNotFresh
		}
		delete(seeds, table)
	}
	if len(seeds) != 0 {
		return ErrNotFresh
	}
	return nil
}
func (r *repository) Create(ctx context.Context, id bootstrap.FreshIdentity, admin *model.AdminCredential) (Record, error) {
	if !id.Valid() || admin != nil && !security.IsValidAdminCredential(*admin) {
		return Record{}, ErrNotFresh
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	if err = ValidatePristine(ctx, tx); err != nil {
		return Record{}, err
	}
	v := Record{Operation: bootstrap.FreshOperation{Version: bootstrap.FreshContractVersion, Identity: id, Revision: 1, GatewayPending: true, AdminInitialized: admin != nil}, Admin: admin, Authority: Authority{ClaimClosed: admin != nil}}
	if err = write(ctx, tx, OperationKey, v.Operation); err == nil {
		err = write(ctx, tx, AuthorityKey, v.Authority)
	}
	if err == nil && admin != nil {
		err = write(ctx, tx, adminKey, admin)
	}
	if err != nil {
		return Record{}, err
	}
	return v, tx.Commit()
}
func (r *repository) Update(ctx context.Context, id bootstrap.FreshIdentity, revision uint64, fn func(*Record) error) (Record, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	v, err := load(ctx, tx)
	if err != nil {
		return Record{}, err
	}
	if v.Operation.Identity != id || v.Operation.Revision != revision || revision == math.MaxUint64 {
		return Record{}, ErrConflict
	}
	before := v
	before.Operation.Strategy.AdoptionDecisions = slices.Clone(v.Operation.Strategy.AdoptionDecisions)
	before.Operation.Strategy.AdoptionResources = slices.Clone(v.Operation.Strategy.AdoptionResources)
	if v.Admin != nil {
		copy := *v.Admin
		before.Admin = &copy
	}
	if v.Connection != nil {
		copy := *v.Connection
		before.Connection = &copy
	}
	if err = fn(&v); err != nil {
		return Record{}, err
	}
	if v.Operation.Identity != id || v.Operation.Revision != revision || before.Admin != nil && !reflect.DeepEqual(before.Admin, v.Admin) || before.Operation.AdminInitialized && !v.Operation.AdminInitialized || before.Authority.ClaimClosed && !v.Authority.ClaimClosed || before.Operation.Routing.Committed && !v.Operation.Routing.Committed {
		return Record{}, ErrConflict
	}
	if before.Connection != nil && !reflect.DeepEqual(before.Connection, v.Connection) {
		return Record{}, ErrConflict
	}
	if before.AdoptionCiphertext != "" && before.AdoptionCiphertext != v.AdoptionCiphertext {
		return Record{}, ErrConflict
	}
	if before.Operation.Strategy.Strategy == bootstrap.AdoptExisting {
		frozen := before.Operation.Strategy
		frozen.Stage = v.Operation.Strategy.Stage
		if !reflect.DeepEqual(frozen, v.Operation.Strategy) || before.Operation.Strategy.Stage == "committed" && v.Operation.Strategy.Stage != "committed" {
			return Record{}, ErrConflict
		}
	}
	v.Operation.Revision++
	if err = valid(v); err != nil {
		return Record{}, err
	}
	if err = write(ctx, tx, OperationKey, v.Operation); err == nil {
		err = write(ctx, tx, AuthorityKey, v.Authority)
	}
	if err == nil && before.Admin == nil && v.Admin != nil {
		err = write(ctx, tx, adminKey, v.Admin)
	}
	if err == nil && before.AdoptionCiphertext == "" && v.AdoptionCiphertext != "" {
		err = write(ctx, tx, adoptionKey, v.AdoptionCiphertext)
	}
	if err == nil && before.Connection == nil && v.Connection != nil {
		disabled := false
		cfg := model.ManagerConfig{CPAConnection: *v.Connection, Collector: model.ManagerCollectorConfig{Enabled: &disabled}, UpdatedAtMS: time.Now().UnixMilli()}
		err = write(ctx, tx, "manager_config_v1", cfg)
	}
	if err != nil {
		return Record{}, err
	}
	return v, tx.Commit()
}
