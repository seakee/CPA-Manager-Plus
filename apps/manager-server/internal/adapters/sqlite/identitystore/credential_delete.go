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

type pendingCredentialDelete struct {
	id, runtimeIdentity, physicalName, ownerInstance string
	generation                                       uint64
	createdAtMS                                      int64
	forwardCompletedAtMS                             sql.NullInt64
	items                                            []credentialDeleteItem
}

type credentialDeleteItem struct {
	credentialID     identity.CredentialID
	sourceAuthID     string
	expectedRevision identity.Revision
}

func validDeleteName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name
}

func loadCurrentCredentialSource(ctx context.Context, tx *sql.Tx, runtimeIdentity, sourceAuthID string) (identity.CredentialIdentity, identity.CredentialSourceBinding, bool, error) {
	var ent identity.CredentialIdentity
	var binding identity.CredentialSourceBinding
	var revision int64
	var lifecycle string
	var rawGeneration *string
	var retiredAt sql.NullInt64
	err := tx.QueryRowContext(ctx, `select i.id, i.revision, i.lifecycle, i.created_at_ms, i.updated_at_ms,
		b.binding_id, b.credential_id, b.runtime_identity, b.source_auth_id,
		b.auth_index, b.provider, b.physical_name, b.account_snapshot, b.account_id_snapshot,
		b.observed_runtime_generation, b.first_seen_at_ms, b.last_seen_at_ms, b.retired_at_ms
		from `+sqliterepo.GatewayCredentialSourceBindingsTable+` b
		join `+sqliterepo.GatewayCredentialIdentitiesTable+` i on i.id = b.credential_id
		where b.runtime_identity = ? and b.source_auth_id = ? and b.retired_at_ms is null`,
		runtimeIdentity, sourceAuthID).Scan(
		&ent.ID, &revision, &lifecycle, &ent.CreatedAtMS, &ent.UpdatedAtMS,
		&binding.BindingID, &binding.CredentialID, &binding.RuntimeIdentity, &binding.SourceAuthID,
		&binding.AuthIndex, &binding.Provider, &binding.PhysicalName, &binding.AccountSnapshot, &binding.AccountIDSnapshot,
		&rawGeneration, &binding.FirstSeenAtMS, &binding.LastSeenAtMS, &retiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, false, nil
	}
	if err != nil {
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, false, fmt.Errorf("load credential delete source: %w", err)
	}
	ent.Revision = identity.Revision(revision)
	ent.Lifecycle = identity.Lifecycle(lifecycle)
	binding.RetiredAtMS = retiredAt.Int64
	binding.ObservedRuntimeGeneration, err = parseObservedGeneration(rawGeneration)
	if err != nil {
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, false, fmt.Errorf("persisted credential generation: %w", err)
	}
	if err := ent.Validate(); err != nil {
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, false, fmt.Errorf("persisted credential identity: %w", err)
	}
	if err := binding.Validate(); err != nil {
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, false, fmt.Errorf("persisted credential binding: %w", err)
	}
	if ent.ID != binding.CredentialID || binding.RuntimeIdentity != runtimeIdentity ||
		binding.SourceAuthID != sourceAuthID || binding.RetiredAtMS != 0 ||
		(ent.Lifecycle != identity.LifecycleActive && ent.Lifecycle != identity.LifecycleMissing) {
		return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, false, errors.New("invalid credential delete source relationship")
	}
	return ent, binding, true, nil
}

