//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"golang.org/x/sys/unix"
)

const (
	DataDirMode        fs.FileMode = 0o1771
	RuntimeRootMode    fs.FileMode = 0o0711
	SupervisorDirMode  fs.FileMode = 0o0710
	GatewayDirMode     fs.FileMode = 0o0700
	GatewayConfigMode  fs.FileMode = 0o0600
	TokenDirMode       fs.FileMode = 0o0750
	TokenFileMode      fs.FileMode = 0o0640
	BootstrapFileMode  fs.FileMode = 0o0444
	TempSecretFileMode fs.FileMode = 0o0600
)

// FileStat describes the file metadata inspected by startup preparation.
type FileStat struct {
	Mode fs.FileMode
	UID  int
	GID  int
	Size int64
}

// UnixPerm returns the 12-bit Unix permission and special mode bits (0o7777).
func (s FileStat) UnixPerm() fs.FileMode {
	return NormalizeUnixPerm(s.Mode)
}

// NormalizeUnixPerm returns the 12-bit Unix permission mode (0o7777), mapping
// fs.ModeSticky/ModeSetgid/ModeSetuid into 0o1000/0o2000/0o4000.
func NormalizeUnixPerm(mode fs.FileMode) fs.FileMode {
	perm := mode & 0o7777
	if mode&fs.ModeSticky != 0 {
		perm |= 0o1000
	}
	if mode&fs.ModeSetgid != 0 {
		perm |= 0o2000
	}
	if mode&fs.ModeSetuid != 0 {
		perm |= 0o4000
	}
	return perm
}

// ToOSFileMode converts a 12-bit Unix mode (such as 0o1771) into the fs.FileMode
// representation expected by os.Chmod.
func ToOSFileMode(mode fs.FileMode) fs.FileMode {
	osMode := mode & fs.ModePerm
	if mode&0o1000 != 0 || mode&fs.ModeSticky != 0 {
		osMode |= fs.ModeSticky
	}
	if mode&0o2000 != 0 || mode&fs.ModeSetgid != 0 {
		osMode |= fs.ModeSetgid
	}
	if mode&0o4000 != 0 || mode&fs.ModeSetuid != 0 {
		osMode |= fs.ModeSetuid
	}
	return osMode
}

// System abstracts filesystem, ownership, permission, and randomness operations
// so startup preparation can be deterministically unit-tested without root privileges.
type System interface {
	Lstat(path string) (FileStat, error)
	ReadDir(path string) ([]fs.DirEntry, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm fs.FileMode) error
	Mkdir(path string, perm fs.FileMode) error
	Chmod(path string, perm fs.FileMode) error
	Chown(path string, uid, gid int) error
	Lchown(path string, uid, gid int) error
	Link(oldname, newname string) error
	Symlink(oldname, newname string) error
	Readlink(name string) (string, error)
	Remove(path string) error
	ReadRandom(dst []byte) error
	Kill(pid int, sig syscall.Signal) error
	OpenPID(pid int) (PIDHandle, error)
}

// PrepareConfig configures the launcher startup preparation step.
// Zero fields fall back to production defaults.
type PrepareConfig struct {
	DataDir                 string
	RuntimeRoot             string
	MountInfoPath           string
	ProcPath                string
	QuiesceTimeout          time.Duration
	ReapChildren            func()
	TokenFile               string
	SeedConfigPath          string
	PasswdPath              string
	GroupPath               string
	ExplicitRuntimeToken    string
	CPAUIDEnv               string
	CPAGIDEnv               string
	RuntimeAddr             string
	RuntimeIdentity         string
	CPAExecutablePath       string
	CPAArtifactManifestPath string
	RuntimeCPAAddr          string
}

// PreparedRuntime holds the resolved paths, validated identities, and secret
// token produced by startup preparation.
type PreparedRuntime struct {
	Layout                  RuntimeLayout
	DataDir                 string
	RuntimeRoot             string
	BootstrapMarkerPath     string
	BootstrapIdentity       string
	SupervisorDir           string
	ArtifactRoot            string
	ArtifactCPARoot         string
	JournalPath             string
	GatewayDir              string
	GatewayAuthDir          string
	GatewayLogsDir          string
	GatewayPluginsDir       string
	GatewayConfigPath       string
	TokenDir                string
	TokenFile               string
	RuntimeToken            string
	CPAUID                  int
	CPAGID                  int
	ManagerUID              int
	ManagerGID              int
	IngressUID              int
	IngressGID              int
	RuntimeAddr             string
	RuntimeIdentity         string
	CPAExecutablePath       string
	CPAArtifactManifestPath string
	RuntimeCPAAddr          string
}

// SupervisorEnv returns the environment variables required by cpamp-runtime-supervisor.
func (p *PreparedRuntime) SupervisorEnv() []string {
	return []string{
		"CPAMP_RUNTIME_ADDR=" + p.RuntimeAddr,
		"CPAMP_RUNTIME_IDENTITY=" + p.RuntimeIdentity,
		"CPAMP_RUNTIME_TOKEN=" + p.RuntimeToken,
		"CPAMP_RUNTIME_JOURNAL_PATH=" + p.JournalPath,
		"CPAMP_CPA_EXECUTABLE=" + p.CPAExecutablePath,
		"CPAMP_CPA_ARTIFACT_MANIFEST=" + p.CPAArtifactManifestPath,
		"CPAMP_RUNTIME_CPA_ADDR=" + p.RuntimeCPAAddr,
		"CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1",
		"CPAMP_RUNTIME_CPA_UID=" + strconv.Itoa(p.CPAUID),
		"CPAMP_RUNTIME_CPA_GID=" + strconv.Itoa(p.CPAGID),
	}
}

