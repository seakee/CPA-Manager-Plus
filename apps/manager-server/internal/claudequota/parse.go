// Package claudequota parses the Anthropic subscription usage response
// (GET https://api.anthropic.com/api/oauth/usage, header
// `anthropic-beta: oauth-2025-04-20`) into quota windows: the 5-hour session
// window, the 7-day weekly window, and per-model weeklies (Opus/Sonnet). It is
// deliberately decoupled from the manager's model package so it can be unit
// tested without any network or CLIProxyAPI dependency.
package claudequota

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

// Window is a provider-neutral quota window lifted from the usage response.
// The probe maps these onto model.CodexInspectionQuotaWindow for storage and
// display, which already renders Codex/xAI windows the same way.
type Window struct {
	ID          string
	LabelKey    string
	UsedPercent *float64 // 0..100
	ResetLabel  string   // original timestamp string, as returned
	ResetAtMS   int64    // epoch milliseconds when resolvable, else 0
}

type windowSpec struct {
	key      string
	id       string
	labelKey string
}

// namedWindows are the top-level objects the usage endpoint returns, in display
// order: session first, then overall weekly, then per-model weeklies.
// IDs and label keys match the existing web Claude quota presentation
// (apps/web/src/i18n/locales/*.json claude_quota.* and the accounts quota card).
var namedWindows = []windowSpec{
	{"five_hour", "claude-five-hour", "claude_quota.five_hour"},
	{"seven_day", "claude-seven-day", "claude_quota.seven_day"},
	{"seven_day_opus", "claude-seven-day-opus", "claude_quota.seven_day_opus"},
	{"seven_day_sonnet", "claude-seven-day-sonnet", "claude_quota.seven_day_sonnet"},
	{"seven_day_cowork", "claude-seven-day-cowork", "claude_quota.seven_day_cowork"},
	{"seven_day_oauth_apps", "claude-seven-day-oauth-apps", "claude_quota.seven_day_oauth_apps"},
}

// Parse decodes the raw usage body and returns its quota windows.
func Parse(body []byte) ([]Window, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	return ParseMap(root), nil
}

// ParseMap extracts windows from an already-decoded usage object. Returns an
// empty slice (never nil) when no recognizable windows are present.
func ParseMap(root map[string]any) []Window {
	windows := make([]Window, 0, len(namedWindows))
	if root == nil {
		return windows
	}
	seen := make(map[string]bool)
	for _, spec := range namedWindows {
		obj, ok := root[spec.key].(map[string]any)
		if !ok {
			continue
		}
		if w, ok := windowFrom(spec.id, spec.labelKey, obj); ok {
			windows = append(windows, w)
			seen[spec.id] = true
		}
	}
	// Some responses carry a limits[] array instead of / in addition to the
	// named objects: { "name"|"window": <id>, "utilization": .., "resets_at": .. }.
	if arr, ok := root["limits"].([]any); ok {
		for _, raw := range arr {
			obj, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id, labelKey := classifyLimit(firstString(obj, "name", "window", "type", "id"))
			if id == "" || seen[id] {
				continue
			}
			if w, ok := windowFrom(id, labelKey, obj); ok {
				windows = append(windows, w)
				seen[id] = true
			}
		}
	}
	return windows
}

func windowFrom(id, labelKey string, obj map[string]any) (Window, bool) {
	w := Window{ID: id, LabelKey: labelKey}
	has := false
	if p, ok := usedPercent(obj); ok {
		w.UsedPercent = &p
		has = true
	}
	if label, ms, ok := resetAt(obj); ok {
		w.ResetLabel = label
		w.ResetAtMS = ms
		has = true
	}
	return w, has
}

// usedPercent normalizes the used amount to a 0..100 percentage. Anthropic's
// `utilization` is a fraction in [0,1]; the explicit percent keys are already
// scaled. NOTE: the `utilization` scale is not contractually documented, so
// this fraction assumption must be confirmed against a live response.
func usedPercent(obj map[string]any) (float64, bool) {
	if raw, ok := obj["utilization"]; ok {
		if v, ok := toFloat(raw); ok {
			return clampPercent(roundPercent(v * 100)), true
		}
	}
	for _, key := range []string{"used_percent", "usedPercent", "percent_used"} {
		if raw, ok := obj[key]; ok {
			if v, ok := toFloat(raw); ok {
				return clampPercent(roundPercent(v)), true
			}
		}
	}
	return 0, false
}

func resetAt(obj map[string]any) (string, int64, bool) {
	for _, key := range []string{"resets_at", "reset_at", "resetsAt"} {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		switch typed := raw.(type) {
		case string:
			s := strings.TrimSpace(typed)
			if s == "" {
				continue
			}
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return s, t.UnixMilli(), true
			}
			return s, 0, true
		case float64:
			// Epoch seconds, unless the value is already in milliseconds.
			ms := int64(typed * 1000)
			if typed > 1e12 {
				ms = int64(typed)
			}
			iso := time.UnixMilli(ms).UTC().Format(time.RFC3339)
			return iso, ms, true
		}
	}
	return "", 0, false
}

func classifyLimit(name string) (string, string) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case n == "":
		return "", ""
	case strings.Contains(n, "opus"):
		return "claude-seven-day-opus", "claude_quota.seven_day_opus"
	case strings.Contains(n, "sonnet"):
		return "claude-seven-day-sonnet", "claude_quota.seven_day_sonnet"
	case strings.Contains(n, "cowork"):
		return "claude-seven-day-cowork", "claude_quota.seven_day_cowork"
	case strings.Contains(n, "oauth"):
		return "claude-seven-day-oauth-apps", "claude_quota.seven_day_oauth_apps"
	case strings.Contains(n, "five") || strings.Contains(n, "5h") || strings.Contains(n, "5_hour") || strings.Contains(n, "session"):
		return "claude-five-hour", "claude_quota.five_hour"
	case strings.Contains(n, "seven") || strings.Contains(n, "7d") || strings.Contains(n, "7_day") || strings.Contains(n, "7day") || strings.Contains(n, "week") || strings.Contains(n, "day"):
		return "claude-seven-day", "claude_quota.seven_day"
	default:
		return "", ""
	}
}

func firstString(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := obj[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func roundPercent(v float64) float64 {
	// Trim floating-point noise (0.28*100 -> 28.000000000000004) while keeping
	// sub-percent precision for display.
	return math.Round(v*1e4) / 1e4
}

func clampPercent(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func toFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}
