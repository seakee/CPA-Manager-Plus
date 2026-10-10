package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
)

// ErrInvalidEventHash is returned when a new usage event does not contain a canonical
// 64-character SHA-256 hexadecimal hash.
var ErrInvalidEventHash = errors.New("invalid usage event hash")

const (
	quotedKeyPattern   = `(?:"([^"\r\n]+)"|'([^'\r\n]+)')`
	unquotedKeyPattern = `\b([a-zA-Z0-9_.-]+(?:[ \t]+[a-zA-Z0-9_.-]+){0,2})\b`
)

var (
	// authColonHeaderRegex matches "Authorization: ..." or "*_authorization: ..." colon headers across all schemes
	authColonHeaderRegex = regexp.MustCompile(`(?i)\b((?:[a-zA-Z0-9_.-]+[ \t]+)?[a-zA-Z0-9_.-]*authorization)(\s*:\s*)[^\r\n]+`)

	// authSimpleAssignmentRegex matches "Authorization=Basic <token>" or "Authorization=Bearer <token>" and preserves trailing diagnostics
	authSimpleAssignmentRegex = regexp.MustCompile(`(?i)\b((?:[a-zA-Z0-9_.-]+[ \t]+)?[a-zA-Z0-9_.-]*authorization)(\s*=\s*)(?:basic|bearer)\s+[A-Za-z0-9._~+/=-]+`)

	// authComplexAssignmentRegex matches unquoted non-Basic/Bearer authorization assignments (e.g. Digest, AWS4) to line end
	authComplexAssignmentRegex = regexp.MustCompile(`(?i)\b((?:[a-zA-Z0-9_.-]+[ \t]+)?[a-zA-Z0-9_.-]*authorization)(\s*=\s*)[a-zA-Z][^\r\n]*`)

	// cookieColonHeaderRegex matches "Cookie: ..." or "*_cookie: ..." colon headers and redacts the full header value
	cookieColonHeaderRegex = regexp.MustCompile(`(?i)\b((?:[a-zA-Z0-9_.-]+[ \t]+)?[a-zA-Z0-9_.-]*cookie)(\s*:\s*)[^\r\n]+`)

	// cookieAssignmentRegex matches unquoted cookie assignments (e.g. cookie=session=abc; refresh=def) to line end
	cookieAssignmentRegex = regexp.MustCompile(`(?i)\b((?:[a-zA-Z0-9_.-]+[ \t]+)?[a-zA-Z0-9_.-]*cookie)(\s*=\s*)([^"'\r\n][^\r\n]*)`)

	// bearerTokenRegex matches standalone Bearer tokens
	bearerTokenRegex = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)

	// pemPrivateKeyBlockRegex matches 6 types of PEM private key blocks:
	// PRIVATE KEY, ENCRYPTED PRIVATE KEY, RSA PRIVATE KEY, DSA PRIVATE KEY, EC PRIVATE KEY, OPENSSH PRIVATE KEY
	pemPrivateKeyBlockRegex = regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |OPENSSH |ENCRYPTED |DSA )?PRIVATE KEY-----.*?-----END (?:RSA |EC |OPENSSH |ENCRYPTED |DSA )?PRIVATE KEY-----`)

	// Quoted key matchers
	quotedKeyDoubleQuotedRegex       = regexp.MustCompile(`(?i)` + quotedKeyPattern + `(\s*[:=]\s*)"((?:[^"\\]|\\.)*)"`)
	quotedKeySingleQuotedRegex       = regexp.MustCompile(`(?i)` + quotedKeyPattern + `(\s*[:=]\s*)'((?:[^'\\]|\\.)*)'`)
	quotedKeyUnterminatedDoubleRegex = regexp.MustCompile(`(?i)` + quotedKeyPattern + `(\s*[:=]\s*)"((?:[^"\\\r\n]|\\.)*\\?)(\r?\n|$)`)
	quotedKeyUnterminatedSingleRegex = regexp.MustCompile(`(?i)` + quotedKeyPattern + `(\s*[:=]\s*)'((?:[^'\\\r\n]|\\.)*\\?)(\r?\n|$)`)
	quotedKeyUnquotedRegex           = regexp.MustCompile(`(?i)` + quotedKeyPattern + `(\s*[:=]\s*)(\[redacted\]|[^"',\s&}\]\r\n]+)`)

	// Unquoted key matchers
	unquotedKeyDoubleQuotedRegex       = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*[:=]\s*)"((?:[^"\\]|\\.)*)"`)
	unquotedKeySingleQuotedRegex       = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*[:=]\s*)'((?:[^'\\]|\\.)*)'`)
	unquotedKeyUnterminatedDoubleRegex = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*[:=]\s*)"((?:[^"\\\r\n]|\\.)*\\?)(\r?\n|$)`)
	unquotedKeyUnterminatedSingleRegex = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*[:=]\s*)'((?:[^'\\\r\n]|\\.)*\\?)(\r?\n|$)`)
	unquotedKeyUnquotedEqualsRegex     = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*=\s*)(\[redacted\]|[^"',\s&}\]\r\n]+)`)
	unquotedKeyUnquotedColonRegex      = regexp.MustCompile(`(?i)` + unquotedKeyPattern + `(\s*:\s*)(\[redacted\]|[^"',\s&}\]\r\n]+)`)

	// strongTokenRegex matches authentic tokens without false-positiving on normal file identifiers
	// like sk-account-production.json, sk-proj-account-backup1.json, or AIza_account.json.
	strongTokenRegex = regexp.MustCompile(`(?i)\b(sk-proj-[A-Za-z0-9_-]{24,}|sk-ant-[A-Za-z0-9_-]{24,}|sk-[A-Za-z0-9]{24,}|github_pat_[A-Za-z0-9_]{40,}|ghp_[A-Za-z0-9]{30,}|AIza[0-9A-Za-z_-]{30,}|hf_[A-Za-z0-9]{30,}|sess-[A-Za-z0-9_-]{24,}|pk_(?:live|test)_[0-9a-zA-Z]{24,}|pk_[0-9a-zA-Z]{24,}|rk_(?:live|test)_[0-9a-zA-Z]{24,}|rk_[0-9a-zA-Z]{24,}|cpamp_[A-Za-z0-9_-]{32,})\b`)

	// malformedUsageKeyRegex handles usage payload "key" alias in malformed JSON fallback path, supporting unterminated quotes
	malformedUsageKeyRegex = regexp.MustCompile(`(?i)(["']key["']\s*[:=]\s*)(?:"(?:[^"\\\r\n]|\\.)*"|'(?:[^'\\\r\n]|\\.)*'|"(?:[^"\\\r\n]|\\.)*\\?|'(?:[^'\\\r\n]|\\.)*\\?|[^"',\s&}\]\r\n]+)`)
)

var secretKeySuffixes = []string{
	"_api_key",
	"_management_key",
	"_access_token",
	"_refresh_token",
	"_id_token",
	"_auth_token",
	"_session_token",
	"_client_secret",
	"_private_key",
	"_password",
	"_passwd",
	"_secret",
	"_authorization",
	"_cookie",
}

var secretExactKeys = map[string]bool{
	"api_key":            true,
	"apikey":             true,
	"x_api_key":          true,
	"xapi_key":           true,
	"xapikey":            true,
	"management_key":     true,
	"managementkey":      true,
	"cpa_management_key": true,
	"cpamanagementkey":   true,
	"authorization":      true,
	"cookie":             true,
	"set_cookie":         true,
	"access_token":       true,
	"refresh_token":      true,
	"id_token":           true,
	"token":              true,
	"client_secret":      true,
	"clientsecret":       true,
	"private_key":        true,
	"privatekey":         true,
	"secret":             true,
	"password":           true,
	"passwd":             true,
	"auth_token":         true,
	"authtoken":          true,
	"session":            true,
	"session_token":      true,
	"sessiontoken":       true,
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// IsCanonicalSHA256Hex checks whether a string is a canonical 64-character SHA-256 hex string.
func IsCanonicalSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F') {
			continue
		}
		return false
	}
	return true
}

