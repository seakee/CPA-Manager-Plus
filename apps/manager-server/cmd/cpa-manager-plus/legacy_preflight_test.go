package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func managerSourceSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	state := map[string]string{}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		var digest [32]byte
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest = sha256.Sum256(data)
		}
		state[path] = fmt.Sprintf("%s/%s/%x", info.Mode(), info.ModTime(), digest)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

func ownedPreflightEnvironment(dir, path string) []string {
	overrides := map[string]string{
		"CPA_MANAGER_CONFIG": filepath.Join(dir, "config.json"), "CPA_UPSTREAM_URL": "",
		"CPA_MANAGEMENT_KEY": "", "CPA_MANAGEMENT_KEY_FILE": filepath.Join(dir, "no-management-secret"),
		"CPA_MANAGER_DATA_KEY": "", "CPA_MANAGER_DATA_KEY_FILE": filepath.Join(dir, "no-key-secret"),
		"CPAMP_RUNTIME_URL": "", "CPAMP_RUNTIME_TOKEN_FILE": "",
	}
	var env []string
	for _, entry := range managerServerTestEnvironment(dir, path) {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func writeOwnedProvisioningConfig(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"dataDir":"."}`), 0600); err != nil {
		t.Fatal(err)
	}
}

type fenceCheckingDiscoveryWriter struct {
	*httptest.ResponseRecorder
	check func()
}

func (w fenceCheckingDiscoveryWriter) WriteHeader(status int) {
	w.check()
	w.ResponseRecorder.WriteHeader(status)
}

func TestReadOnlyDiscoveryRetainsFenceThroughResponseAndRevokesBeforeWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table settings(key text primary key,value text);create table usage_events(id integer primary key)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	marker, err := processlock.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := marker.Close(); err != nil {
		t.Fatal(err)
	}
	handler := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	handler.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(path), legacypreflight.Options{}, time.Second)
	response := httptest.NewRecorder()
	checked := false
	handler.ServeHTTP(fenceCheckingDiscoveryWriter{response, func() {
		checked = true
		other, err := processlock.AcquireExisting(path)
		if other != nil {
			_ = other.Close()
		}
		if !errors.Is(err, processlock.ErrLocked) {
			t.Fatalf("discovery emitted its response without a fence: %v", err)
		}
	}}, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
	if !checked || response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"mode":"recovery"`) {
		t.Fatalf("fenced response status=%d body=%s", response.Code, response.Body.String())
	}
	operatorLock, err := processlock.AcquireExisting(path)
	if err != nil {
		t.Fatalf("response retained its fence: %v", err)
	}
	if err := operatorLock.Close(); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil).WithContext(canceled))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), `"mode"`) {
		t.Fatalf("canceled request reused evidence: status=%d body=%s", response.Code, response.Body.String())
	}
	handler.BeginWritableStartup()
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), `"mode"`) {
		t.Fatalf("writable startup retained read-only authority: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestFreshNoConfigNormalStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	environment := ownedPreflightEnvironment(dir, path)
	for i, entry := range environment {
		if strings.HasPrefix(entry, "CPA_MANAGER_CONFIG=") {
			environment[i] = "CPA_MANAGER_CONFIG="
		}
	}
	for range 2 {
		func() {
			process := startManagerServerProcessWithEnvironment(t, environment)
			defer process.stop(t)
			deadline := time.Now().Add(5 * time.Second)
			status, body := waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.Path)
			for status == http.StatusServiceUnavailable && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
				status, body = waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.Path)
			}
			if status != http.StatusOK || !strings.Contains(body, `"mode":"fresh"`) || !strings.Contains(body, `"status":"fresh_claim_required"`) {
				t.Fatalf("real Fresh startup did not expose claim entry: %d %s", status, body)
			}
			before := managerSourceSnapshot(t, dir)
			for range 8 {
				status, body = waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.Path)
				if status != http.StatusOK || !strings.Contains(body, `"mode":"fresh"`) {
					t.Fatalf("Fresh discovery was only transient: %d %s", status, body)
				}
			}
			for _, endpoint := range []struct {
				method, path string
				status       int
			}{
				{http.MethodGet, "/health", http.StatusServiceUnavailable},
				{http.MethodPost, "/usage-service/config", http.StatusNotFound},
				{http.MethodPost, bootstrapdiscovery.Path, http.StatusMethodNotAllowed},
			} {
				status, _ := waitForManagerResponse(t, process.addr, endpoint.method, endpoint.path)
				if status != endpoint.status {
					t.Fatalf("Fresh %s %s = %d", endpoint.method, endpoint.path, status)
				}
			}
			if !reflect.DeepEqual(before, managerSourceSnapshot(t, dir)) {
				t.Fatal("Fresh HTTP requests changed the source")
			}
			for _, file := range []string{path, filepath.Join(dir, "data.key"), filepath.Join(dir, "config.json"), path + ".manager.lock"} {
				if _, err := os.Lstat(file); !os.IsNotExist(err) {
					t.Fatalf("Fresh startup created product or coordination state: %s err=%v", file, err)
				}
			}
		}()
		if _, err := os.Lstat(path + ".manager.lock"); !os.IsNotExist(err) {
			t.Fatalf("Fresh shutdown retained a process marker: %v", err)
		}
	}
}

