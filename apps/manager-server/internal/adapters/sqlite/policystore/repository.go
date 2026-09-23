package policystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/policystore"
)

type repository struct{ db *sql.DB }

func New(db *sql.DB) ports.Repository { return &repository{db: db} }

func (r *repository) CreatePolicy(ctx context.Context, policy resourcepolicy.QuotaPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.Revision != resourcepolicy.InitialRevision || policy.CreatedAtMS != policy.UpdatedAtMS {
		return fmt.Errorf("%w: new policy must start at revision 1 and creation time", resourcepolicy.ErrInvalidPolicy)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `insert into gateway_quota_policies
		(id, revision, state, enforcement, action, created_at_ms, updated_at_ms)
		values (?, ?, ?, ?, ?, ?, ?)`, policy.ID, policy.Revision, policy.State,
		policy.Enforcement, policy.Action, policy.CreatedAtMS, policy.UpdatedAtMS)
	if err != nil {
		return fmt.Errorf("insert policy: %w", err)
	}
	if err := insertRules(ctx, tx, policy.ID, policy.Rules); err != nil {
		return err
	}
	return tx.Commit()
}

func insertRules(ctx context.Context, tx *sql.Tx, id resourcepolicy.PolicyID, rules []resourcepolicy.QuotaRule) error {
	for _, rule := range rules {
		_, err := tx.ExecContext(ctx, `insert into gateway_quota_policy_rules
			(policy_id, metric, limit_value, window_kind, duration_ms, calendar_months, anchor_at_ms, timezone)
			values (?, ?, ?, ?, ?, ?, ?, ?)`, id, rule.Metric, rule.LimitValue,
			rule.Window.Kind, rule.Window.DurationMS, rule.Window.CalendarMonths,
			rule.Window.AnchorAtMS, rule.Window.Timezone)
		if err != nil {
			return fmt.Errorf("insert policy rule: %w", err)
		}
	}
	return nil
}

func (r *repository) LoadPolicy(ctx context.Context, id resourcepolicy.PolicyID) (resourcepolicy.QuotaPolicy, error) {
	if err := id.Validate(); err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	defer tx.Rollback()
	p, err := loadPolicy(ctx, tx, id)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}