// NormalizeOpaqueHashForPersistence ensures that opaque hash fields (such as SourceHash
// and APIKeyHash) do not leak raw credentials.
// - empty => ""
// - valid 64-char SHA-256 hex => preserve exactly (lowercase or uppercase)
// - any other non-empty value => SHA-256(trimmed), lowercase hex
func NormalizeOpaqueHashForPersistence(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if IsCanonicalSHA256Hex(trimmed) {
		return trimmed
	}
	return sha256Hex(trimmed)
}

// isSecretFieldKey checks whether a field/property name represents a secret.
func isSecretFieldKey(key string) bool {
	normalized := normalizeSecretKey(key)
	if secretExactKeys[normalized] {
		return true
	}
	if strings.HasPrefix(normalized, "secret_") {
		return true
	}
	for _, suffix := range secretKeySuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

// isAuthorizationFieldKey checks whether a field key represents an authorization credential.
func isAuthorizationFieldKey(key string) bool {
	normalized := normalizeSecretKey(key)
	return normalized == "authorization" || strings.HasSuffix(normalized, "_authorization")
}

// isCookieFieldKey checks whether a field key represents a cookie credential.
func isCookieFieldKey(key string) bool {
	normalized := normalizeSecretKey(key)
	return normalized == "cookie" || normalized == "set_cookie" || strings.HasSuffix(normalized, "_cookie")
}

// normalizeSecretKey normalizes a key (handling camelCase, hyphens, and spaces) to snake_case.
func normalizeSecretKey(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return ""
	}
	var builder strings.Builder
	runes := []rune(trimmed)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				if !unicode.IsUpper(prev) && prev != '_' && prev != '-' && prev != ' ' {
					builder.WriteRune('_')
				} else if i+1 < len(runes) && unicode.IsLower(runes[i+1]) && prev != '_' && prev != '-' && prev != ' ' {
					builder.WriteRune('_')
				}
			}
			builder.WriteRune(unicode.ToLower(r))
		} else if r == '-' || r == ' ' {
			builder.WriteRune('_')
		} else {
			builder.WriteRune(r)
		}
	}
	return strings.Trim(builder.String(), "_")
}