func writeHistoricalProfileSource(t *testing.T, path, ref string) {
	t.Helper()
	data, err := os.ReadFile("../../internal/service/legacypreflight/testdata/legacy-profiles/schema-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Fixtures []struct {
			Ref string
			DDL []struct{ SQL string }
		}
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range manifest.Fixtures {
		if fixture.Ref != ref {
			continue
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for _, ddl := range fixture.DDL {
			if _, err := db.Exec(ddl.SQL); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("missing fixed-source schema fixture", ref)
}

func TestBlockedLegacyNormalStartupHasNoSourceSideEffects(t *testing.T) {
	for _, kind := range []string{"legacy", "legacy-profile-A", "legacy-profile-B", "legacy-profile-C", "legacy-profile-partial", "unknown", "corrupt", "empty-database", "orphan-wal", "old-lock-only", "old-lock-initialize", "symlink-config", "old-config-only", "plain-config-only", "admin-key-config-only", "external-data-key-config-only", "recognized-config-only", "inspection-timeout", "invalid-timeout"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "usage.sqlite")
			writeOwnedProvisioningConfig(t, dir)
			switch kind {
			case "legacy-profile-A", "legacy-profile-B", "legacy-profile-C", "legacy-profile-partial":
				ref := map[string]string{"legacy-profile-A": "v1.12.12", "legacy-profile-B": "v1.13.1", "legacy-profile-C": "v1.14.4", "legacy-profile-partial": "v1.14.4"}[kind]
				writeHistoricalProfileSource(t, path, ref)
				if kind == "legacy-profile-partial" {
					db, err := sql.Open("sqlite", path)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := db.Exec("alter table usage_events drop column request_id"); err != nil {
						db.Close()
						t.Fatal(err)
					}
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
				}
			case "old-lock-only", "old-lock-initialize":
				lock, err := processlock.Acquire(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
				if kind == "old-lock-only" {
					if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
						t.Fatal(err)
					}
				}
			case "symlink-config":
				targetDir := t.TempDir()
				target := filepath.Join(targetDir, "config.json")
				if err := os.WriteFile(target, []byte(`{broken`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, "config.json")); err != nil {
					t.Fatal(err)
				}
				targetBefore := managerSourceSnapshot(t, targetDir)
				t.Cleanup(func() {
					if !reflect.DeepEqual(targetBefore, managerSourceSnapshot(t, targetDir)) {
						t.Error("config link rejection changed independent target")
					}
				})
			case "inspection-timeout", "invalid-timeout":
				db, err := sqliterepo.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			case "legacy", "unknown":
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				query := `create table settings(key text primary key,value text);create table usage_events(id integer primary key)`
				if kind == "unknown" {
					query = `create table unrelated(id integer)`
				}
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
				_ = db.Close()
			case "corrupt":
				if err := os.WriteFile(path, []byte("owned-bad-db"), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty-database":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "orphan-wal":
				if err := os.WriteFile(path+"-wal", []byte("owned-orphan"), 0600); err != nil {
					t.Fatal(err)
				}
			case "old-config-only":
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"dataDir":".","cpaUpstreamUrl":"https://owned.invalid"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "admin-key-config-only", "external-data-key-config-only", "recognized-config-only":
				keyDir := t.TempDir()
				keyPath := filepath.Join(keyDir, "owned.key")
				if err := os.WriteFile(keyPath, []byte("OWNED_EXISTING_KEY"), 0600); err != nil {
					t.Fatal(err)
				}
				value := map[string]any{"dataDir": "."}
				if kind == "admin-key-config-only" {
					value["adminKeyFile"] = keyPath
				} else if kind == "external-data-key-config-only" {
					value["dataKeyFile"] = keyPath
				} else {
					value["usageArchiveRetentionEnabled"] = true
				}
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "config.json"), encoded, 0600); err != nil {
					t.Fatal(err)
				}
				keyBefore := managerSourceSnapshot(t, keyDir)
				t.Cleanup(func() {
					if !reflect.DeepEqual(keyBefore, managerSourceSnapshot(t, keyDir)) {
						t.Error("blocked startup changed independent credential source")
					}
				})
			}
			before := managerSourceSnapshot(t, dir)
			if strings.HasPrefix(kind, "legacy-profile-") {
				paths := legacypreflight.DefaultPaths(path)
				paths.Config = filepath.Join(dir, "config.json")
				report, err := legacypreflight.Inspect(context.Background(), paths, legacypreflight.Options{SourceStopped: true})
				if err != nil {
					t.Fatal(err)
				}
				wantCandidate := kind != "legacy-profile-partial"
				if (report.PlannedBridgeCandidate != nil) != wantCandidate || report.ExactSourceRelease.Status != "unknown" || report.UpgradeAuthorized {
					t.Fatalf("diagnostic target changed source identity/authority: %+v", report)
				}
				if wantCandidate && (report.PlannedBridgeCandidate.Status != "planned" || report.PlannedBridgeCandidate.Release != "v1.14.4") {
					t.Fatal("legacy startup regression did not inspect the planned target")
				}
			}
			env := ownedPreflightEnvironment(dir, path)
			if kind == "old-lock-only" {
				env = append(env, "CPA_MANAGER_CONFIG=")
			}
			if kind == "inspection-timeout" {
				env = append(env, "CPA_MANAGER_PREFLIGHT_TIMEOUT=1ns")
			}
			if kind == "invalid-timeout" {
				env = append(env, "CPA_MANAGER_PREFLIGHT_TIMEOUT=0")
			}
			if kind == "invalid-timeout" || kind == "symlink-config" || kind == "old-lock-initialize" {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				args := []string{"-test.run=^TestManagerServerHelperProcess$"}
				if kind == "old-lock-initialize" {
					args = append(args, "--", "initialize")
				}
				cmd := exec.CommandContext(ctx, os.Args[0], args...)
				cmd.Env = env
				output, err := cmd.CombinedOutput()
				if err == nil || ctx.Err() != nil || strings.Contains(string(output), "listening on") {
					t.Fatalf("unsafe startup: err=%v output=%s", err, output)
				}
				if kind == "symlink-config" && !strings.Contains(string(output), "config is not a regular file") {
					t.Fatalf("config link was read before rejection: %s", output)
				}
				if kind == "invalid-timeout" && !strings.Contains(string(output), "CPA_MANAGER_PREFLIGHT_TIMEOUT") {
					t.Fatalf("timeout feedback missing: %s", output)
				}
			} else {
				startupArgs := []string(nil)
				if kind == "old-lock-initialize" {
					startupArgs = []string{"initialize"}
				}
				process := startManagerServerProcessWithEnvironment(t, env, startupArgs...)
				discoveryStatus, discoveryBody := waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.Path)
				if strings.HasPrefix(kind, "legacy") {
					// Listener binding intentionally precedes admission/evidence gathering,
					// so unavailable is a valid transient state. Legacy acceptance requires
					// eventual bounded read-only classification, not first-request success.
					deadline := time.Now().Add(10 * time.Second)
					for discoveryStatus == http.StatusServiceUnavailable && time.Now().Before(deadline) {
						time.Sleep(10 * time.Millisecond)
						discoveryStatus, discoveryBody = waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.Path)
					}
				}
				if discoveryStatus != http.StatusOK && discoveryStatus != http.StatusServiceUnavailable {
					t.Fatalf("blocked %s discovery status=%d body=%s", kind, discoveryStatus, discoveryBody)
				}
				if strings.HasPrefix(kind, "legacy") {
					allowedMode := strings.Contains(discoveryBody, `"mode":"recovery"`) ||
						strings.Contains(discoveryBody, `"mode":"unsupported_upgrade"`)
					forbiddenMode := strings.Contains(discoveryBody, `"mode":"fresh"`) ||
						strings.Contains(discoveryBody, `"mode":"upgrade"`) ||
						strings.Contains(discoveryBody, `"mode":"completed"`)
					if discoveryStatus != http.StatusOK || !allowedMode || forbiddenMode {
						t.Fatalf("unmarked legacy %s discovery status=%d body=%s", kind, discoveryStatus, discoveryBody)
					}
				}
				if kind == "empty-database" && (discoveryStatus != http.StatusServiceUnavailable || strings.Contains(discoveryBody, `"mode"`)) {
					t.Fatalf("unfenced empty source manufactured a mode: status=%d body=%s", discoveryStatus, discoveryBody)
				}
				if kind == "corrupt" && (discoveryStatus != http.StatusServiceUnavailable || strings.Contains(discoveryBody, `"mode"`)) {
					t.Fatalf("unsafe damaged source manufactured a mode: status=%d body=%s", discoveryStatus, discoveryBody)
				}
				healthStatus, _ := waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.HealthPath)
				if healthStatus != http.StatusServiceUnavailable {
					t.Fatalf("blocked %s health status=%d", kind, healthStatus)
				}
				mutationStatus, _ := waitForManagerResponse(t, process.addr, http.MethodPost, "/usage-service/config")
				if mutationStatus != http.StatusNotFound {
					t.Fatalf("blocked %s exposed mutation path with status=%d", kind, mutationStatus)
				}
				process.stop(t)
			}
			if !reflect.DeepEqual(before, managerSourceSnapshot(t, dir)) {
				t.Fatal("blocked startup changed source/config/key/lock")
			}
		})
	}
}

