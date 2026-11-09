//go:build unix

package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSupervisorPreparationRefusesChangedStateWithoutMutations(t *testing.T) {
	for _, version := range []string{"7.3.4", "8.0.55"} {
		for _, tc := range []struct {
			name   string
			change func(*testing.T, *fakeSystem)
			want   string
		}{
			{"gateway auth symlink", func(t *testing.T, sys *fakeSystem) {
				sys.mustSeedDir(t, "/data/runtime/supervisor/private", 0, 0, 0o750)
				sys.mustSeedSymlink(t, "/data/runtime/supervisor/private", "/data/runtime/gateway/auth", CPAUID, CPAGID)
			}, "Gateway state path is not a real directory: /data/runtime/gateway/auth"},
			{"supervisor owner", func(t *testing.T, sys *fakeSystem) {
				m := sys.meta["/data/runtime/supervisor"]
				m.uid = CPAUID
				sys.meta["/data/runtime/supervisor"] = m
			}, "Supervisor persisted directory owner is not root"},
			{"changed transport token", func(t *testing.T, sys *fakeSystem) {
				sys.mustSeedFile(t, "/data/runtime-secret/token", []byte(strings.Repeat("x", 64)), RootUID, ManagerGID, TokenFileMode)
			}, "explicit Runtime transport secret does not match existing secret source"},
			{"invalid transport token", func(t *testing.T, sys *fakeSystem) {
				sys.mustSeedFile(t, "/data/runtime-secret/token", []byte("short"), RootUID, ManagerGID, TokenFileMode)
			}, "Runtime transport secret must contain at least 32 bytes"},
		} {
			t.Run(version+"/"+tc.name, func(t *testing.T) {
				sys := newFakeSystem(t)
				manifest, err := sys.ReadFile(DefaultCPAArtifactManifestPath)
				if err != nil {
					t.Fatal(err)
				}
				sys.mustSeedFile(t, DefaultCPAArtifactManifestPath, bytes.ReplaceAll(manifest, []byte("8.0.55"), []byte(version)), 0, 0, 0o644)
				prepared, err := prepare(PrepareConfig{}, sys)
				if err != nil {
					t.Fatal(err)
				}
				gate := supervisorPreparation(prepared, nil, sys)
				if _, err := gate(context.Background(), ChildSpec{}); err != nil {
					t.Fatal(err)
				}
				tc.change(t, sys)
				before := sys.mutations
				if _, err := gate(context.Background(), ChildSpec{}); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("restart error=%v, want %s", err, tc.want)
				}
				if sys.mutations != before {
					t.Fatalf("refusal mutated state %d times", sys.mutations-before)
				}
			})
		}
	}
}

