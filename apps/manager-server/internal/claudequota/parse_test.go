package claudequota

import "testing"

func pct(windows []Window, id string) (float64, bool) {
	for _, w := range windows {
		if w.ID == id {
			if w.UsedPercent == nil {
				return 0, false
			}
			return *w.UsedPercent, true
		}
	}
	return 0, false
}

func find(windows []Window, id string) (Window, bool) {
	for _, w := range windows {
		if w.ID == id {
			return w, true
		}
	}
	return Window{}, false
}

func TestParseNamedWindows(t *testing.T) {
	body := []byte(`{
		"five_hour":       {"utilization": 0.28, "resets_at": "2026-10-04T17:15:00Z"},
		"seven_day":       {"utilization": 0.60, "resets_at": "2026-10-10T00:00:00Z"},
		"seven_day_opus":  {"utilization": 0.10, "resets_at": "2026-10-10T00:00:00Z"},
		"seven_day_sonnet":{"utilization": 0.00, "resets_at": "2026-10-10T00:00:00Z"}
	}`)
	windows, err := Parse(body)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(windows) != 4 {
		t.Fatalf("expected 4 windows, got %d: %+v", len(windows), windows)
	}
	// Order is session, weekly, opus, sonnet.
	if windows[0].ID != "claude-five-hour" || windows[1].ID != "claude-seven-day" {
		t.Fatalf("unexpected order: %+v", windows)
	}
	if p, ok := pct(windows, "claude-five-hour"); !ok || p != 28 {
		t.Fatalf("session used%% = %v (ok=%v), want 28", p, ok)
	}
	if p, ok := pct(windows, "claude-seven-day"); !ok || p != 60 {
		t.Fatalf("weekly used%% = %v, want 60", p)
	}
	if p, ok := pct(windows, "claude-seven-day-opus"); !ok || p != 10 {
		t.Fatalf("opus used%% = %v, want 10", p)
	}
	w, _ := find(windows, "claude-five-hour")
	if w.ResetLabel != "2026-10-04T17:15:00Z" {
		t.Fatalf("session reset label = %q", w.ResetLabel)
	}
	if w.ResetAtMS == 0 {
		t.Fatalf("session resetAtMs not resolved")
	}
}

func TestParseLimitsArray(t *testing.T) {
	body := []byte(`{
		"limits": [
			{"name": "5_hour",  "utilization": 0.42, "resets_at": "2026-10-04T18:00:00Z"},
			{"name": "7_day",   "utilization": 0.75, "resets_at": "2026-10-11T00:00:00Z"},
			{"name": "weekly_opus", "utilization": 0.05, "resets_at": "2026-10-11T00:00:00Z"}
		]
	}`)
	windows, err := Parse(body)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if p, ok := pct(windows, "claude-five-hour"); !ok || p != 42 {
		t.Fatalf("session used%% = %v, want 42", p)
	}
	if p, ok := pct(windows, "claude-seven-day"); !ok || p != 75 {
		t.Fatalf("weekly used%% = %v, want 75", p)
	}
	if p, ok := pct(windows, "claude-seven-day-opus"); !ok || p != 5 {
		t.Fatalf("opus used%% = %v, want 5", p)
	}
}

func TestParsePercentKeyNotRescaled(t *testing.T) {
	// An explicit percent key is already 0..100 and must not be multiplied.
	body := []byte(`{"five_hour": {"used_percent": 33, "resets_at": "2026-10-04T18:00:00Z"}}`)
	windows, _ := Parse(body)
	if p, ok := pct(windows, "claude-five-hour"); !ok || p != 33 {
		t.Fatalf("used_percent = %v, want 33", p)
	}
}

func TestParseEpochReset(t *testing.T) {
	body := []byte(`{"five_hour": {"utilization": 0.5, "resets_at": 1760000000}}`)
	windows, _ := Parse(body)
	w, ok := find(windows, "claude-five-hour")
	if !ok || w.ResetAtMS != 1760000000000 {
		t.Fatalf("epoch reset ms = %d, want 1760000000000", w.ResetAtMS)
	}
}

func TestParseClampAndEmpty(t *testing.T) {
	if w, _ := Parse([]byte(`{"five_hour": {"utilization": 1.5}}`)); func() bool {
		p, ok := pct(w, "claude-five-hour")
		return ok && p == 100
	}() == false {
		t.Fatalf("over-100 fraction should clamp to 100")
	}
	if w := ParseMap(nil); len(w) != 0 {
		t.Fatalf("nil map should yield 0 windows")
	}
	if w, _ := Parse([]byte(`{"unrelated": 1}`)); len(w) != 0 {
		t.Fatalf("no recognizable windows should yield 0")
	}
}