// PrepareConfigFromEnv constructs a PrepareConfig populated from process environment variables.
func PrepareConfigFromEnv() PrepareConfig {
	return PrepareConfig{
		DataDir:                 DefaultDataDir,
		MountInfoPath:           DefaultMountInfoPath,
		ProcPath:                DefaultProcPath,
		QuiesceTimeout:          CPAQuiescenceTimeout,
		TokenFile:               envOrDefault("CPAMP_RUNTIME_TOKEN_FILE", DefaultRuntimeTokenFile),
		SeedConfigPath:          DefaultSeedConfigPath,
		PasswdPath:              DefaultPasswdPath,
		GroupPath:               DefaultGroupPath,
		ExplicitRuntimeToken:    os.Getenv("CPAMP_RUNTIME_TOKEN"),
		CPAUIDEnv:               envOrDefault("CPAMP_RUNTIME_CPA_UID", strconv.Itoa(CPAUID)),
		CPAGIDEnv:               envOrDefault("CPAMP_RUNTIME_CPA_GID", strconv.Itoa(CPAGID)),
		RuntimeAddr:             envOrDefault("CPAMP_RUNTIME_ADDR", DefaultRuntimeAddr),
		RuntimeIdentity:         envOrDefault("CPAMP_RUNTIME_IDENTITY", DefaultRuntimeIdentity),
		CPAExecutablePath:       envOrDefault("CPAMP_CPA_EXECUTABLE", DefaultCPAExecutablePath),
		CPAArtifactManifestPath: envOrDefault("CPAMP_CPA_ARTIFACT_MANIFEST", DefaultCPAArtifactManifestPath),
		RuntimeCPAAddr:          envOrDefault("CPAMP_RUNTIME_CPA_ADDR", DefaultRuntimeCPAAddr),
	}
}

// Prepare performs the launcher startup preparation step.
func Prepare(cfg PrepareConfig, sys System) (*PreparedRuntime, error) {
	return prepare(cfg, sys)
}

func prepare(cfg PrepareConfig, sys System) (*PreparedRuntime, error) {
	return prepareContext(context.Background(), cfg, sys)
}