func loadPolicy(ctx context.Context, tx *sql.Tx, id resourcepolicy.PolicyID) (resourcepolicy.QuotaPolicy, error) {
	var p resourcepolicy.QuotaPolicy
	var rev int64
	err := tx.QueryRowContext(ctx, `select id, revision, state, enforcement, action, created_at_ms, updated_at_ms
		from gateway_quota_policies where id = ?`, id).Scan(&p.ID, &rev, &p.State,
		&p.Enforcement, &p.Action, &p.CreatedAtMS, &p.UpdatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ports.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Revision = resourcepolicy.Revision(rev)
	rows, err := tx.QueryContext(ctx, `select metric, limit_value, window_kind, duration_ms, calendar_months, anchor_at_ms, timezone
		from gateway_quota_policy_rules where policy_id = ? order by metric`, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var rule resourcepolicy.QuotaRule
		var duration, months, anchor sql.NullInt64
		if err := rows.Scan(&rule.Metric, &rule.LimitValue, &rule.Window.Kind,
			&duration, &months, &anchor, &rule.Window.Timezone); err != nil {
			return p, err
		}
		if duration.Valid {
			rule.Window.DurationMS = &duration.Int64
		}
		if months.Valid {
			rule.Window.CalendarMonths = &months.Int64
		}
		if anchor.Valid {
			rule.Window.AnchorAtMS = &anchor.Int64
		}
		p.Rules = append(p.Rules, rule)
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	if err := p.Validate(); err != nil {
		return resourcepolicy.QuotaPolicy{}, fmt.Errorf("persisted policy invalid: %w", err)
	}
	return p, nil
}

func nextRevision(rev resourcepolicy.Revision, oldMS, nowMS int64) (resourcepolicy.Revision, int64, error) {
	if rev >= math.MaxInt64 || oldMS == math.MaxInt64 || nowMS <= 0 {
		return 0, 0, ports.ErrRevisionOverflow
	}
	if nowMS <= oldMS {
		nowMS = oldMS + 1
	}
	return rev + 1, nowMS, nil
}

func sameRules(a, b []resourcepolicy.QuotaRule) bool {
	if len(a) != len(b) {
		return false
	}
	byMetric := make(map[resourcepolicy.Metric]resourcepolicy.QuotaRule, len(a))
	for _, rule := range a {
		byMetric[rule.Metric] = rule
	}
	for _, rule := range b {
		if !reflect.DeepEqual(byMetric[rule.Metric], rule) {
			return false
		}
	}
	return true
}

func (r *repository) ReplacePolicySpec(ctx context.Context, id resourcepolicy.PolicyID, expected resourcepolicy.Revision, spec resourcepolicy.PolicySpec, nowMS int64) (resourcepolicy.QuotaPolicy, error) {
	if err := id.Validate(); err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	defer tx.Rollback()
	current, err := loadPolicy(ctx, tx, id)
	if err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	if current.Revision != expected {
		return resourcepolicy.QuotaPolicy{}, ports.ErrRevisionConflict
	}
	if err := spec.Validate(); err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	if current.Enforcement == spec.Enforcement && current.Action == spec.Action && sameRules(current.Rules, spec.Rules) {
		return current, nil
	}
	rev, updated, err := nextRevision(current.Revision, current.UpdatedAtMS, nowMS)
	if err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	result, err := tx.ExecContext(ctx, `update gateway_quota_policies set revision = ?, enforcement = ?, action = ?, updated_at_ms = ?
		where id = ? and revision = ?`, rev, spec.Enforcement, spec.Action, updated, id, expected)
	if err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	if err := oneRow(result); err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	if _, err := tx.ExecContext(ctx, `delete from gateway_quota_policy_rules where policy_id = ?`, id); err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	if err := insertRules(ctx, tx, id, spec.Rules); err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	current.Revision, current.UpdatedAtMS, current.PolicySpec = rev, updated, spec
	return current, nil
}

func (r *repository) SetPolicyState(ctx context.Context, id resourcepolicy.PolicyID, expected resourcepolicy.Revision, state resourcepolicy.State, nowMS int64) (resourcepolicy.QuotaPolicy, error) {
	if err := id.Validate(); err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	defer tx.Rollback()
	current, err := loadPolicy(ctx, tx, id)
	if err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	if current.Revision != expected {
		return resourcepolicy.QuotaPolicy{}, ports.ErrRevisionConflict
	}
	if state != resourcepolicy.StateActive && state != resourcepolicy.StateDisabled {
		return resourcepolicy.QuotaPolicy{}, resourcepolicy.ErrInvalidPolicy
	}
	if current.State == state {
		return current, nil
	}
	rev, updated, err := nextRevision(current.Revision, current.UpdatedAtMS, nowMS)
	if err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	result, err := tx.ExecContext(ctx, `update gateway_quota_policies set state = ?, revision = ?, updated_at_ms = ?
		where id = ? and revision = ?`, state, rev, updated, id, expected)
	if err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	if err := oneRow(result); err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return resourcepolicy.QuotaPolicy{}, err
	}
	current.State, current.Revision, current.UpdatedAtMS = state, rev, updated
	return current, nil
}

func oneRow(result sql.Result) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ports.ErrRevisionConflict
	}
	return nil
}

func keyLifecycle(ctx context.Context, tx *sql.Tx, id identity.APIKeyID) error {
	var lifecycle identity.Lifecycle
	err := tx.QueryRowContext(ctx, `select lifecycle from gateway_api_key_identities where id = ?`, id).Scan(&lifecycle)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ErrNotFound
	}
	if err != nil {
		return err
	}
	if lifecycle == identity.LifecycleSuperseded {
		return ports.ErrSupersededAPIKey
	}
	if lifecycle != identity.LifecycleActive && lifecycle != identity.LifecycleMissing {
		return identity.ErrInvalidLifecycle
	}
	return nil
}

