// Package reconnect persists self-service reconnect requests and settings.
package reconnect

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

const settingsKey = "reconnect_settings_v1"

type Repository interface {
	LoadSettings(ctx context.Context) (model.ReconnectSettings, error)
	SaveSettings(ctx context.Context, settings model.ReconnectSettings) (model.ReconnectSettings, error)

	Create(ctx context.Context, request *model.ReconnectRequest) error
	Delete(ctx context.Context, id int64) error
	GetByTokenHash(ctx context.Context, tokenHash string) (*model.ReconnectRequest, error)
	// GetOpen returns the newest pending request for provider+email, or nil.
	GetOpen(ctx context.Context, provider string, email string) (*model.ReconnectRequest, error)
	LastCompletionMS(ctx context.Context, provider string, email string) (int64, error)
	ListPending(ctx context.Context) ([]model.ReconnectRequest, error)
	ListSince(ctx context.Context, sinceMS int64) ([]model.ReconnectRequest, error)
	Update(ctx context.Context, id int64, fields map[string]any) error
	// ClosePending moves every pending request for provider+email to status.
	ClosePending(ctx context.Context, provider string, email string, status string, nowMS int64) error
}

type repository struct {
	db        *sql.DB
	protector *security.Protector
}

func New(db *sql.DB, protector ...*security.Protector) Repository {
	var p *security.Protector
	if len(protector) > 0 {
		p = protector[0]
	}
	return &repository{db: db, protector: p}
}

func (r *repository) LoadSettings(ctx context.Context) (model.ReconnectSettings, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `select value from settings where key = ?`, settingsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DefaultReconnectSettings(), nil
	}
	if err != nil {
		return model.ReconnectSettings{}, err
	}
	settings := model.DefaultReconnectSettings()
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return model.ReconnectSettings{}, err
	}
	if r.protector != nil {
		webhook, err := r.protector.UnprotectString(settings.WebhookURL)
		if err != nil {
			return model.ReconnectSettings{}, err
		}
		settings.WebhookURL = webhook
	}
	return settings.Normalized(), nil
}

