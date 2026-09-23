package decisionstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/gatewaydecision"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/decisionstore"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type repository struct{ db *sql.DB }

func New(db *sql.DB) ports.Repository { return &repository{db: db} }

const eventColumns = `decision_id, schema_version, dedupe_key, api_key_id, policy_id,
	policy_revision, binding_revision, metric, enforcement, action, outcome, reason_code,
	limit_value, observed_value, window_start_ms, window_end_ms, source_usage_event_id,
	source_event_hash, evidence_timestamp_ms, evaluated_at_ms`

func (r *repository) Append(ctx context.Context, event gatewaydecision.QuotaDecisionEvent) (gatewaydecision.QuotaDecisionEvent, bool, error) {
	if err := event.Validate(); err != nil {
		return gatewaydecision.QuotaDecisionEvent{}, false, err
	}
	_, err := r.db.ExecContext(ctx, `insert into gateway_quota_decision_events_v1 (`+eventColumns+`)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.DecisionID, event.SchemaVersion, event.DedupeKey, event.APIKeyID, event.PolicyID,
		event.PolicyRevision, event.BindingRevision, event.Metric, event.Enforcement, event.Action,
		event.Outcome, event.ReasonCode, event.LimitValue, event.ObservedValue,
		event.WindowStartMS, event.WindowEndMS, event.SourceUsageEventID, event.SourceEventHash,
		event.EvidenceTimestampMS, event.EvaluatedAtMS)
	if err == nil {
		return event, true, nil
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && (sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY) {
		first, loadErr := r.loadByDedupeKey(ctx, event.DedupeKey)
		if loadErr == nil {
			if first.SameSemanticContent(event) {
				return first, false, nil
			}
			return gatewaydecision.QuotaDecisionEvent{}, false, ports.ErrDedupeConflict
		}
		if !errors.Is(loadErr, ports.ErrNotFound) {
			return gatewaydecision.QuotaDecisionEvent{}, false, loadErr
		}
	}
	return gatewaydecision.QuotaDecisionEvent{}, false, fmt.Errorf("insert quota decision event: %w", err)
}

func (r *repository) LoadByID(ctx context.Context, id gatewaydecision.DecisionID) (gatewaydecision.QuotaDecisionEvent, error) {
	if err := id.Validate(); err != nil {
		return gatewaydecision.QuotaDecisionEvent{}, err
	}
	return loadEvent(r.db.QueryRowContext(ctx, `select `+eventColumns+`
		from gateway_quota_decision_events_v1 where decision_id = ?`, id))
}

func (r *repository) loadByDedupeKey(ctx context.Context, key gatewaydecision.DedupeKey) (gatewaydecision.QuotaDecisionEvent, error) {
	return loadEvent(r.db.QueryRowContext(ctx, `select `+eventColumns+`
		from gateway_quota_decision_events_v1 where dedupe_key = ?`, key))
}

func loadEvent(row *sql.Row) (gatewaydecision.QuotaDecisionEvent, error) {
	var event gatewaydecision.QuotaDecisionEvent
	var policyRevision, bindingRevision int64
	var observed, start, end sql.NullInt64
	err := row.Scan(&event.DecisionID, &event.SchemaVersion, &event.DedupeKey,
		&event.APIKeyID, &event.PolicyID, &policyRevision, &bindingRevision, &event.Metric,
		&event.Enforcement, &event.Action, &event.Outcome, &event.ReasonCode,
		&event.LimitValue, &observed, &start, &end, &event.SourceUsageEventID,
		&event.SourceEventHash, &event.EvidenceTimestampMS, &event.EvaluatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return gatewaydecision.QuotaDecisionEvent{}, ports.ErrNotFound
	}
	if err != nil {
		return gatewaydecision.QuotaDecisionEvent{}, err
	}
	event.PolicyRevision = resourcepolicy.Revision(policyRevision)
	event.BindingRevision = resourcepolicy.Revision(bindingRevision)
	if observed.Valid {
		event.ObservedValue = &observed.Int64
	}
	if start.Valid {
		event.WindowStartMS = &start.Int64
	}
	if end.Valid {
		event.WindowEndMS = &end.Int64
	}
	if err := event.Validate(); err != nil {
		return gatewaydecision.QuotaDecisionEvent{}, fmt.Errorf("persisted quota decision event invalid: %w", err)
	}
	return event, nil
}
