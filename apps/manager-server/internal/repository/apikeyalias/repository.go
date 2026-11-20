package apikeyalias

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

type Repository interface {
	LoadAll(ctx context.Context) ([]model.APIKeyAlias, error)
	UpsertMany(ctx context.Context, aliases []model.APIKeyAlias, activeHashes []string, allowOrphanCleanup bool) error
	Delete(ctx context.Context, apiKeyHash string) error
}

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

type aliasSchemaInfo struct {
	hasFreshKeys     bool
	hasIdentities    bool
	hasBindings      bool
	hasSecretHistory bool
	hasLegacyAliases bool
}

func inspectAliasSchema(ctx context.Context, tx *sql.Tx) (aliasSchemaInfo, error) {
	rows, err := tx.QueryContext(ctx, `select name
		from sqlite_master
		where type = 'table'
		  and name in (
			'fresh_client_api_keys',
			'gateway_api_key_identities',
			'gateway_api_key_source_bindings',
			'fresh_client_api_key_secret_history',
			'api_key_aliases'
		  )`)
	if err != nil {
		return aliasSchemaInfo{}, err
	}
	defer rows.Close()

	var info aliasSchemaInfo
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return aliasSchemaInfo{}, err
		}
		switch name {
		case "fresh_client_api_keys":
			info.hasFreshKeys = true
		case "gateway_api_key_identities":
			info.hasIdentities = true
		case "gateway_api_key_source_bindings":
			info.hasBindings = true
		case "fresh_client_api_key_secret_history":
			info.hasSecretHistory = true
		case "api_key_aliases":
			info.hasLegacyAliases = true
		}
	}
	return info, rows.Err()
}

func freshClientKeySelectExpressions(ctx context.Context, tx *sql.Tx) (nameExpr, nameRevExpr, originExpr string, err error) {
	rows, err := tx.QueryContext(ctx, `pragma table_info(fresh_client_api_keys)`)
	if err != nil {
		return "", "", "", err
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return "", "", "", err
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		return "", "", "", err
	}

	nameExpr = "''"
	if cols["name"] {
		nameExpr = "k.name"
	}
	nameRevExpr = "1"
	if cols["name_revision"] {
		nameRevExpr = "k.name_revision"
	}
	originExpr = "'workspace'"
	if cols["origin"] {
		originExpr = "k.origin"
	}
	return nameExpr, nameRevExpr, originExpr, nil
}

