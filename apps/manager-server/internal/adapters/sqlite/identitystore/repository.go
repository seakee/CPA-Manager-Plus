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
		where b.runtime_identity = ? and b.api_key_hash = ? and b.retired_at_ms is null and i.lifecycle = ?`,
		runtimeIdentity, canonicalHash, string(identity.LifecycleActive),
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
		where b.runtime_identity = ? and b.source_auth_id = ? and b.retired_at_ms is null and i.lifecycle = ?`,
		runtimeIdentity, sourceAuthID, string(identity.LifecycleActive),
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

	current := identity.APIKeyIdentity{
		ID:          identity.APIKeyID(rawID),
		Revision:    identity.Revision(currentRev),
		Lifecycle:   identity.Lifecycle(currentLC),
		CreatedAtMS: createdAtMS,
		UpdatedAtMS: updatedAtMS,
	}
	if err := current.Validate(); err != nil {
		return identity.APIKeyIdentity{}, fmt.Errorf("persisted api key identity invalid: %w", err)
	}

	// Stale expectedRevision must fail closed, even if nextLifecycle matches currentLC
	if current.Revision != expectedRevision {
		return identity.APIKeyIdentity{}, fmt.Errorf("%w: expected revision %d, current is %d",
			ports.ErrRevisionConflict, expectedRevision, current.Revision)
	}

	// Validate lifecycle transition
	if err := identity.ValidateTransition(current.Lifecycle, nextLifecycle); err != nil {
		return identity.APIKeyIdentity{}, fmt.Errorf("%w: %v", ports.ErrInvalidLifecycleTransition, err)
	}

	// If same-state, return current entity unchanged (expected revision has already been validated)
	if current.Lifecycle == nextLifecycle {
		if err := tx.Commit(); err != nil {
			return identity.APIKeyIdentity{}, fmt.Errorf("commit no-op: %w", err)
		}
		return current, nil
	}

	// Real transition: increment revision
	if current.Revision >= math.MaxInt64 {
		return identity.APIKeyIdentity{}, fmt.Errorf("%w: current revision %d would exceed math.MaxInt64",
			ports.ErrRevisionOverflow, current.Revision)
	}
	nextRev := current.Revision + 1

	// Monotonic timestamp advancement
	persistedUpdatedAt, err := nextUpdatedAt(current.UpdatedAtMS, nowMS)
	if err != nil {
		return identity.APIKeyIdentity{}, err
	}

	res, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayAPIKeyIdentitiesTable+`
		set revision = ?, lifecycle = ?, updated_at_ms = ?
		where id = ? and revision = ?`,
		int64(nextRev),
		string(nextLifecycle),
		persistedUpdatedAt,
		string(id),
		int64(current.Revision),
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
		ID:          current.ID,
		Revision:    nextRev,
		Lifecycle:   nextLifecycle,
		CreatedAtMS: current.CreatedAtMS,
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

	current := identity.CredentialIdentity{
		ID:          identity.CredentialID(rawID),
		Revision:    identity.Revision(currentRev),
		Lifecycle:   identity.Lifecycle(currentLC),
		CreatedAtMS: createdAtMS,
		UpdatedAtMS: updatedAtMS,
	}
	if err := current.Validate(); err != nil {
		return identity.CredentialIdentity{}, fmt.Errorf("persisted credential identity invalid: %w", err)
	}

	// Stale expectedRevision must fail closed, even if nextLifecycle matches currentLC
	if current.Revision != expectedRevision {
		return identity.CredentialIdentity{}, fmt.Errorf("%w: expected revision %d, current is %d",
			ports.ErrRevisionConflict, expectedRevision, current.Revision)
	}

	// Validate lifecycle transition
	if err := identity.ValidateTransition(current.Lifecycle, nextLifecycle); err != nil {
		return identity.CredentialIdentity{}, fmt.Errorf("%w: %v", ports.ErrInvalidLifecycleTransition, err)
	}

	// If same-state, return current entity unchanged (expected revision has already been validated)
	if current.Lifecycle == nextLifecycle {
		if err := tx.Commit(); err != nil {
			return identity.CredentialIdentity{}, fmt.Errorf("commit no-op: %w", err)
		}
		return current, nil
	}

	// Real transition: increment revision
	if current.Revision >= math.MaxInt64 {
		return identity.CredentialIdentity{}, fmt.Errorf("%w: current revision %d would exceed math.MaxInt64",
			ports.ErrRevisionOverflow, current.Revision)
	}
	nextRev := current.Revision + 1

	// Monotonic timestamp advancement
	persistedUpdatedAt, err := nextUpdatedAt(current.UpdatedAtMS, nowMS)
	if err != nil {
		return identity.CredentialIdentity{}, err
	}

	res, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayCredentialIdentitiesTable+`
		set revision = ?, lifecycle = ?, updated_at_ms = ?
		where id = ? and revision = ?`,
		int64(nextRev),
		string(nextLifecycle),
		persistedUpdatedAt,
		string(id),
		int64(current.Revision),
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
		ID:          current.ID,
		Revision:    nextRev,
		Lifecycle:   nextLifecycle,
		CreatedAtMS: current.CreatedAtMS,
		UpdatedAtMS: persistedUpdatedAt,
	}, nil
}

