package identitystore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

type pendingAPIKeyMutation struct {
	id, runtimeIdentity, oldHash, newHash, ownerInstance string
	kind                                                 ports.APIKeyMutationKind
	apiKeyID                                             identity.APIKeyID
	observedGeneration                                   uint64
	expectedRevision                                     identity.Revision
	createdAtMS                                          int64
	forwardCompletedAtMS                                 sql.NullInt64
}

func (m pendingAPIKeyMutation) validate() error {
	if m.id == "" || m.runtimeIdentity == "" || strings.TrimSpace(m.runtimeIdentity) != m.runtimeIdentity ||
		m.ownerInstance == "" || m.createdAtMS <= 0 || m.observedGeneration == 0 ||
		m.expectedRevision == 0 || m.expectedRevision > math.MaxInt64 ||
		!identity.IsValidSHA256Hex(m.oldHash) {
		return errors.New("invalid persisted API-key mutation intent")
	}
	if err := m.apiKeyID.Validate(); err != nil {
		return fmt.Errorf("invalid persisted API-key mutation identity: %w", err)
	}
	if m.forwardCompletedAtMS.Valid && m.forwardCompletedAtMS.Int64 < m.createdAtMS {
		return errors.New("invalid persisted API-key mutation completion time")
	}
	switch m.kind {
	case ports.APIKeyMutationRotate:
		if !identity.IsValidSHA256Hex(m.newHash) || m.oldHash == m.newHash {
			return errors.New("invalid persisted rotation source")
		}
	case ports.APIKeyMutationDelete:
		if m.newHash != "" {
			return errors.New("invalid persisted deletion source")
		}
	default:
		return errors.New("invalid persisted API-key mutation kind")
	}
	return nil
}

func loadPendingMutation(ctx context.Context, tx *sql.Tx, runtimeIdentity string) (pendingAPIKeyMutation, bool, error) {
	var m pendingAPIKeyMutation
	var kind, generation string
	var newHash sql.NullString
	var revision int64
	err := tx.QueryRowContext(ctx, `select id, kind, runtime_identity, observed_runtime_generation,
		api_key_id, expected_revision, old_api_key_hash, new_api_key_hash, created_at_ms,
		owner_instance, forward_completed_at_ms
		from `+sqliterepo.GatewayAPIKeyMutationIntentsTable+` where runtime_identity = ?`,
		runtimeIdentity).Scan(&m.id, &kind, &m.runtimeIdentity, &generation, &m.apiKeyID,
		&revision, &m.oldHash, &newHash, &m.createdAtMS, &m.ownerInstance, &m.forwardCompletedAtMS)
	if errors.Is(err, sql.ErrNoRows) {
		return pendingAPIKeyMutation{}, false, nil
	}
	if err != nil {
		return pendingAPIKeyMutation{}, false, fmt.Errorf("load API-key mutation intent: %w", err)
	}
	m.kind = ports.APIKeyMutationKind(kind)
	m.expectedRevision = identity.Revision(revision)
	m.newHash = newHash.String
	m.observedGeneration, err = parseObservedGeneration(&generation)
	if err != nil {
		return pendingAPIKeyMutation{}, false, fmt.Errorf("persisted API-key mutation generation: %w", err)
	}
	if err := m.validate(); err != nil {
		return pendingAPIKeyMutation{}, false, err
	}
	return m, true, nil
}

func loadCurrentKeySource(ctx context.Context, tx *sql.Tx, runtimeIdentity, hash string) (identity.APIKeyIdentity, identity.APIKeySourceBinding, bool, error) {
	var ent identity.APIKeyIdentity
	var binding identity.APIKeySourceBinding
	var revision int64
	var lifecycle string
	var rawGeneration *string
	var retiredAt sql.NullInt64
	err := tx.QueryRowContext(ctx, `select i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		b.binding_id, b.api_key_id, b.runtime_identity, b.api_key_hash, b.observed_runtime_generation,
		b.first_seen_at_ms, b.last_seen_at_ms, b.retired_at_ms
		from `+sqliterepo.GatewayAPIKeySourceBindingsTable+` b
		join `+sqliterepo.GatewayAPIKeyIdentitiesTable+` i on i.id = b.api_key_id
		where b.runtime_identity = ? and b.api_key_hash = ? and b.retired_at_ms is null`,
		runtimeIdentity, hash).Scan(
		&ent.ID, &revision, &lifecycle, &ent.CreatedAtMS, &ent.UpdatedAtMS,
		&binding.BindingID, &binding.APIKeyID, &binding.RuntimeIdentity, &binding.APIKeyHash,
		&rawGeneration, &binding.FirstSeenAtMS, &binding.LastSeenAtMS, &retiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, false, nil
	}
	if err != nil {
		return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, false, fmt.Errorf("load current API-key source: %w", err)
	}
	ent.Revision = identity.Revision(revision)
	ent.Lifecycle = identity.Lifecycle(lifecycle)
	binding.RetiredAtMS = retiredAt.Int64
	binding.ObservedRuntimeGeneration, err = parseObservedGeneration(rawGeneration)
	if err != nil {
		return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, false, fmt.Errorf("persisted API-key source generation: %w", err)
	}
	if err := ent.Validate(); err != nil {
		return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, false, fmt.Errorf("persisted API-key identity invalid: %w", err)
	}
	if err := binding.Validate(); err != nil {
		return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, false, fmt.Errorf("persisted API-key binding invalid: %w", err)
	}
	if ent.ID != binding.APIKeyID || binding.RuntimeIdentity != runtimeIdentity || binding.APIKeyHash != hash ||
		binding.RetiredAtMS != 0 || ent.Lifecycle == identity.LifecycleSuperseded {
		return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, false, errors.New("persisted API-key source relationship invalid")
	}
	return ent, binding, true, nil
}

