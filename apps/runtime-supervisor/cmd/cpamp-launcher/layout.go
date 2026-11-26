//go:build unix

package main

import (
	"bytes"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

const (
	RootUID = 0
	RootGID = 0

	CPAUID      = 10001
	CPAGID      = 10001
	CPAUserName = "cpamp-cpa"

	ManagerUID      = 10002
	ManagerGID      = 10002
	ManagerUserName = "cpamp-manager"

	IngressUID      = 10003
	IngressGID      = 10003
	IngressUserName = "cpamp-ingress"

	DefaultDataDir                  = "/data"
	DefaultRuntimeRoot              = "/data/runtime"
	DedicatedRuntimeMountPoint      = "/runtime"
	DefaultMountInfoPath            = "/proc/self/mountinfo"
	DefaultProcPath                 = "/proc"
	DefaultRuntimeSecretDir         = "/data/runtime-secret"
	DefaultRuntimeTokenFile         = "/data/runtime-secret/token"
	DefaultLauncherTokenFile        = "/data/runtime-secret/launcher-token"
	DefaultSeedConfigPath           = "/usr/local/share/cpamp/runtime/config.seed.yaml"
	DefaultSeedLineagePath          = "/usr/local/share/cpamp/runtime/seed-lineage.json"
	DefaultSupervisorExecutablePath = "/usr/local/bin/cpamp-runtime-supervisor"
	DefaultManagerExecutablePath    = "/usr/local/bin/cpa-manager-plus"
	DefaultIngressExecutablePath    = "/usr/local/bin/cpamp-ingress"
	DefaultCPAExecutablePath        = "/usr/local/bin/cli-proxy-api"
	DefaultCPAArtifactManifestPath  = "/usr/local/share/cpamp/runtime/cpa-artifact.json"
	DefaultPasswdPath               = "/etc/passwd"
	DefaultGroupPath                = "/etc/group"

	DefaultRuntimeIdentity     = "embedded-default"
	DefaultRuntimeAddr         = "127.0.0.1:9081"
	DefaultManagerAddr         = "127.0.0.1:18319"
	DefaultIngressPublicAddr   = "0.0.0.0:18317"
	DefaultIngressAdminAddr    = "127.0.0.1:9082"
	DefaultRuntimeCPAAddr      = "127.0.0.1:8317"
	DefaultLauncherControlAddr = "127.0.0.1:9083"

	BootstrapMarkerName     = ".cpamp-v8-bootstrap"
	BootstrapIdentityPrefix = "official-cpa-v8-bootstrap:"

	InitialRestartBackoff                 = 1 * time.Second
	MaxRestartBackoff                     = 30 * time.Second
	HealthyUptimeResetThreshold           = 60 * time.Second
	SupervisorShortRunWindow              = 10 * time.Second
	SupervisorMaxConsecutiveShortFailures = 5

	DefaultDrainTimeout             = 90 * time.Second
	IngressDrainGraceExtra          = 5 * time.Second
	ManagerShutdownTimeout          = 15 * time.Second
	SupervisorShutdownTimeout       = 15 * time.Second
	ProcessGroupKillGrace           = 5 * time.Second
	SIGKILLReapWait                 = 500 * time.Millisecond
	ChildIODrainTimeout             = 1 * time.Second
	DefaultReadinessPollInterval    = 100 * time.Millisecond
	ReadinessLogInterval            = 15 * time.Second
	UpgradeWorkerReadyTimeout       = 15 * time.Second
	UpgradeWorkerMaxInitialFailures = 3
	CPAQuiescenceTimeout            = 5 * time.Second
	CPAQuiescencePollInterval       = 10 * time.Millisecond
	DefaultTrialWatchdogTimeout     = 60 * time.Second
	DefaultTrialStabilizationWindow = 5 * time.Second

	CPAMPRootMode               fs.FileMode = 0o0755
	SlotsDirMode                fs.FileMode = 0o0755
	SlotDirMode                 fs.FileMode = 0o0755
	SlotBinMode                 fs.FileMode = 0o0555
	SlotShareDirMode            fs.FileMode = 0o0555
	SlotFileMode                fs.FileMode = 0o0444
	SlotStateFileMode           fs.FileMode = 0o0600
	LauncherTokenFileModeDocker fs.FileMode = 0o0640
	LauncherTokenFileModeNative fs.FileMode = 0o0600

	// ShutdownFixedBudgetLimit bounds every stop-stage budget that is not the
	// configurable drain window. The container stop grace must be at least
	// CPAMP_DRAIN_TIMEOUT + this limit (default grace 150s = 90s + 60s).
	ShutdownFixedBudgetLimit = 60 * time.Second
)

// ShutdownFixedBudget returns the worst-case time the launcher can spend on
// shutdown stages other than the configurable drain window:
//   - Ingress: IngressDrainGraceExtra + SIGKILLReapWait + ChildIODrainTimeout
//   - Manager: ManagerShutdownTimeout + SIGKILLReapWait + ChildIODrainTimeout
//   - Supervisor: SupervisorShutdownTimeout + SIGKILLReapWait + ProcessGroupKillGrace + SIGKILLReapWait + ChildIODrainTimeout
func ShutdownFixedBudget() time.Duration {
	return IngressDrainGraceExtra +
		ManagerShutdownTimeout +
		SupervisorShutdownTimeout +
		ProcessGroupKillGrace +
		4*SIGKILLReapWait +
		3*ChildIODrainTimeout
}

// RuntimeLayout holds the resolved filesystem paths produced for a runtime root.
type RuntimeLayout struct {
	DataDir             string
	RuntimeRoot         string
	BootstrapMarkerPath string
	CPAMPRoot           string
	SlotsDir            string
	SlotStatePath       string
	SupervisorDir       string
	ArtifactRoot        string
	ArtifactCPARoot     string
	JournalPath         string
	GatewayDir          string
	GatewayAuthDir      string
	GatewayLogsDir      string
	GatewayPluginsDir   string
	GatewayConfigPath   string
	TokenDir            string
	TokenFile           string
	LauncherTokenFile   string
}

// TrustedRootOwnedDirs returns the persisted directories that must be real
// root-owned directories (never symlinks).
func (l RuntimeLayout) TrustedRootOwnedDirs() []string {
	return []string{
		l.RuntimeRoot,
		l.CPAMPRoot,
		l.SlotsDir,
		l.SupervisorDir,
		l.ArtifactRoot,
		l.ArtifactCPARoot,
		l.TokenDir,
	}
}

// SupervisorDirs returns the Supervisor-private directories that must be owned
// by root:10001 with mode 0710.
func (l RuntimeLayout) SupervisorDirs() []string {
	return []string{
		l.SupervisorDir,
		l.ArtifactRoot,
		l.ArtifactCPARoot,
	}
}

// GatewayDirs returns the Gateway state directories that must be real
// directories owned by 10001:10001 with mode 0700.
func (l RuntimeLayout) GatewayDirs() []string {
	return []string{
		l.GatewayDir,
		l.GatewayAuthDir,
		l.GatewayLogsDir,
		l.GatewayPluginsDir,
	}
}

// ResolveRuntimeLayout computes all derived paths from the data dir, runtime root, and token file path.
func ResolveRuntimeLayout(dataDir, runtimeRoot, tokenFile string) RuntimeLayout {
	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	if runtimeRoot == "" {
		runtimeRoot = filepath.Join(dataDir, "runtime")
	}
	if tokenFile == "" {
		tokenFile = filepath.Join(dataDir, "runtime-secret", "token")
	}
	cpampRoot := filepath.Join(runtimeRoot, "cpamp")
	slotsDir := filepath.Join(cpampRoot, "slots")
	supervisorDir := filepath.Join(runtimeRoot, "supervisor")
	artifactRoot := filepath.Join(supervisorDir, "artifacts")
	gatewayDir := filepath.Join(runtimeRoot, "gateway")
	tokenDir := filepath.Dir(tokenFile)
	return RuntimeLayout{
		DataDir:             dataDir,
		RuntimeRoot:         runtimeRoot,
		BootstrapMarkerPath: filepath.Join(runtimeRoot, BootstrapMarkerName),
		CPAMPRoot:           cpampRoot,
		SlotsDir:            slotsDir,
		SlotStatePath:       filepath.Join(cpampRoot, "slot-state.json"),
		SupervisorDir:       supervisorDir,
		ArtifactRoot:        artifactRoot,
		ArtifactCPARoot:     filepath.Join(artifactRoot, "cpa"),
		JournalPath:         filepath.Join(supervisorDir, "operations.sqlite"),
		GatewayDir:          gatewayDir,
		GatewayAuthDir:      filepath.Join(gatewayDir, "auth"),
		GatewayLogsDir:      filepath.Join(gatewayDir, "logs"),
		GatewayPluginsDir:   filepath.Join(gatewayDir, "plugins"),
		GatewayConfigPath:   filepath.Join(gatewayDir, "config.yaml"),
		TokenDir:            tokenDir,
		TokenFile:           tokenFile,
		LauncherTokenFile:   filepath.Join(tokenDir, "launcher-token"),
	}
}

// ResolveSeedLineagePath returns the explicit seed-lineage path when provided,
// derives sibling seed-lineage.json when seedConfigPath uses a non-default
// installation prefix, or falls back to DefaultSeedLineagePath.
func ResolveSeedLineagePath(seedConfigPath, seedLineagePath string) string {
	if trimmed := strings.TrimSpace(seedLineagePath); trimmed != "" {
		return trimmed
	}
	trimmedSeed := strings.TrimSpace(seedConfigPath)
	if trimmedSeed != "" && trimmedSeed != DefaultSeedConfigPath {
		return filepath.Join(filepath.Dir(trimmedSeed), "seed-lineage.json")
	}
	return DefaultSeedLineagePath
}

// SelectRuntimeRoot parses Linux /proc/self/mountinfo content and returns
// "/runtime" when it is mounted as a separate mount point, or "/data/runtime" otherwise.
func SelectRuntimeRoot(mountinfo []byte) string {
	for _, rawLine := range bytes.Split(mountinfo, []byte{'\n'}) {
		line := strings.TrimSpace(string(rawLine))
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 5 && unescapeMountInfoPath(fields[4]) == DedicatedRuntimeMountPoint {
			return DedicatedRuntimeMountPoint
		}
	}
	return DefaultRuntimeRoot
}

func unescapeMountInfoPath(raw string) string {
	if !strings.ContainsRune(raw, '\\') {
		return raw
	}
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\\' && i+3 < len(raw) &&
			isOctalDigit(raw[i+1]) && isOctalDigit(raw[i+2]) && isOctalDigit(raw[i+3]) {
			val := int(raw[i+1]-'0')<<6 | int(raw[i+2]-'0')<<3 | int(raw[i+3]-'0')
			if val <= 0o377 {
				b.WriteByte(byte(val))
				i += 3
				continue
			}
		}
		b.WriteByte(raw[i])
	}
	return b.String()
}

func isOctalDigit(c byte) bool {
	return c >= '0' && c <= '7'
}
