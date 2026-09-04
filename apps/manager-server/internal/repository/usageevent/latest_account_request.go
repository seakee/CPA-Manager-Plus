package usageevent

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	latestRequestAuthFileIndex = "idx_usage_events_latest_request_auth_file"
	latestRequestSourceIndex   = "idx_usage_events_latest_request_source"
)

// snapshotLatestRequestByFileAndIndexSQL is the indexed Top-N path for a
// credential with a non-empty auth_index. EXPLAIN tests pin this to the
// composite latest-request auth-file index, including auth_index.
const snapshotLatestRequestByFileAndIndexSQL = `select
	e.id,
	e.timestamp_ms,
	e.failed,
	e.fail_status_code,
	coalesce(e.fail_summary, ''),
	coalesce(e.header_error_kind, ''),
	coalesce(e.header_error_code, ''),
	coalesce(e.header_trace_id, '')
from usage_events e
where e.auth_file_snapshot collate nocase = ?
	and e.auth_index collate nocase = ?
order by e.timestamp_ms desc, e.id desc
limit ?`

// snapshotLatestRequestByFileAndEmptyIndexSQL is the indexed Top-N path for a
// credential whose auth_index is the empty string. EXPLAIN tests pin this to
// the same composite index, including COLLATE NOCASE on auth_index.
const snapshotLatestRequestByFileAndEmptyIndexSQL = `select
	e.id,
	e.timestamp_ms,
	e.failed,
	e.fail_status_code,
	coalesce(e.fail_summary, ''),
	coalesce(e.header_error_kind, ''),
	coalesce(e.header_error_code, ''),
	coalesce(e.header_trace_id, '')
from usage_events e
where e.auth_file_snapshot collate nocase = ?
	and e.auth_index collate nocase = ''
order by e.timestamp_ms desc, e.id desc
limit ?`

// LatestAccountRequestQuery identifies one credential using the immutable
// snapshot captured with a request. AuthFileSnapshot is the primary identity;
// Source is used only for records created before auth-file snapshots existed.
type LatestAccountRequestQuery struct {
	RequestIndex          int
	AuthFileSnapshot      string
	AuthIndex             string
	Provider              string
	AuthAccountIDSnapshot string
	AuthProjectIDSnapshot string
	AccountSnapshot       string
}

// LatestAccountRequest contains the safe diagnostics needed by the credential
// list. Sensitive database-only fields such as fail_body and raw_json are
// deliberately not represented here.
type LatestAccountRequest struct {
	RequestIndex    int
	TimestampMS     int64
	Failed          bool
	FailStatusCode  sql.NullInt64
	FailSummary     string
	HeaderErrorKind string
	HeaderErrorCode string
	HeaderTraceID   string
}

type rankedAccountRequest struct {
	LatestAccountRequest
	id int64
}

type latestRequestPredicate struct {
	sql  string
	args []any
}

func (r *repository) RecentAccountRequests(
	ctx context.Context,
	targets []LatestAccountRequestQuery,
	limit int,
) ([]LatestAccountRequest, error) {
	if len(targets) == 0 || limit <= 0 {
		return []LatestAccountRequest{}, nil
	}
	ready, err := r.latestRequestIndexesReady(ctx)
	if err != nil {
		return nil, err
	}
	if ready {
		return r.recentAccountRequestsIndexed(ctx, targets, limit)
	}
	return r.recentAccountRequestsBatched(ctx, targets, limit)
}

func (r *repository) latestRequestIndexesReady(ctx context.Context) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `select count(*) from sqlite_master
		where type = 'index'
			and tbl_name = 'usage_events'
			and name in (?, ?)`,
		latestRequestAuthFileIndex,
		latestRequestSourceIndex,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count == 2, nil
}

func (r *repository) recentAccountRequestsIndexed(
	ctx context.Context,
	targets []LatestAccountRequestQuery,
	limit int,
) ([]LatestAccountRequest, error) {
	requests := make([]LatestAccountRequest, 0, len(targets)*limit)
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		authFileSnapshot := strings.TrimSpace(target.AuthFileSnapshot)
		if authFileSnapshot == "" {
			continue
		}
		authIndex := strings.TrimSpace(target.AuthIndex)
		snapshot, err := r.recentAccountRequestsByPredicates(
			ctx,
			target.RequestIndex,
			limit,
			withLatestRequestIdentity(
				snapshotLatestRequestPredicates(authFileSnapshot, authIndex),
				target,
			),
		)
		if err != nil {
			return nil, err
		}
		legacy, err := r.recentAccountRequestsByPredicates(
			ctx,
			target.RequestIndex,
			limit,
			withLatestRequestIdentity(
				legacyLatestRequestPredicates(authFileSnapshot, authIndex),
				target,
			),
		)
		if err != nil {
			return nil, err
		}
		requests = append(requests, mergeRecentAccountRequests(limit, snapshot, legacy)...)
	}
	return requests, nil
}

