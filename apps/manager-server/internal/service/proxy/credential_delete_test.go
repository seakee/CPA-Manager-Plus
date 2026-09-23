package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/credentialdeletemutation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type credentialDeleteCPA struct {
	mu                   sync.Mutex
	members              []map[string]any
	deleteStatus         int
	remainingAfterDelete int
	beforeDelete         func()
	transportFailure     bool
}

func (c *credentialDeleteCPA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v0/management/auth-files" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		c.mu.Lock()
		members := append([]map[string]any(nil), c.members...)
		c.mu.Unlock()
		if members == nil {
			members = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": members})
	case http.MethodDelete:
		if c.beforeDelete != nil {
			c.beforeDelete()
		}
		c.mu.Lock()
		if c.remainingAfterDelete < len(c.members) {
			c.members = c.members[:c.remainingAfterDelete]
		}
		c.mu.Unlock()
		if c.transportFailure {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.WriteHeader(c.deleteStatus)
		fmt.Fprint(w, `{"status":"upstream"}`)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func credentialDeleteMembers(sources ...string) []map[string]any {
	members := make([]map[string]any, len(sources))
	for i, source := range sources {
		members[i] = map[string]any{
			"id": source, "name": "shared.json", "auth_index": "idx-" + source,
			"provider": "codex", "account_id": "account-" + source,
		}
	}
	return members
}

func newCredentialDeleteProxyFixture(t *testing.T, upstreamURL string, sources ...string) (*Service, *sql.DB, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "proxy-credential-delete.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SaveSetup(context.Background(), store.Setup{CPAUpstreamURL: upstreamURL, ManagementKey: "management-key"}); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	items := make([]ports.CredentialSnapshotItem, len(sources))
	for i, source := range sources {
		items[i] = ports.CredentialSnapshotItem{SourceAuthID: source, PhysicalName: "shared.json", Provider: "codex"}
	}
	if _, err := st.Identities.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, Credentials: items, NowMS: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	svc := New(managerconfig.New(config.Config{}, st, nil), st)
	mutations, err := credentialdeletemutation.NewService(credentialdeletemutation.Config{
		RuntimeObserver: readyAPIKeyRuntime{},
		Repository:      st.Identities.(ports.CredentialDeleteRepository), ProcessInstanceID: "process-A",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetCredentialDeleteService(mutations)
	return svc, db, st
}

func credentialDeleteRequest(t *testing.T, sources ...string) *http.Request {
	t.Helper()
	identities := make([]map[string]string, len(sources))
	for i, source := range sources {
		identities[i] = map[string]string{
			"name": "shared.json", "runtimeId": source, "authIndex": "idx-" + source,
			"provider": "codex", "accountId": "account-" + source,
		}
	}
	raw, err := json.Marshal(identities)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/v0/management/auth-files?name=shared.json", nil)
	req.Header.Set(authFilePhysicalNameHeader, "shared.json")
	req.Header.Set(authFileDeleteIdentitiesHeader, url.QueryEscape(string(raw)))
	return req
}

func credentialDeleteRecord(t *testing.T, db *sql.DB, source string) (int64, string, sql.NullInt64) {
	t.Helper()
	var rev int64
	var lifecycle string
	var retired sql.NullInt64
	err := db.QueryRow(`select i.revision, i.lifecycle, b.retired_at_ms
		from gateway_credential_source_bindings b join gateway_credential_identities i on i.id = b.credential_id
		where b.source_auth_id = ? and b.retired_at_ms is null or
			(b.source_auth_id = ? and i.lifecycle = 'superseded') order by b.binding_id desc limit 1`,
		source, source).Scan(&rev, &lifecycle, &retired)
	if err != nil {
		t.Fatal(err)
	}
	return rev, lifecycle, retired
}

func pendingCredentialDeleteCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`select count(*) from gateway_credential_delete_intents`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestCredentialDeleteCPA500AfterPhysicalRemovalStillFinalizesAndReturnsFailure(t *testing.T) {
	cpa := &credentialDeleteCPA{members: credentialDeleteMembers("A", "B", "C"), deleteStatus: 500, remainingAfterDelete: 0}
	upstream := httptest.NewServer(cpa)
	defer upstream.Close()
	svc, db, st := newCredentialDeleteProxyFixture(t, upstream.URL, "A", "B", "C")
	cpa.beforeDelete = func() {
		if pendingCredentialDeleteCount(t, db) != 1 {
			t.Error("upstream received delete before durable intent commit")
		}
	}
	if err := st.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName: "shared.json", AuthIndex: "idx-A",
	}); err != nil {
		t.Fatal(err)
	}
	response := proxyAPIKeyRequest(svc, credentialDeleteRequest(t, "A", "B", "C"))
	if response.Code != 500 {
		t.Fatalf("caller status=%d body=%s", response.Code, response.Body.String())
	}
	if pendingCredentialDeleteCount(t, db) != 0 {
		t.Fatal("successful physical deletion left pending intent")
	}
	for _, source := range []string{"A", "B", "C"} {
		rev, lifecycle, retired := credentialDeleteRecord(t, db, source)
		if rev != 2 || lifecycle != "superseded" || !retired.Valid {
			t.Fatalf("%s: revision=%d lifecycle=%s retired=%v", source, rev, lifecycle, retired)
		}
	}
	owners, err := st.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil || len(owners) != 0 {
		t.Fatalf("successful delete ownership=%v err=%v", owners, err)
	}
}

func TestCredentialDeleteTransportErrorAfterRemovalStillFinalizesAndReturnsFailure(t *testing.T) {
	cpa := &credentialDeleteCPA{members: credentialDeleteMembers("A"), remainingAfterDelete: 0, transportFailure: true}
	upstream := httptest.NewServer(cpa)
	defer upstream.Close()
	svc, db, _ := newCredentialDeleteProxyFixture(t, upstream.URL, "A")
	response := proxyAPIKeyRequest(svc, credentialDeleteRequest(t, "A"))
	if response.Code < 400 || pendingCredentialDeleteCount(t, db) != 0 {
		t.Fatalf("transport failure status=%d pending=%d", response.Code, pendingCredentialDeleteCount(t, db))
	}
	rev, lifecycle, retired := credentialDeleteRecord(t, db, "A")
	if rev != 2 || lifecycle != "superseded" || !retired.Valid {
		t.Fatalf("transport failure did not finalize: %d %s %v", rev, lifecycle, retired)
	}
}

func TestCredentialDeleteNotAppliedRestoresOwnershipAndUnknownSuppresses(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		remaining     int
		wantStatus    int
		wantPending   int
		wantOwnership int
	}{
		{"not applied", 500, 2, 500, 0, 1},
		{"2xx not applied", 200, 2, http.StatusBadGateway, 0, 1},
		{"mixed unknown", 200, 1, http.StatusBadGateway, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cpa := &credentialDeleteCPA{members: credentialDeleteMembers("A", "B"), deleteStatus: tc.status, remainingAfterDelete: tc.remaining}
			upstream := httptest.NewServer(cpa)
			defer upstream.Close()
			svc, db, st := newCredentialDeleteProxyFixture(t, upstream.URL, "A", "B")
			if err := st.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
				FileName: "shared.json", AuthIndex: "idx-A",
			}); err != nil {
				t.Fatal(err)
			}
			response := proxyAPIKeyRequest(svc, credentialDeleteRequest(t, "A", "B"))
			if response.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s want=%d", response.Code, response.Body.String(), tc.wantStatus)
			}
			if tc.wantPending == 1 && !strings.Contains(response.Body.String(), "credential_delete_outcome_unknown") {
				t.Fatalf("unknown response lacks stable code: %s", response.Body.String())
			}
			if got := pendingCredentialDeleteCount(t, db); got != tc.wantPending {
				t.Fatalf("pending=%d want=%d", got, tc.wantPending)
			}
			owners, err := st.ListCodexInspectionDisableOwnership(context.Background())
			if err != nil || len(owners) != tc.wantOwnership {
				t.Fatalf("ownership=%v err=%v", owners, err)
			}
			for _, source := range []string{"A", "B"} {
				rev, lifecycle, retired := credentialDeleteRecord(t, db, source)
				if rev != 1 || lifecycle != "active" || retired.Valid {
					t.Fatalf("%s terminalized on %s: %d %s %v", source, tc.name, rev, lifecycle, retired)
				}
			}
		})
	}
}