// prepareContext performs startup preparation, stopping between steps and during
// the recursive ownership walk when ctx is canceled. Cancellation leaves no
// partially written file behind: every multi-step write is atomic.
func prepareContext(ctx context.Context, cfg PrepareConfig, sys System) (*PreparedRuntime, error) {
	if sys == nil {
		sys = DefaultSystem()
	}
	cfg = applyPrepareDefaults(cfg)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateCanonicalServiceAddr("CPAMP_RUNTIME_ADDR", cfg.RuntimeAddr, DefaultRuntimeAddr); err != nil {
		return nil, err
	}
	if err := validateCanonicalServiceAddr("CPAMP_RUNTIME_CPA_ADDR", cfg.RuntimeCPAAddr, DefaultRuntimeCPAAddr); err != nil {
		return nil, err
	}

	cpaUID, cpaGID, err := validateCPAIdentity(cfg, sys)
	if err != nil {
		return nil, err
	}

	reapChildren := cfg.ReapChildren
	if reapChildren == nil {
		reapChildren = childReaperFromContext(ctx)
	}
	if reapChildren == nil {
		if _, isOSSys := sys.(osSystem); isOSSys {
			reapChildren = reapZombieChildrenNonBlocking
		}
	}
	if err := ensureCPAQuiescence(ctx, cfg.ProcPath, cpaUID, cfg.QuiesceTimeout, reapChildren, sys); err != nil {
		return nil, err
	}

	runtimeRoot := cfg.RuntimeRoot
	if runtimeRoot == "" {
		mountinfo, readErr := sys.ReadFile(cfg.MountInfoPath)
		if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("failed to read mountinfo %s: %w", cfg.MountInfoPath, readErr)
		}
		runtimeRoot = SelectRuntimeRoot(mountinfo)
	}

	layout := ResolveRuntimeLayout(cfg.DataDir, runtimeRoot, cfg.TokenFile)

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	seedBytes, err := sys.ReadFile(cfg.SeedConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read seed configuration %s: %w", cfg.SeedConfigPath, err)
	}
	enforceV8, err := shouldEnforceV8Bootstrap(DefaultCPAArtifactManifestPath, sys)
	if err != nil {
		return nil, err
	}
	var bootstrapIdentity string
	bootstrapState := v8BootstrapReady
	if enforceV8 {
		bootstrapIdentity = computeBootstrapIdentity(seedBytes)
		bootstrapState, err = checkV8Bootstrap(layout, seedBytes, bootstrapIdentity, sys)
		if err != nil {
			return nil, err
		}
	}

	if err := checkPreparationState(layout, cfg.ExplicitRuntimeToken, sys); err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := ensureDataDir(layout.DataDir, sys); err != nil {
		return nil, err
	}

	for _, trustedDir := range layout.TrustedRootOwnedDirs() {
		if err := ensureRootOwnedDirectory(trustedDir, sys); err != nil {
			return nil, err
		}
	}

	for _, gatewayDir := range layout.GatewayDirs() {
		if err := ensureRealDirectory(gatewayDir, sys); err != nil {
			return nil, err
		}
	}

	if err := sys.Chown(layout.RuntimeRoot, RootUID, RootGID); err != nil {
		return nil, fmt.Errorf("failed to chown runtime root %s: %w", layout.RuntimeRoot, err)
	}
	if err := sys.Chmod(layout.RuntimeRoot, RuntimeRootMode); err != nil {
		return nil, fmt.Errorf("failed to chmod runtime root %s: %w", layout.RuntimeRoot, err)
	}

	if err := sys.Chown(layout.TokenDir, RootUID, ManagerGID); err != nil {
		return nil, fmt.Errorf("failed to chown token directory %s: %w", layout.TokenDir, err)
	}
	if err := sys.Chmod(layout.TokenDir, TokenDirMode); err != nil {
		return nil, fmt.Errorf("failed to chmod token directory %s: %w", layout.TokenDir, err)
	}

	for _, supDir := range layout.SupervisorDirs() {
		if err := sys.Chown(supDir, RootUID, cpaGID); err != nil {
			return nil, fmt.Errorf("failed to chown supervisor directory %s: %w", supDir, err)
		}
		if err := sys.Chmod(supDir, SupervisorDirMode); err != nil {
			return nil, fmt.Errorf("failed to chmod supervisor directory %s: %w", supDir, err)
		}
	}

	runtimeToken, err := prepareRuntimeToken(layout.TokenFile, cfg.ExplicitRuntimeToken, sys)
	if err != nil {
		return nil, err
	}

	if err := ensureGatewayConfig(layout.GatewayConfigPath, seedBytes, sys); err != nil {
		return nil, err
	}

	if bootstrapState == v8BootstrapInitialize {
		if err := initV8Bootstrap(layout, bootstrapIdentity, sys); err != nil {
			return nil, err
		}
	}

	if err := repairGatewayTree(ctx, layout, cpaUID, cpaGID, sys); err != nil {
		return nil, err
	}
	if layout.RuntimeRoot != DedicatedRuntimeMountPoint {
		if err := ensureRuntimeCompatSymlink(layout.RuntimeRoot, sys); err != nil {
			return nil, err
		}
	}

	return &PreparedRuntime{
		Layout:                  layout,
		DataDir:                 layout.DataDir,
		RuntimeRoot:             layout.RuntimeRoot,
		BootstrapMarkerPath:     layout.BootstrapMarkerPath,
		BootstrapIdentity:       bootstrapIdentity,
		SupervisorDir:           layout.SupervisorDir,
		ArtifactRoot:            layout.ArtifactRoot,
		ArtifactCPARoot:         layout.ArtifactCPARoot,
		JournalPath:             layout.JournalPath,
		GatewayDir:              layout.GatewayDir,
		GatewayAuthDir:          layout.GatewayAuthDir,
		GatewayLogsDir:          layout.GatewayLogsDir,
		GatewayPluginsDir:       layout.GatewayPluginsDir,
		GatewayConfigPath:       layout.GatewayConfigPath,
		TokenDir:                layout.TokenDir,
		TokenFile:               layout.TokenFile,
		RuntimeToken:            runtimeToken,
		CPAUID:                  cpaUID,
		CPAGID:                  cpaGID,
		ManagerUID:              ManagerUID,
		ManagerGID:              ManagerGID,
		IngressUID:              IngressUID,
		IngressGID:              IngressGID,
		RuntimeAddr:             cfg.RuntimeAddr,
		RuntimeIdentity:         cfg.RuntimeIdentity,
		CPAExecutablePath:       cfg.CPAExecutablePath,
		CPAArtifactManifestPath: cfg.CPAArtifactManifestPath,
		RuntimeCPAAddr:          cfg.RuntimeCPAAddr,
	}, nil
}

func applyPrepareDefaults(cfg PrepareConfig) PrepareConfig {
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir
	}
	if cfg.MountInfoPath == "" {
		cfg.MountInfoPath = DefaultMountInfoPath
	}
	if cfg.ProcPath == "" {
		cfg.ProcPath = DefaultProcPath
	}
	if cfg.QuiesceTimeout <= 0 {
		cfg.QuiesceTimeout = CPAQuiescenceTimeout
	}
	if cfg.TokenFile == "" {
		cfg.TokenFile = filepath.Join(cfg.DataDir, "runtime-secret", "token")
	}
	if cfg.SeedConfigPath == "" {
		cfg.SeedConfigPath = DefaultSeedConfigPath
	}
	if cfg.PasswdPath == "" {
		cfg.PasswdPath = DefaultPasswdPath
	}
	if cfg.GroupPath == "" {
		cfg.GroupPath = DefaultGroupPath
	}
	if cfg.CPAUIDEnv == "" {
		cfg.CPAUIDEnv = strconv.Itoa(CPAUID)
	}
	if cfg.CPAGIDEnv == "" {
		cfg.CPAGIDEnv = strconv.Itoa(CPAGID)
	}
	if cfg.RuntimeAddr == "" {
		cfg.RuntimeAddr = DefaultRuntimeAddr
	}
	if cfg.RuntimeIdentity == "" {
		cfg.RuntimeIdentity = DefaultRuntimeIdentity
	}
	if cfg.CPAExecutablePath == "" {
		cfg.CPAExecutablePath = DefaultCPAExecutablePath
	}
	if cfg.CPAArtifactManifestPath == "" {
		cfg.CPAArtifactManifestPath = DefaultCPAArtifactManifestPath
	}
	if cfg.RuntimeCPAAddr == "" {
		cfg.RuntimeCPAAddr = DefaultRuntimeCPAAddr
	}
	return cfg
}

