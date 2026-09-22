package identitystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

type repository struct {
	db *sql.DB
}

// New creates a new SQLite identity store repository.
func New(db *sql.DB) ports.Repository {
	return &repository{db: db}
}

func (r *repository) CreateAPIKey(ctx context.Context, ent identity.APIKeyIdentity, binding identity.APIKeySourceBinding) error {
	if err := ent.Validate(); err != nil {
		return fmt.Errorf("validate APIKeyIdentity: %w", err)
	}
	if err := binding.Validate(); err != nil {
		return fmt.Errorf("validate APIKeySourceBinding: %w", err)
	}
	if ent.ID != binding.APIKeyID {
		return fmt.Errorf("identity ID %q does not match binding APIKeyID %q", ent.ID, binding.APIKeyID)
	}
	if binding.RetiredAtMS != 0 {
		return errors.New("initial source binding must be active (retiredAtMs must be 0)")
	}
	if ent.Lifecycle != identity.LifecycleActive {
		return fmt.Errorf("initial identity lifecycle must be active, got %q", ent.Lifecycle)
	}
	if ent.Revision != identity.InitialRevision {
		return fmt.Errorf("initial identity revision must be %d, got %d", identity.InitialRevision, ent.Revision)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	_, err = tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayAPIKeyIdentitiesTable+` (
		id, revision, lifecycle, created_at_ms, updated_at_ms
	) values (?, ?, ?, ?, ?)`,
		string(ent.ID),
		int64(ent.Revision),
		string(ent.Lifecycle),
		ent.CreatedAtMS,
		ent.UpdatedAtMS,
	)
	if err != nil {
		if isConstraintConflict(err) {
			return fmt.Errorf("%w: %v", ports.ErrSourceBindingConflict, err)
		}
		return fmt.Errorf("insert api key identity: %w", err)
	}

	observedGen := formatObservedGeneration(binding.ObservedRuntimeGeneration)
	_, err = tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayAPIKeySourceBindingsTable+` (
		api_key_id, runtime_identity, api_key_hash, observed_runtime_generation,
		first_seen_at_ms, last_seen_at_ms, retired_at_ms
	) values (?, ?, ?, ?, ?, ?, null)`,
		string(binding.APIKeyID),
		binding.RuntimeIdentity,
		binding.APIKeyHash,
		observedGen,
		binding.FirstSeenAtMS,
		binding.LastSeenAtMS,
	)
	if err != nil {
		if isConstraintConflict(err) {
			return fmt.Errorf("%w: %v", ports.ErrSourceBindingConflict, err)
		}
		return fmt.Errorf("insert api key source binding: %w", err)
	}

	if err := tx.Commit(); err != nil {
		if isConstraintConflict(err) {
			return fmt.Errorf("%w: %v", ports.ErrSourceBindingConflict, err)
		}
		return fmt.Errorf("commit create api key: %w", err)
	}
	return nil
}

