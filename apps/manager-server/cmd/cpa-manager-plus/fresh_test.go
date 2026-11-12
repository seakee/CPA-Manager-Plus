package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func freshClaimEnvironment(dir, path string) []string {
	env := ownedPreflightEnvironment(dir, path)
	for i, entry := range env {
		if strings.HasPrefix(entry, "CPA_MANAGER_ADMIN_KEY=") {
			env[i] = "CPA_MANAGER_ADMIN_KEY="
		}
	}
	return append(env, "CPA_MANAGER_ADMIN_KEY_FILE=")
}

func freshRequest(t *testing.T, addr, resource, token string, body any, out any) int {
	t.Helper()
	var data bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&data).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	method := http.MethodPost
	if resource == "operation" {
		method = http.MethodGet
	}
	req, err := http.NewRequest(method, "http://"+addr+bootstrap.FixedBootstrapAPIPath+resource, &data)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Fresh "+token)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil && res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	} else {
		_, _ = io.Copy(io.Discard, res.Body)
	}
	return res.StatusCode
}

func waitFreshClaim(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		status, body := waitForManagerResponse(t, addr, http.MethodGet, bootstrap.FixedBootstrapDiscoveryPath)
		if status == http.StatusOK && strings.Contains(body, `"status":"fresh_claim_required"`) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("owned Fresh carrier did not activate")
}

