//go:build unix

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	testSlotKeyID          = "cpamp-test-key-2026"
	testSlotV8ManifestJSON = `{"schemaVersion":1,"engine":"cpa","version":"8.0.55","artifactId":"sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"}` + "\n"
)

var (
	testSlotSeed   = bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	testSlotPriv   = ed25519.NewKeyFromSeed(testSlotSeed)
	testSlotPub    = testSlotPriv.Public().(ed25519.PublicKey)
	testSlotPubHex = hex.EncodeToString(testSlotPub)
)

func init() {
	ExtraTrustedSlotPublicKey = testSlotKeyID + ":" + testSlotPubHex
}

type slotSubprocessBehavior struct {
	CrashManager                  bool `json:"crash_manager,omitempty"`
	HangManager                   bool `json:"hang_manager,omitempty"`
	DiscoveryOnly503              bool `json:"discovery_only_503,omitempty"`
	CrashManagerDuringSoakAfterMS int  `json:"crash_manager_during_soak_after_ms,omitempty"`
	CrashManagerExitZero          bool `json:"crash_manager_exit_zero,omitempty"`
	FailUpgradeWorkerCheck        bool `json:"fail_upgrade_worker_check,omitempty"`
	FailUpgradeWorkerDaemon       bool `json:"fail_upgrade_worker_daemon,omitempty"`
}

// maybeRunSlotSubprocessFromTestMain intercepts invocations when the compiled test binary
// is placed inside a simulated Native/Slot bin/ directory and executed as cpamp-launcher,
// cpamp-runtime-supervisor, cpa-manager-plus, or cpamp-ingress.
func maybeRunSlotSubprocessFromTestMain() bool {
	if len(os.Args) == 0 {
		return false
	}
	// Avoid intercepting normal `go test` invocations whose binary ends with `.test`.
	baseName := filepath.Base(os.Args[0])
	switch baseName {
	case "cpamp-launcher":
		var trialTimeout time.Duration
		if raw := strings.TrimSpace(os.Getenv("CPAMP_TEST_TRIAL_TIMEOUT_MS")); raw != "" {
			if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
				trialTimeout = time.Duration(ms) * time.Millisecond
			}
		}
		var trialSoak time.Duration
		if raw := strings.TrimSpace(os.Getenv("CPAMP_TEST_TRIAL_STABILIZATION_MS")); raw != "" {
			if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
				trialSoak = time.Duration(ms) * time.Millisecond
			}
		}
		err := runLauncherEntrypoint(os.Args, os.Environ(), launcherRuntimeHooks{
			allowNonLinux:            true,
			allowNonRoot:             true,
			trialTimeout:             trialTimeout,
			trialStabilizationWindow: trialSoak,
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
		return true

	case "cpamp-runtime-supervisor", "cpa-manager-plus-runtime-supervisor":
		runSimulatedSupervisorProcess()
		return true

	case "cpa-manager-plus":
		runSimulatedManagerProcess()
		return true

	case "cpamp-ingress", "cpa-manager-plus-ingress":
		runSimulatedIngressProcess()
		return true
	}
	return false
}

func runSimulatedSupervisorProcess() {
	addr := strings.TrimSpace(os.Getenv("CPAMP_RUNTIME_ADDR"))
	if addr == "" {
		addr = DefaultRuntimeAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "simulated supervisor listen %s: %v\n", addr, err)
		os.Exit(1)
	}
	defer func() { _ = ln.Close() }()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = srv.Shutdown(ctx)
	cancel()
	os.Exit(0)
}

func runSimulatedIngressProcess() {
	pubAddr := strings.TrimSpace(os.Getenv("CPAMP_INGRESS_ADDR"))
	if pubAddr == "" {
		pubAddr = DefaultIngressPublicAddr
	}
	ctrlAddr := strings.TrimSpace(os.Getenv("CPAMP_INGRESS_CONTROL_ADDR"))
	if ctrlAddr == "" {
		ctrlAddr = DefaultIngressAdminAddr
	}

	pubLn, err := net.Listen("tcp", pubAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "simulated ingress public listen %s: %v\n", pubAddr, err)
		os.Exit(1)
	}
	defer func() { _ = pubLn.Close() }()

	ctrlLn, err := net.Listen("tcp", ctrlAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "simulated ingress control listen %s: %v\n", ctrlAddr, err)
		os.Exit(1)
	}
	defer func() { _ = ctrlLn.Close() }()

	pubSrv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ingress":true}`))
		}),
	}
	ctrlMux := http.NewServeMux()
	ctrlMux.HandleFunc("/v1/drain", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"draining":true}`))
	})
	ctrlSrv := &http.Server{Handler: ctrlMux}

	go func() { _ = pubSrv.Serve(pubLn) }()
	go func() { _ = ctrlSrv.Serve(ctrlLn) }()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = pubSrv.Shutdown(ctx)
	_ = ctrlSrv.Shutdown(ctx)
	cancel()
	os.Exit(0)
}

func runSimulatedManagerProcess() {
	args := os.Args[1:]
	selfExe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(selfExe); err == nil {
		selfExe = resolved
	}
	var beh slotSubprocessBehavior
	behaviorPath := filepath.Join(filepath.Dir(filepath.Dir(selfExe)), "share", "cpamp", "test-behavior.json")
	if raw, err := os.ReadFile(behaviorPath); err == nil {
		_ = json.Unmarshal(raw, &beh)
	}

	if len(args) >= 2 && args[0] == "upgrade" && args[1] == "worker" {
		if len(args) >= 3 && args[2] == "--check" {
			if beh.FailUpgradeWorkerCheck {
				fmt.Fprintln(os.Stderr, "simulated upgrade worker check failure")
				os.Exit(1)
			}
			if beh.FailUpgradeWorkerDaemon {
				fmt.Fprintln(os.Stdout, `{"required":true}`)
				os.Exit(0)
			}
			fmt.Fprintln(os.Stdout, `{"required":false}`)
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "simulated upgrade worker daemon startup failure")
		os.Exit(1)
	}
	if len(args) >= 2 && args[0] == "manager-data-snapshot" && args[1] == "restore" {
		if err := executeSimulatedSnapshotRestore(args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, "restored snapshot successfully")
		os.Exit(0)
	}

	if beh.CrashManager {
		fmt.Fprintln(os.Stderr, "simulated manager startup failure")
		os.Exit(1)
	}
	if beh.HangManager {
		sigCh := make(chan os.Signal, 2)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh
		os.Exit(0)
	}

	var trialHoldActive atomic.Bool
	var trialHoldStartedTrue bool
	rawHoldEnv := os.Getenv("CPAMP_TRIAL_HOLD")
	if rawHoldEnv == "1" {
		trialHoldActive.Store(true)
		trialHoldStartedTrue = true
	}
	if holdLogPath := strings.TrimSpace(os.Getenv("CPAMP_TEST_MGR_HOLD_LOG_FILE")); holdLogPath != "" {
		if lf, lErr := os.OpenFile(holdLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); lErr == nil {
			_, _ = fmt.Fprintf(lf, "pid=%d hold=%s\n", os.Getpid(), rawHoldEnv)
			_ = lf.Close()
		}
	}
	var releaseCount atomic.Int64
	httpAddr := strings.TrimSpace(os.Getenv("HTTP_ADDR"))
	if httpAddr == "" {
		httpAddr = DefaultManagerAddr
	}
	ln, err := net.Listen("tcp", httpAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "simulated manager listen %s: %v\n", httpAddr, err)
		os.Exit(1)
	}
	defer func() { _ = ln.Close() }()

	var soakCrashOnce sync.Once
	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux}
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if beh.DiscoveryOnly503 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "mode": "discovery_only"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":                      true,
			"service":                 "cpa-manager-plus",
			"manager_pid":             os.Getpid(),
			"launcher_pid":            os.Getppid(),
			"exe":                     selfExe,
			"base_image_version":      os.Getenv("CPAMP_BASE_VERSION"),
			"base_install_root":       os.Getenv("CPAMP_BASE_INSTALL_ROOT"),
			"active_slot":             os.Getenv("CPAMP_RUNNING_SLOT"),
			"launcher_control_url":    os.Getenv("CPAMP_LAUNCHER_CONTROL_URL"),
			"launcher_token_file":     os.Getenv("CPAMP_LAUNCHER_TOKEN_FILE"),
			"trial_hold":              trialHoldActive.Load(),
			"trial_hold_started_true": trialHoldStartedTrue,
			"trial_release_calls":     releaseCount.Load(),
		})
		if beh.CrashManagerDuringSoakAfterMS > 0 {
			soakCrashOnce.Do(func() {
				go func() {
					time.Sleep(time.Duration(beh.CrashManagerDuringSoakAfterMS) * time.Millisecond)
					if srv != nil {
						_ = srv.Close()
					}
					_ = ln.Close()
					if beh.CrashManagerExitZero {
						os.Exit(0)
					}
					os.Exit(1)
				}()
			})
		}
	})
	mux.HandleFunc("/.well-known/cpamp-bootstrap/ui", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>bootstrap</html>"))
	})
	mux.HandleFunc("/.well-known/cpamp-bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ready":true}`))
	})
	mux.HandleFunc("/internal/v1/trial/release", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		tokFile := os.Getenv("CPAMP_LAUNCHER_TOKEN_FILE")
		rawTok, err := os.ReadFile(tokFile)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		wantAuth := "Bearer " + strings.TrimSpace(string(rawTok))
		if r.Header.Get("Authorization") != wantAuth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		trialHoldActive.Store(false)
		releaseCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"released":true}`))
	})
	mux.HandleFunc("/__test/spawn-sleeper-and-exit-zero", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		sleepCmd := exec.Command("sleep", "10")
		sleepCmd.Stdout = os.Stdout
		sleepCmd.Stderr = os.Stderr
		if err := sleepCmd.Start(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		sleeperPID := sleepCmd.Process.Pid
		_ = sleepCmd.Process.Release()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"sleeper_pid": sleeperPID})
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		go func() {
			time.Sleep(5 * time.Millisecond)
			_ = ln.Close()
			os.Exit(0)
		}()
	})

	go func() { _ = srv.Serve(ln) }()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	_ = srv.Shutdown(ctx)
	cancel()
	os.Exit(0)
}

func executeSimulatedSnapshotRestore(args []string) error {
	var dbPath, dataKeyPath, snapshotDir string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--db-path":
			if i+1 < len(args) {
				i++
				dbPath = args[i]
			}
		case "--data-key-path":
			if i+1 < len(args) {
				i++
				dataKeyPath = args[i]
			}
		case "--snapshot-dir":
			if i+1 < len(args) {
				i++
				snapshotDir = args[i]
			}
		}
	}
	if dbPath == "" || dataKeyPath == "" || snapshotDir == "" {
		return fmt.Errorf("missing manager-data-snapshot restore arguments")
	}
	backupsParent := filepath.Dir(snapshotDir)
	if _, err := os.Lstat(filepath.Join(backupsParent, "fail-restore.always")); err == nil {
		return fmt.Errorf("simulated persistent snapshot restore failure")
	}
	failOnceFile := filepath.Join(backupsParent, "fail-restore.once")
	if _, err := os.Lstat(failOnceFile); err == nil {
		_ = os.Remove(failOnceFile)
		return fmt.Errorf("simulated transient snapshot restore failure")
	}
	dbSnap, err := os.ReadFile(filepath.Join(snapshotDir, "database"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(dbPath, dbSnap, 0o600); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		_ = os.Remove(dbPath + suffix)
	}
	keySnap, err := os.ReadFile(filepath.Join(snapshotDir, "data-key"))
	if err == nil {
		if err := os.WriteFile(dataKeyPath, keySnap, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// buildSignedSlotArchive builds a deterministic signed .tar.gz slot archive in memory.
type slotArchiveBuildOptions struct {
	Version                string
	SourceCommit           string
	Platform               string
	MinBaseEpoch           int
	SchemaGeneration       int
	KeyID                  string
	PrivKey                ed25519.PrivateKey
	Files                  map[string][]byte
	OmitDefaultFiles       []string
	ExtraTarCallback       func(tw *tar.Writer) error
	TamperManifest         func(raw []byte) []byte
	TamperFileAfter        map[string][]byte
	TrailingTarZeroPadding int
}

func buildSignedSlotArchive(t *testing.T, opts slotArchiveBuildOptions) ([]byte, string) {
	t.Helper()
	if opts.Version == "" {
		opts.Version = "v2.0.0-beta.2"
	}
	if opts.SourceCommit == "" {
		opts.SourceCommit = strings.Repeat("a", 40)
	}
	if opts.Platform == "" {
		opts.Platform = ExpectedSlotPlatform()
	}
	if opts.MinBaseEpoch == 0 {
		opts.MinBaseEpoch = 1
	}
	if opts.SchemaGeneration == 0 {
		opts.SchemaGeneration = 1
	}
	if opts.KeyID == "" {
		opts.KeyID = testSlotKeyID
	}
	if opts.PrivKey == nil {
		opts.PrivKey = testSlotPriv
	}

	files := make(map[string][]byte)
	for _, reqBin := range requiredSlotBinaries {
		files[reqBin] = []byte("#!/bin/sh\nexit 0\n")
	}
	files["share/cpamp/release/base-abi.json"] = []byte(fmt.Sprintf(`{"schema_version":1,"base_epoch":%d,"platform":%q}`+"\n", opts.MinBaseEpoch, opts.Platform))
	files["share/cpamp/runtime/config.seed.yaml"] = []byte(testSeedYAML)
	files["share/cpamp/runtime/seed-lineage.json"] = []byte(`{"acceptedSeedIdentities":[]}` + "\n")
	files["share/cpamp/runtime/cpa-artifact.json"] = []byte(testSlotV8ManifestJSON)
	for k, v := range opts.Files {
		files[k] = v
	}
	for _, omit := range opts.OmitDefaultFiles {
		delete(files, omit)
	}
	manifestEntries := make(map[string]SlotManifestEntry, len(files))
	for rel, content := range files {
		sum := sha256.Sum256(content)
		mode := "0444"
		if strings.HasPrefix(rel, "bin/") {
			mode = "0555"
		}
		manifestEntries[rel] = SlotManifestEntry{
			Size:   int64(len(content)),
			SHA256: "sha256:" + hex.EncodeToString(sum[:]),
			Mode:   mode,
		}
	}

	manifest := SlotManifest{
		SchemaVersion:    1,
		Version:          opts.Version,
		SourceCommit:     opts.SourceCommit,
		Platform:         opts.Platform,
		MinBaseEpoch:     opts.MinBaseEpoch,
		SchemaGeneration: opts.SchemaGeneration,
		KeyID:            opts.KeyID,
		Files:            manifestEntries,
	}
	rawManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	rawManifest = append(rawManifest, '\n')

	msg := append([]byte(SlotManifestDomainPrefix), rawManifest...)
	sigBytes := ed25519.Sign(opts.PrivKey, msg)
	sigDoc := SlotSignature{
		SchemaVersion: 1,
		Algorithm:     "ed25519",
		KeyID:         opts.KeyID,
		Signature:     base64.StdEncoding.EncodeToString(sigBytes),
	}
	rawSig, err := json.MarshalIndent(sigDoc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	rawSig = append(rawSig, '\n')

	if opts.TamperManifest != nil {
		rawManifest = opts.TamperManifest(rawManifest)
	}
	for k, tampered := range opts.TamperFileAfter {
		files[k] = tampered
	}

	var archiveBuf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&archiveBuf, gzip.BestSpeed)
	tw := tar.NewWriter(gz)

	writeReg := func(name string, content []byte, mode int64) {
		hdr := &tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Mode:     mode,
			Size:     int64(len(content)),
			ModTime:  time.Unix(1700000000, 0).UTC(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}

	writeReg(SlotManifestFileName, rawManifest, 0o444)
	writeReg(SlotSignatureFileName, rawSig, 0o444)

	sortedKeys := make([]string, 0, len(files))
	for k := range files {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	emittedDirs := make(map[string]bool)
	for _, rel := range sortedKeys {
		parts := strings.Split(path.Dir(rel), "/")
		var cur string
		for _, p := range parts {
			if p == "" || p == "." {
				continue
			}
			if cur == "" {
				cur = p
			} else {
				cur = cur + "/" + p
			}
			if !emittedDirs[cur] {
				emittedDirs[cur] = true
				if err := tw.WriteHeader(&tar.Header{
					Name:     cur + "/",
					Typeflag: tar.TypeDir,
					Mode:     0o555,
					ModTime:  time.Unix(1700000000, 0).UTC(),
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
		mode := int64(0o444)
		if strings.HasPrefix(rel, "bin/") {
			mode = 0o555
		}
		writeReg(rel, files[rel], mode)
	}

	if opts.ExtraTarCallback != nil {
		if err := opts.ExtraTarCallback(tw); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if opts.TrailingTarZeroPadding > 0 {
		if _, err := gz.Write(make([]byte, opts.TrailingTarZeroPadding)); err != nil {
			t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	archiveBytes := archiveBuf.Bytes()
	outerSum := sha256.Sum256(archiveBytes)
	return archiveBytes, "sha256:" + hex.EncodeToString(outerSum[:])
}

func registerSlotTempCleanup(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = removeSlotTreeSafe(root)
	})
}

func TestSemVerV2ComparisonAndCanonicalSlotValidation(t *testing.T) {
	ring, err := BuildTrustedSlotKeyring()
	if err != nil {
		t.Fatalf("BuildTrustedSlotKeyring failed: %v", err)
	}
	if len(ring[DefaultTrustedSlotKeyID]) != ed25519.PublicKeySize {
		t.Fatalf("missing default embedded key %s", DefaultTrustedSlotKeyID)
	}
	if len(ring[testSlotKeyID]) != ed25519.PublicKeySize {
		t.Fatalf("missing link-time test key %s", testSlotKeyID)
	}

	cmp, err := CompareSemVerV2("2.0.0-beta.2", "v2.0.0-beta.2")
	if err != nil || cmp != 0 {
		t.Fatalf("expected 2.0.0-beta.2 == v2.0.0-beta.2, got cmp=%d err=%v", cmp, err)
	}

	ordered := []string{
		"v2.0.0-alpha.1",
		"v2.0.0-beta.1",
		"v2.0.0-beta.2",
		"v2.0.0-beta.10",
		"v2.0.0-rc.1",
		"v2.0.0",
		"v2.0.1",
		"v2.1.0",
	}
	for i := range len(ordered) - 1 {
		c, err := CompareSemVerV2(ordered[i], ordered[i+1])
		if err != nil || c >= 0 {
			t.Fatalf("expected %s < %s, got cmp=%d err=%v", ordered[i], ordered[i+1], c, err)
		}
	}

	for _, invalidSlot := range []string{
		"",
		"2.0.0-beta.2",
		"v1.17.0",
		"v3.0.0",
		"v2.0.0-beta.01",
		"v2.0.0+build.1",
		"v2.0.0/../evil",
	} {
		if err := ValidateCanonicalSlotVersion(invalidSlot); err == nil {
			t.Fatalf("expected ValidateCanonicalSlotVersion(%q) to fail", invalidSlot)
		}
	}
	if err := ValidateCanonicalSlotVersion("v2.0.0-beta.2"); err != nil {
		t.Fatalf("valid canonical slot version rejected: %v", err)
	}
}

func TestSlotManifestEd25519VerificationAndArchiveGuards(t *testing.T) {
	base := t.TempDir()
	registerSlotTempCleanup(t, base)

	cpampRoot := filepath.Join(base, "cpamp")
	slotsDir := filepath.Join(cpampRoot, "slots")
	statePath := filepath.Join(cpampRoot, "slot-state.json")
	store := NewSlotStoreWithOwner(cpampRoot, slotsDir, statePath, 1, os.Geteuid(), false)

	// 1. Valid archive stages cleanly and passes VerifyInstalledSlot.
	validArchive, validSHA := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version: "v2.0.0-beta.2",
	})
	manifest, written, err := store.StageSlotArchive(bytes.NewReader(validArchive), "v2.0.0-beta.2", validSHA, int64(len(validArchive)), nil)
	if err != nil {
		t.Fatalf("StageSlotArchive valid failed: %v", err)
	}
	if written != int64(len(validArchive)) || manifest.Version != "v2.0.0-beta.2" {
		t.Fatalf("unexpected manifest/written: %+v %d", manifest, written)
	}
	if _, err := store.VerifyInstalledSlot("v2.0.0-beta.2"); err != nil {
		t.Fatalf("VerifyInstalledSlot failed on freshly staged slot: %v", err)
	}

	// Verify file permissions inside staged slot.
	binSt, err := os.Lstat(filepath.Join(slotsDir, "v2.0.0-beta.2", "bin", "cpamp-launcher"))
	if err != nil || binSt.Mode().Perm() != 0o555 {
		t.Fatalf("expected 0555 on slot bin/cpamp-launcher, got err=%v mode=%#o", err, binSt.Mode().Perm())
	}
	abiSt, err := os.Lstat(filepath.Join(slotsDir, "v2.0.0-beta.2", "share", "cpamp", "release", "base-abi.json"))
	if err != nil || abiSt.Mode().Perm() != 0o444 {
		t.Fatalf("expected 0444 on slot share/cpamp/release/base-abi.json, got err=%v mode=%#o", err, abiSt.Mode().Perm())
	}
	if _, err := os.Lstat(filepath.Join(slotsDir, "v2.0.0-beta.2", "share", "cpamp", "provenance.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected provenance.json excluded from slot archive, got err=%v", err)
	}
	for idx, reqShare := range requiredSlotShareFiles {
		missingVer := fmt.Sprintf("v2.0.0-beta.%d", 40+idx)
		missingArchive, missingSHA := buildSignedSlotArchive(t, slotArchiveBuildOptions{
			Version:          missingVer,
			OmitDefaultFiles: []string{reqShare},
		})
		_, _, stageErr := store.StageSlotArchive(bytes.NewReader(missingArchive), missingVer, missingSHA, int64(len(missingArchive)), nil)
		if slotErrorCode(stageErr, "") != SlotCodeArchiveInvalid {
			t.Fatalf("expected archive_invalid when missing %s, got %v", reqShare, stageErr)
		}
	}
	// 2. Non-root owner check enforced when NewSlotStore expects RootUID (0).
	if os.Geteuid() != RootUID {
		rootStore := NewSlotStore(cpampRoot, slotsDir, statePath, 1)
		if _, err := rootStore.VerifyInstalledSlot("v2.0.0-beta.2"); err == nil {
			t.Fatalf("expected root-enforcing SlotStore to reject non-root owned slot directory")
		}
	}

	// 3. Tampered binary inside archive is rejected.
	tamperedBinArchive, tamperedBinSHA := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version: "v2.0.0-beta.3",
		TamperFileAfter: map[string][]byte{
			"bin/cpa-manager-plus": []byte("#!/bin/sh\necho modified\n"),
		},
	})
	_, _, err = store.StageSlotArchive(bytes.NewReader(tamperedBinArchive), "v2.0.0-beta.3", tamperedBinSHA, int64(len(tamperedBinArchive)), nil)
	if slotErrorCode(err, "") != SlotCodeArchiveInvalid {
		t.Fatalf("expected archive_invalid on tampered binary, got %v", err)
	}

	// 4. Tampered manifest after signing is rejected with signature_invalid.
	tamperedManArchive, tamperedManSHA := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version: "v2.0.0-beta.3",
		TamperManifest: func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"schema_generation": 1`), []byte(`"schema_generation": 2`), 1)
		},
	})
	_, _, err = store.StageSlotArchive(bytes.NewReader(tamperedManArchive), "v2.0.0-beta.3", tamperedManSHA, int64(len(tamperedManArchive)), nil)
	if slotErrorCode(err, "") != SlotCodeSignatureInvalid {
		t.Fatalf("expected signature_invalid on tampered manifest, got %v", err)
	}

	// 5. Untrusted key_id is rejected with signature_invalid.
	unknownKeyArchive, unknownKeySHA := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version: "v2.0.0-beta.3",
		KeyID:   "untrusted-key-2099",
	})
	_, _, err = store.StageSlotArchive(bytes.NewReader(unknownKeyArchive), "v2.0.0-beta.3", unknownKeySHA, int64(len(unknownKeyArchive)), nil)
	if slotErrorCode(err, "") != SlotCodeSignatureInvalid {
		t.Fatalf("expected signature_invalid on untrusted key_id, got %v", err)
	}

	// 6. min_base_epoch > host base_epoch is rejected with base_epoch_incompatible.
	futureEpochArchive, futureEpochSHA := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version:      "v2.0.0-beta.3",
		MinBaseEpoch: 2,
	})
	_, _, err = store.StageSlotArchive(bytes.NewReader(futureEpochArchive), "v2.0.0-beta.3", futureEpochSHA, int64(len(futureEpochArchive)), nil)
	if slotErrorCode(err, "") != SlotCodeBaseEpochIncompatible {
		t.Fatalf("expected base_epoch_incompatible when min_base_epoch=2 > base_epoch=1, got %v", err)
	}

	// 7. Symlink entry in tar archive is rejected.
	symlinkArchive, symlinkSHA := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version: "v2.0.0-beta.3",
		ExtraTarCallback: func(tw *tar.Writer) error {
			return tw.WriteHeader(&tar.Header{
				Name:     "share/cpamp/link",
				Typeflag: tar.TypeSymlink,
				Linkname: "/etc/passwd",
				Mode:     0o777,
			})
		},
	})
	_, _, err = store.StageSlotArchive(bytes.NewReader(symlinkArchive), "v2.0.0-beta.3", symlinkSHA, int64(len(symlinkArchive)), nil)
	if slotErrorCode(err, "") != SlotCodeArchiveInvalid {
		t.Fatalf("expected archive_invalid on symlink tar entry, got %v", err)
	}

	// 8. Path traversal "../" entry in tar archive is rejected.
	traversalArchive, traversalSHA := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version: "v2.0.0-beta.3",
		ExtraTarCallback: func(tw *tar.Writer) error {
			data := []byte("escape")
			if err := tw.WriteHeader(&tar.Header{
				Name:     "share/../escape.txt",
				Typeflag: tar.TypeReg,
				Mode:     0o444,
				Size:     int64(len(data)),
			}); err != nil {
				return err
			}
			_, err := tw.Write(data)
			return err
		},
	})
	_, _, err = store.StageSlotArchive(bytes.NewReader(traversalArchive), "v2.0.0-beta.3", traversalSHA, int64(len(traversalArchive)), nil)
	if slotErrorCode(err, "") != SlotCodeArchiveInvalid {
		t.Fatalf("expected archive_invalid on path traversal tar entry, got %v", err)
	}

	// 9. Unlisted extra file inside archive (even under share/) is rejected.
	extraFileArchive, extraFileSHA := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version: "v2.0.0-beta.3",
		ExtraTarCallback: func(tw *tar.Writer) error {
			data := []byte("unlisted")
			if err := tw.WriteHeader(&tar.Header{
				Name:     "share/cpamp/unlisted.json",
				Typeflag: tar.TypeReg,
				Mode:     0o444,
				Size:     int64(len(data)),
			}); err != nil {
				return err
			}
			_, err := tw.Write(data)
			return err
		},
	})
	_, _, err = store.StageSlotArchive(bytes.NewReader(extraFileArchive), "v2.0.0-beta.3", extraFileSHA, int64(len(extraFileArchive)), nil)
	if slotErrorCode(err, "") != SlotCodeArchiveInvalid {
		t.Fatalf("expected archive_invalid on unlisted extra tar file, got %v", err)
	}

	// 10. Post-staging unlisted file or modified binary inside installed slot fails VerifyInstalledSlot.
	slotShareDir := filepath.Join(slotsDir, "v2.0.0-beta.2", "share", "cpamp")
	_ = os.Chmod(slotShareDir, 0o755)
	extraOnDisk := filepath.Join(slotShareDir, "stray.txt")
	if err := os.WriteFile(extraOnDisk, []byte("stray"), 0o444); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(slotShareDir, 0o555)
	if _, err := store.VerifyInstalledSlot("v2.0.0-beta.2"); slotErrorCode(err, "") != SlotCodeArchiveInvalid {
		t.Fatalf("expected VerifyInstalledSlot to reject stray file in installed slot, got %v", err)
	}
}