func (r *repository) CreateCredential(ctx context.Context, ent identity.CredentialIdentity, binding identity.CredentialSourceBinding) error {
	if err := ent.Validate(); err != nil {
		return fmt.Errorf("validate CredentialIdentity: %w", err)
	}
	if err := binding.Validate(); err != nil {
		return fmt.Errorf("validate CredentialSourceBinding: %w", err)
	}
	if ent.ID != binding.CredentialID {
		return fmt.Errorf("identity ID %q does not match binding CredentialID %q", ent.ID, binding.CredentialID)
	}
	if binding.RetiredAtMS != 0 {
		return errors.New("initial source binding must be active (retiredAtMs must be 0)")
	}
	if ent.Lifecycle != identity.LifecycleActive {
		return fmt.Errorf("initial identity lifecycle must be active, got %q", ent.Lifecycle)
	}
	if ent.Revision != identity.InitialRevision {
		return fmt.Errorf("initial identity revision must be %d, got %d", identity.InitialRevision, ent.Revision)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	_, err = tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayCredentialIdentitiesTable+` (
		id, revision, lifecycle, created_at_ms, updated_at_ms
	) values (?, ?, ?, ?, ?)`,
		string(ent.ID),
		int64(ent.Revision),
		string(ent.Lifecycle),
		ent.CreatedAtMS,
		ent.UpdatedAtMS,
	)
	if err != nil {
		if isConstraintConflict(err) {
			return fmt.Errorf("%w: %v", ports.ErrSourceBindingConflict, err)
		}
		return fmt.Errorf("insert credential identity: %w", err)
	}

	observedGen := formatObservedGeneration(binding.ObservedRuntimeGeneration)
	_, err = tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayCredentialSourceBindingsTable+` (
		credential_id, runtime_identity, source_auth_id, auth_index, provider,
		physical_name, account_snapshot, account_id_snapshot, observed_runtime_generation,
		first_seen_at_ms, last_seen_at_ms, retired_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, null)`,
		string(binding.CredentialID),
		binding.RuntimeIdentity,
		binding.SourceAuthID,
		binding.AuthIndex,
		binding.Provider,
		binding.PhysicalName,
		binding.AccountSnapshot,
		binding.AccountIDSnapshot,
		observedGen,
		binding.FirstSeenAtMS,
		binding.LastSeenAtMS,
	)
	if err != nil {
		if isConstraintConflict(err) {
			return fmt.Errorf("%w: %v", ports.ErrSourceBindingConflict, err)
		}
		return fmt.Errorf("insert credential source binding: %w", err)
	}

	if err := tx.Commit(); err != nil {
		if isConstraintConflict(err) {
			return fmt.Errorf("%w: %v", ports.ErrSourceBindingConflict, err)
		}
		return fmt.Errorf("commit create credential: %w", err)
	}
	return nil
}

func (r *repository) LoadAPIKeyByID(ctx context.Context, id identity.APIKeyID) (identity.APIKeyIdentity, error) {
	if err := id.Validate(); err != nil {
		return identity.APIKeyIdentity{}, err
	}

	var (
		rawID       string
		rev         int64
		lifecycle   string
		createdAtMS int64
		updatedAtMS int64
	)
	err := r.db.QueryRowContext(ctx, `select id, revision, lifecycle, created_at_ms, updated_at_ms
		from `+sqliterepo.GatewayAPIKeyIdentitiesTable+`
		where id = ?`, string(id)).Scan(&rawID, &rev, &lifecycle, &createdAtMS, &updatedAtMS)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return identity.APIKeyIdentity{}, ports.ErrNotFound
		}
		return identity.APIKeyIdentity{}, fmt.Errorf("query api key identity: %w", err)
	}

	res := identity.APIKeyIdentity{
		ID:          identity.APIKeyID(rawID),
		Revision:    identity.Revision(rev),
		Lifecycle:   identity.Lifecycle(lifecycle),
		CreatedAtMS: createdAtMS,
		UpdatedAtMS: updatedAtMS,
	}
	if err := res.Validate(); err != nil {
		return identity.APIKeyIdentity{}, fmt.Errorf("persisted row invalid: %w", err)
	}
	return res, nil
}

func (r *repository) LoadCredentialByID(ctx context.Context, id identity.CredentialID) (identity.CredentialIdentity, error) {
	if err := id.Validate(); err != nil {
		return identity.CredentialIdentity{}, err
	}

	var (
		rawID       string
		rev         int64
		lifecycle   string
		createdAtMS int64
		updatedAtMS int64
	)
	err := r.db.QueryRowContext(ctx, `select id, revision, lifecycle, created_at_ms, updated_at_ms
		from `+sqliterepo.GatewayCredentialIdentitiesTable+`
		where id = ?`, string(id)).Scan(&rawID, &rev, &lifecycle, &createdAtMS, &updatedAtMS)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return identity.CredentialIdentity{}, ports.ErrNotFound
		}
		return identity.CredentialIdentity{}, fmt.Errorf("query credential identity: %w", err)
	}

	res := identity.CredentialIdentity{
		ID:          identity.CredentialID(rawID),
		Revision:    identity.Revision(rev),
		Lifecycle:   identity.Lifecycle(lifecycle),
		CreatedAtMS: createdAtMS,
		UpdatedAtMS: updatedAtMS,
	}
	if err := res.Validate(); err != nil {
		return identity.CredentialIdentity{}, fmt.Errorf("persisted row invalid: %w", err)
	}
	return res, nil
}

