package usageevent

import (
	"context"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const defaultResponseMetadataBackfillBatch = 1000

type responseMetadataBackfillRow struct {
	ID                   int64
	TimestampMS          int64
	RawJSON              string
	ResponseMetadataJSON string
}

type responseMetadataBackfillUpdate struct {
	ID                   int64
	PreviousMetadataJSON string
	Derived              usage.ResponseHeaderDerived
}

const responseMetadataBackfillSelect = `with candidates as (
	select
		id,
		timestamp_ms,
		coalesce(raw_json, '') as raw_json,
		coalesce(response_metadata_json, '') as response_metadata_json,
		response_metadata_json is null as response_metadata_missing,
		case when json_valid(response_metadata_json) then response_metadata_json else '{}' end as metadata_json,
		replace(lower(trim(coalesce(provider, ''))), '_', '-') as provider_identity,
		replace(lower(trim(coalesce(auth_provider_snapshot, ''))), '_', '-') as auth_provider_identity,
		replace(replace(replace(lower(trim(coalesce(executor_type, ''))), '-', ''), '_', ''), ' ', '') as executor_identity
	from usage_events
	where id > ? and raw_json is not null
)
select id, timestamp_ms, raw_json, response_metadata_json
from candidates
where
	(response_metadata_missing and (raw_json like '%response_headers%' or raw_json like '%responseHeaders%'))
	or (
		raw_json like '%subscription:free-usage-exhausted%'
		and (
			provider_identity in ('xai', 'x-ai')
			or auth_provider_identity in ('xai', 'x-ai')
			or (
				executor_identity = 'xaiexecutor'
				and provider_identity in ('', 'grok')
				and auth_provider_identity in ('', 'grok')
			)
		)
		and (
			json_type(metadata_json, '$.provider_usage') is null
			or json_type(metadata_json, '$.provider_usage.provider') is null
			or json_type(metadata_json, '$.provider_usage.kind') is null
			or json_type(metadata_json, '$.provider_usage.state') is null
			or json_type(metadata_json, '$.provider_usage.code') is null
			or json_type(metadata_json, '$.provider_usage.unit') is null
			or json_type(metadata_json, '$.provider_usage.window_kind') is null
			or json_type(metadata_json, '$.provider_usage.observed_at_ms') is null
			or json_type(metadata_json, '$.provider_usage.recover_at_ms') is null
			or json_type(metadata_json, '$.provider_usage.source') is null
			or (
				raw_json like '%actual/limit%'
				and (
					json_type(metadata_json, '$.provider_usage.actual') is null
					or json_type(metadata_json, '$.provider_usage.limit') is null
					or json_type(metadata_json, '$.provider_usage.remaining') is null
					or json_type(metadata_json, '$.provider_usage.overage') is null
				)
			)
		)
	)
	or (raw_json like '%X-Ratelimit-Limit-Requests%' and json_type(metadata_json, '$.rate_limit.requests.limit') is null)
	or (raw_json like '%X-Ratelimit-Remaining-Requests%' and json_type(metadata_json, '$.rate_limit.requests.remaining') is null)
	or (raw_json like '%X-Ratelimit-Limit-Tokens%' and json_type(metadata_json, '$.rate_limit.tokens.limit') is null)
	or (raw_json like '%X-Ratelimit-Remaining-Tokens%' and json_type(metadata_json, '$.rate_limit.tokens.remaining') is null)
	or (raw_json like '%X-Data-Retention%' and json_type(metadata_json, '$.data_policy.retention_mode') is null)
	or (
		(raw_json like '%X-Zero-Retention%' or raw_json like '%X-Zero-Data-Retention%')
		and json_type(metadata_json, '$.data_policy.zero_retention') is null
	)
order by id
limit ?`

func (r *repository) BackfillResponseMetadata(ctx context.Context, batchLimit int) (int, error) {
	if batchLimit <= 0 {
		batchLimit = defaultResponseMetadataBackfillBatch
	}

	updates := make([]responseMetadataBackfillUpdate, 0, batchLimit)
	lastID := int64(0)
	for len(updates) < batchLimit {
		items, err := r.responseMetadataBackfillPage(ctx, lastID, batchLimit)
		if err != nil {
			return 0, err
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			lastID = item.ID
			metadata := mergeResponseMetadata(
				item.ResponseMetadataJSON,
				usage.ParseResponseHeaderMetadataFromRawJSON(item.RawJSON, time.UnixMilli(item.TimestampMS)),
			)
			derived := usage.DeriveResponseHeaderMetadata(metadata)
			if derived.MetadataJSON == "" {
				if strings.TrimSpace(item.ResponseMetadataJSON) == "" {
					// Preserve the old backfill's processed-row behavior. A valid empty
					// object prevents unsupported-only header rows from being rescanned
					// on every startup while explicit future feature clauses can still
					// select the row when a new parser is added.
					updates = append(updates, responseMetadataBackfillUpdate{
						ID:                   item.ID,
						PreviousMetadataJSON: item.ResponseMetadataJSON,
						Derived:              usage.ResponseHeaderDerived{MetadataJSON: "{}"},
					})
					if len(updates) == batchLimit {
						break
					}
				}
				continue
			}
			if responseMetadataJSONEqual(item.ResponseMetadataJSON, derived.MetadataJSON) {
				continue
			}
			updates = append(updates, responseMetadataBackfillUpdate{
				ID:                   item.ID,
				PreviousMetadataJSON: item.ResponseMetadataJSON,
				Derived:              derived,
			})
			if len(updates) == batchLimit {
				break
			}
		}
		if len(items) < batchLimit {
			break
		}
	}
	if len(updates) == 0 {
		return 0, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	stmt, err := tx.PrepareContext(ctx, `update usage_events set
	response_metadata_json = ?,
	header_quota_recover_at_ms = ?,
	header_quota_used_percent = ?,
	header_quota_plan_type = ?,
	header_error_kind = ?,
	header_error_code = ?,
	header_trace_id = ?
	where id = ? and coalesce(response_metadata_json, '') = ?`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	updated := 0
	for _, update := range updates {
		derived := update.Derived
		res, err := stmt.ExecContext(
			ctx,
			derived.MetadataJSON,
			nullPositiveInt64(derived.QuotaRecoverAtMS),
			nullFloat(derived.QuotaUsedPercent),
			nullString(derived.QuotaPlanType),
			nullString(derived.ErrorKind),
			nullString(derived.ErrorCode),
			nullString(derived.TraceID),
			update.ID,
			update.PreviousMetadataJSON,
		)
		if err != nil {
			return updated, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return updated, err
		}
		updated += int(affected)
	}
	if err := tx.Commit(); err != nil {
		return updated, err
	}
	return updated, nil
}

func (r *repository) responseMetadataBackfillPage(ctx context.Context, afterID int64, limit int) ([]responseMetadataBackfillRow, error) {
	rows, err := r.db.QueryContext(ctx, responseMetadataBackfillSelect, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]responseMetadataBackfillRow, 0, limit)
	for rows.Next() {
		var item responseMetadataBackfillRow
		if err := rows.Scan(&item.ID, &item.TimestampMS, &item.RawJSON, &item.ResponseMetadataJSON); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func responseMetadataJSONEqual(existingJSON string, candidateJSON string) bool {
	existing := usage.DeriveResponseHeaderMetadata(usage.ResponseHeaderMetadataFromJSON(existingJSON)).MetadataJSON
	return existing != "" && existing == candidateJSON
}

func mergeResponseMetadata(existingJSON string, parsed *usage.ResponseHeaderMetadata) *usage.ResponseHeaderMetadata {
	existing := usage.ResponseHeaderMetadataFromJSON(existingJSON)
	return usage.MergeResponseHeaderMetadata(existing, parsed)
}
