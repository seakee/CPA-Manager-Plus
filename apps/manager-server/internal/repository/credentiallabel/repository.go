package credentiallabel

import (
	"context"
	"database/sql"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

type Repository interface {
	List(context.Context) ([]model.CredentialLabel, error)
	Set(context.Context, string, string) (model.CredentialLabel, error)
}
type repository struct{ db *sql.DB }

func New(db *sql.DB) Repository { return &repository{db: db} }
func (r *repository) List(ctx context.Context) ([]model.CredentialLabel, error) {
	rows, err := r.db.QueryContext(ctx, `select key_hash, label, updated_at_ms from credential_labels order by key_hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := []model.CredentialLabel{}
	for rows.Next() {
		var label model.CredentialLabel
		if err := rows.Scan(&label.KeyHash, &label.Label, &label.UpdatedAtMS); err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}
func (r *repository) Set(ctx context.Context, keyHash, label string) (model.CredentialLabel, error) {
	result := model.CredentialLabel{KeyHash: keyHash, Label: label, UpdatedAtMS: time.Now().UnixMilli()}
	var err error
	if label == "" {
		_, err = r.db.ExecContext(ctx, `delete from credential_labels where key_hash = ?`, keyHash)
	} else {
		_, err = r.db.ExecContext(ctx, `insert into credential_labels(key_hash, label, updated_at_ms) values(?, ?, ?)
   on conflict(key_hash) do update set label=excluded.label, updated_at_ms=excluded.updated_at_ms`, keyHash, label, result.UpdatedAtMS)
	}
	return result, err
}