func matchQuotedKeyCandidate(sub []string) (string, bool) {
	if len(sub) < 4 {
		return "", false
	}
	if sub[1] != "" && isSecretFieldKey(sub[1]) {
		return sub[1], true
	}
	if sub[2] != "" && isSecretFieldKey(sub[2]) {
		return sub[2], true
	}
	return "", false
}

func matchUnquotedKeyCandidate(sub []string) (nonSecretPrefix string, secretKey string, ok bool) {
	if len(sub) < 2 {
		return "", "", false
	}
	rawKey := sub[1]
	if rawKey == "" {
		return "", "", false
	}
	if isSecretFieldKey(rawKey) {
		return "", rawKey, true
	}
	words := strings.Fields(rawKey)
	if len(words) > 1 {
		for i := 1; i < len(words); i++ {
			candidate := strings.Join(words[i:], " ")
			if isSecretFieldKey(candidate) {
				norm := normalizeSecretKey(candidate)
				if norm == "token" {
					continue
				}
				idx := strings.Index(rawKey, candidate)
				var p string
				if idx > 0 {
					p = rawKey[:idx]
				}
				return p, candidate, true
			}
		}
	}
	return "", "", false
}

func sanitizeQuotedKeyAssignments(input string, re *regexp.Regexp, quote string) string {
	return re.ReplaceAllStringFunc(input, func(m string) string {
		sub := re.FindStringSubmatch(m)
		_, ok := matchQuotedKeyCandidate(sub)
		if !ok {
			return m
		}
		sep := sub[3]
		sepIdx := strings.Index(m, sep)
		if sepIdx < 0 {
			return m
		}
		fullPrefix := m[:sepIdx+len(sep)]
		return fullPrefix + quote + "[redacted]" + quote
	})
}

