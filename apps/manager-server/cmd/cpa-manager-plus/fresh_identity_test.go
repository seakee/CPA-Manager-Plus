package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func freshBridgeKeyHash(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func TestFreshEmbeddedLegacyMutationsUseRuntimeFencesAndJournal(t *testing.T) {
	for _, ready := range []bool{false, true} {
		name := "runtime-unavailable"
		if ready {
			name = "runtime-ready"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/runtime/status" || r.Header.Get("Authorization") != "Bearer bridge-runtime-token" {
					http.NotFound(w, r)
					return
				}
				if !ready {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":7,"state":"ready","cpaObservedVersion":"8.0.13","activeGatewayArtifact":{"engine":"cpa","artifactId":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","version":"8.0.13"},"capabilities":[]}`))
			}))
			defer runtime.Close()
			var mu sync.Mutex
			key, credentialPresent, forwards := "bridge-old-key", true, 0
			var dbForJournalCheck *store.Store
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer bridge-management-key" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/v0/management/api-keys" && r.Method == http.MethodGet:
					_ = json.NewEncoder(w).Encode(map[string]any{"api-keys": []string{key}})
				case r.URL.Path == "/v0/management/api-keys" && r.Method == http.MethodPatch:
					var pending bool
					pending, err := dbForJournalCheck.Identities.(ports.MutationRepository).HasPendingAPIKeyMutation(r.Context(), "runtime-01")
					if err != nil || !pending {
						t.Error("API-key mutation reached CPA without a durable intent", err)
					}
					key = "bridge-new-key"
					forwards++
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
					files := []map[string]any{}
					if credentialPresent {
						files = append(files, map[string]any{"id": "source-one", "name": "shared.json", "auth_index": "idx-one", "provider": "codex", "account_id": "account-one"})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
				case r.URL.Path == "/v0/management/auth-files/download" && r.Method == http.MethodGet:
					if !credentialPresent {
						http.NotFound(w, r)
						return
					}
					_, _ = w.Write([]byte(`{}`))
				case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodDelete:
					pending, err := dbForJournalCheck.Identities.(ports.CredentialDeleteRepository).PendingCredentialDeleteSources(r.Context(), "runtime-01")
					if err != nil || len(pending) != 1 {
						t.Error("credential delete reached CPA without a durable intent", err)
					}
					credentialPresent = false
					forwards++
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			tokenFile := filepath.Join(dir, "runtime.token")
			if err := os.WriteFile(tokenFile, []byte("bridge-runtime-token"), 0600); err != nil {
				t.Fatal(err)
			}
			paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
			paths.Config = filepath.Join(dir, "config.json")
			opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			cfg := config.Config{RuntimeURL: runtime.URL, RuntimeTokenFile: tokenFile, CPAUpstreamURL: upstream.URL, ManagementKey: "bridge-management-key"}
			application := newFreshApplication(opened, cfg, nil)
			if _, err = application.store.Update(t.Context(), opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(record *freshstore.Record) error {
				record.Operation.Routing.OperationID = record.Operation.Identity.OperationID
				record.Operation.Routing.Revision = 1
				record.Operation.Routing.Target = "/cpamp-admin"
				record.Operation.Routing.Stage = bootstrap.PrefixCommitted
				record.Operation.Routing.Committed = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			markFreshProductComplete(t, application)
			st := store.New(opened.DB, opened.Protector)
			dbForJournalCheck = st
			if _, err = st.Identities.ApplyPassiveSnapshot(t.Context(), ports.ReconcileSnapshotParams{
				RuntimeIdentity: "runtime-01", ObservedRuntimeGeneration: 7, NowMS: 1000,
				APIKeys:     []ports.APIKeySnapshotItem{{APIKeyHash: freshBridgeKeyHash(key)}},
				Credentials: []ports.CredentialSnapshotItem{{SourceAuthID: "source-one", PhysicalName: "shared.json", Provider: "codex"}},
			}); err != nil {
				t.Fatal(err)
			}
			before, _, err := st.Identities.FindActiveAPIKeyBySource(t.Context(), "runtime-01", freshBridgeKeyHash(key))
			if err != nil {
				t.Fatal(err)
			}
			rotation := httptest.NewRequest(http.MethodPatch, currentInstallManagementAPIV2Prefix+"cpamp/v0/management/api-keys", strings.NewReader(`{"old":"bridge-old-key","new":"bridge-new-key"}`))
			deletion := httptest.NewRequest(http.MethodDelete, currentInstallManagementAPIV2Prefix+"cpamp/v0/management/auth-files?name=shared.json", nil)
			deletion.Header.Set("X-CPAMP-Auth-File-Physical-Name", "shared.json")
			deletion.Header.Set("X-CPAMP-Auth-File-Delete-Identities", url.QueryEscape(`[{"name":"shared.json","runtimeId":"source-one","authIndex":"idx-one","provider":"codex","accountId":"account-one"}]`))
			for _, request := range []*http.Request{rotation, deletion} {
				request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
				response := httptest.NewRecorder()
				application.canonicalAPI(response, request)
				want := http.StatusBadGateway
				if ready {
					want = http.StatusOK
				}
				if response.Code != want {
					t.Fatalf("%s %s returned %d, want %d: %s", request.Method, request.URL.Path, response.Code, want, response.Body.String())
				}
			}
			mu.Lock()
			count := forwards
			mu.Unlock()
			if !ready && count != 0 || ready && count != 2 {
				t.Fatalf("forwarded mutations=%d, ready=%v", count, ready)
			}
			if ready {
				after, _, err := st.Identities.FindActiveAPIKeyBySource(t.Context(), "runtime-01", freshBridgeKeyHash("bridge-new-key"))
				if err != nil || after.ID != before.ID || after.Revision != before.Revision+1 {
					t.Fatal("rotation lost its stable identity", err)
				}
			}
			for _, table := range []string{sqlite.GatewayAPIKeyMutationIntentsTable, sqlite.GatewayCredentialDeleteIntentsTable} {
				var count int
				if err := opened.DB.QueryRow("select count(*) from " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("pending intents in %s: %d, %v", table, count, err)
				}
			}
			if _, err := application.store.Load(t.Context()); err != nil {
				t.Fatal("mutation invalidated completed Fresh ownership", err)
			}
		})
	}
}
