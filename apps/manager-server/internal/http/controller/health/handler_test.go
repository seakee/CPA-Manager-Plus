package health_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/health"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
)

func TestHealthPreservesDefaultTwoFieldContractAndDisclosesSlotState(t *testing.T) {
	origVersion := buildinfo.Version
	t.Cleanup(func() {
		buildinfo.Version = origVersion
		middleware.SetProcessTrialHoldGate(nil)
	})

	h := &health.Handler{ServiceID: "cpa-manager-plus"}

	t.Run("standalone default preserves exact two-field health contract", func(t *testing.T) {
		t.Setenv("CPAMP_BASE_VERSION", "")
		t.Setenv("CPAMP_RUNNING_SLOT", "")
		t.Setenv("CPAMP_TRIAL_HOLD", "")
		middleware.SetProcessTrialHoldGate(nil)

		rec := httptest.NewRecorder()
		h.Health(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 2 || body["ok"] != true || body["service"] != "cpa-manager-plus" {
			t.Fatalf("unexpected bare health body: %s", rec.Body.String())
		}
	})

	t.Run("non-GET rejected with 405", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodHead, http.MethodPut} {
			rec := httptest.NewRecorder()
			h.Health(rec, httptest.NewRequest(method, "/health", nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s /health status = %d, want 405", method, rec.Code)
			}
		}
	})

	t.Run("launcher base-image mode discloses running and base_image_version", func(t *testing.T) {
		buildinfo.Version = "v2.0.0-beta.1"
		t.Setenv("CPAMP_BASE_VERSION", "v2.0.0-beta.1")
		t.Setenv("CPAMP_RUNNING_SLOT", "")
		t.Setenv("CPAMP_TRIAL_HOLD", "")
		middleware.SetProcessTrialHoldGate(nil)

		rec := httptest.NewRecorder()
		h.Health(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["ok"] != true || body["service"] != "cpa-manager-plus" ||
			body["running_version"] != "v2.0.0-beta.1" ||
			body["base_image_version"] != "v2.0.0-beta.1" {
			t.Fatalf("unexpected base-image health body: %s", rec.Body.String())
		}
		if _, hasSlot := body["active_slot"]; hasSlot {
			t.Fatalf("expected active_slot omitted when empty: %s", rec.Body.String())
		}
		if _, hasHold := body["trial_hold"]; hasHold {
			t.Fatalf("expected trial_hold omitted when false: %s", rec.Body.String())
		}
	})

	t.Run("slot trial mode discloses active_slot and live trial_hold release", func(t *testing.T) {
		buildinfo.Version = "v2.0.0-beta.2"
		t.Setenv("CPAMP_BASE_VERSION", "v2.0.0-beta.1")
		t.Setenv("CPAMP_RUNNING_SLOT", "v2.0.0-beta.2")
		t.Setenv("CPAMP_TRIAL_HOLD", "1")

		tokenFile := filepath.Join(t.TempDir(), "launcher-token")
		if err := os.WriteFile(tokenFile, []byte("launcher-secret-token-123\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gate := middleware.NewTrialHoldGate(true, tokenFile)
		middleware.SetProcessTrialHoldGate(gate)

		wrapped := gate.Wrap(http.HandlerFunc(h.Health))

		recBefore := httptest.NewRecorder()
		wrapped.ServeHTTP(recBefore, httptest.NewRequest(http.MethodGet, "/health", nil))
		var before map[string]any
		if err := json.Unmarshal(recBefore.Body.Bytes(), &before); err != nil {
			t.Fatal(err)
		}
		if before["running_version"] != "v2.0.0-beta.2" ||
			before["base_image_version"] != "v2.0.0-beta.1" ||
			before["active_slot"] != "v2.0.0-beta.2" ||
			before["trial_hold"] != true {
			t.Fatalf("unexpected trial health before release: %s", recBefore.Body.String())
		}

		relReq := httptest.NewRequest(http.MethodPost, middleware.TrialReleasePath, nil)
		relReq.RemoteAddr = "127.0.0.1:43210"
		relReq.Header.Set("Authorization", "Bearer launcher-secret-token-123")
		relRec := httptest.NewRecorder()
		wrapped.ServeHTTP(relRec, relReq)
		if relRec.Code != http.StatusOK {
			t.Fatalf("release status = %d: %s", relRec.Code, relRec.Body.String())
		}

		recAfter := httptest.NewRecorder()
		wrapped.ServeHTTP(recAfter, httptest.NewRequest(http.MethodGet, "/health", nil))
		var after map[string]any
		if err := json.Unmarshal(recAfter.Body.Bytes(), &after); err != nil {
			t.Fatal(err)
		}
		if _, hasHold := after["trial_hold"]; hasHold {
			t.Fatalf("expected trial_hold cleared immediately after release: %s", recAfter.Body.String())
		}
		if after["active_slot"] != "v2.0.0-beta.2" || after["base_image_version"] != "v2.0.0-beta.1" {
			t.Fatalf("unexpected health after release: %s", recAfter.Body.String())
		}
	})
}