func sanitizeQuotedKeyUnterminatedAssignments(input string, re *regexp.Regexp, quote string) string {
	return re.ReplaceAllStringFunc(input, func(m string) string {
		sub := re.FindStringSubmatch(m)
		_, ok := matchQuotedKeyCandidate(sub)
		if !ok {
			return m
		}
		sep := sub[3]
		sepIdx := strings.Index(m, sep)
		if sepIdx < 0 {
			return m
		}
		fullPrefix := m[:sepIdx+len(sep)]
		trailing := ""
		if len(sub) > 5 {
			trailing = sub[5]
		}
		return fullPrefix + quote + "[redacted]" + trailing
	})
}

func sanitizeUnquotedKeyAssignments(input string, re *regexp.Regexp, quote string) string {
	return re.ReplaceAllStringFunc(input, func(m string) string {
		sub := re.FindStringSubmatch(m)
		prefix, secretKey, ok := matchUnquotedKeyCandidate(sub)
		if !ok {
			return m
		}
		sep := sub[2]
		sepIdx := strings.Index(m, sep)
		if sepIdx < 0 {
			return m
		}
		fullPrefix := m[:sepIdx+len(sep)]
		if prefix != "" {
			keyIdx := strings.Index(fullPrefix, secretKey)
			if keyIdx > 0 {
				return fullPrefix[:keyIdx] + secretKey + sep + quote + "[redacted]" + quote
			}
		}
		return fullPrefix + quote + "[redacted]" + quote
	})
}

func sanitizeUnquotedKeyUnterminatedAssignments(input string, re *regexp.Regexp, quote string) string {
	return re.ReplaceAllStringFunc(input, func(m string) string {
		sub := re.FindStringSubmatch(m)
		prefix, secretKey, ok := matchUnquotedKeyCandidate(sub)
		if !ok {
			return m
		}
		sep := sub[2]
		sepIdx := strings.Index(m, sep)
		if sepIdx < 0 {
			return m
		}
		fullPrefix := m[:sepIdx+len(sep)]
		trailing := ""
		if len(sub) > 4 {
			trailing = sub[4]
		}
		if prefix != "" {
			keyIdx := strings.Index(fullPrefix, secretKey)
			if keyIdx > 0 {
				return fullPrefix[:keyIdx] + secretKey + sep + quote + "[redacted]" + trailing
			}
		}
		return fullPrefix + quote + "[redacted]" + trailing
	})
}

func containsSecretAssignment(input string) bool {
	// Check quoted key assignments
	for _, re := range []*regexp.Regexp{
		quotedKeyDoubleQuotedRegex,
		quotedKeySingleQuotedRegex,
		quotedKeyUnterminatedDoubleRegex,
		quotedKeyUnterminatedSingleRegex,
		quotedKeyUnquotedRegex,
	} {
		matches := re.FindAllStringSubmatch(input, -1)
		for _, sub := range matches {
			if _, ok := matchQuotedKeyCandidate(sub); ok {
				return true
			}
		}
	}
	// Check unquoted key assignments
	for _, re := range []*regexp.Regexp{
		unquotedKeyDoubleQuotedRegex,
		unquotedKeySingleQuotedRegex,
		unquotedKeyUnterminatedDoubleRegex,
		unquotedKeyUnterminatedSingleRegex,
		unquotedKeyUnquotedEqualsRegex,
		unquotedKeyUnquotedColonRegex,
	} {
		matches := re.FindAllStringSubmatch(input, -1)
		for _, sub := range matches {
			if _, _, ok := matchUnquotedKeyCandidate(sub); ok {
				return true
			}
		}
	}
	return false
}

