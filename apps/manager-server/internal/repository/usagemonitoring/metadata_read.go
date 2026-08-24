package usagemonitoring

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

type selectorRow struct {
	Model             string
	APIKeyHash        string
	Provider          string
	AuthFileSnapshot  string
	AccountSnapshot   string
	AuthLabelSnapshot string
	AuthIndex         string
	Source            string
	SourceHash        string
}

type selectorKey struct {
	model             string
	apiKeyHash        string
	provider          string
	authFileSnapshot  string
	accountSnapshot   string
	authLabelSnapshot string
	authIndex         string
	sourceHash        string
}

type headerRow struct {
	SnapshotKey string
	Item        HeaderSnapshot
}

func (r *repository) LoadFilterOptions(ctx context.Context, filter AnalyticsFilter) (FilterOptionValues, State, bool, error) {
	if !SupportsEventProjectionFilter(filter) {
		return FilterOptionValues{}, State{}, false, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return FilterOptionValues{}, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, available, projectionComplete, err := projectionReadState(ctx, tx)
	if err != nil || !available {
		return FilterOptionValues{}, state, available, err
	}

	source, args := filteredEventSourceSQL(
		filter,
		state.CoverageEventID,
		`coalesce(nullif(p.auth_provider_snapshot, ''), nullif(p.provider, ''), '') as provider,
		p.auth_file_snapshot, p.auth_project_id_snapshot, p.executor_type,
		p.header_error_kind, p.header_error_code, p.header_quota_plan_type,
		p.header_trace_id`,
		`coalesce(nullif(e.auth_provider_snapshot, ''), nullif(e.provider, ''), ''),
		coalesce(e.auth_file_snapshot, ''), coalesce(e.auth_project_id_snapshot, ''),
		coalesce(e.executor_type, ''), coalesce(e.header_error_kind, ''),
		coalesce(e.header_error_code, ''), coalesce(e.header_quota_plan_type, ''),
		coalesce(e.header_trace_id, '')`,
		eventSourceOptions{ProjectionComplete: projectionComplete},
	)
	var encoded [8]string
	err = tx.QueryRowContext(ctx, `with filtered_events as (`+source+`)
		select
			json_group_array(distinct provider) filter (where provider <> ''),
			json_group_array(distinct auth_file_snapshot) filter (where auth_file_snapshot <> ''),
			json_group_array(distinct auth_project_id_snapshot) filter (where auth_project_id_snapshot <> ''),
			json_group_array(distinct executor_type) filter (where executor_type <> ''),
			json_group_array(distinct header_error_kind) filter (where header_error_kind <> ''),
			json_group_array(distinct header_error_code) filter (where header_error_code <> ''),
			json_group_array(distinct header_quota_plan_type) filter (where header_quota_plan_type <> ''),
			json_group_array(distinct header_trace_id) filter (where header_trace_id <> '')
		from filtered_events`, args...).Scan(
		&encoded[0],
		&encoded[1],
		&encoded[2],
		&encoded[3],
		&encoded[4],
		&encoded[5],
		&encoded[6],
		&encoded[7],
	)
	if err != nil {
		return FilterOptionValues{}, state, false, err
	}
	decoded := make([][]string, len(encoded))
	for index, value := range encoded {
		decoded[index], err = decodeSortedFilterOptionValues(value)
		if err != nil {
			return FilterOptionValues{}, state, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return FilterOptionValues{}, state, false, err
	}
	return FilterOptionValues{
		Providers:        decoded[0],
		AuthFiles:        decoded[1],
		ProjectIDs:       decoded[2],
		RequestTypes:     decoded[3],
		HeaderErrorKinds: decoded[4],
		HeaderErrorCodes: decoded[5],
		HeaderQuotaPlans: decoded[6],
		HeaderTraceIDs:   decoded[7],
	}, state, true, nil
}

func decodeSortedFilterOptionValues(encoded string) ([]string, error) {
	values := make([]string, 0)
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, fmt.Errorf("decode usage monitoring filter options: %w", err)
	}
	sort.Strings(values)
	return values, nil
}

func (r *repository) LoadFilterSelectors(ctx context.Context, filter AnalyticsFilter) (FilterSelectorValues, State, bool, error) {
	if !SupportsEventProjectionFilter(filter) {
		return FilterSelectorValues{}, State{}, false, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return FilterSelectorValues{}, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	projectionState, projectionAvailable, projectionComplete, err := projectionReadState(ctx, tx)
	if err != nil || !projectionAvailable {
		return FilterSelectorValues{}, projectionState, projectionAvailable, err
	}
	state, err := stateQuery(ctx, tx, MetadataRollupName)
	if err != nil {
		return FilterSelectorValues{}, projectionState, false, err
	}

	grouped := map[selectorKey]*selectorRow{}
	if state.SchemaVersion == SchemaVersion && SupportsSelectorFilter(filter) {
		fullStartMS := ceilDayMS(filter.FromMS)
		fullEndMS := floorDayMS(filter.ToMS)
		if fullStartMS >= fullEndMS {
			err = mergeProjectedSelectorRows(ctx, tx, projectionState.CoverageEventID, projectionComplete, filter, 0, false, grouped)
		} else {
			if err = mergeStoredSelectorRows(ctx, tx, fullStartMS, fullEndMS, grouped); err == nil {
				tailFilter := filter
				tailFilter.FromMS = fullStartMS
				tailFilter.ToMS = fullEndMS
				err = mergeProjectedSelectorRows(ctx, tx, projectionState.CoverageEventID, projectionComplete, tailFilter, state.CoverageEventID, true, grouped)
			}
			if err == nil && filter.FromMS < fullStartMS {
				edgeFilter := filter
				edgeFilter.ToMS = fullStartMS
				err = mergeProjectedSelectorRows(ctx, tx, projectionState.CoverageEventID, projectionComplete, edgeFilter, 0, false, grouped)
			}
			if err == nil && fullEndMS < filter.ToMS {
				edgeFilter := filter
				edgeFilter.FromMS = fullEndMS
				err = mergeProjectedSelectorRows(ctx, tx, projectionState.CoverageEventID, projectionComplete, edgeFilter, 0, false, grouped)
			}
		}
	} else {
		err = mergeProjectedSelectorRows(ctx, tx, projectionState.CoverageEventID, projectionComplete, filter, 0, false, grouped)
	}
	if err != nil {
		return FilterSelectorValues{}, projectionState, false, err
	}
	if err := tx.Commit(); err != nil {
		return FilterSelectorValues{}, projectionState, false, err
	}
	return buildFilterSelectorValues(grouped), projectionState, true, nil
}

func mergeStoredSelectorRows(
	ctx context.Context,
	tx *sql.Tx,
	fromMS, toMS int64,
	grouped map[selectorKey]*selectorRow,
) error {
	rows, err := tx.QueryContext(ctx, `select
		model, api_key_hash, provider, auth_file_snapshot, account_snapshot,
		auth_label_snapshot, auth_index, max(source), source_hash
	from usage_monitoring_selector_daily_rollups_v1
	where model_format_revision = ? and bucket_ms >= ? and bucket_ms < ?
	group by model, api_key_hash, provider, auth_file_snapshot,
		account_snapshot, auth_label_snapshot, auth_index, source_hash`, usageidentity.ModelFormatVersion, fromMS, toMS)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanSelectorRows(rows, grouped)
}

func mergeProjectedSelectorRows(
	ctx context.Context,
	tx *sql.Tx,
	projectionCoverageEventID int64,
	projectionComplete bool,
	filter AnalyticsFilter,
	afterID int64,
	useAfterID bool,
	grouped map[selectorKey]*selectorRow,
) error {
	if filter.FromMS >= filter.ToMS {
		return nil
	}
	source, args := filteredEventSourceSQL(
		filter,
		projectionCoverageEventID,
		`p.analytics_model as model, p.api_key_hash,
		coalesce(nullif(p.auth_provider_snapshot, ''), nullif(p.provider, ''), '') as auth_provider_snapshot,
		p.auth_file_snapshot, p.account_snapshot, p.auth_label_snapshot,
		p.auth_index, p.source, p.source_hash`,
		usageidentity.SQLRequestAnalyticsModelExpression("e.model", "e.requested_model")+` as model, coalesce(e.api_key_hash, ''),
		coalesce(nullif(e.auth_provider_snapshot, ''), nullif(e.provider, ''), ''),
		coalesce(e.auth_file_snapshot, ''), coalesce(e.account_snapshot, ''),
		coalesce(e.auth_label_snapshot, ''), coalesce(e.auth_index, ''),
		coalesce(e.source, ''), coalesce(e.source_hash, '')`,
		eventSourceOptions{
			AfterID:            afterID,
			UseAfter:           useAfterID,
			ProjectionComplete: projectionComplete,
		},
	)
	rows, err := tx.QueryContext(ctx, `with filtered_events as (`+source+`)
		select model, api_key_hash, auth_provider_snapshot, auth_file_snapshot,
			account_snapshot, auth_label_snapshot, auth_index, max(source), source_hash
		from filtered_events
		group by 1, 2, 3, 4, 5, 6, 7, 9`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanSelectorRows(rows, grouped)
}

func scanSelectorRows(rows *sql.Rows, grouped map[selectorKey]*selectorRow) error {
	for rows.Next() {
		var row selectorRow
		if err := rows.Scan(
			&row.Model,
			&row.APIKeyHash,
			&row.Provider,
			&row.AuthFileSnapshot,
			&row.AccountSnapshot,
			&row.AuthLabelSnapshot,
			&row.AuthIndex,
			&row.Source,
			&row.SourceHash,
		); err != nil {
			return err
		}
		key := selectorKey{
			model:             row.Model,
			apiKeyHash:        row.APIKeyHash,
			provider:          row.Provider,
			authFileSnapshot:  row.AuthFileSnapshot,
			accountSnapshot:   row.AccountSnapshot,
			authLabelSnapshot: row.AuthLabelSnapshot,
			authIndex:         row.AuthIndex,
			sourceHash:        row.SourceHash,
		}
		entry := grouped[key]
		if entry == nil {
			copy := row
			grouped[key] = &copy
			continue
		}
		if row.Source > entry.Source {
			entry.Source = row.Source
		}
	}
	return rows.Err()
}

func buildFilterSelectorValues(grouped map[selectorKey]*selectorRow) FilterSelectorValues {
	models := map[string]struct{}{}
	providers := map[string]struct{}{}
	authFiles := map[string]struct{}{}
	apiKeyHashes := map[string]struct{}{}
	accounts := map[string]struct{}{}
	accountSelectors := map[string]*AccountSelectorValue{}
	apiKeySelectors := map[string]*APIKeySelectorValue{}

	for _, row := range grouped {
		addNonEmptyValue(models, row.Model)
		addNonEmptyValue(providers, row.Provider)
		addNonEmptyValue(authFiles, row.AuthFileSnapshot)
		hash := strings.ToLower(strings.TrimSpace(row.APIKeyHash))
		addNonEmptyValue(apiKeyHashes, hash)

		apiKey := strings.Join([]string{row.APIKeyHash, row.Provider, row.AuthIndex, row.SourceHash}, "\x00")
		apiSelector := apiKeySelectors[apiKey]
		if apiSelector == nil {
			apiSelector = &APIKeySelectorValue{
				APIKeyHash:           row.APIKeyHash,
				AuthProviderSnapshot: row.Provider,
				AuthIndex:            row.AuthIndex,
				Source:               row.Source,
				SourceHash:           row.SourceHash,
			}
			apiKeySelectors[apiKey] = apiSelector
		} else if row.Source > apiSelector.Source {
			apiSelector.Source = row.Source
		}

		if row.AccountSnapshot == "" && row.AuthLabelSnapshot == "" && row.AuthIndex == "" && row.Source == "" && row.SourceHash == "" {
			continue
		}
		addNonEmptyValue(accounts, row.AccountSnapshot)
		accountKey := strings.Join([]string{
			row.AccountSnapshot,
			row.AuthLabelSnapshot,
			row.Provider,
			row.AuthIndex,
			row.SourceHash,
		}, "\x00")
		accountSelector := accountSelectors[accountKey]
		if accountSelector == nil {
			accountSelector = &AccountSelectorValue{
				AccountSnapshot:      row.AccountSnapshot,
				AuthLabelSnapshot:    row.AuthLabelSnapshot,
				AuthProviderSnapshot: row.Provider,
				AuthIndex:            row.AuthIndex,
				Source:               row.Source,
				SourceHash:           row.SourceHash,
			}
			accountSelectors[accountKey] = accountSelector
		} else if row.Source > accountSelector.Source {
			accountSelector.Source = row.Source
		}
	}

	accountValues := make([]AccountSelectorValue, 0, len(accountSelectors))
	for _, value := range accountSelectors {
		accountValues = append(accountValues, *value)
	}
	sort.Slice(accountValues, func(i, j int) bool {
		left, right := accountValues[i], accountValues[j]
		return strings.Join([]string{left.AccountSnapshot, left.AuthLabelSnapshot, left.Source, left.AuthIndex, left.SourceHash}, "\x00") <
			strings.Join([]string{right.AccountSnapshot, right.AuthLabelSnapshot, right.Source, right.AuthIndex, right.SourceHash}, "\x00")
	})
	apiKeyValues := make([]APIKeySelectorValue, 0, len(apiKeySelectors))
	for _, value := range apiKeySelectors {
		apiKeyValues = append(apiKeyValues, *value)
	}
	sort.Slice(apiKeyValues, func(i, j int) bool {
		left, right := apiKeyValues[i], apiKeyValues[j]
		return strings.Join([]string{left.APIKeyHash, left.SourceHash, left.AuthIndex, left.Source, left.AuthProviderSnapshot}, "\x00") <
			strings.Join([]string{right.APIKeyHash, right.SourceHash, right.AuthIndex, right.Source, right.AuthProviderSnapshot}, "\x00")
	})

	return FilterSelectorValues{
		Models:           sortedSet(models),
		APIKeyHashes:     sortedSet(apiKeyHashes),
		Providers:        sortedSet(providers),
		AuthFiles:        sortedSet(authFiles),
		Accounts:         sortedSet(accounts),
		AccountSelectors: accountValues,
		APIKeySelectors:  apiKeyValues,
	}
}

func addNonEmptyValue(values map[string]struct{}, value string) {
	if strings.TrimSpace(value) != "" {
		values[value] = struct{}{}
	}
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (r *repository) LoadHeaderSnapshots(ctx context.Context, sinceMS int64, limit int) ([]HeaderSnapshot, State, bool, error) {
	if limit <= 0 {
		return []HeaderSnapshot{}, State{}, true, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := stateQuery(ctx, tx, MetadataRollupName)
	if err != nil {
		return nil, State{}, false, err
	}
	if state.SchemaVersion != SchemaVersion {
		return nil, state, false, nil
	}

	grouped := map[string]headerRow{}
	if err := mergeStoredHeaderRows(ctx, tx, sinceMS, limit, grouped); err != nil {
		return nil, state, false, err
	}
	if err := mergeRawHeaderRows(ctx, tx, sinceMS, state.CoverageEventID, limit, grouped); err != nil {
		return nil, state, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, state, false, err
	}
	return sortedHeaderSnapshots(grouped, limit), state, true, nil
}

func mergeStoredHeaderRows(ctx context.Context, tx *sql.Tx, sinceMS int64, limit int, grouped map[string]headerRow) error {
	rows, err := tx.QueryContext(ctx, `select
		stored.snapshot_key, stored.event_id, stored.event_hash, stored.timestamp_ms,
		coalesce(usage_event.model, ''),
		`+usageidentity.SQLRequestAnalyticsModelExpression("usage_event.model", "usage_event.requested_model")+`,
		coalesce(usage_event.requested_model, ''), coalesce(usage_event.resolved_model, ''),
		stored.auth_file_snapshot,
		stored.auth_index, stored.account_snapshot, stored.auth_label_snapshot,
		stored.auth_provider_snapshot, stored.auth_project_id_snapshot, stored.source, stored.source_hash,
		stored.response_metadata_json, stored.header_quota_recover_at_ms,
		stored.header_quota_used_percent, stored.header_quota_plan_type, stored.header_error_kind,
		stored.header_error_code, stored.header_trace_id
	from usage_monitoring_header_latest_v1 stored
	left join usage_events usage_event on usage_event.id = stored.event_id
	where stored.timestamp_ms >= ?
	order by stored.timestamp_ms desc, stored.event_id desc
	limit ?`, sinceMS, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanHeaderRows(rows, grouped)
}

func mergeRawHeaderRows(ctx context.Context, tx *sql.Tx, sinceMS, afterID int64, limit int, grouped map[string]headerRow) error {
	query := fmt.Sprintf(`with candidates as (
		select
			id,
			event_hash,
			timestamp_ms,
			coalesce(model, '') as model,
			`+usageidentity.SQLRequestAnalyticsModelExpression("model", "requested_model")+` as analytics_model,
			coalesce(requested_model, '') as requested_model,
			coalesce(resolved_model, '') as resolved_model,
			coalesce(auth_file_snapshot, '') as auth_file_snapshot,
			coalesce(auth_index, '') as auth_index,
			coalesce(account_snapshot, '') as account_snapshot,
			coalesce(auth_label_snapshot, '') as auth_label_snapshot,
			coalesce(nullif(auth_provider_snapshot, ''), provider, '') as auth_provider_snapshot,
			coalesce(auth_project_id_snapshot, '') as auth_project_id_snapshot,
			coalesce(source, '') as source,
			coalesce(source_hash, '') as source_hash,
			coalesce(response_metadata_json, '') as response_metadata_json,
			header_quota_recover_at_ms,
			header_quota_used_percent,
			coalesce(header_quota_plan_type, '') as header_quota_plan_type,
			coalesce(header_error_kind, '') as header_error_kind,
			coalesce(header_error_code, '') as header_error_code,
			coalesce(header_trace_id, '') as header_trace_id,
			%s as snapshot_key
		from usage_events
		where id > ? and timestamp_ms >= ?
		and (
			coalesce(response_metadata_json, '') <> ''
			or header_quota_recover_at_ms is not null
			or header_quota_used_percent is not null
			or coalesce(header_quota_plan_type, '') <> ''
			or coalesce(header_error_kind, '') <> ''
			or coalesce(header_error_code, '') <> ''
			or coalesce(header_trace_id, '') <> ''
		)
		and (
			coalesce(auth_file_snapshot, '') <> ''
			or coalesce(auth_index, '') <> ''
			or coalesce(account_snapshot, '') <> ''
			or coalesce(source_hash, '') <> ''
		)
	), ranked as (
		select *, row_number() over (
			partition by snapshot_key order by timestamp_ms desc, id desc
		) as rn
		from candidates
	)
	select
		snapshot_key, id, event_hash, timestamp_ms,
		model, analytics_model, requested_model, resolved_model,
		auth_file_snapshot,
		auth_index, account_snapshot, auth_label_snapshot,
		auth_provider_snapshot, auth_project_id_snapshot, source, source_hash,
		response_metadata_json, header_quota_recover_at_ms,
		header_quota_used_percent, header_quota_plan_type, header_error_kind,
		header_error_code, header_trace_id
	from ranked where rn = 1
	order by timestamp_ms desc, id desc
		limit ?`, usageprojection.SnapshotKeyExpression(""))
	rows, err := tx.QueryContext(ctx, query, afterID, sinceMS, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanHeaderRows(rows, grouped)
}

func scanHeaderRows(rows *sql.Rows, grouped map[string]headerRow) error {
	for rows.Next() {
		var row headerRow
		var responseMetadataJSON string
		if err := rows.Scan(
			&row.SnapshotKey,
			&row.Item.ID,
			&row.Item.EventHash,
			&row.Item.TimestampMS,
			&row.Item.Model,
			&row.Item.AnalyticsModel,
			&row.Item.RequestedModel,
			&row.Item.ResolvedModel,
			&row.Item.AuthFileSnapshot,
			&row.Item.AuthIndex,
			&row.Item.AccountSnapshot,
			&row.Item.AuthLabelSnapshot,
			&row.Item.AuthProviderSnapshot,
			&row.Item.AuthProjectIDSnapshot,
			&row.Item.Source,
			&row.Item.SourceHash,
			&responseMetadataJSON,
			&row.Item.HeaderQuotaRecoverAtMS,
			&row.Item.HeaderQuotaUsedPercent,
			&row.Item.HeaderQuotaPlanType,
			&row.Item.HeaderErrorKind,
			&row.Item.HeaderErrorCode,
			&row.Item.HeaderTraceID,
		); err != nil {
			return err
		}
		row.Item.ResponseMetadata = usage.ResponseHeaderMetadataFromJSON(responseMetadataJSON)
		current, ok := grouped[row.SnapshotKey]
		if !ok || headerSnapshotNewer(row.Item, current.Item) {
			grouped[row.SnapshotKey] = row
		}
	}
	return rows.Err()
}

func headerSnapshotNewer(left, right HeaderSnapshot) bool {
	return left.TimestampMS > right.TimestampMS ||
		(left.TimestampMS == right.TimestampMS && left.ID > right.ID)
}

func sortedHeaderSnapshots(grouped map[string]headerRow, limit int) []HeaderSnapshot {
	result := make([]HeaderSnapshot, 0, len(grouped))
	for _, row := range grouped {
		result = append(result, row.Item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TimestampMS != result[j].TimestampMS {
			return result[i].TimestampMS > result[j].TimestampMS
		}
		return result[i].ID > result[j].ID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}
