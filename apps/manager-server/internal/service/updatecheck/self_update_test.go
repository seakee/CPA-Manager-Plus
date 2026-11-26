package updatecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
)

func fixtureTime() time.Time {
	return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
}

func slotFixture(tag string) ReleaseInfo {
	info := fixture(tag)
	info.Distribution.Slot = &SlotDistribution{
		Assets: []SlotAsset{
			{
				Name:             "cpamp-slot_" + tag + "_linux_amd64.tar.gz",
				Platform:         "linux/amd64",
				SHA256:           "sha256:" + strings.Repeat("a", 64),
				Size:             4096,
				MinBaseEpoch:     1,
				SchemaGeneration: 1,
			},
			{
				Name:             "cpamp-slot_" + tag + "_linux_arm64.tar.gz",
				Platform:         "linux/arm64",
				SHA256:           "sha256:" + strings.Repeat("b", 64),
				Size:             4192,
				MinBaseEpoch:     1,
				SchemaGeneration: 1,
			},
		},
	}
	return info
}

func seedUpdateStateWithRelease(t *testing.T, st *memoryStore, currentTag, targetTag string, info ReleaseInfo) *Service {
	t.Helper()
	_, digest := releaseSHA256(t, info)
	persisted := State{
		SchemaVersion: 1,
		Preference:    "beta",
		LastSuccess:   fixtureTime(),
		Index: &Index{
			SchemaVersion: 2,
			Revision:      10,
			GeneratedAt:   fixtureTime(),
			Channels: map[string]*Target{
				"stable": nil,
				"rc":     nil,
				"beta":   targetFor(targetTag, digest),
			},
		},
		Releases: map[string]ReleaseInfo{
			targetTag: info,
		},
		ReleaseDigests: map[string]string{
			targetTag: digest,
		},
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	st.data = data
	svc := New(st, currentTag, strings.Repeat("1", 40), false)
	svc.slotArch = "amd64"
	svc.now = fixtureTime
	return svc
}

func TestReleaseInfoSlotValidation(t *testing.T) {
	t.Run("accepts release without slot field for beta.1 compatibility", func(t *testing.T) {
		info := fixture("v2.0.0-beta.1")
		if err := info.Validate("v2.0.0-beta.1"); err != nil {
			t.Fatalf("unexpected validation error: %v", err)
		}
	})

	t.Run("accepts valid slot distribution", func(t *testing.T) {
		info := slotFixture("v2.0.0-beta.2")
		if err := info.Validate("v2.0.0-beta.2"); err != nil {
			t.Fatalf("unexpected validation error: %v", err)
		}
	})

	cases := []struct {
		name   string
		mutate func(*ReleaseInfo)
	}{
		{
			name: "missing slot distribution for beta.2",
			mutate: func(info *ReleaseInfo) {
				info.Distribution.Slot = nil
			},
		},
		{
			name: "empty slot assets",
			mutate: func(info *ReleaseInfo) {
				info.Distribution.Slot.Assets = nil
			},
		},
		{
			name: "path traversal in slot name",
			mutate: func(info *ReleaseInfo) {
				info.Distribution.Slot.Assets[0].Name = "../cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz"
			},
		},
		{
			name: "tag mismatch in slot name",
			mutate: func(info *ReleaseInfo) {
				info.Distribution.Slot.Assets[0].Name = "cpamp-slot_v2.0.0-beta.9_linux_amd64.tar.gz"
			},
		},
		{
			name: "platform mismatch with filename arch",
			mutate: func(info *ReleaseInfo) {
				info.Distribution.Slot.Assets[0].Platform = "linux/arm64"
			},
		},
		{
			name: "invalid sha256",
			mutate: func(info *ReleaseInfo) {
				info.Distribution.Slot.Assets[0].SHA256 = "sha256:bad"
			},
		},
		{
			name: "non-positive size",
			mutate: func(info *ReleaseInfo) {
				info.Distribution.Slot.Assets[0].Size = 0
			},
		},
		{
			name: "min_base_epoch below 1",
			mutate: func(info *ReleaseInfo) {
				info.Distribution.Slot.Assets[0].MinBaseEpoch = 0
			},
		},
		{
			name: "schema_generation below 1",
			mutate: func(info *ReleaseInfo) {
				info.Distribution.Slot.Assets[0].SchemaGeneration = 0
			},
		},
		{
			name: "duplicate platform",
			mutate: func(info *ReleaseInfo) {
				info.Distribution.Slot.Assets[1] = info.Distribution.Slot.Assets[0]
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := slotFixture("v2.0.0-beta.2")
			tc.mutate(&info)
			if err := info.Validate("v2.0.0-beta.2"); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestLauncherClientSecurityAndTokenReload(t *testing.T) {
	t.Run("disabled cleanly when env unset", func(t *testing.T) {
		t.Setenv(LauncherControlURLEnv, "")
		t.Setenv(LauncherTokenFileEnv, "")
		client, err := NewLauncherClientFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client != nil {
			t.Fatal("expected nil client when launcher env is unset")
		}
	})

	t.Run("rejects non-loopback or unsafe control URLs", func(t *testing.T) {
		tokenFile := filepath.Join(t.TempDir(), "launcher-token")
		if err := os.WriteFile(tokenFile, []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, badURL := range []string{
			"",
			"https://127.0.0.1:9083",
			"http://192.168.1.10:9083",
			"http://example.com:9083",
			"http://user:pass@127.0.0.1:9083",
			"http://127.0.0.1:9083/v1",
			"http://127.0.0.1:9083?foo=bar",
			"http://[::1%eth0]:9083",
		} {
			if _, err := NewLauncherClient(badURL, tokenFile); err == nil {
				t.Fatalf("expected error for control URL %q", badURL)
			}
		}
	})

	t.Run("re-reads token file on each call and bypasses env proxy", func(t *testing.T) {
		tokenFile := filepath.Join(t.TempDir(), "launcher-token")
		if err := os.WriteFile(tokenFile, []byte("token-v1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HTTP_PROXY", "http://192.0.2.1:8888")
		t.Setenv("ALL_PROXY", "http://192.0.2.1:8888")

		var callCount atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := callCount.Add(1)
			wantAuth := "Bearer token-v1"
			if n > 1 {
				wantAuth = "Bearer token-v2"
			}
			if got := r.Header.Get("Authorization"); got != wantAuth {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "unauthorized", "message": "mismatch"})
				return
			}
			if n == 2 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "signature_invalid", "message": "bad ed25519 signature"})
				return
			}
			_ = json.NewEncoder(w).Encode(LauncherSlotStatus{
				Supported:   true,
				Mode:        "docker",
				BaseVersion: "v2.0.0-beta.1",
				State:       "none",
			})
		}))
		defer srv.Close()

		client, err := NewLauncherClient(srv.URL, tokenFile)
		if err != nil {
			t.Fatalf("new launcher client: %v", err)
		}
		if _, err := client.Status(context.Background()); err != nil {
			t.Fatalf("first status failed: %v", err)
		}

		if err := os.WriteFile(tokenFile, []byte("token-v2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = client.Status(context.Background())
		var suErr *SelfUpdateError
		if !errors.As(err, &suErr) || suErr.Code != "signature_invalid" || suErr.HTTPStatus != http.StatusUnprocessableEntity {
			t.Fatalf("expected typed 422 signature_invalid error, got %v", err)
		}
	})
}

func TestServiceStatusWithLauncherAndSchemaGuard(t *testing.T) {
	t.Setenv(BaseVersionEnv, "v2.0.0-beta.1")
	t.Setenv(RunningSlotEnv, "v2.0.0-beta.2")

	tokenFile := filepath.Join(t.TempDir(), "launcher-token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(LauncherSlotStatus{
			Supported:                true,
			Mode:                     "docker",
			BaseVersion:              "v2.0.0-beta.1",
			BaseEpoch:                1,
			RunningVersion:           "v2.0.0-beta.2",
			ActiveSlot:               "v2.0.0-beta.2",
			RollbackTargetVersion:    "v2.0.0-beta.1",
			RollbackAvailable:        true,
			RollbackSameSchema:       true,
			PreviousSchemaGeneration: 1,
			SchemaGeneration:         2,
			State:                    "committed",
			StagedSlots:              []string{"v2.0.0-beta.2"},
			LastRolledBackSlot:       "v2.0.0-beta.3",
			LastFailureCode:          "trial_health_timeout",
		})
	}))
	defer srv.Close()

	client, err := NewLauncherClient(srv.URL, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	st := &memoryStore{}
	svc := seedUpdateStateWithRelease(t, st, "v2.0.0-beta.2", "v2.0.0-beta.3", slotFixture("v2.0.0-beta.3"))
	svc.WithLauncherClient(client).WithCurrentSchemaGeneration(2)

	status, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.BaseImageVersion != "v2.0.0-beta.1" || status.ActiveSlot != "v2.0.0-beta.2" {
		t.Fatalf("unexpected top-level dual version fields: %+v", status)
	}
	if status.SelfUpdate == nil || !status.SelfUpdate.Supported {
		t.Fatalf("expected supported self_update status, got %+v", status.SelfUpdate)
	}
	if status.SelfUpdate.RollbackSameSchema || status.SelfUpdate.RollbackBlockedReason != "schema_rollback_forbidden" {
		t.Fatalf("expected schema generation mismatch to disable rollback cleanly, got %+v", status.SelfUpdate)
	}
	if status.SelfUpdate.LastRolledBackSlot != "v2.0.0-beta.3" || status.SelfUpdate.LastFailureCode != "trial_health_timeout" {
		t.Fatalf("unexpected rollback diagnostics: %+v", status.SelfUpdate)
	}
	if !slices.Equal(status.SelfUpdate.CDNPresets, DefaultSlotCDNPresets) {
		t.Fatalf("unexpected cdn_presets: %v", status.SelfUpdate.CDNPresets)
	}
}

