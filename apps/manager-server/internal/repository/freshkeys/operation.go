package freshkeys

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
)

const latestOperationSetting = "fresh_key_operation_latest_v1"
const latestTargetAddSettingPrefix = "fresh_key_operation_target_add_v1:"
const operationPrefix = "fresh_key_operation_v1:"
const (
	OutcomePending   = "pending"
	OutcomeUnknown   = "unknown"
	OutcomeCommitted = "committed"
	OutcomeAborted   = "aborted"
)

// Operation records attempts, not a reusable live-auth grant. Secrets remain in
// the original encrypted canonical records; the S1 journal is never overwritten.
type Operation struct {
	ParentID           string `json:"parentId,omitempty"`
	SourceID           string `json:"sourceId,omitempty"`
	ClaimSHA256        string `json:"claimSHA256,omitempty"`
	ClaimRevision      uint64 `json:"claimRevision,omitempty"`
	IntentDigest       string `json:"intentDigest,omitempty"`
	ID                 string `json:"id"`
	TargetID           string `json:"targetId"`
	KeeperID           string `json:"keeperId"`
	Mode               string `json:"mode"`
	RunID              string `json:"runId"`
	Generation         string `json:"generation"`
	ProviderSeedSHA256 string `json:"providerSeedSHA256"`
	// SnapshotSHA256 is the sorted digest set of the destination's Client Keys as
	// Prepare validated them. Every step is a transformation of that one set, so a
	// resume compares the live list against what was proved — never against a list
	// re-read after the fact. It is a set of digests: the journal still holds no
	// secret.
	SnapshotSHA256 []string  `json:"snapshotSha256,omitempty"`
	Revision       int64     `json:"revision"`
	Outcome        string    `json:"outcome"`
	Steps          []KeyStep `json:"steps"`
	CreatedAtMS    int64     `json:"createdAtMs"`
	CompletedAtMS  int64     `json:"completedAtMs"`
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

// IsTerminal reports whether this journal entry has reached an immutable
// terminal outcome ("committed" or "aborted") and no longer owns the key set.
func (o Operation) IsTerminal() bool {
	return o.Outcome == OutcomeCommitted || o.Outcome == OutcomeAborted
}

// AllStepsUnattempted reports whether every step of this operation remains
// unattempted with no upstream write or receipt recorded.
func (o Operation) AllStepsUnattempted() bool {
	if len(o.Steps) == 0 {
		return false
	}
	for _, s := range o.Steps {
		if s.State != "unattempted" || s.Returned || s.HTTP200 || s.BeginRevision != 0 || s.Receipt != "" {
			return false
		}
	}
	return true
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
	for _, id := range []string{o.ID, o.TargetID} {
		if _, err := identity.ParseAPIKeyID(id); err != nil {
			return false
		}
	}
	switch o.Mode {
	case "add":
		if o.KeeperID != "" {
			return false
		}
	case "revoke":
		if o.KeeperID != "" {
			if _, err := identity.ParseAPIKeyID(o.KeeperID); err != nil || o.TargetID == o.KeeperID {
				return false
			}
		}
	case "replace":
		if _, err := identity.ParseAPIKeyID(o.KeeperID); err != nil || o.TargetID == o.KeeperID {
			return false
		}
	default:
		return false
	}
	if o.RunID == "" || o.Generation == "" || (o.Outcome != OutcomePending && o.Outcome != OutcomeUnknown && o.Outcome != OutcomeCommitted && o.Outcome != OutcomeAborted) {
		return false
	}
	if len(o.SnapshotSHA256) > 0 {
		for i, digest := range o.SnapshotSHA256 {
			if !identity.IsValidSHA256Hex(digest) || (i > 0 && o.SnapshotSHA256[i-1] >= digest) {
				return false
			}
		}
	}
	if o.ParentID != "" && (o.ParentID != "k:"+o.ID || len(o.SourceID) < 16 || o.ClaimRevision == 0 || !identity.IsValidSHA256Hex(o.ClaimSHA256) || !identity.IsValidSHA256Hex(o.IntentDigest)) {
		return false
	}
	if o.ParentID == "" && (o.SourceID != "" || o.ClaimSHA256 != "" || o.ClaimRevision != 0 || o.IntentDigest != "") {
		return false
	}
	var names []string
	switch o.Mode {
	case "add":
		names = []string{"add"}
	case "revoke":
		names = []string{"revoke"}
	case "replace":
		names = []string{"add", "revoke"}
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
	if o.Outcome == OutcomeAborted {
		return o.ParentID == "" && o.CompletedAtMS > 0 && o.AllStepsUnattempted()
	}
	return o.Outcome != OutcomeCommitted || o.Steps[len(o.Steps)-1].State == "observed"
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

// LatestAddOperation returns the newest durable "add" operation recorded for
// canonical targetID, falling back to fallbackOpID (for example key.RequestID)
// when no per-target add pointer exists yet.
func (r *Repository) LatestAddOperation(ctx context.Context, targetID, fallbackOpID string) (*Operation, error) {
	if _, err := identity.ParseAPIKeyID(targetID); err != nil {
		return nil, ErrConflict
	}
	var id string
	err := r.db.QueryRowContext(ctx, "select value from settings where key=?", latestTargetAddSettingPrefix+targetID).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && id != "" {
		o, opErr := r.Operation(ctx, id)
		if opErr != nil {
			return nil, opErr
		}
		if o == nil || o.TargetID != targetID || o.Mode != "add" || o.KeeperID != "" {
			return nil, errors.New("missing or mismatched target add operation journal")
		}
		return o, nil
	}
	if fallbackOpID == "" {
		return nil, nil
	}
	if _, parseErr := identity.ParseAPIKeyID(fallbackOpID); parseErr != nil {
		return nil, nil
	}
	o, opErr := r.Operation(ctx, fallbackOpID)
	if opErr != nil || o == nil {
		return o, opErr
	}
	if o.TargetID != targetID || o.Mode != "add" || o.KeeperID != "" {
		return nil, nil
	}
	return o, nil
}

func (r *Repository) BeginOperation(ctx context.Context, o Operation) error {
	if !validOperation(o) || o.Revision != 1 || o.Outcome == OutcomeAborted {
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
		if previous == nil || !previous.IsTerminal() {
			return ErrConflict
		}
	}
	nowMS := time.Now().UnixMilli()
	data, _ := json.Marshal(o)
	if _, err = tx.ExecContext(ctx, "insert into settings(key,value,updated_at_ms) values(?,?,?)", operationPrefix+o.ID, string(data), nowMS); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at_ms=excluded.updated_at_ms", latestOperationSetting, o.ID, nowMS)
	if err != nil {
		return err
	}
	if o.Mode == "add" && o.KeeperID == "" {
		_, err = tx.ExecContext(ctx, "insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at_ms=excluded.updated_at_ms", latestTargetAddSettingPrefix+o.TargetID, o.ID, nowMS)
		if err != nil {
			return err
		}
	}
	if o.Outcome == OutcomeCommitted {
		if err = commitOperationIdentityTx(ctx, tx, o); err != nil {
			return err
		}
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
	if previous == nil || previous.Revision != o.Revision || previous.IsTerminal() {
		return ErrConflict
	}
	if o.Outcome == OutcomeAborted && (previous.Outcome != OutcomePending || !previous.AllStepsUnattempted() ||
		previous.TargetID != o.TargetID || previous.KeeperID != o.KeeperID || previous.Mode != o.Mode ||
		previous.RunID != o.RunID || previous.Generation != o.Generation) {
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
	if o.Outcome == OutcomeCommitted {
		if err = commitOperationIdentityTx(ctx, tx, o); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func loadOperationIdentityTx(ctx context.Context, tx *sql.Tx, id string) (identity.APIKeyIdentity, bool, error) {
	var (
		rawID     string
		rawRev    int64
		rawLife   string
		createdMS int64
		updatedMS int64
	)
	err := tx.QueryRowContext(ctx, `select id, revision, lifecycle, created_at_ms, updated_at_ms
		from gateway_api_key_identities where id = ?`, id).
		Scan(&rawID, &rawRev, &rawLife, &createdMS, &updatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.APIKeyIdentity{}, false, nil
	}
	if err != nil {
		return identity.APIKeyIdentity{}, false, err
	}
	if rawRev < 1 {
		return identity.APIKeyIdentity{}, false, errors.New("invalid client key identity")
	}
	ent := identity.APIKeyIdentity{
		ID:          identity.APIKeyID(rawID),
		Revision:    identity.Revision(rawRev),
		Lifecycle:   identity.Lifecycle(rawLife),
		CreatedAtMS: createdMS,
		UpdatedAtMS: updatedMS,
	}
	if err := ent.Validate(); err != nil {
		return identity.APIKeyIdentity{}, false, err
	}
	return ent, true, nil
}

func commitOperationIdentityTx(ctx context.Context, tx *sql.Tx, o Operation) error {
	ent, found, err := loadOperationIdentityTx(ctx, tx, o.TargetID)
	if err != nil {
		return err
	}
	if o.Mode == "add" {
		if !found {
			return nil
		}
		if ent.Lifecycle == identity.LifecycleSuperseded {
			return ErrConflict
		}
		if ent.Lifecycle == identity.LifecycleMissing {
			_, err = recoverMissingIdentityTx(ctx, tx, ent, o.CompletedAtMS)
			return err
		}
		return nil
	}
	if !found {
		return ErrConflict
	}
	if ent.Lifecycle != identity.LifecycleActive && ent.Lifecycle != identity.LifecycleMissing {
		return ErrConflict
	}
	if o.KeeperID != "" {
		keeperEnt, keeperFound, err := loadOperationIdentityTx(ctx, tx, o.KeeperID)
		if err != nil {
			return err
		}
		if keeperFound {
			if keeperEnt.Lifecycle == identity.LifecycleSuperseded {
				return ErrConflict
			}
			if keeperEnt.Lifecycle == identity.LifecycleMissing {
				if _, err = recoverMissingIdentityTx(ctx, tx, keeperEnt, o.CompletedAtMS); err != nil {
					return err
				}
			}
		}
	}
	if ent.Revision >= math.MaxInt64 {
		return errors.New("canonical identity revision overflow")
	}
	nextUpdated, err := nextIdentityUpdatedAt(ent.UpdatedAtMS, o.CompletedAtMS)
	if err != nil {
		return err
	}
	nextRev := ent.Revision + 1
	result, err := tx.ExecContext(ctx, `update gateway_api_key_identities
		set lifecycle = ?, revision = ?, updated_at_ms = ?
		where id = ? and lifecycle = ? and revision = ?`,
		string(identity.LifecycleSuperseded), int64(nextRev), nextUpdated,
		o.TargetID, string(ent.Lifecycle), int64(ent.Revision))
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
	// Close the superseded key's half-open attribution window [first_seen_at_ms, retired_at_ms)
	// strictly above max(first_seen_at_ms, last_seen_at_ms) so same-millisecond and
	// backward-skewed commits keep every recorded event attributed without collapsing [t, t).
	type activeTargetBinding struct {
		bindingID     int64
		firstSeenAtMS int64
		lastSeenAtMS  int64
	}
	bRows, err := tx.QueryContext(ctx, `select binding_id, first_seen_at_ms, last_seen_at_ms
		from gateway_api_key_source_bindings
		where api_key_id = ? and retired_at_ms is null
		order by binding_id asc`, o.TargetID)
	if err != nil {
		return err
	}
	var bindings []activeTargetBinding
	for bRows.Next() {
		var b activeTargetBinding
		if err = bRows.Scan(&b.bindingID, &b.firstSeenAtMS, &b.lastSeenAtMS); err != nil {
			_ = bRows.Close()
			return err
		}
		bindings = append(bindings, b)
	}
	if err = bRows.Err(); err != nil {
		_ = bRows.Close()
		return err
	}
	if err = bRows.Close(); err != nil {
		return err
	}
	for _, b := range bindings {
		if b.firstSeenAtMS <= 0 || b.lastSeenAtMS < b.firstSeenAtMS {
			return errors.New("invalid client key source binding")
		}
		floor := max(b.firstSeenAtMS, b.lastSeenAtMS)
		if floor == math.MaxInt64 {
			return errors.New("client key source binding timestamp overflow")
		}
		retiredAtMS := max(o.CompletedAtMS, floor+1)
		res, err := tx.ExecContext(ctx, `update gateway_api_key_source_bindings
			set retired_at_ms = ?
			where binding_id = ? and retired_at_ms is null`,
			retiredAtMS, b.bindingID)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return ErrConflict
		}
	}
	return nil
}
