package identityprojection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	appidentity "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityprojection"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	StateName            = "canonical_identity_v1"
	CurrentSchemaVersion = 1
	defaultBatchLimit    = 500
	maxErrorBytes        = 1024
)

type repository struct {
	db          *sql.DB
	catchUpGate chan struct{}
}

// New creates a new SQLite-backed repository for canonical identity shadow projections.
func New(db *sql.DB) ports.Repository {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &repository{
		db:          db,
		catchUpGate: gate,
	}
}

func (r *repository) acquireCatchUp(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.catchUpGate:
		return nil
	}
}

func (r *repository) releaseCatchUp() {
	select {
	case r.catchUpGate <- struct{}{}:
	default:
	}
}

func (r *repository) GetState(ctx context.Context) (ports.State, error) {
	row := r.db.QueryRowContext(ctx, `SELECT
		state_name, schema_version, status, last_processed_event_id,
		target_event_id, processed_events, last_run_started_at_ms,
		updated_at_ms, finished_at_ms, COALESCE(last_error, '')
	FROM gateway_usage_identity_projection_state
	WHERE state_name = ?`, StateName)

	var s ports.State
	var lastRun, finished sql.NullInt64
	if err := row.Scan(
		&s.StateName,
		&s.SchemaVersion,
		&s.Status,
		&s.LastProcessedEventID,
		&s.TargetEventID,
		&s.ProcessedEvents,
		&lastRun,
		&s.UpdatedAtMS,
		&finished,
		&s.LastError,
	); err != nil {
		return ports.State{}, err
	}
	if lastRun.Valid {
		s.LastRunStartedAtMS = &lastRun.Int64
	}
	if finished.Valid {
		s.FinishedAtMS = &finished.Int64
	}
	return s, nil
}

func (r *repository) GetProjectionByEventID(ctx context.Context, eventID int64) (*ports.UsageIdentityProjection, error) {
	row := r.db.QueryRowContext(ctx, `SELECT
		usage_event_id, event_hash, request_id, evidence_timestamp_ms,
		api_key_state, api_key_id, api_key_source_hash,
		credential_state, credential_id, credential_source_auth_id,
		schema_version, projected_at_ms
	FROM gateway_usage_identity_projection_v1
	WHERE usage_event_id = ?`, eventID)

	return scanProjection(row)
}

func (r *repository) GetProjectionByEventHash(ctx context.Context, eventHash string) (*ports.UsageIdentityProjection, error) {
	row := r.db.QueryRowContext(ctx, `SELECT
		usage_event_id, event_hash, request_id, evidence_timestamp_ms,
		api_key_state, api_key_id, api_key_source_hash,
		credential_state, credential_id, credential_source_auth_id,
		schema_version, projected_at_ms
	FROM gateway_usage_identity_projection_v1
	WHERE event_hash = ?`, eventHash)

	return scanProjection(row)
}

