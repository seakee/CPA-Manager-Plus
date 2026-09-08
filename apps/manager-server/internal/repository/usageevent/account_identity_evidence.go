package usageevent

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	CodexLegacyIdentityEvidenceTable         = "usage_codex_legacy_identity_evidence_v1"
	CodexLegacyIdentityRollupName            = "codex_legacy_identity_v1"
	CodexLegacyIdentityEvidenceSchemaVersion = usageidentity.CodexLegacyIdentityEvidenceSchemaVersion
	// Bump when the stored fields, physical predicates, or chronology projection
	// change. Account-key/model revisions do not change this raw evidence.
	CodexLegacyIdentityEvidenceRevision = "1"
	codexLegacyIdentityTailLimit        = 1000
)

// UpsertCodexLegacyIdentityEvidenceRange compacts only the requested event-ID
// range. The caller commits these rows and its coverage checkpoint together.
// Keep all providers and identity values: contradictory evidence must survive
// compaction and reach the same authority check as the raw reader.
func UpsertCodexLegacyIdentityEvidenceRange(ctx context.Context, tx *sql.Tx, revision string, afterID, throughID int64) error {
	if throughID <= afterID {
		return nil
	}
	physicalPredicates := []struct {
		column string
		filter string
	}{
		{"e.auth_file_snapshot", "e.auth_file_snapshot is not null and e.auth_file_snapshot <> ''"},
		{"e.source", "e.auth_file_snapshot is null" + legacySourceIdentityGuards()},
		{"e.source", "e.auth_file_snapshot = ''" + legacySourceIdentityGuards()},
	}
	for kind, physical := range physicalPredicates {
		query := `insert into ` + CodexLegacyIdentityEvidenceTable + ` (
			structure_revision, physical_kind, physical_file, auth_index,
			provider, auth_provider_snapshot, auth_account_id_snapshot,
			auth_project_id_snapshot, account_snapshot,
			min_evidence_at_ms, max_evidence_at_ms, chronology_unknown
		) select ?, ?, ` + physical.column + `, e.auth_index, ` + legacyAccountIdentityEvidenceColumns + `
		from usage_events e not indexed
		where e.id > ? and e.id <= ?
			and coalesce(e.auth_index, '') <> ''
			and coalesce(` + physical.column + `, '') <> ''
			and ` + physical.filter + legacyAccountIdentityEvidenceGroupBy + `,
			` + physical.column + ` collate nocase, e.auth_index collate nocase
		on conflict do update set
			min_evidence_at_ms = case
				when min_evidence_at_ms = 0 then excluded.min_evidence_at_ms
				when excluded.min_evidence_at_ms = 0 then min_evidence_at_ms
				else min(min_evidence_at_ms, excluded.min_evidence_at_ms)
			end,
			max_evidence_at_ms = max(max_evidence_at_ms, excluded.max_evidence_at_ms),
			chronology_unknown = max(chronology_unknown, excluded.chronology_unknown)`
		if _, err := tx.ExecContext(ctx, query, revision, kind, afterID, throughID); err != nil {
			return fmt.Errorf("compact Codex legacy identity evidence: %w", err)
		}
	}
	return nil
}

// ClearCodexLegacyIdentityEvidenceBatch removes a previous evidence revision
// in bounded transactions before a new revision starts rebuilding.
func ClearCodexLegacyIdentityEvidenceBatch(ctx context.Context, tx *sql.Tx, limit int) (bool, error) {
	if _, err := tx.ExecContext(ctx, `delete from `+CodexLegacyIdentityEvidenceTable+` where rowid in (
		select rowid from `+CodexLegacyIdentityEvidenceTable+` limit ?
	)`, limit); err != nil {
		return false, err
	}
	var pending bool
	err := tx.QueryRowContext(ctx, `select exists(select 1 from `+CodexLegacyIdentityEvidenceTable+` limit 1)`).Scan(&pending)
	return pending, err
}

