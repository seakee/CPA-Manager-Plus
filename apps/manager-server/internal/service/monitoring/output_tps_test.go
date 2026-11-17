package monitoring

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestAccountOutputTPSWeightedModelsAndNull(t *testing.T) {
	stats := []store.AccountModelStat{
		{AccountSnapshot: "account", Model: "a", Calls: 1, OutputTPSSum: 100, OutputTPSSamples: 1, ServiceTier: "default"},
		{AccountSnapshot: "account", Model: "a", Calls: 1, OutputTPSSum: 10, OutputTPSSamples: 1, ServiceTier: "priority"},
		{AccountSnapshot: "account", Model: "b", Calls: 1, OutputTPSSum: 10, OutputTPSSamples: 1},
		{AccountSnapshot: "account", Model: "invalid", Calls: 5},
	}
	rows := buildAccountStats(stats, nil)
	if len(rows) != 1 || rows[0].AverageOutputTPS == nil || *rows[0].AverageOutputTPS != 40 {
		t.Fatalf("weighted account: %+v", rows)
	}
	for _, model := range rows[0].Models {
		switch model.Model {
		case "a":
			if model.AverageOutputTPS == nil || *model.AverageOutputTPS != 55 {
				t.Fatalf("model a: %+v", model)
			}
		case "b":
			if model.AverageOutputTPS == nil || *model.AverageOutputTPS != 10 {
				t.Fatalf("model b: %+v", model)
			}
		case "invalid":
			if model.AverageOutputTPS != nil {
				t.Fatalf("invalid: %+v", model)
			}
		}
	}
	rows = buildAccountStats([]store.AccountModelStat{{Model: "", Calls: 1}}, nil)
	encoded, err := json.Marshal(rows)
	if err != nil || strings.Count(string(encoded), `"average_output_tps":null`) != 2 {
		t.Fatalf("null JSON: %s %v", encoded, err)
	}
}
