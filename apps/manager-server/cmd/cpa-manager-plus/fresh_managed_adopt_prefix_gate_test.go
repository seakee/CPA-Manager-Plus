package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

type prefixGateHarness struct {
	paths       sqlite.InspectionPaths
	opened      *fresh.Opened
	app         *freshApplication
	driver      *dualPrefixRoutingDriver
	runtime     *migrationRuntime
	source      *migrationFakeCPA
	destination *migrationFakeCPA
	session     string
	artifact    model.RuntimeArtifactID
}

func newPrefixGateHarness(t *testing.T, adoptedKeys ...string) *prefixGateHarness {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &migrationRuntime{status: model.RuntimeObservedStatus{
		Identity: "runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady,
		Capabilities:          model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination},
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact},
	}}

	source := newMigrationFakeCPA(t)
	source.auth = migrationSourceKey
	source.config["routing"] = map[string]any{"strategy": "round-robin"}
	sourceKeys := make([]any, 0, len(adoptedKeys))
	for _, key := range adoptedKeys {
		sourceKeys = append(sourceKeys, key)
	}
	source.config["access"] = map[string]any{"api-keys": sourceKeys}
	source.config["oauth"] = map[string]any{"auth-dir": "/source/auth"}
	source.config["server"] = map[string]any{"host": "127.0.0.1", "port": json.Number("8318")}

	destination := newMigrationFakeCPA(t)
	destination.config["config-version"] = json.Number("8")
	destination.config["server"] = map[string]any{"host": "0.0.0.0", "port": json.Number("8317")}
	destination.config["plugins"] = map[string]any{"enabled": false, "dir": "/runtime/supervisor/plugins"}
	destination.config["oauth"] = map[string]any{"auth-dir": "/runtime/supervisor/destination/auth"}
	destination.config["management"] = map[string]any{
		"allow-remote":              true,
		"secret-key":                "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12",
		"disable-control-panel":     true,
		"disable-auto-update-panel": true,
	}
	destination.config["observability"] = map[string]any{
		"logs":  map[string]any{"logging-to-file": false},
		"usage": map[string]any{"usage-statistics-enabled": true},
	}
	destinationKeys := make([]any, 0, len(adoptedKeys))
	for _, key := range adoptedKeys {
		destinationKeys = append(destinationKeys, key)
	}
	destination.config["access"] = map[string]any{"api-keys": destinationKeys}
	destination.authFn = func() string { return runtime.stagedKey }

	driver := &dualPrefixRoutingDriver{}
	app := newFreshApplication(opened, config.Config{}, driver)
	wireAdoptManagedAuthority(app, runtime, destination.URL())
	app.routing = freshrouting.New(app.store, driver)
	app.strategy = freshstrategy.New(freshstrategy.Options{
		Runtime:             runtime,
		Protector:           opened.Protector,
		DestinationBaseURL:  destination.URL(),
		Qualification:       bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
		QualifiedArtifactID: artifact,
		InitialClientKey:    installTestClientKey,
		AdoptionResources:   migratingDecisions(),
	})
	app.externalPermit = func(bootstrap.FreshOperation, uint64, string, string, string) bool { return true }
	session := installationSession(t, app)

	return &prefixGateHarness{
		paths:       paths,
		opened:      opened,
		app:         app,
		driver:      driver,
		runtime:     runtime,
		source:      source,
		destination: destination,
		session:     session,
		artifact:    artifact,
	}
}

