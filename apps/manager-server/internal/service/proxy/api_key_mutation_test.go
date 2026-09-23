package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/cpaidentityinventory"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identitymutation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type readyAPIKeyRuntime struct{}

func (readyAPIKeyRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	return model.RuntimeObservedStatus{State: model.RuntimeStateReady, Identity: "runtime-1", Generation: 1}, nil
}

type unavailableAPIKeyRuntime struct{}

func (unavailableAPIKeyRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	return model.RuntimeObservedStatus{}, errors.New("runtime unavailable")
}

func keyHash(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}

func newAPIKeyProxyFixture(t *testing.T, upstreamURL, oldRaw string) (*Service, *sql.DB, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api-key-mutation.sqlite")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SaveSetup(context.Background(), store.Setup{CPAUpstreamURL: upstreamURL, ManagementKey: "management-key"}); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = st.Identities.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: keyHash(oldRaw)}}, NowMS: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	mutations, err := identitymutation.NewService(identitymutation.Config{
		RuntimeObserver: readyAPIKeyRuntime{}, InventoryClient: cpaidentityinventory.New(nil, nil),
		Repository: st.Identities.(ports.MutationRepository), ProcessInstanceID: "process-A",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(managerconfig.New(config.Config{}, st, nil), st)
	svc.SetAPIKeyMutationService(mutations)
	return svc, db, st
}

func proxyAPIKeyRequest(svc *Service, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	svc.ProxyManagement(recorder, request, func(w http.ResponseWriter, status int, err error) {
		http.Error(w, err.Error(), status)
	})
	return recorder
}

func TestInspectAPIKeyMutationClassification(t *testing.T) {
	cases := []struct {
		name, method, url, body string
		want                    ports.APIKeyMutationKind
		wantErr                 bool
	}{
		{"pure rotation", http.MethodPatch, "/v0/management/api-keys", `{"old":"a","new":"b"}`, ports.APIKeyMutationRotate, false},
		{"mixed index", http.MethodPatch, "/v0/management/api-keys", `{"old":"a","new":"b","index":999,"value":"c"}`, "", true},
		{"index patch", http.MethodPatch, "/v0/management/api-keys", `{"index":0,"value":"b"}`, "", false},
		{"duplicate old", http.MethodPatch, "/v0/management/api-keys", `{"old":"a","old":"b","new":"c"}`, "", true},
		{"unknown field", http.MethodPatch, "/v0/management/api-keys", `{"old":"a","new":"b","extra":"x"}`, "", true},
		{"case variant", http.MethodPatch, "/v0/management/api-keys", `{"Old":"a","new":"b"}`, "", true},
		{"whole list", http.MethodPut, "/v0/management/api-keys", `["a","b"]`, "", false},
		{"value delete", http.MethodDelete, "/v0/management/api-keys?value=a", "", ports.APIKeyMutationDelete, false},
		{"index delete", http.MethodDelete, "/v0/management/api-keys?index=0", "", "", false},
		{"mixed delete", http.MethodDelete, "/v0/management/api-keys?index=999&value=a", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.url, strings.NewReader(tc.body))
			shape, err := inspectAPIKeyMutation(req)
			if (err != nil) != tc.wantErr || shape.kind != tc.want {
				t.Fatalf("shape=%+v err=%v want=%q", shape, err, tc.want)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil || string(body) != tc.body {
				t.Fatalf("request body was changed: %v", err)
			}
		})
	}
}