func sanitizeAuthorizationHeadersAndAssignments(input string) string {
	res := authColonHeaderRegex.ReplaceAllStringFunc(input, func(m string) string {
		sub := authColonHeaderRegex.FindStringSubmatch(m)
		if len(sub) < 3 || !isAuthorizationFieldKey(sub[1]) {
			return m
		}
		val := strings.TrimSpace(m[len(sub[1])+len(sub[2]):])
		if val == "[redacted]" {
			return m
		}
		return sub[1] + sub[2] + "[redacted]"
	})

	res = authSimpleAssignmentRegex.ReplaceAllStringFunc(res, func(m string) string {
		sub := authSimpleAssignmentRegex.FindStringSubmatch(m)
		if len(sub) < 3 || !isAuthorizationFieldKey(sub[1]) {
			return m
		}
		return sub[1] + sub[2] + "[redacted]"
	})

	res = authComplexAssignmentRegex.ReplaceAllStringFunc(res, func(m string) string {
		sub := authComplexAssignmentRegex.FindStringSubmatch(m)
		if len(sub) < 3 || !isAuthorizationFieldKey(sub[1]) {
			return m
		}
		return sub[1] + sub[2] + "[redacted]"
	})
	return res
}

func sanitizeCookieHeadersAndAssignments(input string) string {
	res := cookieColonHeaderRegex.ReplaceAllStringFunc(input, func(m string) string {
		sub := cookieColonHeaderRegex.FindStringSubmatch(m)
		if len(sub) < 3 || !isCookieFieldKey(sub[1]) {
			return m
		}
		val := strings.TrimSpace(m[len(sub[1])+len(sub[2]):])
		if val == "[redacted]" {
			return m
		}
		return sub[1] + sub[2] + "[redacted]"
	})

	res = cookieAssignmentRegex.ReplaceAllStringFunc(res, func(m string) string {
		sub := cookieAssignmentRegex.FindStringSubmatch(m)
		if len(sub) < 4 || !isCookieFieldKey(sub[1]) {
			return m
		}
		val := strings.TrimSpace(sub[3])
		if val == "[redacted]" {
			return m
		}
		return sub[1] + sub[2] + "[redacted]"
	})
	return res
}

func containsAuthorizationHeaderOrAssignment(value string) bool {
	for _, re := range []*regexp.Regexp{authColonHeaderRegex, authSimpleAssignmentRegex, authComplexAssignmentRegex} {
		matches := re.FindAllStringSubmatch(value, -1)
		for _, sub := range matches {
			if len(sub) > 1 && isAuthorizationFieldKey(sub[1]) {
				return true
			}
		}
	}
	return false
}

func containsCookieHeaderOrAssignment(value string) bool {
	for _, re := range []*regexp.Regexp{cookieColonHeaderRegex, cookieAssignmentRegex} {
		matches := re.FindAllStringSubmatch(value, -1)
		for _, sub := range matches {
			if len(sub) > 1 && isCookieFieldKey(sub[1]) {
				return true
			}
		}
	}
	return false
}

// ContainsCredentialToken reports whether value contains an explicit strong token credential.
func ContainsCredentialToken(value string) bool {
	return strongTokenRegex.MatchString(value) || bearerTokenRegex.MatchString(value)
}

// ContainsCredential reports whether value contains credentials (tokens, headers, or secret key-values).
func ContainsCredential(value string) bool {
	return ContainsCredentialToken(value) ||
		containsAuthorizationHeaderOrAssignment(value) ||
		containsCookieHeaderOrAssignment(value) ||
		pemPrivateKeyBlockRegex.MatchString(value) ||
		containsSecretAssignment(value)
}

