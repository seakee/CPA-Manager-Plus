package desktopmonitor

import (
	"strings"
	"testing"
	"time"
)

func TestWidgetStateContainsOnlyTelemetryAndNoCredential(t *testing.T) {
	s := ViewState{Status: StatusConnected, Snapshot: Snapshot{UpdatedAt: time.UnixMilli(1700000000000), Today: DaySummary{Calls: 7, Tokens: 4321}, Requests: []Request{{Model: "</script><img src=x>", InputTokens: 10, OutputTokens: 2, CachedTokens: 3, TotalTokens: 12}}}}
	v := snapshotWidgetState(s)
	script := widgetUpdateScript(v)
	if !v.Loaded || v.TodayTokens != 4321 || v.Latest.Total != 12 {
		t.Fatal("snapshot projection lost telemetry")
	}
	if strings.Contains(script, "</script>") || strings.Contains(script, "<img") {
		t.Fatal("telemetry became executable markup")
	}
	if strings.Contains(script, "managementKey") || strings.Contains(script, "Authorization") {
		t.Fatal("credential-bearing state exposed to renderer")
	}
}

func TestRetainKeyOnlyForSameManager(t *testing.T) {
	c, err := NewClient("https://first.example/cpamp", "saved-key")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := keyForWidgetConnection(c, "https://first.example/cpamp/", "", true, false); got != "saved-key" {
		t.Fatal("same Manager did not retain key")
	}
	for _, other := range []string{"https://second.example/cpamp", "https://first.example:444/cpamp", "https://first.example/another", "http://first.example/cpamp"} {
		if got := keyForWidgetConnection(c, other, "", true, true); got != "" {
			t.Fatalf("credential crossed connection scope: %s", other)
		}
	}
	if got := keyForWidgetConnection(c, "https://second.example/cpamp", "explicit-new-key", false, false); got != "explicit-new-key" {
		t.Fatal("explicit new credential was not used")
	}
}
