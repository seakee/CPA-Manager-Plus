package protocol

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
)

func TestConfiguredStartIsAdvertisedExactly(t *testing.T) {
	h := newStartTestHandler(t, func(context.Context, lifecycle.StartRequest) (journal.Operation, error) {
		return journal.Operation{}, nil
	})
	for _, path := range []string{handshakePath, statusPath} {
		response := request(t, h, http.MethodGet, path, testRuntimeToken)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
		}
		var got map[string]any
		decodeResponse(t, response, &got)
		if !reflect.DeepEqual(got["capabilities"], []any{startCapability}) {
			t.Fatalf("GET %s capabilities=%#v", path, got["capabilities"])
		}
	}
}
