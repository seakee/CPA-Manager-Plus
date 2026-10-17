package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

const strategyTestAdmin = "Cpamp_Strategy_Replay_Admin_Secret_839216"

type replayRuntime struct {
	runtimeservice.RuntimeClient
	status model.RuntimeObservedStatus
	starts int
}

func (r *replayRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	return r.status, nil
}
func (r *replayRuntime) Start(context.Context, model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.starts++
	return model.RuntimeOperationResult{}, nil
}

func TestFreshCommittedStrategyReplayChecksTargetsWithoutUpdating(t *testing.T) {
	for _, strategy := range []bootstrap.CPAStrategy{bootstrap.NewManaged, bootstrap.ConnectExternal} {
		t.Run(string(strategy), func(t *testing.T) {
			ctx := t.Context()
			dir := t.TempDir()
			paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
			paths.Config = filepath.Join(dir, "config.json")
			opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
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
				if r.Method != http.MethodGet || r.URL.Path != "/v8/management/config" {
					t.Errorf("unexpected source access: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
			}))
			defer source.Close()
			artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
			runtime := &replayRuntime{status: model.RuntimeObservedStatus{Identity: "runtime-one", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact}}}
			// Synthetic qualification exercises replay only; production wiring
			// and its two unaccepted qualification/destination gates stay closed.
			options := freshstrategy.Options{Runtime: runtime, Protector: opened.Protector, QualifiedArtifactID: artifact,
				Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true}}
			request := freshstrategy.Request{Strategy: strategy}
			checkpoint := bootstrap.FreshStrategyCheckpoint{Strategy: strategy, Stage: "committed", TargetIdentity: "runtime-one", TargetGeneration: 3, TargetArtifactID: string(artifact)}
			var connection *model.ManagerCPAConnectionConfig
			mutations := []func(*freshstrategy.Request){
				func(r *freshstrategy.Request) { r.ExpectedRuntimeIdentity = "foreign-runtime" },
				func(r *freshstrategy.Request) { r.ExpectedRuntimeGeneration++ },
				func(r *freshstrategy.Request) {
					r.ExpectedArtifactID = model.RuntimeArtifactID("sha256:" + strings.Repeat("b", 64))
				},
			}
			if strategy == bootstrap.NewManaged {
				request.ExpectedRuntimeIdentity, request.ExpectedRuntimeGeneration, request.ExpectedArtifactID = runtime.status.Identity, runtime.status.Generation, artifact
			} else {
				request.BaseURL, request.ManagementKey = source.URL, "private-source-secret"
				result, err := freshstrategy.New(options).Execute(ctx, opened.Record.Operation, request)
				if err != nil {
					t.Fatal(err)
				}
				checkpoint = result.Checkpoint
				connection = &model.ManagerCPAConnectionConfig{CPABaseURL: result.Connection.BaseURL, ManagementKey: result.Connection.ManagementKeyCiphertext}
				mutations = []func(*freshstrategy.Request){
					func(r *freshstrategy.Request) { r.BaseURL += "/foreign" },
					func(r *freshstrategy.Request) { r.ManagementKey = "different-valid-source-secret" },
				}
			}
			app := newFreshApplication(opened, config.Config{}, nil)
			assertFreshAuthority := func(want int) {
				t.Helper()
				response := httptest.NewRecorder()
				app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/routing/fresh-authority", nil))
				if response.Code != want {
					t.Fatal("Fresh authority emitted legacy mode or wrong routing state", response.Code)
				}
			}
			assertFreshAuthority(http.StatusNotFound)
			_, err = app.store.Update(ctx, opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(r *freshstore.Record) error {
				r.Operation.Strategy = checkpoint
				r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: r.Operation.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true}
				r.Connection = connection
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for restart := 0; restart < 2; restart++ {
				app.strategy = freshstrategy.New(options)
				for complete := 0; complete < 2; complete++ {
					assertFreshAuthority(http.StatusOK)
					before, err := app.store.Load(ctx)
					if err != nil {
						t.Fatal(err)
					}
					// Repeat exact requests, including a lost reply, both before
					// completion and after a real close/reopen. Revisions stay put.
					for _, mutate := range append([]func(*freshstrategy.Request){nil, nil}, mutations...) {
						input := request
						if mutate != nil {
							mutate(&input)
						}
						body, err := json.Marshal(struct {
							Identity bootstrap.FreshIdentity `json:"identity"`
							Revision uint64                  `json:"expectedRevision"`
							freshstrategy.Request
						}{before.Operation.Identity, before.Operation.Revision, input})
						if err != nil {
							t.Fatal(err)
						}
						calls := sourceCalls.Load()
						httpRequest := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(body))
						httpRequest.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
						response := httptest.NewRecorder()
						app.canonicalAPI(response, httpRequest)
						want := http.StatusOK
						if mutate != nil {
							want = http.StatusConflict
							if sourceCalls.Load() != calls {
								t.Fatal("conflicting replay contacted a different source")
							}
						}
						if response.Code != want {
							t.Fatalf("restart=%d complete=%d conflict=%v: status=%d body=%s", restart, complete, mutate != nil, response.Code, response.Body.String())
						}
						after, err := app.store.Load(ctx)
						if err != nil || !reflect.DeepEqual(before, after) || runtime.starts != 0 {
							t.Fatalf("replay changed durable state or restarted Runtime: %v", err)
						}
					}
					req := httptest.NewRequest(http.MethodPost, "/control/api/v2/complete", nil)
					req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
					response := httptest.NewRecorder()
					app.canonicalAPI(response, req)
					if response.Code != http.StatusOK {
						t.Fatalf("valid strategy could not complete: %d", response.Code)
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
		})
	}
}

