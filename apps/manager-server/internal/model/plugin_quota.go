package model

// PluginQuotaItem is one labelled quota reading reported by a CPA plugin quota
// provider. Key is the plugin's stable item identity and is what CPAMP stores
// as the provider window id; Label, Format and Currency are the plugin's own
// presentation hints and are never interpreted by the server.
type PluginQuotaItem struct {
	Key      string
	Label    string
	Value    *float64
	ValueRaw string
	Unit     string
	Format   string
	Currency string
}

// PluginQuotaResult is one credential-scoped plugin quota observation. Provider
// is the quota provider CPA reported, which is also the provider used for the
// stored observation, and AuthIndex/AuthFileName are the credential locators
// the panel already renders for an auth file.
type PluginQuotaResult struct {
	PluginID        string
	Provider        string
	DisplayName     string
	SupportsReset   bool
	AuthFileName    string
	AuthLabel       string
	AuthIndex       string
	AccountSnapshot string
	ObservedAtMS    int64
	Items           []PluginQuotaItem
}
