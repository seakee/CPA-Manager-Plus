package upgradewizard

import (
	"encoding/json"
	"net/http"
	"testing"

	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgradeworker"
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
			// The CPA answered, but not with a v8 document. The step recorded its
			// own code, and the step that resolves it is still the connect step:
			// nothing about the upgrade advanced.
			name:      "a document that is not the v8 contract",
			stage:     StageCPAImport,
			errorCode: "external_cpa_config_document_invalid",
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
			// The code is not this step's, so the stage table decides: an import
			// recorded without the cpa_config domain offers the import again.
			name:      "unrelated refusal keeps the stage table",
			stage:     StageCPAImport,
			errorCode: "upgrade_reconcile_blocked",
			want:      ActionCPAImport,
		},
		{
			// The step runs from the copy-in of a second legacy volume as well as
			// from the CPA import, so a stop recorded there is the same standing
			// stop and must not offer the reconciliation that already finished.
			name:      "v7 cpa after copy-in",
			stage:     StageCopyIn,
			errorCode: "external_cpa_version_unsupported",
			want:      ActionExternalConnect,
		},
		{
			name:      "rejected key after reconciliation",
			stage:     StageReconcile,
			errorCode: "external_management_key_rejected",
			want:      ActionExternalConnect,
		},
		{
			name:      "moved target after reconciliation",
			stage:     StageReconcile,
			errorCode: "external_target_moved",
			want:      ActionExternalConnect,
		},
		{
			name:      "rejected key on failed stage",
			stage:     StageFailed,
			errorCode: "external_management_key_rejected",
			want:      ActionExternalConnect,
		},
		{
			name:      "v7 cpa on failed stage",
			stage:     StageFailed,
			errorCode: "external_cpa_version_unsupported",
			want:      ActionExternalConnect,
		},
		{
			name:      "moved target on failed stage",
			stage:     StageFailed,
			errorCode: "external_target_moved",
			want:      ActionExternalConnect,
		},
		{
			name:      "non-external refusal on failed stage keeps reconcile",
			stage:     StageFailed,
			errorCode: "upgrade_reconcile_blocked",
			want:      ActionReconcile,
		},
		{
			// A stage the step cannot run from keeps the stage table: the code
			// there describes something other than this step's refusal, and a
			// recorded verify leaves only the completion of the operation.
			name:      "the same code at a stage the step cannot run from",
			stage:     StageVerify,
			errorCode: "external_cpa_version_unsupported",
			want:      ActionComplete,
		},
		{
			name:  "no refusal",
			stage: StageCPAImport,
			want:  ActionCPAImport,
		},
		{
			name:  "an unrecorded operation",
			stage: "",
			want:  ActionPreflight,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := nextActionForRecordedStage(testCase.stage, testCase.errorCode, nil); got != testCase.want {
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

// TestStatusProjectsAStoppedExternalConnectFromEveryEntryStage reads the route
// from each stage the step legitimately runs from. The step never advances the
// row, so a refusal recorded after the copy-in of a second legacy volume or after
// reconciliation sits on that stage; the wizard must still offer the step that
// resolves it instead of a step the operator already finished.
func TestStatusProjectsAStoppedExternalConnectFromEveryEntryStage(t *testing.T) {
	for _, stage := range []model.UpgradeStage{
		model.UpgradeStageCopyIn,
		model.UpgradeStageReconcile,
		model.UpgradeStageCPAImport,
	} {
		t.Run(string(stage), func(t *testing.T) {
			database := managerStoreWithOperation(t, testAdminKey, model.UpgradeOperation{
				Stage:          stage,
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
			if !status.Recorded || status.Stage != string(stage) {
				t.Fatalf("status = %+v", status)
			}
			if status.NextAction != ActionExternalConnect {
				t.Fatalf("next action at %q = %q, want %q", stage, status.NextAction, ActionExternalConnect)
			}
			if status.ErrorCode != "external_cpa_version_unsupported" {
				t.Fatalf("error code = %q", status.ErrorCode)
			}
		})
	}
}

func TestStatusAfterWorkerRestartOnStageFailedExternalRefusal(t *testing.T) {
	database := managerStoreWithOperation(t, testAdminKey, model.UpgradeOperation{
		Stage:            model.UpgradeStageFailed,
		LastForwardStage: model.UpgradeStageReconcile,
		DomainsDone:      []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainIdentity},
		SourceKind:       "upgraded-legacy",
		ErrorCode:        "external_management_key_rejected",
		ErrorDetail:      "management key rejected by external CPA",
		ManualItems:      []string{},
		Reconciliation:   json.RawMessage(`{}`),
	})

	// 1. Live worker execution state on StageFailed with external-connect refusal.
	for _, code := range []string{
		"external_management_key_rejected",
		string(upgradeservice.UpgradeErrorExternalAuthorityUnavailable),
	} {
		t.Run("live_worker_"+code, func(t *testing.T) {
			h := &Handler{
				LegacyRoot: func() string { return "" },
				Database:   func() string { return database },
				Execution: func() upgradeworker.Execution {
					return upgradeworker.Execution{
						Action: "external-connect",
						Code:   code,
					}
				},
			}
			_, body := request(t, h, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
			var status statusDocument
			if err := json.Unmarshal([]byte(body), &status); err != nil {
				t.Fatalf("decode status: %v; body: %s", err, body)
			}
			if status.Stage != StageFailed || status.NextAction != ActionExternalConnect {
				t.Fatalf("live status = %+v, want stage=%q nextAction=%q", status, StageFailed, ActionExternalConnect)
			}
		})
	}

	// 2. Worker restart / DB fallback (nil or idle Execution) must keep nextAction=external_connect.
	for _, tc := range []struct {
		name string
		exec func() upgradeworker.Execution
	}{
		{name: "db_fallback_nil_execution", exec: nil},
		{name: "restarted_worker_idle_execution", exec: func() upgradeworker.Execution { return upgradeworker.Execution{Available: true} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{
				LegacyRoot: func() string { return "" },
				Database:   func() string { return database },
				Execution:  tc.exec,
				Timeout:    5 * time.Second,
			}
			_, body := request(t, h, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), http.StatusOK)
			var status statusDocument
			if err := json.Unmarshal([]byte(body), &status); err != nil {
				t.Fatalf("decode status: %v; body: %s", err, body)
			}
			if status.Stage != StageFailed || status.NextAction != ActionExternalConnect {
				t.Fatalf("%s status = %+v, want stage=%q nextAction=%q", tc.name, status, StageFailed, ActionExternalConnect)
			}
			if status.ErrorCode != "external_management_key_rejected" {
				t.Fatalf("errorCode = %q, want external_management_key_rejected", status.ErrorCode)
			}
		})
	}
}
