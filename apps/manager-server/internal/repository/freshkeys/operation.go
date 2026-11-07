package freshkeys

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
)

const latestOperationSetting = "fresh_key_operation_latest_v1"
const operationPrefix = "fresh_key_operation_v1:"

// Operation records attempts, not a reusable live-auth grant. Secrets remain in
// the original encrypted canonical records; the S1 journal is never overwritten.
type Operation struct {
	ParentID           string    `json:"parentId,omitempty"`
	SourceID           string    `json:"sourceId,omitempty"`
	ClaimSHA256        string    `json:"claimSHA256,omitempty"`
	ClaimRevision      uint64    `json:"claimRevision,omitempty"`
	IntentDigest       string    `json:"intentDigest,omitempty"`
	ID                 string    `json:"id"`
	TargetID           string    `json:"targetId"`
	KeeperID           string    `json:"keeperId"`
	Mode               string    `json:"mode"`
	RunID              string    `json:"runId"`
	Generation         string    `json:"generation"`
	ProviderSeedSHA256 string    `json:"providerSeedSHA256"`
	Revision           int64     `json:"revision"`
	Outcome            string    `json:"outcome"`
	Steps              []KeyStep `json:"steps"`
	CreatedAtMS        int64     `json:"createdAtMs"`
	CompletedAtMS      int64     `json:"completedAtMs"`
}
type KeyStep struct {
	RuntimeID     string `json:"runtimeId,omitempty"`
	BeginRevision uint64 `json:"beginRevision,omitempty"`
	HTTP200       bool   `json:"http200,omitempty"`
	Receipt       string `json:"receipt,omitempty"`
	Name          string `json:"name"`
	State         string `json:"state"` // unattempted, attempted, observed
	Returned      bool   `json:"returned"`
}

