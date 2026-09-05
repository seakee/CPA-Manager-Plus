package usageidentity

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// FormatVersion changes whenever the canonical account-history identity
// algorithm changes. Persistent derived rollups include this version so they
// can be rebuilt from immutable usage_events without touching raw history.
const FormatVersion = "3"

const (
	keyPrefix                    = "usage-account-history"
	codexAccountIDSnapshotPrefix = "codex-account-id:v1:"
	// CodexIdentityRevision changes independently from FormatVersion because
	// the other providers must retain their existing AccountKey values.
	CodexIdentityRevision = "2"
)

// CodexAccountIDSnapshot marks a freshly observed, explicit ChatGPT account_id
// before it is stored in the legacy project-id snapshot column. Historical
// values in that column predate this provenance marker and must never be
// reinterpreted as a stable Codex account identity.
func CodexAccountIDSnapshot(accountID string) string {
	accountID, ok := NormalizeCodexWorkspaceSnapshot(accountID)
	if !ok {
		return ""
	}
	return codexAccountIDSnapshotPrefix + accountID
}

// CodexAccountIDFromSnapshot returns an account id only when the snapshot has
// explicit provenance from the strict Codex account-id resolver.
func CodexAccountIDFromSnapshot(snapshot string) string {
	snapshot = strings.Trim(snapshot, " ")
	if !strings.HasPrefix(snapshot, codexAccountIDSnapshotPrefix) {
		return ""
	}
	accountID, ok := NormalizeCodexWorkspaceSnapshot(strings.TrimPrefix(snapshot, codexAccountIDSnapshotPrefix))
	if !ok {
		return ""
	}
	return accountID
}

// HasCodexAccountIDSnapshotMarker reports whether a historical project
// snapshot claims Codex account-id provenance, including malformed markers.
// Callers use this to distinguish missing legacy evidence from invalid
// evidence that must fail closed.
func HasCodexAccountIDSnapshotMarker(snapshot string) bool {
	return strings.HasPrefix(strings.Trim(snapshot, " "), codexAccountIDSnapshotPrefix)
}

// LegacyCodexWorkspaceAccountKey returns the v3 Workspace-only Codex key used
// before member-aware identity was introduced. It is intentionally separate
// from AccountKey: new writes must always remain member-aware when strong
// member evidence is available, while narrow compatibility readers may use
// this key to inspect orphaned legacy derived data.
func LegacyCodexWorkspaceAccountKey(provider, workspace string) (string, bool) {
	provider = normalizeProvider(provider)
	workspace, workspaceOK := NormalizeCodexWorkspaceSnapshot(workspace)
	if provider != "codex" || !workspaceOK {
		return "", false
	}
	return encodeCodexKey("codex-account", provider, workspace), true
}

// ProjectIDSnapshot returns only a real provider project snapshot. The
// pre-v3 Codex account marker is retained in immutable history for audit but
// must never be exposed as a project identifier again.
func ProjectIDSnapshot(provider, projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if normalizeProvider(provider) == "codex" && CodexAccountIDFromSnapshot(projectID) != "" {
		return ""
	}
	return projectID
}

// SQLProjectIDSnapshotExpression mirrors ProjectIDSnapshot for SQL filters
// and option lists. Keep this expression equivalent to the Go helper.
func SQLProjectIDSnapshotExpression(alias string) string {
	column := func(name string) string {
		if alias == "" {
			return name
		}
		if strings.HasSuffix(alias, ".") {
			return alias + name
		}
		return alias + "." + name
	}
	provider := "lower(replace(trim(coalesce(nullif(" + column("auth_provider_snapshot") + ", ''), " + column("provider") + ", '')), '_', '-'))"
	marker := "'" + codexAccountIDSnapshotPrefix + "'"
	project := "trim(coalesce(" + column("auth_project_id_snapshot") + ", ''))"
	return "case when " + provider + " = 'codex' and substr(" + project + ", 1, length(" + marker + ")) = " + marker + " then '' else " + project + " end"
}