func TestPrepareInitializesCPAMPSlotDirsAndChildEnvIsolation(t *testing.T) {
	sys := newFakeSystem(t)
	prepared, err := Prepare(PrepareConfig{
		BaseVersion:         "v2.0.0-beta.1",
		RunningSlot:         "v2.0.0-beta.2",
		TrialHold:           true,
		LauncherControlAddr: "127.0.0.1:9083",
	}, sys)
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	sys.assertNode(t, "/data/runtime/cpamp", 0, 0, 0o0755, true)
	sys.assertNode(t, "/data/runtime/cpamp/slots", 0, 0, 0o0755, true)
	sys.assertNode(t, "/data/runtime-secret/launcher-token", 0, 10002, 0o0640, false)
	if len(prepared.LauncherToken) != 64 {
		t.Fatalf("expected 64-hex launcher token, got %q", prepared.LauncherToken)
	}

	containerEnv := map[string]string{
		"PATH":                       "/usr/local/bin:/usr/bin:/bin",
		"CPAMP_LAUNCHER_CONTROL_URL": "http://127.0.0.1:19999",
		"CPAMP_LAUNCHER_TOKEN_FILE":  "/tmp/evil",
		"CPAMP_LAUNCHER_TOKEN":       "should-not-leak",
		"CPAMP_TRIAL_HOLD":           "1",
		"CPAMP_ACTIVE_SLOT":          "v2.0.0-beta.2",
		"CPAMP_BASE_INSTALL_ROOT":    "/",
	}

	supEnv := parseEnvironMap(BuildSupervisorChildEnv(prepared, containerEnv))
	ingEnv := parseEnvironMap(BuildIngressChildEnv(90*time.Second, containerEnv))
	mgrEnv := parseEnvironMap(BuildManagerChildEnv(prepared, containerEnv))

	for role, env := range map[string]map[string]string{"supervisor": supEnv, "ingress": ingEnv} {
		for k := range env {
			if strings.HasPrefix(k, "CPAMP_LAUNCHER_") ||
				k == "CPAMP_TRIAL_HOLD" ||
				k == "CPAMP_RUNNING_SLOT" ||
				k == "CPAMP_BASE_VERSION" ||
				k == "CPAMP_BASE_INSTALL_ROOT" {
				t.Fatalf("%s must never receive %s (got %q)", role, k, env[k])
			}
		}
	}

	if mgrEnv["CPAMP_LAUNCHER_CONTROL_URL"] != "http://127.0.0.1:9083" ||
		mgrEnv["CPAMP_LAUNCHER_TOKEN_FILE"] != "/data/runtime-secret/launcher-token" ||
		mgrEnv["CPAMP_BASE_VERSION"] != "v2.0.0-beta.1" ||
		mgrEnv["CPAMP_BASE_INSTALL_ROOT"] != "/" ||
		mgrEnv["CPAMP_RUNNING_SLOT"] != "v2.0.0-beta.2" ||
		mgrEnv["CPAMP_TRIAL_HOLD"] != "1" {
		t.Fatalf("unexpected manager slot env: %#v", mgrEnv)
	}
	if _, leaked := mgrEnv["CPAMP_LAUNCHER_TOKEN"]; leaked {
		t.Fatalf("manager must not receive raw CPAMP_LAUNCHER_TOKEN")
	}
	prepared.TrialHold = false
	mgrCommittedEnv := parseEnvironMap(BuildManagerChildEnv(prepared, containerEnv))
	if _, hasHold := mgrCommittedEnv["CPAMP_TRIAL_HOLD"]; hasHold {
		t.Fatalf("CPAMP_TRIAL_HOLD must be stripped when TrialHold is false")
	}
}