func TestPrepareSlotValidationAndSources(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "launcher-token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}

	var captured LauncherPrepareRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case launcherPreparePath:
			if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
				t.Fatalf("decode launcher prepare: %v", err)
			}
			_ = json.NewEncoder(w).Encode(SlotPrepareOperation{
				ID:            "slot-prep-1",
				TargetVersion: captured.TargetVersion,
				Phase:         "succeeded",
				ReceivedBytes: 4096,
				TotalBytes:    4096,
			})
		case launcherStatusPath:
			_ = json.NewEncoder(w).Encode(LauncherSlotStatus{
				Supported:   true,
				Mode:        "docker",
				BaseVersion: "v2.0.0-beta.1",
				State:       "none",
				StagedSlots: []string{captured.TargetVersion},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := NewLauncherClient(srv.URL, tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	st := &memoryStore{}
	svc := seedUpdateStateWithRelease(t, st, "v2.0.0-beta.1", "v2.0.0-beta.2", slotFixture("v2.0.0-beta.2"))
	svc.WithLauncherClient(client)

	t.Run("rejects github with cdn_prefix", func(t *testing.T) {
		_, err := svc.PrepareSlot(context.Background(), PrepareSlotInput{
			Version:   "v2.0.0-beta.2",
			Source:    "github",
			CDNPrefix: "https://ghfast.top/",
		})
		var suErr *SelfUpdateError
		if !errors.As(err, &suErr) || suErr.HTTPStatus != http.StatusBadRequest {
			t.Fatalf("expected 400 error, got %v", err)
		}
	})

	t.Run("rejects unsafe cdn prefixes", func(t *testing.T) {
		for _, bad := range []string{
			"",
			"http://ghfast.top/",
			"https://localhost/",
			"https://127.0.0.1/",
			"https://192.168.1.10/",
			"https://user@ghfast.top/",
			"https://ghfast.top/../evil",
			"https://ghfast.top/?q=1",
		} {
			_, err := svc.PrepareSlot(context.Background(), PrepareSlotInput{
				Version:   "v2.0.0-beta.2",
				Source:    "cdn",
				CDNPrefix: bad,
			})
			var suErr *SelfUpdateError
			if !errors.As(err, &suErr) || suErr.HTTPStatus != http.StatusBadRequest {
				t.Fatalf("expected 400 for bad cdn_prefix %q, got %v", bad, err)
			}
		}
	})

	t.Run("prepares via official github url", func(t *testing.T) {
		res, err := svc.PrepareSlot(context.Background(), PrepareSlotInput{
			Version: "v2.0.0-beta.2",
		})
		if err != nil {
			t.Fatalf("PrepareSlot github failed: %v", err)
		}
		wantURL := Repository + "/releases/download/v2.0.0-beta.2/cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz"
		if captured.ArchiveURL != wantURL || captured.ExpectedSHA256 != "sha256:"+strings.Repeat("a", 64) || captured.ExpectedSize != 4096 {
			t.Fatalf("unexpected launcher prepare request: %+v", captured)
		}
		if !slices.Contains(res.StagedSlots, "v2.0.0-beta.2") {
			t.Fatalf("expected staged slot in status: %+v", res)
		}
	})

	t.Run("prepares via explicit cdn prefix", func(t *testing.T) {
		_, err := svc.PrepareSlot(context.Background(), PrepareSlotInput{
			Version:   "v2.0.0-beta.2",
			Source:    "cdn",
			CDNPrefix: "https://ghfast.top/",
		})
		if err != nil {
			t.Fatalf("PrepareSlot cdn failed: %v", err)
		}
		wantURL := "https://ghfast.top/" + Repository + "/releases/download/v2.0.0-beta.2/cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz"
		if captured.ArchiveURL != wantURL {
			t.Fatalf("got archive URL %q, want %q", captured.ArchiveURL, wantURL)
		}
	})

	t.Run("blocks migration_required target", func(t *testing.T) {
		migInfo := slotFixture("v2.0.0-beta.2")
		migInfo.Update.MigrationRequired = true
		migSvc := seedUpdateStateWithRelease(t, &memoryStore{}, "v2.0.0-beta.1", "v2.0.0-beta.2", migInfo)
		migSvc.WithLauncherClient(client)
		_, err := migSvc.PrepareSlot(context.Background(), PrepareSlotInput{Version: "v2.0.0-beta.2"})
		var suErr *SelfUpdateError
		if !errors.As(err, &suErr) || suErr.Code != "migration_required" || suErr.HTTPStatus != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 migration_required, got %v", err)
		}
	})
}

func TestUploadSlotOnlineAndOffline(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "launcher-token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}

	var lastVersion, lastSHA string
	var lastPayload []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case launcherPrepareUploadPath:
			lastVersion = r.URL.Query().Get("target_version")
			lastSHA = r.URL.Query().Get("expected_sha256")
			data, _ := io.ReadAll(r.Body)
			lastPayload = data
			_ = json.NewEncoder(w).Encode(SlotPrepareOperation{
				ID:            "slot-upload-1",
				TargetVersion: lastVersion,
				Phase:         "succeeded",
				ReceivedBytes: int64(len(data)),
				TotalBytes:    int64(len(data)),
			})
		case launcherStatusPath:
			_ = json.NewEncoder(w).Encode(LauncherSlotStatus{
				Supported:   true,
				Mode:        "docker",
				BaseVersion: "v2.0.0-beta.1",
				State:       "none",
				StagedSlots: []string{lastVersion},
			})
		}
	}))
	defer srv.Close()

	client, err := NewLauncherClient(srv.URL, tokenFile)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("passes cached expected_sha256 when release is known", func(t *testing.T) {
		svc := seedUpdateStateWithRelease(t, &memoryStore{}, "v2.0.0-beta.1", "v2.0.0-beta.2", slotFixture("v2.0.0-beta.2"))
		svc.WithLauncherClient(client)
		res, err := svc.UploadSlot(context.Background(), "v2.0.0-beta.2", bytes.NewReader([]byte("tar-gz-bytes")), 12)
		if err != nil {
			t.Fatalf("UploadSlot failed: %v", err)
		}
		if lastVersion != "v2.0.0-beta.2" || lastSHA != "sha256:"+strings.Repeat("a", 64) || string(lastPayload) != "tar-gz-bytes" {
			t.Fatalf("unexpected upload forward: version=%q sha=%q payload=%q", lastVersion, lastSHA, string(lastPayload))
		}
		if !slices.Contains(res.StagedSlots, "v2.0.0-beta.2") {
			t.Fatalf("expected staged slot, got %+v", res)
		}
	})

	t.Run("allows pure offline upload with empty expected_sha256", func(t *testing.T) {
		svc := New(&memoryStore{}, "v2.0.0-beta.1", strings.Repeat("1", 40), false)
		svc.slotArch = "amd64"
		svc.WithLauncherClient(client)
		res, err := svc.UploadSlot(context.Background(), "v2.0.0-beta.9", bytes.NewReader([]byte("offline-signed")), 14)
		if err != nil {
			t.Fatalf("offline UploadSlot failed: %v", err)
		}
		if lastVersion != "v2.0.0-beta.9" || lastSHA != "" || string(lastPayload) != "offline-signed" {
			t.Fatalf("unexpected offline upload: version=%q sha=%q payload=%q", lastVersion, lastSHA, string(lastPayload))
		}
		if !slices.Contains(res.StagedSlots, "v2.0.0-beta.9") {
			t.Fatalf("expected staged slot, got %+v", res)
		}
	})

	t.Run("rejects archive larger than 192 MiB", func(t *testing.T) {
		svc := New(&memoryStore{}, "v2.0.0-beta.1", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client)
		_, err := svc.UploadSlot(context.Background(), "v2.0.0-beta.2", bytes.NewReader([]byte("x")), MaxSlotArchiveBytes+1)
		var suErr *SelfUpdateError
		if !errors.As(err, &suErr) || suErr.Code != "archive_too_large" || suErr.HTTPStatus != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413 archive_too_large, got %v", err)
		}
	})
}