func TestCredentialDeletePersistentGuardCoversAuthFileMutationShapes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "guard.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Identities.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		Credentials: []ports.CredentialSnapshotItem{{SourceAuthID: "A"}}, NowMS: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	repo := st.Identities.(ports.CredentialDeleteRepository)
	if _, err := repo.PrepareCredentialDelete(context.Background(), ports.PrepareCredentialDeleteParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, PhysicalName: "shared.json",
		SourceAuthIDs: []string{"A"}, OwnerInstance: "previous-process", NowMS: 2000,
	}); err != nil {
		t.Fatal(err)
	}
	svc := New(nil, st) // fresh coordinator after Manager restart
	deletes, err := credentialdeletemutation.NewService(credentialdeletemutation.Config{
		RuntimeObserver: readyAPIKeyRuntime{}, Repository: repo, ProcessInstanceID: "new-process",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetCredentialDeleteService(deletes)
	cases := []struct {
		name     string
		mutation authFileOwnershipMutation
		conflict bool
	}{
		{"same file delete", authFileOwnershipMutation{fileNames: []string{"shared.json"}}, true},
		{"same file status", authFileOwnershipMutation{statusMutation: &authFileStatusMutation{physicalName: "shared.json"}}, true},
		{"same file fields", authFileOwnershipMutation{fieldsMutation: &authFileFieldsMutation{identity: cpaauthfiles.Identity{AuthFileName: "shared.json"}}}, true},
		{"same file upload", authFileOwnershipMutation{writeMutation: &authFileWriteMutation{physicalName: "shared.json"}}, true},
		{"unrelated file", authFileOwnershipMutation{fileNames: []string{"other.json"}}, false},
		{"clear all", authFileOwnershipMutation{clearAll: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release, err := svc.acquireAuthFileMutation(context.Background(), tc.mutation)
			if (err != nil) != tc.conflict {
				t.Fatalf("overlap error=%v want conflict=%t", err, tc.conflict)
			}
			if release != nil {
				release()
			}
		})
	}
}

func TestCredentialDeleteRevalidationDriftDoesNotForwardOrLeaveIntent(t *testing.T) {
	var gets, deletes int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets++
			members := credentialDeleteMembers("A")
			if gets > 1 {
				members = credentialDeleteMembers("B")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"files": members})
		case http.MethodDelete:
			deletes++
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer upstream.Close()
	svc, db, st := newCredentialDeleteProxyFixture(t, upstream.URL, "A")
	if err := st.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName: "shared.json", AuthIndex: "idx-A",
	}); err != nil {
		t.Fatal(err)
	}
	response := proxyAPIKeyRequest(svc, credentialDeleteRequest(t, "A"))
	if response.Code < 400 || deletes != 0 || gets < 2 || pendingCredentialDeleteCount(t, db) != 0 {
		t.Fatalf("drift status=%d GET=%d DELETE=%d pending=%d", response.Code, gets, deletes, pendingCredentialDeleteCount(t, db))
	}
	owners, err := st.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil || len(owners) != 1 {
		t.Fatalf("failed prepare did not restore ownership: %v %v", owners, err)
	}
}

