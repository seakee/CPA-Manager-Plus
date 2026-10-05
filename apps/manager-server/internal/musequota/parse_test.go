package musequota

import "testing"

func find(windows []Window, id string) (Window, bool) {
	for _, w := range windows {
		if w.ID == id {
			return w, true
		}
	}
	return Window{}, false
}

func TestParseWindowAndWeekly(t *testing.T) {
	body := []byte(`{
		"subs_tier_name": "Muse Code",
		"is_subs_active": true,
		"subs_usage": {
			"tier": "pro",
			"window": {"used_percent": 38, "resets_at": "2026-10-04T17:00:00Z", "window_duration_mins": 300},
			"weekly": {"used_percent": 62, "resets_at": "2026-10-10T00:00:00Z"}
		}
	}`)
	windows, err := Parse(body)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("expected 2 windows, got %d: %+v", len(windows), windows)
	}
	w, ok := find(windows, "muse-window")
	if !ok || w.UsedPercent == nil || *w.UsedPercent != 38 {
		t.Fatalf("window used%% = %+v, want 38", w.UsedPercent)
	}
	if w.LimitWindowSeconds == nil || *w.LimitWindowSeconds != 18000 {
		t.Fatalf("window duration = %+v, want 18000s", w.LimitWindowSeconds)
	}
	if w.ResetAtMS == 0 {
		t.Fatalf("window resetAtMs not resolved")
	}
	wk, ok := find(windows, "muse-weekly")
	if !ok || wk.UsedPercent == nil || *wk.UsedPercent != 62 {
		t.Fatalf("weekly used%% = %+v, want 62", wk.UsedPercent)
	}
	if wk.LimitWindowSeconds != nil {
		t.Fatalf("weekly must not assume a duration")
	}
}

func TestParseEpochReset(t *testing.T) {
	body := []byte(`{"subs_usage": {"window": {"used_percent": 10, "resets_at": 1760000000}}}`)
	windows, _ := Parse(body)
	w, ok := find(windows, "muse-window")
	if !ok || w.ResetAtMS != 1760000000000 {
		t.Fatalf("epoch reset ms = %d, want 1760000000000", w.ResetAtMS)
	}
}

func TestParseClampAndEmpty(t *testing.T) {
	if w := ParseMap(nil); len(w) != 0 {
		t.Fatalf("nil map should yield 0 windows")
	}
	if w, _ := Parse([]byte(`{"subs_tier_name":"x"}`)); len(w) != 0 {
		t.Fatalf("no subs_usage should yield 0 windows")
	}
	windows, _ := Parse([]byte(`{"subs_usage":{"window":{"used_percent":150,"resets_at":"2026-10-04T17:00:00Z"}}}`))
	w, _ := find(windows, "muse-window")
	if w.UsedPercent == nil || *w.UsedPercent != 100 {
		t.Fatalf("over-100 percent should clamp to 100, got %+v", w.UsedPercent)
	}
}