func withLatestRequestIdentity(
	predicates []latestRequestPredicate,
	target LatestAccountRequestQuery,
) []latestRequestPredicate {
	identitySQL, identityArgs := latestRequestIdentityPredicate(target)
	if identitySQL == "" {
		return predicates
	}
	result := make([]latestRequestPredicate, 0, len(predicates))
	for _, predicate := range predicates {
		args := make([]any, 0, len(predicate.args)+len(identityArgs))
		args = append(args, predicate.args...)
		args = append(args, identityArgs...)
		result = append(result, latestRequestPredicate{
			sql:  predicate.sql + identitySQL,
			args: args,
		})
	}
	return result
}

func latestRequestIdentityPredicate(target LatestAccountRequestQuery) (string, []any) {
	if normalizeLatestRequestProvider(target.Provider) != "codex" {
		return "", nil
	}

	workspaceID, member, identityOK, identityConflict := resolveLatestRequestCodexIdentity(target)
	if identityConflict {
		// Explicitly conflicting Workspace evidence must never fall back to a
		// physical credential selector.
		return " and 1 = 0", nil
	}
	if !identityOK {
		// Missing or weak member evidence is allowed to use the exact physical
		// credential predicate already supplied by the caller.
		return "", nil
	}
	marker := usageidentity.CodexAccountIDSnapshot(workspaceID)
	return `
		and lower(replace(trim(coalesce(nullif(trim(e.auth_provider_snapshot), ''), e.provider, '')), '_', '-')) = 'codex'
		and (trim(coalesce(e.provider, '')) = '' or lower(replace(trim(e.provider), '_', '-')) = 'codex')
		and (trim(coalesce(e.auth_provider_snapshot, '')) = '' or lower(replace(trim(e.auth_provider_snapshot), '_', '-')) = 'codex')
		and lower(trim(coalesce(e.account_snapshot, ''))) = ?
		and (
			(
				trim(coalesce(e.auth_account_id_snapshot, '')) = ?
				and (
					trim(coalesce(e.auth_project_id_snapshot, '')) = ''
					or substr(trim(coalesce(e.auth_project_id_snapshot, '')), 1, length('codex-account-id:v1:')) <> 'codex-account-id:v1:'
					or trim(coalesce(e.auth_project_id_snapshot, '')) = ?
				)
			)
			or (
				trim(coalesce(e.auth_account_id_snapshot, '')) = ''
				and trim(coalesce(e.auth_project_id_snapshot, '')) = ?
			)
		)`, []any{member, workspaceID, marker, marker}
}

// resolveLatestRequestCodexIdentity returns a member filter only when the
// target has complete, non-conflicting Codex Workspace/member evidence. An
// incomplete target deliberately falls back to its file/index predicates;
// only explicit provenance conflicts fail closed.
func resolveLatestRequestCodexIdentity(target LatestAccountRequestQuery) (workspace, member string, complete, conflict bool) {
	workspaceID, workspaceOK := usageidentity.ResolveCodexWorkspace(usageidentity.Fields{
		AuthAccountIDSnapshot: target.AuthAccountIDSnapshot,
		AuthProjectIDSnapshot: target.AuthProjectIDSnapshot,
	})
	workspaceEvidence := strings.Trim(target.AuthAccountIDSnapshot, " ") != "" ||
		strings.HasPrefix(strings.Trim(target.AuthProjectIDSnapshot, " "), "codex-account-id:v1:")
	if workspaceEvidence && !workspaceOK {
		return "", "", false, true
	}
	member, memberOK := usageidentity.NormalizeCodexMemberSnapshot(target.AccountSnapshot)
	if !workspaceOK || !memberOK {
		return "", "", false, false
	}
	return workspaceID, member, true, false
}

