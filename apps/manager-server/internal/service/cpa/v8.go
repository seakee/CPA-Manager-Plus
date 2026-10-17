package cpa

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// safeReadLimit bounds every management safe-read body. The same envelope is
// shared by the v8 probe and the explicit legacy_managed v0 adapter.
const safeReadLimit = 1 << 20

// V8ReadError exposes stable failures only. Neither upstream bodies nor URLs or
// credentials are safe diagnostic text at the Fresh authority boundary.
type V8ReadError string

func (e V8ReadError) Error() string { return string(e) }

const (
	V8OwnerUpgradeRequired   V8ReadError = "owner_upgrade_required"
	V8AuthenticationRejected V8ReadError = "management_authentication_rejected"
	V8ReadUnavailable        V8ReadError = "management_safe_read_unavailable"
	V8InvalidConfig          V8ReadError = "management_v8_config_invalid"
	V8InvalidConnection      V8ReadError = "management_connection_invalid"
)

// V8ConfigObservation intentionally drops config values: config may contain
// provider and client secrets. Safe read is not qualification of write, OAuth,
// usage, lifecycle or Gateway capabilities.
type V8ConfigObservation struct {
	BaseURL          string
	ClientKeyCount   int
	ResourceSections []string
}

func NormalizeV8BaseURL(raw string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "\\%@?#\x00\r\n\t ") {
		return "", V8InvalidConnection
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return "", V8InvalidConnection
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", V8InvalidConnection
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

// ReadV8Config is the sole production Fresh External probe. It performs exactly
// one authenticated GET, with direct transport, fixed DNS resolution, normal TLS
// verification, no redirects/retries, and one five-second total budget. The
// schema follows the AQ-03 required-field projection. This helper does not
// establish independent profile or production-consumer acceptance.
func ReadV8Config(ctx context.Context, baseURL, managementKey string) (V8ConfigObservation, error) {
	return readV8Config(ctx, baseURL, managementKey, nil)
}

// V8AdoptionSnapshot contains only the bounded source inventory consumed by
// Fresh Adoption. ResourceSections are stable category names; config values,
// provider secrets, auth files, usage/history and logs never escape this read.
type V8AdoptionSnapshot struct {
	APIKeys          []string
	ResourceSections []string
}

// ReadV8AdoptionSnapshot is used only after resource-specific server
// qualification. It shares the bounded safe-read transport; no source write,
// lifecycle mutation, usage/history access or queue consumption is performed.
func ReadV8AdoptionSnapshot(ctx context.Context, baseURL, managementKey string) (V8AdoptionSnapshot, error) {
	var keys []string
	observation, err := readV8Config(ctx, baseURL, managementKey, &keys)
	if err != nil {
		return V8AdoptionSnapshot{}, err
	}
	return V8AdoptionSnapshot{APIKeys: keys, ResourceSections: append([]string(nil), observation.ResourceSections...)}, nil
}

// ReadV8AdoptionKeys remains the narrow verification/test projection.
func ReadV8AdoptionKeys(ctx context.Context, baseURL, managementKey string) ([]string, error) {
	snapshot, err := ReadV8AdoptionSnapshot(ctx, baseURL, managementKey)
	return snapshot.APIKeys, err
}

func readV8Config(ctx context.Context, baseURL, managementKey string, adoptionKeys *[]string) (V8ConfigObservation, error) {
	baseURL, err := NormalizeV8BaseURL(baseURL)
	if err != nil {
		return V8ConfigObservation{}, err
	}
	if !validManagementKey(managementKey) {
		return V8ConfigObservation{}, V8InvalidConnection
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, closeClient, err := directReadClient(ctx, baseURL)
	if err != nil {
		return V8ConfigObservation{}, err
	}
	defer closeClient()
	return readV8ConfigWithClient(ctx, client, baseURL, managementKey, adoptionKeys)
}

func validManagementKey(managementKey string) bool {
	if managementKey == "" || managementKey != strings.TrimSpace(managementKey) || len(managementKey) > 4096 {
		return false
	}
	for _, c := range managementKey {
		if c < 0x21 || c == 0x7f {
			return false
		}
	}
	return true
}

// directReadClient is the shared management safe-read transport: direct
// connectivity, no proxy, one fixed DNS resolution, no redirect following, no
// retry and a bounded header/body budget. It never mutates its target.
func directReadClient(ctx context.Context, baseURL string) (*http.Client, func(), error) {
	u, _ := url.Parse(baseURL)
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil || len(addresses) == 0 {
		return nil, nil, V8ReadUnavailable
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	transport.DisableKeepAlives = true
	transport.MaxResponseHeaderBytes = 32 << 10
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(addresses[0].IP.String(), port))
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, transport.CloseIdleConnections, nil
}

func readV8ConfigWithClient(ctx context.Context, client *http.Client, baseURL, managementKey string, adoptionKeys *[]string) (V8ConfigObservation, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v8/management/config", nil)
	if err != nil {
		return V8ConfigObservation{}, V8InvalidConnection
	}
	request.Header.Set("Authorization", "Bearer "+managementKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity, gzip")
	request.Header.Set("Cache-Control", "no-store")
	response, err := client.Do(request)
	if err != nil {
		return V8ConfigObservation{}, V8ReadUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusGone:
		return V8ConfigObservation{}, V8OwnerUpgradeRequired
	case http.StatusUnauthorized, http.StatusForbidden:
		return V8ConfigObservation{}, V8AuthenticationRejected
	case http.StatusOK:
	default:
		return V8ConfigObservation{}, V8ReadUnavailable
	}
	mediaType, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || len(parameters) > 1 ||
		(len(parameters) == 1 && !strings.EqualFold(parameters["charset"], "utf-8")) {
		return V8ConfigObservation{}, V8InvalidConfig
	}
	data, ok := readBoundedJSONBody(response)
	if !ok {
		return V8ConfigObservation{}, V8InvalidConfig
	}
	var config struct {
		Version int `json:"config-version"`
		Server  struct {
			Port int `json:"port"`
		} `json:"server"`
		OAuth struct {
			AuthDir string `json:"auth-dir"`
		} `json:"oauth"`
		Access struct {
			APIKeys json.RawMessage `json:"api-keys"`
		} `json:"access"`
	}
	if err := json.Unmarshal(data, &config); err != nil || config.Version != 8 || config.Server.Port < 1 || config.Server.Port > 65535 || config.OAuth.AuthDir == "" {
		return V8ConfigObservation{}, V8InvalidConfig
	}
	var clientKeys []json.RawMessage
	if err := json.Unmarshal(config.Access.APIKeys, &clientKeys); err != nil || clientKeys == nil {
		return V8ConfigObservation{}, V8InvalidConfig
	}
	for _, raw := range clientKeys {
		var key string
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &key) != nil {
			return V8ConfigObservation{}, V8InvalidConfig
		}
	}
	// Only stable category names escape; arbitrary upstream keys are untrusted.
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(data, &sections); err != nil {
		return V8ConfigObservation{}, V8InvalidConfig
	}
	observation := V8ConfigObservation{BaseURL: baseURL, ClientKeyCount: len(clientKeys)}
	for _, section := range []string{"server", "management", "access", "oauth", "providers", "api-keys", "routing", "client", "logging", "usage", "plugins", "observability"} {
		if _, ok := sections[section]; ok {
			observation.ResourceSections = append(observation.ResourceSections, section)
			delete(sections, section)
		}
	}
	delete(sections, "config-version")
	if len(sections) > 0 {
		observation.ResourceSections = append(observation.ResourceSections, "unknown_cpa_native_resource")
	}
	if adoptionKeys != nil {
		if len(clientKeys) == 0 || len(clientKeys) > 128 {
			return V8ConfigObservation{}, V8InvalidConfig
		}
		keys := make([]string, 0, len(clientKeys))
		seen := map[string]bool{}
		for _, raw := range clientKeys {
			var key string
			_ = json.Unmarshal(raw, &key)
			if key == "" || key != strings.TrimSpace(key) || len(key) > 4096 || strings.ContainsAny(key, "\x00\r\n") || seen[key] {
				return V8ConfigObservation{}, V8InvalidConfig
			}
			for _, character := range key {
				if character < 0x21 || character == 0x7f {
					return V8ConfigObservation{}, V8InvalidConfig
				}
			}
			seen[key] = true
			keys = append(keys, key)
		}
		*adoptionKeys = keys
	}
	return observation, nil
}

var _ error = V8ReadError("")

// readBoundedJSONBody enforces the shared safe-read body envelope after a 200
// response: at most one MiB on the wire and after decompression, identity or
// gzip only, and valid UTF-8. Callers keep their own typed error constants.
func readBoundedJSONBody(response *http.Response) ([]byte, bool) {
	encoded, err := io.ReadAll(io.LimitReader(response.Body, safeReadLimit+1))
	if err != nil || len(encoded) > safeReadLimit {
		return nil, false
	}
	data := encoded
	switch response.Header.Get("Content-Encoding") {
	case "", "identity":
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(encoded))
		if err != nil {
			return nil, false
		}
		data, err = io.ReadAll(io.LimitReader(reader, safeReadLimit+1))
		_ = reader.Close()
		if err != nil || len(data) > safeReadLimit {
			return nil, false
		}
	default:
		return nil, false
	}
	if !utf8.Valid(data) {
		return nil, false
	}
	return data, true
}
