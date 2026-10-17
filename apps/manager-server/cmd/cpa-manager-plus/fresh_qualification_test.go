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

func TestFreshProductionQualificationCannotBeGrantedByHTTPOrRestart(t *testing.T) {
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
			{bootstrap.NewManaged, "", "exact_v8_artifact_qualification_required"},
			{bootstrap.ConnectExternal, "", "required_v8_profile_qualification_required"},
			{bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting, "adoption_source_qualification_required"},
			{bootstrap.AdoptExisting, bootstrap.AdoptLegacyManaged, "adoption_source_qualification_required"},
		} {
			body := map[string]any{
				"identity": before.Operation.Identity, "expectedRevision": before.Operation.Revision,
				"strategy": item.strategy, "adoptSourceClass": item.class,
				"baseUrl": source.URL, "managementKey": "private-source-secret",
				"expectedRuntimeIdentity": "claimed-runtime", "expectedRuntimeGeneration": 7,
				"expectedArtifactId": "sha256:b682e9e42586263f476888361514f68f89ff4ebf89a5dadba394b850b797ce41",
			}
			for _, injected := range []string{"", "qualification", "qualifiedArtifactId", "adoptionCiphertext", "adoptionResources"} {
				if injected != "" {
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
				} else {
					var blocker freshstrategy.Blocker
					if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &blocker) != nil || blocker.Code != item.blocker {
						t.Fatalf("%s/%s: %d %s", item.strategy, item.class, response.Code, response.Body.String())
					}
				}
				after, err := app.store.Load(ctx)
				if err != nil || !reflect.DeepEqual(before, after) || sourceCalls.Load() != 0 {
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