// SanitizeCredentialText scrubs credentials from text or malformed JSON payloads.
func SanitizeCredentialText(value string) string {
	if value == "" {
		return ""
	}
	res := value
	res = sanitizeAuthorizationHeadersAndAssignments(res)
	res = sanitizeCookieHeadersAndAssignments(res)
	res = pemPrivateKeyBlockRegex.ReplaceAllString(res, `[redacted]`)
	res = bearerTokenRegex.ReplaceAllString(res, `Bearer [redacted]`)

	// Quoted key assignments
	res = sanitizeQuotedKeyAssignments(res, quotedKeyDoubleQuotedRegex, "\"")
	res = sanitizeQuotedKeyAssignments(res, quotedKeySingleQuotedRegex, "'")
	res = sanitizeQuotedKeyUnterminatedAssignments(res, quotedKeyUnterminatedDoubleRegex, "\"")
	res = sanitizeQuotedKeyUnterminatedAssignments(res, quotedKeyUnterminatedSingleRegex, "'")
	res = sanitizeQuotedKeyAssignments(res, quotedKeyUnquotedRegex, "")

	// Unquoted key assignments
	res = sanitizeUnquotedKeyAssignments(res, unquotedKeyDoubleQuotedRegex, "\"")
	res = sanitizeUnquotedKeyAssignments(res, unquotedKeySingleQuotedRegex, "'")
	res = sanitizeUnquotedKeyUnterminatedAssignments(res, unquotedKeyUnterminatedDoubleRegex, "\"")
	res = sanitizeUnquotedKeyUnterminatedAssignments(res, unquotedKeyUnterminatedSingleRegex, "'")
	res = sanitizeUnquotedKeyAssignments(res, unquotedKeyUnquotedEqualsRegex, "")
	res = sanitizeUnquotedKeyAssignments(res, unquotedKeyUnquotedColonRegex, "")

	res = strongTokenRegex.ReplaceAllString(res, `[redacted]`)
	return res
}

func sanitizeMalformedUsageJSONFallback(raw string) string {
	cleaned := malformedUsageKeyRegex.ReplaceAllString(raw, `${1}"[redacted]"`)
	return FailSummaryFromBody(cleaned)
}

// SanitizeJSONForPersistence parses raw JSON (using json.Number for precision) and recursively
// redacts secrets from keys, values, and diagnostic strings.
// It verifies that raw contains exactly one valid JSON value (EOF check).
// If raw is not a complete, valid JSON value, it falls back to sanitizeMalformedUsageJSONFallback(trimmed) (<= 4096 bytes).
func SanitizeJSONForPersistence(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err == nil {
		var trailing any
		if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
			sanitized := sanitizeJSONValueWithContext("", payload, 0)
			out, err := json.Marshal(sanitized)
			if err == nil {
				return string(out)
			}
		}
	}
	return sanitizeMalformedUsageJSONFallback(trimmed)
}

func sanitizeJSONValue(value any) any {
	return sanitizeJSONValueWithContext("", value, 0)
}

func sanitizeJSONValueWithContext(parentKey string, value any, depth int) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, child := range v {
			sanitizedKey := key
			if ContainsCredential(key) {
				sanitizedKey = "[redacted-key:" + sha256Hex(key) + "]"
			}

			normalizedKey := normalizeSecretKey(key)

			// Root-level "key" in usage payload is treated as API key alias
			if depth == 0 && normalizedKey == "key" {
				result[sanitizedKey] = "[redacted]"
				continue
			}

			if isSecretFieldKey(key) {
				result[sanitizedKey] = "[redacted]"
				continue
			}

			if maxBytes, ok := requestMetadataMaxBytes(normalizedKey); ok {
				cleaned := SanitizeCredentialText(stringValue(child))
				result[sanitizedKey] = sanitizeRequestMetadata(cleaned, maxBytes)
				continue
			}

			if normalizedKey == "fail_body" || (parentKey == "fail" && normalizedKey == "body") {
				result[sanitizedKey] = FailSummaryFromBody(stringValue(child))
				continue
			}

			result[sanitizedKey] = sanitizeJSONValueWithContext(normalizedKey, child, depth+1)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, child := range v {
			result[i] = sanitizeJSONValueWithContext(parentKey, child, depth+1)
		}
		return result
	case string:
		return SanitizeCredentialText(v)
	case json.Number, bool, nil:
		return v
	default:
		return v
	}
}

