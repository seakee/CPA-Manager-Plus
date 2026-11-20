// Package freshkeys stores staged client secrets, their canonical identities and
// the runtime source binding that lets usage events be attributed to them, all
// in one transaction. It creates no runtime acknowledgments.
package freshkeys

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

type Record struct {
	RequestID       string
	Identity        identity.APIKeyIdentity
	Ciphertext      string
	Hash            string
	Preview         string
	Name            string
	NameRevision    int64
	Origin          string
	LastRotatedAtMS int64
	RevokedAtMS     int64
}

type RotationRecord struct {
	RequestID     string
	APIKeyID      identity.APIKeyID
	OldHash       string
	NewCiphertext string
	NewHash       string
	NewPreview    string
	CreatedAtMS   int64
}

type SecretHistoryRecord struct {
	Hash               string
	APIKeyID           identity.APIKeyID
	Ciphertext         string
	Preview            string
	Origin             string
	RetiredAtMS        int64
	RetiredByRequestID string
	NextHash           string
}

// Client Key origins. A workspace key is created by the Key workspace; the
// installation mints exactly one installer key so CPA never starts with client
// authentication disabled; an adopted key is one an adopted destination already
// carried, registered at adoption completion so the destination's own
// credentials are managed here as well. The origin is storage vocabulary, not an
// authorization: every canonical Key is read through the same projections.
const (
	OriginWorkspace = "workspace"
	OriginInstaller = "installer"
	OriginAdopted   = "adopted"
)

var ErrConflict = errors.New("staged key name revision conflict")
var ErrNotFound = errors.New("staged key not found")

// ErrSourceOwned means the staged key's source hash already has an active
// binding to another identity, so no second attribution may be created.
var ErrSourceOwned = errors.New("staged key source is bound to another identity")

// ErrRetired means a staged key's secret digest already belongs to a canonical
// identity whose lifecycle is no longer active.
var ErrRetired = errors.New("staged key identity is retired")

// ErrJournalConflict means the one-shot initial key install journal is already
// bound to a different installation run, generation, provider seed or key.
var ErrJournalConflict = errors.New("initial key install journal is bound to another installation")

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Repository struct {
	db                            *sql.DB
	verifyAfterInitialInstallHook atomic.Pointer[func()]
}

func New(db *sql.DB) *Repository { return &Repository{db: db} }

