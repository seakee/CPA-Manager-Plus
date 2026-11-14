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

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

type Record struct {
	RequestID    string
	Identity     identity.APIKeyIdentity
	Ciphertext   string
	Hash         string
	Preview      string
	Name         string
	NameRevision int64
	Origin       string
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
		k.secret_ciphertext, k.secret_hash, k.masked_preview, k.name, k.name_revision, k.origin from fresh_client_api_keys k
		join gateway_api_key_identities i on i.id=k.api_key_id order by i.created_at_ms, k.rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Record{}
	for rows.Next() {
		var v Record
		if err = rows.Scan(&v.RequestID, &v.Identity.ID, &v.Identity.Revision, &v.Identity.Lifecycle, &v.Identity.CreatedAtMS, &v.Identity.UpdatedAtMS, &v.Ciphertext, &v.Hash, &v.Preview, &v.Name, &v.NameRevision, &v.Origin); err != nil {
			return nil, err
		}
		if v.Identity.Validate() != nil || v.NameRevision < 1 || !security.IsValidProtectedEnvelope(v.Ciphertext) || !validOrigin(v.Origin) {
			return nil, errors.New("invalid staged client key")
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
	if v.Identity.Validate() != nil || !security.IsValidProtectedEnvelope(v.Ciphertext) || !identity.IsValidSHA256Hex(v.Hash) || v.Origin != OriginWorkspace {
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
	if _, err = tx.ExecContext(ctx, "insert into fresh_client_api_keys(request_id,api_key_id,secret_ciphertext,secret_hash,masked_preview,origin) values(?,?,?,?,?,?)", v.RequestID, i.ID, v.Ciphertext, v.Hash, v.Preview, v.Origin); err != nil {
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
		k.secret_ciphertext, k.secret_hash, k.masked_preview, k.name, k.name_revision, k.origin from fresh_client_api_keys k
		join gateway_api_key_identities i on i.id=k.api_key_id where k.origin=?`, origin).
		Scan(&v.RequestID, &v.Identity.ID, &v.Identity.Revision, &v.Identity.Lifecycle, &v.Identity.CreatedAtMS, &v.Identity.UpdatedAtMS, &v.Ciphertext, &v.Hash, &v.Preview, &v.Name, &v.NameRevision, &v.Origin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v.Identity.Validate() != nil || v.NameRevision < 1 || !security.IsValidProtectedEnvelope(v.Ciphertext) || !validOrigin(v.Origin) {
		return nil, errors.New("invalid staged client key")
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
		v.NameRevision = 1
		if v.Identity.Validate() != nil || !security.IsValidProtectedEnvelope(v.Ciphertext) || !identity.IsValidSHA256Hex(v.Hash) || v.Preview == "" || v.Identity.CreatedAtMS <= 0 {
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
			if _, err = tx.ExecContext(ctx, "insert into fresh_client_api_keys(request_id,api_key_id,secret_ciphertext,secret_hash,masked_preview,origin) values(?,?,?,?,?,?)", v.RequestID, v.Identity.ID, v.Ciphertext, v.Hash, v.Preview, v.Origin); err != nil {
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
		if _, err = tx.ExecContext(ctx, "insert into fresh_client_api_keys(request_id,api_key_id,secret_ciphertext,secret_hash,masked_preview,origin) values(?,?,?,?,?,?)", v.RequestID, i.ID, v.Ciphertext, v.Hash, v.Preview, v.Origin); err != nil {
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
		k.secret_ciphertext, k.secret_hash, k.masked_preview, k.name, k.name_revision, k.origin from fresh_client_api_keys k
		join gateway_api_key_identities i on i.id=k.api_key_id where k.secret_hash=?`, hash).
		Scan(&v.RequestID, &v.Identity.ID, &v.Identity.Revision, &v.Identity.Lifecycle, &v.Identity.CreatedAtMS, &v.Identity.UpdatedAtMS, &v.Ciphertext, &v.Hash, &v.Preview, &v.Name, &v.NameRevision, &v.Origin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v.Identity.Validate() != nil || v.NameRevision < 1 || !security.IsValidProtectedEnvelope(v.Ciphertext) || !validOrigin(v.Origin) {
		return nil, errors.New("invalid staged client key")
	}
	return &v, nil
}

// ByID returns the canonical record for one APIKeyID, or nil when absent.
func (r *Repository) ByID(ctx context.Context, id identity.APIKeyID) (*Record, error) {
	if id.Validate() != nil {
		return nil, errors.New("invalid staged client key id")
	}
	var v Record
	err := r.db.QueryRowContext(ctx, `select k.request_id, i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		k.secret_ciphertext, k.secret_hash, k.masked_preview, k.name, k.name_revision, k.origin from fresh_client_api_keys k
		join gateway_api_key_identities i on i.id=k.api_key_id where i.id=?`, string(id)).
		Scan(&v.RequestID, &v.Identity.ID, &v.Identity.Revision, &v.Identity.Lifecycle, &v.Identity.CreatedAtMS, &v.Identity.UpdatedAtMS, &v.Ciphertext, &v.Hash, &v.Preview, &v.Name, &v.NameRevision, &v.Origin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v.Identity.Validate() != nil || v.NameRevision < 1 || !security.IsValidProtectedEnvelope(v.Ciphertext) || !validOrigin(v.Origin) {
		return nil, errors.New("invalid staged client key")
	}
	return &v, nil
}

func validOrigin(origin string) bool {
	return origin == OriginWorkspace || origin == OriginInstaller || origin == OriginAdopted
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
		lastSeen := max(handoffAtMS, createdAtMS, updatedAtMS)
		if _, err := tx.ExecContext(ctx, `insert into gateway_api_key_source_bindings (
			api_key_id, runtime_identity, api_key_hash, observed_runtime_generation,
			first_seen_at_ms, last_seen_at_ms, retired_at_ms
		) values (?, ?, ?, null, ?, ?, null)`,
			string(id), runtimeIdentity, hash, createdAtMS, lastSeen); err != nil {
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
