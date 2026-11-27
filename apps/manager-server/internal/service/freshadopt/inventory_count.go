package freshadopt

import (
	"context"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// CountSourceInventory reads non-secret per-reference item counts from a v8
// adoption source so the staged inventory screen can display concrete data
// volumes before the operator confirms migration.
//
// Each read is independent and read-only: if either endpoint is unavailable,
// the counts observed from the other are still returned without failing the
// staged strategy checkpoint.
func CountSourceInventory(ctx context.Context, source Source) map[string]int {
	if source == nil {
		return nil
	}
	counts := make(map[string]int)

	if files, err := source.ListCredentialFiles(ctx); err == nil {
		counts[string(bootstrap.AdoptionRefCPAAuthFiles)] = len(files)
	}

	document, docErr := source.ReadConfigDocument(ctx)
	if docErr != nil || document == nil {
		if len(counts) == 0 {
			return nil
		}
		return counts
	}

	// Client API keys under access.api-keys.
	if raw, presence := cpa.LookupConfigValue(document, "access.api-keys"); presence == cpa.ConfigPresent {
		counts[string(bootstrap.AdoptionRefClientAPIKeys)] = nonEmptyStringSliceLen(raw)
	}

	// Copyable gateway leaves and payload rule families.
	for _, path := range cpa.ConfigLeafPaths() {
		if ManagerOwned(path) {
			continue
		}
		if _, presence := cpa.LookupConfigValue(document, path); presence == cpa.ConfigPresent {
			ref := cpa.AdoptionItemRef(path)
			counts[string(ref)]++
		}
	}
	for _, family := range cpa.ConfigRuleFamilyIDs() {
		entry, ok := cpa.LookupConfigRuleFamily(family)
		if !ok || ManagerOwned(entry.Path) {
			continue
		}
		if raw, presence := cpa.LookupConfigValue(document, entry.Path); presence == cpa.ConfigPresent && collectionCount(raw) > 0 {
			ref := cpa.AdoptionItemRef(family)
			counts[string(ref)] += collectionCount(raw)
		}
	}

	// Provider API key groups, OAuth model maps, and structured whole-PUT sections.
	for _, section := range ProviderSections() {
		if ManagerOwned(section) {
			continue
		}
		if raw, presence := cpa.LookupConfigValue(document, section); presence == cpa.ConfigPresent {
			counted := raw
			if section == "credentials.concurrency" {
				clean, ok := stripConcurrencyRevisions(raw)
				if !ok || len(clean) == 0 {
					continue
				}
				counted = clean
			}
			if n := collectionCount(counted); n > 0 {
				ref := cpa.AdoptionItemRef(section)
				counts[string(ref)] += n
			}
		}
	}

	// Host-managed configuration sections (count only host-exclusive keys present at source).
	if n := countPresentPaths(document, []string{
		"server.server-id",
		"server.host",
		"server.port",
		"server.tls",
		"server.discovery",
		"server.kv-store.path",
	}); n > 0 {
		counts[string(bootstrap.AdoptionRefConfigServer)] = n
	}
	if raw, presence := cpa.LookupConfigValue(document, "management"); presence == cpa.ConfigPresent {
		if n := collectionCount(raw); n > 0 {
			counts[string(bootstrap.AdoptionRefConfigManagement)] = n
		}
	}
	if n := countPresentPaths(document, []string{
		"plugins.enabled",
		"plugins.dir",
		"plugins.auth-revision",
	}); n > 0 {
		counts[string(bootstrap.AdoptionRefConfigPlugins)] = n
	}
	if n := countPresentPaths(document, []string{
		"observability.logs.logging-to-file",
		"observability.logs.logs-max-total-size-mb",
		"observability.logs.error-logs-max-files",
	}); n > 0 {
		counts[string(bootstrap.AdoptionRefConfigLogging)] = n
	}
	if raw, presence := cpa.LookupConfigValue(document, "observability.usage"); presence == cpa.ConfigPresent {
		if n := collectionCount(raw); n > 0 {
			counts[string(bootstrap.AdoptionRefConfigUsage)] = n
		}
	}
	if n := countHostObservabilitySubtrees(document); n > 0 {
		counts[string(bootstrap.AdoptionRefConfigObservability)] = n
	}
	if n := len(cpa.UnknownV8ConfigPaths(document)); n > 0 {
		counts[string(bootstrap.AdoptionRefConfigUnknown)] = n
	}
	if len(counts) == 0 {
		return nil
	}
	return counts
}

func nonEmptyStringSliceLen(raw any) int {
	items, ok := raw.([]any)
	if !ok {
		return 0
	}
	n := 0
	for _, item := range items {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			n++
		}
	}
	return n
}

func collectionCount(raw any) int {
	switch typed := raw.(type) {
	case turnCredentialsRedactedSentinel:
		return 1
	case []any:
		return len(typed)
	case map[string]any:
		n := 0
		for _, val := range typed {
			if val != nil {
				n++
			}
		}
		return n
	default:
		return 0
	}
}

func countPresentPaths(document map[string]any, paths []string) int {
	n := 0
	for _, path := range paths {
		if raw, presence := cpa.LookupConfigValue(document, path); presence == cpa.ConfigPresent && nonEmptyPresentValue(raw) {
			n++
		}
	}
	return n
}

func nonEmptyPresentValue(raw any) bool {
	switch typed := raw.(type) {
	case map[string]any:
		return collectionCount(typed) > 0
	case []any:
		return len(typed) > 0
	default:
		return typed != nil
	}
}

func countHostObservabilitySubtrees(document map[string]any) int {
	n := 0
	if raw, presence := cpa.LookupConfigValue(document, "observability.usage"); presence == cpa.ConfigPresent && collectionCount(raw) > 0 {
		n++
	}
	if raw, presence := cpa.LookupConfigValue(document, "observability.pprof"); presence == cpa.ConfigPresent && collectionCount(raw) > 0 {
		n++
	}
	if countPresentPaths(document, []string{
		"observability.logs.logging-to-file",
		"observability.logs.logs-max-total-size-mb",
		"observability.logs.error-logs-max-files",
	}) > 0 {
		n++
	}
	return n
}

