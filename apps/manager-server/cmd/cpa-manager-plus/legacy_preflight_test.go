package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
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

func TestBlockedLegacyNormalStartupHasNoSourceSideEffects(t *testing.T) {
	for _, kind := range []string{"legacy", "unknown", "corrupt", "orphan-wal", "old-config-only"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "usage.sqlite")
			writeOwnedProvisioningConfig(t, dir)
			switch kind {
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
			case "orphan-wal":
				if err := os.WriteFile(path+"-wal", []byte("owned-orphan"), 0600); err != nil {
					t.Fatal(err)
				}
			case "old-config-only":
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"dataDir":".","cpaUpstreamUrl":"https://owned.invalid"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := managerSourceSnapshot(t, dir)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagerServerHelperProcess$")
			cmd.Env = ownedPreflightEnvironment(dir, path)
			output, err := cmd.CombinedOutput()
			if err == nil || ctx.Err() != nil || strings.Contains(string(output), "listening on") {
				t.Fatalf("unsafe startup: err=%v output=%s", err, output)
			}
			if !reflect.DeepEqual(before, managerSourceSnapshot(t, dir)) {
				t.Fatal("blocked startup changed source/config/key/lock")
			}
		})
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
	first := startManagerServerProcess(t, dir, path)
	assertManagerEndpoint(t, first.addr, "/usage-service/info")
	first.stop(t)
	if _, err := os.Stat(filepath.Join(dir, "data.key")); err != nil {
		t.Fatal("fresh native key was not created")
	}
	second := startManagerServerProcess(t, dir, path)
	assertManagerEndpoint(t, second.addr, "/usage-service/info")
	second.stop(t)
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