func TestColdStartArbitrationBaseSupersedesSlotAndPrunes(t *testing.T) {
	base := t.TempDir()
	registerSlotTempCleanup(t, base)

	installRoot := filepath.Join(base, "install")
	if err := os.MkdirAll(filepath.Join(installRoot, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(installRoot, "share", "cpamp", "release"), 0o755); err != nil {
		t.Fatal(err)
	}
	baseLauncher := filepath.Join(installRoot, "bin", "cpamp-launcher")
	if err := os.WriteFile(baseLauncher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Base provenance uses unprefixed "2.0.0-beta.2" to verify normalized comparison against "v2.0.0-beta.2".
	if err := os.WriteFile(filepath.Join(installRoot, "share", "cpamp", "provenance.json"), []byte(`{"version":"2.0.0-beta.2"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(installRoot, "share", "cpamp", "release", "base-abi.json"),
		[]byte(fmt.Sprintf(`{"schema_version":1,"base_epoch":1,"platform":%q}`, ExpectedSlotPlatform())),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	cpampRoot := filepath.Join(base, "runtime", "cpamp")
	slotsDir := filepath.Join(cpampRoot, "slots")
	statePath := filepath.Join(cpampRoot, "slot-state.json")
	store := NewSlotStoreWithOwner(cpampRoot, slotsDir, statePath, 1, os.Geteuid(), false)

	for _, ver := range []string{"v2.0.0-beta.1", "v2.0.0-beta.2", "v2.0.0-beta.3"} {
		arch, sha := buildSignedSlotArchive(t, slotArchiveBuildOptions{Version: ver})
		if _, _, err := store.StageSlotArchive(bytes.NewReader(arch), ver, sha, int64(len(arch)), nil); err != nil {
			t.Fatalf("stage %s: %v", ver, err)
		}
	}
	if err := store.WriteSlotState(SlotState{
		SchemaVersion:       1,
		State:               SlotStateCommitted,
		ActiveSlot:          "v2.0.0-beta.2",
		BaseVersionAtSwitch: "v2.0.0-beta.1",
	}); err != nil {
		t.Fatal(err)
	}

	baseInfo, err := ResolveBaseInstallation(
		LauncherCLIOptions{Mode: LauncherModeNative, HomeDir: installRoot},
		os.Geteuid(),
		func(string) string { return "" },
	)
	if err != nil {
		t.Fatalf("ResolveBaseInstallation: %v", err)
	}
	if baseInfo.BaseVersion != "v2.0.0-beta.2" {
		t.Fatalf("expected normalized BaseVersion v2.0.0-beta.2, got %q", baseInfo.BaseVersion)
	}

	var logBuf bytes.Buffer
	plan, err := ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        baseInfo,
		Store:       store,
		DataDir:     filepath.Join(base, "data"),
		SelfExePath: baseLauncher,
		Args:        []string{baseLauncher, "--mode=native", "--home=" + installRoot},
		LogOut:      &logBuf,
	})
	if err != nil {
		t.Fatalf("ArbitrateColdStart failed: %v", err)
	}
	if plan.ActiveSlot != "" || plan.ExecTarget != nil {
		t.Fatalf("expected base adoption without re-exec, got active=%q exec=%+v", plan.ActiveSlot, plan.ExecTarget)
	}
	if !strings.Contains(logBuf.String(), "base version v2.0.0-beta.2 >= slot v2.0.0-beta.2; adopting base installation and pruning superseded slots") {
		t.Fatalf("missing expected log message, got: %s", logBuf.String())
	}

	stateAfter, exists, err := store.ReadSlotStateIfExists()
	if err != nil || !exists || stateAfter.State != SlotStateNone || stateAfter.ActiveSlot != "" {
		t.Fatalf("expected reset slot-state none, got exists=%v state=%+v err=%v", exists, stateAfter, err)
	}

	remainingSlots, err := store.ListStagedSlots()
	if err != nil {
		t.Fatal(err)
	}
	if len(remainingSlots) != 1 || remainingSlots[0] != "v2.0.0-beta.3" {
		t.Fatalf("expected slots <= v2.0.0-beta.2 pruned and v2.0.0-beta.3 kept, got %v", remainingSlots)
	}

	// Now test SlotVersion (v2.0.0-beta.3) > BaseVersion (v2.0.0-beta.2):
	// First call from baseLauncher returns ExecTarget into slotLauncher;
	// second call from slotLauncher with CPAMP_ACTIVE_SLOT=v2.0.0-beta.3 resolves slot paths.
	if err := store.WriteSlotState(SlotState{
		SchemaVersion:       1,
		State:               SlotStateCommitted,
		ActiveSlot:          "v2.0.0-beta.3",
		BaseVersionAtSwitch: "v2.0.0-beta.2",
	}); err != nil {
		t.Fatal(err)
	}

	reexecPlan, err := ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        baseInfo,
		Store:       store,
		DataDir:     filepath.Join(base, "data"),
		SelfExePath: baseLauncher,
		Args:        []string{baseLauncher, "--mode=native", "--home=" + installRoot},
	})
	if err != nil {
		t.Fatalf("ArbitrateColdStart slot > base failed: %v", err)
	}
	wantSlotLauncher := filepath.Join(slotsDir, "v2.0.0-beta.3", "bin", "cpamp-launcher")
	if reexecPlan.ExecTarget == nil || reexecPlan.ExecTarget.Path != wantSlotLauncher {
		t.Fatalf("expected ExecTarget %s, got %+v", wantSlotLauncher, reexecPlan.ExecTarget)
	}

	inSlotPlan, err := ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        baseInfo,
		Store:       store,
		DataDir:     filepath.Join(base, "data"),
		SelfExePath: wantSlotLauncher,
		Args:        reexecPlan.ExecTarget.Args,
		Environ:     reexecPlan.ExecTarget.Env,
	})
	if err != nil {
		t.Fatalf("ArbitrateColdStart inside active slot failed: %v", err)
	}
	if inSlotPlan.ExecTarget != nil {
		t.Fatalf("expected no further re-exec when already in target slot, got %+v", inSlotPlan.ExecTarget)
	}
	wantSlotRoot := filepath.Join(slotsDir, "v2.0.0-beta.3")
	if inSlotPlan.ManagerExecutable != filepath.Join(wantSlotRoot, "bin", "cpa-manager-plus") ||
		inSlotPlan.SeedConfigPath != filepath.Join(wantSlotRoot, "share", "cpamp", "runtime", "config.seed.yaml") ||
		inSlotPlan.CPAExecutable != baseInfo.CPAExecutable {
		t.Fatalf("unexpected active slot path resolution: %+v", inSlotPlan)
	}
}

func TestSlotControlServerEndpointsSSRFAndRollbackGuards(t *testing.T) {
	base := t.TempDir()
	registerSlotTempCleanup(t, base)

	dataDir := filepath.Join(base, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	baseLauncher := filepath.Join(base, "bin", "cpamp-launcher")
	if err := os.MkdirAll(filepath.Dir(baseLauncher), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(baseLauncher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	store := NewSlotStoreWithOwner(
		filepath.Join(dataDir, "runtime", "cpamp"),
		filepath.Join(dataDir, "runtime", "cpamp", "slots"),
		filepath.Join(dataDir, "runtime", "cpamp", "slot-state.json"),
		1,
		os.Geteuid(),
		false,
	)

	// Non-loopback control address must be rejected.
	if _, err := ValidateLoopbackListenAddr("0.0.0.0:9083"); err == nil {
		t.Fatalf("expected non-loopback control address to be rejected")
	}

	archiveBytes, archiveSHA := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version:          "v2.0.0-beta.2",
		SchemaGeneration: 2,
	})
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect-private" {
			http.Redirect(w, r, "https://127.0.0.1/evil.tar.gz", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(archiveBytes)))
		_, _ = w.Write(archiveBytes)
	}))
	defer tlsSrv.Close()

	_, tlsPort, err := net.SplitHostPort(tlsSrv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	tok := strings.Repeat("d", 64)
	baseInfo := BaseInstallationInfo{
		Mode:               LauncherModeDocker,
		InstallRoot:        base,
		BaseVersion:        "v2.0.0-beta.1",
		BaseEpoch:          1,
		Platform:           ExpectedSlotPlatform(),
		LauncherExecutable: baseLauncher,
	}

	// Start first server with default strict public IP policy to verify SSRF refusal on loopback/private targets.
	strictSrv, err := StartSlotControlServer(SlotControlConfig{
		ListenAddr:    "127.0.0.1:0",
		Mode:          LauncherModeDocker,
		LauncherToken: tok,
		Base:          baseInfo,
		DataDir:       dataDir,
		ManagerUID:    os.Geteuid(),
		Store:         store,
		ArchiveLookupIP: func(_ context.Context, _, host string) ([]netip.Addr, error) {
			if host == "rebind-loopback.example.com" {
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			}
			return nil, fmt.Errorf("unexpected host %s", host)
		},
	})
	if err != nil {
		t.Fatalf("StartSlotControlServer failed: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = strictSrv.Shutdown(ctx)
		cancel()
	}()

	strictBaseURL := "http://" + strictSrv.BoundAddr()

	// Unauthorized request returns 401.
	unauthResp, err := http.Get(strictBaseURL + "/v1/launcher/slot/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", unauthResp.StatusCode)
	}

	doJSONReq := func(method, urlStr, token string, body any) (*http.Response, []byte) {
		t.Helper()
		var rdr io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			rdr = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, urlStr, rdr)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, raw
	}

	// Literal loopback HTTPS URL rejected synchronously with 400 invalid_request.
	resp, _ := doJSONReq(http.MethodPost, strictBaseURL+"/v1/launcher/slot/prepare", tok, map[string]any{
		"target_version":  "v2.0.0-beta.2",
		"archive_url":     "https://127.0.0.1:" + tlsPort + "/slot.tar.gz",
		"expected_sha256": archiveSHA,
		"expected_size":   len(archiveBytes),
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for literal loopback archive_url, got %d", resp.StatusCode)
	}

	// Hostname resolving to 127.0.0.1 fails async download with download_failed.
	resp, _ = doJSONReq(http.MethodPost, strictBaseURL+"/v1/launcher/slot/prepare", tok, map[string]any{
		"target_version":  "v2.0.0-beta.2",
		"archive_url":     "https://rebind-loopback.example.com:" + tlsPort + "/slot.tar.gz",
		"expected_sha256": archiveSHA,
		"expected_size":   len(archiveBytes),
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 for async prepare start, got %d", resp.StatusCode)
	}
	waitForCondition(t, 3*time.Second, func() bool {
		_, raw := doJSONReq(http.MethodGet, strictBaseURL+"/v1/launcher/slot/status", tok, nil)
		var st slotStatusResponse
		_ = json.Unmarshal(raw, &st)
		return st.PrepareOperation != nil && st.PrepareOperation.Phase == SlotPreparePhaseFailed && st.PrepareOperation.ErrorCode == SlotCodeDownloadFailed
	})

	// Test async HTTPS prepare success with test TLS root CA + loopback test dial policy.
	certPool := tlsSrv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	dlSrv, err := StartSlotControlServer(SlotControlConfig{
		ListenAddr:    "127.0.0.1:0",
		Mode:          LauncherModeDocker,
		LauncherToken: tok,
		Base:          baseInfo,
		RunningSlot:   "v2.0.0-beta.2",
		DataDir:       dataDir,
		ManagerUID:    os.Geteuid(),
		Store:         store,
		ArchiveAddrPolicy: func(ip netip.Addr) bool {
			return ip.IsLoopback()
		},
		ArchiveTLSRootCAs: certPool,
		ArchiveLookupIP: func(_ context.Context, _, _ string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = dlSrv.Shutdown(ctx)
		cancel()
	}()

	dlBaseURL := "http://" + dlSrv.BoundAddr()
	archiveBeta3, shaBeta3 := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version:          "v2.0.0-beta.3",
		SchemaGeneration: 2,
	})
	tlsSrvBeta3 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archiveBeta3)
	}))
	defer tlsSrvBeta3.Close()
	_, port3, _ := net.SplitHostPort(tlsSrvBeta3.Listener.Addr().String())
	dlSrv.cfg.ArchiveTLSRootCAs = tlsSrvBeta3.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	resp, _ = doJSONReq(http.MethodPost, dlBaseURL+"/v1/launcher/slot/prepare", tok, map[string]any{
		"target_version":  "v2.0.0-beta.3",
		"archive_url":     "https://cdn.example.com:" + port3 + "/slot-beta3.tar.gz",
		"expected_sha256": shaBeta3,
		"expected_size":   len(archiveBeta3),
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 for valid HTTPS prepare, got %d", resp.StatusCode)
	}
	waitForCondition(t, 5*time.Second, func() bool {
		_, raw := doJSONReq(http.MethodGet, dlBaseURL+"/v1/launcher/slot/status", tok, nil)
		var st slotStatusResponse
		_ = json.Unmarshal(raw, &st)
		return st.PrepareOperation != nil && st.PrepareOperation.Phase == SlotPreparePhaseSucceeded
	})

	// Test cross-schema rollback guard (schema_generation 2 != previous_schema_generation 1 -> 409 schema_rollback_forbidden).
	if err := store.WriteSlotState(SlotState{
		SchemaVersion:            1,
		State:                    SlotStateCommitted,
		ActiveSlot:               "v2.0.0-beta.2",
		PreviousSlot:             "",
		BaseVersionAtSwitch:      "v2.0.0-beta.1",
		SchemaGeneration:         2,
		PreviousSchemaGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}
	_, statusRaw := doJSONReq(http.MethodGet, dlBaseURL+"/v1/launcher/slot/status", tok, nil)
	var st slotStatusResponse
	if err := json.Unmarshal(statusRaw, &st); err != nil {
		t.Fatal(err)
	}
	if st.RollbackAvailable || st.RollbackSameSchema || st.RollbackBlockedReason != SlotCodeSchemaRollbackForbidden {
		t.Fatalf("expected cross-schema rollback blocked in status, got %+v", st)
	}

	rbResp, rbRaw := doJSONReq(http.MethodPost, dlBaseURL+"/v1/launcher/slot/rollback", tok, map[string]any{
		"expected_active_slot":      "v2.0.0-beta.2",
		"current_schema_generation": 2,
	})
	if rbResp.StatusCode != http.StatusConflict || !strings.Contains(string(rbRaw), SlotCodeSchemaRollbackForbidden) {
		t.Fatalf("expected 409 schema_rollback_forbidden, got %d %s", rbResp.StatusCode, string(rbRaw))
	}
}

type subprocessNativeFixture struct {
	HomeDir            string
	DataDir            string
	RunDir             string
	CPAMPRoot          string
	SlotsDir           string
	SlotStatePath      string
	LauncherTokenFile  string
	IngressPublicAddr  string
	RuntimeAddr        string
	ManagerAddr        string
	IngressControlAddr string
	CPAAddr            string
	LauncherCtrlAddr   string
	TestSelfExe        string
}

func setupSubprocessNativeFixture(t *testing.T, baseVersion string) subprocessNativeFixture {
	t.Helper()
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	registerSlotTempCleanup(t, base)

	selfExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	homeDir := filepath.Join(base, "opt-cpamp")
	binDir := filepath.Join(homeDir, "bin")
	dataDir := filepath.Join(homeDir, "data")
	runDir := filepath.Join(homeDir, "run")
	logDir := filepath.Join(homeDir, "logs")
	shareRuntimeDir := filepath.Join(homeDir, "share", "cpamp", "runtime")
	shareReleaseDir := filepath.Join(homeDir, "share", "cpamp", "release")

	for _, d := range []string{binDir, dataDir, runDir, logDir, shareRuntimeDir, shareReleaseDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// Copy current test binary into base bin/ under each native binary name so filepath.Base(os.Args[0])
	// dispatches cleanly in TestMain across unix.Exec and child spawns.
	for _, binName := range []string{
		"cpamp-launcher",
		"cpa-manager-plus-runtime-supervisor",
		"cpa-manager-plus",
		"cpa-manager-plus-ingress",
	} {
		copyExecutableFile(t, selfExe, filepath.Join(binDir, binName), 0o755)
	}
	if err := os.WriteFile(filepath.Join(binDir, "cli-proxy-api"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(homeDir, "share", "cpamp", "provenance.json"), []byte(fmt.Sprintf(`{"version":%q}`+"\n", baseVersion)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(shareReleaseDir, "base-abi.json"),
		[]byte(fmt.Sprintf(`{"schema_version":1,"base_epoch":1,"platform":%q}`+"\n", ExpectedSlotPlatform())),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareRuntimeDir, "config.seed.yaml"), []byte(testSeedYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareRuntimeDir, "seed-lineage.json"), []byte(`{"acceptedSeedIdentities":[]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareRuntimeDir, "cpa-artifact.json"), []byte(testSlotV8ManifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	return subprocessNativeFixture{
		HomeDir:            homeDir,
		DataDir:            dataDir,
		RunDir:             runDir,
		CPAMPRoot:          filepath.Join(dataDir, "runtime", "cpamp"),
		SlotsDir:           filepath.Join(dataDir, "runtime", "cpamp", "slots"),
		SlotStatePath:      filepath.Join(dataDir, "runtime", "cpamp", "slot-state.json"),
		LauncherTokenFile:  filepath.Join(runDir, "launcher-token"),
		IngressPublicAddr:  reserveLoopbackAddr(t),
		RuntimeAddr:        reserveLoopbackAddr(t),
		ManagerAddr:        reserveLoopbackAddr(t),
		IngressControlAddr: reserveLoopbackAddr(t),
		CPAAddr:            reserveLoopbackAddr(t),
		LauncherCtrlAddr:   reserveLoopbackAddr(t),
		TestSelfExe:        selfExe,
	}
}

func copyExecutableFile(t *testing.T, src, dst string, mode os.FileMode) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Chmod(mode); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func buildRealBinarySlotArchive(t *testing.T, selfExe string, version string, behavior slotSubprocessBehavior) ([]byte, string) {
	t.Helper()
	binBytes, err := os.ReadFile(selfExe)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"bin/cpamp-launcher":           binBytes,
		"bin/cpamp-runtime-supervisor": binBytes,
		"bin/cpa-manager-plus":         binBytes,
		"bin/cpamp-ingress":            binBytes,
	}
	if behavior != (slotSubprocessBehavior{}) {
		behJSON, err := json.Marshal(behavior)
		if err != nil {
			t.Fatal(err)
		}
		files["share/cpamp/test-behavior.json"] = behJSON
	}
	return buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version: version,
		Files:   files,
	})
}

func writeVerifiedSnapshotDir(t *testing.T, dataDir, name string, dbContent, keyContent []byte) string {
	t.Helper()
	snapDir := filepath.Join(dataDir, "backups", name)
	if err := os.MkdirAll(snapDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, "database"), dbContent, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, "data-key"), keyContent, 0o600); err != nil {
		t.Fatal(err)
	}
	dbSum := sha256.Sum256(dbContent)
	keySum := sha256.Sum256(keyContent)
	manifest := managerSnapshotManifest{
		Version:           1,
		TargetDBPath:      filepath.Join(dataDir, "manager.db"),
		TargetDataKeyPath: filepath.Join(dataDir, "secret.bin"),
		Files: map[string]managerSnapshotManifestFile{
			"database": {
				Existed: true,
				Mode:    0o600,
				Size:    int64(len(dbContent)),
				SHA256:  hex.EncodeToString(dbSum[:]),
			},
			"database-wal":     {Existed: false},
			"database-shm":     {Existed: false},
			"database-journal": {Existed: false},
			"data-key": {
				Existed: true,
				Mode:    0o600,
				Size:    int64(len(keyContent)),
				SHA256:  hex.EncodeToString(keySum[:]),
			},
		},
	}
	rawMan, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, "manifest.json"), rawMan, 0o600); err != nil {
		t.Fatal(err)
	}
	return snapDir
}

func startSubprocessLauncher(t *testing.T, fx subprocessNativeFixture, extraEnv ...string) (*exec.Cmd, *lockedBufferWriter) {
	t.Helper()
	launcherBin := filepath.Join(fx.HomeDir, "bin", "cpamp-launcher")
	cmd := exec.Command(launcherBin, "--mode=native", "--home="+fx.HomeDir)
	env := append(os.Environ(),
		"CPAMP_NATIVE_HOME="+fx.HomeDir,
		"CPAMP_INGRESS_ADDR="+fx.IngressPublicAddr,
		"CPAMP_RUNTIME_ADDR="+fx.RuntimeAddr,
		"CPAMP_MANAGER_ADDR="+fx.ManagerAddr,
		"CPAMP_INGRESS_CONTROL_ADDR="+fx.IngressControlAddr,
		"CPAMP_RUNTIME_CPA_ADDR="+fx.CPAAddr,
		"CPAMP_LAUNCHER_CONTROL_ADDR="+fx.LauncherCtrlAddr,
		"CPAMP_DRAIN_TIMEOUT=1s",
	)
	env = append(env, extraEnv...)
	cmd.Env = env

	lw := &lockedBufferWriter{buf: &bytes.Buffer{}}
	cmd.Stdout = lw
	cmd.Stderr = lw
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed starting launcher subprocess: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			done := make(chan struct{})
			go func() {
				_, _ = cmd.Process.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		}
	})
	return cmd, lw
}

type lockedBufferWriter struct {
	mu  sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedBufferWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedBufferWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func queryManagerHealthMap(addr string) (map[string]any, bool) {
	client := &http.Client{Timeout: 300 * time.Millisecond}
	resp, err := client.Get("http://" + addr + "/health")
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, false
	}
	return out, true
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	waitForConditionMsg(t, timeout, cond, nil)
}

func waitForConditionMsg(t *testing.T, timeout time.Duration, cond func() bool, diag func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	if diag != nil {
		t.Fatalf("timed out after %s waiting for condition; logs:\n%s", timeout, diag())
	}
	t.Fatalf("timed out after %s waiting for condition", timeout)
}