func (r *repository) FindActiveAPIKeyBySource(ctx context.Context, runtimeIdentity, apiKeyHash string) (identity.APIKeyIdentity, identity.APIKeySourceBinding, error) {
	if runtimeIdentity == "" || strings.TrimSpace(runtimeIdentity) != runtimeIdentity {
		return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, fmt.Errorf("invalid runtime identity: %w", identity.ErrInvalidRuntimeIdentity)
	}
	canonicalHash := strings.ToLower(strings.TrimSpace(apiKeyHash))

	var (
		entID       string
		rev         int64
		lifecycle   string
		createdAtMS int64
		updatedAtMS int64

		bindingID     int64
		bAPIKeyID     string
		bRTIdentity   string
		bHash         string
		observedGen   *string
		firstSeenAtMS int64
		lastSeenAtMS  int64
		retiredAtMS   sql.NullInt64
	)

	err := r.db.QueryRowContext(ctx, `select
		i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		b.binding_id, b.api_key_id, b.runtime_identity, b.api_key_hash, b.observed_runtime_generation,
		b.first_seen_at_ms, b.last_seen_at_ms, b.retired_at_ms
		from `+sqliterepo.GatewayAPIKeySourceBindingsTable+` b
		join `+sqliterepo.GatewayAPIKeyIdentitiesTable+` i on b.api_key_id = i.id
		where b.runtime_identity = ? and b.api_key_hash = ? and b.retired_at_ms is null`,
		runtimeIdentity, canonicalHash,
	).Scan(
		&entID, &rev, &lifecycle, &createdAtMS, &updatedAtMS,
		&bindingID, &bAPIKeyID, &bRTIdentity, &bHash, &observedGen,
		&firstSeenAtMS, &lastSeenAtMS, &retiredAtMS,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, ports.ErrNotFound
		}
		return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, fmt.Errorf("find active api key: %w", err)
	}

	gen, err := parseObservedGeneration(observedGen)
	if err != nil {
		return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, fmt.Errorf("parse observed runtime generation: %w", err)
	}

	ent := identity.APIKeyIdentity{
		ID:          identity.APIKeyID(entID),
		Revision:    identity.Revision(rev),
		Lifecycle:   identity.Lifecycle(lifecycle),
		CreatedAtMS: createdAtMS,
		UpdatedAtMS: updatedAtMS,
	}
	binding := identity.APIKeySourceBinding{
		BindingID:                 bindingID,
		APIKeyID:                  identity.APIKeyID(bAPIKeyID),
		RuntimeIdentity:           bRTIdentity,
		APIKeyHash:                bHash,
		ObservedRuntimeGeneration: gen,
		FirstSeenAtMS:             firstSeenAtMS,
		LastSeenAtMS:              lastSeenAtMS,
		RetiredAtMS:               retiredAtMS.Int64,
	}

	if err := ent.Validate(); err != nil {
		return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, fmt.Errorf("persisted entity invalid: %w", err)
	}
	if err := binding.Validate(); err != nil {
		return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, fmt.Errorf("persisted binding invalid: %w", err)
	}
	return ent, binding, nil
}