func loadPendingCredentialDeletes(ctx context.Context, tx *sql.Tx, runtimeIdentity string) ([]pendingCredentialDelete, error) {
	rows, err := tx.QueryContext(ctx, `select id, runtime_identity, observed_runtime_generation, physical_name,
		owner_instance, created_at_ms, forward_completed_at_ms from `+sqliterepo.GatewayCredentialDeleteIntentsTable+`
		where runtime_identity = ? order by id`, runtimeIdentity)
	if err != nil {
		return nil, err
	}
	var pending []pendingCredentialDelete
	for rows.Next() {
		var m pendingCredentialDelete
		var generation string
		if err := rows.Scan(&m.id, &m.runtimeIdentity, &generation, &m.physicalName,
			&m.ownerInstance, &m.createdAtMS, &m.forwardCompletedAtMS); err != nil {
			rows.Close()
			return nil, err
		}
		m.generation, err = parseObservedGeneration(&generation)
		if err != nil || m.generation == 0 || !validDeleteName(m.runtimeIdentity) ||
			!validDeleteName(m.physicalName) || identity.CredentialID(m.id).Validate() != nil ||
			!validDeleteName(m.ownerInstance) ||
			m.createdAtMS <= 0 || (m.forwardCompletedAtMS.Valid && m.forwardCompletedAtMS.Int64 < m.createdAtMS) {
			rows.Close()
			return nil, errors.New("invalid persisted credential delete intent")
		}
		pending = append(pending, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range pending {
		itemRows, err := tx.QueryContext(ctx, `select credential_id, source_auth_id, expected_revision
			from `+sqliterepo.GatewayCredentialDeleteIntentItemsTable+` where intent_id = ? order by source_auth_id`, pending[i].id)
		if err != nil {
			return nil, err
		}
		seenIDs := make(map[identity.CredentialID]struct{})
		seenSources := make(map[string]struct{})
		for itemRows.Next() {
			var item credentialDeleteItem
			var revision int64
			if err := itemRows.Scan(&item.credentialID, &item.sourceAuthID, &revision); err != nil {
				itemRows.Close()
				return nil, err
			}
			item.expectedRevision = identity.Revision(revision)
			_, duplicateID := seenIDs[item.credentialID]
			if item.credentialID.Validate() != nil || !validDeleteName(item.sourceAuthID) ||
				revision <= 0 || duplicateID {
				itemRows.Close()
				return nil, errors.New("invalid persisted credential delete item")
			}
			if _, ok := seenSources[item.sourceAuthID]; ok {
				itemRows.Close()
				return nil, errors.New("duplicate persisted credential delete source")
			}
			seenIDs[item.credentialID] = struct{}{}
			seenSources[item.sourceAuthID] = struct{}{}
			pending[i].items = append(pending[i].items, item)
		}
		err = itemRows.Err()
		itemRows.Close()
		if err != nil || len(pending[i].items) == 0 {
			return nil, errors.New("invalid persisted credential delete items")
		}
	}
	return pending, nil
}

func (r *repository) CheckPendingCredentialDelete(ctx context.Context, physicalNames []string, all bool) error {
	rows, err := r.db.QueryContext(ctx, `select runtime_identity, physical_name from `+sqliterepo.GatewayCredentialDeleteIntentsTable)
	if err != nil {
		return err
	}
	defer rows.Close()
	names := make(map[string]struct{}, len(physicalNames))
	for _, name := range physicalNames {
		names[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	for rows.Next() {
		var runtimeIdentity, physicalName string
		if err := rows.Scan(&runtimeIdentity, &physicalName); err != nil {
			return err
		}
		if !validDeleteName(runtimeIdentity) || !validDeleteName(physicalName) {
			return errors.New("invalid persisted credential delete overlap")
		}
		if all {
			return ports.ErrPendingCredentialDelete
		}
		if _, found := names[strings.ToLower(physicalName)]; found {
			return ports.ErrPendingCredentialDelete
		}
	}
	return rows.Err()
}

func (r *repository) PrepareCredentialDelete(ctx context.Context, p ports.PrepareCredentialDeleteParams) (string, error) {
	if !validDeleteName(p.RuntimeIdentity) || !validDeleteName(p.PhysicalName) ||
		p.ObservedRuntimeGeneration == 0 || p.OwnerInstance == "" || p.NowMS <= 0 || len(p.SourceAuthIDs) == 0 {
		return "", errors.New("invalid credential delete preparation")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	seenIDs := make(map[identity.CredentialID]struct{})
	items := make([]credentialDeleteItem, 0, len(p.SourceAuthIDs))
	seenSources := make(map[string]struct{})
	for _, source := range p.SourceAuthIDs {
		if !validDeleteName(source) {
			return "", identity.ErrInvalidSourceAuthID
		}
		if _, duplicate := seenSources[source]; duplicate {
			return "", errors.New("duplicate credential delete source")
		}
		seenSources[source] = struct{}{}
		ent, _, found, err := loadCurrentCredentialSource(ctx, tx, p.RuntimeIdentity, source)
		if err != nil {
			return "", err
		}
		if !found {
			return "", ports.ErrNotFound
		}
		if _, duplicate := seenIDs[ent.ID]; duplicate {
			return "", errors.New("duplicate canonical credential in delete")
		}
		seenIDs[ent.ID] = struct{}{}
		items = append(items, credentialDeleteItem{credentialID: ent.ID, sourceAuthID: source, expectedRevision: ent.Revision})
	}
	id, err := identity.NewCredentialID()
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayCredentialDeleteIntentsTable+`
		(id, runtime_identity, observed_runtime_generation, physical_name, owner_instance, created_at_ms)
		values (?, ?, ?, ?, ?, ?)`, string(id), p.RuntimeIdentity,
		strconv.FormatUint(p.ObservedRuntimeGeneration, 10), p.PhysicalName, p.OwnerInstance, p.NowMS)
	if err != nil {
		if isConstraintConflict(err) {
			return "", ports.ErrPendingCredentialDelete
		}
		return "", err
	}
	for _, item := range items {
		_, err := tx.ExecContext(ctx, `insert into `+sqliterepo.GatewayCredentialDeleteIntentItemsTable+`
			(intent_id, credential_id, source_auth_id, expected_revision) values (?, ?, ?, ?)`,
			string(id), string(item.credentialID), item.sourceAuthID, int64(item.expectedRevision))
		if err != nil {
			if isConstraintConflict(err) {
				return "", ports.ErrPendingCredentialDelete
			}
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return string(id), nil
}

func (r *repository) MarkCredentialDeleteForwardComplete(ctx context.Context, intentID, ownerInstance string, nowMS int64) error {
	if intentID == "" || ownerInstance == "" || nowMS <= 0 {
		return errors.New("invalid credential delete completion")
	}
	result, err := r.db.ExecContext(ctx, `update `+sqliterepo.GatewayCredentialDeleteIntentsTable+`
		set forward_completed_at_ms = max(created_at_ms, ?)
		where id = ? and owner_instance = ? and forward_completed_at_ms is null`,
		nowMS, intentID, ownerInstance)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("credential delete completion conflict")
	}
	return nil
}

func observedCredentialSet(sourceIDs []string) (map[string]struct{}, error) {
	set := make(map[string]struct{}, len(sourceIDs))
	for _, source := range sourceIDs {
		if !validDeleteName(source) {
			return nil, identity.ErrInvalidSourceAuthID
		}
		if _, duplicate := set[source]; duplicate {
			return nil, errors.New("ambiguous credential observation")
		}
		set[source] = struct{}{}
	}
	return set, nil
}

func (r *repository) ResolveCredentialDelete(ctx context.Context, p ports.ResolveCredentialDeleteParams) (ports.CredentialDeleteOutcome, error) {
	if !validDeleteName(p.RuntimeIdentity) || p.ObservedRuntimeGeneration == 0 || p.IntentID == "" || p.NowMS <= 0 {
		return ports.CredentialDeleteUnknown, errors.New("invalid credential delete resolution")
	}
	set, err := observedCredentialSet(p.ObservedSourceAuthIDs)
	if err != nil {
		return ports.CredentialDeleteUnknown, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ports.CredentialDeleteUnknown, err
	}
	defer tx.Rollback()
	pending, err := loadPendingCredentialDeletes(ctx, tx, p.RuntimeIdentity)
	if err != nil {
		return ports.CredentialDeleteUnknown, err
	}
	for _, m := range pending {
		if m.id != p.IntentID {
			continue
		}
		if !m.forwardCompletedAtMS.Valid {
			return ports.CredentialDeleteUnknown, errors.New("credential delete forward completion unavailable")
		}
		outcome, err := resolvePendingCredentialDelete(ctx, tx, m, set, p.NowMS)
		if err != nil {
			return ports.CredentialDeleteUnknown, err
		}
		if err := tx.Commit(); err != nil {
			return ports.CredentialDeleteUnknown, err
		}
		return outcome, nil
	}
	return ports.CredentialDeleteUnknown, errors.New("credential delete intent unavailable")
}

func resolvePendingCredentialDelete(ctx context.Context, tx *sql.Tx, m pendingCredentialDelete, observed map[string]struct{}, nowMS int64) (ports.CredentialDeleteOutcome, error) {
	present := 0
	for _, item := range m.items {
		ent, binding, found, err := loadCurrentCredentialSource(ctx, tx, m.runtimeIdentity, item.sourceAuthID)
		if err != nil {
			return ports.CredentialDeleteUnknown, err
		}
		if !found || ent.ID != item.credentialID || binding.RetiredAtMS != 0 {
			return ports.CredentialDeleteUnknown, errors.New("credential delete Canonical source conflict")
		}
		if ent.Revision != item.expectedRevision {
			return ports.CredentialDeleteUnknown, ports.ErrRevisionConflict
		}
		if _, ok := observed[item.sourceAuthID]; ok {
			present++
		}
	}
	if present > 0 && present < len(m.items) {
		return ports.CredentialDeleteUnknown, nil
	}
	outcome := ports.CredentialDeleteNotApplied
	if present == 0 {
		outcome = ports.CredentialDeleteSuccess
		for _, item := range m.items {
			ent, binding, _, err := loadCurrentCredentialSource(ctx, tx, m.runtimeIdentity, item.sourceAuthID)
			if err != nil {
				return ports.CredentialDeleteUnknown, err
			}
			if ent.Revision >= math.MaxInt64 {
				return ports.CredentialDeleteUnknown, ports.ErrRevisionOverflow
			}
			updatedAt, err := nextUpdatedAt(ent.UpdatedAtMS, nowMS)
			if err != nil {
				return ports.CredentialDeleteUnknown, err
			}
			result, err := tx.ExecContext(ctx, `update `+sqliterepo.GatewayCredentialIdentitiesTable+`
				set revision = ?, lifecycle = ?, updated_at_ms = ? where id = ? and revision = ?`,
				int64(ent.Revision+1), string(identity.LifecycleSuperseded), updatedAt, string(ent.ID), int64(ent.Revision))
			if err != nil {
				return ports.CredentialDeleteUnknown, err
			}
			count, err := result.RowsAffected()
			if err != nil || count != 1 {
				return ports.CredentialDeleteUnknown, ports.ErrRevisionConflict
			}
			retiredAt := monotonicObservedAt(nowMS, binding.FirstSeenAtMS, binding.LastSeenAtMS)
			result, err = tx.ExecContext(ctx, `update `+sqliterepo.GatewayCredentialSourceBindingsTable+`
				set retired_at_ms = ? where binding_id = ? and retired_at_ms is null`, retiredAt, binding.BindingID)
			if err != nil {
				return ports.CredentialDeleteUnknown, err
			}
			count, err = result.RowsAffected()
			if err != nil || count != 1 {
				return ports.CredentialDeleteUnknown, errors.New("credential delete retirement conflict")
			}
		}
	}
	result, err := tx.ExecContext(ctx, `delete from `+sqliterepo.GatewayCredentialDeleteIntentsTable+` where id = ?`, m.id)
	if err != nil {
		return ports.CredentialDeleteUnknown, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ports.CredentialDeleteUnknown, errors.New("credential delete intent removal conflict")
	}
	return outcome, nil
}

func resolvePassiveCredentialDeletes(ctx context.Context, tx *sql.Tx, p ports.ReconcileSnapshotParams, observed map[string]ports.CredentialSnapshotItem) (map[string]struct{}, error) {
	pending, err := loadPendingCredentialDeletes(ctx, tx, p.RuntimeIdentity)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(observed))
	for source := range observed {
		set[source] = struct{}{}
	}
	suppressed := make(map[string]struct{})
	for _, m := range pending {
		canResolve := p.ProcessInstanceID != "" && m.ownerInstance != p.ProcessInstanceID
		if m.ownerInstance == p.ProcessInstanceID && m.forwardCompletedAtMS.Valid &&
			p.CaptureStartedAtMS > m.forwardCompletedAtMS.Int64 {
			canResolve = true
		}
		if canResolve {
			outcome, err := resolvePendingCredentialDelete(ctx, tx, m, set, p.NowMS)
			if err != nil {
				return nil, err
			}
			if outcome != ports.CredentialDeleteUnknown {
				continue
			}
		}
		for _, item := range m.items {
			suppressed[item.sourceAuthID] = struct{}{}
		}
	}
	return suppressed, nil
}
