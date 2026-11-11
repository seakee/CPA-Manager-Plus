package freshkeys

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	initialInstallSetting       = "fresh_s1_initial_key_install_v1"
	adoptionRegistrationSetting = "fresh_s1_adoption_registration_v1"
)

// A one-shot mutation journal, not a second secret or Key identity authority.
// It is committed before any network write. Unknown attempts are never removed.
type InitialInstall struct {
	KeyID              string `json:"keyId"`
	RunID              string `json:"runId"`
	Generation         string `json:"generation"`
	AttemptedAtMS      int64  `json:"attemptedAtMs"`
	ProviderSeedSHA256 string `json:"providerSeedSHA256,omitempty"`
	// AdoptedSetSHA256 is set only by RegisterAdoptionTx (`CommitAdoptionTx`)
	// alongside `fresh_s1_adoption_registration_v1`, binding the journal to the
	// canonical adopted key set and runtime identity. ClaimInitialInstall never
	// writes it.
	AdoptedSetSHA256 string `json:"adoptedSetSHA256,omitempty"`
}

type adoptionRegistrationRecord struct {
	RunID              string   `json:"runId"`
	Generation         string   `json:"generation"`
	ProviderSeedSHA256 string   `json:"providerSeedSHA256,omitempty"`
	RuntimeIdentity    string   `json:"runtimeIdentity,omitempty"`
	KeyIDs             []string `json:"keyIds"`
	SetSHA256          string   `json:"setSHA256"`
}

func hashAdoptedKeyIDs(runtimeIdentity string, keyIDs []string) string {
	sorted := slices.Clone(keyIDs)
	slices.Sort(sorted)
	h := sha256.New()
	_, _ = h.Write([]byte(runtimeIdentity))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(h.Sum(nil))
}

