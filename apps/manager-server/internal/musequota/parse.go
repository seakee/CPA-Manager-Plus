// Package musequota parses the Meta/Muse subscription usage response
// (POST https://api.meta.ai/muse-code/key) into quota windows: the current
// (5-hour) window and the weekly window. It mirrors the web's metaQuota.ts
// parser and is decoupled from the manager model so it can be unit tested
// without any network or CLIProxyAPI dependency.
package musequota

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

// Window is a provider-neutral quota window lifted from the usage response.
// The probe maps these onto model.CodexInspectionQuotaWindow for storage and
// display, using the existing web meta_quota.* label keys.
type Window struct {
	ID                 string
	LabelKey           string
	UsedPercent        *float64 // 0..100
	ResetLabel         string
	ResetAtMS          int64
	LimitWindowSeconds *float64
}

// Parse decodes the raw usage body and returns its quota windows.
func Parse(body []byte) ([]Window, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	return ParseMap(root), nil
}

// ParseMap extracts windows from an already-decoded usage object. It reads only
// subs_usage.window and subs_usage.weekly, matching the web's strict whitelist.
func ParseMap(root map[string]any) []Window {
	windows := make([]Window, 0, 2)
	if root == nil {
		return windows
	}
	usage, ok := root["subs_usage"].(map[string]any)
	if !ok {
		return windows
	}
	if w, ok := usage["window"].(map[string]any); ok {
		if window, ok := windowFrom("muse-window", "meta_quota.window", w, true); ok {
			windows = append(windows, window)
		}
	}
	if w, ok := usage["weekly"].(map[string]any); ok {
		if window, ok := windowFrom("muse-weekly", "meta_quota.weekly", w, false); ok {
			windows = append(windows, window)
		}
	}
	return windows
}

func windowFrom(id, labelKey string, obj map[string]any, readDuration bool) (Window, bool) {
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
	if readDuration {
		if d, ok := durationSeconds(obj); ok {
			w.LimitWindowSeconds = &d
		}
	}
	return w, has
}

// usedPercent reads used_percent, which Muse already reports as a 0..100 percent.
func usedPercent(obj map[string]any) (float64, bool) {
	if raw, ok := obj["used_percent"]; ok {
		if v, ok := toFloat(raw); ok {
			return clampPercent(roundPercent(v)), true
		}
	}
	return 0, false
}

func resetAt(obj map[string]any) (string, int64, bool) {
	raw, ok := obj["resets_at"]
	if !ok {
		return "", 0, false
	}
	switch typed := raw.(type) {
	case string:
		s := strings.TrimSpace(typed)
		if s == "" {
			return "", 0, false
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return s, t.UnixMilli(), true
		}
		return s, 0, true
	case float64:
		// Muse returns unix seconds unless the value is already in milliseconds.
		ms := int64(typed * 1000)
		if typed > 1e12 {
			ms = int64(typed)
		}
		return time.UnixMilli(ms).UTC().Format(time.RFC3339), ms, true
	}
	return "", 0, false
}

// durationSeconds reads window_duration_mins (the 5-hour window length).
func durationSeconds(obj map[string]any) (float64, bool) {
	if raw, ok := obj["window_duration_mins"]; ok {
		if v, ok := toFloat(raw); ok && v > 0 {
			return v * 60, true
		}
	}
	return 0, false
}

func roundPercent(v float64) float64 {
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