func (r *repository) FindActiveCredentialBySource(ctx context.Context, runtimeIdentity, sourceAuthID string) (identity.CredentialIdentity, identity.CredentialSourceBinding, error) {
	if runtimeIdentity == "" || strings.TrimSpace(runtimeIdentity) != runtimeIdentity {
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, fmt.Errorf("invalid runtime identity: %w", identity.ErrInvalidRuntimeIdentity)
	}
	if sourceAuthID == "" || strings.TrimSpace(sourceAuthID) != sourceAuthID {
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, fmt.Errorf("invalid source auth id: %w", identity.ErrInvalidSourceAuthID)
	}

	var (
		entID       string
		rev         int64
		lifecycle   string
		createdAtMS int64
		updatedAtMS int64

		bindingID        int64
		bCredID          string
		bRTIdentity      string
		bSourceAuthID    string
		bAuthIndex       string
		bProvider        string
		bPhysicalName    string
		bAccountSnapshot string
		bAccountIDSnap   string
		observedGen      *string
		firstSeenAtMS    int64
		lastSeenAtMS     int64
		retiredAtMS      sql.NullInt64
	)

	err := r.db.QueryRowContext(ctx, `select
		i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		b.binding_id, b.credential_id, b.runtime_identity, b.source_auth_id,
		b.auth_index, b.provider, b.physical_name, b.account_snapshot, b.account_id_snapshot,
		b.observed_runtime_generation, b.first_seen_at_ms, b.last_seen_at_ms, b.retired_at_ms
		from `+sqliterepo.GatewayCredentialSourceBindingsTable+` b
		join `+sqliterepo.GatewayCredentialIdentitiesTable+` i on b.credential_id = i.id
		where b.runtime_identity = ? and b.source_auth_id = ? and b.retired_at_ms is null`,
		runtimeIdentity, sourceAuthID,
	).Scan(
		&entID, &rev, &lifecycle, &createdAtMS, &updatedAtMS,
		&bindingID, &bCredID, &bRTIdentity, &bSourceAuthID,
		&bAuthIndex, &bProvider, &bPhysicalName, &bAccountSnapshot, &bAccountIDSnap,
		&observedGen, &firstSeenAtMS, &lastSeenAtMS, &retiredAtMS,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, ports.ErrNotFound
		}
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, fmt.Errorf("find active credential: %w", err)
	}

	gen, err := parseObservedGeneration(observedGen)
	if err != nil {
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, fmt.Errorf("parse observed runtime generation: %w", err)
	}

	ent := identity.CredentialIdentity{
		ID:          identity.CredentialID(entID),
		Revision:    identity.Revision(rev),
		Lifecycle:   identity.Lifecycle(lifecycle),
		CreatedAtMS: createdAtMS,
		UpdatedAtMS: updatedAtMS,
	}
	binding := identity.CredentialSourceBinding{
		BindingID:                 bindingID,
		CredentialID:              identity.CredentialID(bCredID),
		RuntimeIdentity:           bRTIdentity,
		SourceAuthID:              bSourceAuthID,
		AuthIndex:                 bAuthIndex,
		Provider:                  bProvider,
		PhysicalName:              bPhysicalName,
		AccountSnapshot:           bAccountSnapshot,
		AccountIDSnapshot:         bAccountIDSnap,
		ObservedRuntimeGeneration: gen,
		FirstSeenAtMS:             firstSeenAtMS,
		LastSeenAtMS:              lastSeenAtMS,
		RetiredAtMS:               retiredAtMS.Int64,
	}

	if err := ent.Validate(); err != nil {
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, fmt.Errorf("persisted entity invalid: %w", err)
	}
	if err := binding.Validate(); err != nil {
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, fmt.Errorf("persisted binding invalid: %w", err)
	}
	return ent, binding, nil
}

