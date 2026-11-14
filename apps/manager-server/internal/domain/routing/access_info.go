package routing

// AccessProtocol is non-secret, authenticated client setup information. It is
// not an Ingress route allowlist: the pinned CPA's complete inference surface
// includes additional actions and aliases within these protocol families.
type AccessProtocol struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	BasePath    string `json:"basePath"`
	ExamplePath string `json:"examplePath"`
}

// InferenceProtocols preserves the complete caller-controlled gateway base,
// including an External CPA's existing path. Managed callers supply a verified
// relative prefix; External callers supply a validated absolute connection URL.
// Empty input is not an implicit public-root configuration.
func InferenceProtocols(base string) []AccessProtocol {
	if base == "" || base == "/" {
		return []AccessProtocol{}
	}
	return []AccessProtocol{
		{ID: "openai", Label: "OpenAI", BasePath: base + "/v1", ExamplePath: base + "/v1/chat/completions"},
		{ID: "responses", Label: "Responses", BasePath: base + "/v1", ExamplePath: base + "/v1/responses"},
		{ID: "anthropic", Label: "Anthropic", BasePath: base, ExamplePath: base + "/v1/messages"},
		{ID: "gemini", Label: "Gemini", BasePath: base, ExamplePath: base + "/v1beta/models"},
	}
}
