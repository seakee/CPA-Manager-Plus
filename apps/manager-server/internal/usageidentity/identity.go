package usageidentity

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// FormatVersion changes whenever the canonical account-history identity
// algorithm changes. Persistent derived rollups include this version so they
// can be rebuilt from immutable usage_events without touching raw history.
const FormatVersion = "2"

const keyPrefix = "usage-account-history"

// Fields contains the credential snapshots available on a usage event or an
// account-history request. Display values are deliberately lower priority than
// credential identity fields so two credentials sharing an email never merge.
type Fields struct {
	AuthFileSnapshot      string
	AuthIndex             string
	AuthProviderSnapshot  string
	AuthProjectIDSnapshot string
	AccountSnapshot       string
	AuthLabelSnapshot     string
	Source                string
}

// AccountKey returns the canonical, backend-owned history key for one
// credential snapshot. The key is opaque to clients; RowKey is the response
// correlation contract.
func AccountKey(fields Fields) (string, bool) {
	authFile := effectiveAuthFile(fields)
	authIndex := strings.TrimSpace(fields.AuthIndex)
	provider := normalizeProvider(fields.AuthProviderSnapshot)
	projectID := strings.TrimSpace(fields.AuthProjectIDSnapshot)
	account := strings.TrimSpace(fields.AccountSnapshot)
	label := strings.TrimSpace(fields.AuthLabelSnapshot)

	switch {
	case authFile != "" && authIndex != "":
		return encodeKey("file-index", authFile, authIndex), true
	case authFile != "" && projectID != "":
		return encodeKey("file-project", authFile, provider, projectID), true
	case authFile != "" && account != "":
		return encodeKey("file-account", authFile, provider, account), true
	case authFile != "" && label != "":
		return encodeKey("file-label", authFile, provider, label), true
	case authFile != "":
		return encodeKey("file", authFile, provider), true
	case authIndex != "":
		return encodeKey("auth-index", provider, authIndex), true
	case projectID != "":
		return encodeKey("project", provider, projectID), true
	case account != "":
		return encodeKey("account", provider, account), true
	case label != "":
		return encodeKey("label", provider, label), true
	default:
		return "", false
	}
}

// PricingStructureRevision binds the pricing rollup structure to the identity
// format as well as model/context-tier structure.
func PricingStructureRevision(modelPriceRevision string) string {
	return fmt.Sprintf("model-%s:identity-%s:%s", ModelFormatVersion, FormatVersion, strings.TrimSpace(modelPriceRevision))
}

// AccountHistoryStructureRevision binds the legacy account-history rollup to
// both credential identity and analytics model identity.
func AccountHistoryStructureRevision() string {
	return fmt.Sprintf("identity-%s:model-%s", FormatVersion, ModelFormatVersion)
}

// SQLAccountKeyExpression returns the SQLite expression equivalent of
// AccountKey for a usage_events row. alias may be empty or a trusted internal
// table alias such as "e".
func SQLAccountKeyExpression(alias string) string {
	column := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	trimmed := func(name string) string {
		return "trim(coalesce(" + column(name) + ", ''))"
	}

	authFileSnapshot := trimmed("auth_file_snapshot")
	authIndex := trimmed("auth_index")
	source := trimmed("source")
	account := trimmed("account_snapshot")
	label := trimmed("auth_label_snapshot")
	projectID := trimmed("auth_project_id_snapshot")
	providerSource := "coalesce(nullif(" + trimmed("auth_provider_snapshot") + ", ''), " + trimmed("provider") + ", '')"
	providerNormalized := "case lower(replace(trim(" + providerSource + "), '_', '-')) " +
		"when 'x-ai' then 'xai' when 'grok' then 'xai' " +
		"else lower(replace(trim(" + providerSource + "), '_', '-')) end"
	authFile := "case when " + authFileSnapshot + " <> '' then " + authFileSnapshot +
		" when " + source + " <> '' and " + source + " <> " + account + " and " + source + " <> " + label +
		" then " + source + " else '' end"

	hexValue := func(value string) string { return "hex(" + value + ")" }
	prefix := "'" + keyPrefix + ":" + FormatVersion + ":"
	key := func(kind string, values ...string) string {
		parts := []string{prefix + kind + ":'"}
		for index, value := range values {
			if index > 0 {
				parts = append(parts, "':'")
			}
			parts = append(parts, hexValue(value))
		}
		return strings.Join(parts, " || ")
	}

	return "case " +
		"when " + authFile + " <> '' and " + authIndex + " <> '' then " + key("file-index", authFile, authIndex) + " " +
		"when " + authFile + " <> '' and " + projectID + " <> '' then " + key("file-project", authFile, providerNormalized, projectID) + " " +
		"when " + authFile + " <> '' and " + account + " <> '' then " + key("file-account", authFile, providerNormalized, account) + " " +
		"when " + authFile + " <> '' and " + label + " <> '' then " + key("file-label", authFile, providerNormalized, label) + " " +
		"when " + authFile + " <> '' then " + key("file", authFile, providerNormalized) + " " +
		"when " + authIndex + " <> '' then " + key("auth-index", providerNormalized, authIndex) + " " +
		"when " + projectID + " <> '' then " + key("project", providerNormalized, projectID) + " " +
		"when " + account + " <> '' then " + key("account", providerNormalized, account) + " " +
		"when " + label + " <> '' then " + key("label", providerNormalized, label) + " " +
		"else '' end"
}

func effectiveAuthFile(fields Fields) string {
	if value := strings.TrimSpace(fields.AuthFileSnapshot); value != "" {
		return value
	}
	source := strings.TrimSpace(fields.Source)
	if source == "" || source == strings.TrimSpace(fields.AccountSnapshot) || source == strings.TrimSpace(fields.AuthLabelSnapshot) {
		return ""
	}
	return source
}

// normalizeProvider normalizes provider namespaces for persisted credential
// identity and account history identity keys.
func normalizeProvider(value string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	switch normalized {
	case "x-ai", "grok":
		return "xai"
	default:
		return normalized
	}
}

func encodeKey(kind string, values ...string) string {
	parts := make([]string, 0, len(values)+3)
	parts = append(parts, keyPrefix, FormatVersion, kind)
	for _, value := range values {
		parts = append(parts, strings.ToUpper(hex.EncodeToString([]byte(strings.TrimSpace(value)))))
	}
	return strings.Join(parts, ":")
}