func latestRequestBatchedIdentityPredicate(targetAlias, eventAlias string) string {
	return `(
		` + targetAlias + `.provider <> 'codex'
		or ` + targetAlias + `.identity_mode = 0
		or (
			` + targetAlias + `.identity_mode = 1
			and lower(replace(trim(coalesce(nullif(trim(` + eventAlias + `.auth_provider_snapshot), ''), ` + eventAlias + `.provider, '')), '_', '-')) = 'codex'
			and (trim(coalesce(` + eventAlias + `.provider, '')) = '' or lower(replace(trim(` + eventAlias + `.provider), '_', '-')) = 'codex')
			and (trim(coalesce(` + eventAlias + `.auth_provider_snapshot, '')) = '' or lower(replace(trim(` + eventAlias + `.auth_provider_snapshot), '_', '-')) = 'codex')
			and lower(trim(coalesce(` + eventAlias + `.account_snapshot, ''))) = ` + targetAlias + `.member
			and (
				substr(trim(coalesce(` + targetAlias + `.project_id, '')), 1, length('codex-account-id:v1:')) <> 'codex-account-id:v1:'
				or trim(coalesce(` + targetAlias + `.project_id, '')) = 'codex-account-id:v1:' || ` + targetAlias + `.workspace_id
			)
			and (
				(
					trim(coalesce(` + eventAlias + `.auth_account_id_snapshot, '')) = ` + targetAlias + `.workspace_id
					and (
						trim(coalesce(` + eventAlias + `.auth_project_id_snapshot, '')) = ''
						or substr(trim(coalesce(` + eventAlias + `.auth_project_id_snapshot, '')), 1, length('codex-account-id:v1:')) <> 'codex-account-id:v1:'
						or trim(coalesce(` + eventAlias + `.auth_project_id_snapshot, '')) = 'codex-account-id:v1:' || ` + targetAlias + `.workspace_id
					)
				)
				or (
					trim(coalesce(` + eventAlias + `.auth_account_id_snapshot, '')) = ''
					and trim(coalesce(` + eventAlias + `.auth_project_id_snapshot, '')) = 'codex-account-id:v1:' || ` + targetAlias + `.workspace_id
				)
			)
		)
	)`
}

func normalizeLatestRequestProvider(value string) string {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	if value == "grok" || value == "x-ai" {
		return "xai"
	}
	return value
}

func snapshotLatestRequestPredicates(authFileSnapshot, authIndex string) []latestRequestPredicate {
	return latestRequestPredicates(
		`e.auth_file_snapshot collate nocase = ?`,
		[]any{authFileSnapshot},
		authIndex,
	)
}

func legacyLatestRequestPredicates(authFileSnapshot, authIndex string) []latestRequestPredicate {
	filePredicates := []latestRequestPredicate{
		{sql: `e.auth_file_snapshot is null and e.source collate nocase = ?`, args: []any{authFileSnapshot}},
		{sql: `e.auth_file_snapshot = '' and e.source collate nocase = ?`, args: []any{authFileSnapshot}},
	}
	predicates := make([]latestRequestPredicate, 0, 4)
	for _, filePredicate := range filePredicates {
		predicates = append(predicates, latestRequestPredicates(filePredicate.sql, filePredicate.args, authIndex)...)
	}
	return predicates
}

func latestRequestPredicates(baseSQL string, baseArgs []any, authIndex string) []latestRequestPredicate {
	if authIndex != "" {
		return []latestRequestPredicate{{
			sql:  baseSQL + ` and e.auth_index collate nocase = ?`,
			args: append(append([]any{}, baseArgs...), authIndex),
		}}
	}
	return []latestRequestPredicate{
		{sql: baseSQL + ` and e.auth_index is null`, args: append([]any{}, baseArgs...)},
		{sql: baseSQL + ` and e.auth_index collate nocase = ''`, args: append([]any{}, baseArgs...)},
	}
}

func (r *repository) recentAccountRequestsByPredicates(
	ctx context.Context,
	requestIndex int,
	limit int,
	predicates []latestRequestPredicate,
) ([]rankedAccountRequest, error) {
	parts := make([][]rankedAccountRequest, 0, len(predicates))
	for _, predicate := range predicates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, err := r.recentAccountRequestsByPredicate(ctx, requestIndex, limit, predicate)
		if err != nil {
			return nil, err
		}
		parts = append(parts, rows)
	}
	merged := mergeRankedAccountRequests(limit, parts...)
	return merged, nil
}