func validateCPAIdentity(cfg PrepareConfig, sys System) (int, int, error) {
	if !isDecimalDigits(cfg.CPAUIDEnv) || !isDecimalDigits(cfg.CPAGIDEnv) {
		return 0, 0, errors.New("Runtime CPA UID/GID must be positive decimal integers")
	}
	uid, errUID := strconv.Atoi(cfg.CPAUIDEnv)
	gid, errGID := strconv.Atoi(cfg.CPAGIDEnv)
	if errUID != nil || errGID != nil || uid <= 0 || gid <= 0 {
		return 0, 0, errors.New("Runtime CPA UID/GID must be positive decimal integers")
	}
	if uid != CPAUID || gid != CPAGID {
		return 0, 0, errors.New("Runtime CPA UID/GID does not match the image-owned cpamp-cpa identity")
	}

	passwdBytes, err := sys.ReadFile(cfg.PasswdPath)
	if err != nil {
		return 0, 0, fmt.Errorf("Runtime CPA UID/GID does not match the image-owned cpamp-cpa identity: %w", err)
	}
	passwdUID, passwdGID, ok := lookupPasswdAccount(passwdBytes, CPAUserName)
	if !ok || passwdUID != uid || passwdGID != gid {
		return 0, 0, errors.New("Runtime CPA UID/GID does not match the image-owned cpamp-cpa identity")
	}

	groupBytes, err := sys.ReadFile(cfg.GroupPath)
	if err != nil {
		return 0, 0, fmt.Errorf("Runtime CPA UID/GID does not match the image-owned cpamp-cpa identity: %w", err)
	}
	groupGID, ok := lookupGroupAccount(groupBytes, CPAUserName)
	if !ok || groupGID != gid {
		return 0, 0, errors.New("Runtime CPA UID/GID does not match the image-owned cpamp-cpa identity")
	}

	return uid, gid, nil
}

// validateCanonicalServiceAddr enforces that embedded internal service
// addresses match their fixed canonical loopback host:port values so the
// listener and internal routing topology cannot diverge.
func validateCanonicalServiceAddr(name, addr, canonical string) error {
	if addr != canonical {
		return fmt.Errorf("%s must be the canonical loopback address %s, got %q", name, canonical, addr)
	}
	return nil
}

func isDecimalDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func lookupPasswdAccount(content []byte, username string) (int, int, bool) {
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 || fields[0] != username {
			continue
		}
		uid, errUID := strconv.Atoi(fields[2])
		gid, errGID := strconv.Atoi(fields[3])
		if errUID != nil || errGID != nil {
			return 0, 0, false
		}
		return uid, gid, true
	}
	return 0, 0, false
}

func lookupGroupAccount(content []byte, groupname string) (int, bool) {
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 3 || fields[0] != groupname {
			continue
		}
		gid, err := strconv.Atoi(fields[2])
		if err != nil {
			return 0, false
		}
		return gid, true
	}
	return 0, false
}

func ensureDataDir(dataDir string, sys System) error {
	exists, err := checkDataDirectory(dataDir, sys)
	if err != nil {
		return err
	}
	if !exists {
		if mkErr := sys.Mkdir(dataDir, DataDirMode); mkErr != nil {
			return fmt.Errorf("failed to create data directory %s: %w", dataDir, mkErr)
		}
		if _, err := checkDataDirectory(dataDir, sys); err != nil {
			return err
		}
	}
	if err := sys.Chown(dataDir, RootUID, ManagerGID); err != nil {
		return fmt.Errorf("failed to chown data directory %s: %w", dataDir, err)
	}
	if err := sys.Chmod(dataDir, DataDirMode); err != nil {
		return fmt.Errorf("failed to chmod data directory %s: %w", dataDir, err)
	}
	return nil
}

func computeBootstrapIdentity(seedBytes []byte) string {
	digest := sha256.Sum256(seedBytes)
	return BootstrapIdentityPrefix + hex.EncodeToString(digest[:])
}

type artifactManifest struct {
	SchemaVersion       int         `json:"schemaVersion"`
	Engine              string      `json:"engine"`
	Version             string      `json:"version"`
	ArtifactID          artifact.ID `json:"artifactId"`
	SourceArchiveDigest string      `json:"sourceArchiveDigest,omitempty"`
}