// List returns every staged key in creation order. Keys created inside the
// same millisecond share created_at_ms, so the staged row's insertion order
// breaks the tie; ordering by the random identity would silently reorder them.
func (r *Repository) List(ctx context.Context) ([]Record, error) {
	rows, err := r.db.QueryContext(ctx, `select k.request_id, i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		k.secret_ciphertext, k.secret_hash, k.masked_preview, k.name, k.name_revision, k.origin,
		coalesce((select max(h.retired_at_ms) from fresh_client_api_key_secret_history h where h.api_key_id = k.api_key_id), 0)
		from fresh_client_api_keys k
		join gateway_api_key_identities i on i.id=k.api_key_id order by i.created_at_ms, k.rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Record{}
	for rows.Next() {
		var v Record
		if err = rows.Scan(&v.RequestID, &v.Identity.ID, &v.Identity.Revision, &v.Identity.Lifecycle, &v.Identity.CreatedAtMS, &v.Identity.UpdatedAtMS, &v.Ciphertext, &v.Hash, &v.Preview, &v.Name, &v.NameRevision, &v.Origin, &v.LastRotatedAtMS); err != nil {
			return nil, err
		}
		if v.Identity.Validate() != nil || v.NameRevision < 1 || !validRecordName(v.Name) || !security.IsValidProtectedEnvelope(v.Ciphertext) || !validOrigin(v.Origin) || v.LastRotatedAtMS < 0 {
			return nil, errors.New("invalid staged client key")
		}
		if v.Identity.Lifecycle == identity.LifecycleSuperseded {
			v.RevokedAtMS = v.Identity.UpdatedAtMS
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// Rename changes only display metadata. A duplicate/stale submission cannot
// overwrite a newer edit, even if the first response was lost.
func (r *Repository) Rename(ctx context.Context, id identity.APIKeyID, name string, revision int64) error {
	if revision < 1 || revision >= math.MaxInt64 {
		return ErrConflict
	}
	result, err := r.db.ExecContext(ctx, `update fresh_client_api_keys set name=?, name_revision=name_revision+1
		where api_key_id=? and name_revision=?`, name, id, revision)
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

// Create returns false on an already committed request; the replay never
// returns the old secret. A uniqueness collision cannot create a second identity.
//
// When the committed installation's runtime identity is known, the source
// binding is written in the same transaction: the Canonical Key and its
// attribution window become durable together, so a later passive snapshot can
// never mint a second Canonical ID for the same secret hash.
func (r *Repository) Create(ctx context.Context, v Record, runtimeIdentity string) (bool, error) {
	if v.Origin == "" {
		v.Origin = OriginWorkspace
	}
	if v.NameRevision <= 0 {
		v.NameRevision = 1
	}
	if v.Identity.Validate() != nil || !validRecordName(v.Name) || !security.IsValidProtectedEnvelope(v.Ciphertext) || !identity.IsValidSHA256Hex(v.Hash) || v.Origin != OriginWorkspace {
		return false, errors.New("invalid staged client key")
	}
	if runtimeIdentity != "" && (strings.TrimSpace(runtimeIdentity) != runtimeIdentity || v.Identity.CreatedAtMS <= 0) {
		return false, errors.New("invalid staged client key source")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, "select count(*) from fresh_client_api_keys where request_id=?", v.RequestID).Scan(&exists); err != nil {
		return false, err
	}
	if exists != 0 {
		return false, nil
	}
	var rotReqExists int
	if err = tx.QueryRowContext(ctx, "select count(*) from fresh_client_api_key_rotations where request_id=?", v.RequestID).Scan(&rotReqExists); err != nil {
		return false, err
	}
	if rotReqExists != 0 {
		return false, ErrConflict
	}
	if existingOp, opErr := readOperation(ctx, tx, v.RequestID); opErr != nil {
		return false, opErr
	} else if existingOp != nil && (existingOp.Mode != "add" || existingOp.KeeperID != "") {
		return false, ErrConflict
	}
	existingByHash, err := byHash(ctx, tx, v.Hash)
	if err != nil {
		return false, err
	}
	if existingByHash != nil {
		if existingByHash.Identity.Lifecycle == identity.LifecycleSuperseded {
			return false, ErrRetired
		}
		return false, ErrSourceOwned
	}
	var histHashExists bool
	if err = tx.QueryRowContext(ctx, "select exists(select 1 from fresh_client_api_key_secret_history where secret_hash = ?)", v.Hash).Scan(&histHashExists); err != nil {
		return false, err
	}
	if histHashExists {
		return false, ErrRetired
	}
	var rotHashExists int
	if err = tx.QueryRowContext(ctx, "select count(*) from fresh_client_api_key_rotations where new_secret_hash=?", v.Hash).Scan(&rotHashExists); err != nil {
		return false, err
	}
	if rotHashExists != 0 {
		return false, ErrSourceOwned
	}
	activeRows, retiredRows, err := inspectSourceHashBindingsTx(ctx, tx, v.Hash)
	if err != nil {
		return false, err
	}
	if len(activeRows) == 0 && len(retiredRows) > 0 {
		return false, ErrRetired
	}
	if len(activeRows) > 0 {
		for _, row := range activeRows {
			if row.hasIdentity && row.identity.Lifecycle == identity.LifecycleSuperseded {
				return false, ErrRetired
			}
		}
		return false, ErrSourceOwned
	}
	i := v.Identity
	if _, err = tx.ExecContext(ctx, "insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms) values(?,?,?,?,?)", i.ID, i.Revision, i.Lifecycle, i.CreatedAtMS, i.UpdatedAtMS); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "insert into fresh_client_api_keys(request_id,api_key_id,secret_ciphertext,secret_hash,masked_preview,name,name_revision,origin) values(?,?,?,?,?,?,?,?)", v.RequestID, i.ID, v.Ciphertext, v.Hash, v.Preview, v.Name, v.NameRevision, v.Origin); err != nil {
		return false, err
	}
	if runtimeIdentity != "" {
		if err = bindSource(ctx, tx, i.ID, runtimeIdentity, v.Hash, i.CreatedAtMS); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// Installer returns the single installation-minted Client Key, or nil while the
// installation has not created it. The partial unique index keeps the origin
// singular, so this is the idempotent lookup a retried install replays.
func (r *Repository) Installer(ctx context.Context) (*Record, error) {
	return r.byOrigin(ctx, OriginInstaller)
}

func (r *Repository) byOrigin(ctx context.Context, origin string) (*Record, error) {
	var v Record
	err := r.db.QueryRowContext(ctx, `select k.request_id, i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		k.secret_ciphertext, k.secret_hash, k.masked_preview, k.name, k.name_revision, k.origin,
		coalesce((select max(h.retired_at_ms) from fresh_client_api_key_secret_history h where h.api_key_id = k.api_key_id), 0)
		from fresh_client_api_keys k
		join gateway_api_key_identities i on i.id=k.api_key_id where k.origin=?`, origin).
		Scan(&v.RequestID, &v.Identity.ID, &v.Identity.Revision, &v.Identity.Lifecycle, &v.Identity.CreatedAtMS, &v.Identity.UpdatedAtMS, &v.Ciphertext, &v.Hash, &v.Preview, &v.Name, &v.NameRevision, &v.Origin, &v.LastRotatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v.Identity.Validate() != nil || v.NameRevision < 1 || !validRecordName(v.Name) || !security.IsValidProtectedEnvelope(v.Ciphertext) || !validOrigin(v.Origin) || v.LastRotatedAtMS < 0 {
		return nil, errors.New("invalid staged client key")
	}
	if v.Identity.Lifecycle == identity.LifecycleSuperseded {
		v.RevokedAtMS = v.Identity.UpdatedAtMS
	}
	return &v, nil
}

// CreateInstaller persists the installation-minted Client Key. It returns false
// when the installation already owns one: the caller must replay that record
// instead of minting a second key. The unique partial index is the durable
// fence, so concurrent install attempts cannot both win.
func (r *Repository) CreateInstaller(ctx context.Context, v Record) (bool, error) {
	v.Origin = OriginInstaller
	if v.Identity.Validate() != nil || !security.IsValidProtectedEnvelope(v.Ciphertext) || !identity.IsValidSHA256Hex(v.Hash) || v.Preview == "" || v.Identity.CreatedAtMS <= 0 {
		return false, errors.New("invalid install client key")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, "select count(*) from fresh_client_api_keys where origin=?", OriginInstaller).Scan(&exists); err != nil {
		return false, err
	}
	if exists != 0 {
		return false, nil
	}
	i := v.Identity
	if _, err = tx.ExecContext(ctx, "insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms) values(?,?,?,?,?)", i.ID, i.Revision, i.Lifecycle, i.CreatedAtMS, i.UpdatedAtMS); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "insert into fresh_client_api_keys(request_id,api_key_id,secret_ciphertext,secret_hash,masked_preview,name,origin) values(?,?,?,?,?,?,?)", v.RequestID, i.ID, v.Ciphertext, v.Hash, v.Preview, v.Name, v.Origin); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// StageAdopted persists every Client Key of an adopted destination in one
// transaction. Either every record (reusing an active or passively missing
// identity that already owns the digest, or creating the identity, encrypted
// record and optional source binding) commits together, or none do.
func (r *Repository) StageAdopted(ctx context.Context, candidates []Record, runtimeIdentity string) ([]Record, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("invalid adopted client key repository")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	records, err := r.StageAdoptedTx(ctx, tx, candidates, runtimeIdentity)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return records, nil
}

// StageAdoptedTx persists every Client Key of an adopted or upgraded
// destination inside a caller-owned write transaction.
func (r *Repository) StageAdoptedTx(ctx context.Context, tx *sql.Tx, candidates []Record, runtimeIdentity string) ([]Record, error) {
	if r == nil || tx == nil {
		return nil, errors.New("invalid adopted client key transaction")
	}
	return stageAdoptedTx(ctx, tx, candidates, runtimeIdentity)
}

func stageAdoptedTx(ctx context.Context, tx *sql.Tx, candidates []Record, runtimeIdentity string) ([]Record, error) {
	if len(candidates) == 0 {
		return nil, errors.New("invalid adopted client key")
	}
	if runtimeIdentity != "" && strings.TrimSpace(runtimeIdentity) != runtimeIdentity {
		return nil, errors.New("invalid adopted client key source")
	}
	resolved := make([]Record, 0, len(candidates))
	for _, v := range candidates {
		v.Origin = OriginAdopted
		if v.Name == "" {
			alias, aliasErr := lookupAdoptedAliasTx(ctx, tx, v.Hash)
			if aliasErr != nil {
				return nil, aliasErr
			}
			if alias != "" {
				v.Name = alias
				v.NameRevision = 2
			} else {
				v.NameRevision = 1
			}
		} else {
			v.Name = sanitizeAdoptedAlias(v.Name)
			if v.Name == "" {
				v.NameRevision = 1
			} else if v.NameRevision < 1 {
				v.NameRevision = 1
			}
		}
		if v.Identity.Validate() != nil || !validRecordName(v.Name) || !security.IsValidProtectedEnvelope(v.Ciphertext) || !identity.IsValidSHA256Hex(v.Hash) || v.Preview == "" || v.Identity.CreatedAtMS <= 0 {
			return nil, errors.New("invalid adopted client key")
		}
		handoffAtMS := max(v.Identity.UpdatedAtMS, v.Identity.CreatedAtMS)

		existing, err := byHash(ctx, tx, v.Hash)
		if err != nil {
			return nil, err
		}
		activeRows, retiredRows, err := inspectSourceHashBindingsTx(ctx, tx, v.Hash)
		if err != nil {
			return nil, err
		}

		if existing != nil {
			if existing.Identity.Lifecycle == identity.LifecycleSuperseded {
				return nil, ErrRetired
			}
			if len(activeRows) == 0 && len(retiredRows) > 0 {
				return nil, ErrRetired
			}
			for _, row := range activeRows {
				if !row.hasIdentity || row.identity.Validate() != nil {
					return nil, errors.New("invalid pre-existing client key identity")
				}
				if row.identity.Lifecycle == identity.LifecycleSuperseded {
					return nil, ErrRetired
				}
				if row.apiKeyID != existing.Identity.ID {
					return nil, ErrSourceOwned
				}
			}
			otherHashConflict, err := hasOtherActiveHashForIdentityTx(ctx, tx, existing.Identity.ID, existing.Hash)
			if err != nil {
				return nil, err
			}
			if otherHashConflict {
				return nil, ErrSourceOwned
			}
			if existing.Identity.Lifecycle == identity.LifecycleMissing {
				existing.Identity, err = recoverMissingIdentityTx(ctx, tx, existing.Identity, handoffAtMS)
				if err != nil {
					return nil, err
				}
			}
			if runtimeIdentity != "" {
				if _, err = handoffOrBindSourceTx(
					ctx, tx, existing.Identity.ID, existing.Hash,
					existing.Identity.CreatedAtMS, existing.Identity.UpdatedAtMS,
					handoffAtMS, runtimeIdentity, activeRows,
				); err != nil {
					return nil, err
				}
			}
			resolved = append(resolved, *existing)
			continue
		}

		var requestExists int
		if err = tx.QueryRowContext(ctx, "select count(*) from fresh_client_api_keys where request_id=?", v.RequestID).Scan(&requestExists); err != nil {
			return nil, err
		}
		if requestExists != 0 {
			return nil, errors.New("duplicate adopted client key request")
		}

		var histCandidateExists bool
		if err = tx.QueryRowContext(ctx, "select exists(select 1 from fresh_client_api_key_secret_history where secret_hash = ?)", v.Hash).Scan(&histCandidateExists); err != nil {
			return nil, err
		}
		if histCandidateExists {
			return nil, ErrRetired
		}
		var rotCandidateExists bool
		if err = tx.QueryRowContext(ctx, "select exists(select 1 from fresh_client_api_key_rotations where new_secret_hash = ?)", v.Hash).Scan(&rotCandidateExists); err != nil {
			return nil, err
		}
		if rotCandidateExists {
			return nil, ErrSourceOwned
		}
		if len(activeRows) == 0 && len(retiredRows) > 0 {
			return nil, ErrRetired
		}
		if len(activeRows) > 0 {
			for _, row := range activeRows {
				if !row.hasIdentity || row.identity.Validate() != nil {
					return nil, errors.New("invalid pre-existing client key identity")
				}
				if row.identity.Lifecycle == identity.LifecycleSuperseded {
					return nil, ErrRetired
				}
			}
			boundIdentity := activeRows[0].identity
			for _, row := range activeRows[1:] {
				if row.apiKeyID != boundIdentity.ID {
					return nil, ErrSourceOwned
				}
			}
			var keyIDAlreadyStaged bool
			if err = tx.QueryRowContext(ctx, "select exists(select 1 from fresh_client_api_keys where api_key_id=?)", string(boundIdentity.ID)).Scan(&keyIDAlreadyStaged); err != nil {
				return nil, err
			}
			if keyIDAlreadyStaged {
				return nil, ErrSourceOwned
			}
			otherHashConflict, err := hasOtherActiveHashForIdentityTx(ctx, tx, boundIdentity.ID, v.Hash)
			if err != nil {
				return nil, err
			}
			if otherHashConflict {
				return nil, ErrSourceOwned
			}
			if boundIdentity.Lifecycle == identity.LifecycleMissing {
				boundIdentity, err = recoverMissingIdentityTx(ctx, tx, boundIdentity, handoffAtMS)
				if err != nil {
					return nil, err
				}
			}
			v.Identity = boundIdentity
			if _, err = tx.ExecContext(ctx, "insert into fresh_client_api_keys(request_id,api_key_id,secret_ciphertext,secret_hash,masked_preview,name,name_revision,origin) values(?,?,?,?,?,?,?,?)", v.RequestID, v.Identity.ID, v.Ciphertext, v.Hash, v.Preview, v.Name, v.NameRevision, v.Origin); err != nil {
				return nil, err
			}
			if runtimeIdentity != "" {
				if _, err = handoffOrBindSourceTx(
					ctx, tx, v.Identity.ID, v.Hash,
					v.Identity.CreatedAtMS, v.Identity.UpdatedAtMS,
					handoffAtMS, runtimeIdentity, activeRows,
				); err != nil {
					return nil, err
				}
			}
			resolved = append(resolved, v)
			continue
		}

		var existingLifecycle string
		idErr := tx.QueryRowContext(ctx, "select lifecycle from gateway_api_key_identities where id=?", string(v.Identity.ID)).Scan(&existingLifecycle)
		if idErr == nil {
			if identity.Lifecycle(existingLifecycle) == identity.LifecycleSuperseded {
				return nil, ErrRetired
			}
			return nil, ErrSourceOwned
		}
		if !errors.Is(idErr, sql.ErrNoRows) {
			return nil, idErr
		}
		i := v.Identity
		if _, err = tx.ExecContext(ctx, "insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms) values(?,?,?,?,?)", i.ID, i.Revision, i.Lifecycle, i.CreatedAtMS, i.UpdatedAtMS); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "insert into fresh_client_api_keys(request_id,api_key_id,secret_ciphertext,secret_hash,masked_preview,name,name_revision,origin) values(?,?,?,?,?,?,?,?)", v.RequestID, i.ID, v.Ciphertext, v.Hash, v.Preview, v.Name, v.NameRevision, v.Origin); err != nil {
			return nil, err
		}
		if runtimeIdentity != "" {
			if err = bindSource(ctx, tx, i.ID, runtimeIdentity, v.Hash, i.CreatedAtMS); err != nil {
				return nil, err
			}
		}
		resolved = append(resolved, v)
	}
	return resolved, nil
}

// ByHash returns the canonical record of one secret digest, or nil while no
// record owns it. The digest is unique, so this is the idempotent lookup a
// replayed registration replays.
func (r *Repository) ByHash(ctx context.Context, hash string) (*Record, error) {
	return byHash(ctx, r.db, hash)
}

func byHash(ctx context.Context, q queryer, hash string) (*Record, error) {
	if !identity.IsValidSHA256Hex(hash) {
		return nil, errors.New("invalid staged client secret digest")
	}
	var v Record
	err := q.QueryRowContext(ctx, `select k.request_id, i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		k.secret_ciphertext, k.secret_hash, k.masked_preview, k.name, k.name_revision, k.origin,
		coalesce((select max(h.retired_at_ms) from fresh_client_api_key_secret_history h where h.api_key_id = k.api_key_id), 0)
		from fresh_client_api_keys k
		join gateway_api_key_identities i on i.id=k.api_key_id where k.secret_hash=?`, hash).
		Scan(&v.RequestID, &v.Identity.ID, &v.Identity.Revision, &v.Identity.Lifecycle, &v.Identity.CreatedAtMS, &v.Identity.UpdatedAtMS, &v.Ciphertext, &v.Hash, &v.Preview, &v.Name, &v.NameRevision, &v.Origin, &v.LastRotatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return byRetiredHistoryHash(ctx, q, hash)
	}
	if err != nil {
		return nil, err
	}
	if v.Identity.Validate() != nil || v.NameRevision < 1 || !validRecordName(v.Name) || !security.IsValidProtectedEnvelope(v.Ciphertext) || !validOrigin(v.Origin) || v.LastRotatedAtMS < 0 {
		return nil, errors.New("invalid staged client key")
	}
	if v.Identity.Lifecycle == identity.LifecycleSuperseded {
		v.RevokedAtMS = v.Identity.UpdatedAtMS
	}
	return &v, nil
}

func byRetiredHistoryHash(ctx context.Context, q queryer, hash string) (*Record, error) {
	var (
		v           Record
		dbLifecycle identity.Lifecycle
		retiredAtMS int64
	)
	err := q.QueryRowContext(ctx, `select coalesce(k.request_id, i.id), i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		h.secret_ciphertext, h.secret_hash, h.masked_preview, coalesce(k.name, ''), coalesce(k.name_revision, 1), h.origin, h.retired_at_ms
		from fresh_client_api_key_secret_history h
		join gateway_api_key_identities i on i.id = h.api_key_id
		left join fresh_client_api_keys k on k.api_key_id = h.api_key_id
		where h.secret_hash = ?`, hash).
		Scan(&v.RequestID, &v.Identity.ID, &v.Identity.Revision, &dbLifecycle, &v.Identity.CreatedAtMS, &v.Identity.UpdatedAtMS, &v.Ciphertext, &v.Hash, &v.Preview, &v.Name, &v.NameRevision, &v.Origin, &retiredAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.Identity.Lifecycle = dbLifecycle
	if v.Identity.Validate() != nil || v.NameRevision < 1 || !validRecordName(v.Name) || !security.IsValidProtectedEnvelope(v.Ciphertext) || !validOrigin(v.Origin) || retiredAtMS <= 0 {
		return nil, errors.New("invalid retired client key history")
	}
	// A rotated-out secret digest is permanently retired even while its owning
	// canonical identity remains active under the replacement secret.
	v.Identity.Lifecycle = identity.LifecycleSuperseded
	v.LastRotatedAtMS = retiredAtMS
	v.RevokedAtMS = retiredAtMS
	return &v, nil
}

// ByID returns the canonical record for one APIKeyID, or nil when absent.
func (r *Repository) ByID(ctx context.Context, id identity.APIKeyID) (*Record, error) {
	return byID(ctx, r.db, id)
}

func byID(ctx context.Context, q queryer, id identity.APIKeyID) (*Record, error) {
	if id.Validate() != nil {
		return nil, errors.New("invalid staged client key id")
	}
	var v Record
	err := q.QueryRowContext(ctx, `select k.request_id, i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		k.secret_ciphertext, k.secret_hash, k.masked_preview, k.name, k.name_revision, k.origin,
		coalesce((select max(h.retired_at_ms) from fresh_client_api_key_secret_history h where h.api_key_id = k.api_key_id), 0)
		from fresh_client_api_keys k
		join gateway_api_key_identities i on i.id=k.api_key_id where i.id=?`, string(id)).
		Scan(&v.RequestID, &v.Identity.ID, &v.Identity.Revision, &v.Identity.Lifecycle, &v.Identity.CreatedAtMS, &v.Identity.UpdatedAtMS, &v.Ciphertext, &v.Hash, &v.Preview, &v.Name, &v.NameRevision, &v.Origin, &v.LastRotatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v.Identity.Validate() != nil || v.NameRevision < 1 || !validRecordName(v.Name) || !security.IsValidProtectedEnvelope(v.Ciphertext) || !validOrigin(v.Origin) || v.LastRotatedAtMS < 0 {
		return nil, errors.New("invalid staged client key")
	}
	if v.Identity.Lifecycle == identity.LifecycleSuperseded {
		v.RevokedAtMS = v.Identity.UpdatedAtMS
	}
	return &v, nil
}

func validOrigin(origin string) bool {
	return origin == OriginWorkspace || origin == OriginInstaller || origin == OriginAdopted
}

func validRecordName(name string) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) <= 120 && !strings.ContainsFunc(name, unicode.IsControl)
}

func sanitizeAdoptedAlias(raw string) string {
	if !utf8.ValidString(raw) {
		return ""
	}
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)
	cleaned = strings.TrimSpace(cleaned)
	if utf8.RuneCountInString(cleaned) > 120 {
		runes := []rune(cleaned)
		cleaned = strings.TrimSpace(string(runes[:120]))
	}
	return cleaned
}

func lookupAdoptedAliasTx(ctx context.Context, tx *sql.Tx, hash string) (string, error) {
	var tableCount int
	if err := tx.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = 'api_key_aliases'`).Scan(&tableCount); err != nil {
		return "", err
	}
	if tableCount == 0 {
		return "", nil
	}
	var raw string
	err := tx.QueryRowContext(ctx, `select alias from api_key_aliases where lower(api_key_hash) = lower(?) limit 1`, hash).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sanitizeAdoptedAlias(raw), nil
}

