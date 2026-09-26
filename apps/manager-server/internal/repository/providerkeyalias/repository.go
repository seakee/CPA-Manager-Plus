package providerkeyalias

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

type Repository interface {
	LoadAll(ctx context.Context) ([]model.ProviderKeyAlias, error)
	Upsert(ctx context.Context, alias model.ProviderKeyAlias) error
	Delete(ctx context.Context, provider, apiKeyHash string) error
}

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

func (r *repository) LoadAll(ctx context.Context) ([]model.ProviderKeyAlias, error) {
	rows, err := r.db.QueryContext(ctx, `select provider, api_key_hash, alias, updated_at_ms
		from provider_key_aliases
		order by provider collate nocase, alias collate nocase, api_key_hash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	aliases := []model.ProviderKeyAlias{}
	for rows.Next() {
		var alias model.ProviderKeyAlias
		if err := rows.Scan(&alias.Provider, &alias.APIKeyHash, &alias.Alias, &alias.UpdatedAtMS); err != nil {
			return nil, err
		}
		aliases = append(aliases, alias)
	}
	return aliases, rows.Err()
}

func (r *repository) Upsert(ctx context.Context, alias model.ProviderKeyAlias) error {
	normalized, err := normalize(alias, time.Now().UnixMilli())
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var existingHash string
	err = tx.QueryRowContext(ctx, `
		select api_key_hash
		from provider_key_aliases
		where provider = ? and lower(alias) = lower(?) and api_key_hash <> ?
		limit 1`, normalized.Provider, normalized.Alias, normalized.APIKeyHash).Scan(&existingHash)
	if err == nil {
		return errors.New("provider key alias already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	_, err = tx.ExecContext(ctx, `insert into provider_key_aliases (
		provider, api_key_hash, alias, updated_at_ms
	) values (?, ?, ?, ?)
	on conflict(provider, api_key_hash) do update set
		alias = excluded.alias,
		updated_at_ms = excluded.updated_at_ms`,
		normalized.Provider,
		normalized.APIKeyHash,
		normalized.Alias,
		normalized.UpdatedAtMS,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "provider_key_aliases") {
			return errors.New("provider key alias already exists")
		}
		return err
	}
	return tx.Commit()
}

func (r *repository) Delete(ctx context.Context, provider, apiKeyHash string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	hash := strings.ToLower(strings.TrimSpace(apiKeyHash))
	if !validProvider(provider) {
		return errors.New("valid provider is required")
	}
	if !validHash(hash) {
		return errors.New("valid apiKeyHash is required")
	}
	_, err := r.db.ExecContext(ctx, `delete from provider_key_aliases where provider = ? and api_key_hash = ?`, provider, hash)
	return err
}

func normalize(alias model.ProviderKeyAlias, now int64) (model.ProviderKeyAlias, error) {
	provider := strings.ToLower(strings.TrimSpace(alias.Provider))
	hash := strings.ToLower(strings.TrimSpace(alias.APIKeyHash))
	label := strings.TrimSpace(alias.Alias)
	if !validProvider(provider) {
		return model.ProviderKeyAlias{}, errors.New("valid provider is required")
	}
	if !validHash(hash) {
		return model.ProviderKeyAlias{}, errors.New("valid apiKeyHash is required")
	}
	if label == "" {
		return model.ProviderKeyAlias{}, errors.New("alias is required")
	}
	if len([]rune(label)) > 120 {
		return model.ProviderKeyAlias{}, errors.New("alias must be 120 characters or less")
	}
	if alias.UpdatedAtMS <= 0 {
		alias.UpdatedAtMS = now
	}
	return model.ProviderKeyAlias{
		Provider:    provider,
		APIKeyHash:  hash,
		Alias:       label,
		UpdatedAtMS: alias.UpdatedAtMS,
	}, nil
}

func validProvider(value string) bool {
	return value != "" && len([]rune(value)) <= 64
}

func validHash(value string) bool {
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