func (r *repository) HasPendingAPIKeyMutation(ctx context.Context, runtimeIdentity string) (bool, error) {
	if runtimeIdentity == "" || strings.TrimSpace(runtimeIdentity) != runtimeIdentity {
		return false, identity.ErrInvalidRuntimeIdentity
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	_, found, err := loadPendingMutation(ctx, tx, runtimeIdentity)
	return found, err
}

func (r *repository) HasAnyPendingAPIKeyMutation(ctx context.Context) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `select 1 from `+sqliterepo.GatewayAPIKeyMutationIntentsTable+` limit 1`).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *repository) PrepareAPIKeyMutation(ctx context.Context, p ports.PrepareAPIKeyMutationParams) (string, error) {
	if p.RuntimeIdentity == "" || strings.TrimSpace(p.RuntimeIdentity) != p.RuntimeIdentity ||
		p.ObservedRuntimeGeneration == 0 || p.OwnerInstance == "" || p.NowMS <= 0 ||
		!identity.IsValidSHA256Hex(p.Evidence.OldHash) {
		return "", errors.New("invalid API-key mutation preparation")
	}
	switch p.Kind {
	case ports.APIKeyMutationRotate:
		if p.Evidence.ExactOldCount != 1 || p.Evidence.NormalizedOldCount != 1 ||
			p.Evidence.NormalizedNewCount != 0 || !identity.IsValidSHA256Hex(p.Evidence.NewHash) ||
			p.Evidence.NewHash == p.Evidence.OldHash {
			return "", errors.New("rotation evidence is not authoritative")
		}
	case ports.APIKeyMutationDelete:
		if p.Evidence.NormalizedOldCount < 1 || p.Evidence.NewHash != "" {
			return "", errors.New("delete evidence is not authoritative")
		}
	default:
		return "", errors.New("invalid API-key mutation kind")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin API-key mutation preparation: %w", err)
	}
	defer tx.Rollback()
	if _, found, err := loadPendingMutation(ctx, tx, p.RuntimeIdentity); err != nil {
		return "", err
	} else if found {
		return "", ports.ErrPendingAPIKeyMutation
	}
	ent, binding, found, err := loadCurrentKeySource(ctx, tx, p.RuntimeIdentity, p.Evidence.OldHash)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ports.ErrNotFound
	}
	if ent.Lifecycle != identity.LifecycleActive && ent.Lifecycle != identity.LifecycleMissing {
		return "", ports.ErrInvalidLifecycleTransition
	}
	if binding.RetiredAtMS != 0 {
		return "", errors.New("API-key source is retired")
	}
	if p.Kind == ports.APIKeyMutationRotate {
		_, _, conflict, err := loadCurrentKeySource(ctx, tx, p.RuntimeIdentity, p.Evidence.NewHash)
		if err != nil {
			return "", err
		}
		if conflict {
			return "", ports.ErrSourceBindingConflict
		}
	}
	newID, err := identity.NewAPIKeyID()
	if err != nil {
		return "", err
	}
	var newHash any
	if p.Kind == ports.APIKeyMutationRotate {
		newHash = p.Evidence.NewHash
	}
	_, err = tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayAPIKeyMutationIntentsTable+` (
		id, kind, runtime_identity, observed_runtime_generation, api_key_id, expected_revision,
		old_api_key_hash, new_api_key_hash, created_at_ms, owner_instance
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(newID), string(p.Kind), p.RuntimeIdentity,
		strconv.FormatUint(p.ObservedRuntimeGeneration, 10), string(ent.ID), int64(ent.Revision),
		p.Evidence.OldHash, newHash, p.NowMS, p.OwnerInstance)
	if err != nil {
		if isConstraintConflict(err) {
			return "", ports.ErrPendingAPIKeyMutation
		}
		return "", fmt.Errorf("insert API-key mutation intent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit API-key mutation intent: %w", err)
	}
	return string(newID), nil
}

func (r *repository) MarkAPIKeyMutationForwardComplete(ctx context.Context, intentID, ownerInstance string, nowMS int64) error {
	if intentID == "" || ownerInstance == "" || nowMS <= 0 {
		return errors.New("invalid API-key mutation completion")
	}
	result, err := r.db.ExecContext(ctx, `update `+sqliterepo.GatewayAPIKeyMutationIntentsTable+`
		set forward_completed_at_ms = max(created_at_ms, ?)
		where id = ? and owner_instance = ? and forward_completed_at_ms is null`,
		nowMS, intentID, ownerInstance)
	if err != nil {
		return fmt.Errorf("mark API-key mutation forward complete: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("API-key mutation completion conflict")
	}
	return nil
}

func observedHashSet(hashes []string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		if !identity.IsValidSHA256Hex(hash) {
			return nil, identity.ErrInvalidAPIKeyHash
		}
		set[hash] = struct{}{}
	}
	return set, nil
}

func (r *repository) ResolveAPIKeyMutation(ctx context.Context, p ports.ResolveAPIKeyMutationParams) (ports.APIKeyMutationOutcome, error) {
	if p.RuntimeIdentity == "" || strings.TrimSpace(p.RuntimeIdentity) != p.RuntimeIdentity ||
		p.ObservedRuntimeGeneration == 0 || p.NowMS <= 0 || p.IntentID == "" {
		return "", errors.New("invalid API-key mutation resolution")
	}
	set, err := observedHashSet(p.ObservedHashes)
	if err != nil {
		return "", err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin API-key mutation resolution: %w", err)
	}
	defer tx.Rollback()
	m, found, err := loadPendingMutation(ctx, tx, p.RuntimeIdentity)
	if err != nil {
		return "", err
	}
	if !found || m.id != p.IntentID || !m.forwardCompletedAtMS.Valid {
		return "", errors.New("API-key mutation intent is unavailable for resolution")
	}
	outcome, err := resolvePendingMutation(ctx, tx, m, set, p.ObservedRuntimeGeneration, p.NowMS)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit API-key mutation resolution: %w", err)
	}
	return outcome, nil
}

func resolvePendingMutation(ctx context.Context, tx *sql.Tx, m pendingAPIKeyMutation, hashes map[string]struct{}, generation uint64, nowMS int64) (ports.APIKeyMutationOutcome, error) {
	ent, binding, found, err := loadCurrentKeySource(ctx, tx, m.runtimeIdentity, m.oldHash)
	if err != nil {
		return "", err
	}
	if !found || ent.ID != m.apiKeyID || binding.RetiredAtMS != 0 ||
		(ent.Lifecycle != identity.LifecycleActive &&
			ent.Lifecycle != identity.LifecycleMissing) {
		return "", errors.New("API-key mutation Canonical source conflict")
	}
	if ent.Revision != m.expectedRevision {
		return "", ports.ErrRevisionConflict
	}
	_, oldPresent := hashes[m.oldHash]
	_, newPresent := hashes[m.newHash]
	outcome := ports.APIKeyMutationUnknown
	switch m.kind {
	case ports.APIKeyMutationRotate:
		if !oldPresent && newPresent {
			outcome = ports.APIKeyMutationSuccess
		} else if oldPresent && !newPresent {
			outcome = ports.APIKeyMutationNotApplied
		}
	case ports.APIKeyMutationDelete:
		if oldPresent {
			outcome = ports.APIKeyMutationNotApplied
		} else {
			outcome = ports.APIKeyMutationSuccess
		}
	}
	if outcome == ports.APIKeyMutationUnknown {
		return outcome, nil
	}
	if outcome == ports.APIKeyMutationSuccess {
		if err := finalizeAPIKeyMutation(ctx, tx, m, generation, nowMS); err != nil {
			return "", err
		}
	}
	result, err := tx.ExecContext(ctx, `delete from `+sqliterepo.GatewayAPIKeyMutationIntentsTable+` where id = ?`, m.id)
	if err != nil {
		return "", fmt.Errorf("delete resolved API-key mutation intent: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return "", errors.New("API-key mutation intent deletion conflict")
	}
	return outcome, nil
}

// resolvePassivePending runs under the snapshot's existing immediate-writer
// transaction. A snapshot captured before prepare or while the local request
// was in flight can suppress sources but cannot decide NOT_APPLIED.
func resolvePassivePending(ctx context.Context, tx *sql.Tx, runtimeIdentity, processInstance string, captureStartedAtMS int64, hashes map[string]struct{}, generation uint64, nowMS int64) (map[string]struct{}, error) {
	m, found, err := loadPendingMutation(ctx, tx, runtimeIdentity)
	if err != nil || !found {
		return nil, err
	}
	// The Manager database process lock ensures a different process instance
	// can observe only after the former owner has stopped. This also survives
	// wall-clock rollback across restart.
	canResolve := processInstance != "" && m.ownerInstance != processInstance
	if m.ownerInstance == processInstance && m.forwardCompletedAtMS.Valid &&
		captureStartedAtMS > m.forwardCompletedAtMS.Int64 {
		canResolve = true
	}
	if canResolve {
		outcome, err := resolvePendingMutation(ctx, tx, m, hashes, generation, nowMS)
		if err != nil {
			return nil, err
		}
		if outcome != ports.APIKeyMutationUnknown {
			return nil, nil
		}
	}
	suppressed := map[string]struct{}{m.oldHash: {}}
	if m.kind == ports.APIKeyMutationRotate {
		suppressed[m.newHash] = struct{}{}
	}
	return suppressed, nil
}

func finalizeAPIKeyMutation(ctx context.Context, tx *sql.Tx, m pendingAPIKeyMutation, generation uint64, nowMS int64) error {
	ent, binding, found, err := loadCurrentKeySource(ctx, tx, m.runtimeIdentity, m.oldHash)
	if err != nil {
		return err
	}
	if !found || ent.ID != m.apiKeyID || binding.RetiredAtMS != 0 {
		return errors.New("API-key mutation source conflict")
	}
	if ent.Revision != m.expectedRevision {
		return ports.ErrRevisionConflict
	}
	if ent.Revision >= math.MaxInt64 {
		return ports.ErrRevisionOverflow
	}
	if ent.Lifecycle != identity.LifecycleActive && ent.Lifecycle != identity.LifecycleMissing {
		return ports.ErrInvalidLifecycleTransition
	}
	if m.kind == ports.APIKeyMutationRotate {
		_, _, conflict, err := loadCurrentKeySource(ctx, tx, m.runtimeIdentity, m.newHash)
		if err != nil {
			return err
		}
		if conflict {
			return ports.ErrSourceBindingConflict
		}
	}
	updatedAt, err := nextUpdatedAt(ent.UpdatedAtMS, nowMS)
	if err != nil {
		return err
	}
	retiredAt := monotonicObservedAt(nowMS, binding.FirstSeenAtMS, binding.LastSeenAtMS)
	lifecycle := identity.LifecycleActive
	if m.kind == ports.APIKeyMutationDelete {
		lifecycle = identity.LifecycleSuperseded
	}
	result, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayAPIKeyIdentitiesTable+`
		set revision = ?, lifecycle = ?, updated_at_ms = ? where id = ? and revision = ?`,
		int64(ent.Revision+1), string(lifecycle), updatedAt, string(ent.ID), int64(ent.Revision))
	if err != nil {
		return fmt.Errorf("update API-key mutation identity: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ports.ErrRevisionConflict
	}
	result, err = tx.ExecContext(ctx, `update `+sqliterepo.GatewayAPIKeySourceBindingsTable+`
		set retired_at_ms = ? where binding_id = ? and retired_at_ms is null`, retiredAt, binding.BindingID)
	if err != nil {
		return fmt.Errorf("retire API-key mutation source: %w", err)
	}
	count, err = result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("API-key mutation source retirement conflict")
	}
	if m.kind == ports.APIKeyMutationRotate {
		_, err = tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayAPIKeySourceBindingsTable+` (
			api_key_id, runtime_identity, api_key_hash, observed_runtime_generation,
			first_seen_at_ms, last_seen_at_ms, retired_at_ms
		) values (?, ?, ?, ?, ?, ?, null)`,
			string(ent.ID), m.runtimeIdentity, m.newHash, strconv.FormatUint(generation, 10), nowMS, nowMS)
		if err != nil {
			if isConstraintConflict(err) {
				return ports.ErrSourceBindingConflict
			}
			return fmt.Errorf("insert rotated API-key source: %w", err)
		}
	}
	return nil
}
