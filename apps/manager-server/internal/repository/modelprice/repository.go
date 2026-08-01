package modelprice

import (
	"context"
	"database/sql"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

type Repository interface {
	LoadAll(ctx context.Context) (map[string]model.ModelPrice, error)
	LoadAllTx(ctx context.Context, tx *sql.Tx) (map[string]model.ModelPrice, error)
	ReplaceAll(ctx context.Context, prices map[string]model.ModelPrice) error
	UpsertSynced(ctx context.Context, prices map[string]model.ModelPrice) (model.ModelPriceSyncResult, error)
}

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

func (r *repository) LoadAll(ctx context.Context) (map[string]model.ModelPrice, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	prices, err := r.LoadAllTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return prices, nil
}

func (r *repository) LoadAllTx(ctx context.Context, tx *sql.Tx) (map[string]model.ModelPrice, error) {
	rows, err := tx.QueryContext(ctx, `select
		model, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m,
		prompt_configured, completion_configured, cache_read_configured, cache_creation_configured, source, source_model_id, raw_json,
		updated_at_ms, synced_at_ms
		from model_prices order by model`)
	if err != nil {
		return nil, err
	}

	prices := map[string]model.ModelPrice{}
	for rows.Next() {
		var modelID string
		var price model.ModelPrice
		var source, sourceModelID, rawJSON sql.NullString
		var syncedAt sql.NullInt64
		var promptConfigured, completionConfigured, cacheReadConfigured, cacheCreationConfigured int
		if err := rows.Scan(
			&modelID,
			&price.Prompt,
			&price.Completion,
			&price.Cache,
			&price.CacheRead,
			&price.CacheCreation,
			&promptConfigured,
			&completionConfigured,
			&cacheReadConfigured,
			&cacheCreationConfigured,
			&source,
			&sourceModelID,
			&rawJSON,
			&price.UpdatedAtMS,
			&syncedAt,
		); err != nil {
			return nil, err
		}
		price.Source = source.String
		price.PromptConfigured = promptConfigured != 0
		price.CompletionConfigured = completionConfigured != 0
		price.CacheReadConfigured = cacheReadConfigured != 0
		price.CacheCreationConfigured = cacheCreationConfigured != 0
		price.SourceModelID = sourceModelID.String
		price.RawJSON = rawJSON.String
		if syncedAt.Valid {
			value := syncedAt.Int64
			price.SyncedAtMS = &value
		}
		prices[modelID] = price
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	tierRows, err := tx.QueryContext(ctx, `select
		model, threshold_tokens, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m,
		prompt_configured, completion_configured, cache_configured, cache_read_configured, cache_creation_configured
		from model_price_context_tiers order by model, threshold_tokens`)
	if err != nil {
		return nil, err
	}
	for tierRows.Next() {
		var modelID string
		var tier model.ModelPriceContextTier
		var promptConfigured, completionConfigured, cacheConfigured, cacheReadConfigured, cacheCreationConfigured int
		if err := tierRows.Scan(
			&modelID,
			&tier.ThresholdTokens,
			&tier.Prompt,
			&tier.Completion,
			&tier.Cache,
			&tier.CacheRead,
			&tier.CacheCreation,
			&promptConfigured,
			&completionConfigured,
			&cacheConfigured,
			&cacheReadConfigured,
			&cacheCreationConfigured,
		); err != nil {
			return nil, err
		}
		tier.PromptConfigured = promptConfigured != 0
		tier.CompletionConfigured = completionConfigured != 0
		tier.CacheConfigured = cacheConfigured != 0
		tier.CacheReadConfigured = cacheReadConfigured != 0
		tier.CacheCreationConfigured = cacheCreationConfigured != 0
		price, ok := prices[modelID]
		if !ok {
			continue
		}
		price.ContextTiers = append(price.ContextTiers, tier)
		prices[modelID] = price
	}
	if err := tierRows.Err(); err != nil {
		_ = tierRows.Close()
		return nil, err
	}
	if err := tierRows.Close(); err != nil {
		return nil, err
	}

	serviceTierRows, err := tx.QueryContext(ctx, `select
		model, mode, service_tier, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m,
		prompt_configured, completion_configured, cache_configured, cache_read_configured, cache_creation_configured
		from model_price_service_tiers order by model, mode, service_tier`)
	if err != nil {
		return nil, err
	}
	for serviceTierRows.Next() {
		var modelID string
		var tier model.ModelPriceServiceTier
		var promptConfigured, completionConfigured, cacheConfigured, cacheReadConfigured, cacheCreationConfigured int
		if err := serviceTierRows.Scan(
			&modelID,
			&tier.Mode,
			&tier.ServiceTier,
			&tier.Prompt,
			&tier.Completion,
			&tier.Cache,
			&tier.CacheRead,
			&tier.CacheCreation,
			&promptConfigured,
			&completionConfigured,
			&cacheConfigured,
			&cacheReadConfigured,
			&cacheCreationConfigured,
		); err != nil {
			return nil, err
		}
		tier.PromptConfigured = promptConfigured != 0
		tier.CompletionConfigured = completionConfigured != 0
		tier.CacheConfigured = cacheConfigured != 0
		tier.CacheReadConfigured = cacheReadConfigured != 0
		tier.CacheCreationConfigured = cacheCreationConfigured != 0
		price, ok := prices[modelID]
		if !ok {
			continue
		}
		price.ServiceTiers = append(price.ServiceTiers, tier)
		prices[modelID] = price
	}
	if err := serviceTierRows.Err(); err != nil {
		_ = serviceTierRows.Close()
		return nil, err
	}
	if err := serviceTierRows.Close(); err != nil {
		return nil, err
	}
	return prices, nil
}

func (r *repository) ReplaceAll(ctx context.Context, prices map[string]model.ModelPrice) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	normalizedPrices := make(map[string]model.ModelPrice, len(prices))
	for modelID, price := range prices {
		if err := model.ValidateModelPrice(modelID, price); err != nil {
			return err
		}
		price.ContextTiers, err = model.NormalizeModelPriceContextTiers(price.ContextTiers)
		if err != nil {
			return err
		}
		price.ServiceTiers, err = model.NormalizeModelPriceServiceTiers(price.ServiceTiers)
		if err != nil {
			return err
		}
		normalizedPrices[modelID] = price
	}

	if _, err := tx.ExecContext(ctx, `delete from model_price_service_tiers`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from model_price_context_tiers`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from model_prices`); err != nil {
		return err
	}
	if len(normalizedPrices) == 0 {
		return tx.Commit()
	}

	stmt, err := tx.PrepareContext(ctx, `insert into model_prices (
		model, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m,
		prompt_configured, completion_configured, cache_read_configured, cache_creation_configured, source, source_model_id,
		raw_json, updated_at_ms, synced_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	tierStmt, err := prepareContextTierInsert(ctx, tx)
	if err != nil {
		return err
	}
	defer tierStmt.Close()
	serviceTierStmt, err := prepareServiceTierInsert(ctx, tx)
	if err != nil {
		return err
	}
	defer serviceTierStmt.Close()

	now := time.Now().UnixMilli()
	for modelID, price := range normalizedPrices {
		if _, err := stmt.ExecContext(
			ctx,
			modelID,
			price.Prompt,
			price.Completion,
			price.Cache,
			price.CacheRead,
			price.CacheCreation,
			price.PromptConfigured,
			price.CompletionConfigured,
			price.CacheReadConfigured,
			price.CacheCreationConfigured,
			nullString(price.Source),
			nullString(price.SourceModelID),
			nullString(price.RawJSON),
			now,
			nullInt(price.SyncedAtMS),
		); err != nil {
			return err
		}
		if err := insertContextTiers(ctx, tierStmt, modelID, price.ContextTiers); err != nil {
			return err
		}
		if err := insertServiceTiers(ctx, serviceTierStmt, modelID, price.ServiceTiers); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *repository) UpsertSynced(ctx context.Context, prices map[string]model.ModelPrice) (model.ModelPriceSyncResult, error) {
	if len(prices) == 0 {
		return model.ModelPriceSyncResult{}, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ModelPriceSyncResult{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmt, err := tx.PrepareContext(ctx, `insert into model_prices (
		model, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m,
		prompt_configured, completion_configured, cache_read_configured, cache_creation_configured, source, source_model_id,
		raw_json, updated_at_ms, synced_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	on conflict(model) do update set
		prompt_per_1m = excluded.prompt_per_1m,
		completion_per_1m = excluded.completion_per_1m,
		cache_per_1m = excluded.cache_per_1m,
		cache_read_per_1m = excluded.cache_read_per_1m,
		cache_creation_per_1m = excluded.cache_creation_per_1m,
		prompt_configured = excluded.prompt_configured,
		completion_configured = excluded.completion_configured,
		cache_read_configured = excluded.cache_read_configured,
		cache_creation_configured = excluded.cache_creation_configured,
		source = excluded.source,
		source_model_id = excluded.source_model_id,
		raw_json = excluded.raw_json,
		updated_at_ms = excluded.updated_at_ms,
		synced_at_ms = excluded.synced_at_ms`)
	if err != nil {
		return model.ModelPriceSyncResult{}, err
	}
	defer stmt.Close()
	deleteTierStmt, err := tx.PrepareContext(ctx, `delete from model_price_context_tiers where model = ?`)
	if err != nil {
		return model.ModelPriceSyncResult{}, err
	}
	defer deleteTierStmt.Close()
	tierStmt, err := prepareContextTierInsert(ctx, tx)
	if err != nil {
		return model.ModelPriceSyncResult{}, err
	}
	defer tierStmt.Close()
	deleteServiceTierStmt, err := tx.PrepareContext(ctx, `delete from model_price_service_tiers where model = ?`)
	if err != nil {
		return model.ModelPriceSyncResult{}, err
	}
	defer deleteServiceTierStmt.Close()
	serviceTierStmt, err := prepareServiceTierInsert(ctx, tx)
	if err != nil {
		return model.ModelPriceSyncResult{}, err
	}
	defer serviceTierStmt.Close()

	now := time.Now().UnixMilli()
	result := model.ModelPriceSyncResult{}
	for modelID, price := range prices {
		if err := model.ValidateModelPrice(modelID, price); err != nil {
			result.Skipped++
			continue
		}
		price.ContextTiers, err = model.NormalizeModelPriceContextTiers(price.ContextTiers)
		if err != nil {
			result.Skipped++
			continue
		}
		price.ServiceTiers, err = model.NormalizeModelPriceServiceTiers(price.ServiceTiers)
		if err != nil {
			result.Skipped++
			continue
		}
		if price.Source == "" {
			price.Source = "sync"
		}
		if price.SourceModelID == "" {
			price.SourceModelID = modelID
		}
		price.UpdatedAtMS = now
		price.SyncedAtMS = &now
		if _, err := stmt.ExecContext(
			ctx,
			modelID,
			price.Prompt,
			price.Completion,
			price.Cache,
			price.CacheRead,
			price.CacheCreation,
			price.PromptConfigured,
			price.CompletionConfigured,
			price.CacheReadConfigured,
			price.CacheCreationConfigured,
			nullString(price.Source),
			nullString(price.SourceModelID),
			nullString(price.RawJSON),
			now,
			now,
		); err != nil {
			return model.ModelPriceSyncResult{}, err
		}
		if _, err := deleteTierStmt.ExecContext(ctx, modelID); err != nil {
			return model.ModelPriceSyncResult{}, err
		}
		if err := insertContextTiers(ctx, tierStmt, modelID, price.ContextTiers); err != nil {
			return model.ModelPriceSyncResult{}, err
		}
		if _, err := deleteServiceTierStmt.ExecContext(ctx, modelID); err != nil {
			return model.ModelPriceSyncResult{}, err
		}
		if err := insertServiceTiers(ctx, serviceTierStmt, modelID, price.ServiceTiers); err != nil {
			return model.ModelPriceSyncResult{}, err
		}
		result.Imported++
	}
	if err := tx.Commit(); err != nil {
		return model.ModelPriceSyncResult{}, err
	}
	return result, nil
}

func prepareContextTierInsert(ctx context.Context, tx *sql.Tx) (*sql.Stmt, error) {
	return tx.PrepareContext(ctx, `insert into model_price_context_tiers (
		model, threshold_tokens, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m,
		prompt_configured, completion_configured, cache_configured, cache_read_configured, cache_creation_configured
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
}

func insertContextTiers(ctx context.Context, stmt *sql.Stmt, modelID string, tiers []model.ModelPriceContextTier) error {
	for _, tier := range tiers {
		if _, err := stmt.ExecContext(
			ctx,
			modelID,
			tier.ThresholdTokens,
			tier.Prompt,
			tier.Completion,
			tier.Cache,
			tier.CacheRead,
			tier.CacheCreation,
			tier.PromptConfigured,
			tier.CompletionConfigured,
			tier.CacheConfigured,
			tier.CacheReadConfigured,
			tier.CacheCreationConfigured,
		); err != nil {
			return err
		}
	}
	return nil
}

func prepareServiceTierInsert(ctx context.Context, tx *sql.Tx) (*sql.Stmt, error) {
	return tx.PrepareContext(ctx, `insert into model_price_service_tiers (
		model, mode, service_tier, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m,
		prompt_configured, completion_configured, cache_configured, cache_read_configured, cache_creation_configured
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
}

func insertServiceTiers(ctx context.Context, stmt *sql.Stmt, modelID string, tiers []model.ModelPriceServiceTier) error {
	for _, tier := range tiers {
		if _, err := stmt.ExecContext(
			ctx,
			modelID,
			tier.Mode,
			tier.ServiceTier,
			tier.Prompt,
			tier.Completion,
			tier.Cache,
			tier.CacheRead,
			tier.CacheCreation,
			tier.PromptConfigured,
			tier.CompletionConfigured,
			tier.CacheConfigured,
			tier.CacheReadConfigured,
			tier.CacheCreationConfigured,
		); err != nil {
			return err
		}
	}
	return nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
