package updatecheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	updatechecksvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/updatecheck"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

type stubLauncher struct {
	staged         []string
	activeSlot     string
	prepareCalls   int
	uploadCalls    int
	activateCalls  int
	rollbackCalls  int
	uploadErrCode  string
	rollbackErr    string
	lastPrepareReq updatechecksvc.LauncherPrepareRequest
}

func (s *stubLauncher) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer launcher-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "unauthorized", "message": "bad token"})
			return
		}
		switch r.URL.Path {
		case "/v1/launcher/slot/status":
			_ = json.NewEncoder(w).Encode(updatechecksvc.LauncherSlotStatus{
				Supported:             true,
				Mode:                  "docker",
				BaseVersion:           "v2.0.0-beta.1",
				BaseEpoch:             1,
				RunningVersion:        "v2.0.0-beta.1",
				ActiveSlot:            s.activeSlot,
				RollbackTargetVersion: "v2.0.0-beta.1",
				RollbackAvailable:     s.activeSlot != "",
				RollbackSameSchema:    true,
				State:                 "none",
				StagedSlots:           s.staged,
			})
		case "/v1/launcher/slot/prepare":
			s.prepareCalls++
			_ = json.NewDecoder(r.Body).Decode(&s.lastPrepareReq)
			s.staged = []string{s.lastPrepareReq.TargetVersion}
			_ = json.NewEncoder(w).Encode(updatechecksvc.SlotPrepareOperation{
				ID:            "prep-1",
				TargetVersion: s.lastPrepareReq.TargetVersion,
				Phase:         "succeeded",
				ReceivedBytes: s.lastPrepareReq.ExpectedSize,
				TotalBytes:    s.lastPrepareReq.ExpectedSize,
			})
		case "/v1/launcher/slot/prepare-upload":
			s.uploadCalls++
			_, _ = io.ReadAll(r.Body)
			if s.uploadErrCode != "" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": s.uploadErrCode, "message": "signature verification failed"})
				return
			}
			ver := r.URL.Query().Get("target_version")
			s.staged = []string{ver}
			_ = json.NewEncoder(w).Encode(updatechecksvc.SlotPrepareOperation{
				ID:            "upload-1",
				TargetVersion: ver,
				Phase:         "succeeded",
			})
		case "/v1/launcher/slot/activate":
			s.activateCalls++
			var req updatechecksvc.LauncherActivateRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(updatechecksvc.SlotActionAccepted{
				Accepted:            true,
				TargetVersion:       req.TargetVersion,
				TrialTimeoutSeconds: 60,
				AttemptID:           "att-handler-01",
			})
		case "/v1/launcher/slot/rollback":
			s.rollbackCalls++
			if s.rollbackErr != "" {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": s.rollbackErr, "message": "cross-schema rollback blocked"})
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(updatechecksvc.SlotActionAccepted{
				Accepted:      true,
				TargetVersion: "v2.0.0-beta.1",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func seedUpdateCheckFixture(t *testing.T, st interface{ SaveUpdateCheck(context.Context, []byte) error }, targetTag string) {
	t.Helper()
	var info updatechecksvc.ReleaseInfo
	info.SchemaVersion = 2
	info.Release.Version = targetTag
	info.Release.Stage = "beta"
	info.Release.SourceCommit = strings.Repeat("a", 40)
	info.Content.Summary = map[string]string{"zh": "摘要", "en": "Summary"}
	info.Content.Notes = map[string]string{
		"zh": updatechecksvc.Repository + "/blob/" + targetTag + "/docs/release-notes/" + targetTag + "-zh.md",
		"en": updatechecksvc.Repository + "/blob/" + targetTag + "/docs/release-notes/" + targetTag + "-en.md",
	}
	info.Update.UpgradeGuideURL = info.Content.Notes["en"]
	verTag := strings.TrimPrefix(targetTag, "v")
	slimDigest := "sha256:" + strings.Repeat("1", 64)
	fullDigest := "sha256:" + strings.Repeat("2", 64)
	info.Distribution.Docker.VersionTag = verTag
	info.Distribution.Docker.Images = []updatechecksvc.DockerImage{
		{Role: "slim", Name: "seakee/cpa-manager-plus", Tag: verTag, Digest: slimDigest, Platforms: []string{"linux/amd64"}},
		{Role: "full", Name: "seakee/cpa-manager-plus", Tag: verTag + "-full", Digest: fullDigest, Platforms: []string{"linux/amd64"}},
		{Role: "slim", Name: "ghcr.io/seakee/cpa-manager-plus", Tag: verTag, Digest: slimDigest, Platforms: []string{"linux/amd64"}},
		{Role: "full", Name: "ghcr.io/seakee/cpa-manager-plus", Tag: verTag + "-full", Digest: fullDigest, Platforms: []string{"linux/amd64"}},
	}
	info.Distribution.Native.Assets = []string{"cpa-manager-plus-native-v2_" + targetTag + "_linux_amd64.tar.gz"}
	info.Distribution.Slot = &updatechecksvc.SlotDistribution{
		Assets: []updatechecksvc.SlotAsset{
			{
				Name:             "cpamp-slot_" + targetTag + "_linux_" + runtime.GOARCH + ".tar.gz",
				Platform:         "linux/" + runtime.GOARCH,
				SHA256:           "sha256:" + strings.Repeat("c", 64),
				Size:             2048,
				MinBaseEpoch:     1,
				SchemaGeneration: 1,
			},
		},
	}
	rawInfo, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(rawInfo)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	statePayload, err := json.Marshal(updatechecksvc.State{
		SchemaVersion: 1,
		Preference:    "beta",
		LastSuccess:   time.Now().UTC(),
		Index: &updatechecksvc.Index{
			SchemaVersion: 2,
			Revision:      2,
			GeneratedAt:   time.Now().UTC(),
			Channels: map[string]*updatechecksvc.Target{
				"stable": nil,
				"rc":     nil,
				"beta": {
					Version:           targetTag,
					SchemaVersion:     2,
					ReleaseInfoPath:   "releases/download/" + targetTag + "/release-info.json",
					ReleaseInfoSHA256: digest,
				},
			},
		},
		Releases:       map[string]updatechecksvc.ReleaseInfo{targetTag: info},
		ReleaseDigests: map[string]string{targetTag: digest},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveUpdateCheck(context.Background(), statePayload); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCheckHandlerSlotRoutesAndAuth(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	seedUpdateCheckFixture(t, st, "v2.0.0-beta.2")

	launcher := &stubLauncher{}
	srv := launcher.server(t)
	defer srv.Close()

	tokenFile := filepath.Join(t.TempDir(), "launcher-token")
	if err := os.WriteFile(tokenFile, []byte("launcher-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := updatechecksvc.NewLauncherClient(srv.URL, tokenFile)
	if err != nil {
		t.Fatal(err)
	}

	svc := updatechecksvc.New(st, "v2.0.0-beta.1", strings.Repeat("b", 40), false)
	svc.WithLauncherClient(client)
	var snapshotFail bool
	svc.WithPreSlotSnapshot(func(_ context.Context, target string) (string, int, error) {
		if snapshotFail {
			return "", 0, errors.New("disk full")
		}
		return filepath.Join(cfg.DataDir, "backups", "pre-slot-"+target), 1, nil
	})

	h := &Handler{App: &app.Context{
		Config:             cfg,
		AdminAuthService:   adminauthsvc.New(cfg, st),
		UpdateCheckService: svc,
	}}

	doReq := func(method, path, contentType string, body io.Reader, authed bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, body)
		if authed {
			req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		h.Handle(rec, req)
		return rec
	}

	t.Run("enforces panel authorization", func(t *testing.T) {
		rec := doReq(http.MethodPost, "/usage-service/updates/slot/prepare", "application/json", strings.NewReader(`{"version":"v2.0.0-beta.2"}`), false)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("GET status retains existing contract and adds self_update", func(t *testing.T) {
		rec := doReq(http.MethodGet, "/usage-service/updates", "", nil, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var out updatechecksvc.Status
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.State != "update_available" || out.SelfUpdate == nil || !out.SelfUpdate.Supported {
			t.Fatalf("unexpected status response: %+v", out)
		}
	})

	t.Run("POST /slot/prepare rejects unknown fields and oversized bodies", func(t *testing.T) {
		rec := doReq(http.MethodPost, "/usage-service/updates/slot/prepare", "application/json", strings.NewReader(`{"version":"v2.0.0-beta.2","extra":1}`), true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 on unknown field, got %d", rec.Code)
		}
		big := `{"version":"` + strings.Repeat("a", 4200) + `"}`
		rec = doReq(http.MethodPost, "/usage-service/updates/slot/prepare", "application/json", strings.NewReader(big), true)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413 on >4KiB body, got %d", rec.Code)
		}
	})

	t.Run("POST /slot/prepare succeeds with cdn and github", func(t *testing.T) {
		rec := doReq(http.MethodPost, "/usage-service/updates/slot/prepare", "application/json", strings.NewReader(`{"version":"v2.0.0-beta.2","source":"cdn","cdn_prefix":"https://ghfast.top/"}`), true)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if launcher.prepareCalls != 1 {
			t.Fatalf("expected 1 launcher prepare call, got %d", launcher.prepareCalls)
		}
	})

	t.Run("PUT /slot/prepare-upload validates query, content-type, size, and surfaces launcher 422", func(t *testing.T) {
		rec := doReq(http.MethodPut, "/usage-service/updates/slot/prepare-upload", "application/gzip", bytes.NewReader([]byte("x")), true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 without version, got %d", rec.Code)
		}
		rec = doReq(http.MethodPut, "/usage-service/updates/slot/prepare-upload?version=v2.0.0-beta.2", "text/plain", bytes.NewReader([]byte("x")), true)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 on invalid Content-Type, got %d", rec.Code)
		}

		// Oversized Content-Length (>192 MiB) returns 413 without touching launcher.
		req := httptest.NewRequest(http.MethodPut, "/usage-service/updates/slot/prepare-upload?version=v2.0.0-beta.2", bytes.NewReader([]byte("x")))
		req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
		req.Header.Set("Content-Type", "application/gzip")
		req.ContentLength = updatechecksvc.MaxSlotArchiveBytes + 1
		rec = httptest.NewRecorder()
		h.Handle(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge || launcher.uploadCalls != 0 {
			t.Fatalf("expected 413 without launcher upload call, got code=%d calls=%d", rec.Code, launcher.uploadCalls)
		}

		launcher.uploadErrCode = "signature_invalid"
		rec = doReq(http.MethodPut, "/usage-service/updates/slot/prepare-upload?version=v2.0.0-beta.2", "application/gzip", bytes.NewReader([]byte("bad-sig")), true)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 signature_invalid, got %d: %s", rec.Code, rec.Body.String())
		}

		launcher.uploadErrCode = ""
		rec = doReq(http.MethodPut, "/usage-service/updates/slot/prepare-upload?version=v2.0.0-beta.2", "application/octet-stream", bytes.NewReader([]byte("signed-tarball")), true)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 on valid upload, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("POST /slot/activate returns 500 when snapshot fails and 202 on success", func(t *testing.T) {
		snapshotFail = true
		rec := doReq(http.MethodPost, "/usage-service/updates/slot/activate", "application/json", strings.NewReader(`{"version":"v2.0.0-beta.2"}`), true)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 on snapshot failure, got %d: %s", rec.Code, rec.Body.String())
		}
		if launcher.activateCalls != 0 {
			t.Fatal("launcher activate must not run when snapshot failed")
		}

		snapshotFail = false
		rec = doReq(http.MethodPost, "/usage-service/updates/slot/activate", "application/json", strings.NewReader(`{"version":"v2.0.0-beta.2"}`), true)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
		}
		var accepted updatechecksvc.SlotActionAccepted
		if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
			t.Fatal(err)
		}
		if !accepted.Accepted || accepted.TargetVersion != "v2.0.0-beta.2" || accepted.TrialTimeoutSeconds != 60 || accepted.AttemptID != "att-handler-01" {
			t.Fatalf("unexpected activate body: %+v", accepted)
		}
	})

	t.Run("POST /slot/rollback returns 202 and maps 409 schema_rollback_forbidden", func(t *testing.T) {
		rec := doReq(http.MethodPost, "/usage-service/updates/slot/rollback", "application/json", strings.NewReader(`{"expected_active_slot":"v2.0.0-beta.2"}`), true)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
		}

		launcher.rollbackErr = "schema_rollback_forbidden"
		rec = doReq(http.MethodPost, "/usage-service/updates/slot/rollback", "application/json", strings.NewReader(`{"expected_active_slot":"v2.0.0-beta.2"}`), true)
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
		}
		var errBody map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
			t.Fatal(err)
		}
		if errBody["code"] != "schema_rollback_forbidden" {
			t.Fatalf("expected schema_rollback_forbidden, got %v", errBody)
		}
	})

	t.Run("rejects wrong HTTP methods with 405", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/usage-service/updates/slot/prepare"},
			{http.MethodPost, "/usage-service/updates/slot/prepare-upload?version=v2.0.0-beta.2"},
			{http.MethodGet, "/usage-service/updates/slot/activate"},
			{http.MethodGet, "/usage-service/updates/slot/rollback"},
		} {
			rec := doReq(tc.method, tc.path, "", nil, true)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s returned %d, want 405", tc.method, tc.path, rec.Code)
			}
		}
	})
}

func TestUpdateCheckHandlerPreSwitchFenceWithAppContextAndTrialHoldGate(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	dataKeyPath := filepath.Join(cfg.DataDir, "custom.key")
	if err := os.WriteFile(dataKeyPath, []byte("pre-switch-key-material-01234567"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.DataKeyPath = dataKeyPath

	prevVersion := buildinfo.Version
	buildinfo.Version = "v2.0.0-beta.1"
	t.Cleanup(func() { buildinfo.Version = prevVersion })

	tokenFile := filepath.Join(t.TempDir(), "launcher-token")
	if err := os.WriteFile(tokenFile, []byte("launcher-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	var capturedActivate updatechecksvc.LauncherActivateRequest
	launcherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/launcher/slot/status":
			_ = json.NewEncoder(w).Encode(updatechecksvc.LauncherSlotStatus{
				Supported:      true,
				Mode:           "docker",
				BaseVersion:    "v2.0.0-beta.1",
				BaseEpoch:      1,
				RunningVersion: "v2.0.0-beta.1",
				State:          "none",
				StagedSlots:    []string{"v2.0.0-beta.2"},
			})
		case "/v1/launcher/slot/activate":
			_ = json.NewDecoder(r.Body).Decode(&capturedActivate)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(updatechecksvc.SlotActionAccepted{
				Accepted:            true,
				TargetVersion:       capturedActivate.TargetVersion,
				TrialTimeoutSeconds: 60,
				AttemptID:           "att-e2e-01",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(launcherSrv.Close)

	client, err := updatechecksvc.NewLauncherClient(launcherSrv.URL, tokenFile)
	if err != nil {
		t.Fatal(err)
	}

	appCtx := app.FromExisting(cfg, st, nil, time.Now().UnixMilli(), nil, nil, nil, "cpa-manager-plus")
	appCtx.UpdateCheckService.WithLauncherClient(client)
	h := &Handler{App: appCtx}

	gate := middleware.NewTrialHoldGate(false, tokenFile)
	middleware.SetProcessTrialHoldGate(gate)
	t.Cleanup(func() { middleware.SetProcessTrialHoldGate(nil) })

	writeEntered := make(chan struct{})
	allowWriteCommit := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/concurrent-write", func(w http.ResponseWriter, _ *http.Request) {
		close(writeEntered)
		<-allowWriteCommit
		if _, execErr := st.SQLDB().Exec(`INSERT INTO settings(key, value, updated_at_ms) VALUES ('fenced_write', 'included-in-snapshot', 1)`); execErr != nil {
			http.Error(w, execErr.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/usage-service/updates/slot/activate", h.Handle)
	wrapped := gate.Wrap(mux)

	writeDone := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v2/concurrent-write", nil)
		wrapped.ServeHTTP(rec, req)
		writeDone <- rec.Code
	}()
	<-writeEntered

	var activateStarted atomic.Bool
	activateDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		activateStarted.Store(true)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/usage-service/updates/slot/activate", strings.NewReader(`{"version":"v2.0.0-beta.2"}`))
		req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
		req.Header.Set("Content-Type", "application/json")
		wrapped.ServeHTTP(rec, req)
		activateDone <- rec
	}()

	deadline := time.Now().Add(2 * time.Second)
	for (!activateStarted.Load() || !gate.Active()) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !gate.Active() {
		t.Fatal("expected preActivateFence to engage TrialHoldGate while concurrent write is in flight")
	}
	select {
	case rec := <-activateDone:
		t.Fatalf("activate finished before concurrent write committed: status=%d body=%s", rec.Code, rec.Body.String())
	case <-time.After(30 * time.Millisecond):
	}

	// Let the concurrent write commit to SQLite; ActivateSlot then snapshots and calls launcher.Activate.
	close(allowWriteCommit)
	if code := <-writeDone; code != http.StatusCreated {
		t.Fatalf("concurrent write status = %d, want 201", code)
	}
	actRec := <-activateDone
	if actRec.Code != http.StatusAccepted {
		t.Fatalf("activate status = %d, want 202: %s", actRec.Code, actRec.Body.String())
	}
	if !gate.Active() {
		t.Fatal("expected TrialHoldGate to remain active after 202 Accepted")
	}

	// Post-activate writes on the outgoing Manager are blocked with 503.
	postRec := httptest.NewRecorder()
	wrapped.ServeHTTP(postRec, httptest.NewRequest(http.MethodPost, "/api/v2/concurrent-write", nil))
	if postRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("post-activate write status = %d, want 503", postRec.Code)
	}

	// Verify the snapshot includes the concurrent write and canonical target paths.
	if capturedActivate.SnapshotDir == "" {
		t.Fatal("expected non-empty SnapshotDir sent to launcher")
	}
	snapManifestRaw, err := os.ReadFile(filepath.Join(capturedActivate.SnapshotDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read snapshot manifest: %v", err)
	}
	var snapManifest struct {
		TargetDBPath      string `json:"target_db_path"`
		TargetDataKeyPath string `json:"target_data_key_path"`
	}
	if err := json.Unmarshal(snapManifestRaw, &snapManifest); err != nil {
		t.Fatal(err)
	}
	absDB, _ := filepath.Abs(cfg.DBPath)
	absKey, _ := filepath.Abs(dataKeyPath)
	if snapManifest.TargetDBPath != filepath.Clean(absDB) || snapManifest.TargetDataKeyPath != filepath.Clean(absKey) {
		t.Fatalf("snapshot manifest targets = (%q, %q), want (%q, %q)", snapManifest.TargetDBPath, snapManifest.TargetDataKeyPath, absDB, absKey)
	}
}