func queryStoredCodexLegacyIdentityEvidence(ctx context.Context, queryer SQLQueryer, authFile, authIndex string) ([]legacyAccountIdentityEvidence, bool, error) {
	coverageID, latestID, available, err := codexLegacyIdentityEvidenceReadState(ctx, queryer)
	if err != nil || !available {
		return nil, false, err
	}
	groups := make([]legacyAccountIdentityEvidence, 0)
	for kind, predicate := range legacyAccountIdentityPredicates(authFile, authIndex) {
		rows, err := queryer.QueryContext(ctx, `select
			provider, auth_provider_snapshot, auth_account_id_snapshot,
			auth_project_id_snapshot, account_snapshot,
			min_evidence_at_ms, max_evidence_at_ms, chronology_unknown
		from `+CodexLegacyIdentityEvidenceTable+`
		where structure_revision = ? and physical_kind = ?
			and physical_file = ? collate nocase and auth_index = ? collate nocase`,
			CodexLegacyIdentityEvidenceRevision, kind, authFile, authIndex)
		if err != nil {
			return nil, false, err
		}
		stored, err := scanLegacyAccountIdentityEvidenceRows(rows)
		if err != nil {
			return nil, false, err
		}
		groups = append(groups, stored...)
		if coverageID == latestID {
			continue
		}
		// NOT INDEXED retains the bounded rowid range instead of letting SQLite
		// revisit the credential's entire historical file/source index range.
		query := `select ` + legacyAccountIdentityEvidenceColumns + `from usage_events e not indexed
		where e.id > ? and e.id <= ? and ` + predicate.sql + legacyAccountIdentityEvidenceGroupBy
		args := append([]any{coverageID, latestID}, predicate.args...)
		rows, err = queryer.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, false, err
		}
		tail, err := scanLegacyAccountIdentityEvidenceRows(rows)
		if err != nil {
			return nil, false, err
		}
		groups = append(groups, tail...)
	}
	return groups, true, nil
}

func codexLegacyIdentityEvidenceReadState(ctx context.Context, queryer SQLQueryer) (int64, int64, bool, error) {
	rows, err := queryer.QueryContext(ctx, `select
		s.schema_version, s.structure_revision, s.status, s.coverage_event_id, s.target_event_id,
		coalesce((select max(id) from usage_events), 0)
	from usage_monitoring_rollup_state s
	where s.rollup_name = ? and exists (
		select 1 from sqlite_master where type = 'table' and name = ?
	)`, CodexLegacyIdentityRollupName, CodexLegacyIdentityEvidenceTable)
	if err != nil {
		return 0, 0, false, err
	}
	var version int
	var revision, status string
	var coverageID, targetEventID, rawMaxID int64
	if !rows.Next() {
		err := rows.Err()
		_ = rows.Close()
		return 0, 0, false, err
	}
	if err := rows.Scan(&version, &revision, &status, &coverageID, &targetEventID, &rawMaxID); err != nil {
		_ = rows.Close()
		return 0, 0, false, err
	}
	if err := rows.Close(); err != nil {
		return 0, 0, false, err
	}
	if err := rows.Err(); err != nil {
		return 0, 0, false, err
	}
	latestKnownID := targetEventID
	if rawMaxID > latestKnownID {
		latestKnownID = rawMaxID
	}
	if version != CodexLegacyIdentityEvidenceSchemaVersion || revision != CodexLegacyIdentityEvidenceRevision || coverageID < 0 || coverageID > latestKnownID {
		return 0, 0, false, nil
	}
	if status != "ready" && status != "catching_up" && status != "rebuilding" {
		return 0, 0, false, nil
	}
	if coverageID != latestKnownID {
		rows, err := queryer.QueryContext(ctx, `select count(*) from (
			select id from usage_events where id > ? order by id limit ?
		)`, coverageID, codexLegacyIdentityTailLimit+1)
		if err != nil {
			return 0, 0, false, err
		}
		var count int
		if !rows.Next() {
			err := rows.Err()
			_ = rows.Close()
			return 0, 0, false, err
		}
		if err := rows.Scan(&count); err != nil {
			_ = rows.Close()
			return 0, 0, false, err
		}
		if err := rows.Close(); err != nil {
			return 0, 0, false, err
		}
		if err := rows.Err(); err != nil {
			return 0, 0, false, err
		}
		if count > codexLegacyIdentityTailLimit {
			return 0, 0, false, nil
		}
	}
	return coverageID, latestKnownID, true, nil
}
