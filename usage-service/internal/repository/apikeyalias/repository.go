package apikeyalias

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/model"
)

type Repository interface {
	LoadAll(ctx context.Context) ([]model.APIKeyAlias, error)
	UpsertMany(ctx context.Context, aliases []model.APIKeyAlias) error
	Delete(ctx context.Context, apiKeyHash string) error
}

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

func (r *repository) LoadAll(ctx context.Context) ([]model.APIKeyAlias, error) {
	rows, err := r.db.QueryContext(ctx, `select api_key_hash, alias, updated_at_ms
		from api_key_aliases
		order by alias collate nocase, api_key_hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	aliases := []model.APIKeyAlias{}
	for rows.Next() {
		var alias model.APIKeyAlias
		if err := rows.Scan(&alias.APIKeyHash, &alias.Alias, &alias.UpdatedAtMS); err != nil {
			return nil, err
		}
		aliases = append(aliases, alias)
	}
	return aliases, rows.Err()
}

func (r *repository) UpsertMany(ctx context.Context, aliases []model.APIKeyAlias) error {
	if len(aliases) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	normalizedAliases := make([]model.APIKeyAlias, 0, len(aliases))
	seenAliases := map[string]string{}
	for _, alias := range aliases {
		normalized, err := normalizeAPIKeyAlias(alias, now)
		if err != nil {
			return err
		}
		aliasKey := normalizeAPIKeyAliasUniqueKey(normalized.Alias)
		if existingHash, ok := seenAliases[aliasKey]; ok && existingHash != normalized.APIKeyHash {
			return errors.New("api key alias already exists")
		}
		seenAliases[aliasKey] = normalized.APIKeyHash
		normalizedAliases = append(normalizedAliases, normalized)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	stmt, err := tx.PrepareContext(ctx, `insert into api_key_aliases (
		api_key_hash, alias, updated_at_ms
	) values (?, ?, ?)
	on conflict(api_key_hash) do update set
		alias = excluded.alias,
		updated_at_ms = excluded.updated_at_ms`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	existingRows, err := tx.QueryContext(ctx, `select api_key_hash, alias from api_key_aliases`)
	if err != nil {
		return err
	}
	existingAliases := map[string]string{}
	for existingRows.Next() {
		var apiKeyHash string
		var alias string
		if err := existingRows.Scan(&apiKeyHash, &alias); err != nil {
			_ = existingRows.Close()
			return err
		}
		existingAliases[normalizeAPIKeyAliasUniqueKey(alias)] = apiKeyHash
	}
	if err := existingRows.Close(); err != nil {
		return err
	}
	if err := existingRows.Err(); err != nil {
		return err
	}

	for _, normalized := range normalizedAliases {
		aliasKey := normalizeAPIKeyAliasUniqueKey(normalized.Alias)
		if existingHash, ok := existingAliases[aliasKey]; ok && existingHash != normalized.APIKeyHash {
			return errors.New("api key alias already exists")
		}
		if _, err := stmt.ExecContext(
			ctx,
			normalized.APIKeyHash,
			normalized.Alias,
			normalized.UpdatedAtMS,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *repository) Delete(ctx context.Context, apiKeyHash string) error {
	hash := strings.ToLower(strings.TrimSpace(apiKeyHash))
	if !validAPIKeyHash(hash) {
		return errors.New("valid apiKeyHash is required")
	}
	_, err := r.db.ExecContext(ctx, `delete from api_key_aliases where api_key_hash = ?`, hash)
	return err
}

func normalizeAPIKeyAlias(alias model.APIKeyAlias, now int64) (model.APIKeyAlias, error) {
	hash := strings.ToLower(strings.TrimSpace(alias.APIKeyHash))
	if !validAPIKeyHash(hash) {
		return model.APIKeyAlias{}, errors.New("valid apiKeyHash is required")
	}
	label := strings.TrimSpace(alias.Alias)
	if label == "" {
		return model.APIKeyAlias{}, errors.New("alias is required")
	}
	if len([]rune(label)) > 120 {
		return model.APIKeyAlias{}, errors.New("alias must be 120 characters or less")
	}
	if alias.UpdatedAtMS <= 0 {
		alias.UpdatedAtMS = now
	}
	alias.APIKeyHash = hash
	alias.Alias = label
	return alias, nil
}

func normalizeAPIKeyAliasUniqueKey(alias string) string {
	return strings.ToLower(strings.TrimSpace(alias))
}

func validAPIKeyHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') {
			continue
		}
		return false
	}
	return true
}
