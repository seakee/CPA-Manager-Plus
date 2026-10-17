package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
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
