package freshkeys

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const initialInstallSetting = "fresh_s1_initial_key_install_v1"

// A one-shot mutation journal, not a second secret or Key identity authority.
// It is committed before any network write. Unknown attempts are never removed.
type InitialInstall struct {
	KeyID              string `json:"keyId"`
	RunID              string `json:"runId"`
	Generation         string `json:"generation"`
	AttemptedAtMS      int64  `json:"attemptedAtMs"`
	ProviderSeedSHA256 string `json:"providerSeedSHA256,omitempty"`
}

func (r *Repository) ClaimInitialInstall(ctx context.Context, v InitialInstall) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do nothing`, initialInstallSetting, string(data), time.Now().UnixMilli())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (r *Repository) InitialInstall(ctx context.Context) (*InitialInstall, error) {
	return initialInstall(ctx, r.db)
}

func initialInstall(ctx context.Context, q queryer) (*InitialInstall, error) {
	var data string
	err := q.QueryRowContext(ctx, `select value from settings where key=?`, initialInstallSetting).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v InitialInstall
	if json.Unmarshal([]byte(data), &v) != nil || v.KeyID == "" || v.RunID == "" || v.Generation == "" || v.AttemptedAtMS <= 0 {
		return nil, errors.New("invalid S1 mutation journal")
	}
	return &v, nil
}

// RegisterAdoption stages every adopted Client Key and claims (or verifies a
// matching replay of) the one-shot initial key install journal in one atomic
// SQLite transaction. Any key or journal conflict rolls back the entire batch.
func (r *Repository) RegisterAdoption(ctx context.Context, candidates []Record, runtimeIdentity string, journal InitialInstall) ([]Record, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	records, err := r.RegisterAdoptionTx(ctx, tx, candidates, runtimeIdentity, journal)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return records, nil
}

// RegisterAdoptionTx performs the same atomic adopted-key registration and
// initial-install journal claim inside a caller-owned write transaction.
func (r *Repository) RegisterAdoptionTx(ctx context.Context, tx *sql.Tx, candidates []Record, runtimeIdentity string, journal InitialInstall) ([]Record, error) {
	if tx == nil || journal.RunID == "" || journal.Generation == "" || journal.AttemptedAtMS <= 0 {
		return nil, errors.New("invalid adopted installation journal")
	}
	records, err := stageAdoptedTx(ctx, tx, candidates, runtimeIdentity)
	if err != nil {
		return nil, err
	}
	journal.KeyID = string(records[0].Identity.ID)
	data, err := json.Marshal(journal)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do nothing`, initialInstallSetting, string(data), time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 1 {
		return records, nil
	}
	existing, err := initialInstall(ctx, tx)
	if err != nil {
		return nil, err
	}
	if existing == nil || existing.RunID != journal.RunID || existing.Generation != journal.Generation ||
		existing.ProviderSeedSHA256 != journal.ProviderSeedSHA256 {
		return nil, ErrJournalConflict
	}
	for _, record := range records {
		if string(record.Identity.ID) == existing.KeyID {
			return records, nil
		}
	}
	return nil, ErrJournalConflict
}