// Fields contains the credential snapshots available on a usage event or an
// account-history request. Display values are deliberately lower priority than
// credential identity fields so two credentials sharing an email never merge.
type Fields struct {
	AuthFileSnapshot      string
	AuthIndex             string
	AuthProviderSnapshot  string
	AuthAccountIDSnapshot string
	AuthProjectIDSnapshot string
	AccountSnapshot       string
	AuthLabelSnapshot     string
	Source                string
}

// AccountKey returns the canonical, backend-owned history key for one
// credential snapshot. The key is opaque to clients; RowKey is the response
// correlation contract.
func AccountKey(fields Fields) (string, bool) {
	provider := normalizeProvider(fields.AuthProviderSnapshot)
	if provider == "codex" {
		workspaceID, workspaceOK := ResolveCodexWorkspace(fields)
		// A provenance marker in the historical project snapshot is legacy
		// evidence only. It may validate a legacy alias, but it must not
		// promote an old event into the new stable member bucket.
		if workspaceOK && strings.Trim(fields.AuthAccountIDSnapshot, " ") != "" {
			if member, ok := stableCodexMemberSnapshot(fields); ok {
				return encodeCodexKey("codex-member", provider, workspaceID, member), true
			}
		}
	}
	return LegacyAccountKey(fields)
}