func shouldEnforceV8Bootstrap(manifestPath string, sys System) (bool, error) {
	if strings.TrimSpace(manifestPath) == "" {
		return false, fmt.Errorf("%w: manifest path is empty", artifact.ErrManifestUnavailable)
	}
	manifestBytes, err := sys.ReadFile(manifestPath)
	if err != nil {
		return false, fmt.Errorf("failed to read CPA artifact manifest %s: %w", manifestPath, err)
	}
	var m artifactManifest
	dec := json.NewDecoder(bytes.NewReader(manifestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return false, fmt.Errorf("failed to parse CPA artifact manifest %s: %w", manifestPath, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("failed to parse CPA artifact manifest %s: %w: manifest must contain one JSON value", manifestPath, artifact.ErrManifestInvalid)
	}
	obs := artifact.Observation{
		Engine:     m.Engine,
		ArtifactID: m.ArtifactID,
		Version:    m.Version,
	}
	if m.SchemaVersion != artifact.ManifestSchemaVersion || m.Version == "" || obs.Validate() != nil {
		return false, fmt.Errorf("invalid CPA artifact manifest %s: %w", manifestPath, artifact.ErrManifestInvalid)
	}
	major, ok := parseCanonicalSemverMajor(m.Version)
	if !ok {
		return false, fmt.Errorf("invalid CPA artifact manifest %s version %q: %w", manifestPath, m.Version, artifact.ErrManifestInvalid)
	}
	return major == 8, nil
}

func parseCanonicalSemverMajor(version string) (int, bool) {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return 0, false
	}
	for _, part := range parts {
		if !isDecimalDigits(part) {
			return 0, false
		}
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil || major <= 0 {
		return 0, false
	}
	return major, true
}

func v8Refuse(reason string) error {
	return fmt.Errorf(
		"Default CPA v8 refuses existing or unknown Runtime data: %s; preserve the volume and use its original image. No automatic migration or rollback.",
		reason,
	)
}

// v8BootstrapState reports whether the volume already carries a trusted v8
// bootstrap identity or must be initialized.
type v8BootstrapState int

const (
	v8BootstrapReady v8BootstrapState = iota
	v8BootstrapInitialize
)

// checkV8Bootstrap performs every v8 refusal check without mutating the volume,
// so an untrusted volume is refused before any chown or write happens.
func checkV8Bootstrap(layout RuntimeLayout, seedBytes []byte, identity string, sys System) (v8BootstrapState, error) {
	rootStat, rootErr := sys.Lstat(layout.RuntimeRoot)
	rootExists := rootErr == nil
	if rootErr != nil && !errors.Is(rootErr, fs.ErrNotExist) {
		return v8BootstrapReady, fmt.Errorf("failed to stat runtime root %s: %w", layout.RuntimeRoot, rootErr)
	}
	if rootExists {
		if rootStat.Mode&fs.ModeSymlink != 0 || !rootStat.Mode.IsDir() {
			return v8BootstrapReady, v8Refuse("invalid Runtime root")
		}
		if rootStat.UID != RootUID {
			return v8BootstrapReady, v8Refuse("untrusted Runtime root owner")
		}
	}

	markerStat, markerErr := sys.Lstat(layout.BootstrapMarkerPath)
	markerExists := markerErr == nil
	if markerErr != nil && !errors.Is(markerErr, fs.ErrNotExist) {
		return v8BootstrapReady, fmt.Errorf("failed to stat bootstrap marker %s: %w", layout.BootstrapMarkerPath, markerErr)
	}

	if markerExists {
		if markerStat.Mode&fs.ModeSymlink != 0 || !markerStat.Mode.IsRegular() {
			return v8BootstrapReady, v8Refuse("invalid bootstrap identity")
		}
		if markerStat.UID != RootUID || markerStat.GID != RootGID || markerStat.UnixPerm() != BootstrapFileMode {
			return v8BootstrapReady, v8Refuse("untrusted bootstrap identity")
		}
		markerBytes, err := sys.ReadFile(layout.BootstrapMarkerPath)
		if err != nil {
			return v8BootstrapReady, v8Refuse("invalid bootstrap identity")
		}
		markerContent := strings.TrimRight(string(markerBytes), "\n")
		if markerContent != identity {
			return v8BootstrapReady, v8Refuse("different bootstrap identity")
		}

		gwStat, gwErr := sys.Lstat(layout.GatewayDir)
		if gwErr != nil || gwStat.Mode&fs.ModeSymlink != 0 || !gwStat.Mode.IsDir() {
			return v8BootstrapReady, v8Refuse("invalid Gateway directory")
		}

		cfgStat, cfgErr := sys.Lstat(layout.GatewayConfigPath)
		if cfgErr != nil || cfgStat.Mode&fs.ModeSymlink != 0 || !cfgStat.Mode.IsRegular() {
			return v8BootstrapReady, v8Refuse("missing or invalid bootstrap config")
		}

		cfgBytes, err := sys.ReadFile(layout.GatewayConfigPath)
		if err != nil || !bytes.Equal(seedBytes, cfgBytes) {
			return v8BootstrapReady, v8Refuse("modified or incompatible bootstrap config")
		}
		return v8BootstrapReady, nil
	}

	if rootExists {
		entries, err := sys.ReadDir(layout.RuntimeRoot)
		if err != nil {
			return v8BootstrapReady, fmt.Errorf("failed to read runtime root %s: %w", layout.RuntimeRoot, err)
		}
		if len(entries) > 0 {
			return v8BootstrapReady, v8Refuse("nonempty volume without v8 bootstrap identity")
		}
	}
	return v8BootstrapInitialize, nil
}

// initV8Bootstrap atomically installs the trusted bootstrap identity marker
// after the runtime root, gateway directory, and initial gateway config exist.
func initV8Bootstrap(layout RuntimeLayout, identity string, sys System) error {
	tmpFile := fmt.Sprintf("%s.tmp.%d", layout.BootstrapMarkerPath, os.Getpid())
	_ = sys.Remove(tmpFile)
	if err := sys.WriteFile(tmpFile, []byte(identity+"\n"), BootstrapFileMode); err != nil {
		_ = sys.Remove(tmpFile)
		return fmt.Errorf("failed to write bootstrap marker %s: %w", layout.BootstrapMarkerPath, err)
	}
	if err := sys.Chown(tmpFile, RootUID, RootGID); err != nil {
		_ = sys.Remove(tmpFile)
		return fmt.Errorf("failed to chown bootstrap marker %s: %w", layout.BootstrapMarkerPath, err)
	}
	if err := sys.Chmod(tmpFile, BootstrapFileMode); err != nil {
		_ = sys.Remove(tmpFile)
		return fmt.Errorf("failed to chmod bootstrap marker %s: %w", layout.BootstrapMarkerPath, err)
	}
	if err := sys.Link(tmpFile, layout.BootstrapMarkerPath); err != nil {
		_ = sys.Remove(tmpFile)
		return fmt.Errorf("failed to install bootstrap marker %s: %w", layout.BootstrapMarkerPath, err)
	}
	_ = sys.Remove(tmpFile)
	return nil
}

func ensureRootOwnedDirectory(dir string, sys System) error {
	exists, err := checkRootOwnedDirectory(dir, sys)
	if err != nil {
		return err
	}
	if !exists {
		if mkErr := sys.Mkdir(dir, SupervisorDirMode); mkErr != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, mkErr)
		}
		return requireRootOwnedDirectory(dir, sys)
	}
	return nil
}

