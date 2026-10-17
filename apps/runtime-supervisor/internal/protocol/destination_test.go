package protocol

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
)

type destinationFake struct{ calls int }

func (f *destinationFake) StageDestination(_ context.Context, r lifecycle.DestinationRequest) (journal.Operation, error) {
	f.calls++
	return journal.Operation{OperationID: r.OperationID, OperationType: "stage_destination", RuntimeIdentity: r.ExpectedRuntimeIdentity, RuntimeGeneration: r.ExpectedRuntimeGeneration, State: journal.StateSucceeded}, nil
}

func TestDestinationProtocolIsPrivateBoundedTypedAndSecretFree(t *testing.T) {
	fake := &destinationFake{}
	h, err := NewHandler(Config{RuntimeIdentity: "runtime-01", RuntimeGeneration: 1, Token: "test-runtime-token", Destination: fake})
	if err != nil {
		t.Fatal(err)
	}
	r := lifecycle.DestinationRequest{OperationID: "adopt-destination", ExpectedRuntimeIdentity: "runtime-01", ExpectedRuntimeGeneration: 1, ExpectedActiveArtifactID: artifact.ID("sha256:" + strings.Repeat("a", 64)), SourceIdentity: "external:sha256:" + strings.Repeat("b", 64), SourceClass: "network_existing", APIKeys: []string{"private-client-secret"}, ManagementKey: strings.Repeat("c", 64)}
	body, _ := json.Marshal(r)
	for _, test := range []struct {
		method, token, body string
		want                int
	}{
		{"POST", "", string(body), 401},
		{"GET", "test-runtime-token", string(body), 405},
		{"POST", "test-runtime-token", strings.Replace(string(body), `"operationId":`, `"operationId":"duplicate","operationId":`, 1), 400},
		{"POST", "test-runtime-token", strings.Replace(string(body), `"operationId":`, `"OperationID":`, 1), 400},
		{"POST", "test-runtime-token", strings.TrimSuffix(string(body), "}") + `,"configPath":"/source/config"}`, 400},
		{"POST", "test-runtime-token", string(body) + ` {}`, 400},
		{"POST", "test-runtime-token", string(body), 200},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, destinationPath, strings.NewReader(test.body))
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		h.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatalf("protocol status=%d want=%d", recorder.Code, test.want)
		}
		if strings.Contains(recorder.Body.String(), r.APIKeys[0]) || strings.Contains(recorder.Body.String(), r.ManagementKey) {
			t.Fatal("destination secrets escaped response")
		}
	}
	if fake.calls != 1 {
		t.Fatal("invalid/private request reached executor")
	}
}