func TestSubprocessUnixExecForwardTransitionAndOnlineRollback(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	cmd, launcherLogs := startSubprocessLauncher(t, fx, "CPAMP_TEST_TRIAL_STABILIZATION_MS=200")
	initialLauncherPID := cmd.Process.Pid
	// Wait for base v2.0.0-beta.1 Manager to report healthy.
	var health map[string]any
	waitForConditionMsg(t, 8*time.Second, func() bool {
		var ok bool
		health, ok = queryManagerHealthMap(fx.ManagerAddr)
		return ok && health["active_slot"] == ""
	}, func() string { return launcherLogs.String() })
	if int(health["launcher_pid"].(float64)) != initialLauncherPID {
		t.Fatalf("expected launcher_pid=%d, got %v", initialLauncherPID, health["launcher_pid"])
	}

	rawToken, err := os.ReadFile(fx.LauncherTokenFile)
	if err != nil {
		t.Fatalf("read launcher token file: %v", err)
	}
	launcherToken := strings.TrimSpace(string(rawToken))

	// Stage an extra older slot (v2.0.0-beta.10? let's stage v2.0.0-beta.3 too to verify trial commit pruning!)
	slotBeta2Archive, slotBeta2SHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.2", slotSubprocessBehavior{})
	slotBeta3Archive, slotBeta3SHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.3", slotSubprocessBehavior{})

	uploadSlot := func(ver string, data []byte, sha string) {
		t.Helper()
		u := fmt.Sprintf("http://%s/v1/launcher/slot/prepare-upload?target_version=%s&expected_sha256=%s", fx.LauncherCtrlAddr, ver, sha)
		req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+launcherToken)
		req.Header.Set("Content-Type", "application/gzip")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("prepare-upload %s failed: %d %s", ver, resp.StatusCode, string(body))
		}
	}

	uploadSlot("v2.0.0-beta.2", slotBeta2Archive, slotBeta2SHA)
	uploadSlot("v2.0.0-beta.3", slotBeta3Archive, slotBeta3SHA)

	snapDir := writeVerifiedSnapshotDir(t, fx.DataDir, "pre-slot-beta2", []byte("sqlite-v1"), []byte("key-v1"))

	// Activate v2.0.0-beta.2 -> triggers graceful stop + unix.Exec into slots/v2.0.0-beta.2/bin/cpamp-launcher!
	actBody, _ := json.Marshal(map[string]any{
		"target_version":             "v2.0.0-beta.2",
		"snapshot_dir":               snapDir,
		"previous_schema_generation": 1,
	})
	actReq, _ := http.NewRequest(http.MethodPost, "http://"+fx.LauncherCtrlAddr+"/v1/launcher/slot/activate", bytes.NewReader(actBody))
	actReq.Header.Set("Authorization", "Bearer "+launcherToken)
	actReq.Header.Set("Content-Type", "application/json")
	actResp, err := http.DefaultClient.Do(actReq)
	if err != nil {
		t.Fatalf("activate request failed: %v", err)
	}
	rawActResp, _ := io.ReadAll(actResp.Body)
	_ = actResp.Body.Close()
	if actResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 from activate, got %d (%s)", actResp.StatusCode, string(rawActResp))
	}
	var acceptedResp SlotActionAcceptedResponse
	if err := json.Unmarshal(rawActResp, &acceptedResp); err != nil {
		t.Fatalf("decode activate response: %v", err)
	}
	if !strings.HasPrefix(acceptedResp.AttemptID, "attempt-") || len(acceptedResp.AttemptID) != len("attempt-")+16 {
		t.Fatalf("expected attempt-<16hex> in activate response, got %q", acceptedResp.AttemptID)
	}
	// Wait for v2.0.0-beta.2 to come up, pass the 60s Trial Watchdog, receive POST /internal/v1/trial/release, and commit!
	waitForCondition(t, 20*time.Second, func() bool {
		h, ok := queryManagerHealthMap(fx.ManagerAddr)
		if !ok {
			return false
		}
		if h["active_slot"] != "v2.0.0-beta.2" {
			return false
		}
		if hold, _ := h["trial_hold"].(bool); hold {
			return false
		}
		health = h
		return true
	})

	// 1. Launcher PID MUST be preserved across unix.Exec!
	if int(health["launcher_pid"].(float64)) != initialLauncherPID {
		t.Fatalf("unix.Exec failed to preserve PID: got %v, want %d", health["launcher_pid"], initialLauncherPID)
	}
	// 2. Manager started in TRIAL_HOLD=1 and received POST /internal/v1/trial/release!
	if startedHold, _ := health["trial_hold_started_true"].(bool); !startedHold {
		t.Fatalf("expected Manager to start with CPAMP_TRIAL_HOLD=1 during trial")
	}
	if int(health["trial_release_calls"].(float64)) < 1 {
		t.Fatalf("expected POST /internal/v1/trial/release to be called on trial commit")
	}
	// 3. Manager executable is running from slots/v2.0.0-beta.2/bin/cpa-manager-plus and sees CPAMP_BASE_INSTALL_ROOT!
	exeStr, _ := health["exe"].(string)
	if !strings.Contains(exeStr, filepath.Join("slots", "v2.0.0-beta.2", "bin", "cpa-manager-plus")) {
		t.Fatalf("expected manager exe inside slots/v2.0.0-beta.2, got %q", exeStr)
	}
	if health["base_install_root"] != fx.HomeDir {
		t.Fatalf("expected manager CPAMP_BASE_INSTALL_ROOT=%q, got %v", fx.HomeDir, health["base_install_root"])
	}
	// 4. slot-state.json and slot/status expose last_attempt_id with active_attempt_id cleared, and unrelated slot v2.0.0-beta.3 pruned!
	rawState, err := os.ReadFile(fx.SlotStatePath)
	if err != nil {
		t.Fatal(err)
	}
	var committedState SlotState
	if err := json.Unmarshal(rawState, &committedState); err != nil {
		t.Fatal(err)
	}
	if committedState.State != SlotStateCommitted ||
		committedState.ActiveSlot != "v2.0.0-beta.2" ||
		committedState.TrialID != "" ||
		committedState.ActiveAttemptID != "" ||
		committedState.LastAttemptID != acceptedResp.AttemptID ||
		committedState.SnapshotDir != "" {
		t.Fatalf("unexpected committed slot-state: %+v", committedState)
	}
	statusReq, _ := http.NewRequest(http.MethodGet, "http://"+fx.LauncherCtrlAddr+"/v1/launcher/slot/status", nil)
	statusReq.Header.Set("Authorization", "Bearer "+launcherToken)
	statusResp, err := http.DefaultClient.Do(statusReq)
	if err != nil {
		t.Fatal(err)
	}
	rawStatusBody, _ := io.ReadAll(statusResp.Body)
	_ = statusResp.Body.Close()
	var statusPayload slotStatusResponse
	if err := json.Unmarshal(rawStatusBody, &statusPayload); err != nil {
		t.Fatal(err)
	}
	if statusPayload.ActiveAttemptID != "" || statusPayload.LastAttemptID != acceptedResp.AttemptID {
		t.Fatalf("unexpected attempt ids in slot/status: %+v", statusPayload)
	}
	if _, err := os.Lstat(filepath.Join(fx.SlotsDir, "v2.0.0-beta.3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected unrelated staged slot v2.0.0-beta.3 to be pruned upon trial commit")
	}
	// 5. Trigger online rollback back to base v2.0.0-beta.1 and verify PID is STILL preserved!
	rbBody, _ := json.Marshal(map[string]any{
		"expected_active_slot":      "v2.0.0-beta.2",
		"current_schema_generation": 1,
	})
	rbReq, _ := http.NewRequest(http.MethodPost, "http://"+fx.LauncherCtrlAddr+"/v1/launcher/slot/rollback", bytes.NewReader(rbBody))
	rbReq.Header.Set("Authorization", "Bearer "+launcherToken)
	rbReq.Header.Set("Content-Type", "application/json")
	rbResp, err := http.DefaultClient.Do(rbReq)
	if err != nil {
		t.Fatalf("rollback request failed: %v", err)
	}
	_ = rbResp.Body.Close()
	if rbResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 from rollback, got %d", rbResp.StatusCode)
	}

	waitForCondition(t, 20*time.Second, func() bool {
		h, ok := queryManagerHealthMap(fx.ManagerAddr)
		if !ok || h["active_slot"] != "" {
			return false
		}
		health = h
		return true
	})
	if int(health["launcher_pid"].(float64)) != initialLauncherPID {
		t.Fatalf("rollback unix.Exec failed to preserve PID: got %v, want %d", health["launcher_pid"], initialLauncherPID)
	}
	rawStateAfterRB, err := os.ReadFile(fx.SlotStatePath)
	if err != nil {
		t.Fatal(err)
	}
	var afterRB SlotState
	_ = json.Unmarshal(rawStateAfterRB, &afterRB)
	if afterRB.State != SlotStateNone || afterRB.ActiveSlot != "" || afterRB.LastRolledBackSlot != "v2.0.0-beta.2" {
		t.Fatalf("unexpected post-rollback slot-state: %+v", afterRB)
	}
}

func TestSubprocessTrialWatchdogCrashAndTimeoutRollback(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	cmd, launcherLogs := startSubprocessLauncher(t, fx, "CPAMP_TEST_TRIAL_TIMEOUT_MS=900")
	initialLauncherPID := cmd.Process.Pid

	waitForCondition(t, 15*time.Second, func() bool {
		h, ok := queryManagerHealthMap(fx.ManagerAddr)
		return ok && h["active_slot"] == ""
	})

	rawToken, err := os.ReadFile(fx.LauncherTokenFile)
	if err != nil {
		t.Fatal(err)
	}
	launcherToken := strings.TrimSpace(string(rawToken))

	// Case A: Crashing Manager in v2.0.0-beta.2 triggers snapshot restore + rolled_back + unix.Exec back to v2.0.0-beta.1!
	crashArchive, crashSHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.2", slotSubprocessBehavior{
		CrashManager: true,
	})
	upURL := fmt.Sprintf("http://%s/v1/launcher/slot/prepare-upload?target_version=v2.0.0-beta.2&expected_sha256=%s", fx.LauncherCtrlAddr, crashSHA)
	upReq, _ := http.NewRequest(http.MethodPut, upURL, bytes.NewReader(crashArchive))
	upReq.Header.Set("Authorization", "Bearer "+launcherToken)
	upReq.Header.Set("Content-Type", "application/gzip")
	upResp, err := http.DefaultClient.Do(upReq)
	if err != nil || upResp.StatusCode != http.StatusOK {
		t.Fatalf("upload crashing slot failed: err=%v resp=%v", err, upResp)
	}
	_ = upResp.Body.Close()

	snapDir := writeVerifiedSnapshotDir(t, fx.DataDir, "pre-crash-trial", []byte("clean-pre-trial-sqlite"), []byte("clean-pre-trial-key"))
	// Write dirty bytes into live Manager state files to prove rollback restores the snapshot.
	dbPath := filepath.Join(fx.DataDir, "manager.db")
	keyPath := filepath.Join(fx.DataDir, "secret.bin")
	if err := os.WriteFile(dbPath, []byte("dirty-mutated-sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("dirty-mutated-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	actBody, _ := json.Marshal(map[string]any{
		"target_version":             "v2.0.0-beta.2",
		"snapshot_dir":               snapDir,
		"previous_schema_generation": 1,
	})
	actReq, _ := http.NewRequest(http.MethodPost, "http://"+fx.LauncherCtrlAddr+"/v1/launcher/slot/activate", bytes.NewReader(actBody))
	actReq.Header.Set("Authorization", "Bearer "+launcherToken)
	actReq.Header.Set("Content-Type", "application/json")
	actResp, err := http.DefaultClient.Do(actReq)
	if err != nil || actResp.StatusCode != http.StatusAccepted {
		t.Fatalf("activate crashing slot failed: err=%v resp=%v", err, actResp)
	}
	_ = actResp.Body.Close()

	// Wait for slot-state.json to become rolled_back AND base Manager to recover on the same PID!
	var recoveredHealth map[string]any
	waitForCondition(t, 20*time.Second, func() bool {
		rawSt, err := os.ReadFile(fx.SlotStatePath)
		if err != nil {
			return false
		}
		var st SlotState
		if json.Unmarshal(rawSt, &st) != nil || st.State != SlotStateRolledBack {
			return false
		}
		h, ok := queryManagerHealthMap(fx.ManagerAddr)
		if !ok || h["active_slot"] != "" {
			return false
		}
		recoveredHealth = h
		return true
	})

	if int(recoveredHealth["launcher_pid"].(float64)) != initialLauncherPID {
		t.Fatalf("expected launcher PID %d preserved across crash rollback, got %v", initialLauncherPID, recoveredHealth["launcher_pid"])
	}

	rawSt, err := os.ReadFile(fx.SlotStatePath)
	if err != nil {
		t.Fatal(err)
	}
	var st SlotState
	_ = json.Unmarshal(rawSt, &st)
	if st.State != SlotStateRolledBack || st.LastFailureCode != FailureCodeManagerStartupFailed || st.LastRolledBackSlot != "v2.0.0-beta.2" || st.ActiveSlot != "" {
		t.Fatalf("unexpected rolled_back slot-state after manager crash: %+v", st)
	}
	if !strings.Contains(launcherLogs.String(), "[launcher] trial v2.0.0-beta.2 failed (manager_startup_failed); rolling back before opening writes") {
		t.Fatalf("missing expected trial rollback log line, got:\n%s", launcherLogs.String())
	}

	// Verify manager-data-snapshot restore ran and restored both database and secret key!
	gotDB, err := os.ReadFile(dbPath)
	if err != nil || string(gotDB) != "clean-pre-trial-sqlite" {
		t.Fatalf("expected manager.db restored to clean-pre-trial-sqlite, got %q (err=%v)", string(gotDB), err)
	}
	gotKey, err := os.ReadFile(keyPath)
	if err != nil || string(gotKey) != "clean-pre-trial-key" {
		t.Fatalf("expected secret.bin restored to clean-pre-trial-key, got %q (err=%v)", string(gotKey), err)
	}

	// Case B: Hanging Manager in v2.0.0-beta.3 rejects empty snapshot_dir (422) and triggers trial_timeout rollback with valid snapshot!
	hangArchive, hangSHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.3", slotSubprocessBehavior{
		HangManager: true,
	})
	upURL3 := fmt.Sprintf("http://%s/v1/launcher/slot/prepare-upload?target_version=v2.0.0-beta.3&expected_sha256=%s", fx.LauncherCtrlAddr, hangSHA)
	upReq3, _ := http.NewRequest(http.MethodPut, upURL3, bytes.NewReader(hangArchive))
	upReq3.Header.Set("Authorization", "Bearer "+launcherToken)
	upReq3.Header.Set("Content-Type", "application/gzip")
	upResp3, err := http.DefaultClient.Do(upReq3)
	if err != nil || upResp3.StatusCode != http.StatusOK {
		t.Fatalf("upload hanging slot failed: err=%v resp=%v", err, upResp3)
	}
	_ = upResp3.Body.Close()

	emptySnapBody, _ := json.Marshal(map[string]any{
		"target_version":             "v2.0.0-beta.3",
		"snapshot_dir":               "",
		"previous_schema_generation": 1,
	})
	emptySnapReq, _ := http.NewRequest(http.MethodPost, "http://"+fx.LauncherCtrlAddr+"/v1/launcher/slot/activate", bytes.NewReader(emptySnapBody))
	emptySnapReq.Header.Set("Authorization", "Bearer "+launcherToken)
	emptySnapReq.Header.Set("Content-Type", "application/json")
	emptySnapResp, err := http.DefaultClient.Do(emptySnapReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = emptySnapResp.Body.Close()
	if emptySnapResp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 snapshot_invalid for empty snapshot_dir, got %d", emptySnapResp.StatusCode)
	}

	snapDir3 := writeVerifiedSnapshotDir(t, fx.DataDir, "pre-hang-trial", []byte("clean-pre-trial-sqlite"), []byte("clean-pre-trial-key"))
	actBody3, _ := json.Marshal(map[string]any{
		"target_version":             "v2.0.0-beta.3",
		"snapshot_dir":               snapDir3,
		"previous_schema_generation": 1,
	})
	actReq3, _ := http.NewRequest(http.MethodPost, "http://"+fx.LauncherCtrlAddr+"/v1/launcher/slot/activate", bytes.NewReader(actBody3))
	actReq3.Header.Set("Authorization", "Bearer "+launcherToken)
	actReq3.Header.Set("Content-Type", "application/json")
	actResp3, err := http.DefaultClient.Do(actReq3)
	if err != nil || actResp3.StatusCode != http.StatusAccepted {
		t.Fatalf("activate hanging slot failed: err=%v resp=%v", err, actResp3)
	}
	_ = actResp3.Body.Close()
	waitForCondition(t, 20*time.Second, func() bool {
		rawSt3, err := os.ReadFile(fx.SlotStatePath)
		if err != nil {
			return false
		}
		var st3 SlotState
		if json.Unmarshal(rawSt3, &st3) != nil || st3.LastRolledBackSlot != "v2.0.0-beta.3" || st3.LastFailureCode != FailureCodeTrialTimeout {
			return false
		}
		h, ok := queryManagerHealthMap(fx.ManagerAddr)
		if !ok || h["active_slot"] != "" {
			return false
		}
		recoveredHealth = h
		return true
	})
	if int(recoveredHealth["launcher_pid"].(float64)) != initialLauncherPID {
		t.Fatalf("expected launcher PID %d preserved across timeout rollback, got %v", initialLauncherPID, recoveredHealth["launcher_pid"])
	}
}