func ensureRealDirectory(dir string, sys System) error {
	exists, err := checkRealGatewayDirectory(dir, sys)
	if err != nil {
		return err
	}
	if !exists {
		if mkErr := sys.Mkdir(dir, GatewayDirMode); mkErr != nil {
			return fmt.Errorf("failed to create gateway directory %s: %w", dir, mkErr)
		}
		return requireRealGatewayDirectory(dir, sys)
	}
	return nil
}

func validateRuntimeToken(candidate string) error {
	for i := range len(candidate) {
		if candidate[i] < 0x21 || candidate[i] > 0x7e {
			return errors.New("Runtime transport secret must not contain whitespace")
		}
	}
	if len(candidate) < 32 {
		return errors.New("Runtime transport secret must contain at least 32 bytes")
	}
	return nil
}

func prepareRuntimeToken(tokenFile, explicitToken string, sys System) (string, error) {
	runtimeToken, err := checkRuntimeToken(tokenFile, explicitToken, sys)
	if err != nil {
		return "", err
	}
	if runtimeToken == "" {
		candidate := explicitToken
		if candidate == "" {
			var randomBytes [32]byte
			if randErr := sys.ReadRandom(randomBytes[:]); randErr != nil {
				return "", fmt.Errorf("failed to generate Runtime transport secret: %w", randErr)
			}
			candidate = hex.EncodeToString(randomBytes[:])
		}
		tmpFile := fmt.Sprintf("%s.tmp.%d", tokenFile, os.Getpid())
		_ = sys.Remove(tmpFile)
		if writeErr := sys.WriteFile(tmpFile, []byte(candidate), TempSecretFileMode); writeErr != nil {
			_ = sys.Remove(tmpFile)
			return "", errors.New("failed to initialize Runtime transport secret")
		}
		if chmodErr := sys.Chmod(tmpFile, TempSecretFileMode); chmodErr != nil {
			_ = sys.Remove(tmpFile)
			return "", errors.New("failed to initialize Runtime transport secret")
		}
		linkErr := sys.Link(tmpFile, tokenFile)
		_ = sys.Remove(tmpFile)
		if linkErr != nil {
			checkStat, checkErr := sys.Lstat(tokenFile)
			if checkErr != nil || checkStat.Mode&fs.ModeSymlink != 0 || !checkStat.Mode.IsRegular() {
				return "", errors.New("failed to initialize Runtime transport secret")
			}
		}
	}

	runtimeToken, err = checkRuntimeToken(tokenFile, explicitToken, sys)
	if err != nil {
		return "", err
	}
	if runtimeToken == "" {
		return "", errors.New("failed to initialize Runtime transport secret")
	}
	if err := sys.Chown(tokenFile, RootUID, ManagerGID); err != nil {
		return "", fmt.Errorf("failed to chown token file %s: %w", tokenFile, err)
	}
	if err := sys.Chmod(tokenFile, TokenFileMode); err != nil {
		return "", fmt.Errorf("failed to chmod token file %s: %w", tokenFile, err)
	}
	return runtimeToken, nil
}

func ensureGatewayConfig(configPath string, seedBytes []byte, sys System) error {
	exists, err := checkGatewayConfigFile(configPath, sys)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", configPath, os.Getpid())
	_ = sys.Remove(tmpFile)
	if writeErr := sys.WriteFile(tmpFile, seedBytes, GatewayConfigMode); writeErr != nil {
		_ = sys.Remove(tmpFile)
		return errors.New("failed to initialize Gateway configuration")
	}
	if chmodErr := sys.Chmod(tmpFile, GatewayConfigMode); chmodErr != nil {
		_ = sys.Remove(tmpFile)
		return errors.New("failed to initialize Gateway configuration")
	}
	linkErr := sys.Link(tmpFile, configPath)
	_ = sys.Remove(tmpFile)
	if linkErr != nil {
		if ok, checkErr := checkGatewayConfigFile(configPath, sys); checkErr != nil || !ok {
			return errors.New("failed to initialize Gateway configuration")
		}
	}
	return nil
}

func repairGatewayTree(ctx context.Context, layout RuntimeLayout, cpaUID, cpaGID int, sys System) error {
	if _, isOSSys := sys.(osSystem); isOSSys {
		return repairGatewayTreeNoFollowFD(ctx, layout, cpaUID, cpaGID)
	}
	if err := lchownGatewayTreeNoFollow(ctx, layout.GatewayDir, layout.GatewayDir, cpaUID, cpaGID, sys); err != nil {
		return err
	}
	for _, gatewayDir := range layout.GatewayDirs() {
		if err := requireRealGatewayDirectory(gatewayDir, sys); err != nil {
			return err
		}
		if err := sys.Chmod(gatewayDir, GatewayDirMode); err != nil {
			return fmt.Errorf("failed to chmod gateway directory %s: %w", gatewayDir, err)
		}
	}
	return nil
}