func TestAmbiguousAPIKeyMutationsNeverForward(t *testing.T) {
	var forwarded atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	svc, db, _ := newAPIKeyProxyFixture(t, upstream.URL, "old")
	requests := []*http.Request{
		httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"index":999,"value":"x","old":"old","new":"new"}`)),
		httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?index=999&value=old", nil),
		httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"old":"a","old":"old","new":"new"}`)),
		httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"old":"old","new":"new","extra":"x"}`)),
		httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(strings.Repeat("x", maxAPIKeyMutationInspectionBytes+1))),
	}
	for _, request := range requests {
		response := proxyAPIKeyRequest(svc, request)
		if response.Code != http.StatusConflict {
			t.Fatalf("unsafe request status=%d", response.Code)
		}
	}
	if forwarded.Load() != 0 {
		t.Fatalf("unsafe request reached CPA %d times", forwarded.Load())
	}
	var pending int
	if err := db.QueryRow("select count(*) from " + sqlite.GatewayAPIKeyMutationIntentsTable).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("unsafe request left intent: %d %v", pending, err)
	}
}

func TestAPIKeyRotationIntentCommittedBeforeUpstreamAndFinalized(t *testing.T) {
	const oldRaw = "secret-old-9123"
	const newRaw = "secret-new-7645"
	forwarded := make(chan struct{})
	allowForward := make(chan struct{})
	var mu sync.Mutex
	current := oldRaw
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			value := current
			mu.Unlock()
			fmt.Fprintf(w, `{"api-keys":[%q]}`, value)
		case http.MethodPatch:
			close(forwarded)
			<-allowForward
			mu.Lock()
			current = newRaw
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	svc, db, st := newAPIKeyProxyFixture(t, upstream.URL, oldRaw)
	before, _, err := st.Identities.FindActiveAPIKeyBySource(context.Background(), "runtime-1", keyHash(oldRaw))
	if err != nil {
		t.Fatal(err)
	}
	var capturedLog bytes.Buffer
	oldLogWriter := log.Writer()
	log.SetOutput(&capturedLog)
	defer log.SetOutput(oldLogWriter)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- proxyAPIKeyRequest(svc, httptest.NewRequest(http.MethodPatch,
			"/v0/management/api-keys", strings.NewReader(`{"old":"`+oldRaw+`","new":"`+newRaw+`"}`)))
	}()
	select {
	case <-forwarded:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream mutation did not arrive")
	}
	var oldPersisted, newPersisted string
	if err := db.QueryRow("select old_api_key_hash, new_api_key_hash from "+
		sqlite.GatewayAPIKeyMutationIntentsTable).Scan(&oldPersisted, &newPersisted); err != nil {
		t.Fatalf("upstream proceeded without committed intent: %v", err)
	}
	if oldPersisted != keyHash(oldRaw) || newPersisted != keyHash(newRaw) {
		t.Fatal("intent did not contain expected safe hashes")
	}
	close(allowForward)
	response := <-done
	if response.Code != http.StatusOK {
		t.Fatalf("rotation status=%d body=%q", response.Code, response.Body.String())
	}
	after, binding, err := st.Identities.FindActiveAPIKeyBySource(context.Background(), "runtime-1", keyHash(newRaw))
	if err != nil || after.ID != before.ID || after.Revision != before.Revision+1 ||
		binding.ObservedRuntimeGeneration != 1 {
		t.Fatalf("rotation state: %+v %+v %v", after, binding, err)
	}
	var pending int
	if err := db.QueryRow("select count(*) from " + sqlite.GatewayAPIKeyMutationIntentsTable).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("intent count=%d err=%v", pending, err)
	}
	if strings.Contains(capturedLog.String(), oldRaw) || strings.Contains(capturedLog.String(), newRaw) ||
		strings.Contains(response.Body.String(), oldRaw) || strings.Contains(response.Body.String(), newRaw) {
		t.Fatal("raw API key leaked into log or response")
	}
	var dbSeq int
	var dbName, dbPath string
	if err := db.QueryRow("pragma database_list").Scan(&dbSeq, &dbName, &dbPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("pragma wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dbPath, dbPath + "-wal"} {
		content, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, []byte(oldRaw)) || bytes.Contains(content, []byte(newRaw)) {
			t.Fatal("raw API key was persisted to SQLite")
		}
	}
}

func TestAPIKeyRotationPreflightRejectsUnsafeEvidence(t *testing.T) {
	cases := []struct {
		name string
		keys []string
	}{
		{"exact old absent", []string{"  old  "}},
		{"normalized old duplicate", []string{"old", " old "}},
		{"new already present", []string{"old", "new"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var forwarded int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch {
					forwarded++
					w.WriteHeader(http.StatusOK)
					return
				}
				fmt.Fprintf(w, `{"api-keys":[`)
				for i, key := range tc.keys {
					if i > 0 {
						fmt.Fprint(w, ",")
					}
					fmt.Fprintf(w, "%q", key)
				}
				fmt.Fprint(w, "]}")
			}))
			defer upstream.Close()
			svc, db, _ := newAPIKeyProxyFixture(t, upstream.URL, "old")
			response := proxyAPIKeyRequest(svc, httptest.NewRequest(http.MethodPatch,
				"/v0/management/api-keys", strings.NewReader(`{"old":"old","new":"new"}`)))
			if response.Code != http.StatusConflict || forwarded != 0 {
				t.Fatalf("unsafe mutation forwarded: status=%d forwarded=%d", response.Code, forwarded)
			}
			var pending int
			if err := db.QueryRow("select count(*) from " + sqlite.GatewayAPIKeyMutationIntentsTable).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("unsafe intent persisted: %d %v", pending, err)
			}
		})
	}
}

func TestAPIKeyRotationTransportErrorAfterCPAWriteFinalizes(t *testing.T) {
	const oldRaw, newRaw = "transport-old-secret", "transport-new-secret"
	var mu sync.Mutex
	current := oldRaw
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"api-keys":[%q]}`, current)
			return
		}
		current = newRaw
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()
	svc, db, st := newAPIKeyProxyFixture(t, upstream.URL, oldRaw)
	before, _, err := st.Identities.FindActiveAPIKeyBySource(context.Background(), "runtime-1", keyHash(oldRaw))
	if err != nil {
		t.Fatal(err)
	}
	response := proxyAPIKeyRequest(svc, httptest.NewRequest(http.MethodPatch,
		"/v0/management/api-keys", strings.NewReader(`{"old":"`+oldRaw+`","new":"`+newRaw+`"}`)))
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "api_key_mutation_outcome_unknown") ||
		strings.Contains(response.Body.String(), oldRaw) || strings.Contains(response.Body.String(), newRaw) {
		t.Fatalf("unsafe transport response: %d %q", response.Code, response.Body.String())
	}
	after, _, err := st.Identities.FindActiveAPIKeyBySource(context.Background(), "runtime-1", keyHash(newRaw))
	if err != nil || after.ID != before.ID || after.Revision != before.Revision+1 {
		t.Fatalf("transport error lost identity: %+v %v", after, err)
	}
	var pending int
	if err := db.QueryRow("select count(*) from " + sqlite.GatewayAPIKeyMutationIntentsTable).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("transport error left intent: %d %v", pending, err)
	}
}

