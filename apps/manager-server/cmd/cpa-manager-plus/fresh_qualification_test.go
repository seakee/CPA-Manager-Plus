package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func TestFreshProductionQualificationCannotBeExtendedByHTTPOrRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: "Cpamp_Qualification_Admin_Secret_839216"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if opened != nil {
			_ = opened.Close()
		}
	}()
	var sourceCalls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
	}))
	defer source.Close()
	app := newFreshApplication(opened, config.Config{}, nil)
	handoff, err := app.authority.Issue(ctx)
	if err != nil || !handoff.Resume {
		t.Fatalf("host resume: %v", err)
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handoff.Fragment, "#handoff="))
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatal(err)
	}
	before, err := app.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for restart := 0; restart < 2; restart++ {
		for _, item := range []struct {
			strategy bootstrap.CPAStrategy
			class    bootstrap.AdoptSourceClass
			blocker  string
		}{
			{bootstrap.NewManaged, "", "managed_target_staging_required"},
			{bootstrap.ConnectExternal, "", "management_safe_read_unavailable"},
			{bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting, "adoption_destination_staging_required"},
			{bootstrap.AdoptExisting, bootstrap.AdoptLegacyManaged, "adoption_destination_staging_required"},
		} {
			body := map[string]any{
				"identity": before.Operation.Identity, "expectedRevision": before.Operation.Revision,
				"strategy": item.strategy, "adoptSourceClass": item.class,
				"attemptId": strings.Repeat("a", 32),
				"baseUrl":   "http://127.0.0.1:1", "managementKey": "private-source-secret",
				"expectedRuntimeIdentity": "claimed-runtime", "expectedRuntimeGeneration": 7,
				"expectedArtifactId": "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7",
			}
			for _, injected := range []string{"", "qualification", "qualifiedArtifactId", "adoptionCiphertext", "adoptionResources"} {
				if injected != "" {
					// Keep a reachable source behind malformed HTTP input so the
					// no-source-request assertion detects an authority bypass.
					body["baseUrl"] = source.URL
					body[injected] = map[string]any{"ManagementGeneration": "v8", "ArtifactQualified": true, "RequiredProfileQualified": true, "StagingV8TransitionQualified": true}
				}
				encoded, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodPost, bootstrap.FixedBootstrapAPIPath+"strategy", bytes.NewReader(encoded))
				request.Header.Set("Authorization", "Fresh "+session.Token)
				response := httptest.NewRecorder()
				app.fixedStrategy(response, request)
				if injected != "" {
					if response.Code != http.StatusBadRequest {
						t.Fatalf("HTTP %s accepted: %d", injected, response.Code)
					}
					delete(body, injected)
				} else if item.strategy == bootstrap.AdoptExisting {
					// A claim-authenticated request for the adopted source class
					// now reaches the strategy adapter, which refuses it here only
					// because no destination Runtime capability exists. A legacy
					// or unspecified class stays refused before any source read.
					wantCode, wantBlocker := http.StatusConflict, "adoption_destination_staging_required"
					if item.class != bootstrap.AdoptNetworkExisting {
						wantBlocker = "adoption_source_class_refused"
					}
					var blocker freshstrategy.Blocker
					if response.Code != wantCode || json.Unmarshal(response.Body.Bytes(), &blocker) != nil || blocker.Code != wantBlocker {
						t.Fatalf("Adopt %s without Host authority: %d %s", item.class, response.Code, response.Body.String())
					}
				} else {
					var blocker freshstrategy.Blocker
					if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &blocker) != nil || blocker.Code != item.blocker {
						t.Fatalf("%s/%s: %d %s", item.strategy, item.class, response.Code, response.Body.String())
					}
				}
				after, err := app.store.Load(ctx)
				if err != nil || !reflect.DeepEqual(before, after) || sourceCalls.Load() != 0 || !after.Operation.GatewayPending {
					t.Fatal("blocked request changed source or durable Fresh state")
				}
			}
		}
		if restart == 0 {
			if err := opened.Close(); err != nil {
				t.Fatal(err)
			}
			opened = nil
			opened, err = fresh.Open(ctx, paths, fresh.OpenOptions{})
			if err != nil {
				t.Fatal(err)
			}
			app = newFreshApplication(opened, config.Config{}, nil)
		}
	}
}

// The canonical surface shares one admission point with the claim-authenticated
// one: `legacy_managed` and an unspecified class stay refused with their own
// typed code, and an admitted `network_existing` proceeds to the strategy
// adapter. Every refusal below happens before the source GET and before any
// Manager checkpoint mutation, so none of them has a side effect.
func TestFreshAdoptCanonicalRefusalsHaveNoSourceOrStateEffect(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	defer closeApp()
	var sourceCalls atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer source.Close()

	before, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		sourceClass bootstrap.AdoptSourceClass
		blocker     string
	}{
		// Admitted, then refused only because this fixture has no destination
		// Runtime capability. It is still typed, and still before the source.
		{bootstrap.AdoptNetworkExisting, "adoption_destination_staging_required"},
		{bootstrap.AdoptLegacyManaged, "adoption_source_class_refused"},
		{bootstrap.AdoptSourceNone, "adoption_source_class_refused"},
	} {
		body, err := json.Marshal(struct {
			Identity         bootstrap.FreshIdentity `json:"identity"`
			ExpectedRevision uint64                  `json:"expectedRevision"`
			freshstrategy.Request
		}{before.Operation.Identity, before.Operation.Revision, freshstrategy.Request{
			Strategy: bootstrap.AdoptExisting, AdoptSourceClass: item.sourceClass,
			BaseURL: source.URL, ManagementKey: "private-source-secret",
		}})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		response := httptest.NewRecorder()
		app.canonicalAPI(response, request)
		var blocker freshstrategy.Blocker
		if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &blocker) != nil || blocker.Code != item.blocker {
			t.Fatalf("canonical Adopt %q: %d %s", item.sourceClass, response.Code, response.Body.String())
		}
		after, err := app.store.Load(t.Context())
		if err != nil || !reflect.DeepEqual(before, after) || sourceCalls.Load() != 0 {
			t.Fatalf("Adopt %q touched source or Manager state: %v", item.sourceClass, err)
		}
	}
}