func repairGatewayTreeNoFollowFD(ctx context.Context, layout RuntimeLayout, cpaUID, cpaGID int) error {
	rootFD, err := openRealDirFD(unix.AT_FDCWD, layout.GatewayDir, layout.GatewayDir)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)

	if err := walkGatewayDirFD(ctx, rootFD, layout.GatewayDir, cpaUID, cpaGID); err != nil {
		return err
	}
	if err := unix.Fchmod(rootFD, uint32(NormalizeUnixPerm(GatewayDirMode))); err != nil {
		return fmt.Errorf("failed to chmod gateway directory %s: %w", layout.GatewayDir, err)
	}
	for _, fullSubPath := range layout.GatewayDirs() {
		if fullSubPath == layout.GatewayDir {
			continue
		}
		subName := filepath.Base(fullSubPath)
		subFD, openErr := openRealDirFD(rootFD, subName, fullSubPath)
		if openErr != nil {
			return openErr
		}
		chownErr := unix.Fchown(subFD, cpaUID, cpaGID)
		chmodErr := unix.Fchmod(subFD, uint32(NormalizeUnixPerm(GatewayDirMode)))
		_ = unix.Close(subFD)
		if chownErr != nil {
			return fmt.Errorf("failed to chown gateway directory %s: %w", fullSubPath, chownErr)
		}
		if chmodErr != nil {
			return fmt.Errorf("failed to chmod gateway directory %s: %w", fullSubPath, chmodErr)
		}
	}
	return nil
}

func openRealDirFD(parentFD int, name, displayPath string) (int, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("Gateway state path is not a real directory: %s: %w", displayPath, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || (st.Mode&unix.S_IFMT) != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("Gateway state path is not a real directory: %s", displayPath)
	}
	return fd, nil
}

func readDirFromFD(dirFD int, displayPath string) ([]fs.DirEntry, error) {
	dupFD, err := unix.Dup(dirFD)
	if err != nil {
		return nil, fmt.Errorf("failed to dup directory fd for %s: %w", displayPath, err)
	}
	unix.CloseOnExec(dupFD)
	f := os.NewFile(uintptr(dupFD), displayPath)
	if f == nil {
		_ = unix.Close(dupFD)
		return nil, fmt.Errorf("failed to wrap directory fd for %s", displayPath)
	}
	entries, readErr := f.ReadDir(-1)
	_ = f.Close()
	if readErr != nil {
		return nil, fmt.Errorf("failed to read directory %s: %w", displayPath, readErr)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return entries, nil
}

func readDirNoFollow(path string) ([]fs.DirEntry, error) {
	fd, err := openRealDirFD(unix.AT_FDCWD, path, path)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	return readDirFromFD(fd, path)
}

func walkGatewayDirFD(ctx context.Context, dirFD int, dirPath string, uid, gid int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Fchown(dirFD, uid, gid); err != nil {
		return fmt.Errorf("failed to fchown %s: %w", dirPath, err)
	}
	entries, err := readDirFromFD(dirFD, dirPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		childPath := filepath.Join(dirPath, name)
		var st unix.Stat_t
		if err := unix.Fstatat(dirFD, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("failed to fstatat %s: %w", childPath, err)
		}
		if (st.Mode & unix.S_IFMT) == unix.S_IFDIR {
			childFD, openErr := openRealDirFD(dirFD, name, childPath)
			if openErr != nil {
				return openErr
			}
			walkErr := walkGatewayDirFD(ctx, childFD, childPath, uid, gid)
			_ = unix.Close(childFD)
			if walkErr != nil {
				return walkErr
			}
			continue
		}
		if err := unix.Fchownat(dirFD, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("failed to fchownat %s: %w", childPath, err)
		}
	}
	return nil
}

func requireGatewayAncestorsRealDirs(rootDir, targetPath string, sys System) error {
	cleanRoot := filepath.Clean(rootDir)
	cleanTarget := filepath.Clean(targetPath)
	if cleanTarget == cleanRoot {
		return requireRealGatewayDirectory(cleanRoot, sys)
	}
	rel, err := filepath.Rel(cleanRoot, filepath.Dir(cleanTarget))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Gateway state path is outside root %s: %s", cleanRoot, cleanTarget)
	}
	cur := cleanRoot
	if err := requireRealGatewayDirectory(cur, sys); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		if err := requireRealGatewayDirectory(cur, sys); err != nil {
			return err
		}
	}
	return nil
}

func lchownGatewayTreeNoFollow(ctx context.Context, rootDir, path string, uid, gid int, sys System) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireGatewayAncestorsRealDirs(rootDir, path, sys); err != nil {
		return err
	}
	stat, err := sys.Lstat(path)
	if err != nil {
		return fmt.Errorf("failed to lstat %s: %w", path, err)
	}
	if err := sys.Lchown(path, uid, gid); err != nil {
		return fmt.Errorf("failed to lchown %s: %w", path, err)
	}
	if stat.Mode&fs.ModeSymlink != 0 || !stat.Mode.IsDir() {
		return nil
	}
	entries, err := sys.ReadDir(path)
	if err != nil {
		return fmt.Errorf("Gateway state path is not a real directory: %s: %w", path, err)
	}
	if err := requireRealGatewayDirectory(path, sys); err != nil {
		return err
	}
	for _, entry := range entries {
		childPath := filepath.Join(path, entry.Name())
		if err := lchownGatewayTreeNoFollow(ctx, rootDir, childPath, uid, gid, sys); err != nil {
			return err
		}
	}
	return nil
}

