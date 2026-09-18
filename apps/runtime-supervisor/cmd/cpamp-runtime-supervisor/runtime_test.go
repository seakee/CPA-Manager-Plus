package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const startHelperName = "cpamp-start-test-child.exe"

func expectedRuntimeCapabilities() []string {
	capabilities := []string{"start", "stop", "restart"}
	if runtimeupdate.SupportedPlatform(runtime.GOOS, runtime.GOARCH) {
		capabilities = append(capabilities, "prepare_update", "activate_update")
	}
	return append(capabilities, "observe_update_operation")
}

func TestPersistedSelectionDrivesStartupAndContainerGenerationRecreation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable fixture is a Unix shell script")
	}
	directory := t.TempDir()
	bundled := filepath.Join(directory, "bundled-cpa")
	spawnLog := filepath.Join(directory, "selection-spawns")
	bundledBytes := writeArtifactGateway(t, bundled, spawnLog, "A")
	bundledID := exactArtifactID(bundledBytes)
	bundledManifest := filepath.Join(directory, "bundled-artifact.json")
	if err := os.WriteFile(bundledManifest, []byte(fmt.Sprintf(
		`{"schemaVersion":1,"engine":"cpa","version":"7.3.3","artifactId":%q}`, bundledID,
	)), 0o600); err != nil {
		t.Fatal(err)
	}

	supervisorRoot := filepath.Join(directory, "supervisor")
	stageRoot := filepath.Join(supervisorRoot, "artifacts", "cpa")
	stageDirectory := filepath.Join(stageRoot, "7.3.4")
	if err := os.MkdirAll(stageDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	stagedExecutable := filepath.Join(stageDirectory, "cli-proxy-api")
	stagedBytes := writeArtifactGateway(t, stagedExecutable, spawnLog, "B")
	if err := os.Chmod(stagedExecutable, 0o755); err != nil {
		t.Fatal(err)
	}
	stagedID := exactArtifactID(stagedBytes)
	stagedMetadata := filepath.Join(stageDirectory, "artifact.json")
	if err := os.WriteFile(stagedMetadata, []byte(fmt.Sprintf(
		`{"schemaVersion":1,"engine":"cpa","version":"7.3.4","artifactId":%q,"sourceArchiveDigest":"sha256:%s"}`,
		stagedID, strings.Repeat("c", 64),
	)), 0o444); err != nil {
		t.Fatal(err)
	}
	stages, err := runtimeupdate.NewStore(stageRoot)
	if err != nil {
		t.Fatal(err)
	}
	selections, err := selection.NewStore(filepath.Join(supervisorRoot, "active", "cpa"), stages)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := selections.ResolveFinalized("7.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if err := selections.Commit(candidate); err != nil {
		t.Fatal(err)
	}

	cfg := loadTestConfig(t, fixedGeneration(41))
	cfg.journalPath = filepath.Join(supervisorRoot, "operations.sqlite")
	cfg.cpaExecutable = bundled
	cfg.cpaArtifactManifest = bundledManifest
	cfg.cpaAddr = unreadyCPAAddr(t)
	first, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	status := requestRuntime(t, first, "/v1/runtime/status")
	if status.ActiveGatewayArtifact == nil || status.ActiveGatewayArtifact.ArtifactID != stagedID ||
		status.ActiveGatewayArtifact.Version != "7.3.4" || status.RuntimeGeneration != 41 {
		t.Fatalf("selected startup status = %+v", status)
	}
	if started := runtimeStart(first, t.Context(), "start-b-1", 41); started.Code != http.StatusOK {
		t.Fatalf("Start selected B = %d, %s", started.Code, started.Body.String())
	}
	awaitArtifactGatewaySpawns(t, spawnLog, 1)
	if stopped := runtimeStop(first, t.Context(), "stop-b-1", 41); stopped.Code != http.StatusOK {
		t.Fatalf("Stop selected B = %d, %s", stopped.Code, stopped.Body.String())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	cfg.runtimeGeneration = 42
	second, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = second.Close()
		killArtifactGateways(spawnLog)
	})
	status = requestRuntime(t, second, "/v1/runtime/status")
	if status.ActiveGatewayArtifact == nil || status.ActiveGatewayArtifact.ArtifactID != stagedID || status.RuntimeGeneration != 42 {
		t.Fatalf("recreated selected status = %+v", status)
	}
	if started := runtimeStart(second, t.Context(), "start-b-2", 42); started.Code != http.StatusOK {
		t.Fatalf("Start recreated B = %d, %s", started.Code, started.Body.String())
	}
	awaitArtifactGatewaySpawns(t, spawnLog, 2)
}