func TestActivateSlotSnapshotFailClosedAndRollback(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "launcher-token")
	if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}

	var staged []string
	var activateCalled atomic.Bool
	var capturedActivate LauncherActivateRequest
	var capturedRollback LauncherRollbackRequest
	var rollbackErrCode string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case launcherStatusPath:
			_ = json.NewEncoder(w).Encode(LauncherSlotStatus{
				Supported:   true,
				Mode:        "docker",
				BaseVersion: "v2.0.0-beta.1",
				StagedSlots: staged,
			})
		case launcherActivatePath:
			activateCalled.Store(true)
			_ = json.NewDecoder(r.Body).Decode(&capturedActivate)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(SlotActionAccepted{
				Accepted:            true,
				TargetVersion:       capturedActivate.TargetVersion,
				TrialTimeoutSeconds: 60,
			})
		case launcherRollbackPath:
			_ = json.NewDecoder(r.Body).Decode(&capturedRollback)
			if rollbackErrCode != "" {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": rollbackErrCode, "message": "schema changed"})
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(SlotActionAccepted{
				Accepted:      true,
				TargetVersion: "v2.0.0-beta.1",
			})
		}
	}))
	defer srv.Close()

	client, err := NewLauncherClient(srv.URL, tokenFile)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("rejects unstaged slot before running snapshot", func(t *testing.T) {
		staged = nil
		snapshotRan := false
		svc := New(&memoryStore{}, "v2.0.0-beta.1", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client).WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
			snapshotRan = true
			return "/data/backups/pre-slot", 1, nil
		})
		_, err := svc.ActivateSlot(context.Background(), "v2.0.0-beta.2")
		var suErr *SelfUpdateError
		if !errors.As(err, &suErr) || suErr.Code != "slot_not_staged" || suErr.HTTPStatus != http.StatusConflict {
			t.Fatalf("expected 409 slot_not_staged, got %v", err)
		}
		if snapshotRan || activateCalled.Load() {
			t.Fatal("snapshot and activate must not run when slot is unstaged")
		}
	})

	t.Run("fails closed when preSlotSnapshot errors", func(t *testing.T) {
		staged = []string{"v2.0.0-beta.2"}
		activateCalled.Store(false)
		svc := New(&memoryStore{}, "v2.0.0-beta.1", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client).WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
			return "", 0, errors.New("vacuum into failed")
		})
		_, err := svc.ActivateSlot(context.Background(), "v2.0.0-beta.2")
		var suErr *SelfUpdateError
		if !errors.As(err, &suErr) || suErr.Code != "snapshot_failed" || suErr.HTTPStatus != http.StatusInternalServerError {
			t.Fatalf("expected 500 snapshot_failed, got %v", err)
		}
		if activateCalled.Load() {
			t.Fatal("launcher activate must not be called when snapshot fails")
		}
	})

	t.Run("forwards snapshot_dir and previous_schema_generation on activate", func(t *testing.T) {
		staged = []string{"v2.0.0-beta.2"}
		activateCalled.Store(false)
		svc := New(&memoryStore{}, "v2.0.0-beta.1", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client).WithCurrentSchemaGeneration(2).WithPreSlotSnapshot(func(_ context.Context, target string) (string, int, error) {
			return "/data/backups/pre-slot-v2.0.0-beta.1-to-" + target, 2, nil
		})
		res, err := svc.ActivateSlot(context.Background(), "v2.0.0-beta.2")
		if err != nil {
			t.Fatalf("ActivateSlot failed: %v", err)
		}
		if !res.Accepted || res.TargetVersion != "v2.0.0-beta.2" || res.TrialTimeoutSeconds != 60 {
			t.Fatalf("unexpected accepted result: %+v", res)
		}
		if capturedActivate.SnapshotDir != "/data/backups/pre-slot-v2.0.0-beta.1-to-v2.0.0-beta.2" || capturedActivate.PreviousSchemaGeneration != 2 {
			t.Fatalf("unexpected launcher activate payload: %+v", capturedActivate)
		}
	})

	t.Run("forwards rollback and maps schema_rollback_forbidden", func(t *testing.T) {
		svc := New(&memoryStore{}, "v2.0.0-beta.2", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client).WithCurrentSchemaGeneration(2)

		rollbackErrCode = ""
		res, err := svc.RollbackSlot(context.Background(), "v2.0.0-beta.2")
		if err != nil {
			t.Fatalf("RollbackSlot failed: %v", err)
		}
		if !res.Accepted || res.TargetVersion != "v2.0.0-beta.1" {
			t.Fatalf("unexpected rollback result: %+v", res)
		}
		if capturedRollback.ExpectedActiveSlot != "v2.0.0-beta.2" || capturedRollback.CurrentSchemaGeneration != 2 {
			t.Fatalf("unexpected rollback payload: %+v", capturedRollback)
		}

		rollbackErrCode = "schema_rollback_forbidden"
		_, err = svc.RollbackSlot(context.Background(), "v2.0.0-beta.2")
		var suErr *SelfUpdateError
		if !errors.As(err, &suErr) || suErr.Code != "schema_rollback_forbidden" || suErr.HTTPStatus != http.StatusConflict {
			t.Fatalf("expected 409 schema_rollback_forbidden, got %v", err)
		}
	})
}