func TestStartupDiscoveryRunsBeforeWritableMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table settings(key text primary key,value text); create table usage_events(id integer primary key)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	credential, err := security.NewAdminCredential("cpamp_real_fixture_key", "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into settings(key,value) values('admin_credential_v1',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// A real legacy source predates the v2 Manager marker. Discovery must use
	// no-migrate read-only evidence without creating source-adjacent ownership.
	if _, err := os.Lstat(path + ".manager.lock"); !os.IsNotExist(err) {
		t.Fatalf("legacy fixture unexpectedly has Manager marker: %v", err)
	}
	before := managerSourceSnapshot(t, dir)
	process := startManagerServerProcessWithEnvironment(t, ownedPreflightEnvironment(dir, path))
	defer process.stop(t)

	status, body := waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.Path)
	// Listener binding deliberately precedes admission and evidence gathering.
	deadline := time.Now().Add(10 * time.Second)
	for status == http.StatusServiceUnavailable && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		status, body = waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.Path)
	}
	if status != http.StatusOK || !strings.Contains(body, `"mode":"unsupported_upgrade"`) || !strings.Contains(body, `"qualification_unavailable"`) {
		t.Fatalf("legacy discovery status=%d body=%s", status, body)
	}
	if _, err := os.Lstat(filepath.Join(dir, "data.key")); !os.IsNotExist(err) {
		t.Fatalf("discovery created data.key: %v", err)
	}
	if after := managerSourceSnapshot(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatal("discovery changed source/config/bootstrap state before writable startup")
	}
	healthStatus, _ := waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.HealthPath)
	if healthStatus != http.StatusServiceUnavailable {
		t.Fatalf("pre-write health status=%d", healthStatus)
	}
	mutationStatus, _ := waitForManagerResponse(t, process.addr, http.MethodPost, "/usage-service/config")
	if mutationStatus != http.StatusNotFound {
		t.Fatalf("pre-write mutation route status=%d want 404", mutationStatus)
	}
	if after := managerSourceSnapshot(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatal("discovery/blocked startup changed source/config/bootstrap state")
	}

	assertDiscovery := func(wantStatus int, wantMode string) {
		t.Helper()
		before := managerSourceSnapshot(t, dir)
		status, body := waitForManagerResponse(t, process.addr, http.MethodGet, bootstrapdiscovery.Path)
		if status != wantStatus || (wantMode == "" && strings.Contains(body, `"mode"`)) ||
			(wantMode != "" && !strings.Contains(body, `"mode":"`+wantMode+`"`)) {
			t.Fatalf("current source discovery status=%d body=%s, want %d mode=%q", status, body, wantStatus, wantMode)
		}
		if !reflect.DeepEqual(before, managerSourceSnapshot(t, dir)) {
			t.Fatal("refresh changed source/config/key/lock state")
		}
	}
	changeSource := func(replace bool, credential string) {
		t.Helper()
		if replace {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if replace {
			if _, err := db.Exec(`create table settings(key text primary key,value text);create table usage_events(id integer primary key)`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`insert or replace into settings(key,value) values('admin_credential_v1',?)`, credential); err != nil {
			t.Fatal(err)
		}
	}
	// The same process re-inspects current evidence on every permitted request.
	changeSource(false, `{"version":1,"salt":"broken"}`)
	assertDiscovery(http.StatusOK, "recovery")
	changeSource(true, string(raw))
	assertDiscovery(http.StatusOK, "unsupported_upgrade")
	marker, err := processlock.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := marker.Close(); err != nil {
		t.Fatal(err)
	}
	operatorLock, err := processlock.AcquireExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	assertDiscovery(http.StatusServiceUnavailable, "")
	if err := operatorLock.Close(); err != nil {
		t.Fatal(err)
	}
	assertDiscovery(http.StatusOK, "unsupported_upgrade")
	if err := os.Remove(path + ".manager.lock"); err != nil {
		t.Fatal(err)
	}
	assertDiscovery(http.StatusOK, "unsupported_upgrade")
}

func TestFreshReadOnlyDiscoveryDoesNotCreateArtifacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	paths := legacypreflight.DefaultPaths(path)
	paths.Config = filepath.Join(dir, "config.json")
	before := managerSourceSnapshot(t, dir)
	report, err := legacypreflight.Inspect(context.Background(), paths, legacypreflight.Options{SourceStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	result := bootstrapdiscovery.Project(bootstrapdiscovery.Input{Report: report})
	if result.Status != bootstrap.PublicFreshClaimRequired || result.Mode == nil || *result.Mode != bootstrap.Fresh ||
		result.Reason != bootstrap.ReasonFreshClaimRequired || !result.AuthenticationRequired {
		t.Fatalf("fresh read-only discovery=%#v report=%+v", result, report)
	}
	response := httptest.NewRecorder()
	bootstrapdiscovery.Handler{Discovery: result}.ServeHTTP(response, httptest.NewRequest(http.MethodGet, bootstrapdiscovery.Path, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"mode":"fresh"`) {
		t.Fatalf("fresh discovery HTTP response=%d body=%s", response.Code, response.Body.String())
	}
	if _, err := os.Lstat(filepath.Join(dir, "data.key")); !os.IsNotExist(err) {
		t.Fatalf("fresh discovery created data.key: %v", err)
	}
	if after := managerSourceSnapshot(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatal("fresh read-only discovery changed source-adjacent state")
	}
}

func TestNativeSourceDiscoveryDoesNotInferCompleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	runFreshInitializeProcess(t, ownedPreflightEnvironment(dir, path))
	first := startManagerServerProcess(t, dir, path)
	status, body := waitForManagerResponse(t, first.addr, http.MethodGet, bootstrapdiscovery.Path)
	if status != http.StatusServiceUnavailable || strings.Contains(body, `"mode"`) {
		t.Fatalf("fresh graph readiness completed Bootstrap or reused Fresh evidence: status=%d body=%s", status, body)
	}
	first.stop(t)
	second := startManagerServerProcess(t, dir, path)
	defer second.stop(t)
	status, body = waitForManagerResponse(t, second.addr, http.MethodGet, bootstrapdiscovery.Path)
	if status != http.StatusServiceUnavailable || strings.Contains(body, `"mode"`) {
		t.Fatalf("native graph readiness inferred completion or reused pre-write evidence: status=%d body=%s", status, body)
	}
}

func TestNativePreflightRetainsEncryptedKeyGuardsForBothSettings(t *testing.T) {
	for _, setting := range []string{"setup", "manager_config_v1"} {
		t.Run(setting, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "usage.sqlite")
			writeOwnedProvisioningConfig(t, dir)
			db, err := sqliterepo.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			protector, err := security.NewProtector(bytes.Repeat([]byte{42}, 32))
			if err != nil {
				t.Fatal(err)
			}
			ciphertext, err := protector.ProtectString("OWNED_SECRET")
			if err != nil {
				t.Fatal(err)
			}
			value := `{"managementKey":"` + ciphertext + `"}`
			if setting == "manager_config_v1" {
				value = `{"cpaConnection":{"managementKey":"` + ciphertext + `"}}`
			}
			if _, err := db.Exec(`insert into settings(key,value,updated_at_ms)values(?,?,1)`, setting, value); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`insert into settings(key,value,updated_at_ms)values('bootstrap_state_v1','{"connectionStorageMigrationVersion":2}',1)`); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			lock, err := processlock.Acquire(path)
			if err != nil {
				t.Fatal(err)
			}
			_ = lock.Close()
			before := managerSourceSnapshot(t, dir)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagerServerHelperProcess$")
			cmd.Env = ownedPreflightEnvironment(dir, path)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "data key is missing") || strings.Contains(string(output), "OWNED_SECRET") {
				t.Fatalf("missing-key guard err=%v output=%s", err, output)
			}
			if !reflect.DeepEqual(before, managerSourceSnapshot(t, dir)) {
				t.Fatal("missing-key startup created a replacement key or changed source")
			}
		})
	}
}

func TestLegacyPreflightMainDispatchReturnsBeforeServer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	before := managerSourceSnapshot(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLegacyPreflightCLIHelperProcess$", "--", "legacy-preflight", "--db", path)
	cmd.Env = append(ownedPreflightEnvironment(dir, path), "CPAMP_PREFLIGHT_CLI_HELPER=1")
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err == nil || ctx.Err() != nil {
		t.Fatalf("CLI err=%v", err)
	}
	var report legacypreflight.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("JSON=%s err=%v", out.String(), err)
	}
	if report.SourceKind != "missing-database" || strings.Contains(errOut.String(), "listening on") {
		t.Fatal("CLI dispatched server")
	}
	if !reflect.DeepEqual(before, managerSourceSnapshot(t, dir)) {
		t.Fatal("CLI generated normal startup artifacts")
	}
}

func TestLegacyPreflightRejectsNonregularSourceBeforeTimeout(t *testing.T) {
	if _, err := exec.LookPath("mkfifo"); err != nil {
		t.Skip("named pipes unavailable on this platform")
	}
	for _, role := range []string{"database", "config"} {
		t.Run(role, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "usage.sqlite")
			fifo := path
			args := []string{"legacy-preflight", "--db", path, "--timeout", "50ms"}
			if role == "config" {
				fifo = filepath.Join(dir, "config.json")
				args = append(args, "--config", fifo)
			}
			if output, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
				t.Fatalf("create owned FIFO: %v %s", err, output)
			}
			before := managerSourceSnapshot(t, dir)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestLegacyPreflightCLIHelperProcess$", "--"}, args...)...)
			cmd.Env = append(ownedPreflightEnvironment(dir, path), "CPAMP_PREFLIGHT_CLI_HELPER=1")
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			err := cmd.Run()
			if err == nil || ctx.Err() != nil {
				t.Fatalf("nonregular %s did not fail promptly: err=%v ctx=%v output=%s", role, err, ctx.Err(), errOut.String())
			}
			var report legacypreflight.Report
			if err := json.Unmarshal(out.Bytes(), &report); err != nil || len(report.Blocked) == 0 {
				t.Fatalf("nonregular source lacks blocked JSON: %s err=%v", out.String(), err)
			}
			if !reflect.DeepEqual(before, managerSourceSnapshot(t, dir)) {
				t.Fatal("nonregular source rejection changed source")
			}
		})
	}
}

func TestLegacyPreflightCLIHelperProcess(t *testing.T) {
	if os.Getenv("CPAMP_PREFLIGHT_CLI_HELPER") != "1" {
		t.Skip("helper process")
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"cpa-manager-plus"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing CLI separator")
}

func TestNativeV2FreshAndReopenStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	for _, entry := range ownedPreflightEnvironment(dir, path) {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CPA_") || strings.HasPrefix(key, "CPAMP_RUNTIME_") {
			t.Setenv(key, value)
		}
	}
	runFreshInitializeProcess(t, ownedPreflightEnvironment(dir, path))
	first := startManagerServerProcess(t, dir, path)
	assertFreshPendingDiscovery(t, first.addr)
	first.stop(t)
	paths := legacypreflight.DefaultPaths(path)
	paths.Config = filepath.Join(dir, "config.json")
	before := managerSourceSnapshot(t, dir)
	report, err := legacypreflight.Inspect(context.Background(), paths, legacypreflight.Options{SourceStopped: true})
	if err != nil || report.SourceKind != "native-v2" || report.PlannedBridgeCandidate != nil || report.UpgradeAuthorized {
		t.Fatalf("native source acquired a legacy planned target: report=%+v err=%v", report, err)
	}
	if !reflect.DeepEqual(before, managerSourceSnapshot(t, dir)) {
		t.Fatal("native diagnostic inspection changed source")
	}
	if _, err := os.Stat(filepath.Join(dir, "data.key")); err != nil {
		t.Fatal("fresh native key was not created")
	}
	second := startManagerServerProcess(t, dir, path)
	assertFreshPendingDiscovery(t, second.addr)
	second.stop(t)
}

func TestFreshInitializationFailureAllowsRetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions; Windows tests fenced cancellation and cleanup directly")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	keyDir := t.TempDir()
	keyPath := filepath.Join(keyDir, "data.key")
	if err := os.Chmod(keyDir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(keyDir, 0700)
	// Root bypasses this owned filesystem fault, so verify the precondition.
	if err := os.WriteFile(filepath.Join(keyDir, "probe"), nil, 0600); err == nil {
		t.Skip("current user bypasses read-only directory permissions")
	}
	env := ownedPreflightEnvironment(dir, path)
	for i, entry := range env {
		if strings.HasPrefix(entry, "CPA_MANAGER_DATA_KEY_PATH=") {
			env[i] = "CPA_MANAGER_DATA_KEY_PATH=" + keyPath
		}
	}
	before := managerSourceSnapshot(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagerServerHelperProcess$", "--", "initialize")
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err == nil || ctx.Err() != nil || !strings.Contains(string(output), "Fresh initialization/handoff unavailable") {
		t.Fatalf("initialization did not reach owned write failure: %v\n%s", err, output)
	}
	// The pending receipt is intentionally durable even before data.key exists.
	// Retry must resume the same admission instead of rolling this marker back.
	after := managerSourceSnapshot(t, dir)
	delete(before, dir)
	delete(after, dir)
	delete(after, path+".manager.lock")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("failed fresh initialization left source evidence:\n%s", output)
	}
	if _, err := os.Lstat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("key created: %v", err)
	}
	markerBefore, err := os.Stat(path + ".manager.lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CPA_") || strings.HasPrefix(key, "CPAMP_RUNTIME_") {
			t.Setenv(key, value)
		}
	}
	runFreshInitializeProcess(t, env)
	markerAfter, err := os.Stat(path + ".manager.lock")
	if err != nil || !os.SameFile(markerBefore, markerAfter) {
		t.Fatal("retry replaced original marker")
	}
	retry := startManagerServerProcessWithEnvironment(t, env)
	defer retry.stop(t)
	assertFreshPendingDiscovery(t, retry.addr)
}

func TestExplicitInitializeWithoutConfigRetainsWritableStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	t.Setenv("CPA_MANAGER_CONFIG", "")
	t.Setenv("CPA_UPSTREAM_URL", "")
	t.Setenv("CPA_MANAGEMENT_KEY", "")
	t.Setenv("CPA_MANAGEMENT_KEY_FILE", filepath.Join(dir, "no-management-secret"))
	t.Setenv("CPA_MANAGER_DATA_KEY", "")
	t.Setenv("CPA_MANAGER_DATA_KEY_FILE", filepath.Join(dir, "no-key-secret"))
	t.Setenv("CPAMP_RUNTIME_URL", "")
	t.Setenv("CPAMP_RUNTIME_TOKEN_FILE", "")
	env := ownedPreflightEnvironment(dir, path)
	for i, value := range env {
		if strings.HasPrefix(value, "CPA_MANAGER_CONFIG=") {
			env[i] = "CPA_MANAGER_CONFIG="
		}
	}
	runFreshInitializeProcess(t, env)
	server := startManagerServerProcessWithEnvironment(t, env)
	defer server.stop(t)
	assertFreshPendingDiscovery(t, server.addr)
}

func runFreshInitializeProcess(t *testing.T, environment []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagerServerHelperProcess$", "--", "initialize")
	command.Env = environment
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("host initialize: %v %s", err, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), bootstrap.FixedBootstrapUIPath+"#handoff=") || strings.Contains(stderr.String(), "handoff=") || strings.Contains(stderr.String(), "admin key generated") {
		t.Fatal("handoff was not restricted to host stdout")
	}
	// The test helper returns from main into testing, which appends PASS on
	// stdout. The real initialize command emits exactly the first handoff line.
	handoff, _, _ := strings.Cut(stdout.String(), "\n")
	return strings.TrimSpace(handoff)
}

func assertFreshPendingDiscovery(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, body := waitForManagerResponse(t, addr, http.MethodGet, bootstrapdiscovery.Path)
		if status == http.StatusServiceUnavailable && strings.Contains(body, `"status":"unavailable"`) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("host-admin Fresh did not remain pre-canonical unavailable")
}

func TestNativeV2CrashWALStartupReadsCommittedUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeV2CrashFixtureProcess$")
	cmd.Env = append(ownedPreflightEnvironment(dir, path), "CPAMP_NATIVE_CRASH_FIXTURE="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture: %v %s", err, output)
	}
	if err := os.Remove(path + "-shm"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range ownedPreflightEnvironment(dir, path) {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "CPA_") || strings.HasPrefix(key, "CPAMP_RUNTIME_") {
			t.Setenv(key, value)
		}
	}
	server := startManagerServerProcess(t, dir, path)
	server.stop(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`select count(*) from usage_events where event_hash='owned-wal-event'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed WAL usage lost: count=%d", count)
	}
}

func TestNativeV2CrashFixtureProcess(t *testing.T) {
	path := os.Getenv("CPAMP_NATIVE_CRASH_FIXTURE")
	if path == "" {
		t.Skip("helper process")
	}
	lock, err := processlock.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock
	db, err := sqliterepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, query := range []string{`pragma wal_autocheckpoint=0`, `pragma wal_checkpoint(truncate)`,
		`insert into usage_events(id,request_id,event_hash,timestamp_ms,timestamp,model,requested_model,created_at_ms) values(1,'owned-request','owned-wal-event',1,'1','owned-model','owned-model',1)`} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(0)
}
