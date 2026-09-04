package usageevent

import (
	"context"
	"database/sql"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

// SQLQueryer is the read-only subset shared by *sql.DB and *sql.Tx. Keeping
// the identity check on this narrow interface lets account-history and
// account-window readers evaluate the same evidence inside their own snapshot
// transaction.
type SQLQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type legacyAccountIdentityPredicate struct {
	sql  string
	args []any
}

// maxCodexLegacyIdentityProbe is the largest matching set this check will
// inspect per predicate. The request path cannot scan an unbounded
// usage_events bucket; a larger set is treated as inconclusive and refused.
const maxCodexLegacyIdentityProbe = 512

// ResolveCodexLegacyAccountKey returns the file/index account key only when
// every inspected matching usage event remains attributable to the requested
// Codex account. Sets larger than maxCodexLegacyIdentityProbe are refused.
// A false result is intentionally non-error: callers must fail closed and
// continue with the stable key only.
func ResolveCodexLegacyAccountKey(
	ctx context.Context,
	queryer SQLQueryer,
	fields usageidentity.Fields,
) (string, bool, error) {
	legacyKey, targetAccountID, authFile, authIndex, valid := codexLegacyTarget(fields)
	if !valid {
		return "", false, nil
	}
	allowed, err := inspectCodexLegacyIdentity(ctx, queryer, authFile, authIndex, targetAccountID)
	if err != nil || !allowed {
		return "", false, err
	}
	return legacyKey, true, nil
}

func inspectCodexLegacyIdentity(
	ctx context.Context,
	queryer SQLQueryer,
	authFile, authIndex, targetAccountID string,
) (bool, error) {
	for _, predicate := range legacyAccountIdentityPredicates(authFile, authIndex) {
		allowed, err := inspectCodexLegacyIdentityPredicate(ctx, queryer, predicate, targetAccountID)
		if err != nil {
			return false, err
		}
		if !allowed {
			return false, nil
		}
	}
	return true, nil
}

func inspectCodexLegacyIdentityPredicate(
	ctx context.Context,
	queryer SQLQueryer,
	predicate legacyAccountIdentityPredicate,
	targetAccountID string,
) (bool, error) {
	args := make([]any, 0, len(predicate.args)+1)
	args = append(args, predicate.args...)
	args = append(args, maxCodexLegacyIdentityProbe+1)
	rows, err := queryer.QueryContext(ctx, `select
		coalesce(e.provider, ''),
		coalesce(e.auth_provider_snapshot, ''),
		coalesce(e.auth_account_id_snapshot, ''),
		coalesce(e.auth_project_id_snapshot, '')
	from usage_events e
	where `+predicate.sql+`
	order by e.timestamp_ms desc, e.id desc
	limit ?`, args...)
	if err != nil {
		return false, err
	}
	return scanLegacyAccountIdentity(rows, targetAccountID, maxCodexLegacyIdentityProbe)
}

// ResolveCodexLegacyAccountKey evaluates the same check through a short
// read-only transaction for service-layer account-history requests.
func (r *repository) ResolveCodexLegacyAccountKey(
	ctx context.Context,
	fields usageidentity.Fields,
) (string, bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	key, allowed, err := ResolveCodexLegacyAccountKey(ctx, tx, fields)
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return key, allowed, nil
}

func codexLegacyTarget(fields usageidentity.Fields) (string, string, string, string, bool) {
	targetAccountID := strings.TrimSpace(fields.AuthAccountIDSnapshot)
	if !codexLegacyIdentityRequested(fields) {
		return "", "", "", "", false
	}
	authFile, authIndex, ok := codexLegacyCredential(fields)
	if !ok {
		return "", "", "", "", false
	}
	legacyKey, valid := usageidentity.LegacyAccountKey(fields)
	if !valid {
		return "", "", "", "", false
	}
	return legacyKey, targetAccountID, authFile, authIndex, true
}

func codexLegacyIdentityRequested(fields usageidentity.Fields) bool {
	return normalizeIdentityProvider(fields.AuthProviderSnapshot) == "codex" &&
		strings.TrimSpace(fields.AuthAccountIDSnapshot) != ""
}

func codexLegacyCredential(fields usageidentity.Fields) (string, string, bool) {
	authFile := strings.TrimSpace(fields.AuthFileSnapshot)
	if authFile == "" {
		authFile = codexLegacySourceFile(fields)
	}
	authIndex := strings.TrimSpace(fields.AuthIndex)
	if authFile == "" || authIndex == "" {
		return "", "", false
	}
	return authFile, authIndex, true
}

func codexLegacySourceFile(fields usageidentity.Fields) string {
	source := strings.TrimSpace(fields.Source)
	account := strings.TrimSpace(fields.AccountSnapshot)
	label := strings.TrimSpace(fields.AuthLabelSnapshot)
	if source == "" || strings.EqualFold(source, account) || strings.EqualFold(source, label) {
		return ""
	}
	return source
}

func legacyAccountIdentityPredicates(authFile, authIndex string) []legacyAccountIdentityPredicate {
	predicates := make([]legacyAccountIdentityPredicate, 0, 6)
	appendIndexPredicates := func(base string, baseArgs []any) {
		if authIndex != "" {
			predicates = append(predicates, legacyAccountIdentityPredicate{
				sql:  base + ` and e.auth_index collate nocase = ?`,
				args: append(append([]any{}, baseArgs...), authIndex),
			})
			return
		}
		predicates = append(predicates,
			legacyAccountIdentityPredicate{
				sql:  base + ` and e.auth_index is null`,
				args: append([]any{}, baseArgs...),
			},
			legacyAccountIdentityPredicate{
				sql:  base + ` and e.auth_index collate nocase = ''`,
				args: append([]any{}, baseArgs...),
			},
		)
	}

	appendIndexPredicates(`e.auth_file_snapshot collate nocase = ?`, []any{authFile})
	legacySourceBase := `e.auth_file_snapshot is null and e.source collate nocase = ?` + legacySourceIdentityGuards()
	appendIndexPredicates(legacySourceBase, []any{authFile})
	legacyEmptySourceBase := `e.auth_file_snapshot = '' and e.source collate nocase = ?` + legacySourceIdentityGuards()
	appendIndexPredicates(legacyEmptySourceBase, []any{authFile})
	return predicates
}

func legacySourceIdentityGuards() string {
	// source is used as a physical file only when it is not merely the display
	// account or label. Keep the indexed source/auth_index predicates intact and
	// apply these guards as residual filters.
	return `
		and (e.account_snapshot is null or lower(trim(e.source)) <> lower(trim(e.account_snapshot)))
		and (e.auth_label_snapshot is null or lower(trim(e.source)) <> lower(trim(e.auth_label_snapshot)))`
}

func scanLegacyAccountIdentity(rows *sql.Rows, targetAccountID string, maxRows int) (bool, error) {
	defer rows.Close()
	seenRows := 0
	for rows.Next() {
		ok, err := consumeLegacyIdentityRow(rows, &seenRows, maxRows, targetAccountID)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, rows.Err()
}

func consumeLegacyIdentityRow(rows *sql.Rows, seenRows *int, maxRows int, targetAccountID string) (bool, error) {
	*seenRows++
	if *seenRows > maxRows {
		return false, nil
	}
	var provider, authProvider, accountID, projectID string
	if err := rows.Scan(&provider, &authProvider, &accountID, &projectID); err != nil {
		return false, err
	}
	return legacyIdentityRowAllowed(provider, authProvider, accountID, projectID, targetAccountID), nil
}

func legacyIdentityRowAllowed(provider, authProvider, accountID, projectID, targetAccountID string) bool {
	return legacyIdentityProviderAllowed(provider, authProvider) &&
		legacyIdentityAccountIDsAllowed(accountID, projectID, targetAccountID)
}

func legacyIdentityProviderAllowed(provider, authProvider string) bool {
	hasProvider := false
	for _, value := range []string{provider, authProvider} {
		normalized := normalizeIdentityProvider(value)
		if normalized == "" {
			continue
		}
		hasProvider = true
		if normalized != "codex" {
			return false
		}
	}
	return hasProvider
}

func legacyIdentityAccountIDsAllowed(accountID, projectID, targetAccountID string) bool {
	for _, value := range []string{
		strings.TrimSpace(accountID),
		usageidentity.CodexAccountIDFromSnapshot(projectID),
	} {
		if value == "" {
			continue
		}
		if value != targetAccountID {
			return false
		}
	}
	return true
}

func normalizeIdentityProvider(value string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	switch normalized {
	case "x-ai", "grok":
		return "xai"
	default:
		return normalized
	}
}
