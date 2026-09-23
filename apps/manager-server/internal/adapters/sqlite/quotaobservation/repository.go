package quotaobservation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityprojection"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/quotaobservation"
)

var ErrSourceNotFound = errors.New("source usage event not found")

type repository struct{ db *sql.DB }
type view struct{ tx *sql.Tx }

func New(db *sql.DB) ports.Repository { return &repository{db: db} }

func (r *repository) WithSnapshot(ctx context.Context, evaluate func(ports.View) error) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := evaluate(view{tx}); err != nil {
		return err
	}
	return tx.Commit()
}

func (v view) Source(ctx context.Context, id int64) (ports.Source, error) {
	var source ports.Source
	err := v.tx.QueryRowContext(ctx, `select id, event_hash, timestamp_ms from usage_events where id = ?`, id).
		Scan(&source.ID, &source.EventHash, &source.TimestampMS)
	if errors.Is(err, sql.ErrNoRows) {
		return source, ErrSourceNotFound
	}
	if err != nil {
		return source, err
	}
	if source.ID <= 0 || source.EventHash == "" || source.TimestampMS <= 0 {
		return source, errors.New("invalid source usage event")
	}
	return source, nil
}

func (v view) State(ctx context.Context) (ports.ProjectionState, error) {
	var state ports.ProjectionState
	err := v.tx.QueryRowContext(ctx, `select schema_version, status, last_processed_event_id, binding_revision
		from gateway_usage_identity_projection_state where state_name = 'canonical_identity_v1'`).
		Scan(&state.SchemaVersion, &state.Status, &state.LastProcessedEventID, &state.BindingRevision)
	return state, err
}

func (v view) Projection(ctx context.Context, id int64) (*identityprojection.UsageIdentityProjection, error) {
	var p identityprojection.UsageIdentityProjection
	var keyID, credentialID sql.NullString
	err := v.tx.QueryRowContext(ctx, `select usage_event_id, event_hash, request_id, evidence_timestamp_ms,
		api_key_state, api_key_id, api_key_source_hash, credential_state, credential_id,
		credential_source_auth_id, schema_version, projected_at_ms
		from gateway_usage_identity_projection_v1 where usage_event_id = ?`, id).
		Scan(&p.UsageEventID, &p.EventHash, &p.RequestID, &p.EvidenceTimestampMS,
			&p.APIKeyState, &keyID, &p.APIKeySourceHash, &p.CredentialState, &credentialID,
			&p.CredentialSourceAuthID, &p.SchemaVersion, &p.ProjectedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if keyID.Valid {
		p.APIKeyID = &keyID.String
	}
	if credentialID.Valid {
		p.CredentialID = &credentialID.String
	}
	return &p, nil
}

func (v view) Binding(ctx context.Context, id identity.APIKeyID) (*resourcepolicy.PolicyBinding, error) {
	var b resourcepolicy.PolicyBinding
	var revision int64
	err := v.tx.QueryRowContext(ctx, `select api_key_id, policy_id, revision, enabled, created_at_ms, updated_at_ms
		from gateway_api_key_policy_bindings where api_key_id = ?`, id).
		Scan(&b.APIKeyID, &b.PolicyID, &revision, &b.Enabled, &b.CreatedAtMS, &b.UpdatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.Revision = resourcepolicy.Revision(revision)
	if err := b.Validate(); err != nil {
		return nil, fmt.Errorf("invalid persisted binding: %w", err)
	}
	return &b, nil
}

func (v view) Policy(ctx context.Context, id resourcepolicy.PolicyID) (resourcepolicy.QuotaPolicy, error) {
	var p resourcepolicy.QuotaPolicy
	var revision int64
	err := v.tx.QueryRowContext(ctx, `select id, revision, state, enforcement, action, created_at_ms, updated_at_ms
		from gateway_quota_policies where id = ?`, id).
		Scan(&p.ID, &revision, &p.State, &p.Enforcement, &p.Action, &p.CreatedAtMS, &p.UpdatedAtMS)
	if err != nil {
		return p, err
	}
	p.Revision = resourcepolicy.Revision(revision)
	rows, err := v.tx.QueryContext(ctx, `select metric, limit_value, window_kind, duration_ms,
		calendar_months, anchor_at_ms, timezone from gateway_quota_policy_rules
		where policy_id = ? order by metric`, id)
	if err != nil {
		return p, err
	}
	for rows.Next() {
		var rule resourcepolicy.QuotaRule
		var duration, months, anchor sql.NullInt64
		if err := rows.Scan(&rule.Metric, &rule.LimitValue, &rule.Window.Kind,
			&duration, &months, &anchor, &rule.Window.Timezone); err != nil {
			rows.Close()
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
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	if err := p.Validate(); err != nil {
		return resourcepolicy.QuotaPolicy{}, fmt.Errorf("invalid persisted policy: %w", err)
	}
	return p, nil
}

const observationQuery = `select u.total_tokens, u.failed
	from usage_events u
	join gateway_usage_identity_projection_v1 p on p.usage_event_id = u.id
	where p.api_key_state = 'mapped' and p.api_key_id = ?
	and u.id <= ? and u.timestamp_ms >= ? and u.timestamp_ms < ?`

func (v view) Observe(ctx context.Context, id identity.APIKeyID, metric resourcepolicy.Metric, highWater, start, cappedEnd int64) (ports.Observation, error) {
	rows, err := v.tx.QueryContext(ctx, observationQuery, id, highWater, start, cappedEnd)
	if err != nil {
		return ports.Observation{}, err
	}
	defer rows.Close()
	result := ports.Observation{}
	var sum int64
	for rows.Next() {
		var tokens, failed int64
		if err := rows.Scan(&tokens, &failed); err != nil {
			return ports.Observation{}, err
		}
		if result.Count == math.MaxInt64 {
			result.Overflow = true
		} else {
			result.Count++
		}
		if metric == resourcepolicy.MetricToken {
			if tokens < 0 || failed == 0 && tokens == 0 || failed != 0 && failed != 1 {
				result.TokenIncomplete = true
			}
			if tokens >= 0 && !result.Overflow {
				if tokens > math.MaxInt64-sum {
					result.Overflow = true
				} else {
					sum += tokens
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return ports.Observation{}, err
	}
	if !result.Overflow {
		result.TokenSum = &sum
	}
	return result, nil
}
