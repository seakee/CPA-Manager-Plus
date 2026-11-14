package freshrouting

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/fullweb"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
)

func newInitializedTwoPrefixStore(t *testing.T, strategy bootstrap.CPAStrategy) (freshstore.Store, bootstrap.FreshIdentity, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqlite.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := freshstore.New(db)
	id := bootstrap.FreshIdentity{
		AdmissionID:    "admission-2p",
		InstallationID: "install-2p",
		Generation:     "gen-2p",
		OperationID:    "operation-two-prefixes",
		DatabasePath:   p,
	}
	adminKey := "cpamp_RoutingAdmin0123456789!"
	cred, err := security.NewAdminCredential(adminKey, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Create(t.Context(), id, &cred)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Operation.Routing.SuggestedControlBasePath == "" || rec.Operation.Routing.SuggestedInferenceBasePath == "" {
		t.Fatalf("expected persisted CSPRNG suggestions on Create, got %+v", rec.Operation.Routing)
	}
	if strategy != "" {
		// The strategy checkpoint is the real one for each strategy: a Manager
		// staged destination carries its artifact digest, and an External CPA is
		// named by the content digest of the endpoint it was validated against
		// (never by a bare URL field the checkpoint does not have).
		_, err = store.Update(t.Context(), id, rec.Operation.Revision, func(r *freshstore.Record) error {
			checkpoint := bootstrap.FreshStrategyCheckpoint{
				Strategy:         strategy,
				Stage:            "committed",
				TargetIdentity:   string(id.OperationID),
				TargetGeneration: 1,
			}
			switch strategy {
			case bootstrap.NewManaged:
				checkpoint.TargetArtifactID = "sha256:" + strings.Repeat("a", 64)
			case bootstrap.ConnectExternal:
				digest := sha256.Sum256([]byte("https://external-cpa.example.com"))
				checkpoint.TargetIdentity = "external:sha256:" + hex.EncodeToString(digest[:])
				protector, protectErr := security.NewProtector([]byte("routing-external-fixture"))
				if protectErr != nil {
					return protectErr
				}
				protected, protectErr := protector.ProtectString(strings.Repeat("e", 64))
				if protectErr != nil {
					return protectErr
				}
				r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "https://external-cpa.example.com", ManagementKey: protected}
			}
			r.Operation.Strategy = checkpoint
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return store, id, adminKey
}

func TestManagedTwoPrefixPrepareActivateRecoveryAndSnapshot(t *testing.T) {
	store, id, _ := newInitializedTwoPrefixStore(t, bootstrap.NewManaged)
	driver := &routeDriver{verifyErr: ErrUnverified}
	svc := New(store, driver)

	// Managed strategy rejects missing inferenceTarget.
	if _, err := svc.PrepareAccess(t.Context(), id, AccessIntent{Target: "/cpamp-admin"}); err == nil {
		t.Fatal("managed PrepareAccess accepted empty inferenceTarget")
	} else if code, ok := bootstrap.AccessPrefixCodeOf(err); !ok || code != bootstrap.CodeInferencePrefixRequired {
		t.Fatalf("code = %q (ok=%t), want %s", code, ok, bootstrap.CodeInferencePrefixRequired)
	}

	// Identical prefixes without sharedEntry rejected; distinct valid pair accepted.
	if _, err := svc.PrepareAccess(t.Context(), id, AccessIntent{Target: "/same", InferenceTarget: "/same", SharedEntry: false}); err == nil {
		t.Fatal("unconfirmed shared prefix accepted")
	}
	op, err := svc.PrepareAccess(t.Context(), id, AccessIntent{
		Target:          "/cpamp-admin",
		InferenceTarget: "/cpamp-api",
		SharedEntry:     false,
	})
	if err != nil {
		t.Fatalf("PrepareAccess failed: %v", err)
	}
	if op.Routing.Stage != bootstrap.PrefixIntentPersisted || op.Routing.Committed || op.Routing.InferenceCommitted {
		t.Fatalf("unexpected prepared routing state: %+v", op.Routing)
	}

	// First Activate fails verification after persisting PrefixRouteActivated; checkpoint survives restart.
	if _, err := svc.Activate(t.Context(), id); !errors.Is(err, ErrUnverified) {
		t.Fatalf("expected ErrUnverified, got %v", err)
	}
	reloaded, err := store.Load(t.Context())
	if err != nil || reloaded.Operation.Routing.Stage != bootstrap.PrefixRouteActivated ||
		reloaded.Operation.Routing.Target != "/cpamp-admin" || reloaded.Operation.Routing.InferenceTarget != "/cpamp-api" {
		t.Fatalf("checkpoint lost across interrupted activation: %+v (err=%v)", reloaded.Operation.Routing, err)
	}

	// Resume after verification passes commits both prefixes atomically.
	driver.verifyErr = nil
	committed, err := New(store, driver).Activate(t.Context(), id)
	if err != nil {
		t.Fatalf("resumed Activate failed: %v", err)
	}
	if !committed.Routing.Committed || !committed.Routing.InferenceCommitted || committed.MissingInferenceCompletionRequired() {
		t.Fatalf("expected committed dual prefixes, got %+v", committed.Routing)
	}

	snap := svc.Snapshot(t.Context())
	if ctrl, ok := snap.EffectiveControlBasePath(); !ok || ctrl != "/cpamp-admin" {
		t.Fatalf("EffectiveControlBasePath = (%q, %t)", ctrl, ok)
	}
	if inf, ok := snap.EffectiveInferenceBasePath(); !ok || inf != "/cpamp-api" {
		t.Fatalf("EffectiveInferenceBasePath = (%q, %t)", inf, ok)
	}
}

func TestExternalStrategyRejectsInferencePrefixAndCommitsControlOnly(t *testing.T) {
	store, id, _ := newInitializedTwoPrefixStore(t, bootstrap.ConnectExternal)
	driver := &routeDriver{}
	svc := New(store, driver)

	if _, err := svc.PrepareAccess(t.Context(), id, AccessIntent{Target: "/cpamp-admin", InferenceTarget: "/cpamp-api"}); err == nil {
		t.Fatal("external strategy accepted managed inferenceTarget")
	} else if code, ok := bootstrap.AccessPrefixCodeOf(err); !ok || code != bootstrap.CodeExternalInferencePrefixForbidden {
		t.Fatalf("code = %q, want %s", code, bootstrap.CodeExternalInferencePrefixForbidden)
	}

	if _, err := svc.PrepareAccess(t.Context(), id, AccessIntent{Target: "/cpamp-admin"}); err != nil {
		t.Fatalf("external control-only prepare failed: %v", err)
	}
	committed, err := svc.Activate(t.Context(), id)
	if err != nil {
		t.Fatalf("external activate failed: %v", err)
	}
	if !committed.Routing.Committed || committed.Routing.InferenceCommitted || committed.MissingInferenceCompletionRequired() {
		t.Fatalf("unexpected external routing checkpoint: %+v", committed.Routing)
	}
	snap := svc.Snapshot(t.Context())
	if _, ok := snap.EffectiveControlBasePath(); !ok {
		t.Fatal("external EffectiveControlBasePath not ready")
	}
	if inf, ok := snap.EffectiveInferenceBasePath(); ok || inf != "" {
		t.Fatalf("external EffectiveInferenceBasePath = (%q, %t), want empty", inf, ok)
	}
}

func TestExistingV2MissingInferenceOneTimeAuthenticatedCompletionAndNoUnauthLeak(t *testing.T) {
	store, id, adminKey := newInitializedTwoPrefixStore(t, bootstrap.NewManaged)
	// Simulate an existing installed v2 Managed store committed before InferenceTarget existed.
	rec, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rec, err = store.Update(t.Context(), id, rec.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = id.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = "/existing-console"
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		r.Operation.ProductComplete = true
		return nil
	})
	if err != nil {
		t.Fatalf("seed pre-inference v2 store: %v", err)
	}

	driver := &routeDriver{}
	svc := New(store, driver)

	// 1. Control route stays open, inference stays closed, unauthenticated HTML/PublicIdentity never leak prefixes.
	snap := svc.Snapshot(t.Context())
	if ctrl, ok := snap.EffectiveControlBasePath(); !ok || ctrl != "/existing-console" || !snap.MissingInferenceCompletionRequired {
		t.Fatalf("unexpected pre-completion snapshot: %+v (ctrl=%q ok=%t)", snap, ctrl, ok)
	}
	if inf, ok := snap.EffectiveInferenceBasePath(); ok || inf != "" {
		t.Fatalf("pre-completion EffectiveInferenceBasePath = (%q, %t), want closed", inf, ok)
	}

	dist := fstest.MapFS{
		"index.html":               {Data: []byte(`<!doctype html><html><body><div id="root"></div><script src="./assets/index-AbCd1234.js"></script><script>__CPAMP_ROUTING_CONTEXT__</script></body></html>`)},
		"assets/index-AbCd1234.js": {Data: []byte(`console.log("ok")`)},
	}
	htmlReq := httptest.NewRequest(http.MethodGet, "/existing-console", nil)
	htmlReq.Header.Set("Accept", "text/html")
	htmlRec := httptest.NewRecorder()
	(fullweb.Handler{Snapshot: snap, Dist: dist}).ServeHTTP(htmlRec, htmlReq)
	if htmlRec.Code != http.StatusOK || !strings.Contains(htmlRec.Body.String(), `"inferenceCompletionRequired":true`) ||
		strings.Contains(htmlRec.Body.String(), "suggestedInferenceBasePath") {
		t.Fatalf("unexpected unauthenticated HTML context: %d %s", htmlRec.Code, htmlRec.Body.String())
	}

	validateAdmin := func(authHeader string) func(freshstore.Record) error {
		bearer := security.ExtractBearerToken(authHeader)
		return func(cur freshstore.Record) error {
			if !cur.Operation.Routing.Committed || cur.Admin == nil || !security.VerifyAdminKey(*cur.Admin, bearer) {
				return fresh.ErrAuthority
			}
			return nil
		}
	}

	// 2. Unauthenticated GET/POST completion fails closed (401).
	unauthRec := httptest.NewRecorder()
	svc.ServeAccessCompletion(unauthRec, httptest.NewRequest(http.MethodGet, "/existing-console/api/v2/installation/access/completion", nil), rec, validateAdmin(""))
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth GET completion = %d, want 401", unauthRec.Code)
	}

	// 3. Authenticated GET generates and persists a 16-char CSPRNG suggestion.
	getRec := httptest.NewRecorder()
	svc.ServeAccessCompletion(getRec, httptest.NewRequest(http.MethodGet, "/existing-console/api/v2/installation/access/completion", nil), rec, validateAdmin("Bearer "+adminKey))
	if getRec.Code != http.StatusOK {
		t.Fatalf("auth GET completion = %d: %s", getRec.Code, getRec.Body.String())
	}
	var initialStatus AccessCompletionStatus
	if err := json.Unmarshal(getRec.Body.Bytes(), &initialStatus); err != nil {
		t.Fatal(err)
	}
	if !initialStatus.Required || initialStatus.SuggestedInferenceBasePath == "" {
		t.Fatalf("expected required=true and suggestion, got %+v", initialStatus)
	}

	// 4. Authenticated POST completes inference prefix once and opens EffectiveInferenceBasePath.
	latest, _ := store.Load(t.Context())
	postBody := bytes.NewBufferString(`{"inferenceBasePath":"/cpamp-api","sharedEntry":false}`)
	postRec := httptest.NewRecorder()
	svc.ServeAccessCompletion(postRec, httptest.NewRequest(http.MethodPost, "/existing-console/api/v2/installation/access/completion", postBody), latest, validateAdmin("Bearer "+adminKey))
	if postRec.Code != http.StatusOK {
		t.Fatalf("auth POST completion = %d: %s", postRec.Code, postRec.Body.String())
	}
	var completedStatus AccessCompletionStatus
	if err := json.Unmarshal(postRec.Body.Bytes(), &completedStatus); err != nil {
		t.Fatal(err)
	}
	if completedStatus.Required || !completedStatus.InferenceCommitted || completedStatus.InferenceBasePath != "/cpamp-api" {
		t.Fatalf("unexpected completed status: %+v", completedStatus)
	}

	// PublicIdentity still omits InferenceTarget after completion.
	afterRecord, _ := store.Load(t.Context())
	// A lost successful reply can be retried without another write or a new path.
	replayRec := httptest.NewRecorder()
	svc.ServeAccessCompletion(replayRec, httptest.NewRequest(http.MethodPost, "/existing-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/cpamp-api","sharedEntry":false}`)), afterRecord, validateAdmin("Bearer "+adminKey))
	if replayRec.Code != http.StatusOK {
		t.Fatalf("committed same-intent replay = %d", replayRec.Code)
	}
	afterReplay, err := store.Load(t.Context())
	if err != nil || afterReplay.Operation.Revision != afterRecord.Operation.Revision {
		t.Fatalf("same-intent replay mutated the canonical operation: %v", err)
	}
	pubJSON, _ := json.Marshal(PublicIdentityFor(afterRecord.Operation))
	if strings.Contains(string(pubJSON), "cpamp-api") || strings.Contains(string(pubJSON), "inferenceTarget") {
		t.Fatalf("PublicIdentity leaked inferenceTarget: %s", string(pubJSON))
	}

	// 5. Second POST is strictly rejected as one-time already committed (409).
	secondRec := httptest.NewRecorder()
	svc.ServeAccessCompletion(secondRec, httptest.NewRequest(http.MethodPost, "/existing-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/second-api","sharedEntry":false}`)), afterRecord, validateAdmin("Bearer "+adminKey))
	if secondRec.Code != http.StatusConflict || !strings.Contains(secondRec.Body.String(), string(bootstrap.CodeInferencePrefixAlreadyCommitted)) {
		t.Fatalf("second POST = %d %s, want 409 %s", secondRec.Code, secondRec.Body.String(), bootstrap.CodeInferencePrefixAlreadyCommitted)
	}
}