func (r *Repository) ClaimInitialInstall(ctx context.Context, v InitialInstall) error {
	if v.AdoptedSetSHA256 != "" {
		return errors.New("adoption set digest is reserved for adoption registration")
	}
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
	keyIDs := make([]string, 0, len(records))
	for _, rec := range records {
		id := string(rec.Identity.ID)
		if !slices.Contains(keyIDs, id) {
			keyIDs = append(keyIDs, id)
		}
	}
	setSHA256 := hashAdoptedKeyIDs(runtimeIdentity, keyIDs)
	journal.KeyID = string(records[0].Identity.ID)
	journal.AdoptedSetSHA256 = setSHA256
	data, err := json.Marshal(journal)
	if err != nil {
		return nil, err
	}
	regData, err := json.Marshal(adoptionRegistrationRecord{
		RunID:              journal.RunID,
		Generation:         journal.Generation,
		ProviderSeedSHA256: journal.ProviderSeedSHA256,
		RuntimeIdentity:    runtimeIdentity,
		KeyIDs:             keyIDs,
		SetSHA256:          setSHA256,
	})
	if err != nil {
		return nil, err
	}
	nowMS := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do nothing`, initialInstallSetting, string(data), nowMS)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 1 {
		regRes, err := tx.ExecContext(ctx, `insert into settings(key,value,updated_at_ms) values(?,?,?) on conflict(key) do nothing`, adoptionRegistrationSetting, string(regData), nowMS)
		if err != nil {
			return nil, err
		}
		regN, err := regRes.RowsAffected()
		if err != nil {
			return nil, err
		}
		if regN != 1 {
			return nil, ErrJournalConflict
		}
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
	if existing.AdoptedSetSHA256 == "" {
		var existingReg string
		err = tx.QueryRowContext(ctx, `select value from settings where key=?`, adoptionRegistrationSetting).Scan(&existingReg)
		if err == nil {
			return nil, ErrJournalConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `update settings set value=?, updated_at_ms=? where key=?`, string(data), nowMS, initialInstallSetting); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `insert into settings(key,value,updated_at_ms) values(?,?,?)`, adoptionRegistrationSetting, string(regData), nowMS); err != nil {
			return nil, err
		}
		return records, nil
	}
	if !slices.Contains(keyIDs, existing.KeyID) {
		return nil, ErrJournalConflict
	}
	ready, err := verifyAdoptionRegistration(ctx, tx, journal.RunID, journal.Generation, journal.ProviderSeedSHA256, runtimeIdentity, nil)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, ErrJournalConflict
	}
	return records, nil
}

// VerifyAdoptionRegistration checks whether this installation holds atomic
// CommitAdoptionTx registration evidence (`AdoptedSetSHA256` +
// `fresh_s1_adoption_registration_v1` + canonical key and runtime source-binding
// rows). It returns `(false, nil)` when no adoption registration exists yet
// (including when only a non-adoption ClaimInitialInstall row exists for the
// same run/generation/seed), and returns `ErrJournalConflict` when a foreign or
// inconsistent journal is present.
func (r *Repository) VerifyAdoptionRegistration(ctx context.Context, runID, generation, providerSeedSHA256, runtimeIdentity string) (bool, error) {
	// Read the initial-install journal, adoption-registration record, canonical
	// keys, and source bindings in one read-only SQLite snapshot so a concurrent
	// completion/backfill commit cannot mix pre-commit and post-commit rows into
	// a spurious ErrJournalConflict.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var afterInitialRead func()
	if testing.Testing() {
		if hook := r.verifyAfterInitialInstallHook.Load(); hook != nil {
			afterInitialRead = *hook
		}
	}
	return verifyAdoptionRegistration(ctx, tx, runID, generation, providerSeedSHA256, runtimeIdentity, afterInitialRead)
}

// SetVerifyAfterInitialInstallHookForTest installs a deterministic test seam
// invoked inside VerifyAdoptionRegistration immediately after reading the
// initial-install journal row and before reading the adoption-registration
// record. It panics if called outside `go test`.
func (r *Repository) SetVerifyAfterInitialInstallHookForTest(hook func()) {
	if !testing.Testing() {
		panic("SetVerifyAfterInitialInstallHookForTest called outside tests")
	}
	if hook == nil {
		r.verifyAfterInitialInstallHook.Store(nil)
		return
	}
	r.verifyAfterInitialInstallHook.Store(&hook)
}

func verifyAdoptionRegistration(ctx context.Context, q queryer, runID, generation, providerSeedSHA256, runtimeIdentity string, afterInitialRead func()) (bool, error) {
	inst, err := initialInstall(ctx, q)
	if err != nil {
		return false, err
	}
	if afterInitialRead != nil {
		afterInitialRead()
	}
	var rawReg string
	regErr := q.QueryRowContext(ctx, `select value from settings where key=?`, adoptionRegistrationSetting).Scan(&rawReg)
	hasReg := regErr == nil
	if regErr != nil && !errors.Is(regErr, sql.ErrNoRows) {
		return false, regErr
	}
	if inst == nil {
		if hasReg {
			return false, ErrJournalConflict
		}
		return false, nil
	}
	if inst.RunID != runID || inst.Generation != generation || inst.ProviderSeedSHA256 != providerSeedSHA256 {
		return false, ErrJournalConflict
	}
	if inst.AdoptedSetSHA256 == "" {
		if hasReg {
			return false, ErrJournalConflict
		}
		return false, nil
	}
	if !hasReg {
		return false, ErrJournalConflict
	}
	var reg adoptionRegistrationRecord
	if json.Unmarshal([]byte(rawReg), &reg) != nil || len(reg.KeyIDs) == 0 {
		return false, ErrJournalConflict
	}
	if reg.RunID != runID || reg.Generation != generation || reg.ProviderSeedSHA256 != providerSeedSHA256 ||
		reg.RuntimeIdentity != runtimeIdentity || reg.SetSHA256 != inst.AdoptedSetSHA256 ||
		hashAdoptedKeyIDs(reg.RuntimeIdentity, reg.KeyIDs) != inst.AdoptedSetSHA256 ||
		!slices.Contains(reg.KeyIDs, inst.KeyID) {
		return false, ErrJournalConflict
	}
	seen := make(map[string]struct{}, len(reg.KeyIDs))
	for _, keyID := range reg.KeyIDs {
		if keyID == "" {
			return false, ErrJournalConflict
		}
		if _, dup := seen[keyID]; dup {
			return false, ErrJournalConflict
		}
		seen[keyID] = struct{}{}
		var secretHash string
		err := q.QueryRowContext(ctx, `select k.secret_hash from fresh_client_api_keys k
			join gateway_api_key_identities i on i.id=k.api_key_id where k.api_key_id=?`, keyID).Scan(&secretHash)
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrJournalConflict
		}
		if err != nil {
			return false, err
		}
		if runtimeIdentity != "" {
			var bound bool
			if err = q.QueryRowContext(ctx, `select exists(
				select 1 from gateway_api_key_source_bindings
				where api_key_id = ? and runtime_identity = ? and api_key_hash = ?)`,
				keyID, runtimeIdentity, secretHash).Scan(&bound); err != nil {
				return false, err
			}
			if !bound {
				return false, ErrJournalConflict
			}
		}
	}
	return true, nil
}