func (r *repository) SetAPIKeyLifecycle(
	ctx context.Context,
	id identity.APIKeyID,
	expectedRevision identity.Revision,
	nextLifecycle identity.Lifecycle,
	nowMS int64,
) (identity.APIKeyIdentity, error) {
	if err := id.Validate(); err != nil {
		return identity.APIKeyIdentity{}, err
	}
	if !nextLifecycle.IsValid() {
		return identity.APIKeyIdentity{}, fmt.Errorf("%w: %q", identity.ErrInvalidLifecycle, nextLifecycle)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return identity.APIKeyIdentity{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var (
		rawID       string
		currentRev  int64
		currentLC   string
		createdAtMS int64
		updatedAtMS int64
	)
	err = tx.QueryRowContext(ctx, `select id, revision, lifecycle, created_at_ms, updated_at_ms
		from `+sqliterepo.GatewayAPIKeyIdentitiesTable+`
		where id = ?`, string(id)).Scan(&rawID, &currentRev, &currentLC, &createdAtMS, &updatedAtMS)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return identity.APIKeyIdentity{}, ports.ErrNotFound
		}
		return identity.APIKeyIdentity{}, fmt.Errorf("query current api key identity: %w", err)
	}

	// Stale expectedRevision must fail closed, even if nextLifecycle matches currentLC
	if identity.Revision(currentRev) != expectedRevision {
		return identity.APIKeyIdentity{}, fmt.Errorf("%w: expected revision %d, current is %d",
			ports.ErrRevisionConflict, expectedRevision, currentRev)
	}

	// Validate lifecycle transition
	if err := identity.ValidateTransition(identity.Lifecycle(currentLC), nextLifecycle); err != nil {
		return identity.APIKeyIdentity{}, fmt.Errorf("%w: %v", ports.ErrInvalidLifecycleTransition, err)
	}

	// If same-state, return current entity unchanged (expected revision has already been validated)
	if identity.Lifecycle(currentLC) == nextLifecycle {
		if err := tx.Commit(); err != nil {
			return identity.APIKeyIdentity{}, fmt.Errorf("commit no-op: %w", err)
		}
		return identity.APIKeyIdentity{
			ID:          identity.APIKeyID(rawID),
			Revision:    identity.Revision(currentRev),
			Lifecycle:   identity.Lifecycle(currentLC),
			CreatedAtMS: createdAtMS,
			UpdatedAtMS: updatedAtMS,
		}, nil
	}

	// Real transition: increment revision
	if identity.Revision(currentRev) >= math.MaxInt64 {
		return identity.APIKeyIdentity{}, fmt.Errorf("%w: current revision %d would exceed math.MaxInt64",
			ports.ErrRevisionOverflow, currentRev)
	}
	nextRev := identity.Revision(currentRev) + 1

	// Monotonic timestamp advancement
	persistedUpdatedAt := nowMS
	if persistedUpdatedAt <= updatedAtMS {
		if updatedAtMS == math.MaxInt64 {
			return identity.APIKeyIdentity{}, errors.New("cannot advance updatedAtMs: int64 max reached")
		}
		persistedUpdatedAt = updatedAtMS + 1
	}

	res, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayAPIKeyIdentitiesTable+`
		set revision = ?, lifecycle = ?, updated_at_ms = ?
		where id = ? and revision = ?`,
		int64(nextRev),
		string(nextLifecycle),
		persistedUpdatedAt,
		string(id),
		currentRev,
	)
	if err != nil {
		return identity.APIKeyIdentity{}, fmt.Errorf("update api key lifecycle: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return identity.APIKeyIdentity{}, fmt.Errorf("read rows affected: %w", err)
	}
	if rowsAffected != 1 {
		return identity.APIKeyIdentity{}, fmt.Errorf("%w: expected 1 row affected, got %d", ports.ErrRevisionConflict, rowsAffected)
	}

	if err := tx.Commit(); err != nil {
		return identity.APIKeyIdentity{}, fmt.Errorf("commit update: %w", err)
	}

	return identity.APIKeyIdentity{
		ID:          identity.APIKeyID(rawID),
		Revision:    nextRev,
		Lifecycle:   nextLifecycle,
		CreatedAtMS: createdAtMS,
		UpdatedAtMS: persistedUpdatedAt,
	}, nil
}