func scanProjection(scanner interface{ Scan(dest ...any) error }) (*ports.UsageIdentityProjection, error) {
	var p ports.UsageIdentityProjection
	var apiKeyID, credID sql.NullString

	if err := scanner.Scan(
		&p.UsageEventID,
		&p.EventHash,
		&p.RequestID,
		&p.EvidenceTimestampMS,
		&p.APIKeyState,
		&apiKeyID,
		&p.APIKeySourceHash,
		&p.CredentialState,
		&credID,
		&p.CredentialSourceAuthID,
		&p.SchemaVersion,
		&p.ProjectedAtMS,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if apiKeyID.Valid {
		p.APIKeyID = &apiKeyID.String
	}
	if credID.Valid {
		p.CredentialID = &credID.String
	}
	return &p, nil
}

func (r *repository) CatchUp(ctx context.Context, limit int, nowMS int64) (ports.CatchUpResult, error) {
	if limit <= 0 {
		limit = defaultBatchLimit
	}
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}

	if err := r.acquireCatchUp(ctx); err != nil {
		return ports.CatchUpResult{}, err
	}
	defer r.releaseCatchUp()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ports.CatchUpResult{}, fmt.Errorf("begin catch-up tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Update last_run_started_at_ms to mark active run
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_usage_identity_projection_state SET
		last_run_started_at_ms = ?
	WHERE state_name = ? AND schema_version = ?`, nowMS, StateName, CurrentSchemaVersion); err != nil {
		return ports.CatchUpResult{}, fmt.Errorf("update last run started: %w", err)
	}

	// Read current checkpoint state
	var state ports.State
	var lastRun, finished sql.NullInt64
	row := tx.QueryRowContext(ctx, `SELECT
		state_name, schema_version, status, last_processed_event_id,
		target_event_id, processed_events, last_run_started_at_ms,
		updated_at_ms, finished_at_ms, COALESCE(last_error, '')
	FROM gateway_usage_identity_projection_state
	WHERE state_name = ? AND schema_version = ?`, StateName, CurrentSchemaVersion)

	if err := row.Scan(
		&state.StateName,
		&state.SchemaVersion,
		&state.Status,
		&state.LastProcessedEventID,
		&state.TargetEventID,
		&state.ProcessedEvents,
		&lastRun,
		&state.UpdatedAtMS,
		&finished,
		&state.LastError,
	); err != nil {
		return ports.CatchUpResult{}, fmt.Errorf("read projection state: %w", err)
	}

	var latestID int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM usage_events`).Scan(&latestID); err != nil {
		return ports.CatchUpResult{}, fmt.Errorf("query max usage_event id: %w", err)
	}

	targetEventID := state.TargetEventID
	if targetEventID == 0 || (state.LastProcessedEventID >= targetEventID && latestID > targetEventID) {
		targetEventID = latestID
	}
	if targetEventID < state.LastProcessedEventID {
		targetEventID = state.LastProcessedEventID
	}

	// Read next bounded batch of usage_events strictly by id order
	eventRows, err := tx.QueryContext(ctx, `SELECT
		id, event_hash, COALESCE(request_id, ''), timestamp_ms,
		COALESCE(api_key_hash, ''), COALESCE(raw_json, '')
	FROM usage_events
	WHERE id > ?
	ORDER BY id ASC
	LIMIT ?`, state.LastProcessedEventID, limit)
	if err != nil {
		return ports.CatchUpResult{}, fmt.Errorf("query usage events batch: %w", err)
	}
	defer eventRows.Close()

	var batch []appidentity.EventInput
	for eventRows.Next() {
		var ev appidentity.EventInput
		if err := eventRows.Scan(
			&ev.UsageEventID,
			&ev.EventHash,
			&ev.RequestID,
			&ev.TimestampMS,
			&ev.APIKeyHash,
			&ev.RawJSON,
		); err != nil {
			return ports.CatchUpResult{}, fmt.Errorf("scan usage event: %w", err)
		}
		batch = append(batch, ev)
	}
	if err := eventRows.Err(); err != nil {
		return ports.CatchUpResult{}, fmt.Errorf("iterate usage events: %w", err)
	}
	eventRows.Close()

	// If no new events in batch, evaluate if caught up
	if len(batch) == 0 {
		pending := latestID > state.LastProcessedEventID
		status := "ready"
		var finishedAtVal any = nowMS
		if pending {
			status = "backfilling"
			finishedAtVal = nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_usage_identity_projection_state SET
			status = ?,
			target_event_id = ?,
			updated_at_ms = ?,
			finished_at_ms = ?,
			last_error = NULL
		WHERE state_name = ? AND schema_version = ?`,
			status, targetEventID, nowMS, finishedAtVal, StateName, CurrentSchemaVersion); err != nil {
			return ports.CatchUpResult{}, fmt.Errorf("update finished state: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return ports.CatchUpResult{}, fmt.Errorf("commit catch-up tx: %w", err)
		}
		return ports.CatchUpResult{
			Processed:            0,
			LastProcessedEventID: state.LastProcessedEventID,
			TargetEventID:        targetEventID,
			Pending:              pending,
			Rebuilt:              false,
		}, nil
	}

	// Prepare queries for bindings and projection insert
	apiKeyStmt, err := tx.PrepareContext(ctx, `SELECT
		api_key_id, runtime_identity, first_seen_at_ms, COALESCE(retired_at_ms, 0)
	FROM gateway_api_key_source_bindings
	WHERE api_key_hash = ?`)
	if err != nil {
		return ports.CatchUpResult{}, fmt.Errorf("prepare api key bindings query: %w", err)
	}
	defer apiKeyStmt.Close()

	credStmt, err := tx.PrepareContext(ctx, `SELECT
		credential_id, runtime_identity, first_seen_at_ms, COALESCE(retired_at_ms, 0)
	FROM gateway_credential_source_bindings
	WHERE source_auth_id = ?`)
	if err != nil {
		return ports.CatchUpResult{}, fmt.Errorf("prepare credential bindings query: %w", err)
	}
	defer credStmt.Close()

	insertProjStmt, err := tx.PrepareContext(ctx, `INSERT INTO gateway_usage_identity_projection_v1 (
		usage_event_id, event_hash, request_id, evidence_timestamp_ms,
		api_key_state, api_key_id, api_key_source_hash,
		credential_state, credential_id, credential_source_auth_id,
		schema_version, projected_at_ms
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(usage_event_id) DO UPDATE SET
		event_hash = excluded.event_hash,
		request_id = excluded.request_id,
		evidence_timestamp_ms = excluded.evidence_timestamp_ms,
		api_key_state = excluded.api_key_state,
		api_key_id = excluded.api_key_id,
		api_key_source_hash = excluded.api_key_source_hash,
		credential_state = excluded.credential_state,
		credential_id = excluded.credential_id,
		credential_source_auth_id = excluded.credential_source_auth_id,
		schema_version = excluded.schema_version,
		projected_at_ms = excluded.projected_at_ms`)
	if err != nil {
		return ports.CatchUpResult{}, fmt.Errorf("prepare projection insert: %w", err)
	}
	defer insertProjStmt.Close()

	for _, ev := range batch {
		var apiKeyBindings []appidentity.SourceBindingRecord
		if strings.TrimSpace(ev.APIKeyHash) != "" {
			rows, qErr := apiKeyStmt.QueryContext(ctx, strings.TrimSpace(ev.APIKeyHash))
			if qErr != nil {
				return ports.CatchUpResult{}, fmt.Errorf("query api key bindings for event %d: %w", ev.UsageEventID, qErr)
			}
			for rows.Next() {
				var b appidentity.SourceBindingRecord
				if err := rows.Scan(&b.CanonicalID, &b.RuntimeIdentity, &b.FirstSeenAtMS, &b.RetiredAtMS); err != nil {
					rows.Close()
					return ports.CatchUpResult{}, fmt.Errorf("scan api key binding: %w", err)
				}
				apiKeyBindings = append(apiKeyBindings, b)
			}
			rows.Close()
		}

		var credBindings []appidentity.SourceBindingRecord
		sourceAuthID, credState := appidentity.ExtractCredentialSourceAuthID(ev.RawJSON)
		if credState != ports.StateAmbiguous && credState != ports.StateUnknown && sourceAuthID != "" {
			rows, qErr := credStmt.QueryContext(ctx, sourceAuthID)
			if qErr != nil {
				return ports.CatchUpResult{}, fmt.Errorf("query cred bindings for event %d: %w", ev.UsageEventID, qErr)
			}
			for rows.Next() {
				var b appidentity.SourceBindingRecord
				if err := rows.Scan(&b.CanonicalID, &b.RuntimeIdentity, &b.FirstSeenAtMS, &b.RetiredAtMS); err != nil {
					rows.Close()
					return ports.CatchUpResult{}, fmt.Errorf("scan cred binding: %w", err)
				}
				credBindings = append(credBindings, b)
			}
			rows.Close()
		}

		proj, mapErr := appidentity.MapEvent(ev, apiKeyBindings, credBindings, nowMS)
		if mapErr != nil {
			return ports.CatchUpResult{}, fmt.Errorf("map event %d: %w", ev.UsageEventID, mapErr)
		}

		var apiKeyIDVal, credIDVal any
		if proj.APIKeyID != nil {
			apiKeyIDVal = *proj.APIKeyID
		}
		if proj.CredentialID != nil {
			credIDVal = *proj.CredentialID
		}

		if _, err := insertProjStmt.ExecContext(
			ctx,
			proj.UsageEventID,
			proj.EventHash,
			proj.RequestID,
			proj.EvidenceTimestampMS,
			string(proj.APIKeyState),
			apiKeyIDVal,
			proj.APIKeySourceHash,
			string(proj.CredentialState),
			credIDVal,
			proj.CredentialSourceAuthID,
			proj.SchemaVersion,
			proj.ProjectedAtMS,
		); err != nil {
			return ports.CatchUpResult{}, fmt.Errorf("insert projection for event %d: %w", proj.UsageEventID, err)
		}
	}

	newLastID := batch[len(batch)-1].UsageEventID
	newProcessed := state.ProcessedEvents + int64(len(batch))
	pending := newLastID < latestID
	status := "ready"
	var finishedAtVal any = nowMS
	if pending {
		status = "backfilling"
		finishedAtVal = nil
	}

	if _, err := tx.ExecContext(ctx, `UPDATE gateway_usage_identity_projection_state SET
		status = ?,
		last_processed_event_id = ?,
		target_event_id = ?,
		processed_events = ?,
		updated_at_ms = ?,
		finished_at_ms = ?,
		last_error = NULL
	WHERE state_name = ? AND schema_version = ?`,
		status, newLastID, targetEventID, newProcessed, nowMS, finishedAtVal, StateName, CurrentSchemaVersion); err != nil {
		return ports.CatchUpResult{}, fmt.Errorf("update checkpoint state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return ports.CatchUpResult{}, fmt.Errorf("commit batch catch-up tx: %w", err)
	}

	return ports.CatchUpResult{
		Processed:            len(batch),
		LastProcessedEventID: newLastID,
		TargetEventID:        targetEventID,
		Pending:              pending,
		Rebuilt:              false,
	}, nil
}

func (r *repository) RecordFailure(ctx context.Context, catchUpErr error, nowMS int64) error {
	if catchUpErr == nil {
		return nil
	}
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	sanitized := sanitizeError(catchUpErr.Error())

	_, err := r.db.ExecContext(ctx, `UPDATE gateway_usage_identity_projection_state SET
		status = 'error',
		last_error = ?,
		updated_at_ms = ?
	WHERE state_name = ? AND schema_version = ?`,
		sanitized, nowMS, StateName, CurrentSchemaVersion)
	return err
}

func (r *repository) Reset(ctx context.Context) error {
	if err := r.acquireCatchUp(ctx); err != nil {
		return err
	}
	defer r.releaseCatchUp()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin reset tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_usage_identity_projection_v1`); err != nil {
		return fmt.Errorf("truncate projection table: %w", err)
	}

	nowMS := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_usage_identity_projection_state SET
		status = 'pending',
		last_processed_event_id = 0,
		target_event_id = 0,
		processed_events = 0,
		last_run_started_at_ms = NULL,
		finished_at_ms = NULL,
		last_error = NULL,
		updated_at_ms = ?
	WHERE state_name = ? AND schema_version = ?`, nowMS, StateName, CurrentSchemaVersion); err != nil {
		return fmt.Errorf("reset checkpoint state: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reset tx: %w", err)
	}
	return nil
}

func sanitizeError(msg string) string {
	cleaned := usage.FailSummaryFromBody(msg)
	if len(cleaned) > maxErrorBytes {
		cleaned = cleaned[:maxErrorBytes]
	}
	return strings.TrimSpace(cleaned)
}