func TestActivateSlotPreSwitchFenceVersionCheckAndAttemptPlumbing(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "launcher-token")
	if err := os.WriteFile(tokenFile, []byte("secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	var (
		runningVersion = "v2.0.0-beta.2"
		state          = "committed"
		staged         = []string{"v2.0.0-beta.1", "v2.0.0-beta.2", "v2.0.0-beta.3"}
		activateErr    string
	)
	launcherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case launcherStatusPath:
			_ = json.NewEncoder(w).Encode(LauncherSlotStatus{
				Supported:         true,
				Mode:              "docker",
				BaseVersion:       "v2.0.0-beta.1",
				BaseEpoch:         1,
				RunningVersion:    runningVersion,
				ActiveSlot:        runningVersion,
				RollbackAvailable: true,
				State:             state,
				ActiveAttemptID:   "att-active-01",
				LastAttemptID:     "att-prev-00",
				StagedSlots:       staged,
			})
		case launcherActivatePath:
			if activateErr != "" {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": activateErr, "message": "launcher rejected activate"})
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(SlotActionAccepted{
				Accepted:            true,
				TargetVersion:       "v2.0.0-beta.3",
				TrialTimeoutSeconds: 60,
				AttemptID:           "att-next-02",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(launcherSrv.Close)

	client, err := NewLauncherClient(launcherSrv.URL, tokenFile)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("rejects equal or older staged version with 409 version_conflict before touching fence or snapshot", func(t *testing.T) {
		var fenceCalls, snapCalls int
		svc := New(&memoryStore{}, "v2.0.0-beta.2", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client).
			WithPreActivateFence(func(context.Context) (func(), error) {
				fenceCalls++
				return func() {}, nil
			}).
			WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
				snapCalls++
				return "/tmp/unused", 1, nil
			})

		for _, target := range []string{"v2.0.0-beta.2", "v2.0.0-beta.1"} {
			_, err := svc.ActivateSlot(context.Background(), target)
			var suErr *SelfUpdateError
			if !errors.As(err, &suErr) || suErr.Code != "version_conflict" || suErr.HTTPStatus != http.StatusConflict {
				t.Fatalf("ActivateSlot(%s) = %v, want 409 version_conflict", target, err)
			}
		}
		if fenceCalls != 0 || snapCalls != 0 {
			t.Fatalf("fenceCalls=%d snapCalls=%d, want both 0 on version_conflict", fenceCalls, snapCalls)
		}
	})

	t.Run("releases fence on snapshot failure, cleans orphan snapshot and releases fence on launcher error, keeps fence held on 202", func(t *testing.T) {
		var fenceActive bool
		fenceHook := func(context.Context) (func(), error) {
			fenceActive = true
			return func() { fenceActive = false }, nil
		}

		// 1. Snapshot failure releases fence.
		svc := New(&memoryStore{}, "v2.0.0-beta.2", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client).WithPreActivateFence(fenceHook).WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
			if !fenceActive {
				t.Fatal("expected preActivateFence to be active before preSlotSnapshot runs")
			}
			return "", 0, errors.New("disk full")
		})
		if _, err := svc.ActivateSlot(context.Background(), "v2.0.0-beta.3"); err == nil || fenceActive {
			t.Fatalf("expected snapshot failure to release fence, err=%v fenceActive=%v", err, fenceActive)
		}

		// 2. Launcher activate failure removes orphan snapshotDir and releases fence.
		orphanSnap := filepath.Join(t.TempDir(), "pre-slot-orphan")
		activateErr = "slot_busy"
		svc.WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
			if mkErr := os.MkdirAll(orphanSnap, 0o700); mkErr != nil {
				return "", 0, mkErr
			}
			if wErr := os.WriteFile(filepath.Join(orphanSnap, "manifest.json"), []byte(`{"version":1}`), 0o600); wErr != nil {
				return "", 0, wErr
			}
			if wErr := os.WriteFile(filepath.Join(orphanSnap, "database"), []byte("db"), 0o600); wErr != nil {
				return "", 0, wErr
			}
			return orphanSnap, 1, nil
		})
		_, err := svc.ActivateSlot(context.Background(), "v2.0.0-beta.3")
		if err == nil || fenceActive {
			t.Fatalf("expected launcher error to release fence, err=%v fenceActive=%v", err, fenceActive)
		}
		if _, statErr := os.Stat(orphanSnap); !os.IsNotExist(statErr) {
			t.Fatalf("expected orphan snapshot directory %s to be removed, statErr=%v", orphanSnap, statErr)
		}

		// 3. Successful 202 Accepted keeps fence active and threads attempt_id.
		activateErr = ""
		validSnap := filepath.Join(t.TempDir(), "pre-slot-kept")
		svc.WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
			if mkErr := os.MkdirAll(validSnap, 0o700); mkErr != nil {
				return "", 0, mkErr
			}
			return validSnap, 1, nil
		})
		accepted, err := svc.ActivateSlot(context.Background(), "v2.0.0-beta.3")
		if err != nil {
			t.Fatalf("ActivateSlot failed: %v", err)
		}
		if !fenceActive {
			t.Fatal("expected TrialHoldGate fence to remain active after 202 Accepted")
		}
		if accepted.AttemptID != "att-next-02" {
			t.Fatalf("accepted.AttemptID = %q, want att-next-02", accepted.AttemptID)
		}
	})

	t.Run("threads active_attempt_id, last_attempt_id, and recovery_required rollback guard", func(t *testing.T) {
		state = "recovery_required"
		svc := New(&memoryStore{}, "v2.0.0-beta.2", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client)
		st, err := svc.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if st.SelfUpdate == nil {
			t.Fatal("expected SelfUpdate status")
		}
		if st.SelfUpdate.State != "recovery_required" || st.SelfUpdate.RollbackAvailable {
			t.Fatalf("recovery_required status = %+v, want RollbackAvailable=false", st.SelfUpdate)
		}
		if st.SelfUpdate.ActiveAttemptID != "att-active-01" || st.SelfUpdate.LastAttemptID != "att-prev-00" {
			t.Fatalf("attempt IDs = (%q, %q), want (att-active-01, att-prev-00)", st.SelfUpdate.ActiveAttemptID, st.SelfUpdate.LastAttemptID)
		}
	})
}