func (r *repository) SetCredentialLifecycle(
	ctx context.Context,
	id identity.CredentialID,
	expectedRevision identity.Revision,
	nextLifecycle identity.Lifecycle,
	nowMS int64,
) (identity.CredentialIdentity, error) {
	if err := id.Validate(); err != nil {
		return identity.CredentialIdentity{}, err
	}
	if !nextLifecycle.IsValid() {
		return identity.CredentialIdentity{}, fmt.Errorf("%w: %q", identity.ErrInvalidLifecycle, nextLifecycle)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return identity.CredentialIdentity{}, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var (
		rawID       string
		currentRev  int64
		currentLC   string
		createdAtMS int64
		updatedAtMS int64
	)
	err = tx.QueryRowContext(ctx, `select id, revision, lifecycle, created_at_ms, updated_at_ms
		from `+sqliterepo.GatewayCredentialIdentitiesTable+`
		where id = ?`, string(id)).Scan(&rawID, &currentRev, &currentLC, &createdAtMS, &updatedAtMS)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return identity.CredentialIdentity{}, ports.ErrNotFound
		}
		return identity.CredentialIdentity{}, fmt.Errorf("query current credential identity: %w", err)
	}

	// Stale expectedRevision must fail closed, even if nextLifecycle matches currentLC
	if identity.Revision(currentRev) != expectedRevision {
		return identity.CredentialIdentity{}, fmt.Errorf("%w: expected revision %d, current is %d",
			ports.ErrRevisionConflict, expectedRevision, currentRev)
	}

	// Validate lifecycle transition
	if err := identity.ValidateTransition(identity.Lifecycle(currentLC), nextLifecycle); err != nil {
		return identity.CredentialIdentity{}, fmt.Errorf("%w: %v", ports.ErrInvalidLifecycleTransition, err)
	}

	// If same-state, return current entity unchanged (expected revision has already been validated)
	if identity.Lifecycle(currentLC) == nextLifecycle {
		if err := tx.Commit(); err != nil {
			return identity.CredentialIdentity{}, fmt.Errorf("commit no-op: %w", err)
		}
		return identity.CredentialIdentity{
			ID:          identity.CredentialID(rawID),
			Revision:    identity.Revision(currentRev),
			Lifecycle:   identity.Lifecycle(currentLC),
			CreatedAtMS: createdAtMS,
			UpdatedAtMS: updatedAtMS,
		}, nil
	}

	// Real transition: increment revision
	if identity.Revision(currentRev) >= math.MaxInt64 {
		return identity.CredentialIdentity{}, fmt.Errorf("%w: current revision %d would exceed math.MaxInt64",
			ports.ErrRevisionOverflow, currentRev)
	}
	nextRev := identity.Revision(currentRev) + 1

	// Monotonic timestamp advancement
	persistedUpdatedAt := nowMS
	if persistedUpdatedAt <= updatedAtMS {
		if updatedAtMS == math.MaxInt64 {
			return identity.CredentialIdentity{}, errors.New("cannot advance updatedAtMs: int64 max reached")
		}
		persistedUpdatedAt = updatedAtMS + 1
	}

	res, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayCredentialIdentitiesTable+`
		set revision = ?, lifecycle = ?, updated_at_ms = ?
		where id = ? and revision = ?`,
		int64(nextRev),
		string(nextLifecycle),
		persistedUpdatedAt,
		string(id),
		currentRev,
	)
	if err != nil {
		return identity.CredentialIdentity{}, fmt.Errorf("update credential lifecycle: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return identity.CredentialIdentity{}, fmt.Errorf("read rows affected: %w", err)
	}
	if rowsAffected != 1 {
		return identity.CredentialIdentity{}, fmt.Errorf("%w: expected 1 row affected, got %d", ports.ErrRevisionConflict, rowsAffected)
	}

	if err := tx.Commit(); err != nil {
		return identity.CredentialIdentity{}, fmt.Errorf("commit update: %w", err)
	}

	return identity.CredentialIdentity{
		ID:          identity.CredentialID(rawID),
		Revision:    nextRev,
		Lifecycle:   nextLifecycle,
		CreatedAtMS: createdAtMS,
		UpdatedAtMS: persistedUpdatedAt,
	}, nil
}

func formatObservedGeneration(generation uint64) *string {
	if generation == 0 {
		return nil
	}
	s := strconv.FormatUint(generation, 10)
	return &s
}

func parseObservedGeneration(raw *string) (uint64, error) {
	if raw == nil {
		return 0, nil
	}
	s := *raw
	if s == "" || s == "0" {
		return 0, fmt.Errorf("invalid canonical generation decimal %q: must be non-empty and non-zero", s)
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("invalid canonical generation decimal %q: leading zeros are forbidden", s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("invalid canonical generation decimal %q: non-digit character %q", s, s[i])
		}
	}
	val, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse generation string %q: %w", s, err)
	}
	return val, nil
}

func isConstraintConflict(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code() & 0xff
		if code == sqlite3.SQLITE_CONSTRAINT {
			return true
		}
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "unique constraint failed") ||
		strings.Contains(lower, "constraint failed")
}