func (h *prefixGateHarness) reload(t *testing.T) freshstore.Record {
	t.Helper()
	rec, err := h.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func (h *prefixGateHarness) waitAdoptMigration(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for h.app.adoptMigrationInFlight() {
		if time.Now().After(deadline) {
			t.Fatal("Adopt migration did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *prefixGateHarness) doJSON(t *testing.T, method, path, auth string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, body)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.app.ServeHTTP(rec, req)
	return rec
}

func (h *prefixGateHarness) strategyPayload(strategy bootstrap.CPAStrategy) freshstrategy.Request {
	switch strategy {
	case bootstrap.NewManaged:
		return freshstrategy.Request{
			Strategy:                  bootstrap.NewManaged,
			ExpectedRuntimeIdentity:   "runtime",
			ExpectedRuntimeGeneration: 3,
			ExpectedArtifactID:        h.artifact,
		}
	case bootstrap.AdoptExisting:
		return freshstrategy.Request{
			Strategy:                  bootstrap.AdoptExisting,
			AdoptSourceClass:          bootstrap.AdoptNetworkExisting,
			BaseURL:                   h.source.URL(),
			ManagementKey:             migrationSourceKey,
			ExpectedRuntimeIdentity:   "runtime",
			ExpectedRuntimeGeneration: 3,
			ExpectedArtifactID:        h.artifact,
		}
	default:
		return freshstrategy.Request{Strategy: strategy}
	}
}

func decodePrefixGateCode(body []byte) string {
	var parsed struct {
		Code      string `json:"Code"`
		LowerCode string `json:"code"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	if parsed.Code != "" {
		return parsed.Code
	}
	return parsed.LowerCode
}

func TestFreshManagedAndAdoptEnforceCommittedInferencePrefixInvariant(t *testing.T) {
	t.Run("control_only_prepared_before_strategy_refuses_managed_and_adopt", func(t *testing.T) {
		for _, strategy := range []bootstrap.CPAStrategy{bootstrap.NewManaged, bootstrap.AdoptExisting} {
			t.Run(string(strategy), func(t *testing.T) {
				h := newPrefixGateHarness(t)
				rec := h.reload(t)

				prep := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"routing/prepare", "Fresh "+h.session, map[string]any{
					"identity": rec.Operation.Identity,
					"target":   "/cpamp-admin",
				})
				if prep.Code != http.StatusOK {
					t.Fatalf("POST routing/prepare control-only before strategy = %d %s, want 200", prep.Code, prep.Body.String())
				}

				afterPrep := h.reload(t)
				strat := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"strategy", "Fresh "+h.session, struct {
					Identity         bootstrap.FreshIdentity `json:"identity"`
					ExpectedRevision uint64                  `json:"expectedRevision"`
					freshstrategy.Request
				}{
					Identity:         afterPrep.Operation.Identity,
					ExpectedRevision: afterPrep.Operation.Revision,
					Request:          h.strategyPayload(strategy),
				})
				if strat.Code != http.StatusConflict || decodePrefixGateCode(strat.Body.Bytes()) != string(bootstrap.CodeInferencePrefixRequired) {
					t.Fatalf("POST fixed strategy (%s) after control-only prepare = %d %s, want 409 %s",
						strategy, strat.Code, strat.Body.String(), bootstrap.CodeInferencePrefixRequired)
				}
				if len(h.runtime.stages) != 0 || len(h.runtime.starts) != 0 {
					t.Fatalf("refused strategy (%s) invoked Runtime Supervisor: stages=%d starts=%d",
						strategy, len(h.runtime.stages), len(h.runtime.starts))
				}
				afterRefusal := h.reload(t)
				if afterRefusal.Operation.Strategy.Strategy != "" || afterRefusal.Operation.ProductComplete {
					t.Fatalf("refused strategy (%s) mutated operation: %+v", strategy, afterRefusal.Operation)
				}
			})
		}
	})

	t.Run("control_only_activated_before_strategy_refuses_managed_and_adopt_and_never_completes", func(t *testing.T) {
		for _, strategy := range []bootstrap.CPAStrategy{bootstrap.NewManaged, bootstrap.AdoptExisting} {
			t.Run(string(strategy), func(t *testing.T) {
				h := newPrefixGateHarness(t)
				rec := h.reload(t)

				prep := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"routing/prepare", "Fresh "+h.session, map[string]any{
					"identity": rec.Operation.Identity,
					"target":   "/cpamp-admin",
				})
				if prep.Code != http.StatusOK {
					t.Fatalf("POST routing/prepare = %d %s, want 200", prep.Code, prep.Body.String())
				}
				act := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"routing/activate", "Fresh "+h.session, map[string]any{
					"identity": rec.Operation.Identity,
				})
				if act.Code != http.StatusOK {
					t.Fatalf("POST routing/activate = %d %s, want 200", act.Code, act.Body.String())
				}

				afterAct := h.reload(t)
				if !afterAct.Operation.Routing.Committed || afterAct.Operation.Routing.InferenceCommitted {
					t.Fatalf("unexpected control-only activated routing: %+v", afterAct.Operation.Routing)
				}

				for _, auth := range []string{"Fresh " + h.session, "Bearer " + strategyTestAdmin} {
					strat := h.doJSON(t, http.MethodPost, "/cpamp-admin/api/v2/strategy", auth, struct {
						Identity         bootstrap.FreshIdentity `json:"identity"`
						ExpectedRevision uint64                  `json:"expectedRevision"`
						freshstrategy.Request
					}{
						Identity:         afterAct.Operation.Identity,
						ExpectedRevision: afterAct.Operation.Revision,
						Request:          h.strategyPayload(strategy),
					})
					if strat.Code != http.StatusConflict || decodePrefixGateCode(strat.Body.Bytes()) != string(bootstrap.CodeInferencePrefixRequired) {
						t.Fatalf("POST canonical strategy (%s, auth=%s) = %d %s, want 409 %s",
							strategy, strings.Fields(auth)[0], strat.Code, strat.Body.String(), bootstrap.CodeInferencePrefixRequired)
					}
				}
				if len(h.runtime.stages) != 0 || len(h.runtime.starts) != 0 {
					t.Fatalf("refused canonical strategy (%s) invoked Runtime Supervisor: stages=%d starts=%d",
						strategy, len(h.runtime.stages), len(h.runtime.starts))
				}

				comp := h.doJSON(t, http.MethodPost, "/cpamp-admin/api/v2/complete", "Bearer "+strategyTestAdmin, map[string]any{})
				if comp.Code == http.StatusOK {
					t.Fatalf("POST complete (%s) unexpectedly succeeded: %s", strategy, comp.Body.String())
				}
				finalRec := h.reload(t)
				if finalRec.Operation.ProductComplete || finalRec.Operation.Strategy.Strategy != "" {
					t.Fatalf("control-only sequence mutated completion or strategy (%s): %+v", strategy, finalRec.Operation)
				}
			})
		}
	})

	t.Run("first_completion_refuses_missing_or_uncommitted_inference_and_recovers_via_access_completion", func(t *testing.T) {
		for _, strategy := range []bootstrap.CPAStrategy{bootstrap.NewManaged, bootstrap.AdoptExisting} {
			for _, stagedUncommittedInference := range []bool{false, true} {
				name := string(strategy) + "_missing_inference"
				if stagedUncommittedInference {
					name = string(strategy) + "_uncommitted_inference"
				}
				t.Run(name, func(t *testing.T) {
					adoptedKey := "sk-" + strings.Repeat("kM7", 20)
					h := newPrefixGateHarness(t, adoptedKey)
					ctx := t.Context()

					// Stage and commit the strategy before routing is prepared, then simulate an
					// incomplete checkpoint whose control route committed without InferenceCommitted.
					s1 := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"strategy", "Fresh "+h.session, struct {
						Identity         bootstrap.FreshIdentity `json:"identity"`
						ExpectedRevision uint64                  `json:"expectedRevision"`
						freshstrategy.Request
					}{
						Identity:         h.reload(t).Operation.Identity,
						ExpectedRevision: h.reload(t).Operation.Revision,
						Request:          h.strategyPayload(strategy),
					})
					if s1.Code != http.StatusOK {
						t.Fatalf("strategy stage 1 (%s) = %d %s", strategy, s1.Code, s1.Body.String())
					}
					s2 := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"strategy", "Fresh "+h.session, struct {
						Identity         bootstrap.FreshIdentity `json:"identity"`
						ExpectedRevision uint64                  `json:"expectedRevision"`
						freshstrategy.Request
					}{
						Identity:         h.reload(t).Operation.Identity,
						ExpectedRevision: h.reload(t).Operation.Revision,
						Request:          h.strategyPayload(strategy),
					})
					if s2.Code != http.StatusOK {
						t.Fatalf("strategy stage 2 (%s) = %d %s", strategy, s2.Code, s2.Body.String())
					}
					if strategy == bootstrap.AdoptExisting {
						h.waitAdoptMigration(t)
					}

					cur := h.reload(t)
					updated, err := h.app.store.Update(ctx, cur.Operation.Identity, cur.Operation.Revision, func(r *freshstore.Record) error {
						r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
						r.Operation.Routing.Revision = 1
						r.Operation.Routing.Target = "/cpamp-admin"
						if stagedUncommittedInference {
							r.Operation.Routing.InferenceTarget = "/cpamp-api"
						}
						r.Operation.Routing.Stage = bootstrap.PrefixCommitted
						r.Operation.Routing.Committed = true
						r.Operation.Routing.InferenceCommitted = false
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					h.driver.state = freshrouting.StateFor(updated.Operation)

					refused := h.doJSON(t, http.MethodPost, "/cpamp-admin/api/v2/complete", "Bearer "+strategyTestAdmin, map[string]any{})
					if refused.Code != http.StatusConflict {
						t.Fatalf("POST complete (%s) without InferenceCommitted = %d %s, want 409",
							name, refused.Code, refused.Body.String())
					}
					if h.reload(t).Operation.ProductComplete {
						t.Fatalf("POST complete (%s) set ProductComplete without InferenceCommitted", name)
					}

					// Complete missing/uncommitted inference via the authenticated one-time endpoint,
					// then verify POST complete succeeds and publishes InferenceCommitted to Ingress.
					fill := h.doJSON(t, http.MethodPost, "/cpamp-admin/api/v2/installation/access/completion", "Bearer "+strategyTestAdmin, map[string]any{
						"inferenceBasePath": "/cpamp-api",
						"sharedEntry":       false,
					})
					if fill.Code != http.StatusOK {
						t.Fatalf("POST installation/access/completion (%s) = %d %s, want 200",
							name, fill.Code, fill.Body.String())
					}

					completed := h.doJSON(t, http.MethodPost, "/cpamp-admin/api/v2/complete", "Bearer "+strategyTestAdmin, map[string]any{})
					if completed.Code != http.StatusOK {
						t.Fatalf("POST complete (%s) after access completion = %d %s, want 200",
							name, completed.Code, completed.Body.String())
					}
					after := h.reload(t)
					if !after.Operation.ProductComplete || !after.Operation.Routing.InferenceCommitted || after.Operation.Routing.InferenceTarget != "/cpamp-api" {
						t.Fatalf("unexpected operation after recovery (%s): %+v", name, after.Operation)
					}
					authRec := h.doJSON(t, http.MethodGet, freshrouting.AuthorityPath, "", nil)
					if authRec.Code != http.StatusOK || !strings.Contains(authRec.Body.String(), `"inferenceCommitted":true`) {
						t.Fatalf("fresh-authority after recovery (%s) = %d %s", name, authRec.Code, authRec.Body.String())
					}
				})
			}
		}
	})

	t.Run("dual_prefix_prepared_before_strategy_refuses_connect_external_without_source_call", func(t *testing.T) {
		h := newPrefixGateHarness(t)
		var extHits atomic.Int32
		extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			extHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
		}))
		defer extCPA.Close()

		rec := h.reload(t)
		prep := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"routing/prepare", "Fresh "+h.session, map[string]any{
			"identity":        rec.Operation.Identity,
			"target":          "/cpamp-admin",
			"inferenceTarget": "/cpamp-api",
			"sharedEntry":     false,
		})
		if prep.Code != http.StatusOK {
			t.Fatalf("POST routing/prepare dual-prefix = %d %s, want 200", prep.Code, prep.Body.String())
		}

		afterPrep := h.reload(t)
		ext := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"strategy", "Fresh "+h.session, struct {
			Identity         bootstrap.FreshIdentity `json:"identity"`
			ExpectedRevision uint64                  `json:"expectedRevision"`
			AttemptID        string                  `json:"attemptId"`
			freshstrategy.Request
		}{
			Identity:         afterPrep.Operation.Identity,
			ExpectedRevision: afterPrep.Operation.Revision,
			AttemptID:        strings.Repeat("c", 32),
			Request: freshstrategy.Request{
				Strategy:      bootstrap.ConnectExternal,
				BaseURL:       extCPA.URL,
				ManagementKey: "external-secret-key",
			},
		})
		if ext.Code != http.StatusConflict || decodePrefixGateCode(ext.Body.Bytes()) != string(bootstrap.CodeExternalInferencePrefixForbidden) {
			t.Fatalf("POST strategy (ConnectExternal) after dual-prefix prepare = %d %s, want 409 %s",
				ext.Code, ext.Body.String(), bootstrap.CodeExternalInferencePrefixForbidden)
		}
		if extHits.Load() != 0 {
			t.Fatalf("ConnectExternal contacted external CPA despite dual-prefix checkpoint: hits=%d", extHits.Load())
		}
	})
}

func TestFreshPrefixModesCompleteValidManagedAdoptExternalAndPreserveHistoricalInstall(t *testing.T) {
	runManagedOrAdopt := func(t *testing.T, strategy bootstrap.CPAStrategy, routingFirst bool, control, inference string, shared bool) {
		t.Helper()
		adoptedKey := "sk-" + strings.Repeat("pQ9", 20)
		h := newPrefixGateHarness(t, adoptedKey)

		runStrategy := func(carrierPrefix, auth string) {
			t.Helper()
			for stage := 1; stage <= 2; stage++ {
				cur := h.reload(t)
				res := h.doJSON(t, http.MethodPost, carrierPrefix+"strategy", auth, struct {
					Identity         bootstrap.FreshIdentity `json:"identity"`
					ExpectedRevision uint64                  `json:"expectedRevision"`
					freshstrategy.Request
				}{
					Identity:         cur.Operation.Identity,
					ExpectedRevision: cur.Operation.Revision,
					Request:          h.strategyPayload(strategy),
				})
				if res.Code != http.StatusOK {
					t.Fatalf("strategy (%s, stage=%d) = %d %s, want 200", strategy, stage, res.Code, res.Body.String())
				}
			}
			if strategy == bootstrap.AdoptExisting {
				h.waitAdoptMigration(t)
			}
		}

		runRouting := func() {
			t.Helper()
			cur := h.reload(t)
			prep := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"routing/prepare", "Fresh "+h.session, map[string]any{
				"identity":        cur.Operation.Identity,
				"target":          control,
				"inferenceTarget": inference,
				"sharedEntry":     shared,
			})
			if prep.Code != http.StatusOK {
				t.Fatalf("POST routing/prepare = %d %s, want 200", prep.Code, prep.Body.String())
			}
			act := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"routing/activate", "Fresh "+h.session, map[string]any{
				"identity": cur.Operation.Identity,
			})
			if act.Code != http.StatusOK {
				t.Fatalf("POST routing/activate = %d %s, want 200", act.Code, act.Body.String())
			}
		}

		if routingFirst {
			runRouting()
			runStrategy(control+"/api/v2/", "Fresh "+h.session)
		} else {
			runStrategy(bootstrap.FixedBootstrapAPIPath, "Fresh "+h.session)
			runRouting()
		}

		comp := h.doJSON(t, http.MethodPost, control+"/api/v2/complete", "Fresh "+h.session, map[string]any{})
		if comp.Code != http.StatusOK {
			t.Fatalf("POST complete (%s) = %d %s, want 200", strategy, comp.Code, comp.Body.String())
		}
		done := h.reload(t)
		if !done.Operation.ProductComplete ||
			!done.Operation.Routing.Committed ||
			!done.Operation.Routing.InferenceCommitted ||
			done.Operation.Routing.Target != control ||
			done.Operation.Routing.InferenceTarget != inference ||
			done.Operation.Routing.SharedEntry != shared {
			t.Fatalf("unexpected completed operation (%s): %+v", strategy, done.Operation)
		}
		authRec := h.doJSON(t, http.MethodGet, freshrouting.AuthorityPath, "", nil)
		if authRec.Code != http.StatusOK || !strings.Contains(authRec.Body.String(), `"inferenceCommitted":true`) {
			t.Fatalf("fresh-authority (%s) = %d %s", strategy, authRec.Code, authRec.Body.String())
		}
	}

	t.Run("valid_distinct_prefixes_work_for_managed_and_adopt_both_orders", func(t *testing.T) {
		for _, strategy := range []bootstrap.CPAStrategy{bootstrap.NewManaged, bootstrap.AdoptExisting} {
			for _, routingFirst := range []bool{false, true} {
				order := "strategy_then_routing"
				if routingFirst {
					order = "routing_then_strategy"
				}
				t.Run(string(strategy)+"_"+order, func(t *testing.T) {
					runManagedOrAdopt(t, strategy, routingFirst, "/cpamp-admin", "/cpamp-api", false)
				})
			}
		}
	})

	t.Run("valid_shared_prefix_works_for_managed_and_adopt", func(t *testing.T) {
		for _, strategy := range []bootstrap.CPAStrategy{bootstrap.NewManaged, bootstrap.AdoptExisting} {
			t.Run(string(strategy), func(t *testing.T) {
				runManagedOrAdopt(t, strategy, true, "/cpamp-shared", "/cpamp-shared", true)
			})
		}
	})

	t.Run("external_control_only_prepared_before_strategy_completes_cleanly", func(t *testing.T) {
		h := newPrefixGateHarness(t)
		h.runtime.status.State = model.RuntimeStateOffline
		h.runtime.status.Recovery = &model.RuntimeRecoveryObservation{State: model.RuntimeRecoveryStateInactive}
		extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer external-secret-key" || r.URL.Path != "/v8/management/config" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
		}))
		defer extCPA.Close()

		cur := h.reload(t)
		prep := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"routing/prepare", "Fresh "+h.session, map[string]any{
			"identity": cur.Operation.Identity,
			"target":   "/cpamp-admin",
		})
		if prep.Code != http.StatusOK {
			t.Fatalf("POST routing/prepare = %d %s, want 200", prep.Code, prep.Body.String())
		}
		act := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"routing/activate", "Fresh "+h.session, map[string]any{
			"identity": cur.Operation.Identity,
		})
		if act.Code != http.StatusOK {
			t.Fatalf("POST routing/activate = %d %s, want 200", act.Code, act.Body.String())
		}

		afterAct := h.reload(t)
		strat := h.doJSON(t, http.MethodPost, "/cpamp-admin/api/v2/strategy", "Fresh "+h.session, struct {
			Identity         bootstrap.FreshIdentity `json:"identity"`
			ExpectedRevision uint64                  `json:"expectedRevision"`
			AttemptID        string                  `json:"attemptId"`
			freshstrategy.Request
		}{
			Identity:         afterAct.Operation.Identity,
			ExpectedRevision: afterAct.Operation.Revision,
			AttemptID:        strings.Repeat("e", 32),
			Request: freshstrategy.Request{
				Strategy:      bootstrap.ConnectExternal,
				BaseURL:       extCPA.URL,
				ManagementKey: "external-secret-key",
			},
		})
		if strat.Code != http.StatusOK {
			t.Fatalf("POST strategy (ConnectExternal) = %d %s, want 200", strat.Code, strat.Body.String())
		}

		comp := h.doJSON(t, http.MethodPost, "/cpamp-admin/api/v2/complete", "Fresh "+h.session, map[string]any{})
		if comp.Code != http.StatusOK {
			t.Fatalf("POST complete (ConnectExternal) = %d %s, want 200", comp.Code, comp.Body.String())
		}
		done := h.reload(t)
		if !done.Operation.ProductComplete || !done.Operation.Routing.Committed || done.Operation.Routing.InferenceCommitted || done.Operation.Routing.InferenceTarget != "" {
			t.Fatalf("unexpected External completed state: %+v", done.Operation)
		}
	})

	t.Run("historical_completed_managed_and_adopt_missing_inference_load_replay_and_complete_once", func(t *testing.T) {
		for _, strategy := range []bootstrap.CPAStrategy{bootstrap.NewManaged, bootstrap.AdoptExisting} {
			t.Run(string(strategy), func(t *testing.T) {
				adoptedKey := "sk-" + strings.Repeat("hR4", 20)
				h := newPrefixGateHarness(t, adoptedKey)
				ctx := t.Context()

				for stage := 1; stage <= 2; stage++ {
					cur := h.reload(t)
					res := h.doJSON(t, http.MethodPost, bootstrap.FixedBootstrapAPIPath+"strategy", "Fresh "+h.session, struct {
						Identity         bootstrap.FreshIdentity `json:"identity"`
						ExpectedRevision uint64                  `json:"expectedRevision"`
						freshstrategy.Request
					}{
						Identity:         cur.Operation.Identity,
						ExpectedRevision: cur.Operation.Revision,
						Request:          h.strategyPayload(strategy),
					})
					if res.Code != http.StatusOK {
						t.Fatalf("strategy (%s, stage=%d) = %d %s", strategy, stage, res.Code, res.Body.String())
					}
				}
				if strategy == bootstrap.AdoptExisting {
					h.waitAdoptMigration(t)
				}

				// Persist a historical pre-dual-prefix v2 completed record directly in SQLite,
				// then close and reopen the store from disk to prove startup load compatibility.
				cur := h.reload(t)
				if _, err := h.app.store.Update(ctx, cur.Operation.Identity, cur.Operation.Revision, func(r *freshstore.Record) error {
					r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
					r.Operation.Routing.Revision = 1
					r.Operation.Routing.Target = "/cpamp-admin"
					r.Operation.Routing.InferenceTarget = ""
					r.Operation.Routing.Stage = bootstrap.PrefixCommitted
					r.Operation.Routing.Committed = true
					r.Operation.Routing.InferenceCommitted = false
					r.Operation.ProductComplete = true
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if err := h.opened.Close(); err != nil {
					t.Fatal(err)
				}

				reopened, err := fresh.Open(ctx, h.paths, fresh.OpenOptions{})
				if err != nil {
					t.Fatalf("fresh.Open rejected historical completed %s record: %v", strategy, err)
				}
				t.Cleanup(func() { _ = reopened.Close() })

				driver := &dualPrefixRoutingDriver{state: freshrouting.StateFor(reopened.Record.Operation)}
				reopenedApp := newFreshApplication(reopened, config.Config{}, driver)
				wireAdoptManagedAuthority(reopenedApp, h.runtime, h.destination.URL())
				reopenedApp.routing = freshrouting.New(reopenedApp.store, driver)
				h.opened = reopened
				h.app = reopenedApp
				h.driver = driver

				opRec := h.doJSON(t, http.MethodGet, "/cpamp-admin/api/v2/operation", "Bearer "+strategyTestAdmin, nil)
				if opRec.Code != http.StatusOK || !strings.Contains(opRec.Body.String(), `"missingInferenceCompletionRequired":true`) {
					t.Fatalf("GET operation on historical %s = %d %s", strategy, opRec.Code, opRec.Body.String())
				}

				replay := h.doJSON(t, http.MethodPost, "/cpamp-admin/api/v2/complete", "Bearer "+strategyTestAdmin, map[string]any{})
				if replay.Code != http.StatusOK {
					t.Fatalf("POST complete replay on historical %s = %d %s, want 200", strategy, replay.Code, replay.Body.String())
				}

				statusRec := h.doJSON(t, http.MethodGet, "/cpamp-admin/api/v2/installation/access/completion", "Bearer "+strategyTestAdmin, nil)
				if statusRec.Code != http.StatusOK || !strings.Contains(statusRec.Body.String(), `"required":true`) {
					t.Fatalf("GET installation/access/completion on historical %s = %d %s", strategy, statusRec.Code, statusRec.Body.String())
				}

				postCompletion := h.doJSON(t, http.MethodPost, "/cpamp-admin/api/v2/installation/access/completion", "Bearer "+strategyTestAdmin, map[string]any{
					"inferenceBasePath": "/cpamp-api",
					"sharedEntry":       false,
				})
				if postCompletion.Code != http.StatusOK || !strings.Contains(postCompletion.Body.String(), `"required":false`) {
					t.Fatalf("POST installation/access/completion on historical %s = %d %s, want 200", strategy, postCompletion.Code, postCompletion.Body.String())
				}

				secondCompletion := h.doJSON(t, http.MethodPost, "/cpamp-admin/api/v2/installation/access/completion", "Bearer "+strategyTestAdmin, map[string]any{
					"inferenceBasePath": "/cpamp-api-2",
					"sharedEntry":       false,
				})
				if secondCompletion.Code != http.StatusConflict || decodePrefixGateCode(secondCompletion.Body.Bytes()) != string(bootstrap.CodeInferencePrefixAlreadyCommitted) {
					t.Fatalf("second POST installation/access/completion on historical %s = %d %s, want 409 %s",
						strategy, secondCompletion.Code, secondCompletion.Body.String(), bootstrap.CodeInferencePrefixAlreadyCommitted)
				}
			})
		}
	})
}