type reapContextKey struct{}

func withChildReaper(ctx context.Context, reap func()) context.Context {
	if reap == nil {
		return ctx
	}
	return context.WithValue(ctx, reapContextKey{}, reap)
}

func childReaperFromContext(ctx context.Context) func() {
	if ctx == nil {
		return nil
	}
	if fn, ok := ctx.Value(reapContextKey{}).(func()); ok {
		return fn
	}
	return nil
}

func reapZombieChildrenNonBlocking() {
	for {
		var status syscall.WaitStatus
		wpid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if wpid > 0 {
			continue
		}
		if err == nil || errors.Is(err, syscall.ECHILD) || errors.Is(err, syscall.EINTR) {
			return
		}
		return
	}
}
func envOrDefault(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

type osSystem struct{}

// DefaultSystem returns the real OS-backed System implementation.
func DefaultSystem() System {
	return osSystem{}
}

func (osSystem) Lstat(path string) (FileStat, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return FileStat{}, err
	}
	uid, gid, ok := extractStatUIDGID(info)
	if !ok {
		return FileStat{}, fmt.Errorf("numeric file owner unavailable for %s", path)
	}
	mode := info.Mode()
	if mode&fs.ModeSticky != 0 {
		mode |= 0o1000
	}
	if mode&fs.ModeSetgid != 0 {
		mode |= 0o2000
	}
	if mode&fs.ModeSetuid != 0 {
		mode |= 0o4000
	}
	return FileStat{
		Mode: mode,
		UID:  uid,
		GID:  gid,
		Size: info.Size(),
	}, nil
}

func (osSystem) ReadDir(path string) ([]fs.DirEntry, error) {
	return readDirNoFollow(path)
}

func (osSystem) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (osSystem) WriteFile(path string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(path, data, ToOSFileMode(perm))
}

func (osSystem) Mkdir(path string, perm fs.FileMode) error {
	return os.Mkdir(path, ToOSFileMode(perm))
}

func (osSystem) Chmod(path string, perm fs.FileMode) error {
	fd, err := unix.Openat(unix.AT_FDCWD, path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Fchmod(fd, uint32(NormalizeUnixPerm(perm)))
}

func (osSystem) Chown(path string, uid, gid int) error {
	return os.Chown(path, uid, gid)
}

func (osSystem) Lchown(path string, uid, gid int) error {
	return os.Lchown(path, uid, gid)
}

func (osSystem) Link(oldname, newname string) error {
	return os.Link(oldname, newname)
}

func (osSystem) Symlink(oldname, newname string) error {
	return os.Symlink(oldname, newname)
}

func (osSystem) Readlink(name string) (string, error) {
	return os.Readlink(name)
}

func ensureRuntimeCompatSymlink(runtimeRoot string, sys System) error {
	state, err := checkRuntimeCompatSymlinkState(runtimeRoot, sys)
	if err != nil {
		return err
	}
	switch state {
	case compatSymlinkValid:
		return sys.Lchown(DedicatedRuntimeMountPoint, RootUID, RootGID)
	case compatSymlinkEmptyDir:
		if rmErr := sys.Remove(DedicatedRuntimeMountPoint); rmErr != nil {
			return fmt.Errorf("failed to remove empty %s directory: %w", DedicatedRuntimeMountPoint, rmErr)
		}
	}

	if err := sys.Symlink(runtimeRoot, DedicatedRuntimeMountPoint); err != nil {
		return fmt.Errorf("failed to symlink %s -> %s: %w", DedicatedRuntimeMountPoint, runtimeRoot, err)
	}
	if err := sys.Lchown(DedicatedRuntimeMountPoint, RootUID, RootGID); err != nil {
		return fmt.Errorf("failed to lchown %s: %w", DedicatedRuntimeMountPoint, err)
	}
	return nil
}

func (osSystem) Remove(path string) error {
	return os.Remove(path)
}

func (osSystem) ReadRandom(dst []byte) error {
	_, err := rand.Read(dst)
	return err
}

func (osSystem) Kill(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}

func (osSystem) OpenPID(pid int) (PIDHandle, error) {
	return openOSPIDHandle(pid)
}

func extractStatUIDGID(info fs.FileInfo) (int, int, bool) {
	if info == nil || info.Sys() == nil {
		return 0, 0, false
	}
	val := reflect.ValueOf(info.Sys())
	if val.Kind() == reflect.Pointer {
		if val.IsNil() {
			return 0, 0, false
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return 0, 0, false
	}
	uidField := val.FieldByName("Uid")
	gidField := val.FieldByName("Gid")
	if !uidField.IsValid() || !gidField.IsValid() {
		return 0, 0, false
	}
	uid, okUID := uintFieldToInt(uidField)
	gid, okGID := uintFieldToInt(gidField)
	if !okUID || !okGID {
		return 0, 0, false
	}
	return uid, gid, true
}

func uintFieldToInt(field reflect.Value) (int, bool) {
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(field.Uint()), true
	default:
		return 0, false
	}
}
