package modelprice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

type Repository interface {
	LoadAll(ctx context.Context) (map[string]model.ModelPrice, error)
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
	rows, err := r.db.QueryContext(ctx, `select
		model, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m,
		prompt_configured, completion_configured, cache_read_configured, cache_creation_configured, source, source_model_id, raw_json,
		updated_at_ms, synced_at_ms
		from model_prices order by model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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
	return prices, rows.Err()
}

func (r *repository) ReplaceAll(ctx context.Context, prices map[string]model.ModelPrice) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx, `delete from model_prices`); err != nil {
		return err
	}
	if len(prices) == 0 {
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

	now := time.Now().UnixMilli()
	for modelID, price := range prices {
		if err := validateModelPrice(modelID, price); err != nil {
			return err
		}
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

	now := time.Now().UnixMilli()
	result := model.ModelPriceSyncResult{}
	for modelID, price := range prices {
		if err := validateModelPrice(modelID, price); err != nil {
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
		result.Imported++
	}
	if err := tx.Commit(); err != nil {
		return model.ModelPriceSyncResult{}, err
	}
	return result, nil
}

func validateModelPrice(modelID string, price model.ModelPrice) error {
	if modelID == "" {
		return errors.New("model is required")
	}
	if !validPriceValue(price.Prompt) || !validPriceValue(price.Completion) || !validPriceValue(price.Cache) ||
		!validPriceValue(price.CacheRead) || !validPriceValue(price.CacheCreation) {
		return fmt.Errorf("invalid model price for %s", modelID)
	}
	return nil
}

func validPriceValue(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
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