func TestPersistedSelectionCorruptionFailsStartupWithoutBundledFallback(t *testing.T) {
	directory := t.TempDir()
	bundled := filepath.Join(directory, "bundled-cpa")
	if err := os.WriteFile(bundled, []byte("bundled"), 0o755); err != nil {
		t.Fatal(err)
	}
	supervisorRoot := filepath.Join(directory, "supervisor")
	selectionRoot := filepath.Join(supervisorRoot, "active", "cpa")
	if err := os.MkdirAll(selectionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	selectionJSON := fmt.Sprintf(
		`{"schemaVersion":1,"engine":"cpa","version":"7.3.4","artifactId":"sha256:%s"}`,
		strings.Repeat("a", 64),
	)
	if err := os.WriteFile(filepath.Join(selectionRoot, "selection.json"), []byte(selectionJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := loadTestConfig(t, fixedGeneration(41))
	cfg.journalPath = filepath.Join(supervisorRoot, "operations.sqlite")
	cfg.cpaExecutable = bundled
	cfg.cpaAddr = unreadyCPAAddr(t)
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err == nil || handler != nil || !strings.Contains(err.Error(), "resolve active selection") {
		t.Fatalf("newRuntimeHandler() = %v, %v", handler, err)
	}
}

func TestActiveArtifactObservationIsCachedAndOrthogonalToRuntimeGeneration(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "gateway")
	manifest := filepath.Join(directory, "artifact.json")
	executableBytes := []byte("stable-executable-bytes")
	if err := os.WriteFile(executable, executableBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(executableBytes)
	artifactID := fmt.Sprintf("sha256:%x", digest)
	manifestJSON := fmt.Sprintf(`{"schemaVersion":1,"engine":"cpa","version":"7.3.3","artifactId":%q}`, artifactID)
	if err := os.WriteFile(manifest, []byte(manifestJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := loadTestConfig(t, fixedGeneration(41))
	cfg.journalPath = filepath.Join(directory, "operations.sqlite")
	cfg.cpaExecutable = executable
	cfg.cpaArtifactManifest = manifest
	cfg.cpaAddr = unreadyCPAAddr(t)
	first, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	firstStatus := requestRuntime(t, first, "/v1/runtime/status")
	if firstStatus.RuntimeGeneration != 41 || firstStatus.CPAObservedVersion != "7.3.3" ||
		firstStatus.ActiveGatewayArtifact == nil ||
		firstStatus.ActiveGatewayArtifact.Engine != "cpa" ||
		firstStatus.ActiveGatewayArtifact.ArtifactID != artifactID ||
		firstStatus.ActiveGatewayArtifact.Version != "7.3.3" {
		t.Fatalf("first artifact observation = %+v", firstStatus)
	}

	// Status is a cache read. Even external tampering after the owning startup
	// observation cannot turn polling into repeated full-file hashing.
	if err := os.WriteFile(executable, []byte("tampered-after-startup"), 0o700); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		status := requestRuntime(t, first, "/v1/runtime/status")
		if status.ActiveGatewayArtifact == nil || status.ActiveGatewayArtifact.ArtifactID != artifactID {
			t.Fatalf("status rehashed executable instead of reading cache: %+v", status)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(executable, executableBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.runtimeGeneration = 42
	second, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	secondStatus := requestRuntime(t, second, "/v1/runtime/status")
	if secondStatus.RuntimeGeneration != 42 || secondStatus.ActiveGatewayArtifact == nil ||
		secondStatus.ActiveGatewayArtifact.ArtifactID != artifactID {
		t.Fatalf("new generation changed stable artifact identity: %+v", secondStatus)
	}
}

func TestActiveArtifactRefreshesAtRestartSpawnBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("atomic executable replacement while running is a Unix regression")
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "gateway")
	replacement := filepath.Join(directory, "gateway-next")
	spawnLog := filepath.Join(directory, "artifact-spawns")
	manifest := filepath.Join(directory, "artifact.json")
	bytesA := writeArtifactGateway(t, executable, spawnLog, "A")
	artifactA := exactArtifactID(bytesA)
	manifestJSON := fmt.Sprintf(`{"schemaVersion":1,"engine":"cpa","version":"version-a","artifactId":%q}`, artifactA)
	if err := os.WriteFile(manifest, []byte(manifestJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := loadTestConfig(t, fixedGeneration(41))
	cfg.journalPath = filepath.Join(directory, "operations.sqlite")
	cfg.cpaExecutable = executable
	cfg.cpaArtifactManifest = manifest
	cfg.cpaAddr = unreadyCPAAddr(t)
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = handler.Close()
		killArtifactGateways(spawnLog)
	})

	if started := runtimeStart(handler, t.Context(), "start-a", 41); started.Code != http.StatusOK {
		t.Fatalf("Start A = %d, %s", started.Code, started.Body.String())
	}
	awaitArtifactGatewaySpawns(t, spawnLog, 1)
	statusA := requestRuntime(t, handler, "/v1/runtime/status")
	if statusA.ActiveGatewayArtifact == nil || statusA.ActiveGatewayArtifact.ArtifactID != artifactA ||
		statusA.ActiveGatewayArtifact.Version != "version-a" || statusA.CPAObservedVersion != "version-a" {
		t.Fatalf("spawned A artifact = %+v", statusA)
	}

	bytesB := writeArtifactGateway(t, replacement, spawnLog, "B")
	artifactB := exactArtifactID(bytesB)
	if artifactA == artifactB {
		t.Fatal("test Gateway artifacts unexpectedly have the same digest")
	}
	if err := os.Rename(replacement, executable); err != nil {
		t.Fatal(err)
	}
	if cached := requestRuntime(t, handler, "/v1/runtime/status"); cached.ActiveGatewayArtifact == nil ||
		cached.ActiveGatewayArtifact.ArtifactID != artifactA {
		t.Fatalf("polling changed the pre-spawn cache: %+v", cached)
	}
	if restarted := runtimeRestart(handler, t.Context(), "restart-b", 41); restarted.Code != http.StatusOK {
		t.Fatalf("Restart B = %d, %s", restarted.Code, restarted.Body.String())
	}
	awaitArtifactGatewaySpawns(t, spawnLog, 2)
	statusB := requestRuntime(t, handler, "/v1/runtime/status")
	if statusB.ActiveGatewayArtifact == nil || statusB.ActiveGatewayArtifact.ArtifactID != artifactB ||
		statusB.ActiveGatewayArtifact.Version != "" || statusB.CPAObservedVersion != "" {
		t.Fatalf("replacement spawn did not publish exact B without stale version: %+v", statusB)
	}

	if stopped := runtimeStop(handler, t.Context(), "stop-b", 41); stopped.Code != http.StatusOK {
		t.Fatalf("Stop B = %d, %s", stopped.Code, stopped.Body.String())
	}
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if missing := runtimeStart(handler, t.Context(), "start-missing", 41); missing.Code == http.StatusOK {
		t.Fatalf("missing executable Start unexpectedly succeeded: %s", missing.Body.String())
	}
	missingStatus := requestRuntime(t, handler, "/v1/runtime/status")
	if missingStatus.ActiveGatewayArtifact != nil || missingStatus.CPAObservedVersion != "" {
		t.Fatalf("failed spawn retained stale B identity: %+v", missingStatus)
	}
}

func TestActiveArtifactRefreshesAtAutomaticRecoverySpawnBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("atomic executable replacement while running is a Unix regression")
	}
	directory := t.TempDir()
	executable := filepath.Join(directory, "gateway")
	replacement := filepath.Join(directory, "gateway-next")
	spawnLog := filepath.Join(directory, "artifact-spawns")
	artifactA := exactArtifactID(writeArtifactGateway(t, executable, spawnLog, "A"))

	cfg := loadTestConfig(t, fixedGeneration(41))
	cfg.journalPath = filepath.Join(directory, "operations.sqlite")
	cfg.cpaExecutable = executable
	cfg.cpaAddr = unreadyCPAAddr(t)
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = handler.Close()
		killArtifactGateways(spawnLog)
	})

	if started := runtimeStart(handler, t.Context(), "start-a", 41); started.Code != http.StatusOK {
		t.Fatalf("Start A = %d, %s", started.Code, started.Body.String())
	}
	pids := awaitArtifactGatewaySpawns(t, spawnLog, 1)
	artifactB := exactArtifactID(writeArtifactGateway(t, replacement, spawnLog, "B"))
	if err := os.Rename(replacement, executable); err != nil {
		t.Fatal(err)
	}
	if cached := requestRuntime(t, handler, "/v1/runtime/status"); cached.ActiveGatewayArtifact == nil ||
		cached.ActiveGatewayArtifact.ArtifactID != artifactA {
		t.Fatalf("polling changed the pre-recovery cache: %+v", cached)
	}
	process, err := os.FindProcess(pids[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	awaitArtifactGatewaySpawns(t, spawnLog, 2)
	for deadline := time.Now().Add(3 * time.Second); ; {
		status := requestRuntime(t, handler, "/v1/runtime/status")
		if status.Recovery != nil && status.Recovery.State == "armed" && status.Recovery.AttemptsRemaining == 2 {
			if status.ActiveGatewayArtifact == nil || status.ActiveGatewayArtifact.ArtifactID != artifactB {
				t.Fatalf("automatic recovery did not publish exact B: %+v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("automatic recovery did not settle with B: %+v", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if stopped := runtimeStop(handler, t.Context(), "stop-b", 41); stopped.Code != http.StatusOK {
		t.Fatalf("Stop recovered B = %d, %s", stopped.Code, stopped.Body.String())
	}
}

func writeArtifactGateway(t *testing.T, path, spawnLog, marker string) []byte {
	t.Helper()
	content := []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s %s\\n' \"$$\" >> %q\nexec sleep 30\n", marker, spawnLog))
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	return content
}

func exactArtifactID(content []byte) string {
	digest := sha256.Sum256(content)
	return fmt.Sprintf("sha256:%x", digest)
}

func awaitArtifactGatewaySpawns(t *testing.T, path string, count int) []int {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		encoded, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Fields(string(encoded))
			if len(lines) > count*2 {
				t.Fatalf("artifact Gateway spawn fields = %q, want %d records", lines, count)
			}
			if len(lines) == count*2 {
				pids := make([]int, count)
				for index := range count {
					if _, err := fmt.Sscan(lines[index*2], &pids[index]); err != nil || pids[index] <= 0 {
						t.Fatalf("artifact Gateway PID %q: %v", lines[index*2], err)
					}
				}
				return pids
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("did not observe %d artifact Gateway spawns", count)
	return nil
}

func killArtifactGateways(path string) {
	encoded, _ := os.ReadFile(path)
	fields := strings.Fields(string(encoded))
	for index := 0; index+1 < len(fields); index += 2 {
		var pid int
		if _, err := fmt.Sscan(fields[index], &pid); err == nil && pid > 0 {
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Kill()
			}
		}
	}
}

// A private copy of the test executable selects the helper by filename. The
// configured executable needs neither arguments nor inherited environment.
func TestMain(m *testing.M) {
	if executable, err := os.Executable(); err == nil && filepath.Base(executable) == startHelperName {
		directory := filepath.Dir(executable)
		var names []string
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			names = append(names, name)
		}
		// Only names are evidence; never serialize inherited credential values.
		if err := os.WriteFile(filepath.Join(directory, "environment-names"), []byte(strings.Join(names, "\n")), 0o600); err != nil {
			os.Exit(96)
		}
		file, err := os.OpenFile(filepath.Join(directory, "spawns"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(96)
		}
		_, _ = fmt.Fprintln(file, os.Getpid())
		_ = file.Close()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
			if _, err := os.Stat(filepath.Join(directory, "exit")); err == nil {
				os.Exit(0)
			}
			time.Sleep(5 * time.Millisecond)
		}
		os.Exit(97)
	}
	os.Exit(m.Run())
}

func TestLifecycleConfigurationIsAllOrNothing(t *testing.T) {
	tests := []struct {
		name, journal, executable, manifest string
		wantError                           bool
	}{
		{"read-only", "", "", "", false},
		{"journal only", "operations.sqlite", "", "", true},
		{"executable only", "", "cpa", "", true},
		{"manifest only", "", "", "artifact.json", true},
		{"enabled", " operations.sqlite ", " cpa ", " artifact.json ", false},
		{"invalid executable", "operations.sqlite", "cpa\x00", "", true},
		{"invalid manifest", "operations.sqlite", "cpa", "artifact\x00", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := validConfigValues()
			values["CPAMP_RUNTIME_JOURNAL_PATH"] = test.journal
			values["CPAMP_CPA_EXECUTABLE"] = test.executable
			values["CPAMP_CPA_ARTIFACT_MANIFEST"] = test.manifest
			cfg, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(41))
			if (err != nil) != test.wantError {
				t.Fatalf("loadConfig = %+v, %v", cfg, err)
			}
			if err == nil && (cfg.journalPath != strings.TrimSpace(test.journal) ||
				cfg.cpaExecutable != strings.TrimSpace(test.executable) ||
				cfg.cpaArtifactManifest != strings.TrimSpace(test.manifest)) {
				t.Fatalf("lifecycle configuration = %+v", cfg)
			}
		})
	}
}

func TestRuntimeStartupFailsIfJournalCannotOpen(t *testing.T) {
	cfg := loadTestConfig(t, fixedGeneration(41))
	cfg.cpaExecutable = "cpa"
	cfg.journalPath = t.TempDir() // A directory cannot be opened as SQLite.
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err == nil || handler != nil {
		t.Fatalf("startup with unavailable journal = %+v, %v", handler, err)
	}
}

func TestRuntimeStartEndToEnd(t *testing.T) {
	directory := t.TempDir()
	values := validConfigValues()
	values["CPAMP_RUNTIME_ADDR"] = "127.0.0.1:18318"
	values["CPAMP_RUNTIME_CPA_ADDR"] = unreadyCPAAddr(t)
	values["CPAMP_RUNTIME_JOURNAL_PATH"] = filepath.Join(directory, "runtime", "operations.sqlite")
	values["CPAMP_CPA_EXECUTABLE"] = copyStartHelper(t, directory)
	values["CPAMP_RUNTIME_FUTURE_SECRET"] = "test-only-private-value"
	values["NORMAL_SENTINEL"] = "test-only-ordinary-value"
	values["CPAMP_DEPLOYMENT_SENTINEL"] = "test-only-deployment-value"
	values["HTTP_PROXY"] = "http://http-proxy.invalid:18080"
	values["HTTPS_PROXY"] = "http://https-proxy.invalid:18443"
	values["NO_PROXY"] = "bypass.invalid"
	for key, value := range values {
		t.Setenv(key, value)
	}
	cfg, err := loadConfig(os.Getenv, fixedGeneration(41))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	exitFile := filepath.Join(directory, "exit")
	t.Cleanup(func() { _ = os.WriteFile(exitFile, nil, 0o600) })
	assertRuntime := func(h http.Handler, generation uint64, wantState string) {
		t.Helper()
		for _, path := range []string{"/v1/runtime/handshake", "/v1/runtime/status"} {
			got := requestRuntime(t, h, path)
			if got.RuntimeGeneration != generation || !reflect.DeepEqual(got.Capabilities, expectedRuntimeCapabilities()) ||
				got.CPAObservedVersion != "" {
				t.Fatalf("Start changed authority or invented version: %+v", got)
			}
			if path == "/v1/runtime/status" && ((wantState != "" && got.State != wantState) ||
				(wantState == "" && got.State != "offline" && got.State != "starting")) {
				t.Fatalf("unexpected readiness: %+v, want %q", got, wantState)
			}
			if path == "/v1/runtime/status" {
				if got.Recovery == nil || got.Recovery.AttemptsRemaining < 0 || got.Recovery.AttemptsRemaining > 3 {
					t.Fatalf("invalid recovery observation: %+v", got)
				}
				if wantState == "offline" && (got.Recovery.State != "inactive" || got.Recovery.AttemptsRemaining != 0) {
					t.Fatalf("fresh Supervisor restored a recovery lease: %+v", got)
				}
				if wantState == "starting" && (got.Recovery.State != "armed" || got.Recovery.AttemptsRemaining != 3) {
					t.Fatalf("durably succeeded Start did not arm recovery: %+v", got)
				}
			}
		}
	}
	assertRuntime(handler, 41, "offline")

	// An unauthorized request cannot create intent or start the local executable.
	req := httptest.NewRequest(http.MethodPost, "/v1/runtime/operations/start", strings.NewReader(startBody("unauthorized", 41)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized Start = %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(directory, "spawns")); !os.IsNotExist(err) {
		t.Fatal("unauthorized Start spawned a child")
	}

	ctx, cancel := context.WithCancel(t.Context())
	w = runtimeStart(handler, ctx, "first", 41)
	if w.Code != http.StatusOK {
		t.Fatalf("Start = %d, %s", w.Code, w.Body.String())
	}
	cancel()
	awaitSpawnCount(t, directory, 1)
	assertRuntime(handler, 41, "starting")
	if replay := runtimeStart(handler, t.Context(), "first", 41); replay.Code != http.StatusOK || replay.Body.String() != w.Body.String() {
		t.Fatalf("same-ID replay = %d, %s", replay.Code, replay.Body.String())
	}
	conflict := runtimeStart(handler, t.Context(), "while-running", 41)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "operation_state_conflict") {
		t.Fatalf("new ID while running = %d, %s", conflict.Code, conflict.Body.String())
	}

	reader, err := journal.Open(t.Context(), cfg.journalPath, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	for _, id := range []string{"unauthorized", "while-running"} {
		if _, err := reader.Get(t.Context(), cfg.runtimeIdentity, id); !errors.Is(err, journal.ErrOperationNotFound) {
			t.Fatalf("rejected request %q wrote intent: %v", id, err)
		}
	}
	stored, err := reader.Get(t.Context(), cfg.runtimeIdentity, "first")
	if err != nil || stored.State != journal.StateSucceeded || stored.RuntimeGeneration != 41 {
		t.Fatalf("durable Start result = %+v, %v", stored, err)
	}

	// Only a new ID after confirmed reap can spawn again. An early retry must
	// remain a precondition conflict and must not consume that new ID.
	if err := os.WriteFile(exitFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; {
		next := runtimeStart(handler, t.Context(), "after-exit", 41)
		if next.Code == http.StatusOK {
			break
		}
		if next.Code != http.StatusConflict || time.Now().After(deadline) {
			t.Fatalf("Start after exit = %d, %s", next.Code, next.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	awaitSpawnCount(t, directory, 2)
	assertRuntime(handler, 41, "") // The released helper may already be reaped.
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if closed := runtimeStart(handler, t.Context(), "closed", 41); closed.Code != http.StatusServiceUnavailable {
		t.Fatalf("request after journal shutdown = %d", closed.Code)
	}

	// A fresh Supervisor generation reopens the same durable namespace.
	cfg.runtimeGeneration = 42
	restarted, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	assertRuntime(restarted, 42, "offline")
	if stale := runtimeStart(restarted, t.Context(), "first", 41); stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "stale_runtime_generation") {
		t.Fatalf("stale replay = %d, %s", stale.Code, stale.Body.String())
	}
	replay := runtimeStart(restarted, t.Context(), "first", 42)
	var result struct {
		RuntimeGeneration uint64        `json:"runtimeGeneration"`
		State             journal.State `json:"state"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if replay.Code != http.StatusOK || result.RuntimeGeneration != 41 || result.State != journal.StateSucceeded {
		t.Fatalf("cross-generation replay = %d, %s", replay.Code, replay.Body.String())
	}
	awaitSpawnCount(t, directory, 2)
}

func TestConfiguredRuntimeStatusStartsWithInactiveRecoveryLease(t *testing.T) {
	directory := t.TempDir()
	cfg := loadTestConfig(t, fixedGeneration(41))
	cfg.journalPath = filepath.Join(directory, "runtime", "operations.sqlite")
	cfg.cpaExecutable = filepath.Join(directory, "not-started-cpa")
	cfg.cpaAddr = unreadyCPAAddr(t)
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })

	status := requestRuntime(t, handler, "/v1/runtime/status")
	if status.State != "offline" || status.Recovery == nil || status.Recovery.State != "inactive" ||
		status.Recovery.AttemptsRemaining != 0 || !reflect.DeepEqual(status.Capabilities, expectedRuntimeCapabilities()) {
		t.Fatalf("startup status = %+v", status)
	}
	handshake := requestRuntime(t, handler, "/v1/runtime/handshake")
	if handshake.Recovery != nil {
		t.Fatalf("handshake exposed recovery observation: %+v", handshake)
	}
}

func TestRuntimeAutomaticallyRecoversConfirmedUnexpectedExit(t *testing.T) {
	directory := t.TempDir()
	values := validConfigValues()
	values["CPAMP_RUNTIME_CPA_ADDR"] = unreadyCPAAddr(t)
	values["CPAMP_RUNTIME_JOURNAL_PATH"] = filepath.Join(directory, "runtime", "operations.sqlite")
	values["CPAMP_CPA_EXECUTABLE"] = copyStartHelper(t, directory)
	values["NORMAL_SENTINEL"] = "test-only-ordinary-value"
	values["CPAMP_DEPLOYMENT_SENTINEL"] = "test-only-deployment-value"
	values["HTTP_PROXY"] = "http://http-proxy.invalid:18080"
	values["HTTPS_PROXY"] = "http://https-proxy.invalid:18443"
	values["NO_PROXY"] = "bypass.invalid"
	for key, value := range values {
		t.Setenv(key, value)
	}
	cfg, err := loadConfig(os.Getenv, fixedGeneration(41))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })

	if started := runtimeStart(handler, t.Context(), "start", 41); started.Code != http.StatusOK {
		t.Fatalf("Start = %d, %s", started.Code, started.Body.String())
	}
	awaitSpawnCount(t, directory, 1)
	exitFile := filepath.Join(directory, "exit")
	if err := os.WriteFile(exitFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		status := requestRuntime(t, handler, "/v1/runtime/status")
		if status.State == "offline" && status.Recovery != nil && status.Recovery.State == "recovering" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unexpected exit was not scheduled for recovery: %+v", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.Remove(exitFile); err != nil {
		t.Fatal(err)
	}
	awaitSpawnCount(t, directory, 2)
	for deadline := time.Now().Add(3 * time.Second); ; {
		status := requestRuntime(t, handler, "/v1/runtime/status")
		if status.Recovery != nil && status.Recovery.State == "armed" && status.Recovery.AttemptsRemaining == 2 {
			if status.State != "starting" || status.RuntimeGeneration != 41 ||
				!reflect.DeepEqual(status.Capabilities, expectedRuntimeCapabilities()) {
				t.Fatalf("recovered Runtime changed availability contract: %+v", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("automatic replacement did not arm remaining budget: %+v", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if stopped := runtimeStop(handler, t.Context(), "cleanup-stop", 41); stopped.Code != http.StatusOK {
		t.Fatalf("Stop recovered child = %d, %s", stopped.Code, stopped.Body.String())
	}
	status := requestRuntime(t, handler, "/v1/runtime/status")
	if status.Recovery == nil || status.Recovery.State != "inactive" || status.Recovery.AttemptsRemaining != 0 {
		t.Fatalf("Stop did not disarm recovery: %+v", status)
	}
}

func TestRuntimeStopEndToEndKeepsReplayAwayFromReplacementChild(t *testing.T) {
	directory := t.TempDir()
	values := validConfigValues()
	values["CPAMP_RUNTIME_CPA_ADDR"] = unreadyCPAAddr(t)
	values["CPAMP_RUNTIME_JOURNAL_PATH"] = filepath.Join(directory, "runtime", "operations.sqlite")
	values["CPAMP_CPA_EXECUTABLE"] = copyStartHelper(t, directory)
	values["NORMAL_SENTINEL"] = "test-only-ordinary-value"
	values["CPAMP_DEPLOYMENT_SENTINEL"] = "test-only-deployment-value"
	values["HTTP_PROXY"] = "http://http-proxy.invalid:18080"
	values["HTTPS_PROXY"] = "http://https-proxy.invalid:18443"
	values["NO_PROXY"] = "bypass.invalid"
	for key, value := range values {
		t.Setenv(key, value)
	}
	cfg, err := loadConfig(os.Getenv, fixedGeneration(41))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	exitFile := filepath.Join(directory, "exit")
	t.Cleanup(func() { _ = os.WriteFile(exitFile, nil, 0o600) })

	for _, path := range []string{"/v1/runtime/handshake", "/v1/runtime/status"} {
		got := requestRuntime(t, handler, path)
		if got.RuntimeGeneration != 41 || !reflect.DeepEqual(got.Capabilities, expectedRuntimeCapabilities()) ||
			got.CPAObservedVersion != "" || (path == "/v1/runtime/status" && got.State != "offline") {
			t.Fatalf("configured lifecycle metadata = %+v", got)
		}
	}

	if started := runtimeStart(handler, t.Context(), "first-start", 41); started.Code != http.StatusOK {
		t.Fatalf("Start = %d, %s", started.Code, started.Body.String())
	}
	awaitSpawnCount(t, directory, 1)

	// Authentication fails before decode/journal/process access. The existing
	// child must remain running and the operation ID must remain unseen.
	req := httptest.NewRequest(http.MethodPost, "/v1/runtime/operations/stop", strings.NewReader(startBody("unauthorized-stop", 41)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized Stop = %d, %s", w.Code, w.Body.String())
	}
	if proof := runtimeStart(handler, t.Context(), "still-running", 41); proof.Code != http.StatusConflict ||
		!strings.Contains(proof.Body.String(), "operation_state_conflict") {
		t.Fatalf("unauthorized Stop changed child ownership = %d, %s", proof.Code, proof.Body.String())
	}

	stopped := runtimeStop(handler, t.Context(), "first-stop", 41)
	if stopped.Code != http.StatusOK {
		t.Fatalf("Stop = %d, %s", stopped.Code, stopped.Body.String())
	}
	var stoppedOperation struct {
		RuntimeGeneration uint64        `json:"runtimeGeneration"`
		State             journal.State `json:"state"`
	}
	if err := json.Unmarshal(stopped.Body.Bytes(), &stoppedOperation); err != nil {
		t.Fatal(err)
	}
	if stoppedOperation.RuntimeGeneration != 41 || stoppedOperation.State != journal.StateSucceeded {
		t.Fatalf("Stop result = %s", stopped.Body.String())
	}
	if replay := runtimeStop(handler, t.Context(), "first-stop", 41); replay.Code != http.StatusOK || replay.Body.String() != stopped.Body.String() {
		t.Fatalf("same-ID Stop replay = %d, %s", replay.Code, replay.Body.String())
	}
	if absent := runtimeStop(handler, t.Context(), "already-exited", 41); absent.Code != http.StatusConflict ||
		!strings.Contains(absent.Body.String(), "operation_state_conflict") {
		t.Fatalf("Stop without running child = %d, %s", absent.Code, absent.Body.String())
	}

	if replacement := runtimeStart(handler, t.Context(), "replacement-start", 41); replacement.Code != http.StatusOK {
		t.Fatalf("replacement Start after confirmed reap = %d, %s", replacement.Code, replacement.Body.String())
	}
	awaitSpawnCount(t, directory, 2)
	if replay := runtimeStop(handler, t.Context(), "first-stop", 41); replay.Code != http.StatusOK || replay.Body.String() != stopped.Body.String() {
		t.Fatalf("old Stop replay with replacement child = %d, %s", replay.Code, replay.Body.String())
	}
	if proof := runtimeStart(handler, t.Context(), "replacement-still-running", 41); proof.Code != http.StatusConflict ||
		!strings.Contains(proof.Body.String(), "operation_state_conflict") {
		t.Fatalf("old Stop replay affected replacement child = %d, %s", proof.Code, proof.Body.String())
	}

	reader, err := journal.Open(t.Context(), cfg.journalPath, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, id := range []string{"unauthorized-stop", "still-running", "already-exited", "replacement-still-running"} {
		if _, err := reader.Get(t.Context(), cfg.runtimeIdentity, id); !errors.Is(err, journal.ErrOperationNotFound) {
			t.Fatalf("rejected operation %q wrote intent: %v", id, err)
		}
	}
	stored, err := reader.Get(t.Context(), cfg.runtimeIdentity, "first-stop")
	if err != nil || stored.State != journal.StateSucceeded || stored.OperationType != "stop" || stored.RuntimeGeneration != 41 {
		t.Fatalf("durable Stop result = %+v, %v", stored, err)
	}

	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if closed := runtimeStop(handler, t.Context(), "closed-stop", 41); closed.Code != http.StatusServiceUnavailable {
		t.Fatalf("Stop after journal shutdown = %d, %s", closed.Code, closed.Body.String())
	}
}

func TestRuntimeRestartEndToEndReplacesOwnedChildExactlyOnce(t *testing.T) {
	directory := t.TempDir()
	values := validConfigValues()
	values["CPAMP_RUNTIME_CPA_ADDR"] = unreadyCPAAddr(t)
	values["CPAMP_RUNTIME_JOURNAL_PATH"] = filepath.Join(directory, "runtime", "operations.sqlite")
	values["CPAMP_CPA_EXECUTABLE"] = copyStartHelper(t, directory)
	values["NORMAL_SENTINEL"] = "test-only-ordinary-value"
	values["CPAMP_DEPLOYMENT_SENTINEL"] = "test-only-deployment-value"
	values["HTTP_PROXY"] = "http://http-proxy.invalid:18080"
	values["HTTPS_PROXY"] = "http://https-proxy.invalid:18443"
	values["NO_PROXY"] = "bypass.invalid"
	for key, value := range values {
		t.Setenv(key, value)
	}
	cfg, err := loadConfig(os.Getenv, fixedGeneration(41))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := newRuntimeHandler(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	exitFile := filepath.Join(directory, "exit")
	t.Cleanup(func() { _ = os.WriteFile(exitFile, nil, 0o600) })

	if started := runtimeStart(handler, t.Context(), "initial-start", 41); started.Code != http.StatusOK {
		t.Fatalf("initial Start = %d, %s", started.Code, started.Body.String())
	}
	awaitSpawnCount(t, directory, 1)

	restarted := runtimeRestart(handler, t.Context(), "restart", 41)
	if restarted.Code != http.StatusOK {
		t.Fatalf("Restart = %d, %s", restarted.Code, restarted.Body.String())
	}
	var operation struct {
		OperationType     string        `json:"operationType"`
		RuntimeGeneration uint64        `json:"runtimeGeneration"`
		State             journal.State `json:"state"`
	}
	if err := json.Unmarshal(restarted.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.OperationType != "restart" || operation.RuntimeGeneration != 41 || operation.State != journal.StateSucceeded {
		t.Fatalf("Restart result = %s", restarted.Body.String())
	}
	awaitSpawnCount(t, directory, 2)
	for _, path := range []string{"/v1/runtime/handshake", "/v1/runtime/status"} {
		got := requestRuntime(t, handler, path)
		if got.RuntimeGeneration != 41 || !reflect.DeepEqual(got.Capabilities, expectedRuntimeCapabilities()) ||
			got.CPAObservedVersion != "" || (path == "/v1/runtime/status" && got.State != "starting") {
			t.Fatalf("Restart changed authority or invented readiness: %+v", got)
		}
	}
	if replay := runtimeRestart(handler, t.Context(), "restart", 41); replay.Code != http.StatusOK || replay.Body.String() != restarted.Body.String() {
		t.Fatalf("same-ID Restart replay = %d, %s", replay.Code, replay.Body.String())
	}
	awaitSpawnCount(t, directory, 2)
	if proof := runtimeStart(handler, t.Context(), "replacement-still-running", 41); proof.Code != http.StatusConflict ||
		!strings.Contains(proof.Body.String(), "operation_state_conflict") {
		t.Fatalf("Restart replay affected replacement child = %d, %s", proof.Code, proof.Body.String())
	}
	if stopped := runtimeStop(handler, t.Context(), "cleanup-stop", 41); stopped.Code != http.StatusOK {
		t.Fatalf("Stop replacement = %d, %s", stopped.Code, stopped.Body.String())
	}
	if absent := runtimeRestart(handler, t.Context(), "restart-without-child", 41); absent.Code != http.StatusConflict ||
		!strings.Contains(absent.Body.String(), "operation_state_conflict") {
		t.Fatalf("Restart without running child = %d, %s", absent.Code, absent.Body.String())
	}

	reader, err := journal.Open(t.Context(), cfg.journalPath, journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	stored, err := reader.Get(t.Context(), cfg.runtimeIdentity, "restart")
	if err != nil || stored.State != journal.StateSucceeded || stored.OperationType != "restart" || stored.RuntimeGeneration != 41 {
		t.Fatalf("durable Restart result = %+v, %v", stored, err)
	}
	for _, id := range []string{"replacement-still-running", "restart-without-child"} {
		if _, err := reader.Get(t.Context(), cfg.runtimeIdentity, id); !errors.Is(err, journal.ErrOperationNotFound) {
			t.Fatalf("rejected operation %q wrote intent: %v", id, err)
		}
	}
}

func copyStartHelper(t *testing.T, directory string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	name := filepath.Join(directory, startHelperName)
	destination, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(destination, source)
	if err := errors.Join(copyErr, destination.Close()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(directory, "exit"), nil, 0o600)
		// Windows retains an executable file until the child actually exits.
		for deadline := time.Now().Add(10 * time.Second); ; {
			err := os.Remove(name)
			if err == nil || os.IsNotExist(err) {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("remove helper executable: %v", err)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	return name
}

func startBody(id string, generation uint64) string {
	return fmt.Sprintf(`{"operationId":%q,"expectedRuntimeIdentity":"runtime-01","expectedRuntimeGeneration":%d}`, id, generation)
}

func runtimeStart(handler http.Handler, ctx context.Context, id string, generation uint64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/runtime/operations/start", strings.NewReader(startBody(id, generation))).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer runtime-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func runtimeStop(handler http.Handler, ctx context.Context, id string, generation uint64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/runtime/operations/stop", strings.NewReader(startBody(id, generation))).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer runtime-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func runtimeRestart(handler http.Handler, ctx context.Context, id string, generation uint64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/runtime/operations/restart", strings.NewReader(startBody(id, generation))).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer runtime-token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func awaitSpawnCount(t *testing.T, directory string, count int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		data, err := os.ReadFile(filepath.Join(directory, "spawns"))
		if err == nil {
			got := len(strings.Fields(string(data)))
			if got > count {
				t.Fatalf("spawn count = %d, want %d", got, count)
			}
			if got == count {
				names, err := os.ReadFile(filepath.Join(directory, "environment-names"))
				if err != nil {
					t.Fatal(err)
				}
				inherited := make(map[string]bool)
				for _, name := range strings.Fields(string(names)) {
					if runtime.GOOS == "windows" {
						name = strings.ToUpper(name)
					}
					inherited[name] = true
					if strings.HasPrefix(name, "CPAMP_RUNTIME_") ||
						name == "CPAMP_CPA_EXECUTABLE" ||
						name == "CPAMP_CPA_ARTIFACT_MANIFEST" {
						t.Errorf("typed Start passed private environment variable %s to CPA", name)
					}
				}
				for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "NORMAL_SENTINEL", "CPAMP_DEPLOYMENT_SENTINEL"} {
					if !inherited[name] {
						t.Errorf("typed Start dropped ordinary environment variable %s", name)
					}
				}
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("did not observe %d child spawns", count)
}
