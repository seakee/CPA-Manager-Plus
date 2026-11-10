package cpa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// PrivateProviderSeed is the nonsecret identity of ONE host-prepared fixture.
// It is supplied only by the existing root-owned, short-lived S1 permit. There
// is no default target, public configuration API, discovery or second writer.
type PrivateProviderSeed struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Target           string `json:"target"`
	CredentialSHA256 string `json:"credentialSHA256"`
}

// Binding is stored in the existing one-shot install journal, so a later Host
// permit cannot retarget the installed Key to another fixture in the same run.
// Historical blank/S2-A journals retain their empty binding.
func (s PrivateProviderSeed) Binding() string {
	if s == (PrivateProviderSeed{}) {
		return ""
	}
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s PrivateProviderSeed) Valid() bool {
	u, err := url.Parse(s.Target)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" &&
		u.Host == u.Hostname() && strings.Contains(u.Hostname(), ".") && net.ParseIP(u.Hostname()) == nil &&
		strings.ToLower(u.Host) == u.Host && u.Path == "/v1" && u.RawPath == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" &&
		privateSeedName(s.Provider) && privateSeedName(s.Model) && s.Model != "s1-proof-model" && validHex(s.CredentialSHA256)
}

func privateSeedName(v string) bool {
	if len(v) == 0 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._/", c)) {
			return false
		}
	}
	return true
}

// NewPrivateS2BTransport retains the S1 transport and unique InstallOne writer.
// The source is reloaded on each observation, including after Manager restart.
func NewPrivateS2BTransport(source func() (PrivateProviderSeed, error)) *Transport {
	t := NewTransport()
	if source == nil {
		source = func() (PrivateProviderSeed, error) { return PrivateProviderSeed{}, ErrSourceConflict }
	}
	t.providerSeed = source
	return t
}

// Reject duplicate/aliased fields before interpreting the management JSON.
// EqualFold also catches Unicode simple-fold aliases used by encoding/json.
func uniqueSeedJSON(d *json.Decoder, depth int) bool {
	if depth > 16 {
		return false
	}
	t, err := d.Token()
	if err != nil {
		return false
	}
	open, ok := t.(json.Delim)
	if !ok {
		return true
	}
	if open != '{' && open != '[' {
		return false
	}
	var names []string
	for d.More() {
		if open == '{' {
			t, err = d.Token()
			name, ok := t.(string)
			if err != nil || !ok || len(names) >= 8 {
				return false
			}
			for _, previous := range names {
				if strings.EqualFold(name, previous) {
					return false
				}
			}
			names = append(names, name)
		}
		if !uniqueSeedJSON(d, depth+1) {
			return false
		}
	}
	t, err = d.Token()
	return err == nil && (open == '{' && t == json.Delim('}') || open == '[' && t == json.Delim(']'))
}

func privateProviderConfig(data []byte, seed PrivateProviderSeed, clientKey string) bool {
	if !seed.Valid() {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if !uniqueSeedJSON(d, 0) || d.Decode(new(any)) != io.EOF {
		return false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || len(root) != 8 {
		return false
	}
	var providers map[string]json.RawMessage
	if json.Unmarshal(root["api-keys"], &providers) != nil || len(providers) != 1 {
		return false
	}
	var groups []map[string]json.RawMessage
	if json.Unmarshal(providers["openai-compatibility"], &groups) != nil || len(groups) != 1 || len(groups[0]) != 5 {
		return false
	}
	g := groups[0]
	var provider, target string
	if json.Unmarshal(g["name"], &provider) != nil || provider != seed.Provider ||
		json.Unmarshal(g["base-url"], &target) != nil || target != seed.Target || !bytes.Equal(g["request-retry"], []byte("0")) {
		return false
	}
	var models []map[string]string
	if json.Unmarshal(g["models"], &models) != nil || len(models) != 1 || len(models[0]) != 2 ||
		models[0]["name"] != seed.Model || models[0]["alias"] != seed.Model {
		return false
	}
	var keys []map[string]string
	if json.Unmarshal(g["keys"], &keys) != nil || len(keys) != 1 || len(keys[0]) != 2 {
		return false
	}
	credential, authIndex := keys[0]["api-key"], keys[0]["auth_index"]
	digest := sha256.Sum256([]byte(credential))
	if len(credential) < 16 || len(credential) > 4096 || !printable(credential) ||
		hex.EncodeToString(digest[:]) != seed.CredentialSHA256 || !ValidAuthIndex(authIndex) {
		return false
	}
	// Only remove the separately checked provider; the original exact S1 empty
	// seed validator remains authoritative for every other configuration field.
	delete(root, "api-keys")
	if clientKey != "" {
		var access map[string]json.RawMessage
		var installed []string
		if json.Unmarshal(root["access"], &access) != nil || len(access) != 1 ||
			json.Unmarshal(access["api-keys"], &installed) != nil || len(installed) != 1 || installed[0] != clientKey {
			return false
		}
		root["access"] = json.RawMessage(`{"api-keys":[]}`)
	}
	base, err := json.Marshal(root)
	return err == nil && emptyProofConfig(base)
}

func (t *Transport) providerConfigured(ctx context.Context, management, clientKey string) bool {
	seed, err := t.providerSeed()
	if err != nil {
		return false
	}
	status, body, err := t.request(ctx, http.MethodGet, "/v8/management/config", management, nil)
	if err == nil && status == 200 {
		body, err = t.postInstallProjection(ctx, body)
	}
	return err == nil && status == 200 && privateProviderConfig(body, seed, clientKey)
}

// ValidAuthIndex reports whether s is CPA's 16-character lowercase hexadecimal
// credential auth_index (sdk/cliproxy/auth/types.go:580-587).
func ValidAuthIndex(s string) bool {
	if len(s) != 16 {
		return false
	}
	indexBytes, err := hex.DecodeString(s)
	return err == nil && len(indexBytes) == 8 && hex.EncodeToString(indexBytes) == s
}