func TestActivateSlotAmbiguousTransportDropKeepsSnapshotAndFence(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "launcher-token")
	if err := os.WriteFile(tokenFile, []byte("secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	attemptIDPattern := regexp.MustCompile(`^attempt-[0-9a-f]{16}$`)
	var (
		dropConnection    atomic.Bool
		switchAccepted    atomic.Bool
		switchCompleted   atomic.Bool
		statusFails       atomic.Bool
		activateCtxSawErr atomic.Bool
		mu                sync.Mutex
		reservedAttemptID string
		lastSeenReq       LauncherActivateRequest
	)
	abortedAttempts := make(map[string]bool)
	launcherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case launcherStatusPath:
			if statusFails.Load() {
				hj, ok := w.(http.Hijacker)
				if ok {
					conn, _, hErr := hj.Hijack()
					if hErr == nil {
						_ = conn.Close()
						return
					}
				}
			}
			mu.Lock()
			reserved := reservedAttemptID
			mu.Unlock()
			activeSlot := "v2.0.0-beta.2"
			state := "committed"
			activeAttempt := ""
			lastAttempt := "attempt-0000000000000001"
			if switchAccepted.Load() {
				// Mirrors handleStatus exposing pendingSwitch.AttemptID immediately
				// even before supervise.go writes slot-state.json!
				state = "trial"
				activeAttempt = reserved
				lastAttempt = reserved
			} else if switchCompleted.Load() {
				state = "rolled_back"
				activeAttempt = ""
				lastAttempt = reserved
			}
			_ = json.NewEncoder(w).Encode(LauncherSlotStatus{
				Supported:       true,
				Mode:            "docker",
				BaseVersion:     "v2.0.0-beta.1",
				RunningVersion:  activeSlot,
				ActiveSlot:      activeSlot,
				State:           state,
				ActiveAttemptID: activeAttempt,
				LastAttemptID:   lastAttempt,
				StagedSlots:     []string{"v2.0.0-beta.2", "v2.0.0-beta.3"},
			})
		case launcherActivatePath:
			if r.Context().Err() != nil {
				activateCtxSawErr.Store(true)
			}
			var req LauncherActivateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode activate request: %v", err)
				return
			}
			if !attemptIDPattern.MatchString(req.AttemptID) {
				t.Errorf("expected ActivateSlot to send ^attempt-[0-9a-f]{16}$, got %q", req.AttemptID)
			}
			mu.Lock()
			lastSeenReq = req
			if switchAccepted.Load() || switchCompleted.Load() {
				reservedAttemptID = req.AttemptID
			}
			mu.Unlock()
			if dropConnection.Load() {
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Error("expected ResponseWriter to implement http.Hijacker")
					return
				}
				conn, _, hErr := hj.Hijack()
				if hErr != nil {
					t.Errorf("hijack failed: %v", hErr)
					return
				}
				_ = conn.Close()
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(SlotActionAccepted{
				Accepted:            true,
				TargetVersion:       "v2.0.0-beta.3",
				TrialTimeoutSeconds: 60,
				AttemptID:           req.AttemptID,
			})
		case launcherAbortAttemptPath:
			if statusFails.Load() {
				hj, ok := w.(http.Hijacker)
				if ok {
					conn, _, hErr := hj.Hijack()
					if hErr == nil {
						_ = conn.Close()
						return
					}
				}
			}
			var req LauncherAbortAttemptRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode abort-attempt request: %v", err)
				return
			}
			mu.Lock()
			outcome := LauncherAbortOutcomeAborted
			if (switchAccepted.Load() || switchCompleted.Load()) && reservedAttemptID == req.AttemptID {
				outcome = LauncherAbortOutcomeAccepted
			} else {
				abortedAttempts[req.AttemptID] = true
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(LauncherAbortAttemptResponse{
				AttemptID: req.AttemptID,
				Outcome:   outcome,
			})
		}
	}))
	t.Cleanup(launcherSrv.Close)

	client, err := NewLauncherClient(launcherSrv.URL, tokenFile)
	if err != nil {
		t.Fatal(err)
	}

	createSnapshotFixture := func(t *testing.T, name string) string {
		t.Helper()
		snapDir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(snapDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(snapDir, "manifest.json"), []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(snapDir, "database"), []byte("db-bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		return snapDir
	}

	t.Run("dropped connection when Status also fails returns 502, preserves snapshotDir, and keeps TrialHoldGate engaged", func(t *testing.T) {
		dropConnection.Store(true)
		switchAccepted.Store(false)
		switchCompleted.Store(false)
		statusFails.Store(false)

		var fenceEngaged bool
		snapDir := createSnapshotFixture(t, "pre-slot-accepted-drop")
		svc := New(&memoryStore{}, "v2.0.0-beta.2", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client).
			WithPreActivateFence(func(context.Context) (func(), error) {
				fenceEngaged = true
				return func() { fenceEngaged = false }, nil
			}).
			WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
				switchAccepted.Store(true)
				statusFails.Store(true)
				return snapDir, 1, nil
			})
		_, actErr := svc.ActivateSlot(context.Background(), "v2.0.0-beta.3")
		var suErr *SelfUpdateError
		if !errors.As(actErr, &suErr) || suErr.DefiniteRejection || suErr.HTTPStatus != http.StatusBadGateway {
			t.Fatalf("expected 502 non-DefiniteRejection SelfUpdateError when Status fails, got %v (suErr=%+v)", actErr, suErr)
		}
		if !fenceEngaged {
			t.Fatal("expected TrialHoldGate fence to remain engaged when activate outcome is ambiguous")
		}
		if _, statErr := os.Stat(filepath.Join(snapDir, "database")); statErr != nil {
			t.Fatalf("expected snapshotDir to be preserved on ambiguous activate error, statErr=%v", statErr)
		}
	})

	t.Run("dropped 202 response while attemptID is reserved in pendingSwitch returns Accepted, keeps fence engaged, and preserves snapshotDir", func(t *testing.T) {
		dropConnection.Store(true)
		switchAccepted.Store(false)
		switchCompleted.Store(false)
		statusFails.Store(false)

		var fenceEngaged bool
		snapDir := createSnapshotFixture(t, "pre-slot-status-pending-switch")
		svc := New(&memoryStore{}, "v2.0.0-beta.2", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client).
			WithPreActivateFence(func(context.Context) (func(), error) {
				fenceEngaged = true
				return func() { fenceEngaged = false }, nil
			}).
			WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
				switchAccepted.Store(true)
				return snapDir, 1, nil
			})

		accepted, actErr := svc.ActivateSlot(context.Background(), "v2.0.0-beta.3")
		if actErr != nil {
			t.Fatalf("expected ActivateSlot to recover Accepted when Status confirms reserved attemptID, got %v", actErr)
		}
		mu.Lock()
		sentAttemptID := lastSeenReq.AttemptID
		mu.Unlock()
		if !accepted.Accepted || accepted.AttemptID != sentAttemptID || accepted.TargetVersion != "v2.0.0-beta.3" || accepted.TrialTimeoutSeconds != 60 {
			t.Fatalf("unexpected recovered SlotActionAccepted: %+v (want AttemptID=%q)", accepted, sentAttemptID)
		}
		if !fenceEngaged {
			t.Fatal("expected TrialHoldGate fence to remain engaged when Status confirms reserved attemptID")
		}
		if _, statErr := os.Stat(filepath.Join(snapDir, "database")); statErr != nil {
			t.Fatalf("expected snapshotDir preserved, statErr=%v", statErr)
		}
	})

	t.Run("dropped 202 response when Status exposes matching LastAttemptID also keeps fence engaged and preserves snapshotDir", func(t *testing.T) {
		dropConnection.Store(true)
		switchAccepted.Store(false)
		switchCompleted.Store(false)
		statusFails.Store(false)

		var fenceEngaged bool
		snapDir := createSnapshotFixture(t, "pre-slot-status-last-attempt")
		svc := New(&memoryStore{}, "v2.0.0-beta.2", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client).
			WithPreActivateFence(func(context.Context) (func(), error) {
				fenceEngaged = true
				return func() { fenceEngaged = false }, nil
			}).
			WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
				switchCompleted.Store(true)
				return snapDir, 1, nil
			})

		accepted, actErr := svc.ActivateSlot(context.Background(), "v2.0.0-beta.3")
		if actErr != nil || !accepted.Accepted {
			t.Fatalf("expected Accepted when LastAttemptID matches sent attemptID, accepted=%+v err=%v", accepted, actErr)
		}
		if !fenceEngaged {
			t.Fatal("expected fence to remain engaged when LastAttemptID matches")
		}
		if _, statErr := os.Stat(filepath.Join(snapDir, "database")); statErr != nil {
			t.Fatalf("expected snapshotDir preserved, statErr=%v", statErr)
		}
	})

	t.Run("dropped connection when Status proves attemptID was not accepted preserves snapshotDir and releases fence", func(t *testing.T) {
		dropConnection.Store(true)
		switchAccepted.Store(false)
		switchCompleted.Store(false)
		statusFails.Store(false)
		mu.Lock()
		reservedAttemptID = "attempt-deadbeefdeadbeef"
		mu.Unlock()

		var fenceEngaged bool
		snapDir := createSnapshotFixture(t, "pre-slot-status-idle")
		svc := New(&memoryStore{}, "v2.0.0-beta.2", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client).
			WithPreActivateFence(func(context.Context) (func(), error) {
				fenceEngaged = true
				return func() { fenceEngaged = false }, nil
			}).
			WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
				return snapDir, 1, nil
			})

		_, actErr := svc.ActivateSlot(context.Background(), "v2.0.0-beta.3")
		if actErr == nil {
			t.Fatal("expected transport error from dropped connection when attemptID was never accepted")
		}
		if fenceEngaged {
			t.Fatal("expected fence to be released when Status proves attemptID was not accepted")
		}
		if _, statErr := os.Stat(filepath.Join(snapDir, "database")); statErr != nil {
			t.Fatalf("expected snapshotDir never to be deleted on non-DefiniteRejection error, statErr=%v", statErr)
		}
	})
	t.Run("caller context cancellation during preSlotSnapshot return does not cancel loopback Activate", func(t *testing.T) {
		dropConnection.Store(false)
		switchAccepted.Store(false)
		statusFails.Store(false)
		activateCtxSawErr.Store(false)

		reqCtx, cancelReq := context.WithCancel(context.Background())
		defer cancelReq()
		snapDir := createSnapshotFixture(t, "pre-slot-detached-ctx")
		svc := New(&memoryStore{}, "v2.0.0-beta.2", strings.Repeat("1", 40), false)
		svc.WithLauncherClient(client).
			WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
				cancelReq()
				return snapDir, 1, nil
			})

		accepted, actErr := svc.ActivateSlot(reqCtx, "v2.0.0-beta.3")
		if actErr != nil || !accepted.Accepted || activateCtxSawErr.Load() {
			t.Fatalf("expected detached Activate to succeed despite caller disconnect, accepted=%+v err=%v sawErr=%v", accepted, actErr, activateCtxSawErr.Load())
		}
	})
}