func monotonicObservedAt(requested, firstSeen, currentLastSeen int64) int64 {
	next := requested
	if next < currentLastSeen {
		next = currentLastSeen
	}
	if next < firstSeen {
		next = firstSeen
	}
	return next
}

func nextUpdatedAt(current, requested int64) (int64, error) {
	if requested > current {
		return requested, nil
	}
	if current == math.MaxInt64 {
		return 0, errors.New("cannot advance updatedAtMs: int64 max reached")
	}
	return current + 1, nil
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

func (r *repository) ApplyPassiveSnapshot(ctx context.Context, params ports.ReconcileSnapshotParams) (ports.ReconcileSnapshotResult, error) {
	if params.RuntimeIdentity == "" || strings.TrimSpace(params.RuntimeIdentity) != params.RuntimeIdentity {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: %q", identity.ErrInvalidRuntimeIdentity, params.RuntimeIdentity)
	}
	if params.ObservedRuntimeGeneration == 0 {
		return ports.ReconcileSnapshotResult{}, errors.New("observed runtime generation must be non-zero")
	}
	if params.NowMS <= 0 {
		return ports.ReconcileSnapshotResult{}, errors.New("nowMS must be positive")
	}

	// Validate snapshot API keys
	uniqueAPIKeys := make(map[string]struct{}, len(params.APIKeys))
	for idx, k := range params.APIKeys {
		canonicalHash := strings.ToLower(strings.TrimSpace(k.APIKeyHash))
		if !identity.IsValidSHA256Hex(canonicalHash) {
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("item at index %d: %w", idx, identity.ErrInvalidAPIKeyHash)
		}
		uniqueAPIKeys[canonicalHash] = struct{}{}
	}

	// Validate snapshot credentials
	uniqueCredentials := make(map[string]ports.CredentialSnapshotItem, len(params.Credentials))
	for idx, c := range params.Credentials {
		sourceAuthID := strings.TrimSpace(c.SourceAuthID)
		if sourceAuthID == "" {
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("item at index %d: %w", idx, identity.ErrInvalidSourceAuthID)
		}
		c.SourceAuthID = sourceAuthID
		uniqueCredentials[sourceAuthID] = c
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("begin snapshot tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var result ports.ReconcileSnapshotResult

	suppressedAPIKeys, err := resolvePassivePending(ctx, tx, params.RuntimeIdentity,
		params.ProcessInstanceID, params.CaptureStartedAtMS, uniqueAPIKeys,
		params.ObservedRuntimeGeneration, params.NowMS)
	if err != nil {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("resolve pending API-key mutation: %w", err)
	}
	suppressedCredentials, err := resolvePassiveCredentialDeletes(ctx, tx, params, uniqueCredentials)
	if err != nil {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("resolve pending credential delete: %w", err)
	}

	// --- 1. Reconcile API Keys ---
	type existingAPIKey struct {
		entity  identity.APIKeyIdentity
		binding identity.APIKeySourceBinding
	}

	rows, err := tx.QueryContext(ctx, `select
		i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		b.binding_id, b.api_key_id, b.runtime_identity, b.api_key_hash,
		b.observed_runtime_generation, b.first_seen_at_ms, b.last_seen_at_ms, b.retired_at_ms
		from `+sqliterepo.GatewayAPIKeySourceBindingsTable+` b
		join `+sqliterepo.GatewayAPIKeyIdentitiesTable+` i on b.api_key_id = i.id
		where b.runtime_identity = ? and b.retired_at_ms is null`, params.RuntimeIdentity)
	if err != nil {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("query current api keys: %w", err)
	}
	defer rows.Close()

	existingAPIKeys := make(map[string]existingAPIKey)
	for rows.Next() {
		var (
			rawID         string
			rev           int64
			lifecycle     string
			createdAtMS   int64
			updatedAtMS   int64
			bindingID     int64
			bAPIKeyID     string
			bRTIdentity   string
			bHash         string
			observedGen   *string
			firstSeenAtMS int64
			lastSeenAtMS  int64
			retiredAtMS   sql.NullInt64
		)
		if err := rows.Scan(
			&rawID, &rev, &lifecycle, &createdAtMS, &updatedAtMS,
			&bindingID, &bAPIKeyID, &bRTIdentity, &bHash,
			&observedGen, &firstSeenAtMS, &lastSeenAtMS, &retiredAtMS,
		); err != nil {
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("scan current api key: %w", err)
		}

		gen, err := parseObservedGeneration(observedGen)
		if err != nil {
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("parse observed runtime generation: %w", err)
		}

		ent := identity.APIKeyIdentity{
			ID:          identity.APIKeyID(rawID),
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
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("persisted entity invalid: %w", err)
		}
		if err := binding.Validate(); err != nil {
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("persisted binding invalid: %w", err)
		}
		if ent.Lifecycle == identity.LifecycleSuperseded {
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: active binding points to superseded api key %q",
				identity.ErrInvalidLifecycleTransition, ent.ID)
		}

		existingAPIKeys[binding.APIKeyHash] = existingAPIKey{
			entity:  ent,
			binding: binding,
		}
	}
	if err := rows.Err(); err != nil {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("iterate current api keys: %w", err)
	}
	rows.Close()

	observedGenStr := formatObservedGeneration(params.ObservedRuntimeGeneration)

	// Process present API keys
	for hash := range uniqueAPIKeys {
		if _, suppressed := suppressedAPIKeys[hash]; suppressed {
			continue
		}
		if existing, exists := existingAPIKeys[hash]; exists {
			switch existing.entity.Lifecycle {
			case identity.LifecycleActive:
				// Active stays active: same ID, same business revision, refresh binding last seen & generation
				nextLastSeen := monotonicObservedAt(params.NowMS, existing.binding.FirstSeenAtMS, existing.binding.LastSeenAtMS)
				_, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayAPIKeySourceBindingsTable+`
					set last_seen_at_ms = ?, observed_runtime_generation = ?
					where binding_id = ?`,
					nextLastSeen, observedGenStr, existing.binding.BindingID)
				if err != nil {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("refresh api key binding: %w", err)
				}
				result.APIKeysRefreshed++
			case identity.LifecycleMissing:
				// Missing returns: same ID, missing -> active, revision + 1
				if existing.entity.Revision >= math.MaxInt64 {
					return ports.ReconcileSnapshotResult{}, ports.ErrRevisionOverflow
				}
				nextRev := existing.entity.Revision + 1
				persistedUpdatedAt, err := nextUpdatedAt(existing.entity.UpdatedAtMS, params.NowMS)
				if err != nil {
					return ports.ReconcileSnapshotResult{}, err
				}
				res, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayAPIKeyIdentitiesTable+`
					set revision = ?, lifecycle = ?, updated_at_ms = ?
					where id = ? and revision = ?`,
					int64(nextRev), string(identity.LifecycleActive), persistedUpdatedAt,
					string(existing.entity.ID), int64(existing.entity.Revision))
				if err != nil {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("recover api key identity: %w", err)
				}
				ra, err := res.RowsAffected()
				if err != nil || ra != 1 {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: api key recovery conflict", ports.ErrRevisionConflict)
				}
				nextLastSeen := monotonicObservedAt(params.NowMS, existing.binding.FirstSeenAtMS, existing.binding.LastSeenAtMS)
				_, err = tx.ExecContext(ctx, `update `+sqliterepo.GatewayAPIKeySourceBindingsTable+`
					set last_seen_at_ms = ?, observed_runtime_generation = ?
					where binding_id = ?`,
					nextLastSeen, observedGenStr, existing.binding.BindingID)
				if err != nil {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("refresh recovered api key binding: %w", err)
				}
				result.APIKeysRecovered++
			}
		} else {
			// New source: create new Canonical ID + initial binding
			newID, err := identity.NewAPIKeyID()
			if err != nil {
				return ports.ReconcileSnapshotResult{}, fmt.Errorf("generate api key id: %w", err)
			}
			_, err = tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayAPIKeyIdentitiesTable+` (
				id, revision, lifecycle, created_at_ms, updated_at_ms
			) values (?, ?, ?, ?, ?)`,
				string(newID), int64(identity.InitialRevision), string(identity.LifecycleActive),
				params.NowMS, params.NowMS)
			if err != nil {
				return ports.ReconcileSnapshotResult{}, fmt.Errorf("insert api key identity: %w", err)
			}
			_, err = tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayAPIKeySourceBindingsTable+` (
				api_key_id, runtime_identity, api_key_hash, observed_runtime_generation,
				first_seen_at_ms, last_seen_at_ms, retired_at_ms
			) values (?, ?, ?, ?, ?, ?, null)`,
				string(newID), params.RuntimeIdentity, hash, observedGenStr,
				params.NowMS, params.NowMS)
			if err != nil {
				if isConstraintConflict(err) {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: %v", ports.ErrSourceBindingConflict, err)
				}
				return ports.ReconcileSnapshotResult{}, fmt.Errorf("insert api key source binding: %w", err)
			}
			result.APIKeysCreated++
		}
	}

	// Negative evidence for API keys: active sources absent from snapshot transition to missing
	for hash, existing := range existingAPIKeys {
		if _, suppressed := suppressedAPIKeys[hash]; suppressed {
			continue
		}
		if _, present := uniqueAPIKeys[hash]; !present {
			if existing.entity.Lifecycle == identity.LifecycleActive {
				if existing.entity.Revision >= math.MaxInt64 {
					return ports.ReconcileSnapshotResult{}, ports.ErrRevisionOverflow
				}
				nextRev := existing.entity.Revision + 1
				persistedUpdatedAt, err := nextUpdatedAt(existing.entity.UpdatedAtMS, params.NowMS)
				if err != nil {
					return ports.ReconcileSnapshotResult{}, err
				}
				res, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayAPIKeyIdentitiesTable+`
					set revision = ?, lifecycle = ?, updated_at_ms = ?
					where id = ? and revision = ?`,
					int64(nextRev), string(identity.LifecycleMissing), persistedUpdatedAt,
					string(existing.entity.ID), int64(existing.entity.Revision))
				if err != nil {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("set api key missing: %w", err)
				}
				ra, err := res.RowsAffected()
				if err != nil || ra != 1 {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: api key missing conflict", ports.ErrRevisionConflict)
				}
				// Note: binding remains retired_at_ms IS NULL so source can recover to same ID
				result.APIKeysMissing++
			}
		}
	}

	// --- 2. Reconcile Credentials ---
	type existingCred struct {
		entity  identity.CredentialIdentity
		binding identity.CredentialSourceBinding
	}

	cRows, err := tx.QueryContext(ctx, `select
		i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		b.binding_id, b.credential_id, b.runtime_identity, b.source_auth_id,
		b.auth_index, b.provider, b.physical_name, b.account_snapshot, b.account_id_snapshot,
		b.observed_runtime_generation, b.first_seen_at_ms, b.last_seen_at_ms, b.retired_at_ms
		from `+sqliterepo.GatewayCredentialSourceBindingsTable+` b
		join `+sqliterepo.GatewayCredentialIdentitiesTable+` i on b.credential_id = i.id
		where b.runtime_identity = ? and b.retired_at_ms is null`, params.RuntimeIdentity)
	if err != nil {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("query current credentials: %w", err)
	}
	defer cRows.Close()

	existingCreds := make(map[string]existingCred)
	for cRows.Next() {
		var (
			rawID            string
			rev              int64
			lifecycle        string
			createdAtMS      int64
			updatedAtMS      int64
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
		if err := cRows.Scan(
			&rawID, &rev, &lifecycle, &createdAtMS, &updatedAtMS,
			&bindingID, &bCredID, &bRTIdentity, &bSourceAuthID,
			&bAuthIndex, &bProvider, &bPhysicalName, &bAccountSnapshot, &bAccountIDSnap,
			&observedGen, &firstSeenAtMS, &lastSeenAtMS, &retiredAtMS,
		); err != nil {
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("scan current credential: %w", err)
		}

		gen, err := parseObservedGeneration(observedGen)
		if err != nil {
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("parse observed runtime generation: %w", err)
		}

		ent := identity.CredentialIdentity{
			ID:          identity.CredentialID(rawID),
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
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("persisted entity invalid: %w", err)
		}
		if err := binding.Validate(); err != nil {
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("persisted binding invalid: %w", err)
		}
		if ent.Lifecycle == identity.LifecycleSuperseded {
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: active binding points to superseded credential %q",
				identity.ErrInvalidLifecycleTransition, ent.ID)
		}

		existingCreds[binding.SourceAuthID] = existingCred{
			entity:  ent,
			binding: binding,
		}
	}
	if err := cRows.Err(); err != nil {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("iterate current credentials: %w", err)
	}
	cRows.Close()

	// Process present credentials
	for authID, credItem := range uniqueCredentials {
		if _, suppressed := suppressedCredentials[authID]; suppressed {
			continue
		}
		if existing, exists := existingCreds[authID]; exists {
			switch existing.entity.Lifecycle {
			case identity.LifecycleActive:
				// Active stays active: same ID, same revision, refresh metadata & generation in binding
				nextLastSeen := monotonicObservedAt(params.NowMS, existing.binding.FirstSeenAtMS, existing.binding.LastSeenAtMS)
				_, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayCredentialSourceBindingsTable+`
					set auth_index = ?, provider = ?, physical_name = ?,
					    account_snapshot = ?, account_id_snapshot = ?,
					    observed_runtime_generation = ?, last_seen_at_ms = ?
					where binding_id = ?`,
					credItem.AuthIndex, credItem.Provider, credItem.PhysicalName,
					credItem.AccountSnapshot, credItem.AccountIDSnapshot,
					observedGenStr, nextLastSeen, existing.binding.BindingID)
				if err != nil {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("refresh credential binding: %w", err)
				}
				result.CredentialsRefreshed++
			case identity.LifecycleMissing:
				// Missing returns: same ID, missing -> active, revision + 1
				if existing.entity.Revision >= math.MaxInt64 {
					return ports.ReconcileSnapshotResult{}, ports.ErrRevisionOverflow
				}
				nextRev := existing.entity.Revision + 1
				persistedUpdatedAt, err := nextUpdatedAt(existing.entity.UpdatedAtMS, params.NowMS)
				if err != nil {
					return ports.ReconcileSnapshotResult{}, err
				}
				res, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayCredentialIdentitiesTable+`
					set revision = ?, lifecycle = ?, updated_at_ms = ?
					where id = ? and revision = ?`,
					int64(nextRev), string(identity.LifecycleActive), persistedUpdatedAt,
					string(existing.entity.ID), int64(existing.entity.Revision))
				if err != nil {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("recover credential identity: %w", err)
				}
				ra, err := res.RowsAffected()
				if err != nil || ra != 1 {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: credential recovery conflict", ports.ErrRevisionConflict)
				}
				nextLastSeen := monotonicObservedAt(params.NowMS, existing.binding.FirstSeenAtMS, existing.binding.LastSeenAtMS)
				_, err = tx.ExecContext(ctx, `update `+sqliterepo.GatewayCredentialSourceBindingsTable+`
					set auth_index = ?, provider = ?, physical_name = ?,
					    account_snapshot = ?, account_id_snapshot = ?,
					    observed_runtime_generation = ?, last_seen_at_ms = ?
					where binding_id = ?`,
					credItem.AuthIndex, credItem.Provider, credItem.PhysicalName,
					credItem.AccountSnapshot, credItem.AccountIDSnapshot,
					observedGenStr, nextLastSeen, existing.binding.BindingID)
				if err != nil {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("refresh recovered credential binding: %w", err)
				}
				result.CredentialsRecovered++
			}
		} else {
			// New source: create new Canonical CredentialID + initial binding
			newID, err := identity.NewCredentialID()
			if err != nil {
				return ports.ReconcileSnapshotResult{}, fmt.Errorf("generate credential id: %w", err)
			}
			_, err = tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayCredentialIdentitiesTable+` (
				id, revision, lifecycle, created_at_ms, updated_at_ms
			) values (?, ?, ?, ?, ?)`,
				string(newID), int64(identity.InitialRevision), string(identity.LifecycleActive),
				params.NowMS, params.NowMS)
			if err != nil {
				return ports.ReconcileSnapshotResult{}, fmt.Errorf("insert credential identity: %w", err)
			}
			_, err = tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayCredentialSourceBindingsTable+` (
				credential_id, runtime_identity, source_auth_id,
				auth_index, provider, physical_name,
				account_snapshot, account_id_snapshot,
				observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms
			) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, null)`,
				string(newID), params.RuntimeIdentity, authID,
				credItem.AuthIndex, credItem.Provider, credItem.PhysicalName,
				credItem.AccountSnapshot, credItem.AccountIDSnapshot,
				observedGenStr, params.NowMS, params.NowMS)
			if err != nil {
				if isConstraintConflict(err) {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: %v", ports.ErrSourceBindingConflict, err)
				}
				return ports.ReconcileSnapshotResult{}, fmt.Errorf("insert credential source binding: %w", err)
			}
			result.CredentialsCreated++
		}
	}

	// Negative evidence for Credentials: active sources absent from snapshot transition to missing
	for authID, existing := range existingCreds {
		if _, suppressed := suppressedCredentials[authID]; suppressed {
			continue
		}
		if _, present := uniqueCredentials[authID]; !present {
			if existing.entity.Lifecycle == identity.LifecycleActive {
				if existing.entity.Revision >= math.MaxInt64 {
					return ports.ReconcileSnapshotResult{}, ports.ErrRevisionOverflow
				}
				nextRev := existing.entity.Revision + 1
				persistedUpdatedAt, err := nextUpdatedAt(existing.entity.UpdatedAtMS, params.NowMS)
				if err != nil {
					return ports.ReconcileSnapshotResult{}, err
				}
				res, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayCredentialIdentitiesTable+`
					set revision = ?, lifecycle = ?, updated_at_ms = ?
					where id = ? and revision = ?`,
					int64(nextRev), string(identity.LifecycleMissing), persistedUpdatedAt,
					string(existing.entity.ID), int64(existing.entity.Revision))
				if err != nil {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("set credential missing: %w", err)
				}
				ra, err := res.RowsAffected()
				if err != nil || ra != 1 {
					return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: credential missing conflict", ports.ErrRevisionConflict)
				}
				// Note: binding remains retired_at_ms IS NULL
				result.CredentialsMissing++
			}
		}
	}

	// Atomic Commit
	if err := tx.Commit(); err != nil {
		if isConstraintConflict(err) {
			return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: %v", ports.ErrSourceBindingConflict, err)
		}
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("commit passive snapshot: %w", err)
	}

	return result, nil
}