func readOperation(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (*Operation, error) {
	var data string
	err := q.QueryRowContext(ctx, "select value from settings where key=?", operationPrefix+id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var o Operation
	if json.Unmarshal([]byte(data), &o) != nil || o.ID != id || o.Revision < 1 || !validOperation(o) {
		return nil, errors.New("invalid key operation journal")
	}
	return &o, nil
}
func validOperation(o Operation) bool {
	for _, id := range []string{o.ID, o.TargetID, o.KeeperID} {
		if _, err := identity.ParseAPIKeyID(id); err != nil {
			return false
		}
	}
	if o.TargetID == o.KeeperID || o.RunID == "" || o.Generation == "" || (o.Outcome != "pending" && o.Outcome != "unknown" && o.Outcome != "committed") {
		return false
	}
	if o.ParentID != "" && (o.ParentID != "k:"+o.ID || len(o.SourceID) < 16 || o.ClaimRevision == 0 || !identity.IsValidSHA256Hex(o.ClaimSHA256) || !identity.IsValidSHA256Hex(o.IntentDigest)) {
		return false
	}
	if o.ParentID == "" && (o.SourceID != "" || o.ClaimSHA256 != "" || o.ClaimRevision != 0 || o.IntentDigest != "") {
		return false
	}
	names := []string{"revoke"}
	if o.Mode == "replace" {
		names = []string{"add", "revoke"}
	} else if o.Mode != "revoke" {
		return false
	}
	if len(o.Steps) != len(names) {
		return false
	}
	for i, s := range o.Steps {
		if o.ParentID != "" {
			ordinal := "0"
			if i == 1 {
				ordinal = "1"
			}
			if s.RuntimeID != o.ParentID+":"+ordinal+":"+names[i] || (s.HTTP200 && (s.BeginRevision <= o.ClaimRevision || !s.Returned)) || (s.State == "observed" && (!s.HTTP200 || !identity.IsValidSHA256Hex(s.Receipt))) {
				return false
			}
			if (s.State == "unattempted" && (s.Returned || s.HTTP200 || s.BeginRevision != 0 || s.Receipt != "")) || (s.Receipt != "" && (!s.HTTP200 || !identity.IsValidSHA256Hex(s.Receipt))) {
				return false
			}
		} else if s.RuntimeID != "" || s.BeginRevision != 0 || s.HTTP200 || s.Receipt != "" {
			return false
		}
		if s.Name != names[i] || (s.State != "unattempted" && s.State != "attempted" && s.State != "observed") {
			return false
		}
		if i > 0 && s.State != "unattempted" && o.Steps[i-1].State != "observed" {
			return false
		}
	}
	return o.Outcome != "committed" || o.Steps[len(o.Steps)-1].State == "observed"
}
func (r *Repository) Operation(ctx context.Context, id string) (*Operation, error) {
	return readOperation(ctx, r.db, id)
}
func (r *Repository) LatestOperation(ctx context.Context) (*Operation, error) {
	var id string
	err := r.db.QueryRowContext(ctx, "select value from settings where key=?", latestOperationSetting).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o, err := r.Operation(ctx, id)
	if err == nil && o == nil {
		return nil, errors.New("missing latest key operation journal")
	}
	return o, err
}

func (r *Repository) BeginOperation(ctx context.Context, o Operation) error {
	if !validOperation(o) || o.Revision != 1 {
		return ErrConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var latest string
	err = tx.QueryRowContext(ctx, "select value from settings where key=?", latestOperationSetting).Scan(&latest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if latest != "" {
		previous, e := readOperation(ctx, tx, latest)
		if e != nil {
			return e
		}
		if previous == nil || previous.Outcome != "committed" {
			return ErrConflict
		}
	}
	data, _ := json.Marshal(o)
	if _, err = tx.ExecContext(ctx, "insert into settings(key,value,updated_at_ms) values(?,?,?)", operationPrefix+o.ID, string(data), time.Now().UnixMilli()); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at_ms=excluded.updated_at_ms", latestOperationSetting, o.ID, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// SaveOperation is a journal CAS, never a claim of upstream CAS. The final
// identity transition and operation result commit in the same SQLite transaction.
func (r *Repository) SaveOperation(ctx context.Context, o Operation) error {
	if !validOperation(o) {
		return ErrConflict
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	previous, err := readOperation(ctx, tx, o.ID)
	if err != nil {
		return err
	}
	if previous == nil || previous.Revision != o.Revision || previous.Outcome == "committed" {
		return ErrConflict
	}
	if previous.ParentID != "" || o.ParentID != "" {
		if previous.ParentID != o.ParentID || previous.SourceID != o.SourceID || previous.ClaimSHA256 != o.ClaimSHA256 || previous.ClaimRevision != o.ClaimRevision || previous.IntentDigest != o.IntentDigest || previous.TargetID != o.TargetID || previous.KeeperID != o.KeeperID || previous.Mode != o.Mode || previous.RunID != o.RunID || previous.Generation != o.Generation || previous.ProviderSeedSHA256 != o.ProviderSeedSHA256 {
			return ErrConflict
		}
		for n, p := range previous.Steps {
			s := o.Steps[n]
			if p.RuntimeID != s.RuntimeID || (p.State == "observed" && p != s) || (p.State == "attempted" && s.State == "unattempted") || (p.HTTP200 && !s.HTTP200) || (p.BeginRevision != 0 && p.BeginRevision != s.BeginRevision) || (p.Receipt != "" && p.Receipt != s.Receipt) {
				return ErrConflict
			}
		}
	}
	o.Revision++
	data, _ := json.Marshal(o)
	old, _ := json.Marshal(previous)
	result, err := tx.ExecContext(ctx, "update settings set value=?,updated_at_ms=? where key=? and value=?", string(data), time.Now().UnixMilli(), operationPrefix+o.ID, string(old))
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
	if o.Outcome == "committed" {
		result, err = tx.ExecContext(ctx, "update gateway_api_key_identities set lifecycle='superseded',revision=revision+1,updated_at_ms=? where id=? and lifecycle='active'", o.CompletedAtMS, o.TargetID)
		if err != nil {
			return err
		}
		n, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
		// Close the superseded key's attribution window at the same instant. The
		// binding keeps its first_seen value, so every event already recorded for
		// that key stays attributed while the terminal identity holds no active
		// binding and a replacement can never inherit its history.
		if _, err = tx.ExecContext(ctx, `update gateway_api_key_source_bindings
			set retired_at_ms = case when ? > first_seen_at_ms then ? else first_seen_at_ms end
			where api_key_id = ? and retired_at_ms is null`, o.CompletedAtMS, o.CompletedAtMS, o.TargetID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