type blockingSaveUpdateStore struct {
	memoryStore
	enteredSave chan struct{}
	allowSave   chan struct{}
	saveCalls   atomic.Int64
	enterOnce   sync.Once
}

func (b *blockingSaveUpdateStore) SaveUpdateCheck(ctx context.Context, data []byte) error {
	b.saveCalls.Add(1)
	if b.enteredSave != nil {
		b.enterOnce.Do(func() { close(b.enteredSave) })
	}
	if b.allowSave != nil {
		<-b.allowSave
	}
	return b.memoryStore.SaveUpdateCheck(ctx, data)
}

func TestUpdateCheckSaveFencedUnderTrialHold(t *testing.T) {
	t.Cleanup(func() {
		middleware.SetProcessTrialHoldGate(nil)
	})
	gate := middleware.NewTrialHoldGate(false, "")
	middleware.SetProcessTrialHoldGate(gate)

	st := &blockingSaveUpdateStore{
		enteredSave: make(chan struct{}),
		allowSave:   make(chan struct{}),
	}
	svc := New(st, "v2.0.0-beta.1", strings.Repeat("1", 40), false)

	saveDone := make(chan error, 1)
	go func() {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		saveDone <- svc.saveBackgroundCache(context.Background())
	}()
	<-st.enteredSave

	middleware.EngageProcessTrialHold()
	drainDone := make(chan error, 1)
	go func() {
		drainDone <- middleware.WaitOtherInflightMutations(context.Background())
	}()

	select {
	case err := <-drainDone:
		t.Fatalf("WaitOtherInflightMutations returned while updatecheck.save was mid-write: %v", err)
	case <-time.After(40 * time.Millisecond):
	}

	close(st.allowSave)
	if err := <-saveDone; err != nil {
		t.Fatalf("in-flight save failed: %v", err)
	}
	if err := <-drainDone; err != nil {
		t.Fatalf("WaitOtherInflightMutations failed: %v", err)
	}

	beforeCalls := st.saveCalls.Load()
	svc.mu.Lock()
	err := svc.saveBackgroundCache(context.Background())
	persistedFailed := svc.persistenceFailed
	svc.mu.Unlock()
	if err != nil || persistedFailed {
		t.Fatalf("expected save under TrialHold to skip cleanly without persistenceFailed, err=%v persistenceFailed=%v", err, persistedFailed)
	}
	if st.saveCalls.Load() != beforeCalls {
		t.Fatalf("expected zero SaveUpdateCheck calls while TrialHold is active, got %d -> %d", beforeCalls, st.saveCalls.Load())
	}
}