func (r *repository) BindAPIKey(ctx context.Context, binding resourcepolicy.PolicyBinding) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	if binding.Revision != resourcepolicy.InitialRevision || binding.CreatedAtMS != binding.UpdatedAtMS {
		return resourcepolicy.ErrInvalidBinding
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := keyLifecycle(ctx, tx, binding.APIKeyID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `insert into gateway_api_key_policy_bindings
		(api_key_id, policy_id, revision, enabled, created_at_ms, updated_at_ms)
		values (?, ?, ?, ?, ?, ?)`, binding.APIKeyID, binding.PolicyID, binding.Revision,
		binding.Enabled, binding.CreatedAtMS, binding.UpdatedAtMS)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *repository) LoadBinding(ctx context.Context, id identity.APIKeyID) (resourcepolicy.PolicyBinding, error) {
	if err := id.Validate(); err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	return loadBinding(ctx, r.db, id)
}

type rowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadBinding(ctx context.Context, reader rowReader, id identity.APIKeyID) (resourcepolicy.PolicyBinding, error) {
	var b resourcepolicy.PolicyBinding
	var rev int64
	err := reader.QueryRowContext(ctx, `select api_key_id, policy_id, revision, enabled, created_at_ms, updated_at_ms
		from gateway_api_key_policy_bindings where api_key_id = ?`, id).Scan(&b.APIKeyID, &b.PolicyID, &rev,
		&b.Enabled, &b.CreatedAtMS, &b.UpdatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ports.ErrNotFound
	}
	if err != nil {
		return b, err
	}
	b.Revision = resourcepolicy.Revision(rev)
	if err := b.Validate(); err != nil {
		return resourcepolicy.PolicyBinding{}, fmt.Errorf("persisted binding invalid: %w", err)
	}
	return b, nil
}

func (r *repository) RebindAPIKey(ctx context.Context, id identity.APIKeyID, expected resourcepolicy.Revision, policyID resourcepolicy.PolicyID, nowMS int64) (resourcepolicy.PolicyBinding, error) {
	if err := id.Validate(); err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	if err := policyID.Validate(); err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	defer tx.Rollback()
	b, err := loadBinding(ctx, tx, id)
	if err != nil {
		return b, err
	}
	if b.Revision != expected {
		return resourcepolicy.PolicyBinding{}, ports.ErrRevisionConflict
	}
	if err := keyLifecycle(ctx, tx, id); err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	if b.PolicyID == policyID {
		return b, nil
	}
	rev, updated, err := nextRevision(b.Revision, b.UpdatedAtMS, nowMS)
	if err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	result, err := tx.ExecContext(ctx, `update gateway_api_key_policy_bindings set policy_id = ?, revision = ?, updated_at_ms = ?
		where api_key_id = ? and revision = ?`, policyID, rev, updated, id, expected)
	if err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	if err := oneRow(result); err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	b.PolicyID, b.Revision, b.UpdatedAtMS = policyID, rev, updated
	return b, nil
}

func (r *repository) SetBindingEnabled(ctx context.Context, id identity.APIKeyID, expected resourcepolicy.Revision, enabled bool, nowMS int64) (resourcepolicy.PolicyBinding, error) {
	if err := id.Validate(); err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	defer tx.Rollback()
	b, err := loadBinding(ctx, tx, id)
	if err != nil {
		return b, err
	}
	if b.Revision != expected {
		return resourcepolicy.PolicyBinding{}, ports.ErrRevisionConflict
	}
	if b.Enabled == enabled {
		return b, nil
	}
	rev, updated, err := nextRevision(b.Revision, b.UpdatedAtMS, nowMS)
	if err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	result, err := tx.ExecContext(ctx, `update gateway_api_key_policy_bindings set enabled = ?, revision = ?, updated_at_ms = ?
		where api_key_id = ? and revision = ?`, enabled, rev, updated, id, expected)
	if err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	if err := oneRow(result); err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return resourcepolicy.PolicyBinding{}, err
	}
	b.Enabled, b.Revision, b.UpdatedAtMS = enabled, rev, updated
	return b, nil
}