// SanitizeDiagnosticBody sanitizes failure bodies without truncating to 4096 bytes.
// It verifies that body is exactly one complete JSON value.
// If malformed or mixed text, it falls back to SanitizeCredentialText(full input) without truncation.
func SanitizeDiagnosticBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return ""
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err == nil {
		var trailing any
		if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
			sanitized := sanitizeDiagnosticJSONValue(payload)
			if out, err := json.Marshal(sanitized); err == nil {
				return string(out)
			}
		}
	}
	return SanitizeCredentialText(trimmed)
}

func sanitizeDiagnosticJSONValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, child := range v {
			sanitizedKey := key
			if ContainsCredential(key) {
				sanitizedKey = "[redacted-key:" + sha256Hex(key) + "]"
			}

			if isSecretFieldKey(key) {
				result[sanitizedKey] = "[redacted]"
				continue
			}

			result[sanitizedKey] = sanitizeDiagnosticJSONValue(child)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, child := range v {
			result[i] = sanitizeDiagnosticJSONValue(child)
		}
		return result
	case string:
		return SanitizeCredentialText(v)
	case json.Number, bool, nil:
		return v
	default:
		return v
	}
}

// PrepareSensitiveFieldsForPersistence enforces the persistence security boundary on an Event
// before saving it to SQLite. Business and account semantics are strictly preserved.
func PrepareSensitiveFieldsForPersistence(event Event) Event {
	// 1. Normalize opaque hashes first
	event.SourceHash = NormalizeOpaqueHashForPersistence(event.SourceHash)
	event.APIKeyHash = NormalizeOpaqueHashForPersistence(event.APIKeyHash)

	// 2. Correlation pseudonymization for Source
	trimmedSource := strings.TrimSpace(event.Source)
	if trimmedSource != "" {
		sourceSHA := sha256Hex(trimmedSource)
		if ContainsCredential(trimmedSource) || (event.APIKeyHash != "" && strings.EqualFold(sourceSHA, event.APIKeyHash)) {
			event.Source = "h:" + sourceSHA
			event.SourceHash = sourceSHA
		}
	}

	// 3. Diagnostics payloads
	if event.FailBody != "" {
		event.FailBody = SanitizeDiagnosticBody(event.FailBody)
	}

	if event.FailSummary != "" {
		event.FailSummary = FailSummaryFromBody(event.FailSummary)
	} else if event.FailBody != "" {
		event.FailSummary = FailSummaryFromBody(event.FailBody)
	}

	if event.RawJSON != "" {
		event.RawJSON = SanitizeJSONForPersistence(event.RawJSON)
	}

	if event.ResponseMetadataJSON != "" {
		event.ResponseMetadataJSON = SanitizeJSONForPersistence(event.ResponseMetadataJSON)
	}

	if event.ResponseMetadata != nil {
		sanitizeResponseHeaderMetadata(event.ResponseMetadata)
	}

	// 4. Standalone persistence scalar string fields that may carry query/path/diagnostic secrets
	if event.Endpoint != "" {
		event.Endpoint = SanitizeCredentialText(event.Endpoint)
	}
	if event.Path != "" {
		event.Path = SanitizeCredentialText(event.Path)
	}
	if event.ClientIP != "" {
		event.ClientIP = SanitizeCredentialText(event.ClientIP)
	}
	if event.XForwardedFor != "" {
		event.XForwardedFor = SanitizeCredentialText(event.XForwardedFor)
	}
	if event.UserAgent != "" {
		event.UserAgent = SanitizeCredentialText(event.UserAgent)
	}
	if event.HeaderQuotaPlanType != "" {
		event.HeaderQuotaPlanType = SanitizeCredentialText(event.HeaderQuotaPlanType)
	}
	if event.HeaderErrorKind != "" {
		event.HeaderErrorKind = SanitizeCredentialText(event.HeaderErrorKind)
	}
	if event.HeaderErrorCode != "" {
		event.HeaderErrorCode = SanitizeCredentialText(event.HeaderErrorCode)
	}
	if event.HeaderTraceID != "" {
		event.HeaderTraceID = SanitizeCredentialText(event.HeaderTraceID)
	}

	return event
}