func TestActivateSlotSlowSnapshotValidationTimeoutAbortsAttemptAndReleasesFence(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "launcher-token")
	if err := os.WriteFile(tokenFile, []byte("secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	var (
		mu                        sync.Mutex
		pendingSwitchAttempt      string
		abortedAttempts           = make(map[string]struct{})
		beforeReserveEntered      = make(chan struct{})
		allowBeforeReserveProceed = make(chan struct{})
		activateHandlerDone       = make(chan int, 1)
		abortCalls                atomic.Int32
	)
	beforeActivateReserveHook := func() {
		close(beforeReserveEntered)
		<-allowBeforeReserveProceed
	}

	launcherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case launcherStatusPath:
			mu.Lock()
			activeAtt := pendingSwitchAttempt
			mu.Unlock()
			state := "committed"
			if activeAtt != "" {
				state = "trial"
			}
			_ = json.NewEncoder(w).Encode(LauncherSlotStatus{
				Supported:       true,
				Mode:            "docker",
				BaseVersion:     "v2.0.0-beta.1",
				RunningVersion:  "v2.0.0-beta.2",
				ActiveSlot:      "v2.0.0-beta.2",
				State:           state,
				ActiveAttemptID: activeAtt,
				LastAttemptID:   activeAtt,
				StagedSlots:     []string{"v2.0.0-beta.2", "v2.0.0-beta.3"},
			})
		case launcherActivatePath:
			var req LauncherActivateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode activate request: %v", err)
				return
			}
			// Simulate ValidateManagerDataSnapshotDir still reading snapshotDir on slow disk.
			if _, err := os.ReadFile(filepath.Join(req.SnapshotDir, "database")); err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": "snapshot_invalid", "message": err.Error()})
				activateHandlerDone <- http.StatusUnprocessableEntity
				return
			}
			beforeActivateReserveHook()
			// Re-read after Manager's AbortAttempt returned to prove snapshotDir was NOT deleted!
			if _, err := os.ReadFile(filepath.Join(req.SnapshotDir, "database")); err != nil {
				t.Errorf("snapshotDir removed while Launcher validation was still in flight: %v", err)
			}
			mu.Lock()
			_, aborted := abortedAttempts[req.AttemptID]
			if r.Context().Err() != nil || aborted {
				mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"code":    "slot_conflict",
					"message": "activation attempt was canceled or aborted",
				})
				activateHandlerDone <- http.StatusConflict
				return
			}
			pendingSwitchAttempt = req.AttemptID
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(SlotActionAccepted{
				Accepted:            true,
				TargetVersion:       req.TargetVersion,
				TrialTimeoutSeconds: 60,
				AttemptID:           req.AttemptID,
			})
			activateHandlerDone <- http.StatusAccepted
		case launcherAbortAttemptPath:
			abortCalls.Add(1)
			var req LauncherAbortAttemptRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode abort-attempt: %v", err)
				return
			}
			mu.Lock()
			outcome := LauncherAbortOutcomeAccepted
			if pendingSwitchAttempt != req.AttemptID {
				abortedAttempts[req.AttemptID] = struct{}{}
				outcome = LauncherAbortOutcomeAborted
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(LauncherAbortAttemptResponse{
				AttemptID: req.AttemptID,
				Outcome:   outcome,
			})
		}
	}))
	t.Cleanup(launcherSrv.Close)

	client, err := NewLauncherClient(launcherSrv.URL, tokenFile)
	if err != nil {
		t.Fatal(err)
	}

	snapDir := filepath.Join(t.TempDir(), "pre-slot-slow-validation")
	if err := os.MkdirAll(snapDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, "manifest.json"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, "database"), []byte("db-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	var fenceEngaged atomic.Bool
	svc := New(&memoryStore{}, "v2.0.0-beta.2", strings.Repeat("1", 40), false)
	svc.activateTimeout = 60 * time.Millisecond
	svc.WithLauncherClient(client).
		WithPreActivateFence(func(context.Context) (func(), error) {
			fenceEngaged.Store(true)
			return func() { fenceEngaged.Store(false) }, nil
		}).
		WithPreSlotSnapshot(func(context.Context, string) (string, int, error) {
			return snapDir, 1, nil
		})

	actDone := make(chan error, 1)
	go func() {
		_, actErr := svc.ActivateSlot(context.Background(), "v2.0.0-beta.3")
		actDone <- actErr
	}()

	<-beforeReserveEntered
	// Manager times out Activate after 60ms, calls AbortAttempt -> "aborted", and releases write fence.
	select {
	case actErr := <-actDone:
		if actErr == nil {
			t.Fatal("expected ActivateSlot timeout error after slow snapshot validation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ActivateSlot to abort timed-out attempt")
	}

	if abortCalls.Load() != 1 {
		t.Fatalf("expected exactly 1 AbortAttempt call, got %d", abortCalls.Load())
	}
	if fenceEngaged.Load() {
		t.Fatal("expected write fence to be released after AbortAttempt confirmed outcome=aborted")
	}
	if _, statErr := os.Stat(filepath.Join(snapDir, "database")); statErr != nil {
		t.Fatalf("expected snapshotDir preserved for in-flight Launcher validation, statErr=%v", statErr)
	}

	// Now let Launcher's slow snapshot validation finish: it must refuse to accept the aborted attempt!
	close(allowBeforeReserveProceed)
	select {
	case code := <-activateHandlerDone:
		if code != http.StatusConflict {
			t.Fatalf("expected Launcher handleActivate to reject aborted attempt with 409, got %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for slow Launcher handleActivate to finish")
	}
	mu.Lock()
	gotPending := pendingSwitchAttempt
	mu.Unlock()
	if gotPending != "" {
		t.Fatalf("expected pendingSwitchAttempt to remain empty after tombstone, got %q", gotPending)
	}
}

func TestUpdateCheckPersistChannelClaimDismissSingleLeaseUnderTrialHold(t *testing.T) {
	t.Cleanup(func() {
		middleware.SetProcessTrialHoldGate(nil)
		middleware.ReleaseProcessTrialHold()
	})
	gate := middleware.NewTrialHoldGate(false, "")
	middleware.SetProcessTrialHoldGate(gate)

	st := &blockingSaveUpdateStore{
		enteredSave: make(chan struct{}),
		allowSave:   make(chan struct{}),
	}
	svc := New(st, "v2.0.0-beta.1", strings.Repeat("1", 40), false)

	// 1. In-flight PersistChannel holds its single lease across SaveUpdateCheck so
	// WaitOtherInflightMutations blocks until SQLite persistence completes.
	persistDone := make(chan error, 1)
	go func() {
		_, err := svc.PersistChannel(context.Background(), "beta")
		persistDone <- err
	}()
	<-st.enteredSave

	middleware.EngageProcessTrialHold()
	drainDone := make(chan error, 1)
	go func() {
		drainDone <- middleware.WaitOtherInflightMutations(context.Background())
	}()
	select {
	case err := <-drainDone:
		t.Fatalf("WaitOtherInflightMutations returned before PersistChannel finished writing SQLite: %v", err)
	case <-time.After(40 * time.Millisecond):
	}

	close(st.allowSave)
	if err := <-persistDone; err != nil {
		t.Fatalf("in-flight PersistChannel failed: %v", err)
	}
	if err := <-drainDone; err != nil {
		t.Fatalf("WaitOtherInflightMutations failed: %v", err)
	}
	if saved, err := svc.SavedChannel(context.Background()); err != nil || saved != "beta" {
		t.Fatalf("expected PersistChannel to durably write beta before drain completed, got saved=%q err=%v", saved, err)
	}

	// 2. While TrialHold is active, PersistChannel, SetChannel, Claim, and Dismiss
	// fail closed with 503 upgrade_trial_verifying without mutating in-memory Preference.
	st.allowSave = nil
	st.enteredSave = nil
	beforeSaveCalls := st.saveCalls.Load()

	_, persistErr := svc.PersistChannel(context.Background(), "stable")
	var suErr *SelfUpdateError
	if !errors.As(persistErr, &suErr) || suErr.HTTPStatus != http.StatusServiceUnavailable || suErr.Code != middleware.TrialHoldErrorCode {
		t.Fatalf("expected 503 %s from PersistChannel under TrialHold, got %v", middleware.TrialHoldErrorCode, persistErr)
	}
	if status, err := svc.Status(context.Background()); err != nil || status.ChannelPreference != "beta" {
		t.Fatalf("expected in-memory ChannelPreference to remain beta when PersistChannel is held, got status=%+v err=%v", status, err)
	}
	if st.saveCalls.Load() != beforeSaveCalls {
		t.Fatalf("expected zero SaveUpdateCheck calls for blocked PersistChannel, got %d -> %d", beforeSaveCalls, st.saveCalls.Load())
	}

	if _, claimErr := svc.Claim(context.Background()); !errors.As(claimErr, &suErr) || suErr.HTTPStatus != http.StatusServiceUnavailable || suErr.Code != middleware.TrialHoldErrorCode {
		t.Fatalf("expected 503 %s from Claim under TrialHold, got %v", middleware.TrialHoldErrorCode, claimErr)
	}
	if dismissErr := svc.Dismiss(context.Background(), "v2.0.0-beta.2"); !errors.As(dismissErr, &suErr) || suErr.HTTPStatus != http.StatusServiceUnavailable || suErr.Code != middleware.TrialHoldErrorCode {
		t.Fatalf("expected 503 %s from Dismiss under TrialHold, got %v", middleware.TrialHoldErrorCode, dismissErr)
	}
}