func TestCredentialDeleteRuntimeOnlyMembersDoNotRequireRuntimeFence(t *testing.T) {
	members := credentialDeleteMembers("A", "B")
	for _, member := range members {
		member["runtime_only"] = true
	}
	cpa := &credentialDeleteCPA{members: members, deleteStatus: 200, remainingAfterDelete: 0}
	upstream := httptest.NewServer(cpa)
	defer upstream.Close()
	svc, db, _ := newCredentialDeleteProxyFixture(t, upstream.URL)
	deletes, err := credentialdeletemutation.NewService(credentialdeletemutation.Config{
		RuntimeObserver:   unavailableAPIKeyRuntime{},
		Repository:        svc.store.Identities.(ports.CredentialDeleteRepository),
		ProcessInstanceID: "process-A",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetCredentialDeleteService(deletes)
	response := proxyAPIKeyRequest(svc, credentialDeleteRequest(t, "A", "B"))
	if response.Code != 200 || pendingCredentialDeleteCount(t, db) != 0 {
		t.Fatalf("runtime-only delete status=%d pending=%d body=%s",
			response.Code, pendingCredentialDeleteCount(t, db), response.Body.String())
	}
}

func TestExternalCredentialDeleteKeepsPassiveCanonicalSemantics(t *testing.T) {
	cpa := &credentialDeleteCPA{members: credentialDeleteMembers("A"), deleteStatus: 200, remainingAfterDelete: 0}
	upstream := httptest.NewServer(cpa)
	defer upstream.Close()
	_, db, st := newCredentialDeleteProxyFixture(t, upstream.URL, "A")
	external := New(managerconfig.New(config.Config{}, st, nil), st)
	response := proxyAPIKeyRequest(external, credentialDeleteRequest(t, "A"))
	if response.Code != 200 || pendingCredentialDeleteCount(t, db) != 0 {
		t.Fatalf("external delete status=%d pending=%d", response.Code, pendingCredentialDeleteCount(t, db))
	}
	rev, lifecycle, retired := credentialDeleteRecord(t, db, "A")
	if rev != 1 || lifecycle != "active" || retired.Valid {
		t.Fatalf("external mode terminalized credential: %d %s %v", rev, lifecycle, retired)
	}
}