func TestFreshCompleteRejectsMalformedStrategyCheckpoint(t *testing.T) {
	for _, kind := range []string{"shape", "missing-external-connection", "mismatched-external-connection"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
			paths.Config = filepath.Join(dir, "config.json")
			opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			op := opened.Record.Operation
			op.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: op.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true}
			op.Strategy.Stage = "committed"
			if kind != "shape" {
				digest := sha256.Sum256([]byte("http://accepted-source:8317"))
				op.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.ConnectExternal, Stage: "committed", TargetIdentity: "external:sha256:" + hex.EncodeToString(digest[:]), TargetGeneration: 1}
				if kind == "mismatched-external-connection" {
					secret, err := opened.Protector.ProtectString("private-source-key")
					if err != nil {
						t.Fatal(err)
					}
					disabled := false
					connection, err := json.Marshal(model.ManagerConfig{CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: "http://different-source:8317", ManagementKey: secret}, Collector: model.ManagerCollectorConfig{Enabled: &disabled}})
					if err != nil {
						t.Fatal(err)
					}
					if _, err = opened.DB.Exec("insert into settings(key,value,updated_at_ms) values('manager_config_v1',?,1)", string(connection)); err != nil {
						t.Fatal(err)
					}
				}
			}
			data, err := json.Marshal(op)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := opened.DB.Exec("update settings set value=? where key=?", string(data), freshstore.OperationKey); err != nil {
				t.Fatal(err)
			}
			app := newFreshApplication(opened, config.Config{}, nil)
			startup := newStartupHTTPHandler(bootstrap.PublicDiscovery{})
			pending := httptest.NewRecorder()
			startup.ServeHTTP(pending, httptest.NewRequest(http.MethodGet, "/internal/routing/fresh-authority", nil))
			if pending.Code == http.StatusNoContent {
				t.Fatal("pending startup acknowledged legacy mode")
			}
			req := httptest.NewRequest(http.MethodPost, "/control/api/v2/complete", nil)
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			response := httptest.NewRecorder()
			app.canonicalAPI(response, req)
			if response.Code == http.StatusOK {
				t.Fatal("malformed strategy checkpoint completed the product")
			}
			var after string
			if err := opened.DB.QueryRow("select value from settings where key=?", freshstore.OperationKey).Scan(&after); err != nil || after != string(data) {
				t.Fatal("rejected completion changed durable checkpoint", err)
			}
			if err := opened.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{})
			if reopened != nil {
				_ = reopened.Close()
			}
			if err == nil {
				t.Fatal("restart admitted malformed strategy checkpoint")
			}
		})
	}
}