func loadLegacyAliasesTx(ctx context.Context, tx *sql.Tx) ([]model.APIKeyAlias, error) {
	rows, err := tx.QueryContext(ctx, `select api_key_hash, alias, updated_at_ms
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

func normalizeManagedKeyDirectoryAlias(origin string, nameRevision int64, name string) string {
	trimmed := strings.TrimSpace(name)
	if origin == "installer" && nameRevision <= 1 && (name == "Initial key" || trimmed == "Initial key") {
		return ""
	}
	return trimmed
}

func (r *repository) LoadAll(ctx context.Context) ([]model.APIKeyAlias, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	schema, err := inspectAliasSchema(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !schema.hasFreshKeys || !schema.hasIdentities {
		return loadLegacyAliasesTx(ctx, tx)
	}

	nameExpr, nameRevExpr, originExpr, err := freshClientKeySelectExpressions(ctx, tx)
	if err != nil {
		return nil, err
	}

	historyPreviewByHash := map[string]string{}
	if schema.hasSecretHistory {
		histRows, err := tx.QueryContext(ctx, `select secret_hash, masked_preview
			from fresh_client_api_key_secret_history
			order by retired_at_ms desc`)
		if err != nil {
			return nil, err
		}
		for histRows.Next() {
			var (
				rawHash string
				preview string
			)
			if err := histRows.Scan(&rawHash, &preview); err != nil {
				_ = histRows.Close()
				return nil, err
			}
			hash := strings.ToLower(strings.TrimSpace(rawHash))
			if hash == "" {
				continue
			}
			if _, exists := historyPreviewByHash[hash]; !exists {
				historyPreviewByHash[hash] = strings.TrimSpace(preview)
			}
		}
		if err := histRows.Close(); err != nil {
			return nil, err
		}
		if err := histRows.Err(); err != nil {
			return nil, err
		}
	}

	aliases := []model.APIKeyAlias{}
	covered := map[string]struct{}{}

	if schema.hasBindings {
		bindingRows, err := tx.QueryContext(ctx, `select
				b.api_key_hash,
				k.api_key_id,
				`+nameExpr+`,
				`+nameRevExpr+`,
				`+originExpr+`,
				k.masked_preview,
				k.secret_hash,
				i.lifecycle,
				i.updated_at_ms,
				b.retired_at_ms
			from gateway_api_key_source_bindings b
			join fresh_client_api_keys k on k.api_key_id = b.api_key_id
			join gateway_api_key_identities i on i.id = k.api_key_id
			order by
				case when b.retired_at_ms is null then 0 else 1 end asc,
				case when i.lifecycle = 'active' then 0 else 1 end asc,
				b.last_seen_at_ms desc,
				i.updated_at_ms desc,
				b.binding_id desc`)
		if err != nil {
			return nil, err
		}
		for bindingRows.Next() {
			var (
				rawBindingHash string
				apiKeyID       string
				name           string
				nameRevision   int64
				origin         string
				maskedPreview  string
				rawSecretHash  string
				lifecycle      string
				updatedAtMS    int64
				retiredAtMS    sql.NullInt64
			)
			if err := bindingRows.Scan(
				&rawBindingHash,
				&apiKeyID,
				&name,
				&nameRevision,
				&origin,
				&maskedPreview,
				&rawSecretHash,
				&lifecycle,
				&updatedAtMS,
				&retiredAtMS,
			); err != nil {
				_ = bindingRows.Close()
				return nil, err
			}
			hash := strings.ToLower(strings.TrimSpace(rawBindingHash))
			if hash == "" {
				continue
			}
			if _, exists := covered[hash]; exists {
				continue
			}
			origin = strings.TrimSpace(origin)
			preview := strings.TrimSpace(maskedPreview)
			secretHash := strings.ToLower(strings.TrimSpace(rawSecretHash))
			trimmedLifecycle := strings.TrimSpace(lifecycle)
			if hash != secretHash {
				preview = historyPreviewByHash[hash]
				if trimmedLifecycle != "superseded" {
					trimmedLifecycle = "rotated"
				}
				if retiredAtMS.Valid && retiredAtMS.Int64 > 0 {
					updatedAtMS = retiredAtMS.Int64
				}
			}
			aliases = append(aliases, model.APIKeyAlias{
				APIKeyHash:    hash,
				Alias:         normalizeManagedKeyDirectoryAlias(origin, nameRevision, name),
				UpdatedAtMS:   updatedAtMS,
				APIKeyID:      strings.TrimSpace(apiKeyID),
				MaskedPreview: preview,
				Origin:        origin,
				Lifecycle:     trimmedLifecycle,
			})
			covered[hash] = struct{}{}
		}
		if err := bindingRows.Close(); err != nil {
			return nil, err
		}
		if err := bindingRows.Err(); err != nil {
			return nil, err
		}
	}

	keyRows, err := tx.QueryContext(ctx, `select
			k.secret_hash,
			k.api_key_id,
			`+nameExpr+`,
			`+nameRevExpr+`,
			`+originExpr+`,
			k.masked_preview,
			i.lifecycle,
			i.updated_at_ms
		from fresh_client_api_keys k
		join gateway_api_key_identities i on i.id = k.api_key_id
		order by
			case when i.lifecycle = 'active' then 0 else 1 end asc,
			i.updated_at_ms desc,
			k.api_key_id asc`)
	if err != nil {
		return nil, err
	}
	for keyRows.Next() {
		var (
			rawSecretHash string
			apiKeyID      string
			name          string
			nameRevision  int64
			origin        string
			maskedPreview string
			lifecycle     string
			updatedAtMS   int64
		)
		if err := keyRows.Scan(
			&rawSecretHash,
			&apiKeyID,
			&name,
			&nameRevision,
			&origin,
			&maskedPreview,
			&lifecycle,
			&updatedAtMS,
		); err != nil {
			_ = keyRows.Close()
			return nil, err
		}
		hash := strings.ToLower(strings.TrimSpace(rawSecretHash))
		if hash == "" {
			continue
		}
		if _, exists := covered[hash]; exists {
			continue
		}
		origin = strings.TrimSpace(origin)
		aliases = append(aliases, model.APIKeyAlias{
			APIKeyHash:    hash,
			Alias:         normalizeManagedKeyDirectoryAlias(origin, nameRevision, name),
			UpdatedAtMS:   updatedAtMS,
			APIKeyID:      strings.TrimSpace(apiKeyID),
			MaskedPreview: strings.TrimSpace(maskedPreview),
			Origin:        origin,
			Lifecycle:     strings.TrimSpace(lifecycle),
		})
		covered[hash] = struct{}{}
	}
	if err := keyRows.Close(); err != nil {
		return nil, err
	}
	if err := keyRows.Err(); err != nil {
		return nil, err
	}

	if schema.hasSecretHistory {
		histKeyRows, err := tx.QueryContext(ctx, `select
				h.secret_hash,
				k.api_key_id,
				`+nameExpr+`,
				`+nameRevExpr+`,
				`+originExpr+`,
				h.masked_preview,
				i.lifecycle,
				i.updated_at_ms,
				h.retired_at_ms
			from fresh_client_api_key_secret_history h
			join fresh_client_api_keys k on k.api_key_id = h.api_key_id
			join gateway_api_key_identities i on i.id = k.api_key_id
			order by h.retired_at_ms desc, i.updated_at_ms desc, k.api_key_id asc`)
		if err != nil {
			return nil, err
		}
		for histKeyRows.Next() {
			var (
				rawHistHash       string
				apiKeyID          string
				name              string
				nameRevision      int64
				origin            string
				histMaskedPreview string
				lifecycle         string
				updatedAtMS       int64
				retiredAtMS       int64
			)
			if err := histKeyRows.Scan(
				&rawHistHash,
				&apiKeyID,
				&name,
				&nameRevision,
				&origin,
				&histMaskedPreview,
				&lifecycle,
				&updatedAtMS,
				&retiredAtMS,
			); err != nil {
				_ = histKeyRows.Close()
				return nil, err
			}
			hash := strings.ToLower(strings.TrimSpace(rawHistHash))
			if hash == "" {
				continue
			}
			if _, exists := covered[hash]; exists {
				continue
			}
			origin = strings.TrimSpace(origin)
			trimmedLifecycle := strings.TrimSpace(lifecycle)
			if trimmedLifecycle != "superseded" {
				trimmedLifecycle = "rotated"
			}
			if retiredAtMS > 0 {
				updatedAtMS = retiredAtMS
			}
			aliases = append(aliases, model.APIKeyAlias{
				APIKeyHash:    hash,
				Alias:         normalizeManagedKeyDirectoryAlias(origin, nameRevision, name),
				UpdatedAtMS:   updatedAtMS,
				APIKeyID:      strings.TrimSpace(apiKeyID),
				MaskedPreview: strings.TrimSpace(histMaskedPreview),
				Origin:        origin,
				Lifecycle:     trimmedLifecycle,
			})
			covered[hash] = struct{}{}
		}
		if err := histKeyRows.Close(); err != nil {
			return nil, err
		}
		if err := histKeyRows.Err(); err != nil {
			return nil, err
		}
	}

	if schema.hasLegacyAliases {
		legacyAliases, err := loadLegacyAliasesTx(ctx, tx)
		if err != nil {
			return nil, err
		}
		for _, legacy := range legacyAliases {
			hash := strings.ToLower(strings.TrimSpace(legacy.APIKeyHash))
			if hash == "" {
				continue
			}
			if _, exists := covered[hash]; exists {
				continue
			}
			aliases = append(aliases, legacy)
			covered[hash] = struct{}{}
		}
	}

	sort.SliceStable(aliases, func(i, j int) bool {
		left := strings.ToLower(aliases[i].Alias)
		right := strings.ToLower(aliases[j].Alias)
		if left != right {
			return left < right
		}
		return aliases[i].APIKeyHash < aliases[j].APIKeyHash
	})
	return aliases, nil
}

func (r *repository) UpsertMany(ctx context.Context, aliases []model.APIKeyAlias, activeHashes []string, allowOrphanCleanup bool) error {
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

	var activeSet map[string]struct{}
	if len(activeHashes) > 0 {
		activeSet = make(map[string]struct{}, len(activeHashes)+len(normalizedAliases))
		for _, h := range activeHashes {
			hash := strings.ToLower(strings.TrimSpace(h))
			if validAPIKeyHash(hash) {
				activeSet[hash] = struct{}{}
			}
		}
		for _, normalized := range normalizedAliases {
			activeSet[normalized.APIKeyHash] = struct{}{}
		}
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

	deleteStmt, err := tx.PrepareContext(ctx, `delete from api_key_aliases where api_key_hash = ?`)
	if err != nil {
		return err
	}
	defer deleteStmt.Close()

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
			if activeSet == nil {
				return errors.New("api key alias already exists")
			}
			if _, isActive := activeSet[existingHash]; isActive {
				return errors.New("api key alias already exists")
			}
			if !allowOrphanCleanup {
				return errors.New("api key alias already exists")
			}
			if _, err := deleteStmt.ExecContext(ctx, existingHash); err != nil {
				return err
			}
			delete(existingAliases, aliasKey)
		}
		if _, err := stmt.ExecContext(
			ctx,
			normalized.APIKeyHash,
			normalized.Alias,
			normalized.UpdatedAtMS,
		); err != nil {
			return err
		}
		existingAliases[aliasKey] = normalized.APIKeyHash
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