func TestSnapshotValidationHardeningAndLargeDatabaseStreaming(t *testing.T) {
	if DefaultSnapshotRestoreTimeout != 2*time.Minute {
		t.Fatalf("expected DefaultSnapshotRestoreTimeout=2m, got %s", DefaultSnapshotRestoreTimeout)
	}
	if releaseManagerTrialHold(context.Background(), "", "tok", 50*time.Millisecond) {
		t.Fatalf("expected releaseManagerTrialHold to return false when managerAddr is empty")
	}
	if releaseManagerTrialHold(context.Background(), "127.0.0.1:9082", "", 50*time.Millisecond) {
		t.Fatalf("expected releaseManagerTrialHold to return false when launcherToken is empty")
	}

	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	dataDir := filepath.Join(base, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	uid := os.Geteuid()

	// 1. Valid snapshot passes.
	validSnap := writeVerifiedSnapshotDir(t, dataDir, "valid-snap", []byte("db-bytes"), []byte("key-bytes"))
	if err := ValidateManagerDataSnapshotDir(validSnap, dataDir, uid); err != nil {
		t.Fatalf("expected valid snapshot to pass: %v", err)
	}

	// 2. Extra unexpected entry in snapshotDir is rejected.
	extraFile := filepath.Join(validSnap, "extra.tmp")
	if err := os.WriteFile(extraFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagerDataSnapshotDir(validSnap, dataDir, uid); slotErrorCode(err, "") != SlotCodeSnapshotInvalid {
		t.Fatalf("expected snapshot_invalid for extra entry, got %v", err)
	}
	_ = os.Remove(extraFile)

	// 3. Intermediate symlink segment under dataDir is rejected via openRelSubdirNoFollow.
	realOutside := filepath.Join(base, "outside-backups")
	if err := os.MkdirAll(realOutside, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkSeg := filepath.Join(dataDir, "sym-backups")
	if err := os.Symlink(realOutside, symlinkSeg); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagerDataSnapshotDir(filepath.Join(symlinkSeg, "snap"), dataDir, uid); slotErrorCode(err, "") != SlotCodeSnapshotInvalid {
		t.Fatalf("expected snapshot_invalid for symlinked parent segment, got %v", err)
	}

	// 4. database.Existed == false is rejected.
	noDBSnap := writeVerifiedSnapshotDir(t, dataDir, "no-db-snap", []byte("db"), []byte("key"))
	_ = os.Remove(filepath.Join(noDBSnap, "database"))
	keySum := sha256.Sum256([]byte("key"))
	noDBMan := managerSnapshotManifest{
		Version:           1,
		TargetDBPath:      filepath.Join(dataDir, "manager.db"),
		TargetDataKeyPath: filepath.Join(dataDir, "secret.bin"),
		Files: map[string]managerSnapshotManifestFile{
			"database":         {Existed: false},
			"database-wal":     {Existed: false},
			"database-shm":     {Existed: false},
			"database-journal": {Existed: false},
			"data-key":         {Existed: true, Mode: 0o600, Size: 3, SHA256: hex.EncodeToString(keySum[:])},
		},
	}
	rawNoDB, _ := json.Marshal(noDBMan)
	_ = os.WriteFile(filepath.Join(noDBSnap, "manifest.json"), rawNoDB, 0o600)
	if err := ValidateManagerDataSnapshotDir(noDBSnap, dataDir, uid); slotErrorCode(err, "") != SlotCodeSnapshotInvalid {
		t.Fatalf("expected snapshot_invalid when database.Existed is false, got %v", err)
	}

	// 5. Invalid target_db_path / target_data_key_path (empty, outside dataDir, or symlinked parent) is rejected.
	badTargetSnap := writeVerifiedSnapshotDir(t, dataDir, "bad-target-snap", []byte("db"), []byte("key"))
	dbSum := sha256.Sum256([]byte("db"))
	for _, badDBPath := range []string{
		"",
		filepath.Join(base, "outside.db"),
		filepath.Join(badTargetSnap, "inside-snap.db"),
		filepath.Join(symlinkSeg, "nested.db"),
	} {
		man := managerSnapshotManifest{
			Version:           1,
			TargetDBPath:      badDBPath,
			TargetDataKeyPath: filepath.Join(dataDir, "secret.bin"),
			Files: map[string]managerSnapshotManifestFile{
				"database":         {Existed: true, Mode: 0o600, Size: 2, SHA256: hex.EncodeToString(dbSum[:])},
				"database-wal":     {Existed: false},
				"database-shm":     {Existed: false},
				"database-journal": {Existed: false},
				"data-key":         {Existed: true, Mode: 0o600, Size: 3, SHA256: hex.EncodeToString(keySum[:])},
			},
		}
		rawMan, _ := json.Marshal(man)
		_ = os.WriteFile(filepath.Join(badTargetSnap, "manifest.json"), rawMan, 0o600)
		if err := ValidateManagerDataSnapshotDir(badTargetSnap, dataDir, uid); slotErrorCode(err, "") != SlotCodeSnapshotInvalid {
			t.Fatalf("expected snapshot_invalid for target_db_path=%q, got %v", badDBPath, err)
		}
	}

	// 6. Database snapshot > MaxSlotUncompressedBytes (512 MiB) streams cleanly without artificial cap.
	largeSnap := filepath.Join(dataDir, "backups", "large-db-snap")
	if err := os.MkdirAll(largeSnap, 0o700); err != nil {
		t.Fatal(err)
	}
	largeSize := int64(MaxSlotUncompressedBytes + 1024*1024) // 513 MiB sparse file
	largeDBFile, err := os.OpenFile(filepath.Join(largeSnap, "database"), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := largeDBFile.Truncate(largeSize); err != nil {
		_ = largeDBFile.Close()
		t.Fatal(err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, largeDBFile); err != nil {
		_ = largeDBFile.Close()
		t.Fatal(err)
	}
	_ = largeDBFile.Close()
	if err := os.WriteFile(filepath.Join(largeSnap, "data-key"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	largeMan := managerSnapshotManifest{
		Version:           1,
		TargetDBPath:      filepath.Join(dataDir, "manager.db"),
		TargetDataKeyPath: filepath.Join(dataDir, "secret.bin"),
		Files: map[string]managerSnapshotManifestFile{
			"database":         {Existed: true, Mode: 0o600, Size: largeSize, SHA256: hex.EncodeToString(h.Sum(nil))},
			"database-wal":     {Existed: false},
			"database-shm":     {Existed: false},
			"database-journal": {Existed: false},
			"data-key":         {Existed: true, Mode: 0o600, Size: 3, SHA256: hex.EncodeToString(keySum[:])},
		},
	}
	rawLargeMan, _ := json.Marshal(largeMan)
	if err := os.WriteFile(filepath.Join(largeSnap, "manifest.json"), rawLargeMan, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagerDataSnapshotDir(largeSnap, dataDir, uid); err != nil {
		t.Fatalf("expected >512 MiB database snapshot to stream-validate without cap failure, got: %v", err)
	}
}

func TestForwardOnlyVersionChecksAndSchemaDowngradeGuard(t *testing.T) {
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	registerSlotTempCleanup(t, base)

	dataDir := filepath.Join(base, "data")
	cpampRoot := filepath.Join(dataDir, "runtime", "cpamp")
	slotsDir := filepath.Join(cpampRoot, "slots")
	statePath := filepath.Join(cpampRoot, "slot-state.json")
	for _, d := range []string{slotsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store := NewSlotStoreWithOwner(cpampRoot, slotsDir, statePath, 1, os.Geteuid(), false)

	// Stage v2.0.0-beta.2 (schema 1) and v2.0.0-beta.4 (schema 1).
	archBeta2, shaBeta2 := buildSignedSlotArchive(t, slotArchiveBuildOptions{Version: "v2.0.0-beta.2", SchemaGeneration: 1})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(archBeta2), "v2.0.0-beta.2", shaBeta2, int64(len(archBeta2)), nil); err != nil {
		t.Fatal(err)
	}
	archBeta4, shaBeta4 := buildSignedSlotArchive(t, slotArchiveBuildOptions{Version: "v2.0.0-beta.4", SchemaGeneration: 1})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(archBeta4), "v2.0.0-beta.4", shaBeta4, int64(len(archBeta4)), nil); err != nil {
		t.Fatal(err)
	}

	// Running slot is v2.0.0-beta.3 (> base v2.0.0-beta.1), so v2.0.0-beta.2 is > base but < running!
	tok := strings.Repeat("d", 64)
	srv, err := StartSlotControlServer(SlotControlConfig{
		ListenAddr:    "127.0.0.1:0",
		Mode:          LauncherModeDocker,
		LauncherToken: tok,
		Base: BaseInstallationInfo{
			Mode:        LauncherModeDocker,
			InstallRoot: base,
			BaseVersion: "v2.0.0-beta.1",
			BaseEpoch:   1,
			Platform:    ExpectedSlotPlatform(),
		},
		RunningSlot:  "v2.0.0-beta.3",
		DataDir:      dataDir,
		ManagerUID:   os.Geteuid(),
		TrialTimeout: 60 * time.Second,
		Store:        store,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}()
	baseURL := "http://" + srv.BoundAddr()

	doReq := func(method, url string, contentType string, body io.Reader) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequest(method, url, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, string(raw)
	}

	// 1. prepare rejects target_version <= runningVersion (v2.0.0-beta.2 < v2.0.0-beta.3).
	prepBody, _ := json.Marshal(map[string]any{
		"target_version":  "v2.0.0-beta.2",
		"archive_url":     "https://github.com/org/repo/releases/download/v2.0.0-beta.2/slot.tar.gz",
		"expected_sha256": shaBeta2,
	})
	resp, raw := doReq(http.MethodPost, baseURL+"/v1/launcher/slot/prepare", "application/json", bytes.NewReader(prepBody))
	if resp.StatusCode != http.StatusConflict || !strings.Contains(raw, SlotCodeVersionConflict) {
		t.Fatalf("expected 409 version_conflict from prepare downgrade, got %d %s", resp.StatusCode, raw)
	}

	// 2. prepare-upload rejects target_version <= runningVersion.
	upURL := fmt.Sprintf("%s/v1/launcher/slot/prepare-upload?target_version=v2.0.0-beta.2&expected_sha256=%s", baseURL, shaBeta2)
	resp, raw = doReq(http.MethodPut, upURL, "application/gzip", bytes.NewReader(archBeta2))
	if resp.StatusCode != http.StatusConflict || !strings.Contains(raw, SlotCodeVersionConflict) {
		t.Fatalf("expected 409 version_conflict from prepare-upload downgrade, got %d %s", resp.StatusCode, raw)
	}

	// 3. activate rejects target_version <= runningVersion.
	snapDir := writeVerifiedSnapshotDir(t, dataDir, "snap-forward", []byte("db"), []byte("key"))
	actDowngrade, _ := json.Marshal(map[string]any{
		"target_version":             "v2.0.0-beta.2",
		"snapshot_dir":               snapDir,
		"previous_schema_generation": 1,
	})
	resp, raw = doReq(http.MethodPost, baseURL+"/v1/launcher/slot/activate", "application/json", bytes.NewReader(actDowngrade))
	if resp.StatusCode != http.StatusConflict || !strings.Contains(raw, SlotCodeVersionConflict) {
		t.Fatalf("expected 409 version_conflict from activate downgrade, got %d %s", resp.StatusCode, raw)
	}

	// 4. activate rejects manifest.SchemaGeneration (1) < prevSchemaGen (2) with 409 schema_rollback_forbidden!
	actSchemaDown, _ := json.Marshal(map[string]any{
		"target_version":             "v2.0.0-beta.4",
		"snapshot_dir":               snapDir,
		"previous_schema_generation": 2,
	})
	resp, raw = doReq(http.MethodPost, baseURL+"/v1/launcher/slot/activate", "application/json", bytes.NewReader(actSchemaDown))
	if resp.StatusCode != http.StatusConflict || !strings.Contains(raw, SlotCodeSchemaRollbackForbidden) {
		t.Fatalf("expected 409 schema_rollback_forbidden when manifest.SchemaGeneration < prevSchemaGen, got %d %s", resp.StatusCode, raw)
	}
}

func TestColdStartTrialBootCountAndRecoveryRequiredStateMachine(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	store := NewSlotStoreWithOwner(fx.CPAMPRoot, fx.SlotsDir, fx.SlotStatePath, 1, os.Geteuid(), false)
	if err := os.MkdirAll(fx.SlotsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	slotBeta2Archive, slotBeta2SHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.2", slotSubprocessBehavior{})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(slotBeta2Archive), "v2.0.0-beta.2", slotBeta2SHA, int64(len(slotBeta2Archive)), nil); err != nil {
		t.Fatal(err)
	}

	baseInfo, err := ResolveBaseInstallation(LauncherCLIOptions{Mode: LauncherModeNative, HomeDir: fx.HomeDir}, os.Geteuid(), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	slotLauncherExe := filepath.Join(fx.SlotsDir, "v2.0.0-beta.2", "bin", "cpamp-launcher")

	snapDir := writeVerifiedSnapshotDir(t, fx.DataDir, "pre-boot-loop", []byte("clean-db-v1"), []byte("clean-key-v1"))
	dbPath := filepath.Join(fx.DataDir, "manager.db")
	keyPath := filepath.Join(fx.DataDir, "secret.bin")
	_ = os.WriteFile(dbPath, []byte("mutated-db"), 0o600)
	_ = os.WriteFile(keyPath, []byte("mutated-key"), 0o600)

	if err := WriteSwitchSlotState(store, baseInfo.BaseVersion, "", SlotSwitchAction{
		Kind:                     SlotSwitchActivate,
		AttemptID:                "attempt-0123456789abcdef",
		TargetVersion:            "v2.0.0-beta.2",
		SnapshotDir:              snapDir,
		TargetSchemaGeneration:   1,
		PreviousSchemaGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. First entry inside target slot sets TrialBootCount = 1 and grants execution plan.
	var logBuf bytes.Buffer
	plan1, err := ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        baseInfo,
		Store:       store,
		DataDir:     fx.DataDir,
		ManagerUID:  os.Geteuid(),
		SelfExePath: slotLauncherExe,
		Args:        []string{slotLauncherExe, "--mode=native", "--home=" + fx.HomeDir},
		Environ:     []string{"CPAMP_ACTIVE_SLOT=v2.0.0-beta.2"},
		LogOut:      &logBuf,
	})
	if err != nil {
		t.Fatalf("first trial ArbitrateColdStart failed: %v", err)
	}
	if plan1.ExecTarget != nil || !plan1.TrialHold || plan1.State.TrialBootCount != 1 {
		t.Fatalf("expected TrialHold=true and TrialBootCount=1 on first target boot, got %+v", plan1)
	}

	// 2. Inject persistent restore failure so second trial boot (boot loop!) enters recovery_required and fails closed!
	failAlwaysFlag := filepath.Join(filepath.Dir(snapDir), "fail-restore.always")
	if err := os.WriteFile(failAlwaysFlag, []byte("fail\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        baseInfo,
		Store:       store,
		DataDir:     fx.DataDir,
		ManagerUID:  os.Geteuid(),
		SelfExePath: slotLauncherExe,
		Args:        []string{slotLauncherExe, "--mode=native", "--home=" + fx.HomeDir},
		Environ:     []string{"CPAMP_ACTIVE_SLOT=v2.0.0-beta.2"},
		LogOut:      &logBuf,
	})
	wantSnapRestoreCmd := "cpa-manager-plus restore-pre-migrate --snapshot-dir " + snapDir + " --confirm"
	if err == nil || !strings.Contains(err.Error(), wantSnapRestoreCmd) {
		t.Fatalf("expected fail-closed error naming %q, got %v", wantSnapRestoreCmd, err)
	}

	recState, exists, err := store.ReadSlotStateIfExists()
	if err != nil || !exists {
		t.Fatalf("read recovery_required state: exists=%v err=%v", exists, err)
	}
	if recState.State != SlotStateRecoveryRequired ||
		recState.SnapshotDir != snapDir ||
		recState.ActiveSlot != "v2.0.0-beta.2" ||
		recState.ActiveAttemptID != "" ||
		recState.LastAttemptID != "attempt-0123456789abcdef" ||
		recState.LastFailureCode != FailureCodeSnapshotRestoreFailed {
		t.Fatalf("unexpected recovery_required slot-state: %+v", recState)
	}

	// 3. Subsequent cold start while restore still fails retries once and STILL fails closed (never booting Manager over mutated-db!).
	_, err = ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        baseInfo,
		Store:       store,
		DataDir:     fx.DataDir,
		ManagerUID:  os.Geteuid(),
		SelfExePath: baseInfo.LauncherExecutable,
		Args:        []string{baseInfo.LauncherExecutable, "--mode=native", "--home=" + fx.HomeDir},
		Environ:     []string{},
		LogOut:      &logBuf,
	})
	if err == nil || !strings.Contains(err.Error(), wantSnapRestoreCmd) {
		t.Fatalf("expected recovery_required cold start to fail closed naming %q, got %v", wantSnapRestoreCmd, err)
	}
	if gotDB, _ := os.ReadFile(dbPath); string(gotDB) != "mutated-db" {
		t.Fatalf("expected mutated-db still untouched while restore fails, got %q", string(gotDB))
	}

	// 4. Remove persistent restore failure: next cold start retries verification + restore once, restores clean DB, and transitions to rolled_back!
	_ = os.Remove(failAlwaysFlag)
	recoveredPlan, err := ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        baseInfo,
		Store:       store,
		DataDir:     fx.DataDir,
		ManagerUID:  os.Geteuid(),
		SelfExePath: baseInfo.LauncherExecutable,
		Args:        []string{baseInfo.LauncherExecutable, "--mode=native", "--home=" + fx.HomeDir},
		Environ:     []string{},
		LogOut:      &logBuf,
	})
	if err != nil {
		t.Fatalf("expected cold-start recovery_required retry to succeed once restore is healthy: %v", err)
	}
	if recoveredPlan.ExecTarget != nil || recoveredPlan.ActiveSlot != "" || recoveredPlan.State.State != SlotStateRolledBack {
		t.Fatalf("unexpected recovered plan: %+v", recoveredPlan)
	}
	if gotDB, _ := os.ReadFile(dbPath); string(gotDB) != "clean-db-v1" {
		t.Fatalf("expected manager.db restored to clean-db-v1, got %q", string(gotDB))
	}
	if gotKey, _ := os.ReadFile(keyPath); string(gotKey) != "clean-key-v1" {
		t.Fatalf("expected secret.bin restored to clean-key-v1, got %q", string(gotKey))
	}

	// 5. Also verify normal trial_boot_loop rollback (when restore succeeds on first attempt) records FailureCodeTrialBootLoop!
	snapLoop := writeVerifiedSnapshotDir(t, fx.DataDir, "pre-boot-loop-ok", []byte("clean-db-v2"), []byte("clean-key-v2"))
	_ = os.WriteFile(dbPath, []byte("dirty-v2"), 0o600)
	if err := store.WriteSlotState(SlotState{
		SchemaVersion:            1,
		State:                    SlotStateTrial,
		ActiveSlot:               "v2.0.0-beta.2",
		PreviousSlot:             "",
		BaseVersionAtSwitch:      baseInfo.BaseVersion,
		TrialID:                  "attempt-fedcba9876543210",
		TrialBootCount:           1,
		ActiveAttemptID:          "attempt-fedcba9876543210",
		LastAttemptID:            "attempt-fedcba9876543210",
		SnapshotDir:              snapLoop,
		SchemaGeneration:         1,
		PreviousSchemaGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}
	loopPlan, err := ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        baseInfo,
		Store:       store,
		DataDir:     fx.DataDir,
		ManagerUID:  os.Geteuid(),
		SelfExePath: baseInfo.LauncherExecutable,
		Args:        []string{baseInfo.LauncherExecutable, "--mode=native", "--home=" + fx.HomeDir},
		Environ:     []string{},
		LogOut:      &logBuf,
	})
	if err != nil {
		t.Fatalf("trial_boot_loop rollback failed: %v", err)
	}
	if loopPlan.State.State != SlotStateRolledBack ||
		loopPlan.State.LastFailureCode != FailureCodeTrialBootLoop ||
		loopPlan.State.ActiveAttemptID != "" ||
		loopPlan.State.LastAttemptID != "attempt-fedcba9876543210" {
		t.Fatalf("unexpected state after trial_boot_loop rollback: %+v", loopPlan.State)
	}

	// 6. Verify untrusted/corrupted PreviousSlot during ExecuteTrialRollback enters recovery_required!
	archBeta3, shaBeta3 := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.3", slotSubprocessBehavior{})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(archBeta3), "v2.0.0-beta.3", shaBeta3, int64(len(archBeta3)), nil); err != nil {
		t.Fatal(err)
	}
	// Corrupt previous slot v2.0.0-beta.2 manifest signature!
	prevSigPath := filepath.Join(fx.SlotsDir, "v2.0.0-beta.2", "slot-manifest.json.sig")
	_ = os.Chmod(filepath.Join(fx.SlotsDir, "v2.0.0-beta.2"), 0o755)
	_ = os.Remove(prevSigPath)

	snapPrevBad := writeVerifiedSnapshotDir(t, fx.DataDir, "pre-prev-corrupt", []byte("clean-db-v3"), []byte("clean-key-v3"))
	if err := store.WriteSlotState(SlotState{
		SchemaVersion:            1,
		State:                    SlotStateTrial,
		ActiveSlot:               "v2.0.0-beta.3",
		PreviousSlot:             "v2.0.0-beta.2",
		BaseVersionAtSwitch:      baseInfo.BaseVersion,
		TrialID:                  "attempt-1111222233334444",
		TrialBootCount:           1,
		ActiveAttemptID:          "attempt-1111222233334444",
		LastAttemptID:            "attempt-1111222233334444",
		SnapshotDir:              snapPrevBad,
		SchemaGeneration:         1,
		PreviousSchemaGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}
	rbTr, rbSt, rbErr := ExecuteTrialRollback(TrialWatchdogConfig{
		Store:           store,
		ActiveSlot:      "v2.0.0-beta.3",
		DataDir:         fx.DataDir,
		ManagerUID:      os.Geteuid(),
		BaseInstallRoot: fx.HomeDir,
		BaseVersion:     baseInfo.BaseVersion,
	}, SlotState{}, FailureCodeManagerStartupFailed, &logBuf)
	if rbErr == nil || rbTr != nil || rbSt.State != SlotStateRecoveryRequired || rbSt.PreviousSlot != "v2.0.0-beta.2" || rbSt.SnapshotDir != snapPrevBad {
		t.Fatalf("expected recovery_required when PreviousSlot fails verification, got tr=%v st=%+v err=%v", rbTr, rbSt, rbErr)
	}
}

func TestNativeFilesystemPermissionsTightening(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	looseDB := filepath.Join(fx.DataDir, "manager.db")
	looseKey := filepath.Join(fx.DataDir, "secret.bin")
	looseScript := filepath.Join(fx.DataDir, "helper.bin")
	nestedDir := filepath.Join(fx.DataDir, "backups")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(looseDB, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(looseKey, []byte("key"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(looseScript, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	store := NewSlotStoreWithOwner(fx.CPAMPRoot, fx.SlotsDir, fx.SlotStatePath, 1, os.Geteuid(), false)
	baseInfo, err := ResolveBaseInstallation(LauncherCLIOptions{Mode: LauncherModeNative, HomeDir: fx.HomeDir}, os.Geteuid(), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	paths, err := resolveNativePathsAndAddrs(fx.HomeDir, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	layout := ResolveRuntimeLayout(paths.DataDir, paths.RuntimeRoot, paths.TokenFile)
	// Pre-create supervisor/active with loose 0755 mode to prove prepareNativeFilesystem resets it to 0700!
	activeCPADir := filepath.Join(layout.SupervisorDir, "active", "cpa")
	if err := os.MkdirAll(activeCPADir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(filepath.Join(layout.SupervisorDir, "active"), 0o755)
	_ = os.Chmod(activeCPADir, 0o755)

	plan := &ArbitratedExecutionPlan{
		Base:                    baseInfo,
		Store:                   store,
		RunningVersion:          baseInfo.BaseVersion,
		SupervisorExecutable:    baseInfo.SupervisorExecutable,
		ManagerExecutable:       baseInfo.ManagerExecutable,
		IngressExecutable:       baseInfo.IngressExecutable,
		CPAExecutable:           baseInfo.CPAExecutable,
		SeedConfigPath:          baseInfo.SeedConfigPath,
		SeedLineagePath:         baseInfo.SeedLineagePath,
		CPAArtifactManifestPath: baseInfo.CPAArtifactManifestPath,
	}
	prepared, err := prepareNativeFilesystem(context.Background(), paths, layout, plan, os.Geteuid(), os.Getegid(), map[string]string{})
	if err != nil {
		t.Fatalf("prepareNativeFilesystem failed: %v", err)
	}
	if prepared.BaseInstallRoot != fx.HomeDir {
		t.Fatalf("expected BaseInstallRoot=%q, got %q", fx.HomeDir, prepared.BaseInstallRoot)
	}

	for path, wantPerm := range map[string]os.FileMode{
		looseDB:     0o600,
		looseKey:    0o600,
		looseScript: 0o700,
		filepath.Join(layout.SupervisorDir, "active"): 0o700,
		activeCPADir: 0o700,
	} {
		st, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("lstat %s: %v", path, err)
		}
		if st.Mode().Perm() != wantPerm {
			t.Fatalf("expected %s mode %#o, got %#o", path, wantPerm, st.Mode().Perm())
		}
	}
}

func TestSubprocessSoakDiscovery503AndPreSupervisionRollback(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	cmd, launcherLogs := startSubprocessLauncher(
		t,
		fx,
		"CPAMP_TEST_TRIAL_TIMEOUT_MS=3600",
		"CPAMP_TEST_TRIAL_STABILIZATION_MS=1000",
		"CPAMP_UPGRADE_WORKER_SOCKET=/run/cpamp-upgrade/worker.sock",
		"CPAMP_UPGRADE_SOURCE_ROOT="+fx.DataDir,
	)
	initialLauncherPID := cmd.Process.Pid

	waitForCondition(t, 15*time.Second, func() bool {
		h, ok := queryManagerHealthMap(fx.ManagerAddr)
		return ok && h["active_slot"] == ""
	})

	rawToken, err := os.ReadFile(fx.LauncherTokenFile)
	if err != nil {
		t.Fatal(err)
	}
	launcherToken := strings.TrimSpace(string(rawToken))

	uploadAndActivate := func(ver string, beh slotSubprocessBehavior, snapName string) {
		t.Helper()
		arch, sha := buildRealBinarySlotArchive(t, fx.TestSelfExe, ver, beh)
		upURL := fmt.Sprintf("http://%s/v1/launcher/slot/prepare-upload?target_version=%s&expected_sha256=%s", fx.LauncherCtrlAddr, ver, sha)
		upReq, _ := http.NewRequest(http.MethodPut, upURL, bytes.NewReader(arch))
		upReq.Header.Set("Authorization", "Bearer "+launcherToken)
		upReq.Header.Set("Content-Type", "application/gzip")
		upResp, err := http.DefaultClient.Do(upReq)
		if err != nil || upResp.StatusCode != http.StatusOK {
			t.Fatalf("upload %s failed: err=%v resp=%v", ver, err, upResp)
		}
		_ = upResp.Body.Close()

		snapDir := writeVerifiedSnapshotDir(t, fx.DataDir, snapName, []byte("clean-db"), []byte("clean-key"))
		actBody, _ := json.Marshal(map[string]any{
			"target_version":             ver,
			"snapshot_dir":               snapDir,
			"previous_schema_generation": 1,
		})
		actReq, _ := http.NewRequest(http.MethodPost, "http://"+fx.LauncherCtrlAddr+"/v1/launcher/slot/activate", bytes.NewReader(actBody))
		actReq.Header.Set("Authorization", "Bearer "+launcherToken)
		actReq.Header.Set("Content-Type", "application/json")
		actResp, err := http.DefaultClient.Do(actReq)
		if err != nil || actResp.StatusCode != http.StatusAccepted {
			t.Fatalf("activate %s failed: err=%v resp=%v", ver, err, actResp)
		}
		_ = actResp.Body.Close()
	}

	waitRollback := func(wantSlot, wantFailureCode string) {
		t.Helper()
		var recovered map[string]any
		waitForConditionMsg(t, 25*time.Second, func() bool {
			rawSt, err := os.ReadFile(fx.SlotStatePath)
			if err != nil {
				return false
			}
			var st SlotState
			if json.Unmarshal(rawSt, &st) != nil || st.State != SlotStateRolledBack || st.LastRolledBackSlot != wantSlot || st.LastFailureCode != wantFailureCode {
				return false
			}
			h, ok := queryManagerHealthMap(fx.ManagerAddr)
			if !ok || h["active_slot"] != "" {
				return false
			}
			recovered = h
			return true
		}, func() string { return launcherLogs.String() })
		if int(recovered["launcher_pid"].(float64)) != initialLauncherPID {
			t.Fatalf("expected launcher PID %d preserved after %s (%s), got %v", initialLauncherPID, wantSlot, wantFailureCode, recovered["launcher_pid"])
		}
	}

	// Case 1: /health == 503 with /.well-known/cpamp-bootstrap/ui == 200 MUST NOT satisfy trial readiness!
	uploadAndActivate("v2.0.0-beta.2", slotSubprocessBehavior{DiscoveryOnly503: true}, "snap-503")
	waitRollback("v2.0.0-beta.2", FailureCodeTrialTimeout)

	// Case 2: Manager serves /health == 200 initially, then exits (status 0) mid-stabilization soak -> rolls back before commit!
	uploadAndActivate("v2.0.0-beta.3", slotSubprocessBehavior{
		CrashManagerDuringSoakAfterMS: 100,
		CrashManagerExitZero:          true,
	}, "snap-soak-exit")
	waitRollback("v2.0.0-beta.3", FailureCodeManagerStartupFailed)

	// Case 3: Pre-supervision step (configureUpgradeWorker) fails during trial -> rolls back via ExecuteTrialRollback + unix.Exec!
	uploadAndActivate("v2.0.0-beta.4", slotSubprocessBehavior{FailUpgradeWorkerCheck: true}, "snap-presup-fail")
	waitRollback("v2.0.0-beta.4", FailureCodeManagerStartupFailed)

	// Case 4: upgradeSvc fails readiness during trial -> triggers trial rollback instead of continuing degraded!
	uploadAndActivate("v2.0.0-beta.5", slotSubprocessBehavior{FailUpgradeWorkerDaemon: true}, "snap-upgsvc-fail")
	waitRollback("v2.0.0-beta.5", FailureCodeManagerStartupFailed)
}

func TestSlotStageGzipTrailerCRC32AndSingleStreamMeter(t *testing.T) {
	newTestStore := func(t *testing.T) *SlotStore {
		t.Helper()
		root := t.TempDir()
		registerSlotTempCleanup(t, root)
		cpampRoot := filepath.Join(root, "cpamp")
		slotsDir := filepath.Join(cpampRoot, "slots")
		statePath := filepath.Join(cpampRoot, "slot-state.json")
		return NewSlotStoreWithOwner(cpampRoot, slotsDir, statePath, 1, os.Geteuid(), false)
	}

	paddedArchive, paddedSHA := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version:                "v2.0.0-beta.2",
		TrailingTarZeroPadding: 8 * 1024,
	})

	t.Run("accepts valid signed tar.gz with 8 KiB trailing tar zero blocks", func(t *testing.T) {
		store := newTestStore(t)
		manifest, n, err := store.StageSlotArchive(
			bytes.NewReader(paddedArchive),
			"v2.0.0-beta.2",
			paddedSHA,
			int64(len(paddedArchive)),
			nil,
		)
		if err != nil {
			t.Fatalf("expected padded archive to succeed, got: %v", err)
		}
		if n != int64(len(paddedArchive)) || manifest.Version != "v2.0.0-beta.2" {
			t.Fatalf("unexpected staged result: n=%d want=%d ver=%s", n, len(paddedArchive), manifest.Version)
		}
	})

	t.Run("rejects tampered byte at -16 in 8 KiB zero-padded tar.gz with 422 archive_invalid", func(t *testing.T) {
		store := newTestStore(t)
		tampered := append([]byte(nil), paddedArchive...)
		tampered[len(tampered)-16] ^= 0xFF
		// Pass empty expectedOuterSHA256 and 0 expectedOuterSize so outer digest checks
		// cannot mask a missed gzip trailer / zero-padding verification.
		_, _, err := store.StageSlotArchive(
			bytes.NewReader(tampered),
			"v2.0.0-beta.2",
			"",
			0,
			nil,
		)
		var slotErr *SlotError
		if !errors.As(err, &slotErr) || slotErr.Code != SlotCodeArchiveInvalid || slotErr.HTTPStatus != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 archive_invalid on tampered[-16], got %v", err)
		}
	})

	t.Run("rejects 1 byte appended after gzip trailer even within single bufio read", func(t *testing.T) {
		store := newTestStore(t)
		withTrailingByte := append(append([]byte(nil), paddedArchive...), 0x00)
		_, _, err := store.StageSlotArchive(
			bytes.NewReader(withTrailingByte),
			"v2.0.0-beta.2",
			"",
			0,
			nil,
		)
		var slotErr *SlotError
		if !errors.As(err, &slotErr) || slotErr.Code != SlotCodeArchiveInvalid || slotErr.HTTPStatus != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 archive_invalid on 1 trailing byte after gzip trailer, got %v", err)
		}
	})

	t.Run("rejects non-zero byte or >64 KiB padding after tar EOF marker", func(t *testing.T) {
		baseArchive, _ := buildSignedSlotArchive(t, slotArchiveBuildOptions{
			Version: "v2.0.0-beta.2",
		})
		zr, err := gzip.NewReader(bytes.NewReader(baseArchive))
		if err != nil {
			t.Fatal(err)
		}
		rawTar, err := io.ReadAll(zr)
		if err != nil {
			t.Fatal(err)
		}
		_ = zr.Close()

		repackTarWithTail := func(tail []byte) []byte {
			var out bytes.Buffer
			zw := gzip.NewWriter(&out)
			if _, wErr := zw.Write(rawTar); wErr != nil {
				t.Fatal(wErr)
			}
			if _, wErr := zw.Write(tail); wErr != nil {
				t.Fatal(wErr)
			}
			if cErr := zw.Close(); cErr != nil {
				t.Fatal(cErr)
			}
			return out.Bytes()
		}

		nonZeroTail := make([]byte, 1024)
		nonZeroTail[512] = 0x7F
		store := newTestStore(t)
		_, _, err = store.StageSlotArchive(bytes.NewReader(repackTarWithTail(nonZeroTail)), "v2.0.0-beta.2", "", 0, nil)
		var slotErr *SlotError
		if !errors.As(err, &slotErr) || slotErr.Code != SlotCodeArchiveInvalid {
			t.Fatalf("expected archive_invalid on non-zero tail after tar EOF, got %v", err)
		}

		oversizedZeroTail := make([]byte, 64*1024+512)
		_, _, err = store.StageSlotArchive(bytes.NewReader(repackTarWithTail(oversizedZeroTail)), "v2.0.0-beta.2", "", 0, nil)
		if !errors.As(err, &slotErr) || slotErr.Code != SlotCodeArchiveInvalid {
			t.Fatalf("expected archive_invalid on >64 KiB zero tail after tar EOF, got %v", err)
		}
	})
}

func TestArbitrateColdStartBundledVsSlimCPAArtifactManifestAndRestoreFlags(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	store := NewSlotStoreWithOwner(fx.CPAMPRoot, fx.SlotsDir, fx.SlotStatePath, 1, os.Geteuid(), false)

	slotManifestJSON := `{"schemaVersion":1,"engine":"cpa","version":"8.9.99","artifactId":"sha256:` + strings.Repeat("f", 64) + `"}` + "\n"
	arch, sha := buildSignedSlotArchive(t, slotArchiveBuildOptions{
		Version: "v2.0.0-beta.2",
		Files: map[string][]byte{
			"share/cpamp/runtime/cpa-artifact.json": []byte(slotManifestJSON),
		},
	})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(arch), "v2.0.0-beta.2", sha, int64(len(arch)), nil); err != nil {
		t.Fatalf("stage slot: %v", err)
	}
	if err := store.WriteSlotState(SlotState{
		SchemaVersion:       1,
		State:               SlotStateCommitted,
		ActiveSlot:          "v2.0.0-beta.2",
		BaseVersionAtSwitch: "v2.0.0-beta.1",
		SchemaGeneration:    1,
		UpdatedAt:           time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	baseInfo, err := ResolveBaseInstallation(LauncherCLIOptions{Mode: LauncherModeNative, HomeDir: fx.HomeDir}, os.Geteuid(), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	slotRoot := filepath.Join(fx.SlotsDir, "v2.0.0-beta.2")
	slotLauncherExe := filepath.Join(slotRoot, "bin", "cpamp-launcher")

	// 1. Bundled CPA executable present (docker-full / native): keep Base.CPAArtifactManifestPath!
	bundledPlan, err := ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        baseInfo,
		Store:       store,
		DataDir:     fx.DataDir,
		ManagerUID:  os.Geteuid(),
		SelfExePath: slotLauncherExe,
		Args:        []string{slotLauncherExe, "--mode=native", "--home=" + fx.HomeDir},
		Environ:     []string{"CPAMP_ACTIVE_SLOT=v2.0.0-beta.2"},
		LogOut:      io.Discard,
	})
	if err != nil {
		t.Fatalf("ArbitrateColdStart bundled failed: %v", err)
	}
	if bundledPlan.CPAArtifactManifestPath != baseInfo.CPAArtifactManifestPath {
		t.Fatalf("bundled CPAArtifactManifestPath = %q, want base %q", bundledPlan.CPAArtifactManifestPath, baseInfo.CPAArtifactManifestPath)
	}

	// 2. Slim mode (no bundled CPA binary at Base.CPAExecutable): use slot's cpa-artifact.json!
	slimBase := baseInfo
	slimBase.CPAExecutable = filepath.Join(fx.HomeDir, "bin", "nonexistent-cli-proxy-api")
	slimPlan, err := ArbitrateColdStart(ColdStartArbitrationInput{
		Base:        slimBase,
		Store:       store,
		DataDir:     fx.DataDir,
		ManagerUID:  os.Geteuid(),
		SelfExePath: slotLauncherExe,
		Args:        []string{slotLauncherExe, "--mode=native", "--home=" + fx.HomeDir},
		Environ:     []string{"CPAMP_ACTIVE_SLOT=v2.0.0-beta.2"},
		LogOut:      io.Discard,
	})
	if err != nil {
		t.Fatalf("ArbitrateColdStart slim failed: %v", err)
	}
	wantSlotManifest := filepath.Join(slotRoot, "share", "cpamp", "runtime", "cpa-artifact.json")
	if slimPlan.CPAArtifactManifestPath != wantSlotManifest {
		t.Fatalf("slim CPAArtifactManifestPath = %q, want slot %q", slimPlan.CPAArtifactManifestPath, wantSlotManifest)
	}

	// 3. Recovery CLI flag formatting when SnapshotDir is empty uses --latest --confirm.
	if got := formatRestorePreMigrateCommand(""); got != "cpa-manager-plus restore-pre-migrate --latest --confirm" {
		t.Fatalf("formatRestorePreMigrateCommand(\"\") = %q", got)
	}
	if got := formatRestorePreMigrateCommand("/var/lib/cpamp/backups/snap-1"); got != "cpa-manager-plus restore-pre-migrate --snapshot-dir /var/lib/cpamp/backups/snap-1 --confirm" {
		t.Fatalf("formatRestorePreMigrateCommand(snap) = %q", got)
	}
}

func TestProcPIDListensOnTCPAddrAndNativePortPreflight(t *testing.T) {
	encodeV4 := func(ip string, port uint16) string {
		addr := netip.MustParseAddr(ip).As4()
		v := uint32(addr[0]) | uint32(addr[1])<<8 | uint32(addr[2])<<16 | uint32(addr[3])<<24
		return fmt.Sprintf("%08X:%04X", v, port)
	}
	encodeV6 := func(ip string, port uint16) string {
		addr := netip.MustParseAddr(ip).As16()
		var b strings.Builder
		for i := range 4 {
			off := i * 4
			v := uint32(addr[off]) | uint32(addr[off+1])<<8 | uint32(addr[off+2])<<16 | uint32(addr[off+3])<<24
			_, _ = fmt.Fprintf(&b, "%08X", v)
		}
		_, _ = fmt.Fprintf(&b, ":%04X", port)
		return b.String()
	}

	procRoot := t.TempDir()
	const pid = 4242
	fdDir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
	netDir := filepath.Join(procRoot, "net")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[55001]", filepath.Join(fdDir, "3")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[55002]", filepath.Join(fdDir, "4")); err != nil {
		t.Fatal(err)
	}

	tcpTable := strings.Join([]string{
		"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
		"   0: " + encodeV4("127.0.0.1", 18318) + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 55001 1 0000000000000000 100 0 0 10 0",
		"   1: " + encodeV4("127.0.0.1", 18318) + " " + encodeV4("127.0.0.1", 51234) + " 01 00000000:00000000 00:00000000 00000000     0        0 99991 1 0000000000000000 20 0 0 10 0",
		"   2: " + encodeV4("127.0.0.1", 18319) + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 88888 1 0000000000000000 100 0 0 10 0",
	}, "\n") + "\n"
	tcp6Table := strings.Join([]string{
		"  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode",
		"   0: " + encodeV6("::1", 18328) + " 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 55002 1 0000000000000000 100 0 0 10 0",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(netDir, "tcp"), []byte(tcpTable), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "tcp6"), []byte(tcp6Table), 0o644); err != nil {
		t.Fatal(err)
	}

	if !procPIDListensOnTCPAddr(procRoot, pid, "127.0.0.1:18318") {
		t.Fatal("expected pid to own TCP_LISTEN socket on 127.0.0.1:18318")
	}
	if !procPIDListensOnTCPAddr(procRoot, pid, "[::1]:18328") {
		t.Fatal("expected pid to own TCP_LISTEN socket on [::1]:18328")
	}
	if procPIDListensOnTCPAddr(procRoot, pid, "127.0.0.1:18319") {
		t.Fatal("expected unowned listener on 127.0.0.1:18319 to fail closed")
	}
	if procPIDListensOnTCPAddr(procRoot, pid, "127.0.0.1:19999") {
		t.Fatal("expected absent listener on 127.0.0.1:19999 to return false")
	}

	// Foreign dual-stack wildcard listener on [::]:18318 must fail closed even if pid owns 127.0.0.1:18318!
	tcp6ForeignWildcard := tcp6Table + "   1: " + encodeV6("::", 18318) + " 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 77777 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(netDir, "tcp6"), []byte(tcp6ForeignWildcard), 0o644); err != nil {
		t.Fatal(err)
	}
	if procPIDListensOnTCPAddr(procRoot, pid, "127.0.0.1:18318") {
		t.Fatal("expected foreign [::]:18318 listener alongside child 127.0.0.1:18318 to fail closed")
	}

	// Native preflight port check rejects pre-existing listener and passes once closed.
	occupiedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	occupiedAddr := occupiedLn.Addr().String()
	freeAddr1 := reserveLoopbackAddr(t)
	freeAddr2 := reserveLoopbackAddr(t)
	freeAddr3 := reserveLoopbackAddr(t)
	paths := nativePathsAndAddrs{
		RuntimeAddr:        freeAddr1,
		ManagerAddr:        occupiedAddr,
		IngressPublicAddr:  freeAddr2,
		IngressControlAddr: freeAddr3,
	}
	if preflightErr := checkNativePortsAvailable(paths); preflightErr == nil || !strings.Contains(preflightErr.Error(), "manager") {
		_ = occupiedLn.Close()
		t.Fatalf("expected checkNativePortsAvailable to reject occupied manager port, got %v", preflightErr)
	}
	_ = occupiedLn.Close()
	if preflightErr := checkNativePortsAvailable(paths); preflightErr != nil {
		t.Fatalf("expected checkNativePortsAvailable to pass once listener closed, got %v", preflightErr)
	}

	// Runtime supervisor HTTP readiness probe checks Bearer auth on /v1/runtime/status and /health fallback on 404.
	const wantToken = "rt-secret-token-0123456789abcdef"
	var mode atomic.Int32 // 0 = 200 status, 1 = 404 status + 200 health, 2 = 401 status
	var healthSawAuth atomic.Bool
	supSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/runtime/status":
			if r.Header.Get("Authorization") != "Bearer "+wantToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			switch mode.Load() {
			case 0:
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"state":"running"}`))
			case 1:
				w.WriteHeader(http.StatusNotFound)
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		case "/health":
			if r.Header.Get("Authorization") != "" {
				healthSawAuth.Store(true)
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer supSrv.Close()
	supAddr := strings.TrimPrefix(supSrv.URL, "http://")
	probeClient := newLoopbackNoProxyHTTPClient(time.Second)

	if !probeRuntimeSupervisorHTTPReady(context.Background(), probeClient, supAddr, wantToken) {
		t.Fatal("expected /v1/runtime/status 200 with valid Bearer token to succeed")
	}
	if probeRuntimeSupervisorHTTPReady(context.Background(), probeClient, supAddr, "wrong-token") {
		t.Fatal("expected wrong Bearer token to fail readiness")
	}
	mode.Store(1)
	if !probeRuntimeSupervisorHTTPReady(context.Background(), probeClient, supAddr, wantToken) {
		t.Fatal("expected 404 on /v1/runtime/status to fall back to /health 200")
	}
	if healthSawAuth.Load() {
		t.Fatal("expected /health fallback probe not to forward Authorization bearer token")
	}
	mode.Store(2)
	if probeRuntimeSupervisorHTTPReady(context.Background(), probeClient, supAddr, wantToken) {
		t.Fatal("expected 503 on /v1/runtime/status to fail readiness")
	}
}

func newTestNativeSlotStore(fx subprocessNativeFixture) *SlotStore {
	store := NewSlotStoreWithOwner(
		fx.CPAMPRoot,
		fx.SlotsDir,
		fx.SlotStatePath,
		1,
		os.Geteuid(),
		false,
	)
	store.Keyring = map[string]ed25519.PublicKey{testSlotKeyID: testSlotPub}
	return store
}

func TestSlotControlActivateAttemptReservationAndValidation(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	store := newTestNativeSlotStore(fx)
	slotBeta2Archive, slotBeta2SHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.2", slotSubprocessBehavior{})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(slotBeta2Archive), "v2.0.0-beta.2", slotBeta2SHA, int64(len(slotBeta2Archive)), nil); err != nil {
		t.Fatal(err)
	}
	slotBeta3Archive, slotBeta3SHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.3", slotSubprocessBehavior{})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(slotBeta3Archive), "v2.0.0-beta.3", slotBeta3SHA, int64(len(slotBeta3Archive)), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteSlotState(SlotState{
		SchemaVersion:            1,
		State:                    SlotStateCommitted,
		ActiveSlot:               "v2.0.0-beta.2",
		BaseVersionAtSwitch:      "v2.0.0-beta.1",
		LastAttemptID:            "attempt-0000000000000001",
		SchemaGeneration:         1,
		PreviousSchemaGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}
	snapDir := writeVerifiedSnapshotDir(t, fx.DataDir, "pre-slot-attempt-reserve", []byte("db-clean"), []byte("key-clean"))
	baseInfo, err := ResolveBaseInstallation(LauncherCLIOptions{Mode: LauncherModeNative, HomeDir: fx.HomeDir}, os.Geteuid(), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.Repeat("c", 64)
	srv, err := StartSlotControlServer(SlotControlConfig{
		ListenAddr:    "127.0.0.1:0",
		Mode:          LauncherModeNative,
		LauncherToken: tok,
		Base:          baseInfo,
		RunningSlot:   "v2.0.0-beta.2",
		DataDir:       fx.DataDir,
		ManagerUID:    os.Geteuid(),
		TrialTimeout:  60 * time.Second,
		Store:         store,
	})
	if err != nil {
		t.Fatalf("StartSlotControlServer failed: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}()
	baseURL := "http://" + srv.BoundAddr()

	postActivate := func(body map[string]any) (*http.Response, error) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/launcher/slot/activate", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		return http.DefaultClient.Do(req)
	}

	for _, badAttemptID := range []string{
		"att-123",
		"attempt-0123456789ABCDEF",
		"attempt-short",
		" attempt-0123456789abcdef",
		"attempt-0123456789abcdeg",
	} {
		resp, err := postActivate(map[string]any{
			"target_version":             "v2.0.0-beta.3",
			"snapshot_dir":               snapDir,
			"previous_schema_generation": 1,
			"attempt_id":                 badAttemptID,
		})
		if err != nil {
			t.Fatalf("bad attempt_id %q request error: %v", badAttemptID, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), SlotCodeInvalidRequest) {
			t.Fatalf("expected 400 invalid_request for %q, got %d (%s)", badAttemptID, resp.StatusCode, string(raw))
		}
	}

	// Simulate dropping the TCP connection right AFTER s.pendingSwitch is reserved under s.mu
	// and BEFORE HTTP 202 is written OR slot-state.json on disk is updated!
	srv.afterActivateReserveHook = func(w http.ResponseWriter) bool {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("expected http.Hijacker")
			return false
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack failed: %v", err)
			return false
		}
		_ = conn.Close()
		return true
	}

	const wantAttemptID = "attempt-0123456789abcdef"
	_, dropErr := postActivate(map[string]any{
		"target_version":             "v2.0.0-beta.3",
		"snapshot_dir":               snapDir,
		"previous_schema_generation": 1,
		"attempt_id":                 wantAttemptID,
	})
	if dropErr == nil {
		t.Fatal("expected dropped 202 response to fail client HTTP read")
	}

	// slot-state.json on disk is STILL committed (WriteSwitchSlotState has NOT run yet),
	// yet GET /v1/launcher/slot/status serialized under s.mu MUST immediately expose wantAttemptID!
	diskSt, _, err := store.ReadSlotStateIfExists()
	if err != nil || diskSt.State != SlotStateCommitted {
		t.Fatalf("expected on-disk slot-state still committed before drain, got %+v err=%v", diskSt, err)
	}

	statusReq, _ := http.NewRequest(http.MethodGet, baseURL+"/v1/launcher/slot/status", nil)
	statusReq.Header.Set("Authorization", "Bearer "+tok)
	statusResp, err := http.DefaultClient.Do(statusReq)
	if err != nil {
		t.Fatalf("status probe after dropped 202 failed: %v", err)
	}
	var stResp slotStatusResponse
	if err := json.NewDecoder(statusResp.Body).Decode(&stResp); err != nil {
		_ = statusResp.Body.Close()
		t.Fatalf("decode status response: %v", err)
	}
	_ = statusResp.Body.Close()

	if stResp.State != SlotStateTrial || stResp.ActiveAttemptID != wantAttemptID || stResp.LastAttemptID != wantAttemptID || stResp.RollbackAvailable {
		t.Fatalf("expected pendingSwitch reservation immediately visible in status, got %+v", stResp)
	}
}

func TestShellQuotePOSIXArgAndRestorePreMigrateCommandWithSpaces(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	spacedDataDir := filepath.Join(fx.DataDir, "dir with spaces")
	if err := os.MkdirAll(spacedDataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	spacedSnapDir := writeVerifiedSnapshotDir(t, spacedDataDir, "pre slot '1' $var", []byte("db"), []byte("key"))

	if _, err := readValidatedManagerSnapshotManifest(spacedSnapDir, spacedDataDir, os.Geteuid()); err != nil {
		t.Fatalf("expected readValidatedManagerSnapshotManifest to accept snapshot dir with spaces via F_DUPFD_CLOEXEC, got %v", err)
	}

	gotCmd := formatRestorePreMigrateCommand(spacedSnapDir)
	wantQuoted := "'" + strings.ReplaceAll(spacedSnapDir, "'", `'\''`) + "'"
	wantCmd := "cpa-manager-plus restore-pre-migrate --snapshot-dir " + wantQuoted + " --confirm"
	if gotCmd != wantCmd {
		t.Fatalf("formatRestorePreMigrateCommand(%q)\n got: %s\nwant: %s", spacedSnapDir, gotCmd, wantCmd)
	}

	plainCmd := formatRestorePreMigrateCommand("/var/lib/cpamp/backups/pre-slot-v2.0.0")
	if plainCmd != "cpa-manager-plus restore-pre-migrate --snapshot-dir /var/lib/cpamp/backups/pre-slot-v2.0.0 --confirm" {
		t.Fatalf("expected safe path unquoted, got %s", plainCmd)
	}
	if latestCmd := formatRestorePreMigrateCommand("   "); latestCmd != "cpa-manager-plus restore-pre-migrate --latest --confirm" {
		t.Fatalf("unexpected fallback command: %s", latestCmd)
	}
}

func buildTrialSupervisorTestConfig(
	t *testing.T,
	fx subprocessNativeFixture,
	store *SlotStore,
	slotVersion string,
	holdLogFile string,
	stderr io.Writer,
) SupervisorConfig {
	t.Helper()
	slotBinDir := filepath.Join(fx.SlotsDir, slotVersion, "bin")
	baseBinDir := filepath.Join(fx.HomeDir, "bin")
	launcherToken := strings.Repeat("e", 64)
	if err := os.WriteFile(fx.LauncherTokenFile, []byte(launcherToken), 0o600); err != nil {
		t.Fatal(err)
	}
	commonEnv := append(os.Environ(),
		"CPAMP_NATIVE_HOME="+fx.HomeDir,
		"CPAMP_INGRESS_ADDR="+fx.IngressPublicAddr,
		"CPAMP_RUNTIME_ADDR="+fx.RuntimeAddr,
		"CPAMP_MANAGER_ADDR="+fx.ManagerAddr,
		"CPAMP_INGRESS_CONTROL_ADDR="+fx.IngressControlAddr,
		"CPAMP_RUNTIME_CPA_ADDR="+fx.CPAAddr,
		"CPAMP_LAUNCHER_CONTROL_ADDR="+fx.LauncherCtrlAddr,
		"CPAMP_BASE_VERSION=v2.0.0-beta.1",
		"CPAMP_BASE_INSTALL_ROOT="+fx.HomeDir,
		"CPAMP_RUNNING_SLOT="+slotVersion,
		"CPAMP_LAUNCHER_TOKEN_FILE="+fx.LauncherTokenFile,
	)
	mgrEnv := append(append([]string{}, commonEnv...),
		"HTTP_ADDR="+fx.ManagerAddr,
		"CPAMP_TRIAL_HOLD=1",
		"CPAMP_TEST_MGR_HOLD_LOG_FILE="+holdLogFile,
	)
	timings := (TimingConfig{
		InitialBackoff:            30 * time.Millisecond,
		MaxBackoff:                100 * time.Millisecond,
		TrialWatchdogTimeout:      5 * time.Second,
		TrialStabilizationWindow:  80 * time.Millisecond,
		DrainTimeout:              100 * time.Millisecond,
		ManagerShutdownTimeout:    300 * time.Millisecond,
		SupervisorShutdownTimeout: 300 * time.Millisecond,
	}).withDefaults(func(string) string { return "" })

	return SupervisorConfig{
		SupervisorSpec: ChildSpec{
			Name:       "supervisor",
			Prefix:     "[supervisor] ",
			Executable: filepath.Join(slotBinDir, "cpamp-runtime-supervisor"),
			Dir:        fx.DataDir,
			Env:        commonEnv,
		},
		ManagerSpec: ChildSpec{
			Name:       "manager",
			Prefix:     "[manager] ",
			Executable: filepath.Join(slotBinDir, "cpa-manager-plus"),
			Dir:        fx.DataDir,
			Env:        mgrEnv,
		},
		IngressSpec: ChildSpec{
			Name:       "ingress",
			Prefix:     "[ingress] ",
			Executable: filepath.Join(slotBinDir, "cpamp-ingress"),
			Dir:        fx.DataDir,
			Env:        commonEnv,
		},
		SupervisorTCPAddr:    fx.RuntimeAddr,
		ManagerHealthURL:     "http://" + fx.ManagerAddr + "/health",
		IngressPublicTCPAddr: NormalizeTCPProbeAddr(fx.IngressPublicAddr),
		IngressAdminTCPAddr:  fx.IngressControlAddr,
		Timings:              timings,
		Stdout:               stderr,
		Stderr:               stderr,
		SetCredentials:       false,
		SlotStore:            store,
		BaseInstallRoot:      fx.HomeDir,
		BaseVersion:          "v2.0.0-beta.1",
		ActiveSlot:           slotVersion,
		LauncherArgs:         []string{filepath.Join(slotBinDir, "cpamp-launcher"), "--mode=native", "--home=" + fx.HomeDir},
		BaseEnviron:          commonEnv,
		Trial: &TrialWatchdogConfig{
			Store:               store,
			ActiveSlot:          slotVersion,
			LauncherToken:       launcherToken,
			ManagerAddr:         fx.ManagerAddr,
			DataDir:             fx.DataDir,
			ManagerUID:          os.Geteuid(),
			ManagerGID:          os.Getegid(),
			RollbackLauncherExe: filepath.Join(baseBinDir, "cpamp-launcher"),
			RollbackManagerExe:  filepath.Join(baseBinDir, "cpa-manager-plus"),
			BaseInstallRoot:     fx.HomeDir,
			BaseVersion:         "v2.0.0-beta.1",
			LauncherArgs:        []string{filepath.Join(baseBinDir, "cpamp-launcher"), "--mode=native", "--home=" + fx.HomeDir},
			BaseEnviron:         commonEnv,
			TrialReleaseTimeout: time.Second,
		},
	}
}

func TestTrialCommitDirSyncFailureAfterRenameRestoresOriginalSnapshotAndKeepsHold(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	store := newTestNativeSlotStore(fx)

	slotArchive, slotSHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.2", slotSubprocessBehavior{})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(slotArchive), "v2.0.0-beta.2", slotSHA, int64(len(slotArchive)), nil); err != nil {
		t.Fatal(err)
	}
	snapDir := writeVerifiedSnapshotDir(t, fx.DataDir, "pre-commit-dir-sync-fail", []byte("clean-pre-trial-db"), []byte("clean-pre-trial-key"))
	dbPath := filepath.Join(fx.DataDir, "manager.db")
	keyPath := filepath.Join(fx.DataDir, "secret.bin")
	if err := os.WriteFile(dbPath, []byte("mutated-during-migration-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("mutated-during-migration-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	const wantAttempt = "attempt-feedfacecafebeef"
	if err := store.WriteSlotState(SlotState{
		SchemaVersion:            1,
		State:                    SlotStateTrial,
		ActiveSlot:               "v2.0.0-beta.2",
		PreviousSlot:             "",
		BaseVersionAtSwitch:      "v2.0.0-beta.1",
		TrialID:                  "trial-sync-fail",
		TrialBootCount:           1,
		ActiveAttemptID:          wantAttempt,
		LastAttemptID:            wantAttempt,
		SnapshotDir:              snapDir,
		SchemaGeneration:         2,
		PreviousSchemaGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}

	var failedOnce atomic.Bool
	var sawRenamedEmptySnapshot atomic.Bool
	store.afterRenameSyncHook = func() error {
		if failedOnce.CompareAndSwap(false, true) {
			// Verify os.Rename ALREADY replaced slot-state.json on disk with state="committed" and snapshot_dir="".
			onDisk, exists, rErr := store.ReadSlotStateIfExists()
			if rErr == nil && exists && onDisk.State == SlotStateCommitted && onDisk.SnapshotDir == "" {
				sawRenamedEmptySnapshot.Store(true)
			}
			return errors.New("simulated cpamp directory fsync EIO after rename")
		}
		return nil
	}

	holdLogFile := filepath.Join(t.TempDir(), "mgr-hold.log")
	logs := &lockedBufferWriter{buf: &bytes.Buffer{}}
	cfg := buildTrialSupervisorTestConfig(t, fx, store, "v2.0.0-beta.2", holdLogFile, logs)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tr, err := RunSupervisorWithSlotTransition(ctx, cfg)
	if err != nil {
		t.Fatalf("expected rollback ExecTransition on commit dir-sync failure, got err=%v\nlogs:\n%s", err, logs.String())
	}
	if tr == nil || !strings.HasSuffix(tr.Path, filepath.Join("bin", "cpamp-launcher")) {
		t.Fatalf("expected rollback transition to base launcher, got %+v", tr)
	}
	if !sawRenamedEmptySnapshot.Load() {
		t.Fatal("expected afterRenameSyncHook to run after os.Rename wrote committed state with empty SnapshotDir")
	}

	gotDB, err := os.ReadFile(dbPath)
	if err != nil || string(gotDB) != "clean-pre-trial-db" {
		t.Fatalf("expected rollback to recover initialTrialState.SnapshotDir and restore manager.db, got %q err=%v", string(gotDB), err)
	}
	gotKey, err := os.ReadFile(keyPath)
	if err != nil || string(gotKey) != "clean-pre-trial-key" {
		t.Fatalf("expected rollback to restore secret.bin, got %q err=%v", string(gotKey), err)
	}

	finalSt, exists, err := store.ReadSlotStateIfExists()
	if err != nil || !exists {
		t.Fatalf("read final slot-state: exists=%t err=%v", exists, err)
	}
	if finalSt.State != SlotStateRolledBack ||
		finalSt.LastFailureCode != SlotCodeInternalError ||
		finalSt.LastAttemptID != wantAttempt ||
		finalSt.SchemaGeneration != 1 ||
		finalSt.ActiveSlot != "" {
		t.Fatalf("unexpected final rolled_back state after commit fsync failure: %+v", finalSt)
	}

	holdLogRaw, err := os.ReadFile(holdLogFile)
	if err != nil || len(strings.TrimSpace(string(holdLogRaw))) == 0 {
		t.Fatalf("expected manager hold log entries, got %q err=%v", string(holdLogRaw), err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(holdLogRaw)), "\n") {
		if !strings.HasSuffix(line, "hold=1") {
			t.Fatalf("expected every manager child launch to keep CPAMP_TRIAL_HOLD=1, got %q", line)
		}
	}
}

func TestTrialChildExitBeforeDurableCommitKeepsTrialHoldAndRollsBack(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	store := newTestNativeSlotStore(fx)

	slotArchive, slotSHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.2", slotSubprocessBehavior{})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(slotArchive), "v2.0.0-beta.2", slotSHA, int64(len(slotArchive)), nil); err != nil {
		t.Fatal(err)
	}

	snapDir := writeVerifiedSnapshotDir(t, fx.DataDir, "pre-commit-child-exit", []byte("clean-pre-exit-db"), []byte("clean-pre-exit-key"))
	dbPath := filepath.Join(fx.DataDir, "manager.db")
	keyPath := filepath.Join(fx.DataDir, "secret.bin")
	if err := os.WriteFile(dbPath, []byte("dirty-pre-exit-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("dirty-pre-exit-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	const wantAttempt = "attempt-0011223344556677"
	if err := store.WriteSlotState(SlotState{
		SchemaVersion:            1,
		State:                    SlotStateTrial,
		ActiveSlot:               "v2.0.0-beta.2",
		PreviousSlot:             "",
		BaseVersionAtSwitch:      "v2.0.0-beta.1",
		TrialID:                  "trial-child-exit",
		TrialBootCount:           1,
		ActiveAttemptID:          wantAttempt,
		LastAttemptID:            wantAttempt,
		SnapshotDir:              snapDir,
		SchemaGeneration:         2,
		PreviousSchemaGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}

	holdLogFile := filepath.Join(t.TempDir(), "mgr-restart-hold.log")
	logs := &lockedBufferWriter{buf: &bytes.Buffer{}}
	cfg := buildTrialSupervisorTestConfig(t, fx, store, "v2.0.0-beta.2", holdLogFile, logs)

	var latestMgrPID atomic.Int64
	cfg.OnChildStart = func(name string, pid int) {
		if name == "manager" {
			latestMgrPID.Store(int64(pid))
		}
	}
	cfg.beforeTrialCommit = func() {
		pid := int(latestMgrPID.Load())
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := queryManagerHealthMap(fx.ManagerAddr); !ok {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		// Give supervisor loop a brief window past InitialBackoff (30ms) so any
		// restart attempt before shutdown would expose missing CPAMP_TRIAL_HOLD.
		time.Sleep(80 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tr, err := RunSupervisorWithSlotTransition(ctx, cfg)
	if err != nil {
		t.Fatalf("expected rollback ExecTransition when manager exited before durable commit, got err=%v\nlogs:\n%s", err, logs.String())
	}
	if tr == nil {
		t.Fatal("expected non-nil rollback ExecTransition")
	}

	holdLogRaw, err := os.ReadFile(holdLogFile)
	if err != nil {
		t.Fatalf("read manager hold log: %v", err)
	}
	trimmedHoldLog := strings.TrimSpace(string(holdLogRaw))
	if trimmedHoldLog == "" {
		t.Fatal("expected at least one manager start recorded in hold log")
	}
	for line := range strings.SplitSeq(trimmedHoldLog, "\n") {
		if !strings.HasSuffix(line, "hold=1") {
			t.Fatalf("expected every manager child launch before durable commit to keep CPAMP_TRIAL_HOLD=1, got %q", line)
		}
	}
	gotDB, err := os.ReadFile(dbPath)
	if err != nil || string(gotDB) != "clean-pre-exit-db" {
		t.Fatalf("expected manager.db restored after pre-commit child exit, got %q err=%v", string(gotDB), err)
	}
	gotKey, err := os.ReadFile(keyPath)
	if err != nil || string(gotKey) != "clean-pre-exit-key" {
		t.Fatalf("expected secret.bin restored after pre-commit child exit, got %q err=%v", string(gotKey), err)
	}
	finalSt, exists, err := store.ReadSlotStateIfExists()
	if err != nil || !exists || finalSt.State != SlotStateRolledBack || finalSt.LastFailureCode != FailureCodeManagerStartupFailed {
		t.Fatalf("expected rolled_back with manager_startup_failed, got exists=%t err=%v state=%+v", exists, err, finalSt)
	}
}

func TestSlotControlAbortAttemptTombstonesSlowActivate(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	store := newTestNativeSlotStore(fx)
	slotBeta2Archive, slotBeta2SHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.2", slotSubprocessBehavior{})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(slotBeta2Archive), "v2.0.0-beta.2", slotBeta2SHA, int64(len(slotBeta2Archive)), nil); err != nil {
		t.Fatal(err)
	}
	slotBeta3Archive, slotBeta3SHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.3", slotSubprocessBehavior{})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(slotBeta3Archive), "v2.0.0-beta.3", slotBeta3SHA, int64(len(slotBeta3Archive)), nil); err != nil {
		t.Fatal(err)
	}
	const committedAttemptID = "attempt-0000000000000001"
	if err := store.WriteSlotState(SlotState{
		SchemaVersion:            1,
		State:                    SlotStateCommitted,
		ActiveSlot:               "v2.0.0-beta.2",
		BaseVersionAtSwitch:      "v2.0.0-beta.1",
		LastAttemptID:            committedAttemptID,
		SchemaGeneration:         1,
		PreviousSchemaGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}
	snapDir := writeVerifiedSnapshotDir(t, fx.DataDir, "pre-slot-slow-validate", []byte("db-clean"), []byte("key-clean"))
	baseInfo, err := ResolveBaseInstallation(LauncherCLIOptions{Mode: LauncherModeNative, HomeDir: fx.HomeDir}, os.Geteuid(), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	tok := strings.Repeat("d", 64)
	srv, err := StartSlotControlServer(SlotControlConfig{
		ListenAddr:    "127.0.0.1:0",
		Mode:          LauncherModeNative,
		LauncherToken: tok,
		Base:          baseInfo,
		RunningSlot:   "v2.0.0-beta.2",
		DataDir:       fx.DataDir,
		ManagerUID:    os.Geteuid(),
		TrialTimeout:  60 * time.Second,
		Store:         store,
	})
	if err != nil {
		t.Fatalf("StartSlotControlServer failed: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}()
	baseURL := "http://" + srv.BoundAddr()

	postAbort := func(attemptID string) (*http.Response, slotAbortAttemptResponse) {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"attempt_id": attemptID})
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/launcher/slot/abort-attempt", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, reqErr := http.DefaultClient.Do(req)
		if reqErr != nil {
			t.Fatalf("abort-attempt request failed: %v", reqErr)
		}
		var out slotAbortAttemptResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		_ = resp.Body.Close()
		return resp, out
	}

	// Already-recorded LastAttemptID reports outcome="accepted".
	if resp, out := postAbort(committedAttemptID); resp.StatusCode != http.StatusOK || out.Outcome != SlotAbortOutcomeAccepted {
		t.Fatalf("expected committed LastAttemptID to report accepted, got status=%d out=%+v", resp.StatusCode, out)
	}

	// Slow snapshot validation window: client times out and calls abort-attempt BEFORE s.pendingSwitch is reserved.
	enteredHook := make(chan struct{})
	releaseHook := make(chan struct{})
	srv.mu.Lock()
	srv.beforeActivateReserveHook = func() {
		close(enteredHook)
		<-releaseHook
	}
	srv.mu.Unlock()

	const slowAttemptID = "attempt-abcdef0123456789"
	actErrCh := make(chan error, 1)
	go func() {
		actCtx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		raw, _ := json.Marshal(map[string]any{
			"target_version":             "v2.0.0-beta.3",
			"snapshot_dir":               snapDir,
			"previous_schema_generation": 1,
			"attempt_id":                 slowAttemptID,
		})
		req, _ := http.NewRequestWithContext(actCtx, http.MethodPost, baseURL+"/v1/launcher/slot/activate", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, reqErr := http.DefaultClient.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		actErrCh <- reqErr
	}()

	<-enteredHook
	if actErr := <-actErrCh; actErr == nil {
		t.Fatal("expected slow activate request to time out on client side")
	}

	// Tombstone slowAttemptID while validation is still running in the background.
	if resp, out := postAbort(slowAttemptID); resp.StatusCode != http.StatusOK || out.Outcome != SlotAbortOutcomeAborted || out.AttemptID != slowAttemptID {
		t.Fatalf("expected abort-attempt outcome=aborted, got status=%d out=%+v", resp.StatusCode, out)
	}

	// Let slow validation finish: handleActivate must refuse to accept slowAttemptID and must release s.busy!
	close(releaseHook)
	srv.mu.Lock()
	srv.beforeActivateReserveHook = nil
	srv.mu.Unlock()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		srv.mu.Lock()
		busy := srv.busy
		srv.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case sw := <-srv.SwitchChan():
		t.Fatalf("aborted slow attempt must NEVER queue switch action: %+v", sw)
	default:
	}

	// Even a brand-new uncanceled POST /activate carrying slowAttemptID is terminally barred!
	rawRetry, _ := json.Marshal(map[string]any{
		"target_version":             "v2.0.0-beta.3",
		"snapshot_dir":               snapDir,
		"previous_schema_generation": 1,
		"attempt_id":                 slowAttemptID,
	})
	retryReq, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/launcher/slot/activate", bytes.NewReader(rawRetry))
	retryReq.Header.Set("Authorization", "Bearer "+tok)
	retryReq.Header.Set("Content-Type", "application/json")
	retryResp, err := http.DefaultClient.Do(retryReq)
	if err != nil {
		t.Fatalf("retry activate failed: %v", err)
	}
	retryBody, _ := io.ReadAll(retryResp.Body)
	_ = retryResp.Body.Close()
	if retryResp.StatusCode != http.StatusConflict || !strings.Contains(string(retryBody), SlotCodeSlotConflict) {
		t.Fatalf("expected 409 %s on re-sent tombstoned attempt, got %d body=%s", SlotCodeSlotConflict, retryResp.StatusCode, string(retryBody))
	}
}

func TestTrialChildExitBlockingPipeDrainAfterSoakDoesNotCommitAndRollsBack(t *testing.T) {
	fx := setupSubprocessNativeFixture(t, "v2.0.0-beta.1")
	store := newTestNativeSlotStore(fx)

	slotArchive, slotSHA := buildRealBinarySlotArchive(t, fx.TestSelfExe, "v2.0.0-beta.2", slotSubprocessBehavior{})
	if _, _, err := store.StageSlotArchive(bytes.NewReader(slotArchive), "v2.0.0-beta.2", slotSHA, int64(len(slotArchive)), nil); err != nil {
		t.Fatal(err)
	}

	snapDir := writeVerifiedSnapshotDir(t, fx.DataDir, "pre-commit-pipe-drain", []byte("clean-pipe-drain-db"), []byte("clean-pipe-drain-key"))
	dbPath := filepath.Join(fx.DataDir, "manager.db")
	keyPath := filepath.Join(fx.DataDir, "secret.bin")
	if err := os.WriteFile(dbPath, []byte("dirty-pipe-drain-db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("dirty-pipe-drain-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	const wantAttempt = "attempt-7766554433221100"
	if err := store.WriteSlotState(SlotState{
		SchemaVersion:            1,
		State:                    SlotStateTrial,
		ActiveSlot:               "v2.0.0-beta.2",
		PreviousSlot:             "",
		BaseVersionAtSwitch:      "v2.0.0-beta.1",
		TrialID:                  "trial-pipe-drain-exit",
		TrialBootCount:           1,
		ActiveAttemptID:          wantAttempt,
		LastAttemptID:            wantAttempt,
		SnapshotDir:              snapDir,
		SchemaGeneration:         2,
		PreviousSchemaGeneration: 1,
	}); err != nil {
		t.Fatal(err)
	}

	holdLogFile := filepath.Join(t.TempDir(), "mgr-pipe-drain-hold.log")
	logs := &lockedBufferWriter{buf: &bytes.Buffer{}}
	cfg := buildTrialSupervisorTestConfig(t, fx, store, "v2.0.0-beta.2", holdLogFile, logs)
	// Keep ChildIODrainTimeout long enough that waitChildIODone is guaranteed to remain blocked
	// on the background sleeper's inherited stdout FD throughout persistCommittedTrialState!
	cfg.Timings.ChildIODrainTimeout = 450 * time.Millisecond

	var sleeperPID atomic.Int64
	t.Cleanup(func() {
		if pid := int(sleeperPID.Load()); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	cfg.beforeTrialCommit = func() {
		req, _ := http.NewRequest(http.MethodPost, "http://"+fx.ManagerAddr+"/__test/spawn-sleeper-and-exit-zero", nil)
		resp, reqErr := http.DefaultClient.Do(req)
		if reqErr != nil {
			t.Errorf("trigger spawn-sleeper-and-exit-zero: %v", reqErr)
			return
		}
		var out struct {
			SleeperPID int `json:"sleeper_pid"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		_ = resp.Body.Close()
		sleeperPID.Store(int64(out.SleeperPID))

		// Wait only until the manager process itself closes its listener and exits (a few ms),
		// well before ChildIODrainTimeout (450ms) expires, so drainPipes/waitChildIODone is actively blocked!
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := queryManagerHealthMap(fx.ManagerAddr); !ok {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(15 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tr, err := RunSupervisorWithSlotTransition(ctx, cfg)
	if err != nil {
		t.Fatalf("expected rollback ExecTransition when child exited with open inherited stdout FD, got err=%v\nlogs:\n%s", err, logs.String())
	}
	if tr == nil {
		t.Fatal("expected non-nil rollback ExecTransition")
	}

	holdLogRaw, err := os.ReadFile(holdLogFile)
	if err != nil {
		t.Fatalf("read manager hold log: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(holdLogRaw)), "\n") {
		if !strings.HasSuffix(line, "hold=1") {
			t.Fatalf("expected CPAMP_TRIAL_HOLD=1 preserved across child exit during pipe drain, got %q", line)
		}
	}
	gotDB, err := os.ReadFile(dbPath)
	if err != nil || string(gotDB) != "clean-pipe-drain-db" {
		t.Fatalf("expected manager.db restored on rollback, got %q err=%v", string(gotDB), err)
	}
	gotKey, err := os.ReadFile(keyPath)
	if err != nil || string(gotKey) != "clean-pipe-drain-key" {
		t.Fatalf("expected secret.bin restored on rollback, got %q err=%v", string(gotKey), err)
	}
	finalSt, exists, err := store.ReadSlotStateIfExists()
	if err != nil || !exists || finalSt.State != SlotStateRolledBack || finalSt.LastFailureCode != FailureCodeManagerStartupFailed {
		t.Fatalf("expected rolled_back with manager_startup_failed, got exists=%t err=%v state=%+v", exists, err, finalSt)
	}
}

func TestResolveBaseInstallationAcceptsDevAndCIBaseProvenanceLabels(t *testing.T) {
	installRoot := t.TempDir()
	shareDir := filepath.Join(installRoot, "usr", "local", "share", "cpamp")
	releaseDir := filepath.Join(shareDir, "release")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	abiJSON := fmt.Sprintf("{\"schema_version\":1,\"base_epoch\":1,\"platform\":%q}\n", ExpectedSlotPlatform())
	if err := os.WriteFile(filepath.Join(releaseDir, "base-abi.json"), []byte(abiJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, label := range []string{"ci", "dev", "nightly-20261126"} {
		prov := fmt.Sprintf("{\"version\":%q}\n", label)
		if err := os.WriteFile(filepath.Join(shareDir, "provenance.json"), []byte(prov), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := ResolveBaseInstallation(LauncherCLIOptions{Mode: LauncherModeDocker}, os.Geteuid(), func(k string) string {
			if k == "CPAMP_BASE_INSTALL_ROOT" {
				return installRoot
			}
			return ""
		})
		if err != nil {
			t.Fatalf("ResolveBaseInstallation(%q) unexpected error: %v", label, err)
		}
		if info.BaseVersion != label {
			t.Fatalf("ResolveBaseInstallation(%q).BaseVersion = %q, want %q", label, info.BaseVersion, label)
		}
	}

	for _, invalid := range []string{"", "unknown", "v2.0", "2.0", "../bad"} {
		prov := fmt.Sprintf("{\"version\":%q}\n", invalid)
		if err := os.WriteFile(filepath.Join(shareDir, "provenance.json"), []byte(prov), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveBaseInstallation(LauncherCLIOptions{Mode: LauncherModeDocker}, os.Geteuid(), func(k string) string {
			if k == "CPAMP_BASE_INSTALL_ROOT" {
				return installRoot
			}
			return ""
		}); err == nil {
			t.Fatalf("ResolveBaseInstallation(%q) unexpectedly succeeded", invalid)
		}
	}
}