func (r *repository) SaveSettings(ctx context.Context, settings model.ReconnectSettings) (model.ReconnectSettings, error) {
	settings = settings.Normalized()
	settings.UpdatedAtMS = time.Now().UnixMilli()
	stored := settings
	if r.protector != nil {
		webhook, err := r.protector.ProtectString(settings.WebhookURL)
		if err != nil {
			return model.ReconnectSettings{}, err
		}
		stored.WebhookURL = webhook
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return model.ReconnectSettings{}, err
	}
	_, err = r.db.ExecContext(ctx,
		`insert into settings(key, value, updated_at_ms) values(?, ?, ?)
		 on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		settingsKey, string(data), settings.UpdatedAtMS)
	if err != nil {
		return model.ReconnectSettings{}, err
	}
	return settings, nil
}

const requestColumns = `id, provider, email, auth_file_name, token_hash, status, purpose, manual, reason,
	oauth_state, oauth_started_at_ms, oauth_deadline_ms, known_auth_files, followup_count,
	created_at_ms, expires_at_ms, notified_at_ms, completed_at_ms`

type scanner interface{ Scan(dest ...any) error }

func scanRequest(row scanner) (*model.ReconnectRequest, error) {
	var req model.ReconnectRequest
	var manual int
	if err := row.Scan(&req.ID, &req.Provider, &req.Email, &req.AuthFileName, &req.TokenHash, &req.Status,
		&req.Purpose, &manual, &req.Reason, &req.OAuthState, &req.OAuthStartedMS, &req.OAuthDeadline,
		&req.KnownAuthFiles, &req.FollowupCount, &req.CreatedAtMS, &req.ExpiresAtMS, &req.NotifiedAtMS,
		&req.CompletedAtMS); err != nil {
		return nil, err
	}
	req.Manual = manual != 0
	return &req, nil
}

func (r *repository) Create(ctx context.Context, req *model.ReconnectRequest) error {
	manual := 0
	if req.Manual {
		manual = 1
	}
	res, err := r.db.ExecContext(ctx,
		`insert into reconnect_requests(provider, email, auth_file_name, token_hash, status, purpose, manual, reason,
			oauth_state, oauth_started_at_ms, oauth_deadline_ms, known_auth_files, followup_count,
			created_at_ms, expires_at_ms, notified_at_ms, completed_at_ms)
		 values(?, ?, ?, ?, ?, ?, ?, ?, '', 0, 0, '', 0, ?, ?, 0, 0)`,
		req.Provider, strings.ToLower(strings.TrimSpace(req.Email)), req.AuthFileName, req.TokenHash, req.Status,
		req.Purpose, manual, req.Reason, req.CreatedAtMS, req.ExpiresAtMS)
	if err != nil {
		return err
	}
	req.ID, err = res.LastInsertId()
	return err
}

func (r *repository) Delete(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `delete from reconnect_requests where id = ?`, id)
	return err
}

func (r *repository) one(ctx context.Context, query string, args ...any) (*model.ReconnectRequest, error) {
	req, err := scanRequest(r.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return req, err
}

func (r *repository) many(ctx context.Context, query string, args ...any) ([]model.ReconnectRequest, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ReconnectRequest
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *req)
	}
	return out, rows.Err()
}

func (r *repository) GetByTokenHash(ctx context.Context, tokenHash string) (*model.ReconnectRequest, error) {
	return r.one(ctx, `select `+requestColumns+` from reconnect_requests where token_hash = ?`, tokenHash)
}

func (r *repository) GetOpen(ctx context.Context, provider string, email string) (*model.ReconnectRequest, error) {
	return r.one(ctx, `select `+requestColumns+` from reconnect_requests
		where provider = ? and email = ? and status = ? order by id desc limit 1`,
		provider, strings.ToLower(strings.TrimSpace(email)), model.ReconnectStatusPending)
}

func (r *repository) LastCompletionMS(ctx context.Context, provider string, email string) (int64, error) {
	var ms sql.NullInt64
	err := r.db.QueryRowContext(ctx, `select max(completed_at_ms) from reconnect_requests
		where provider = ? and email = ? and status = ?`,
		provider, strings.ToLower(strings.TrimSpace(email)), model.ReconnectStatusCompleted).Scan(&ms)
	if err != nil {
		return 0, err
	}
	return ms.Int64, nil
}

func (r *repository) ListPending(ctx context.Context) ([]model.ReconnectRequest, error) {
	return r.many(ctx, `select `+requestColumns+` from reconnect_requests where status = ? order by id`, model.ReconnectStatusPending)
}

func (r *repository) ListSince(ctx context.Context, sinceMS int64) ([]model.ReconnectRequest, error) {
	return r.many(ctx, `select `+requestColumns+` from reconnect_requests
		where created_at_ms >= ? or status = ? order by id`, sinceMS, model.ReconnectStatusPending)
}

// updatableColumns guards Update against arbitrary column names.
var updatableColumns = map[string]bool{
	"token_hash": true, "status": true, "purpose": true, "oauth_state": true, "oauth_started_at_ms": true,
	"oauth_deadline_ms": true, "known_auth_files": true, "followup_count": true, "expires_at_ms": true,
	"notified_at_ms": true, "completed_at_ms": true,
}

func (r *repository) Update(ctx context.Context, id int64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	sets := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields)+1)
	for column, value := range fields {
		if !updatableColumns[column] {
			return errors.New("reconnect: unknown column " + column)
		}
		sets = append(sets, column+" = ?")
		args = append(args, value)
	}
	args = append(args, id)
	_, err := r.db.ExecContext(ctx, `update reconnect_requests set `+strings.Join(sets, ", ")+` where id = ?`, args...)
	return err
}

func (r *repository) ClosePending(ctx context.Context, provider string, email string, status string, nowMS int64) error {
	_, err := r.db.ExecContext(ctx, `update reconnect_requests set status = ?, completed_at_ms = ?
		where provider = ? and email = ? and status = ?`,
		status, nowMS, provider, strings.ToLower(strings.TrimSpace(email)), model.ReconnectStatusPending)
	return err
}
