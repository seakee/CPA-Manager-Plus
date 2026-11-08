//go:build unix

package main

import (
	"bytes"
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
	DefaultRuntimeSecretDir         = "/data/runtime-secret"
	DefaultRuntimeTokenFile         = "/data/runtime-secret/token"
	DefaultSeedConfigPath           = "/usr/local/share/cpamp/runtime/config.seed.yaml"
	DefaultSupervisorExecutablePath = "/usr/local/bin/cpamp-runtime-supervisor"
	DefaultManagerExecutablePath    = "/usr/local/bin/cpa-manager-plus"
	DefaultIngressExecutablePath    = "/usr/local/bin/cpamp-ingress"
	DefaultCPAExecutablePath        = "/usr/local/bin/cli-proxy-api"
	DefaultCPAArtifactManifestPath  = "/usr/local/share/cpamp/runtime/cpa-artifact.json"
	DefaultPasswdPath               = "/etc/passwd"
	DefaultGroupPath                = "/etc/group"

	DefaultRuntimeIdentity   = "embedded-default"
	DefaultRuntimeAddr       = "127.0.0.1:9081"
	DefaultManagerAddr       = "127.0.0.1:18319"
	DefaultIngressPublicAddr = "0.0.0.0:18317"
	DefaultIngressAdminAddr  = "127.0.0.1:9082"
	DefaultRuntimeCPAAddr    = "127.0.0.1:8317"

	BootstrapMarkerName     = ".cpamp-v8-bootstrap"
	BootstrapIdentityPrefix = "official-cpa-v8-bootstrap:"

	InitialRestartBackoff                 = 1 * time.Second
	MaxRestartBackoff                     = 30 * time.Second
	HealthyUptimeResetThreshold           = 60 * time.Second
	SupervisorShortRunWindow              = 10 * time.Second
	SupervisorMaxConsecutiveShortFailures = 5

	DefaultDrainTimeout          = 90 * time.Second
	IngressDrainGraceExtra       = 5 * time.Second
	ManagerShutdownTimeout       = 15 * time.Second
	SupervisorShutdownTimeout    = 15 * time.Second
	ProcessGroupKillGrace        = 5 * time.Second
	SIGKILLReapWait              = 500 * time.Millisecond
	DefaultReadinessPollInterval = 100 * time.Millisecond
	ReadinessLogInterval         = 15 * time.Second

	// ShutdownFixedBudgetLimit bounds every stop-stage budget that is not the
	// configurable drain window. The container stop grace must be at least
	// CPAMP_DRAIN_TIMEOUT + this limit (default grace 150s = 90s + 60s).
	ShutdownFixedBudgetLimit = 60 * time.Second
)

// ShutdownFixedBudget returns the worst-case time the launcher can spend on
// shutdown stages other than the configurable drain window.
func ShutdownFixedBudget() time.Duration {
	return IngressDrainGraceExtra + ManagerShutdownTimeout + SupervisorShutdownTimeout + ProcessGroupKillGrace + SIGKILLReapWait
}

// RuntimeLayout holds the resolved filesystem paths produced for a runtime root.
type RuntimeLayout struct {
	DataDir             string
	RuntimeRoot         string
	BootstrapMarkerPath string
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
	supervisorDir := filepath.Join(runtimeRoot, "supervisor")
	artifactRoot := filepath.Join(supervisorDir, "artifacts")
	gatewayDir := filepath.Join(runtimeRoot, "gateway")
	return RuntimeLayout{
		DataDir:             dataDir,
		RuntimeRoot:         runtimeRoot,
		BootstrapMarkerPath: filepath.Join(runtimeRoot, BootstrapMarkerName),
		SupervisorDir:       supervisorDir,
		ArtifactRoot:        artifactRoot,
		ArtifactCPARoot:     filepath.Join(artifactRoot, "cpa"),
		JournalPath:         filepath.Join(supervisorDir, "operations.sqlite"),
		GatewayDir:          gatewayDir,
		GatewayAuthDir:      filepath.Join(gatewayDir, "auth"),
		GatewayLogsDir:      filepath.Join(gatewayDir, "logs"),
		GatewayPluginsDir:   filepath.Join(gatewayDir, "plugins"),
		GatewayConfigPath:   filepath.Join(gatewayDir, "config.yaml"),
		TokenDir:            filepath.Dir(tokenFile),
		TokenFile:           tokenFile,
	}
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
	return strings.ReplaceAll(raw, `\040`, " ")
}
