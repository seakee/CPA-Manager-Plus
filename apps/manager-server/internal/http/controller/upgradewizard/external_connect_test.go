package upgradewizard

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// TestNextActionForAnExternalConnectRefusal proves the projection offers the step
// that resolves a stopped External connect instead of the reconciliation that
// follows it, and that every other recorded refusal keeps the stage table.
func TestNextActionForAnExternalConnectRefusal(t *testing.T) {
	cases := []struct {
		name      string
		stage     string
		errorCode string
		want      string
	}{
		{
			name:      "v7 cpa",
			stage:     StageCPAImport,
			errorCode: "external_cpa_version_unsupported",
			want:      ActionExternalConnect,
		},
		{
			name:      "rejected key",
			stage:     StageCPAImport,
			errorCode: "external_management_key_rejected",
			want:      ActionExternalConnect,
		},
		{
			name:      "moved target",
			stage:     StageCPAImport,
			errorCode: "external_target_moved",
			want:      ActionExternalConnect,
		},
		{
			name:      "unrelated refusal keeps the stage table",
			stage:     StageCPAImport,
			errorCode: "upgrade_reconcile_blocked",
			want:      ActionReconcile,
		},
		{
			name:      "the same code at another stage keeps the stage table",
			stage:     StageReconcile,
			errorCode: "external_cpa_version_unsupported",
			want:      ActionReconcile,
		},
		{
			name:  "no refusal",
			stage: StageCPAImport,
			want:  ActionReconcile,
		},
		{
			name:  "an unrecorded operation",
			stage: "",
			want:  ActionPreflight,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := nextActionForRecordedStage(testCase.stage, testCase.errorCode); got != testCase.want {
				t.Fatalf("nextActionForRecordedStage(%q, %q) = %q, want %q",
					testCase.stage, testCase.errorCode, got, testCase.want)
			}
		})
	}
}

// TestStatusProjectsAStoppedExternalConnect reads the whole route: a row parked at
// cpa_import by the External connect step must reach the browser with its code,
// its detail and the step that resolves it.
func TestStatusProjectsAStoppedExternalConnect(t *testing.T) {
	database := managerStoreWithOperation(t, testAdminKey, model.UpgradeOperation{
		Stage:          model.UpgradeStageCPAImport,
		DomainsDone:    []string{model.UpgradeDomainSourceCopy},
		SourceKind:     "upgraded-legacy",
		ErrorCode:      "external_cpa_version_unsupported",
		ErrorDetail:    "the CPA answered version v7.2.1, which is not the v8 management contract this Manager writes",
		ManualItems:    []string{},
		Reconciliation: json.RawMessage(`{}`),
	})
	handler := &Handler{
		LegacyRoot: func() string { return "" },
		Database:   func() string { return database },
	}
	_, body := request(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
	var status statusDocument
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("decode status: %v; body: %s", err, body)
	}
	if !status.Recorded || status.Stage != StageCPAImport {
		t.Fatalf("status = %+v", status)
	}
	if status.NextAction != ActionExternalConnect {
		t.Fatalf("next action = %q, want %q", status.NextAction, ActionExternalConnect)
	}
	if status.ErrorCode != "external_cpa_version_unsupported" {
		t.Fatalf("error code = %q", status.ErrorCode)
	}
	if status.ErrorDetail == "" {
		t.Fatalf("the refusal detail was dropped: %+v", status)
	}
}