func TestSupervisorRestartPreparationControlsSpawning(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		name := "valid volume restarts"
		if refuse {
			name = "repeated refusals stop spawning and exit nonzero"
		}
		t.Run(name, func(t *testing.T) {
			sys := newFakeSystem(t)
			prepared, err := prepare(PrepareConfig{}, sys)
			if err != nil {
				t.Fatal(err)
			}
			gate := supervisorPreparation(prepared, nil, sys)
			eventFile := filepath.Join(t.TempDir(), "events.log")
			var logs bytes.Buffer
			cfg := SupervisorConfig{
				SupervisorSpec: helperChildSpec(t, "supervisor", "CPAMP_TEST_EVENT_FILE="+eventFile, "CPAMP_TEST_EXIT_AFTER_MS=50"),
				ManagerSpec:    helperChildSpec(t, "manager", "CPAMP_TEST_EVENT_FILE="+eventFile),
				IngressSpec:    helperChildSpec(t, "ingress", "CPAMP_TEST_EVENT_FILE="+eventFile),
				Timings:        fastTimings(), Stdout: &bytes.Buffer{}, Stderr: &logs,
			}
			cfg.Timings.SupervisorShortRunWindow = 10 * time.Second
			cfg.Timings.InitialBackoff = 20 * time.Millisecond
			cfg.Timings.MaxBackoff = 20 * time.Millisecond
			attempts, refusals := 0, 0
			cfg.BeforeSupervisorStart = func(ctx context.Context, spec ChildSpec) (ChildSpec, error) {
				attempts++
				if refuse && attempts == 2 {
					sys.mustSeedDir(t, "/data/runtime/supervisor/private", 0, 0, 0o750)
					sys.mustSeedSymlink(t, "/data/runtime/supervisor/private", "/data/runtime/gateway/auth", CPAUID, CPAGID)
				}
				before := sys.mutations
				refreshed, err := gate(ctx, spec)
				if err != nil {
					refusals++
					if sys.mutations != before {
						t.Errorf("refusal mutated state %d times", sys.mutations-before)
					}
					return spec, err
				}
				if refreshed.Dir != prepared.GatewayDir || parseEnvironMap(refreshed.Env)["CPAMP_RUNTIME_TOKEN"] != prepared.RuntimeToken {
					t.Error("restart did not reuse the prepared paths and transport token")
				}
				// The real helper runs on the host, outside the fake volume paths.
				return spec, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			err = RunSupervisor(ctx, cfg)
			if err == nil || !strings.Contains(err.Error(), "supervisor failed 5 consecutive starts") {
				t.Fatalf("expected fatal short-start policy, got %v", err)
			}
			if attempts != 5 {
				t.Fatalf("attempts=%d, want 5", attempts)
			}
			events := parseEvents(t, eventFile)
			starts := 0
			for _, event := range events {
				if event.Role == "supervisor" && event.Kind == "start" {
					starts++
				}
			}
			if refuse {
				if starts != 1 || refusals != 4 {
					t.Fatalf("Supervisor starts=%d refusals=%d, want 1 and 4", starts, refusals)
				}
				if !strings.Contains(logs.String(), "[launcher] failed to start supervisor: Gateway state path is not a real directory: /data/runtime/gateway/auth") {
					t.Fatalf("missing exact refusal in logs: %s", logs.String())
				}
			} else if starts != 5 || refusals != 0 {
				t.Fatalf("valid Supervisor starts=%d refusals=%d", starts, refusals)
			}
			// Siblings remain alive during refusal retries and terminate only in
			// the final ordered shutdown after the failure limit is exhausted.
			for _, role := range []string{"manager", "ingress"} {
				roleStarts, roleTerms := 0, 0
				for _, event := range events {
					if event.Role == role && event.Kind == "start" {
						roleStarts++
					}
					if event.Role == role && event.Kind == "sigterm" {
						roleTerms++
					}
				}
				if roleStarts != 1 || roleTerms != 1 {
					t.Fatalf("%s starts=%d terms=%d", role, roleStarts, roleTerms)
				}
			}
			if _, err := os.Stat(eventFile); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSupervisorPreparationLeavesPrivateStateOwnershipForSupervisor(t *testing.T) {
	sys := newFakeSystem(t)
	prepared, err := prepare(PrepareConfig{}, sys)
	if err != nil {
		t.Fatal(err)
	}
	// The Supervisor must still see these ownership mismatches on startup;
	// launcher preparation must not recursively repair its private state.
	paths := []string{
		"/data/runtime/supervisor/operations.sqlite",
		"/data/runtime/supervisor/active/cpa/selection.json",
		"/data/runtime/supervisor/artifacts/cpa/7.3.4/cli-proxy-api",
	}
	for _, path := range paths {
		sys.mustSeedFile(t, path, []byte("private state"), CPAUID, CPAGID, 0o600)
	}
	for _, path := range []string{"/data/runtime/supervisor/active", "/data/runtime/supervisor/artifacts/cpa/7.3.4"} {
		sys.mustSeedDir(t, path, CPAUID, CPAGID, 0o510)
	}
	if _, err := supervisorPreparation(prepared, nil, sys)(context.Background(), ChildSpec{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range append(paths, "/data/runtime/supervisor/active", "/data/runtime/supervisor/artifacts/cpa/7.3.4") {
		stat, err := sys.Lstat(path)
		if err != nil || stat.UID != CPAUID || stat.GID != CPAGID {
			t.Fatalf("private state ownership changed at %s: %+v, %v", path, stat, err)
		}
	}
}

func TestSupervisorPreparationRechecksBootstrapIdentityAndConfig(t *testing.T) {
	for _, path := range []string{"/data/runtime/.cpamp-v8-bootstrap", "/data/runtime/gateway/config.yaml"} {
		t.Run(path, func(t *testing.T) {
			sys := newFakeSystem(t)
			prepared, err := prepare(PrepareConfig{}, sys)
			if err != nil {
				t.Fatal(err)
			}
			if err := sys.WriteFile(path, []byte("different"), 0o444); err != nil {
				t.Fatal(err)
			}
			before := sys.mutations
			_, err = supervisorPreparation(prepared, nil, sys)(context.Background(), ChildSpec{})
			if err == nil || !strings.Contains(err.Error(), "Default CPA v8 refuses") {
				t.Fatalf("expected v8 refusal, got %v", err)
			}
			if sys.mutations != before {
				t.Fatalf("bootstrap refusal mutated state %d times", sys.mutations-before)
			}
		})
	}
}

type r8FilesystemModes struct{ *fakeSystem }

func (s r8FilesystemModes) Chmod(path string, mode fs.FileMode) error {
	if err := s.fakeSystem.Chmod(path, mode); err != nil {
		return err
	}
	return os.Chmod(s.hostPath(path), mode)
}

func TestR8RestartRefusesChangedAuthBeforeFinalModeRepair(t *testing.T) {
	sys := newFakeSystem(t)
	prepared, err := prepare(PrepareConfig{}, sys)
	if err != nil {
		t.Fatal(err)
	}
	private := "/data/runtime/supervisor/review-private"
	auth := "/data/runtime/gateway/auth"
	sys.mustSeedDir(t, private, RootUID, RootGID, 0o750)
	if err := os.Chmod(sys.hostPath(private), 0o750); err != nil {
		t.Fatal(err)
	}
	changed := false
	sys.opHook = func(op, path string) {
		if !changed && op == "chmod" && path == auth {
			changed = true
			sys.mustSeedSymlink(t, sys.hostPath(private), auth, CPAUID, CPAGID)
		}
	}
	_, gateErr := supervisorPreparation(prepared, nil, r8FilesystemModes{sys})(context.Background(), ChildSpec{})
	if !changed {
		t.Fatal("expected an auth-directory change during final mode repair")
	}
	got, err := os.Stat(sys.hostPath(private))
	if err != nil {
		t.Fatal(err)
	}
	if gateErr == nil || got.Mode().Perm() != 0o750 {
		t.Fatalf("restart must refuse changed auth without changing private target: gate error=%v, private mode=%04o; want refusal and 0750", gateErr, got.Mode().Perm())
	}
}

type r8ObservedOwnership struct {
	*fakeSystem
	outside *[]string
}

func (s r8ObservedOwnership) Lchown(path string, uid, gid int) error {
	parent, err := filepath.EvalSymlinks(filepath.Dir(s.hostPath(path)))
	if err != nil {
		return err
	}
	resolved := filepath.Join(parent, filepath.Base(path))
	root := s.hostPath("/data/runtime/gateway")
	if strings.HasPrefix(path, "/data/runtime/gateway/") && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		*s.outside = append(*s.outside, path)
	}
	return s.fakeSystem.Lchown(path, uid, gid)
}

func TestR8RestartOwnershipWalkStaysWithinGateway(t *testing.T) {
	sys := newFakeSystem(t)
	prepared, err := prepare(PrepareConfig{}, sys)
	if err != nil {
		t.Fatal(err)
	}
	private := "/data/runtime/supervisor/review-private"
	auth := "/data/runtime/gateway/auth"
	sys.mustSeedDir(t, private, RootUID, RootGID, 0o750)
	sys.mustSeedFile(t, private+"/record", []byte("private fixture"), RootUID, RootGID, 0o600)
	changed := false
	sys.opHook = func(op, path string) {
		if !changed && op == "readdir" && path == auth {
			changed = true
			sys.mustSeedSymlink(t, sys.hostPath(private), auth, CPAUID, CPAGID)
		}
	}
	var outside []string
	_, gateErr := supervisorPreparation(prepared, nil, r8ObservedOwnership{sys, &outside})(context.Background(), ChildSpec{})
	if !changed {
		t.Fatal("expected auth-directory change between metadata inspection and directory read")
	}
	if len(outside) > 0 || gateErr == nil {
		t.Fatalf("restart ownership walk resolved outside Gateway: %v; gate error=%v", outside, gateErr)
	}
}

func TestR8RestartRestoresMissingTokenWithoutRotation(t *testing.T) {
	sys := newFakeSystem(t)
	prepared, err := prepare(PrepareConfig{}, sys)
	if err != nil {
		t.Fatal(err)
	}
	if err := sys.Remove(prepared.TokenFile); err != nil {
		t.Fatal(err)
	}
	spec, err := supervisorPreparation(prepared, nil, sys)(context.Background(), ChildSpec{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := sys.ReadFile(prepared.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != prepared.RuntimeToken || parseEnvironMap(spec.Env)["CPAMP_RUNTIME_TOKEN"] != prepared.RuntimeToken {
		t.Fatal("restart changed the established transport token")
	}
	sys.assertNode(t, prepared.TokenFile, RootUID, ManagerGID, TokenFileMode, false)
}

func seedProcStatus(t *testing.T, sys *fakeSystem, pid int, ruid, euid, suid, fsuid int) {
	t.Helper()
	status := fmt.Sprintf("Name:\thelper\nState:\tS (sleeping)\nPid:\t%d\nUid:\t%d\t%d\t%d\t%d\n", pid, ruid, euid, suid, fsuid)
	sys.mustSeedFile(t, fmt.Sprintf("/proc/%d/status", pid), []byte(status), RootUID, RootGID, 0o444)
}

func TestCPAQuiescenceBeforePrepareAndRestart(t *testing.T) {
	t.Run("kills only pure uid 10001 processes and spares pid 1 root manager and ingress", func(t *testing.T) {
		sys := newFakeSystem(t)
		seedProcStatus(t, sys, 1, 0, 0, 0, 0)
		seedProcStatus(t, sys, 200, 0, 0, 0, 0)
		seedProcStatus(t, sys, 301, 10002, 10002, 10002, 10002)
		seedProcStatus(t, sys, 302, 10003, 10003, 10003, 10003)
		seedProcStatus(t, sys, 401, 10001, 10001, 10001, 10001)
		seedProcStatus(t, sys, 402, 10001, 10001, 10001, 10001)

		prepared, err := prepare(PrepareConfig{}, sys)
		if err != nil {
			t.Fatalf("prepare failed: %v", err)
		}
		slices.Sort(sys.killCalls)
		wantKilled := []int{401, 402}
		if !slices.Equal(sys.killCalls, wantKilled) {
			t.Fatalf("killed PIDs=%v, want %v", sys.killCalls, wantKilled)
		}

		// Seed another surviving helper before restart; restart gate must kill it too.
		sys.killCalls = nil
		seedProcStatus(t, sys, 505, 10001, 10001, 10001, 10001)
		if _, err := supervisorPreparation(prepared, nil, sys)(context.Background(), ChildSpec{}); err != nil {
			t.Fatalf("restart gate failed: %v", err)
		}
		if !slices.Equal(sys.killCalls, []int{505}) {
			t.Fatalf("restart killed PIDs=%v, want [505]", sys.killCalls)
		}
	})
	t.Run("refuses with zero volume mutations when uid 10001 process cannot be quiesced", func(t *testing.T) {
		sys := newFakeSystem(t)
		prepared, err := prepare(PrepareConfig{}, sys)
		if err != nil {
			t.Fatal(err)
		}
		seedProcStatus(t, sys, 777, 10001, 10001, 10001, 10001)
		sys.keepAlivePIDs = map[int]bool{777: true}

		before := sys.mutations
		_, err = prepareContext(context.Background(), PrepareConfig{
			DataDir:              prepared.DataDir,
			RuntimeRoot:          prepared.RuntimeRoot,
			TokenFile:            prepared.TokenFile,
			ExplicitRuntimeToken: prepared.RuntimeToken,
			QuiesceTimeout:       40 * time.Millisecond,
		}, sys)
		if err == nil || !strings.Contains(err.Error(), "failed to quiesce CPA UID 10001 processes") {
			t.Fatalf("expected quiescence timeout refusal, got: %v", err)
		}
		if sys.mutations != before {
			t.Fatalf("quiescence refusal mutated volume %d times; want 0", sys.mutations-before)
		}
	})
}

func TestDescriptorRelativeGatewayRepairNoFollow(t *testing.T) {
	base := t.TempDir()
	layout := ResolveRuntimeLayout(filepath.Join(base, "data"), filepath.Join(base, "data", "runtime"), filepath.Join(base, "data", "runtime-secret", "token"))
	for _, dir := range layout.GatewayDirs() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(layout.GatewayConfigPath, []byte(testSeedYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	uid := os.Getuid()
	gid := os.Getgid()
	if err := repairGatewayTreeNoFollowFD(context.Background(), layout, uid, gid); err != nil {
		t.Fatalf("repairGatewayTreeNoFollowFD on valid tree failed: %v", err)
	}
	for _, dir := range layout.GatewayDirs() {
		info, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != GatewayDirMode {
			t.Fatalf("%s mode=%04o, want %04o", dir, info.Mode().Perm(), GatewayDirMode)
		}
	}

	privateDir := filepath.Join(base, "private-target")
	if err := os.MkdirAll(privateDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privateDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(layout.GatewayAuthDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(privateDir, layout.GatewayAuthDir); err != nil {
		t.Fatal(err)
	}

	err := repairGatewayTreeNoFollowFD(context.Background(), layout, uid, gid)
	if err == nil || !strings.Contains(err.Error(), "Gateway state path is not a real directory") {
		t.Fatalf("expected symlinked auth refusal from repairGatewayTreeNoFollowFD, got: %v", err)
	}
	privInfo, err := os.Stat(privateDir)
	if err != nil {
		t.Fatal(err)
	}
	if privInfo.Mode().Perm() != 0o750 {
		t.Fatalf("private target mode changed to %04o; want 0750", privInfo.Mode().Perm())
	}
}

func TestR9DataModeRetainsStickyBitWithOSChmod(t *testing.T) {
	path := t.TempDir()
	if err := (osSystem{}).Chmod(path, DataDirMode); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeSticky == 0 || NormalizeUnixPerm(info.Mode()) != DataDirMode {
		t.Fatalf("OS chmod mode=%04o; want sticky data mode %04o", NormalizeUnixPerm(info.Mode()), DataDirMode)
	}

	// Verify setgid bit preservation as well on a real filesystem directory.
	setgidMode := fs.FileMode(0o2750)
	if err := (osSystem{}).Chmod(path, setgidMode); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeSetgid == 0 || NormalizeUnixPerm(info.Mode()) != setgidMode {
		t.Fatalf("OS chmod mode=%04o; want setgid mode %04o", NormalizeUnixPerm(info.Mode()), setgidMode)
	}
}

type r9ReusedPIDSystem struct {
	*fakeSystem
	t                *testing.T
	replaced         bool
	signalledManager bool
}

func (s *r9ReusedPIDSystem) ReadFile(path string) ([]byte, error) {
	data, err := s.fakeSystem.ReadFile(path)
	if err == nil && path == "/proc/601/status" && !s.replaced {
		s.replaced = true
		seedProcStatus(s.t, s.fakeSystem, 601, ManagerUID, ManagerUID, ManagerUID, ManagerUID)
	}
	return data, err
}

func (s *r9ReusedPIDSystem) Kill(pid int, sig syscall.Signal) error {
	if pid == 601 && s.replaced {
		data, err := s.fakeSystem.ReadFile("/proc/601/status")
		if err == nil {
			uids, ok := parseProcStatusUIDs(data)
			s.signalledManager = ok && uids[0] == ManagerUID
		}
	}
	return s.fakeSystem.Kill(pid, sig)
}

func TestR9QuiescencePreservesManagerAfterPIDReuse(t *testing.T) {
	sys := newFakeSystem(t)
	seedProcStatus(t, sys, 601, CPAUID, CPAUID, CPAUID, CPAUID)
	fixture := &r9ReusedPIDSystem{fakeSystem: sys, t: t}
	err := ensureCPAQuiescence(context.Background(), "/proc", CPAUID, time.Second, nil, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.signalledManager {
		t.Fatal("quiescence signalled a Manager-identity process after the numeric PID changed identity")
	}
}

func TestQuiescenceSkipsProcessThatChangesUIDAndRefusesWhenPidfdUnavailable(t *testing.T) {
	t.Run("matched process changing uid to root before signal is not killed", func(t *testing.T) {
		sys := newFakeSystem(t)
		seedProcStatus(t, sys, 602, CPAUID, CPAUID, CPAUID, CPAUID)
		sys.openPIDHook = func(pid int) (PIDHandle, error) {
			if pid == 602 {
				seedProcStatus(t, sys, 602, RootUID, RootUID, RootUID, RootUID)
			}
			return sysKillPIDHandle{}, nil
		}
		if err := ensureCPAQuiescence(context.Background(), "/proc", CPAUID, time.Second, nil, sys); err != nil {
			t.Fatalf("unexpected quiescence error: %v", err)
		}
		if len(sys.killCalls) != 0 {
			t.Fatalf("expected 0 kill calls after uid change, got %v", sys.killCalls)
		}
	})

	t.Run("ENOSYS from OpenPID refuses start with zero volume mutations", func(t *testing.T) {
		sys := newFakeSystem(t)
		seedProcStatus(t, sys, 603, CPAUID, CPAUID, CPAUID, CPAUID)
		sys.openPIDHook = func(int) (PIDHandle, error) {
			return nil, syscall.ENOSYS
		}
		before := sys.mutations
		_, err := prepare(PrepareConfig{}, sys)
		if err == nil || !strings.Contains(err.Error(), "pidfd_open is required to quiesce CPA processes safely") {
			t.Fatalf("expected pidfd ENOSYS refusal, got: %v", err)
		}
		if sys.mutations != before {
			t.Fatalf("pidfd ENOSYS refusal mutated volume %d times; want 0", sys.mutations-before)
		}
	})
}

func TestPrivatePIDNamespaceAndProcfsPreconditions(t *testing.T) {
	t.Run("pid != 1 refuses with zero kills and zero volume mutations", func(t *testing.T) {
		sys := newFakeSystem(t)
		sys.selfPID = 42
		seedProcStatus(t, sys, 401, CPAUID, CPAUID, CPAUID, CPAUID)
		before := sys.mutations

		_, err := prepare(PrepareConfig{}, sys)
		if err == nil || !strings.Contains(err.Error(), "cpamp-launcher must run as PID 1 in a private PID namespace (got pid 42)") {
			t.Fatalf("expected non-PID-1 refusal, got: %v", err)
		}
		if len(sys.killCalls) != 0 {
			t.Fatalf("expected 0 Kill calls when pid != 1, got %v", sys.killCalls)
		}
		if sys.mutations != before {
			t.Fatalf("non-PID-1 refusal mutated volume %d times; want 0", sys.mutations-before)
		}
	})

	t.Run("/proc not procfs refuses with zero kills and zero volume mutations", func(t *testing.T) {
		sys := newFakeSystem(t)
		sys.procfsMagic = 0xef53 // ext4/non-procfs magic
		seedProcStatus(t, sys, 401, CPAUID, CPAUID, CPAUID, CPAUID)
		before := sys.mutations

		_, err := prepare(PrepareConfig{}, sys)
		if err == nil || !strings.Contains(err.Error(), "proc path /proc is not a procfs mount") {
			t.Fatalf("expected non-procfs refusal, got: %v", err)
		}
		if len(sys.killCalls) != 0 {
			t.Fatalf("expected 0 Kill calls when /proc is not procfs, got %v", sys.killCalls)
		}
		if sys.mutations != before {
			t.Fatalf("non-procfs refusal mutated volume %d times; want 0", sys.mutations-before)
		}
	})

	t.Run("/proc/self mismatch refuses with zero kills and zero volume mutations", func(t *testing.T) {
		sys := newFakeSystem(t)
		sys.mustSeedSymlink(t, "19", DefaultProcPath+"/self", RootUID, RootGID)
		seedProcStatus(t, sys, 401, CPAUID, CPAUID, CPAUID, CPAUID)
		before := sys.mutations

		_, err := prepare(PrepareConfig{}, sys)
		if err == nil || !strings.Contains(err.Error(), "proc self at /proc/self resolved to \"19\", want \"1\"") {
			t.Fatalf("expected /proc/self mismatch refusal, got: %v", err)
		}
		if len(sys.killCalls) != 0 {
			t.Fatalf("expected 0 Kill calls on /proc/self mismatch, got %v", sys.killCalls)
		}
		if sys.mutations != before {
			t.Fatalf("/proc/self mismatch refusal mutated volume %d times; want 0", sys.mutations-before)
		}
	})

	for _, tc := range []struct {
		name      string
		mntID     int
		mountinfo string
		wantErr   string
	}{
		{
			name:      "hidepid=2 refused",
			mntID:     612,
			mountinfo: "612 600 0:65 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw,hidepid=2,gid=65534\n",
			wantErr:   "procfs at /proc (mnt_id 612) is mounted with hidepid=2; cpamp-launcher requires an unfiltered procfs (no hidepid)",
		},
		{
			name:      "hidepid=invisible refused",
			mntID:     612,
			mountinfo: "612 600 0:65 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw,hidepid=invisible,gid=65534\n",
			wantErr:   "procfs at /proc (mnt_id 612) is mounted with hidepid=invisible; cpamp-launcher requires an unfiltered procfs (no hidepid)",
		},
		{
			name:      "hidepid=1 refused",
			mntID:     612,
			mountinfo: "612 600 0:65 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw,hidepid=1\n",
			wantErr:   "procfs at /proc (mnt_id 612) is mounted with hidepid=1; cpamp-launcher requires an unfiltered procfs (no hidepid)",
		},
		{
			name:      "hidepid=noaccess refused",
			mntID:     612,
			mountinfo: "612 600 0:65 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw,hidepid=noaccess\n",
			wantErr:   "procfs at /proc (mnt_id 612) is mounted with hidepid=noaccess; cpamp-launcher requires an unfiltered procfs (no hidepid)",
		},
		{
			name:      "visible mount by mnt_id is filtered while last mountinfo record is clean -> refused",
			mntID:     237,
			mountinfo: "237 249 0:65 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw,hidepid=invisible,gid=65534\n249 600 0:64 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw\n",
			wantErr:   "procfs at /proc (mnt_id 237) is mounted with hidepid=invisible; cpamp-launcher requires an unfiltered procfs (no hidepid)",
		},
		{
			name:      "mnt_id not found in mountinfo refused",
			mntID:     999,
			mountinfo: "612 600 0:65 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw\n",
			wantErr:   "procfs mount entry with mnt_id 999 for /proc not found in /proc/self/mountinfo; cpamp-launcher requires an unfiltered procfs (no hidepid)",
		},
		{
			name:      "no proc entry for procPath refused",
			mntID:     612,
			mountinfo: "36 35 0:32 / / rw,relatime - overlay overlay rw\n",
			wantErr:   "procfs mount entry with mnt_id 612 for /proc not found in /proc/self/mountinfo; cpamp-launcher requires an unfiltered procfs (no hidepid)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys := newFakeSystem(t)
			sys.procMountID = tc.mntID
			sys.mustSeedFile(t, DefaultMountInfoPath, []byte(tc.mountinfo), RootUID, RootGID, 0o0444)
			seedProcStatus(t, sys, 401, CPAUID, CPAUID, CPAUID, CPAUID)
			before := sys.mutations

			_, err := prepare(PrepareConfig{}, sys)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q, got: %v", tc.wantErr, err)
			}
			if len(sys.killCalls) != 0 {
				t.Fatalf("expected 0 Kill calls, got %v", sys.killCalls)
			}
			if sys.mutations != before {
				t.Fatalf("refusal mutated volume %d times; want 0", sys.mutations-before)
			}
		})
	}

	t.Run("visible mount by mnt_id is clean while earlier record is filtered -> accepted", func(t *testing.T) {
		sys := newFakeSystem(t)
		sys.procMountID = 249
		sys.mustSeedFile(t, DefaultMountInfoPath, []byte(
			"237 600 0:64 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw,hidepid=invisible,gid=65534\n"+
				"249 237 0:65 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw,hidepid=0,subset=pid\n",
		), RootUID, RootGID, 0o0444)
		if _, err := prepare(PrepareConfig{}, sys); err != nil {
			t.Fatalf("expected visible clean mount (mnt_id 249) to succeed, got: %v", err)
		}
	})

	t.Run("MountID error refuses with zero kills and zero volume mutations", func(t *testing.T) {
		sys := newFakeSystem(t)
		sys.mountIDErr = syscall.EACCES
		seedProcStatus(t, sys, 401, CPAUID, CPAUID, CPAUID, CPAUID)
		before := sys.mutations

		_, err := prepare(PrepareConfig{}, sys)
		if err == nil || !strings.Contains(err.Error(), "failed to resolve mount ID for /proc (cpamp-launcher requires an unfiltered procfs with no hidepid)") {
			t.Fatalf("expected MountID error refusal, got: %v", err)
		}
		if len(sys.killCalls) != 0 {
			t.Fatalf("expected 0 Kill calls on MountID error, got %v", sys.killCalls)
		}
		if sys.mutations != before {
			t.Fatalf("MountID error refusal mutated volume %d times; want 0", sys.mutations-before)
		}
	})

	t.Run("parseFDInfoMountID parses mnt_id line", func(t *testing.T) {
		got, err := parseFDInfoMountID([]byte("pos:\t0\nflags:\t0200000\nmnt_id:\t237\nino:\t1\n"))
		if err != nil || got != 237 {
			t.Fatalf("parseFDInfoMountID = (%d, %v), want (237, nil)", got, err)
		}
	})

	t.Run("mountinfo missing refuses with zero kills and zero volume mutations", func(t *testing.T) {
		sys := newFakeSystem(t)
		if err := os.Remove(sys.hostPath(DefaultMountInfoPath)); err != nil {
			t.Fatal(err)
		}
		delete(sys.meta, DefaultMountInfoPath)
		seedProcStatus(t, sys, 401, CPAUID, CPAUID, CPAUID, CPAUID)
		before := sys.mutations

		_, err := prepare(PrepareConfig{}, sys)
		if err == nil || !strings.Contains(err.Error(), "failed to read /proc/self/mountinfo (cpamp-launcher requires an unfiltered procfs with no hidepid)") {
			t.Fatalf("expected missing mountinfo refusal, got: %v", err)
		}
		if len(sys.killCalls) != 0 {
			t.Fatalf("expected 0 Kill calls when mountinfo missing, got %v", sys.killCalls)
		}
		if sys.mutations != before {
			t.Fatalf("missing mountinfo refusal mutated volume %d times; want 0", sys.mutations-before)
		}
	})

	t.Run("status EACCES refuses with zero kills and zero volume mutations", func(t *testing.T) {
		sys := newFakeSystem(t)
		seedProcStatus(t, sys, 401, CPAUID, CPAUID, CPAUID, CPAUID)
		sys.readFileErr = map[string]error{
			"/proc/401/status": syscall.EACCES,
		}
		before := sys.mutations

		_, err := prepare(PrepareConfig{}, sys)
		if err == nil || !strings.Contains(err.Error(), "failed to read /proc/401/status (cpamp-launcher requires an unfiltered procfs with no hidepid)") {
			t.Fatalf("expected status EACCES refusal, got: %v", err)
		}
		if len(sys.killCalls) != 0 {
			t.Fatalf("expected 0 Kill calls on status EACCES, got %v", sys.killCalls)
		}
		if sys.mutations != before {
			t.Fatalf("status EACCES refusal mutated volume %d times; want 0", sys.mutations-before)
		}
	})
}

func TestMissingProcWhileFakeCPAExistsRefusesWithoutMutationOrSupervisorSpawn(t *testing.T) {
	sys := newFakeSystem(t)
	prepared, err := prepare(PrepareConfig{}, sys)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a lingering CPA process while /proc becomes unavailable before a restart.
	seedProcStatus(t, sys, 401, CPAUID, CPAUID, CPAUID, CPAUID)
	if err := os.RemoveAll(sys.hostPath(DefaultProcPath)); err != nil {
		t.Fatal(err)
	}
	delete(sys.meta, DefaultProcPath)

	before := sys.mutations
	gate := supervisorPreparation(prepared, nil, sys)
	if _, err := gate(context.Background(), ChildSpec{}); err == nil || !strings.Contains(err.Error(), "failed to stat procfs at /proc") {
		t.Fatalf("expected missing /proc refusal, got: %v", err)
	}
	if len(sys.killCalls) != 0 {
		t.Fatalf("expected 0 Kill calls when /proc is missing, got %v", sys.killCalls)
	}
	if sys.mutations != before {
		t.Fatalf("missing /proc refusal mutated volume %d times; want 0", sys.mutations-before)
	}

	// Verify RunSupervisor never spawns the Supervisor when /proc is missing.
	eventFile := filepath.Join(t.TempDir(), "events.log")
	if err := os.WriteFile(eventFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := SupervisorConfig{
		SupervisorSpec:        helperChildSpec(t, "supervisor", "CPAMP_TEST_EVENT_FILE="+eventFile),
		ManagerSpec:           helperChildSpec(t, "manager", "CPAMP_TEST_EVENT_FILE="+eventFile),
		IngressSpec:           helperChildSpec(t, "ingress", "CPAMP_TEST_EVENT_FILE="+eventFile),
		BeforeSupervisorStart: gate,
		Timings:               fastTimings(),
		Stdout:                &bytes.Buffer{},
		Stderr:                &bytes.Buffer{},
	}
	cfg.Timings.InitialBackoff = 15 * time.Millisecond
	cfg.Timings.MaxBackoff = 15 * time.Millisecond

	if err := RunSupervisor(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "supervisor failed 5 consecutive starts") {
		t.Fatalf("expected fatal refusal from missing /proc, got: %v", err)
	}
	assertNoRoleStarted(t, eventFile, "supervisor")
	assertNoRoleStarted(t, eventFile, "manager")
	assertNoRoleStarted(t, eventFile, "ingress")
}

func TestProcUIDClassificationAndMixedUIDRefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		uids [4]int
		want string
	}{
		{
			name: "mixed [10001,10002,10002,10002]",
			uids: [4]int{10001, 10002, 10002, 10002},
			want: "process 410 has mixed CPA and non-CPA UIDs [real=10001 effective=10002 saved=10002 fs=10002]",
		},
		{
			name: "mixed [10002,10003,10001,10002]",
			uids: [4]int{10002, 10003, 10001, 10002},
			want: "process 410 has mixed CPA and non-CPA UIDs [real=10002 effective=10003 saved=10001 fs=10002]",
		},
		{
			name: "mixed [0,10001,0,0]",
			uids: [4]int{0, 10001, 0, 0},
			want: "process 410 has mixed CPA and non-CPA UIDs [real=0 effective=10001 saved=0 fs=0]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sys := newFakeSystem(t)
			seedProcStatus(t, sys, 409, CPAUID, CPAUID, CPAUID, CPAUID)
			seedProcStatus(t, sys, 410, tc.uids[0], tc.uids[1], tc.uids[2], tc.uids[3])
			before := sys.mutations

			_, err := prepare(PrepareConfig{}, sys)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected mixed-UID refusal %q, got: %v", tc.want, err)
			}
			if len(sys.killCalls) != 0 {
				t.Fatalf("expected 0 Kill calls on mixed-UID refusal, got %v", sys.killCalls)
			}
			if sys.mutations != before {
				t.Fatalf("mixed-UID refusal mutated volume %d times; want 0", sys.mutations-before)
			}
		})
	}

	t.Run("pure Manager pure Ingress and pure CPA handled appropriately", func(t *testing.T) {
		sys := newFakeSystem(t)
		seedProcStatus(t, sys, 301, ManagerUID, ManagerUID, ManagerUID, ManagerUID)
		seedProcStatus(t, sys, 302, IngressUID, IngressUID, IngressUID, IngressUID)
		seedProcStatus(t, sys, 401, CPAUID, CPAUID, CPAUID, CPAUID)

		if _, err := prepare(PrepareConfig{}, sys); err != nil {
			t.Fatalf("unexpected error with pure Manager, pure Ingress, and pure CPA: %v", err)
		}
		if !slices.Equal(sys.killCalls, []int{401}) {
			t.Fatalf("killed PIDs=%v, want [401] (only pure CPA killed)", sys.killCalls)
		}
	})
}
