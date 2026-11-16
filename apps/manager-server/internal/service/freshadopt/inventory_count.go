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
		if !ok {
			continue
		}
		if raw, presence := cpa.LookupConfigValue(document, entry.Path); presence == cpa.ConfigPresent && collectionCount(raw) > 0 {
			ref := cpa.AdoptionItemRef(family)
			counts[string(ref)] += collectionCount(raw)
		}
	}

	// Provider API key groups and OAuth model maps.
	for _, section := range ProviderSections() {
		if ManagerOwned(section) {
			continue
		}
		if raw, presence := cpa.LookupConfigValue(document, section); presence == cpa.ConfigPresent {
			if n := collectionCount(raw); n > 0 {
				ref := cpa.AdoptionItemRef(section)
				counts[string(ref)] += n
			}
		}
	}

	// Manual-review configuration sections (non-empty keys present at source).
	manualSections := []struct {
		path string
		ref  bootstrap.ResourceRef
	}{
		{path: "server", ref: bootstrap.AdoptionRefConfigServer},
		{path: "management", ref: bootstrap.AdoptionRefConfigManagement},
		{path: "plugins", ref: bootstrap.AdoptionRefConfigPlugins},
		{path: "observability", ref: bootstrap.AdoptionRefConfigObservability},
		{path: "observability.logs", ref: bootstrap.AdoptionRefConfigLogging},
		{path: "observability.usage", ref: bootstrap.AdoptionRefConfigUsage},
	}
	for _, entry := range manualSections {
		if raw, presence := cpa.LookupConfigValue(document, entry.path); presence == cpa.ConfigPresent {
			if n := collectionCount(raw); n > 0 {
				counts[string(entry.ref)] = n
			}
		}
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
