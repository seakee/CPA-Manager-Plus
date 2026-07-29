package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestServerUsageSnapshotProtocolRequiresAdminAndPreservesLegacyRoutes(t *testing.T) {
	cpa := testutil.NewCPAMock(t)
	setup := &store.Setup{CPAUpstreamURL: cpa.URL(), ManagementKey: "management-key", Queue: "usage", PopSide: "right"}
	handler, db := newCompatHandler(t, testutil.NewConfig(t), setup)

	first := compatEvent("same-shape-a", 10)
	second := compatEvent("same-shape-b", 10)
	first.RequestID = "request-a"
	second.RequestID = "request-b"
	first.RawJSON = `{"authorization":"must-not-leak"}`
	first.FailBody = "Bearer must-not-leak"
	if _, err := db.InsertEvents(context.Background(), []usage.Event{first, second}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	unauthorized := testutil.Request(t, handler, http.MethodGet, "/v1/management/usage/events?limit=1", "", "")
	testutil.RequireStatus(t, unauthorized, http.StatusUnauthorized)
	cpaKey := testutil.Request(t, handler, http.MethodGet, "/v1/management/usage/events?limit=1", "", "management-key")
	testutil.RequireStatus(t, cpaKey, http.StatusUnauthorized)

	firstRR := testutil.Request(t, handler, http.MethodGet, "/v1/management/usage/events?limit=1", "", testutil.AdminKey)
	testutil.RequireStatus(t, firstRR, http.StatusOK)
	var page struct {
		ProtocolVersion int    `json:"protocol_version"`
		SnapshotID      string `json:"snapshot_id"`
		MaxEventID      int64  `json:"max_event_id"`
		RowCount        int64  `json:"row_count"`
		Digest          string `json:"digest"`
		DigestAlgorithm string `json:"digest_algorithm"`
		Complete        bool   `json:"complete"`
		NextCursor      string `json:"next_cursor"`
		Events          []struct {
			EventID   int64  `json:"event_id"`
			RequestID string `json:"request_id"`
			EventHash string `json:"event_hash"`
		} `json:"events"`
	}
	testutil.DecodeJSON(t, firstRR, &page)
	if page.ProtocolVersion != 1 || page.SnapshotID == "" || page.MaxEventID != 2 || page.RowCount != 2 ||
		page.Digest == "" || page.DigestAlgorithm != "sha256-chain:event-id-record-digest:v1" || page.Complete ||
		page.NextCursor == "" || len(page.Events) != 1 || page.Events[0].EventID == 0 ||
		page.Events[0].RequestID != "request-a" || page.Events[0].EventHash != "same-shape-a" {
		t.Fatalf("snapshot page = %#v", page)
	}
	if strings.Contains(firstRR.Body.String(), "must-not-leak") {
		t.Fatalf("snapshot exposed sensitive fields: %s", firstRR.Body.String())
	}

	badCursor := page.NextCursor[:len(page.NextCursor)-1] + "x"
	tamperedRR := testutil.Request(
		t,
		handler,
		http.MethodGet,
		"/v1/management/usage/events?snapshot_id="+page.SnapshotID+"&cursor="+badCursor+"&limit=1",
		"",
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, tamperedRR, http.StatusBadRequest)
	if !strings.Contains(tamperedRR.Body.String(), `"code":"usage_snapshot_invalid_cursor"`) {
		t.Fatalf("tampered cursor body = %s", tamperedRR.Body.String())
	}

	legacyUsage := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage", "", testutil.AdminKey)
	testutil.RequireStatus(t, legacyUsage, http.StatusOK)
	if !strings.Contains(legacyUsage.Body.String(), `"total_requests":2`) || strings.Contains(legacyUsage.Body.String(), `"event_id"`) {
		t.Fatalf("legacy usage body changed incompatibly: %s", legacyUsage.Body.String())
	}
	legacyExport := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/export", "", testutil.AdminKey)
	testutil.RequireStatus(t, legacyExport, http.StatusOK)
	if !strings.Contains(legacyExport.Body.String(), `"event_hash":"same-shape-a"`) ||
		strings.Contains(legacyExport.Body.String(), `"event_id"`) {
		t.Fatalf("legacy export body changed incompatibly: %s", legacyExport.Body.String())
	}
}
