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

type Repository struct{ db *sql.DB }

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
// transaction. Either every record (reusing an active identity that already
// owns the digest, or creating the identity, encrypted record and optional
// source binding) commits together, or none do.
func (r *Repository) StageAdopted(ctx context.Context, candidates []Record, runtimeIdentity string) ([]Record, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	records, err := stageAdoptedTx(ctx, tx, candidates, runtimeIdentity)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return records, nil
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
		existing, err := byHash(ctx, tx, v.Hash)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			if existing.Identity.Lifecycle != identity.LifecycleActive {
				return nil, ErrRetired
			}
			if runtimeIdentity != "" {
				var conflict bool
				if err = tx.QueryRowContext(ctx, `select exists(
					select 1 from gateway_api_key_source_bindings
					where api_key_hash = ? and retired_at_ms is null and api_key_id <> ?)`, existing.Hash, string(existing.Identity.ID)).Scan(&conflict); err != nil {
					return nil, err
				}
				if conflict {
					return nil, ErrSourceOwned
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

func validOrigin(origin string) bool {
	return origin == OriginWorkspace || origin == OriginInstaller || origin == OriginAdopted
}

// bindSource records one attribution window. An active binding of the same
// source hash that already belongs to another identity is a conflict: one hash
// has exactly one Canonical Key.
func bindSource(ctx context.Context, tx *sql.Tx, id identity.APIKeyID, runtimeIdentity, hash string, nowMS int64) error {
	if id.Validate() != nil || !identity.IsValidSHA256Hex(hash) || runtimeIdentity == "" ||
		strings.TrimSpace(runtimeIdentity) != runtimeIdentity || nowMS <= 0 {
		return errors.New("invalid staged client key source")
	}
	var owned bool
	if err := tx.QueryRowContext(ctx, `select exists(
		select 1 from gateway_api_key_source_bindings
		where api_key_hash = ? and retired_at_ms is null)`, hash).Scan(&owned); err != nil {
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
// runtime identity of the now committed installation. It is idempotent and
// never rebinds a source hash that an active binding already owns.
//
// The attribution window opens at the key's own creation instant, not at this
// call: a staged key can only produce traffic after its secret existed, and a
// later startup must not erase what an earlier one could already attribute.
func (r *Repository) BindFreshKeySources(ctx context.Context, runtimeIdentity string, nowMS int64) (int64, error) {
	if runtimeIdentity == "" || strings.TrimSpace(runtimeIdentity) != runtimeIdentity || nowMS <= 0 {
		return 0, errors.New("invalid staged client key source")
	}
	result, err := r.db.ExecContext(ctx, `insert into gateway_api_key_source_bindings (
		api_key_id, runtime_identity, api_key_hash, observed_runtime_generation,
		first_seen_at_ms, last_seen_at_ms, retired_at_ms
	)
	select k.api_key_id, ?, k.secret_hash, null, i.created_at_ms, ?, null
	from fresh_client_api_keys k
	join gateway_api_key_identities i on i.id = k.api_key_id
	where i.lifecycle = 'active'
	  and i.created_at_ms > 0
	  and not exists (
		select 1 from gateway_api_key_source_bindings b
		where b.api_key_hash = k.secret_hash and b.retired_at_ms is null
	  )`, runtimeIdentity, nowMS)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
