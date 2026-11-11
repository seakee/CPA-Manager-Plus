package desktopmonitor

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed widget.html
var widgetHTML string

//go:embed widget.js
var widgetJS string

func widgetDocument() string {
	return strings.Replace(widgetHTML, "/*__WIDGET_SCRIPT__*/", widgetJS, 1)
}

type widgetRequest struct {
	Model     string   `json:"model"`
	Timestamp int64    `json:"timestamp"`
	Failed    bool     `json:"failed"`
	Stream    bool     `json:"stream"`
	Input     int64    `json:"input"`
	Output    int64    `json:"output"`
	Cached    int64    `json:"cached"`
	Total     int64    `json:"total"`
	Speed     *float64 `json:"speed"`
	TTFT      *float64 `json:"ttft"`
	Duration  *float64 `json:"duration"`
	Cost      *float64 `json:"cost"`
}

func projectWidgetRequest(r Request) widgetRequest {
	return widgetRequest{Model: r.Model, Timestamp: r.Timestamp.UnixMilli(), Failed: r.Failed, Stream: r.Stream, Input: r.InputTokens, Output: r.OutputTokens, Cached: r.CachedTokens, Total: r.TotalTokens, Speed: r.TokensPerSecond, TTFT: r.TTFTMS, Duration: r.LatencyMS, Cost: r.Cost}
}

type widgetState struct {
	Lang        string          `json:"lang"`
	Compact     bool            `json:"compact"`
	Editing     bool            `json:"editing"`
	Pinned      bool            `json:"pinned"`
	Remember    bool            `json:"remember"`
	AllowHTTP   bool            `json:"allowHTTP"`
	HasKey      bool            `json:"hasKey"`
	Base        string          `json:"base"`
	Notice      string          `json:"notice"`
	Status      string          `json:"status"`
	Loaded      bool            `json:"loaded"`
	Partial     bool            `json:"partial"`
	UpdatedAt   int64           `json:"updatedAt"`
	Latest      *widgetRequest  `json:"latest"`
	History     []widgetRequest `json:"history"`
	TodayCalls  int64           `json:"todayCalls"`
	TodayTokens int64           `json:"todayTokens"`
	TodayCost   *float64        `json:"todayCost"`
}

func snapshotWidgetState(state ViewState) widgetState {
	s := state.Snapshot
	v := widgetState{Status: state.Status, Partial: s.Partial, TodayCalls: s.Today.Calls, TodayTokens: s.Today.Tokens, TodayCost: s.Today.Cost}
	if !s.UpdatedAt.IsZero() {
		v.Loaded = true
		v.UpdatedAt = s.UpdatedAt.UnixMilli()
	}
	for i, r := range s.Requests {
		p := projectWidgetRequest(r)
		if i == 0 {
			v.Latest = &p
		} else if i < 3 {
			v.History = append(v.History, p)
		}
	}
	return v
}

// A blank password may retain the current key only for the exact same Manager.
// Changing URL must never forward another connection's credential.
func keyForWidgetConnection(current *Client, base, supplied string, keep, allowHTTP bool) string {
	if supplied != "" {
		return supplied
	}
	if !keep || current == nil {
		return ""
	}
	normalized, err := normalizeBaseURLWith(base, allowHTTP)
	if err == nil && normalized == current.base {
		return current.key
	}
	return ""
}

func widgetUpdateScript(v widgetState) string {
	b, _ := json.Marshal(v)
	return "window.applyMonitorState(" + string(b) + ");"
}