// LegacyAccountKey returns the format-v2 credential identity. Account-window
// reads use it only as an exact file/index compatibility key for Codex events
// collected before a stable account_id snapshot was available.
func LegacyAccountKey(fields Fields) (string, bool) {
	authFile := effectiveAuthFile(fields)
	authIndex := strings.TrimSpace(fields.AuthIndex)
	provider := normalizeProvider(fields.AuthProviderSnapshot)
	projectID := strings.TrimSpace(fields.AuthProjectIDSnapshot)
	if provider == "codex" && CodexAccountIDFromSnapshot(projectID) != "" {
		projectID = ""
	}
	account := strings.TrimSpace(fields.AccountSnapshot)
	label := strings.TrimSpace(fields.AuthLabelSnapshot)
	// A Codex account snapshot is a Workspace identifier, and account/label/
	// project values are display or provider-scoped fallbacks rather than a
	// credential identity. Keep Codex's fallback at the physical credential
	// boundary so an incomplete member snapshot cannot merge a shared
	// Workspace. A missing history bucket is safer than a cross-member bucket.
	if provider == "codex" {
		switch {
		case authFile != "" && authIndex != "":
			return encodeKey("file-index", authFile, authIndex), true
		case authFile != "":
			return encodeKey("file", authFile, provider), true
		case authIndex != "":
			return encodeKey("auth-index", provider, authIndex), true
		default:
			return "", false
		}
	}

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

func PricingStructureRevision(modelPriceRevision string) string {
	return fmt.Sprintf("model-%s:identity-%s:codex-%s:%s", ModelFormatVersion, FormatVersion, CodexIdentityRevision, strings.TrimSpace(modelPriceRevision))
}

func AccountHistoryStructureRevision() string {
	return fmt.Sprintf("identity-%s:codex-%s:model-%s", FormatVersion, CodexIdentityRevision, ModelFormatVersion)
}

// MonitoringProjectionStructureRevision versions the derived monitoring
// projection independently from account-history rollups. The projection's
// search document intentionally omits the historical Codex account marker
// from the project field, so changing that expression must rebuild only the
// projection/search derivation from immutable usage_events.
func MonitoringProjectionStructureRevision() string {
	return AccountHistoryStructureRevision() + ":project-v1"
}

func SQLAccountKeyExpression(alias string) string {
	return sqlAccountKeyExpression(alias, false)
}

// SQLAccountKeyExpressionWithoutProject mirrors SQLAccountKeyExpression for
// derived tables that intentionally do not persist auth_project_id_snapshot.
// Their remaining identity columns are sufficient for file/index and Codex
// workspace/member keys, which are the only keys eligible for the
// account-window daily fast path.
func SQLAccountKeyExpressionWithoutProject(alias string) string {
	return sqlAccountKeyExpression(alias, true)
}

func sqlAccountKeyExpression(alias string, withoutProject bool) string {
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
	accountID := trimmed("auth_account_id_snapshot")
	projectID := "''"
	if !withoutProject {
		projectID = trimmed("auth_project_id_snapshot")
	}
	providerSource := "coalesce(nullif(" + trimmed("auth_provider_snapshot") + ", ''), " + trimmed("provider") + ", '')"
	providerNormalized := "case lower(replace(" + providerSource + ", '_', '-')) " +
		"when 'x-ai' then 'xai' when 'grok' then 'xai' " +
		"else lower(replace(" + providerSource + ", '_', '-')) end"
	authFile := "case when " + authFileSnapshot + " <> '' then " + authFileSnapshot +
		" when " + source + " <> '' and " + source + " <> " + account + " and " + source + " <> " + label +
		" then " + source + " else '' end"
	marker := "'" + codexAccountIDSnapshotPrefix + "'"
	legacyMarkerAccountID := "case when substr(" + projectID + ", 1, length(" + marker + ")) = " + marker +
		" then trim(substr(" + projectID + ", length(" + marker + ") + 1)) else '' end"
	codexMember := "lower(" + account + ")"
	printableASCII := func(value string) string {
		return value + " <> '' and length(" + value + ") = length(cast(" + value + " as blob)) and " +
			value + " not glob '*[^!-~]*'"
	}
	directWorkspacePresent := accountID + " <> ''"
	markedWorkspacePresent := "substr(" + projectID + ", 1, length(" + marker + ")) = " + marker
	markedWorkspaceValid := "(" + markedWorkspacePresent + " = 0 or " + legacyMarkerAccountID + " <> '')"
	codexWorkspaceValid := markedWorkspaceValid + " and (" +
		"(" + directWorkspacePresent + " = 0) or (" + markedWorkspacePresent + " = 0) or " +
		accountID + " = " + legacyMarkerAccountID + ")"
	codexWorkspaceID := accountID
	codexMemberValid := providerNormalized + " = 'codex' and " + directWorkspacePresent + " and " + codexWorkspaceValid + " and " +
		printableASCII(account) + " and " +
		"instr(" + codexMember + ", '@') > 1 and " +
		"instr(" + codexMember + ", '@') < length(" + codexMember + ") and " +
		"length(" + codexMember + ") - length(replace(" + codexMember + ", '@', '')) = 1"
	legacyProjectID := "case when " + providerNormalized + " = 'codex' and " + legacyMarkerAccountID + " <> '' then '' else " + projectID + " end"

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
		"when " + codexMemberValid + " then " + key("codex-member", providerNormalized, codexWorkspaceID, codexMember) + " " +
		"when " + authFile + " <> '' and " + authIndex + " <> '' then " + key("file-index", authFile, authIndex) + " " +
		"when " + providerNormalized + " = 'codex' and " + authFile + " <> '' then " + key("file", authFile, providerNormalized) + " " +
		"when " + providerNormalized + " <> 'codex' and " + authFile + " <> '' and " + legacyProjectID + " <> '' then " + key("file-project", authFile, providerNormalized, legacyProjectID) + " " +
		"when " + providerNormalized + " <> 'codex' and " + authFile + " <> '' and " + account + " <> '' then " + key("file-account", authFile, providerNormalized, account) + " " +
		"when " + providerNormalized + " <> 'codex' and " + authFile + " <> '' and " + label + " <> '' then " + key("file-label", authFile, providerNormalized, label) + " " +
		"when " + authFile + " <> '' then " + key("file", authFile, providerNormalized) + " " +
		"when " + authIndex + " <> '' then " + key("auth-index", providerNormalized, authIndex) + " " +
		"when " + providerNormalized + " <> 'codex' and " + legacyProjectID + " <> '' then " + key("project", providerNormalized, legacyProjectID) + " " +
		"when " + providerNormalized + " <> 'codex' and " + account + " <> '' then " + key("account", providerNormalized, account) + " " +
		"when " + providerNormalized + " <> 'codex' and " + label + " <> '' then " + key("label", providerNormalized, label) + " " +
		"else '' end"
}

// ResolveCodexWorkspace returns a workspace only when all explicit Workspace
// evidence agrees. The legacy project snapshot is trusted only when it carries
// the explicit CodexAccountIDSnapshot provenance marker; generic project IDs
// are never Workspace evidence. Invalid or conflicting evidence is rejected so
// callers can fall back to credential identity or fail closed as appropriate.
func ResolveCodexWorkspace(fields Fields) (string, bool) {
	directRaw := fields.AuthAccountIDSnapshot
	markedRaw := fields.AuthProjectIDSnapshot
	directPresent := strings.Trim(directRaw, " ") != ""
	markedPresent := strings.HasPrefix(strings.Trim(markedRaw, " "), codexAccountIDSnapshotPrefix)

	direct := ""
	if directPresent {
		var ok bool
		direct, ok = NormalizeCodexWorkspaceSnapshot(directRaw)
		if !ok {
			return "", false
		}
	}
	marked := ""
	if markedPresent {
		marked = CodexAccountIDFromSnapshot(markedRaw)
		if marked == "" {
			return "", false
		}
	}
	if direct != "" && marked != "" && direct != marked {
		return "", false
	}
	if direct != "" {
		return direct, true
	}
	if marked != "" {
		return marked, true
	}
	return "", false
}

// NormalizeCodexMemberSnapshot returns a member identity only for a
// conservative email-shaped account snapshot. Labels, filenames, and other
// display values are deliberately rejected so a shared Workspace cannot be
// mistaken for a member.
func NormalizeCodexMemberSnapshot(value string) (string, bool) {
	// SQLite's trim/lower functions are ASCII-oriented. Keep the accepted
	// identity deliberately ASCII-compatible so runtime and SQL rebuilds cannot
	// normalize the same member differently.
	value = strings.Trim(value, " ")
	if value == "" || strings.Count(value, "@") != 1 {
		return "", false
	}
	for index := 0; index < len(value); index++ {
		if value[index] <= 0x20 || value[index] >= 0x7f {
			return "", false
		}
	}
	parts := strings.SplitN(value, "@", 2)
	if parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return strings.ToLower(value), true
}

// NormalizeCodexWorkspaceSnapshot applies the same trimming SQLite's default
// trim() uses. Workspace IDs are opaque; only an empty value is unusable.
func NormalizeCodexWorkspaceSnapshot(value string) (string, bool) {
	value = strings.Trim(value, " ")
	if value == "" {
		return "", false
	}
	return value, true
}

func stableCodexMemberSnapshot(fields Fields) (string, bool) {
	return NormalizeCodexMemberSnapshot(fields.AccountSnapshot)
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
	return encodeKeyWithTrim(kind, strings.TrimSpace, values...)
}

// encodeCodexKey uses the same ASCII-space trimming as the SQL identity
// expression. Keep this separate from encodeKey so the Codex opaque workspace
// path cannot change the established AccountKey encoding for other providers.
func encodeCodexKey(kind string, values ...string) string {
	return encodeKeyWithTrim(kind, func(value string) string { return strings.Trim(value, " ") }, values...)
}

func encodeKeyWithTrim(kind string, trim func(string) string, values ...string) string {
	parts := make([]string, 0, len(values)+3)
	parts = append(parts, keyPrefix, FormatVersion, kind)
	for _, value := range values {
		parts = append(parts, strings.ToUpper(hex.EncodeToString([]byte(trim(value)))))
	}
	return strings.Join(parts, ":")
}
