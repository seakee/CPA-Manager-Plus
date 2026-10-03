package reconnect

import (
	"strings"
	"time"
)

// provider describes one kind of subscription login CPA holds that its owner
// can reconnect. The flow, reminders and table are shared; only these differ.
type provider struct {
	// ID is the provider name in CPA auth files and on each request.
	ID string
	// AuthURLPath starts an OAuth login on the CPA management API.
	AuthURLPath string
	// CallbackProvider is the provider name /oauth-callback expects
	// (callback logins only).
	CallbackProvider string
	// Device: approved on the provider's site; CPA polls, nothing is pasted.
	Device bool
	// Window is how long CPA waits after Connect when it does not say.
	Window time.Duration
	// Name is how the login is called in messages.
	Name string
}

const (
	ProviderClaude      = "claude"
	ProviderCodex       = "codex"
	ProviderAntigravity = "antigravity"
	ProviderXAI         = "xai"
	ProviderMeta        = "meta"
)

var providers = map[string]provider{
	ProviderClaude:      {ID: ProviderClaude, AuthURLPath: "/anthropic-auth-url", CallbackProvider: "anthropic", Window: 5 * time.Minute, Name: "Claude"},
	ProviderCodex:       {ID: ProviderCodex, AuthURLPath: "/codex-auth-url", CallbackProvider: "codex", Window: 5 * time.Minute, Name: "ChatGPT (Codex)"},
	ProviderAntigravity: {ID: ProviderAntigravity, AuthURLPath: "/antigravity-auth-url", CallbackProvider: "antigravity", Window: 5 * time.Minute, Name: "Antigravity"},
	ProviderXAI:         {ID: ProviderXAI, AuthURLPath: "/xai-auth-url", Device: true, Window: 30 * time.Minute, Name: "xAI (Grok)"},
	ProviderMeta:        {ID: ProviderMeta, AuthURLPath: "/meta-auth-url", Device: true, Window: 15 * time.Minute, Name: "Muse (Meta)"},
}

// ProviderIDs lists supported providers in display order.
var ProviderIDs = []string{ProviderClaude, ProviderCodex, ProviderAntigravity, ProviderXAI, ProviderMeta}

func providerFor(id string) provider {
	if p, ok := providers[strings.ToLower(strings.TrimSpace(id))]; ok {
		return p
	}
	return providers[ProviderClaude]
}

// IsProvider reports whether id is a supported provider.
func IsProvider(id string) bool {
	_, ok := providers[strings.ToLower(strings.TrimSpace(id))]
	return ok
}

func ownerKey(provider, email string) string {
	return strings.ToLower(provider) + "|" + strings.ToLower(strings.TrimSpace(email))
}