// StageRotation persists or replays an Option B in-place secret rotation
// candidate for one non-retired canonical Client Key.
func (r *Repository) StageRotation(ctx context.Context, candidate RotationRecord) (stored RotationRecord, target Record, created bool, committed bool, err error) {
	if _, reqErr := identity.ParseAPIKeyID(candidate.RequestID); reqErr != nil ||
		candidate.APIKeyID.Validate() != nil ||
		!security.IsValidProtectedEnvelope(candidate.NewCiphertext) ||
		!identity.IsValidSHA256Hex(candidate.NewHash) ||
		candidate.NewPreview == "" ||
		candidate.CreatedAtMS <= 0 {
		return RotationRecord{}, Record{}, false, false, errors.New("invalid staged client key rotation")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	defer tx.Rollback()

	targetPtr, err := byID(ctx, tx, candidate.APIKeyID)
	if err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	if targetPtr == nil {
		return RotationRecord{}, Record{}, false, false, ErrNotFound
	}
	if targetPtr.Identity.Lifecycle == identity.LifecycleSuperseded {
		return RotationRecord{}, Record{}, false, false, ErrRetired
	}

	var keyRequestExists int
	if err = tx.QueryRowContext(ctx, "select count(*) from fresh_client_api_keys where request_id = ?", candidate.RequestID).Scan(&keyRequestExists); err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	if keyRequestExists != 0 {
		return RotationRecord{}, Record{}, false, false, ErrConflict
	}

	opByReq, err := readOperation(ctx, tx, candidate.RequestID)
	if err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	if opByReq != nil {
		if opByReq.Mode != "rotate" || opByReq.TargetID != string(candidate.APIKeyID) || opByReq.KeeperID != "" {
			return RotationRecord{}, Record{}, false, false, ErrConflict
		}
		if opByReq.Outcome == OutcomeCommitted {
			return RotationRecord{}, *targetPtr, false, true, nil
		}
		if opByReq.Outcome == OutcomeAborted {
			return RotationRecord{}, Record{}, false, false, ErrConflict
		}
	}

	rotByReq, err := rotationByRequestID(ctx, tx, candidate.RequestID)
	if err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	if rotByReq != nil {
		if rotByReq.APIKeyID != candidate.APIKeyID || rotByReq.OldHash != targetPtr.Hash {
			return RotationRecord{}, Record{}, false, false, ErrConflict
		}
		return *rotByReq, *targetPtr, false, false, nil
	}
	if opByReq != nil {
		return RotationRecord{}, Record{}, false, false, errors.New("staged client key rotation candidate missing")
	}

	rotByKey, err := rotationByKeyID(ctx, tx, candidate.APIKeyID)
	if err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	if rotByKey != nil {
		oldOp, opErr := readOperation(ctx, tx, rotByKey.RequestID)
		if opErr != nil {
			return RotationRecord{}, Record{}, false, false, opErr
		}
		if oldOp != nil && oldOp.IsTerminal() {
			if _, err = tx.ExecContext(ctx, "delete from fresh_client_api_key_rotations where request_id = ?", rotByKey.RequestID); err != nil {
				return RotationRecord{}, Record{}, false, false, err
			}
		} else {
			return RotationRecord{}, Record{}, false, false, ErrConflict
		}
	}

	if candidate.NewHash == targetPtr.Hash {
		return RotationRecord{}, Record{}, false, false, ErrSourceOwned
	}
	existingByHash, err := byHash(ctx, tx, candidate.NewHash)
	if err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	if existingByHash != nil {
		if existingByHash.Identity.Lifecycle == identity.LifecycleSuperseded {
			return RotationRecord{}, Record{}, false, false, ErrRetired
		}
		return RotationRecord{}, Record{}, false, false, ErrSourceOwned
	}
	var histExists bool
	if err = tx.QueryRowContext(ctx, "select exists(select 1 from fresh_client_api_key_secret_history where secret_hash = ?)", candidate.NewHash).Scan(&histExists); err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	if histExists {
		return RotationRecord{}, Record{}, false, false, ErrRetired
	}
	activeRows, retiredRows, err := inspectSourceHashBindingsTx(ctx, tx, candidate.NewHash)
	if err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	if len(activeRows) == 0 && len(retiredRows) > 0 {
		return RotationRecord{}, Record{}, false, false, ErrRetired
	}
	if len(activeRows) > 0 {
		return RotationRecord{}, Record{}, false, false, ErrSourceOwned
	}
	var rotHashExists int
	if err = tx.QueryRowContext(ctx, "select count(*) from fresh_client_api_key_rotations where new_secret_hash = ?", candidate.NewHash).Scan(&rotHashExists); err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	if rotHashExists != 0 {
		return RotationRecord{}, Record{}, false, false, ErrSourceOwned
	}

	candidate.OldHash = targetPtr.Hash
	if _, err = tx.ExecContext(ctx, `insert into fresh_client_api_key_rotations (
		request_id, api_key_id, old_secret_hash, new_secret_ciphertext, new_secret_hash, new_masked_preview, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?)`,
		candidate.RequestID, candidate.APIKeyID, candidate.OldHash, candidate.NewCiphertext, candidate.NewHash, candidate.NewPreview, candidate.CreatedAtMS); err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	if err = tx.Commit(); err != nil {
		return RotationRecord{}, Record{}, false, false, err
	}
	return candidate, *targetPtr, true, false, nil
}

// DiscardUnstartedRotation removes a staged rotation candidate only while no
// operation journal row exists for requestID. Once BeginOperation persists the
// journal row, SaveOperation owns candidate lifecycle.
func (r *Repository) DiscardUnstartedRotation(ctx context.Context, requestID string, targetID identity.APIKeyID) error {
	if _, err := identity.ParseAPIKeyID(requestID); err != nil || targetID.Validate() != nil {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	op, err := readOperation(ctx, tx, requestID)
	if err != nil {
		return err
	}
	if op != nil {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "delete from fresh_client_api_key_rotations where request_id = ? and api_key_id = ?", requestID, string(targetID)); err != nil {
		return err
	}
	return tx.Commit()
}

// RotationByRequestID returns the staged rotation candidate for requestID, or nil.
func (r *Repository) RotationByRequestID(ctx context.Context, requestID string) (*RotationRecord, error) {
	return rotationByRequestID(ctx, r.db, requestID)
}

func rotationByRequestID(ctx context.Context, q queryer, requestID string) (*RotationRecord, error) {
	if _, err := identity.ParseAPIKeyID(requestID); err != nil {
		return nil, errors.New("invalid staged client key rotation request id")
	}
	var rot RotationRecord
	err := q.QueryRowContext(ctx, `select request_id, api_key_id, old_secret_hash, new_secret_ciphertext, new_secret_hash, new_masked_preview, created_at_ms
		from fresh_client_api_key_rotations where request_id = ?`, requestID).
		Scan(&rot.RequestID, &rot.APIKeyID, &rot.OldHash, &rot.NewCiphertext, &rot.NewHash, &rot.NewPreview, &rot.CreatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !validRotationRecord(rot) {
		return nil, errors.New("invalid staged client key rotation")
	}
	return &rot, nil
}

func rotationByKeyID(ctx context.Context, q queryer, keyID identity.APIKeyID) (*RotationRecord, error) {
	if keyID.Validate() != nil {
		return nil, errors.New("invalid staged client key id")
	}
	var rot RotationRecord
	err := q.QueryRowContext(ctx, `select request_id, api_key_id, old_secret_hash, new_secret_ciphertext, new_secret_hash, new_masked_preview, created_at_ms
		from fresh_client_api_key_rotations where api_key_id = ?`, string(keyID)).
		Scan(&rot.RequestID, &rot.APIKeyID, &rot.OldHash, &rot.NewCiphertext, &rot.NewHash, &rot.NewPreview, &rot.CreatedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !validRotationRecord(rot) {
		return nil, errors.New("invalid staged client key rotation")
	}
	return &rot, nil
}

func validRotationRecord(rot RotationRecord) bool {
	_, reqErr := identity.ParseAPIKeyID(rot.RequestID)
	return reqErr == nil &&
		rot.APIKeyID.Validate() == nil &&
		identity.IsValidSHA256Hex(rot.OldHash) &&
		security.IsValidProtectedEnvelope(rot.NewCiphertext) &&
		identity.IsValidSHA256Hex(rot.NewHash) &&
		rot.OldHash != rot.NewHash &&
		rot.NewPreview != "" &&
		rot.CreatedAtMS > 0
}

// ActiveStagedRotationHashes returns new_secret_hash digests for non-aborted
// rotation candidates targeting non-retired canonical Client Keys.
func (r *Repository) ActiveStagedRotationHashes(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `select r.request_id, r.api_key_id, r.old_secret_hash, r.new_secret_ciphertext, r.new_secret_hash, r.new_masked_preview, r.created_at_ms, i.lifecycle
		from fresh_client_api_key_rotations r
		join gateway_api_key_identities i on i.id = r.api_key_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type candidate struct {
		rot       RotationRecord
		lifecycle identity.Lifecycle
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.rot.RequestID, &c.rot.APIKeyID, &c.rot.OldHash, &c.rot.NewCiphertext, &c.rot.NewHash, &c.rot.NewPreview, &c.rot.CreatedAtMS, &c.lifecycle); err != nil {
			return nil, err
		}
		if !validRotationRecord(c.rot) {
			return nil, errors.New("invalid staged client key rotation")
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if c.lifecycle != identity.LifecycleActive && c.lifecycle != identity.LifecycleMissing {
			continue
		}
		op, err := readOperation(ctx, r.db, c.rot.RequestID)
		if err != nil {
			return nil, err
		}
		if op != nil && op.Outcome == OutcomeAborted {
			continue
		}
		out[c.rot.NewHash] = true
	}
	return out, nil
}

// LatestSecretHistoryByID returns the most recently retired secret record of id.
func (r *Repository) LatestSecretHistoryByID(ctx context.Context, id identity.APIKeyID) (*SecretHistoryRecord, error) {
	if id.Validate() != nil {
		return nil, errors.New("invalid client key id")
	}
	var rec SecretHistoryRecord
	err := r.db.QueryRowContext(ctx, `select secret_hash, api_key_id, secret_ciphertext, masked_preview, origin, retired_at_ms, retired_by_request_id, next_secret_hash
		from fresh_client_api_key_secret_history
		where api_key_id = ?
		order by retired_at_ms desc, rowid desc limit 1`, string(id)).
		Scan(&rec.Hash, &rec.APIKeyID, &rec.Ciphertext, &rec.Preview, &rec.Origin, &rec.RetiredAtMS, &rec.RetiredByRequestID, &rec.NextHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !identity.IsValidSHA256Hex(rec.Hash) || rec.APIKeyID != id || !security.IsValidProtectedEnvelope(rec.Ciphertext) || rec.Preview == "" || !validOrigin(rec.Origin) || rec.RetiredAtMS <= 0 {
		return nil, errors.New("invalid client key secret history")
	}
	return &rec, nil
}

// SecretHistoryByRequestID returns the retired secret record produced by one
// committed rotate operation requestID, or nil when requestID has no history row.
func (r *Repository) SecretHistoryByRequestID(ctx context.Context, requestID string) (*SecretHistoryRecord, error) {
	if _, err := identity.ParseAPIKeyID(requestID); err != nil {
		return nil, errors.New("invalid client key rotation request id")
	}
	var rec SecretHistoryRecord
	err := r.db.QueryRowContext(ctx, `select secret_hash, api_key_id, secret_ciphertext, masked_preview, origin, retired_at_ms, retired_by_request_id, next_secret_hash
		from fresh_client_api_key_secret_history
		where retired_by_request_id = ?
		order by retired_at_ms desc, rowid desc limit 1`, requestID).
		Scan(&rec.Hash, &rec.APIKeyID, &rec.Ciphertext, &rec.Preview, &rec.Origin, &rec.RetiredAtMS, &rec.RetiredByRequestID, &rec.NextHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !identity.IsValidSHA256Hex(rec.Hash) || rec.APIKeyID.Validate() != nil || !security.IsValidProtectedEnvelope(rec.Ciphertext) || rec.Preview == "" || !validOrigin(rec.Origin) || rec.RetiredAtMS <= 0 {
		return nil, errors.New("invalid client key secret history")
	}
	return &rec, nil
}

// SecretHistoryByHash returns the retired secret record for secretHash, or nil.
func (r *Repository) SecretHistoryByHash(ctx context.Context, secretHash string) (*SecretHistoryRecord, error) {
	if !identity.IsValidSHA256Hex(secretHash) {
		return nil, errors.New("invalid client key secret hash")
	}
	var rec SecretHistoryRecord
	err := r.db.QueryRowContext(ctx, `select secret_hash, api_key_id, secret_ciphertext, masked_preview, origin, retired_at_ms, retired_by_request_id, next_secret_hash
		from fresh_client_api_key_secret_history
		where secret_hash = ? limit 1`, secretHash).
		Scan(&rec.Hash, &rec.APIKeyID, &rec.Ciphertext, &rec.Preview, &rec.Origin, &rec.RetiredAtMS, &rec.RetiredByRequestID, &rec.NextHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !identity.IsValidSHA256Hex(rec.Hash) || rec.APIKeyID.Validate() != nil || !security.IsValidProtectedEnvelope(rec.Ciphertext) || rec.Preview == "" || !validOrigin(rec.Origin) || rec.RetiredAtMS <= 0 {
		return nil, errors.New("invalid client key secret history")
	}
	return &rec, nil
}

type hashBindingRow struct {
	bindingID       int64
	apiKeyID        identity.APIKeyID
	runtimeIdentity string
	firstSeenAtMS   int64
	lastSeenAtMS    int64
	retiredAtMS     sql.NullInt64
	hasIdentity     bool
	identity        identity.APIKeyIdentity
}

func inspectSourceHashBindingsTx(ctx context.Context, tx *sql.Tx, hash string) ([]hashBindingRow, []hashBindingRow, error) {
	rows, err := tx.QueryContext(ctx, `select b.binding_id, b.api_key_id, b.runtime_identity,
		b.first_seen_at_ms, b.last_seen_at_ms, b.retired_at_ms,
		i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms
		from gateway_api_key_source_bindings b
		left join gateway_api_key_identities i on i.id = b.api_key_id
		where b.api_key_hash = ?
		order by b.binding_id asc`, hash)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var active, retired []hashBindingRow
	for rows.Next() {
		var row hashBindingRow
		var (
			identID        sql.NullString
			identRevision  sql.NullInt64
			identLifecycle sql.NullString
			identCreated   sql.NullInt64
			identUpdated   sql.NullInt64
		)
		if err = rows.Scan(
			&row.bindingID, &row.apiKeyID, &row.runtimeIdentity,
			&row.firstSeenAtMS, &row.lastSeenAtMS, &row.retiredAtMS,
			&identID, &identRevision, &identLifecycle, &identCreated, &identUpdated,
		); err != nil {
			return nil, nil, err
		}
		if identID.Valid {
			row.hasIdentity = true
			if identRevision.Int64 > 0 {
				row.identity.Revision = identity.Revision(identRevision.Int64)
			}
			row.identity.ID = identity.APIKeyID(identID.String)
			row.identity.Lifecycle = identity.Lifecycle(identLifecycle.String)
			row.identity.CreatedAtMS = identCreated.Int64
			row.identity.UpdatedAtMS = identUpdated.Int64
		}
		if row.retiredAtMS.Valid {
			retired = append(retired, row)
		} else {
			active = append(active, row)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	return active, retired, nil
}

func hasOtherActiveHashForIdentityTx(ctx context.Context, tx *sql.Tx, id identity.APIKeyID, hash string) (bool, error) {
	var conflict bool
	err := tx.QueryRowContext(ctx, `select exists(
		select 1 from gateway_api_key_source_bindings
		where retired_at_ms is null
		  and api_key_id = ?
		  and api_key_hash <> ?)`,
		string(id), hash).Scan(&conflict)
	return conflict, err
}

func nextIdentityUpdatedAt(current, requested int64) (int64, error) {
	if requested > current {
		return requested, nil
	}
	if current == math.MaxInt64 {
		return 0, errors.New("canonical identity timestamp overflow")
	}
	return current + 1, nil
}

func recoverMissingIdentityTx(ctx context.Context, tx *sql.Tx, ent identity.APIKeyIdentity, requestedAtMS int64) (identity.APIKeyIdentity, error) {
	if ent.Lifecycle != identity.LifecycleMissing {
		return ent, nil
	}
	if ent.Revision >= math.MaxInt64 {
		return identity.APIKeyIdentity{}, errors.New("canonical identity revision overflow")
	}
	nextUpdated, err := nextIdentityUpdatedAt(ent.UpdatedAtMS, requestedAtMS)
	if err != nil {
		return identity.APIKeyIdentity{}, err
	}
	nextRev := ent.Revision + 1
	res, err := tx.ExecContext(ctx, `update gateway_api_key_identities
		set lifecycle = ?, revision = ?, updated_at_ms = ?
		where id = ? and lifecycle = ? and revision = ?`,
		string(identity.LifecycleActive), int64(nextRev), nextUpdated,
		string(ent.ID), string(identity.LifecycleMissing), int64(ent.Revision))
	if err != nil {
		return identity.APIKeyIdentity{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return identity.APIKeyIdentity{}, err
	}
	if n != 1 {
		return identity.APIKeyIdentity{}, ErrConflict
	}
	ent.Lifecycle = identity.LifecycleActive
	ent.Revision = nextRev
	ent.UpdatedAtMS = nextUpdated
	return ent, nil
}

// handoffOrBindSourceTx ensures (id, hash) holds a single active attribution
// window on runtimeIdentity without overlapping any prior runtime window.
func handoffOrBindSourceTx(
	ctx context.Context,
	tx *sql.Tx,
	id identity.APIKeyID,
	hash string,
	createdAtMS int64,
	updatedAtMS int64,
	handoffAtMS int64,
	runtimeIdentity string,
	activeRows []hashBindingRow,
) (bool, error) {
	if id.Validate() != nil || !identity.IsValidSHA256Hex(hash) || runtimeIdentity == "" ||
		strings.TrimSpace(runtimeIdentity) != runtimeIdentity || createdAtMS <= 0 || handoffAtMS <= 0 {
		return false, errors.New("invalid staged client key source")
	}
	if len(activeRows) == 0 {
		type secretHistoryRow struct {
			secretHash  string
			retiredAtMS int64
		}
		histRows, err := tx.QueryContext(ctx, `select secret_hash, retired_at_ms
			from fresh_client_api_key_secret_history
			where api_key_id = ?
			order by retired_at_ms asc, rowid asc`, string(id))
		if err != nil {
			return false, err
		}
		var history []secretHistoryRow
		for histRows.Next() {
			var h secretHistoryRow
			if err = histRows.Scan(&h.secretHash, &h.retiredAtMS); err != nil {
				histRows.Close()
				return false, err
			}
			history = append(history, h)
		}
		if err = histRows.Err(); err != nil {
			histRows.Close()
			return false, err
		}
		if err = histRows.Close(); err != nil {
			return false, err
		}
		segStart := createdAtMS
		var latestRetiredMS int64
		for _, h := range history {
			if !identity.IsValidSHA256Hex(h.secretHash) {
				return false, errors.New("invalid client key secret history hash")
			}
			hActive, hRetired, err := inspectSourceHashBindingsTx(ctx, tx, h.secretHash)
			if err != nil {
				return false, err
			}
			for _, row := range hActive {
				if !row.hasIdentity || row.identity.Validate() != nil {
					return false, errors.New("invalid pre-existing client key identity")
				}
				if row.apiKeyID != id {
					return false, ErrSourceOwned
				}
			}
			alreadyBound := false
			for _, row := range hActive {
				if row.apiKeyID == id && row.runtimeIdentity == runtimeIdentity {
					alreadyBound = true
					break
				}
			}
			if !alreadyBound {
				for _, row := range hRetired {
					if row.apiKeyID == id && row.runtimeIdentity == runtimeIdentity {
						alreadyBound = true
						break
					}
				}
			}
			endMS := max(h.retiredAtMS, segStart)
			if !alreadyBound {
				if _, err := tx.ExecContext(ctx, `insert into gateway_api_key_source_bindings (
					api_key_id, runtime_identity, api_key_hash, observed_runtime_generation,
					first_seen_at_ms, last_seen_at_ms, retired_at_ms
				) values (?, ?, ?, null, ?, ?, ?)`,
					string(id), runtimeIdentity, h.secretHash, segStart, endMS, endMS); err != nil {
					return false, err
				}
			}
			segStart = endMS
			latestRetiredMS = max(latestRetiredMS, endMS)
		}
		firstSeen := max(createdAtMS, latestRetiredMS)
		lastSeen := max(handoffAtMS, firstSeen, updatedAtMS)
		if _, err := tx.ExecContext(ctx, `insert into gateway_api_key_source_bindings (
			api_key_id, runtime_identity, api_key_hash, observed_runtime_generation,
			first_seen_at_ms, last_seen_at_ms, retired_at_ms
		) values (?, ?, ?, null, ?, ?, null)`,
			string(id), runtimeIdentity, hash, firstSeen, lastSeen); err != nil {
			return false, err
		}
		return true, nil
	}
	if len(activeRows) == 1 && activeRows[0].runtimeIdentity == runtimeIdentity {
		if activeRows[0].firstSeenAtMS <= 0 || activeRows[0].lastSeenAtMS < activeRows[0].firstSeenAtMS {
			return false, errors.New("invalid client key source binding")
		}
		return false, nil
	}

	effectiveHandoffMS := handoffAtMS
	alreadyOnTarget := false
	for _, row := range activeRows {
		if row.runtimeIdentity == "" || strings.TrimSpace(row.runtimeIdentity) != row.runtimeIdentity ||
			row.firstSeenAtMS <= 0 || row.lastSeenAtMS < row.firstSeenAtMS {
			return false, errors.New("invalid client key source binding")
		}
		if row.runtimeIdentity == runtimeIdentity {
			alreadyOnTarget = true
			effectiveHandoffMS = max(effectiveHandoffMS, row.firstSeenAtMS)
		} else {
			effectiveHandoffMS = max(effectiveHandoffMS, row.firstSeenAtMS, row.lastSeenAtMS)
		}
	}

	if _, err := tx.ExecContext(ctx, `update gateway_api_key_source_bindings
		set retired_at_ms = ?
		where api_key_id = ?
		  and api_key_hash = ?
		  and runtime_identity <> ?
		  and retired_at_ms is null`,
		effectiveHandoffMS, string(id), hash, runtimeIdentity); err != nil {
		return false, err
	}

	if alreadyOnTarget {
		if _, err := tx.ExecContext(ctx, `update gateway_api_key_source_bindings
			set first_seen_at_ms = ?,
			    last_seen_at_ms = case when last_seen_at_ms > ? then last_seen_at_ms else ? end
			where api_key_id = ?
			  and runtime_identity = ?
			  and api_key_hash = ?
			  and retired_at_ms is null`,
			effectiveHandoffMS, effectiveHandoffMS, effectiveHandoffMS,
			string(id), runtimeIdentity, hash); err != nil {
			return false, err
		}
		return true, nil
	}

	if _, err := tx.ExecContext(ctx, `insert into gateway_api_key_source_bindings (
		api_key_id, runtime_identity, api_key_hash, observed_runtime_generation,
		first_seen_at_ms, last_seen_at_ms, retired_at_ms
	) values (?, ?, ?, null, ?, ?, null)`,
		string(id), runtimeIdentity, hash, effectiveHandoffMS, effectiveHandoffMS); err != nil {
		return false, err
	}
	return true, nil
}

// bindSource records one attribution window. An active binding of the same
// source hash or identity is a conflict: one hash has exactly one Canonical Key.
func bindSource(ctx context.Context, tx *sql.Tx, id identity.APIKeyID, runtimeIdentity, hash string, nowMS int64) error {
	if id.Validate() != nil || !identity.IsValidSHA256Hex(hash) || runtimeIdentity == "" ||
		strings.TrimSpace(runtimeIdentity) != runtimeIdentity || nowMS <= 0 {
		return errors.New("invalid staged client key source")
	}
	var owned bool
	if err := tx.QueryRowContext(ctx, `select exists(
		select 1 from gateway_api_key_source_bindings
		where retired_at_ms is null and (api_key_hash = ? or api_key_id = ?))`, hash, string(id)).Scan(&owned); err != nil {
		return err
	}
	if owned {
		return ErrSourceOwned
	}
	_, err := tx.ExecContext(ctx, `insert into gateway_api_key_source_bindings (
		api_key_id, runtime_identity, api_key_hash, observed_runtime_generation,
		first_seen_at_ms, last_seen_at_ms, retired_at_ms
	) values (?, ?, ?, null, ?, ?, null)`, string(id), runtimeIdentity, hash, nowMS, nowMS)
	return err
}

// BindFreshKeySources binds staged keys that have no attribution yet to the
// runtime identity of the now committed installation, and atomically hands off
// same-identity source windows from prior runtimes without overlapping active
// intervals. It is idempotent and fails closed with ErrSourceOwned when another
// active identity owns the hash or the identity owns another active hash.
func (r *Repository) BindFreshKeySources(ctx context.Context, runtimeIdentity string, nowMS int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("invalid staged client key repository")
	}
	if runtimeIdentity == "" || strings.TrimSpace(runtimeIdentity) != runtimeIdentity || nowMS <= 0 {
		return 0, errors.New("invalid staged client key source")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	bound, err := r.BindFreshKeySourcesTx(ctx, tx, runtimeIdentity, nowMS)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return bound, nil
}

// BindFreshKeySourcesTx binds unbound staged keys and hands off prior-runtime
// source bindings to runtimeIdentity inside a caller-owned write transaction.
func (r *Repository) BindFreshKeySourcesTx(ctx context.Context, tx *sql.Tx, runtimeIdentity string, nowMS int64) (int64, error) {
	if r == nil || tx == nil {
		return 0, errors.New("invalid staged client key transaction")
	}
	return bindFreshKeySourcesTx(ctx, tx, runtimeIdentity, nowMS)
}

func bindFreshKeySourcesTx(ctx context.Context, tx *sql.Tx, runtimeIdentity string, nowMS int64) (int64, error) {
	if runtimeIdentity == "" || strings.TrimSpace(runtimeIdentity) != runtimeIdentity || nowMS <= 0 {
		return 0, errors.New("invalid staged client key source")
	}
	type stagedSourceRow struct {
		id       identity.APIKeyID
		hash     string
		identity identity.APIKeyIdentity
	}
	rows, err := tx.QueryContext(ctx, `select k.api_key_id, k.secret_hash,
		i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms
		from fresh_client_api_keys k
		join gateway_api_key_identities i on i.id = k.api_key_id
		order by i.created_at_ms, k.rowid`)
	if err != nil {
		return 0, err
	}
	var staged []stagedSourceRow
	for rows.Next() {
		var item stagedSourceRow
		if err = rows.Scan(
			&item.id, &item.hash,
			&item.identity.ID, &item.identity.Revision, &item.identity.Lifecycle,
			&item.identity.CreatedAtMS, &item.identity.UpdatedAtMS,
		); err != nil {
			rows.Close()
			return 0, err
		}
		staged = append(staged, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}

	var bound int64
	for _, item := range staged {
		if item.identity.Validate() != nil || item.id != item.identity.ID || !identity.IsValidSHA256Hex(item.hash) {
			return 0, errors.New("invalid staged client key")
		}
		if item.identity.Lifecycle == identity.LifecycleSuperseded {
			continue
		}
		activeRows, retiredRows, err := inspectSourceHashBindingsTx(ctx, tx, item.hash)
		if err != nil {
			return 0, err
		}
		if len(activeRows) == 0 && len(retiredRows) > 0 {
			continue
		}
		for _, row := range activeRows {
			if !row.hasIdentity || row.identity.Validate() != nil {
				return 0, errors.New("invalid pre-existing client key identity")
			}
			if row.apiKeyID != item.id {
				return 0, ErrSourceOwned
			}
			if row.identity.Lifecycle == identity.LifecycleSuperseded {
				continue
			}
		}
		otherHashConflict, err := hasOtherActiveHashForIdentityTx(ctx, tx, item.id, item.hash)
		if err != nil {
			return 0, err
		}
		if otherHashConflict {
			return 0, ErrSourceOwned
		}
		needsBindOrHandoff := len(activeRows) != 1 || activeRows[0].runtimeIdentity != runtimeIdentity
		if needsBindOrHandoff && item.identity.Lifecycle == identity.LifecycleMissing {
			handoffFloorMS := nowMS
			for _, row := range activeRows {
				if row.runtimeIdentity == runtimeIdentity {
					handoffFloorMS = max(handoffFloorMS, row.firstSeenAtMS)
				} else {
					handoffFloorMS = max(handoffFloorMS, row.firstSeenAtMS, row.lastSeenAtMS)
				}
			}
			item.identity, err = recoverMissingIdentityTx(ctx, tx, item.identity, handoffFloorMS)
			if err != nil {
				return 0, err
			}
		}
		changed, err := handoffOrBindSourceTx(
			ctx, tx, item.id, item.hash,
			item.identity.CreatedAtMS, item.identity.UpdatedAtMS,
			nowMS, runtimeIdentity, activeRows,
		)
		if err != nil {
			return 0, err
		}
		if changed {
			bound++
		}
	}
	return bound, nil
}