func TestAPIKeyMutationUnknownKeepsIntentAndBlocksUnsupportedOverlap(t *testing.T) {
	const oldRaw, newRaw = "secret-old-9123", "secret-new-7645"
	var mu sync.Mutex
	getCount := 0
	forwarded := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			getCount++
			count := getCount
			mu.Unlock()
			if count == 1 {
				fmt.Fprintf(w, `{"api-keys":[%q]}`, oldRaw)
			} else {
				fmt.Fprint(w, `{"api-keys":null}`)
			}
		case http.MethodPatch:
			mu.Lock()
			forwarded++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			mu.Lock()
			forwarded++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer upstream.Close()
	svc, db, st := newAPIKeyProxyFixture(t, upstream.URL, oldRaw)
	response := proxyAPIKeyRequest(svc, httptest.NewRequest(http.MethodPatch,
		"/v0/management/api-keys", strings.NewReader(`{"old":"`+oldRaw+`","new":"`+newRaw+`"}`)))
	if response.Code != http.StatusBadGateway ||
		!strings.Contains(response.Body.String(), "api_key_mutation_outcome_unknown") {
		t.Fatalf("unknown outcome response: %d %q", response.Code, response.Body.String())
	}
	mutations, err := identitymutation.NewService(identitymutation.Config{
		RuntimeObserver: unavailableAPIKeyRuntime{}, InventoryClient: cpaidentityinventory.New(nil, nil),
		Repository: st.Identities.(ports.MutationRepository), ProcessInstanceID: "process-A",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAPIKeyMutationService(mutations)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPut, "/v0/management/api-keys", strings.NewReader(`["another"]`)),
		httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"index":0,"value":"another"}`)),
		httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?index=0", nil),
	} {
		response := proxyAPIKeyRequest(svc, request)
		if response.Code != http.StatusConflict {
			t.Fatalf("overlap %s status=%d", request.Method, response.Code)
		}
	}
	mu.Lock()
	count := forwarded
	mu.Unlock()
	if count != 1 {
		t.Fatalf("overlap reached upstream: %d", count)
	}
	var pending int
	if err := db.QueryRow("select count(*) from " + sqlite.GatewayAPIKeyMutationIntentsTable).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("unknown intent lost: %d %v", pending, err)
	}
	if strings.Contains(response.Body.String(), oldRaw) || strings.Contains(response.Body.String(), newRaw) {
		t.Fatal("unknown outcome exposed raw key")
	}
	_, err = st.Identities.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2, ProcessInstanceID: "process-B",
		CaptureStartedAtMS: time.Now().UnixMilli() + 1, NowMS: time.Now().UnixMilli() + 2,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: keyHash(newRaw)}},
	})
	if err != nil {
		t.Fatalf("crash recovery from unknown: %v", err)
	}
}

func TestAPIKeyDeleteByValueSupersedes(t *testing.T) {
	const oldRaw = "delete-secret-5682"
	var mu sync.Mutex
	current := oldRaw
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if current == "" {
				fmt.Fprint(w, `{"api-keys":[]}`)
			} else {
				fmt.Fprintf(w, `{"api-keys":[%q]}`, current)
			}
		case http.MethodDelete:
			current = ""
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	svc, db, st := newAPIKeyProxyFixture(t, upstream.URL, oldRaw)
	before, _, err := st.Identities.FindActiveAPIKeyBySource(context.Background(), "runtime-1", keyHash(oldRaw))
	if err != nil {
		t.Fatal(err)
	}
	response := proxyAPIKeyRequest(svc, httptest.NewRequest(http.MethodDelete,
		"/v0/management/api-keys?value="+oldRaw, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%q", response.Code, response.Body.String())
	}
	after, err := st.Identities.LoadAPIKeyByID(context.Background(), before.ID)
	if err != nil || after.Revision != before.Revision+1 || after.Lifecycle != "superseded" {
		t.Fatalf("delete final state: %+v %v", after, err)
	}
	var retiredAt sql.NullInt64
	if err := db.QueryRow("select retired_at_ms from gateway_api_key_source_bindings where api_key_hash = ?", keyHash(oldRaw)).Scan(&retiredAt); err != nil ||
		!retiredAt.Valid {
		t.Fatalf("deleted binding not retired: %+v %v", retiredAt, err)
	}
	var pending int
	if err := db.QueryRow("select count(*) from " + sqlite.GatewayAPIKeyMutationIntentsTable).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("delete intent remains: %d %v", pending, err)
	}
}

func TestRepresentationOnlyAndUnsupportedFormsRemainTransportOnly(t *testing.T) {
	const oldRaw = "  source-key  "
	var mu sync.Mutex
	forwarded := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet {
			t.Error("transport-only mutation unexpectedly fetched API-key preflight")
		}
		forwarded++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	svc, db, st := newAPIKeyProxyFixture(t, upstream.URL, oldRaw)
	mutations, err := identitymutation.NewService(identitymutation.Config{
		RuntimeObserver: unavailableAPIKeyRuntime{}, InventoryClient: cpaidentityinventory.New(nil, nil),
		Repository: st.Identities.(ports.MutationRepository), ProcessInstanceID: "process-A",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAPIKeyMutationService(mutations)
	before, _, err := st.Identities.FindActiveAPIKeyBySource(context.Background(), "runtime-1", keyHash(oldRaw))
	if err != nil {
		t.Fatal(err)
	}
	requests := []*http.Request{
		httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"old":"  source-key  ","new":"source-key"}`)),
		httptest.NewRequest(http.MethodPatch, "/v0/management/api-keys", strings.NewReader(`{"index":0,"value":"x"}`)),
		httptest.NewRequest(http.MethodPut, "/v0/management/api-keys", strings.NewReader(`["x"]`)),
		httptest.NewRequest(http.MethodDelete, "/v0/management/api-keys?index=0", nil),
	}
	for _, request := range requests {
		response := proxyAPIKeyRequest(svc, request)
		if response.Code != http.StatusOK {
			t.Fatalf("transport-only status=%d body=%q", response.Code, response.Body.String())
		}
	}
	mu.Lock()
	count := forwarded
	mu.Unlock()
	if count != len(requests) {
		t.Fatalf("forwarded=%d want=%d", count, len(requests))
	}
	var pending int
	if err := db.QueryRow("select count(*) from " + sqlite.GatewayAPIKeyMutationIntentsTable).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("transport-only forms created intent: %d %v", pending, err)
	}
	after, err := st.Identities.LoadAPIKeyByID(context.Background(), before.ID)
	if err != nil || after != before {
		t.Fatalf("transport-only form mutated Canonical identity: %+v %v", after, err)
	}
}

func TestExternalAPIKeyProxyKeepsTransportWithoutCanonicalIntent(t *testing.T) {
	var forwarded atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	dbPath := filepath.Join(t.TempDir(), "external.sqlite")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSetup(context.Background(), store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"}); err != nil {
		t.Fatal(err)
	}
	svc := New(managerconfig.New(config.Config{}, st, nil), st)
	response := proxyAPIKeyRequest(svc, httptest.NewRequest(http.MethodPatch,
		"/v0/management/api-keys", strings.NewReader(`{"old":"old","new":"new"}`)))
	if response.Code != http.StatusOK || !forwarded.Load() {
		t.Fatalf("External transport changed: status=%d forwarded=%v", response.Code, forwarded.Load())
	}
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var pending int
	if err := db.QueryRow("select count(*) from " + sqlite.GatewayAPIKeyMutationIntentsTable).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("External created Canonical intent: %d %v", pending, err)
	}
}