func (r *repository) recentAccountRequestsByPredicate(
	ctx context.Context,
	requestIndex int,
	limit int,
	predicate latestRequestPredicate,
) ([]rankedAccountRequest, error) {
	args := append(append([]any{}, predicate.args...), limit)
	rows, err := r.db.QueryContext(ctx, `select
	e.id,
	e.timestamp_ms,
	e.failed,
	e.fail_status_code,
	coalesce(e.fail_summary, ''),
	coalesce(e.header_error_kind, ''),
	coalesce(e.header_error_code, ''),
	coalesce(e.header_trace_id, '')
from usage_events e
where `+predicate.sql+`
order by e.timestamp_ms desc, e.id desc
limit ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	requests := make([]rankedAccountRequest, 0, limit)
	for rows.Next() {
		var request rankedAccountRequest
		var failed int
		if err := rows.Scan(
			&request.id,
			&request.TimestampMS,
			&failed,
			&request.FailStatusCode,
			&request.FailSummary,
			&request.HeaderErrorKind,
			&request.HeaderErrorCode,
			&request.HeaderTraceID,
		); err != nil {
			return nil, err
		}
		request.RequestIndex = requestIndex
		request.Failed = failed != 0
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func (r *repository) recentAccountRequestsBatched(
	ctx context.Context,
	targets []LatestAccountRequestQuery,
	limit int,
) ([]LatestAccountRequest, error) {
	values := make([]string, 0, len(targets))
	args := make([]any, 0, len(targets)*8+1)
	for _, target := range targets {
		authFileSnapshot := strings.TrimSpace(target.AuthFileSnapshot)
		if authFileSnapshot == "" {
			continue
		}
		provider := normalizeLatestRequestProvider(target.Provider)
		workspaceID, member := "", ""
		identityMode := 0
		if provider == "codex" {
			var identityOK, identityConflict bool
			workspaceID, member, identityOK, identityConflict = resolveLatestRequestCodexIdentity(target)
			switch {
			case identityConflict:
				identityMode = 2
			case identityOK:
				identityMode = 1
			}
		}
		values = append(values, "(?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(
			args,
			target.RequestIndex,
			authFileSnapshot,
			strings.TrimSpace(target.AuthIndex),
			provider,
			workspaceID,
			member,
			strings.TrimSpace(target.AuthProjectIDSnapshot),
			identityMode,
		)
	}
	if len(values) == 0 {
		return []LatestAccountRequest{}, nil
	}
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, `with credential_targets(
	request_index, auth_file_snapshot, auth_index, provider, workspace_id, member, project_id, identity_mode
) as (
	values `+strings.Join(values, ",")+`
), snapshot_candidates as (
	select
		t.request_index,
		e.id,
		e.timestamp_ms,
		e.failed,
		e.fail_status_code,
		coalesce(e.fail_summary, '') as fail_summary,
		coalesce(e.header_error_kind, '') as header_error_kind,
		coalesce(e.header_error_code, '') as header_error_code,
		coalesce(e.header_trace_id, '') as header_trace_id
	from credential_targets t
	join usage_events e
		on e.auth_file_snapshot collate nocase = t.auth_file_snapshot
		and coalesce(e.auth_index, '') collate nocase = t.auth_index
		and `+latestRequestBatchedIdentityPredicate("t", "e")+`
), legacy_source_candidates as (
	select
		t.request_index,
		e.id,
		e.timestamp_ms,
		e.failed,
		e.fail_status_code,
		coalesce(e.fail_summary, '') as fail_summary,
		coalesce(e.header_error_kind, '') as header_error_kind,
		coalesce(e.header_error_code, '') as header_error_code,
		coalesce(e.header_trace_id, '') as header_trace_id
	from credential_targets t
	join usage_events e
		on coalesce(e.auth_file_snapshot, '') = ''
		and e.source collate nocase = t.auth_file_snapshot
		and coalesce(e.auth_index, '') collate nocase = t.auth_index
		and `+latestRequestBatchedIdentityPredicate("t", "e")+`
), candidates as (
	select * from snapshot_candidates
	union all
	select * from legacy_source_candidates
), ranked as (
	select
		request_index,
		timestamp_ms,
		failed,
		fail_status_code,
		fail_summary,
		header_error_kind,
		header_error_code,
		header_trace_id,
		row_number() over (
			partition by request_index
			order by timestamp_ms desc, id desc
		) as row_number
	from candidates
)
select
	request_index,
	timestamp_ms,
	failed,
	fail_status_code,
	fail_summary,
	header_error_kind,
	header_error_code,
	header_trace_id
from ranked
where row_number <= ?
order by request_index, row_number`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	requests := make([]LatestAccountRequest, 0, len(values)*limit)
	for rows.Next() {
		var request LatestAccountRequest
		var failed int
		if err := rows.Scan(
			&request.RequestIndex,
			&request.TimestampMS,
			&failed,
			&request.FailStatusCode,
			&request.FailSummary,
			&request.HeaderErrorKind,
			&request.HeaderErrorCode,
			&request.HeaderTraceID,
		); err != nil {
			return nil, err
		}
		request.Failed = failed != 0
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func mergeRecentAccountRequests(limit int, parts ...[]rankedAccountRequest) []LatestAccountRequest {
	merged := mergeRankedAccountRequests(limit, parts...)
	if len(merged) == 0 {
		return nil
	}
	requests := make([]LatestAccountRequest, len(merged))
	for i, request := range merged {
		requests[i] = request.LatestAccountRequest
	}
	return requests
}

func mergeRankedAccountRequests(limit int, parts ...[]rankedAccountRequest) []rankedAccountRequest {
	total := 0
	for _, part := range parts {
		total += len(part)
	}
	if total == 0 {
		return nil
	}
	merged := make([]rankedAccountRequest, 0, total)
	for _, part := range parts {
		merged = append(merged, part...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].TimestampMS != merged[j].TimestampMS {
			return merged[i].TimestampMS > merged[j].TimestampMS
		}
		return merged[i].id > merged[j].id
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}