func TestFreshHostInitializeActivatesExistingReadOnlyListenerAndResumes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	env := freshClaimEnvironment(dir, path)
	server := startManagerServerProcessWithEnvironment(t, env)
	defer func() {
		if server != nil {
			server.stop(t)
		}
	}()
	if status, _ := waitForManagerResponse(t, server.addr, http.MethodGet, "/health"); status != http.StatusServiceUnavailable {
		t.Fatalf("uninitialized Manager health = %d", status)
	}
	handoff := runFreshInitializeProcess(t, env)
	waitFreshClaim(t, server.addr)
	data, err := base64.RawURLEncoding.DecodeString(strings.SplitN(handoff, "#handoff=", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	var claim fresh.ExchangeRequest
	if json.Unmarshal(data, &claim) != nil {
		t.Fatal("invalid host handoff")
	}
	var session fresh.SessionResult
	if freshRequest(t, server.addr, "claim/exchange", "", claim, &session) != http.StatusOK {
		t.Fatal("claim exchange failed")
	}
	if status, body := waitForManagerResponse(t, server.addr, http.MethodGet, "/health"); status != http.StatusOK || !strings.Contains(body, `"service":"cpa-manager-plus"`) {
		t.Fatalf("Fresh takeover health = %d: %s", status, body)
	}
	weakCommit := fresh.AdminCommitRequest{Identity: session.Operation.Identity, CommitID: "weak-commit", Key: "too-short"}
	if status := freshRequest(t, server.addr, "admin/commit", session.Token, weakCommit, nil); status != http.StatusBadRequest {
		t.Fatalf("weak admin key commit status = %d, want 400", status)
	}
	var generated struct {
		Key string `json:"key"`
	}
	if freshRequest(t, server.addr, "admin/generate", session.Token, nil, &generated) != http.StatusOK || !strings.HasPrefix(generated.Key, "cpamp_") {
		t.Fatal("admin key generation failed")
	}
	key := "Cpamp_Fresh_Manual_Admin_Secret_839216"
	commit := fresh.AdminCommitRequest{Identity: session.Operation.Identity, CommitID: "lost-response-commit", Key: key}
	var op bootstrap.FreshOperation
	if freshRequest(t, server.addr, "admin/commit", session.Token, commit, &op) != http.StatusOK || !op.AdminInitialized {
		t.Fatal("admin commit failed")
	}
	if freshRequest(t, server.addr, "admin/generate", session.Token, nil, nil) == http.StatusOK {
		t.Fatal("initialized session retained admin generation capability")
	}
	assertFreshPendingDiscovery(t, server.addr)
	if freshRequest(t, server.addr, "admin/commit", session.Token, commit, &op) != http.StatusOK {
		t.Fatal("lost response did not reconcile")
	}
	server.stop(t)
	server = nil
	server = startManagerServerProcessWithEnvironment(t, env)
	// The listener intentionally starts before fenced ownership verification.
	// Reconcile this safe read only after the same Fresh carrier is installed.
	deadline := time.Now().Add(6 * time.Second)
	resumed := false
	for time.Now().Before(deadline) {
		if freshRequest(t, server.addr, "operation", session.Token, nil, &op) == http.StatusOK {
			resumed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !resumed || op.Identity != claim.Identity || !op.AdminInitialized {
		t.Fatal("restart lost same-operation continuation")
	}
	if status, _ := waitForManagerResponse(t, server.addr, http.MethodGet, "/health"); status != http.StatusOK {
		t.Fatalf("resumed Fresh health = %d", status)
	}
	if freshRequest(t, server.addr, "claim/exchange", "", claim, nil) == http.StatusOK {
		t.Fatal("restart reopened claim")
	}
	logs := server.logs.String()
	for _, secret := range []string{claim.Token, session.Token, key, "#handoff="} {
		if strings.Contains(logs, secret) {
			t.Fatal("Fresh secret entered service log")
		}
	}
}

func TestFreshStartupRetriesOriginalAdmissionAfterInitializerReleasesFence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	paths := legacypreflight.DefaultPaths(path)
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(context.Background(), paths, fresh.OpenOptions{Initialize: true})
	if err != nil {
		t.Fatal(err)
	}
	id := opened.Record.Operation.Identity
	_, err = fresh.New(freshstore.New(opened.DB)).Issue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	writeOwnedProvisioningConfig(t, dir)
	server := startManagerServerProcessWithEnvironment(t, freshClaimEnvironment(dir, path))
	defer func() {
		if server != nil {
			server.stop(t)
		}
	}()
	// The real listener starts while the host admission is still fenced.
	status, _ := waitForManagerResponse(t, server.addr, http.MethodGet, bootstrap.FixedBootstrapDiscoveryPath)
	if status != http.StatusServiceUnavailable {
		t.Fatal("held admission exposed authority")
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	waitFreshClaim(t, server.addr)
	// Inspect only after shutting down the holder; identity must not regenerate.
	server.stop(t)
	server = nil
	reopened, err := fresh.Open(context.Background(), paths, fresh.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Record.Operation.Identity != id {
		t.Fatal("startup regenerated admission")
	}
}

func TestFreshConfiguredButUnverifiableHostAuthorityCannotOpenClaim(t *testing.T) {
	for _, kind := range []string{"missing-file", "empty-file", "invalid-key"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "usage.sqlite")
			writeOwnedProvisioningConfig(t, dir)
			for _, entry := range freshClaimEnvironment(dir, path) {
				key, value, _ := strings.Cut(entry, "=")
				t.Setenv(key, value)
			}
			secretPath := filepath.Join(dir, "host-admin")
			t.Setenv("CPA_MANAGER_ADMIN_KEY_FILE", secretPath)
			if kind == "empty-file" {
				if err := os.WriteFile(secretPath, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "invalid-key" {
				t.Setenv("CPA_MANAGER_ADMIN_KEY", "weak")
			}
			var stdout bytes.Buffer
			if err := runFreshInitialize(t.Context(), &stdout); err == nil || stdout.Len() != 0 {
				t.Fatal("unverifiable host authority issued handoff")
			}
			if _, err := os.Stat(path); kind != "invalid-key" && !os.IsNotExist(err) {
				t.Fatal("unverifiable host authority created DB")
			}
		})
	}
}

func TestFreshNewInstallationPreparesEmptyTableDerivedIndexesBeforeInitialKeyBinding(t *testing.T) {
	harness := newFreshUsageHarness(t)
	defer harness.cleanup()

	status, err := sqliterepo.ReadDerivedMaintenanceStatus(t.Context(), harness.db)
	if err != nil {
		t.Fatalf("read derived maintenance status: %v", err)
	}
	if status.Required || status.DeferredIndexes != 0 {
		t.Fatalf("fresh installation reported pending database maintenance after initial key binding: %+v", status)
	}
}

func TestFreshNewInstallationWiresDatabaseStatusAndCPAUpdateEndpoints(t *testing.T) {
	harness := newFreshUsageHarness(t)
	defer harness.cleanup()
	harness.app.runtime = &freshRuntimeTestClient{status: freshRuntimeReadyStatus()}

	statusRec := harness.call("GET", "cpamp/status", "")
	if statusRec.Code != http.StatusOK {
		t.Fatalf("GET /control/api/v2/cpamp/status status=%d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var statusPayload map[string]any
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusPayload); err != nil {
		t.Fatalf("decode status payload: %v", err)
	}
	dbSnapshot, ok := statusPayload["database"].(map[string]any)
	if !ok {
		t.Fatalf("expected database snapshot in /cpamp/status payload, got %+v", statusPayload)
	}
	if dbBytes, _ := dbSnapshot["databaseBytes"].(float64); dbBytes <= 0 {
		t.Fatalf("expected positive databaseBytes in /cpamp/status payload, got %+v", dbSnapshot)
	}
	checkpoint, ok := dbSnapshot["checkpoint"].(map[string]any)
	if !ok || checkpoint["mode"] != sqliterepo.WALCheckpointModeUnobserved {
		t.Fatalf("expected unobserved checkpoint mode in Fresh /cpamp/status payload, got %+v", dbSnapshot["checkpoint"])
	}

	updatesRec := harness.call("GET", "cpamp/usage-service/runtime/updates", "")
	if updatesRec.Code != http.StatusOK {
		t.Fatalf("GET /control/api/v2/cpamp/usage-service/runtime/updates status=%d body=%s", updatesRec.Code, updatesRec.Body.String())
	}
}

func runFreshInitializeFailure(t *testing.T, environment []string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagerServerHelperProcess$", "--", "initialize")
	command.Env = environment
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil || ctx.Err() != nil {
		t.Fatalf("expected non-zero initialize exit, err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	stderrLine := strings.TrimSpace(stderr.String())
	if len(stderrLine) > 20 && stderrLine[4] == '/' && stderrLine[7] == '/' && stderrLine[10] == ' ' && stderrLine[13] == ':' && stderrLine[16] == ':' && stderrLine[19] == ' ' {
		stderrLine = stderrLine[20:]
	}
	return stdout.String(), stderrLine
}

func TestFreshInitializeReportsCompletedAndActiveStatesDistinctFromOwnershipFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	env := ownedPreflightEnvironment(dir, path)

	runFreshInitializeProcess(t, env)
	paths := legacypreflight.DefaultPaths(path)
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	genericRefusal := freshInitializeErrorMessage(errors.New("unclassified"))
	stdoutLocked, stderrLocked := runFreshInitializeFailure(t, env)
	if stdoutLocked != "" ||
		strings.Contains(stderrLocked, "#handoff=") ||
		strings.Contains(stderrLocked, genericRefusal) ||
		!strings.Contains(stderrLocked, "initialize") ||
		strings.Contains(stderrLocked, "/console") {
		opened.Close()
		t.Fatalf("locked in-progress initialize output: stdout=%q stderr=%q", stdoutLocked, stderrLocked)
	}
	store := freshstore.New(opened.DB)
	record, err := store.Load(t.Context())
	if err != nil {
		opened.Close()
		t.Fatal(err)
	}
	updated, err := store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy:         bootstrap.NewManaged,
			TargetIdentity:   "managed-v8",
			TargetArtifactID: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			TargetGeneration: 1,
			Stage:            "committed",
		}
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: r.Operation.Identity.OperationID,
			Revision:    1,
			Target:      "/console",
			Stage:       bootstrap.PrefixCommitted,
			Committed:   true,
		}
		return nil
	})
	if err != nil {
		opened.Close()
		t.Fatal(err)
	}

	// 1. Active initial route (Routing.Committed && !ProductComplete) while live:
	stdoutActiveLive, stderrActiveLive := runFreshInitializeFailure(t, env)
	if stdoutActiveLive != "" ||
		strings.Contains(stderrActiveLive, "#handoff=") ||
		strings.Contains(stderrActiveLive, genericRefusal) ||
		!strings.Contains(stderrActiveLive, "/console") ||
		stderrActiveLive == stderrLocked {
		opened.Close()
		t.Fatalf("live active-route initialize output: stdout=%q stderr=%q", stdoutActiveLive, stderrActiveLive)
	}

	// 2. Active initial route while stopped:
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	stdoutActiveStopped, stderrActiveStopped := runFreshInitializeFailure(t, env)
	if stdoutActiveStopped != "" || stderrActiveStopped != stderrActiveLive {
		t.Fatalf("stopped active-route initialize output: stdout=%q stderr=%q want=%q", stdoutActiveStopped, stderrActiveStopped, stderrActiveLive)
	}

	// Advance to ProductComplete == true and verify live & stopped completed messages:
	opened, err = fresh.Open(t.Context(), paths, fresh.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store = freshstore.New(opened.DB)
	if _, err := store.Update(t.Context(), updated.Operation.Identity, updated.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		opened.Close()
		t.Fatal(err)
	}

	// 3. Completed installation while live:
	stdoutLive, stderrLive := runFreshInitializeFailure(t, env)
	if stdoutLive != "" ||
		strings.Contains(stderrLive, "#handoff=") ||
		strings.Contains(stderrLive, genericRefusal) ||
		!strings.Contains(stderrLive, "/console") ||
		stderrLive == stderrActiveLive ||
		stderrLive == stderrLocked {
		opened.Close()
		t.Fatalf("live completed initialize output: stdout=%q stderr=%q", stdoutLive, stderrLive)
	}

	// 4. Completed installation while stopped:
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	stdoutStopped, stderrStopped := runFreshInitializeFailure(t, env)
	if stdoutStopped != "" || stderrStopped != stderrLive {
		t.Fatalf("stopped completed initialize output: stdout=%q stderr=%q want=%q", stdoutStopped, stderrStopped, stderrLive)
	}

	// 5. Fallback formatting when ControlBasePath is empty or unvalidated:
	activeFallback := freshInitializeErrorMessage(fresh.RoutingCommittedError{})
	completeFallback := freshInitializeErrorMessage(fresh.AlreadyCompleteError{})
	for name, msg := range map[string]string{"active": activeFallback, "complete": completeFallback} {
		if msg == "" || msg == genericRefusal || strings.Contains(msg, "/console") || strings.Contains(msg, "  ") || strings.Contains(msg, `""`) {
			t.Fatalf("unexpected %s fallback message: %q", name, msg)
		}
	}
	if activeFallback == completeFallback {
		t.Fatalf("active and complete fallback messages should remain distinct: %q", activeFallback)
	}
}

func TestFreshInitializeFailsClosedWhenLockedStoreHasDamagedCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	writeOwnedProvisioningConfig(t, dir)
	env := ownedPreflightEnvironment(dir, path)

	runFreshInitializeProcess(t, env)
	paths := legacypreflight.DefaultPaths(path)
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if _, err := opened.DB.Exec("update settings set value='malformed' where key=?", freshstore.OperationKey); err != nil {
		t.Fatal(err)
	}
	genericRefusal := freshInitializeErrorMessage(errors.New("unclassified"))
	stdout, stderr := runFreshInitializeFailure(t, env)
	if stdout != "" ||
		strings.Contains(stderr, "#handoff=") ||
		!strings.Contains(stderr, genericRefusal) {
		t.Fatalf("damaged locked store initialize output: stdout=%q stderr=%q", stdout, stderr)
	}
}
