package upgrade

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/maintenancebundle"
)

// Rollback is modelled, never executed here. The Manager runs inside a
// container and must not drive the Docker socket, so a rollback is a plan the
// operator applies plus a verdict the Manager records. Nothing below writes
// outside the v2 data root and the operation row, and nothing writes to the 1.x
// source.
const (
	// rollbackWorkDirectoryName is this operation's report area inside the v2
	// data root. The plan is evidence for the operator, not an authorization.
	rollbackWorkDirectoryName = ".upgrade-rollback"
	rollbackPlanFileName      = "plan.json"

	// LegacyImageRepository is the 1.x Manager image. The rollback pins it to
	// the exact version the operation observed; a moving tag is refused.
	LegacyImageRepository = "seakee/cpa-manager-plus"
	LegacyComposeProject  = "cpamp"
	LegacyComposeService  = "cpa-manager-plus"
	// LegacyVolumeSuffix is how the 1.x compose file names its data volume
	// below the compose project. The full name is derived from the project the
	// plan restarts the stack under, never assumed, and the container path it
	// is mounted at is stated beside it so the binding is checkable.
	LegacyVolumeSuffix = "_cpa-manager-plus-data"
	// LegacyVolumeName is the volume of the default 1.x compose project. A plan
	// for another project derives its own name from that project.
	LegacyVolumeName       = LegacyComposeProject + LegacyVolumeSuffix
	LegacyVolumeMountPoint = "/data"
	// ManagerDefaultPort is the port the v2 Manager publishes unless it is
	// configured otherwise. The 1.x manager defaulted to the same number and the
	// two state the same service identity on GET /health, so a rollback cannot
	// prove itself against this port: a 2xx there may be the v2 stack describing
	// itself.
	ManagerDefaultPort = 18317
	// LegacyRollbackPort is the port the rollback plan publishes the restarted
	// 1.x manager on. It is deliberately beside ManagerDefaultPort: while the v2
	// stack still holds its port, nothing can answer a probe on this one as v2,
	// and a plan whose port is already taken is refused instead of applied.
	LegacyRollbackPort = ManagerDefaultPort + 1
	// LegacyHealthPath is the one unauthenticated endpoint the 1.x manager
	// serves. Every other endpoint of the 1.x surface requires credentials, so
	// a rollback proves the volume with this endpoint and the identity it
	// states, not with a bare 2xx.
	LegacyHealthPath = "/health"
	// LegacyServiceID is the identity the 1.x manager states on GET /health as
	// {"ok":true,"service":"cpa-manager-plus"}. A probe that answers 2xx for a
	// different identity is refused: something else is listening. The v2 Manager
	// states the same identity, which is why the probe target is checked against
	// this Manager's own listener before the answer is trusted.
	LegacyServiceID = "cpa-manager-plus"

	// rollbackEvidenceFreshness bounds how old an observation may be when it is
	// recorded. An operator who probed the 1.x stack hours ago has not observed
	// the install that is being recorded now.
	rollbackEvidenceFreshness = 15 * time.Minute
	// rollbackEvidenceFutureSkew tolerates a small clock difference between the
	// operator's host and the Manager.
	rollbackEvidenceFutureSkew = 2 * time.Minute

	// maxHealthBodyBytes bounds the health response read: the contract is a
	// two-field object.
	maxHealthBodyBytes = 4 * 1024
	// maxReportedIdentity bounds an identity echoed back in a refusal.
	maxReportedIdentity = 64
)

// composeProjectPattern is the compose project name alphabet. A name outside it
// cannot name a volume, so it is refused instead of being turned into one.
var composeProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// rollbackApplicableStages are the recorded stages a rollback may start from.
// A rolled-back row is deliberately absent: the verdict is recorded once.
var rollbackApplicableStages = map[model.UpgradeStage]bool{
	model.UpgradeStageCopyIn:          true,
	model.UpgradeStageReconcile:       true,
	model.UpgradeStageCPAImport:       true,
	model.UpgradeStageExternalConnect: true,
	model.UpgradeStageVerify:          true,
	model.UpgradeStageComplete:        true,
}

// RollbackOptions addresses the two roots a rollback decision needs. The
// operation row is read from the v2 database inside TargetRoot.
type RollbackOptions struct {
	// LegacyRoot is the 1.x data directory the operation copied out of.
	LegacyRoot string
	// TargetRoot is the v2 data directory that holds the operation row and
	// receives the plan report.
	TargetRoot string
	// BackupDir is the recorded backup snapshot the plan verifies against.
	// Empty uses the v2 default backup directory inside TargetRoot.
	BackupDir string
	// TagOverride replaces the derived v1 image tag. An empty value derives
	// v<source_version> from the recorded row.
	TagOverride string
	// Project is the compose project the 1.x stack ran under; it names the
	// volume. Empty uses the default 1.x project.
	Project string
	// Port is the port the plan publishes the restarted 1.x manager on. Empty
	// uses LegacyRollbackPort, which never collides with the v2 Manager's own
	// port.
	Port int
	// ManagerAddress is the v2 Manager's own listen address (host:port). The plan
	// may not publish its port, and when the operator names it the plan also
	// refuses while the v2 stack still answers there. Empty assumes the
	// documented Manager default port.
	ManagerAddress string
	// AllowImageFamilyMismatch accepts a 1.x image from another version family
	// than the recorded source. It is the explicit override; the default keeps
	// the rollback on the version the operation copied.
	AllowImageFamilyMismatch bool
}

// Plan is the rollback instruction set. It names the exact image to pin, the
// volume and mountpoint the 1.x stack restarts against, and the exact compose
// invocation, so the operator never has to guess. It grants nothing: only
// RecordRollback changes the operation.
type Plan struct {
	TargetRoot string `json:"targetRoot"`
	// SourceRoot is the 1.x data directory the operation recorded and this plan
	// was derived from. The volume claim rests on it.
	SourceRoot    string `json:"sourceRoot"`
	SourceVersion string `json:"sourceVersion"`
	Image         string `json:"image"`
	// ImageFamilyOverride records that the plan pins a release outside the
	// recorded source's family because the operator asked for it, so the verify
	// command printed with the plan stays exact.
	ImageFamilyOverride bool   `json:"imageFamilyOverride,omitempty"`
	Volume              string `json:"volume"`
	VolumeMountPoint    string `json:"volumeMountPoint"`
	ComposeProject      string `json:"composeProject"`
	ComposeService      string `json:"composeService"`
	ComposeInvocation   string `json:"composeInvocation"`
	ComposeFile         string `json:"composeFile"`
	// Port is the host port the plan publishes the 1.x manager on. It never
	// collides with the port the v2 Manager holds, so an answer there cannot be
	// the v2 stack describing itself.
	Port int `json:"port"`
	// ManagerPort is the port the v2 Manager listens on. The plan states it so
	// the operator knows which listener has to be gone before a probe can only
	// reach v1.
	ManagerPort int `json:"managerPort"`
	// ManagerAddress is the v2 Manager address the plan was derived against, when
	// the operator named one. The printed verify command carries it, so the
	// verdict refuses a probe of that listener too.
	ManagerAddress string `json:"managerAddress,omitempty"`
	// BackupDirectory is the recorded snapshot the verdict re-verifies. The
	// printed verify command names it, so the operator cannot record against a
	// different copy.
	BackupDirectory      string             `json:"backupDirectory"`
	Preconditions        []string           `json:"preconditions"`
	HealthChecks         []string           `json:"healthChecks"`
	SourceFingerprint    string             `json:"sourceFingerprint"`
	BackupManifestSHA256 string             `json:"backupManifestSha256"`
	Stage                model.UpgradeStage `json:"stage"`
	ReportPath           string             `json:"reportPath"`
	Digest               string             `json:"digest"`
}

// PrepareRollback derives the rollback plan from the recorded operation and the
// 1.x volume. It refuses unless the operation is in a rollback-applicable stage
// (upgrade_rollback_not_applicable), the directory under decision is the one the
// operation recorded (upgrade_rollback_volume_unbound), the source is still the
// volume the operation described (upgrade_source_changed), the evidence the plan
// rests on still holds (upgrade_rollback_unproven), the 1.x image resolves to an
// exact release in the recorded version family (upgrade_rollback_image_unpinned,
// upgrade_rollback_image_family), the plan's port is neither the port this
// Manager holds (upgrade_rollback_port_collision) nor taken by anything else
// (upgrade_rollback_port_busy, upgrade_rollback_v2_running) and the two roots do
// not contain each other (upgrade_rollback_roots_overlap). A refusal writes
// nothing.
//
// A row written before the source-root column existed is bound once by the
// fingerprint the copy-in recorded: that is the only path that can still record
// which direction the rollback restores, because the copy-in refuses to run
// again after it is recorded.
func PrepareRollback(ctx context.Context, options RollbackOptions) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if strings.TrimSpace(options.LegacyRoot) == "" || strings.TrimSpace(options.TargetRoot) == "" {
		return Plan{}, errors.New("a rollback plan needs the 1.x source root and the v2 data root")
	}
	sourceRoot, err := resolveLegacyRoot(options.LegacyRoot)
	if err != nil {
		return Plan{}, err
	}
	if sourceRoot, err = canonicalPath(sourceRoot); err != nil {
		return Plan{}, fmt.Errorf("resolve the 1.x source root: %w", err)
	}
	targetRoot, err := resolveTargetRoot(options.TargetRoot)
	if err != nil {
		return Plan{}, err
	}
	if targetRoot, err = canonicalPath(targetRoot); err != nil {
		return Plan{}, fmt.Errorf("resolve the v2 data root: %w", err)
	}
	if err := refuseRootOverlap(sourceRoot, targetRoot); err != nil {
		return Plan{}, err
	}
	recorded, found, err := readRollbackRecord(ctx, targetRoot)
	if err != nil {
		return Plan{}, fmt.Errorf("read the recorded upgrade operation: %w", err)
	}
	if !found {
		return Plan{}, newUpgradeError(UpgradeErrorRollbackNotApplicable,
			"no upgrade operation is recorded, so there is nothing to roll back", nil)
	}
	if !rollbackApplicableStages[recorded.Stage] {
		return Plan{}, newUpgradeError(UpgradeErrorRollbackNotApplicable,
			fmt.Sprintf("the operation is recorded at stage %q, which a rollback cannot start from", string(recorded.Stage)), nil)
	}
	fingerprint, _, err := fingerprintSource(sourceRoot)
	if err != nil {
		return Plan{}, fmt.Errorf("fingerprint the 1.x source: %w", err)
	}
	if recorded.SourceFingerprintBefore == "" || recorded.SourceFingerprintAfter == "" {
		return Plan{}, newUpgradeError(UpgradeErrorRollbackUnproven,
			"the recorded operation carries no before/after source fingerprint, so a rollback cannot be shown safe", nil)
	}
	// A row that names no directory has nothing to compare against yet: the
	// binding decides whether this directory can still be proven to be the one
	// the copy-in read, and refuses with what works when it cannot.
	if strings.TrimSpace(recorded.SourceRoot) != "" && fingerprint != recorded.SourceFingerprintBefore {
		return Plan{}, newUpgradeError(UpgradeErrorSourceChanged,
			"the 1.x source is not the volume this operation copied; a v1 restart would not restore the recorded state", nil)
	}
	if err := requireBoundedSourceRoot(recorded, sourceRoot, fingerprint); err != nil {
		return Plan{}, err
	}
	if strings.TrimSpace(recorded.BackupManifestSHA256) == "" {
		return Plan{}, newUpgradeError(UpgradeErrorRollbackUnproven,
			"the recorded operation names no backup manifest, so the copied-out state cannot be re-verified", nil)
	}
	image, err := rollbackImage(recorded, options.TagOverride, options.AllowImageFamilyMismatch)
	if err != nil {
		return Plan{}, err
	}
	project, volume, err := rollbackVolume(options.Project)
	if err != nil {
		return Plan{}, err
	}
	// The port the plan publishes and the v2 stack's own port are decided before
	// anything is written: a plan is only rollback-ready when the restarted v1
	// can answer where v2 cannot.
	port, err := rollbackPlanPort(options.Port, options.ManagerAddress)
	if err != nil {
		return Plan{}, err
	}
	if err := requireV2Stopped(ctx, options.ManagerAddress); err != nil {
		return Plan{}, err
	}
	bundleDir, err := rollbackBundleDir(options.BackupDir, targetRoot)
	if err != nil {
		return Plan{}, err
	}
	// Every refusal above wrote nothing. Binding the recorded root may backfill a
	// row written before the column existed, which is a write to the v2 store, so
	// it happens last.
	recorded, err = bindSourceRoot(ctx, recorded, targetRoot, sourceRoot)
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{
		TargetRoot:          targetRoot,
		SourceRoot:          recorded.SourceRoot,
		SourceVersion:       recorded.SourceVersion,
		Image:               image,
		ImageFamilyOverride: options.AllowImageFamilyMismatch,
		Volume:              volume,
		VolumeMountPoint:    LegacyVolumeMountPoint,
		ComposeProject:      project,
		ComposeService:      LegacyComposeService,
		ComposeInvocation: fmt.Sprintf("docker compose -p %s -f - up -d %s",
			project, LegacyComposeService),
		ComposeFile:          rollbackComposeFile(image, volume, port),
		Port:                 port,
		ManagerPort:          managerAddressPort(options.ManagerAddress),
		ManagerAddress:       strings.TrimSpace(options.ManagerAddress),
		BackupDirectory:      bundleDir,
		Preconditions:        rollbackPreconditions(project, volume, image, recorded.SourceRoot, bundleDir, port, managerAddressPort(options.ManagerAddress)),
		HealthChecks:         rollbackHealthChecks(port),
		SourceFingerprint:    fingerprint,
		BackupManifestSHA256: recorded.BackupManifestSHA256,
		Stage:                recorded.Stage,
		ReportPath:           filepath.Join(targetRoot, rollbackWorkDirectoryName, rollbackPlanFileName),
	}
	if plan.Digest, err = planDigest(plan); err != nil {
		return Plan{}, err
	}
	if err := writePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// readRollbackRecord reads the operation row from a v2 data root that may not
// hold a database at all. A missing store is "no record", not a failure: the
// operator has to be told there is nothing to roll back, not that a file is
// absent.
func readRollbackRecord(ctx context.Context, targetRoot string) (model.UpgradeOperation, bool, error) {
	databasePath := filepath.Join(targetRoot, databaseFileName)
	if _, err := os.Lstat(databasePath); err != nil {
		if os.IsNotExist(err) {
			return model.UpgradeOperation{}, false, nil
		}
		return model.UpgradeOperation{}, false, err
	}
	return ReadRecord(ctx, databasePath)
}

// sourceRootRecorded reports whether the row already names the 1.x directory it
// copied from.
func sourceRootRecorded(recorded model.UpgradeOperation) bool {
	return strings.TrimSpace(recorded.SourceRoot) != ""
}

// requireBoundedSourceRoot binds a rollback decision to the 1.x directory the
// copy-in recorded, and writes nothing. A different directory is a different
// volume. A row written before the column existed names none: the fingerprint
// the copy-in stored is the only proof left, because the copy-in refuses to run
// again once it is recorded, so a directory that reproduces it may be bound and
// anything else may not. The refusal for the impossible case says what works.
func requireBoundedSourceRoot(recorded model.UpgradeOperation, sourceRoot, fingerprint string) error {
	if sourceRootRecorded(recorded) {
		if filepath.Clean(recorded.SourceRoot) != filepath.Clean(sourceRoot) {
			return newUpgradeError(UpgradeErrorRollbackVolumeUnbound,
				"the 1.x data directory given here is not the one this operation recorded; a v1 restart against it would not be the original volume, so pass the directory the copy-in read", nil)
		}
		return nil
	}
	if recorded.SourceFingerprintBefore != "" && fingerprint == recorded.SourceFingerprintBefore {
		return nil
	}
	return newUpgradeError(UpgradeErrorRollbackVolumeUnbound,
		"the recorded operation names no 1.x data directory, and the directory given here does not reproduce the fingerprint the copy-in recorded, so nothing can bind this rollback to a volume; point --legacy at the mounted 1.x volume (docker volume inspect) whose contents reproduce that fingerprint, or run --prepare while that volume is still stopped and unchanged", nil)
}

// bindSourceRoot records the directory of a row that names none. It is the one
// write a binding may make, so callers run it after every refusal and before
// their own write.
func bindSourceRoot(ctx context.Context, recorded model.UpgradeOperation, targetRoot, sourceRoot string) (model.UpgradeOperation, error) {
	if sourceRootRecorded(recorded) {
		return recorded, nil
	}
	backfilled, err := backfillRecordedSourceRoot(ctx, targetRoot, sourceRoot)
	if err != nil {
		return recorded, fmt.Errorf("record the 1.x data directory this operation copied: %w", err)
	}
	return backfilled, nil
}

// backfillRecordedSourceRoot writes the directory once, through a writable open
// so the store's own migration is what adds the column if it is missing.
func backfillRecordedSourceRoot(ctx context.Context, targetRoot, sourceRoot string) (model.UpgradeOperation, error) {
	db, err := managersqlite.Open(filepath.Join(targetRoot, databaseFileName))
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	defer func() { _ = db.Close() }()
	return upgradeoperation.New(db).RecordSourceRoot(ctx, sourceRoot)
}

// refuseRootOverlap keeps every rollback write inside the v2 data root. The 1.x
// volume is a source, never a destination, so a target nested under it (or a
// source nested under the target) is refused before anything is read or
// written.
func refuseRootOverlap(sourceRoot, targetRoot string) error {
	if rootsOverlap(sourceRoot, targetRoot) {
		return newUpgradeError(UpgradeErrorRollbackRootsOverlap,
			"the v2 data root and the 1.x source root must not contain each other", nil)
	}
	return nil
}

// rootsOverlap reports whether either root contains the other. Copy-in applies
// the same rule; both must, or one path could write the volume the other only
// reads.
func rootsOverlap(sourceRoot, targetRoot string) bool {
	return within(sourceRoot, targetRoot) || within(targetRoot, sourceRoot)
}

// rollbackVolume derives the 1.x volume name from the compose project the stack
// restarts under, and refuses a project name that cannot name a volume.
func rollbackVolume(project string) (string, string, error) {
	trimmed := strings.TrimSpace(project)
	if trimmed == "" {
		trimmed = LegacyComposeProject
	}
	if !composeProjectPattern.MatchString(trimmed) {
		return "", "", newUpgradeError(UpgradeErrorRollbackVolumeUnbound,
			"the compose project must be a lowercase alphanumeric name with - or _, because it names the 1.x volume", nil)
	}
	return trimmed, trimmed + LegacyVolumeSuffix, nil
}

// rollbackPlanPort resolves the port the plan publishes the restarted 1.x
// manager on. It refuses the port this Manager itself listens on, because the
// v2 stack holds it while it runs and both managers answer /health the same way,
// and it refuses a port something else already holds, because the plan would
// then be applied against whatever answers there.
func rollbackPlanPort(port int, managerAddress string) (int, error) {
	resolved := port
	if resolved == 0 {
		resolved = LegacyRollbackPort
	}
	if resolved < 1 || resolved > 65535 {
		return 0, fmt.Errorf("the 1.x rollback port %d is not a TCP port", resolved)
	}
	if managerPort := managerAddressPort(managerAddress); resolved == managerPort {
		return 0, newUpgradeError(UpgradeErrorRollbackPortCollision,
			fmt.Sprintf("the plan would publish port %d, which is the port this Manager listens on; the v2 stack holds it while it runs and answers /health with the same identity, so start v1 on the plan's own port instead", resolved), nil)
	}
	if !portFree(resolved) {
		return 0, newUpgradeError(UpgradeErrorRollbackPortBusy,
			fmt.Sprintf("port %d is already held on this host, so the 1.x stack cannot publish it and a probe there would answer whatever holds it; stop that listener and prepare the plan again", resolved), nil)
	}
	return resolved, nil
}

// requireV2Stopped refuses a plan while the v2 stack still answers on the
// address the operator named. An unnamed address proves nothing either way, so
// it is not guessed at: the operator names the published address when the plan
// must prove the stack is down.
func requireV2Stopped(ctx context.Context, managerAddress string) error {
	address := strings.TrimSpace(managerAddress)
	if address == "" {
		return nil
	}
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	connection, err := dialer.DialContext(ctx, "tcp", managerAddressDialTarget(address))
	if err != nil {
		return nil
	}
	_ = connection.Close()
	return newUpgradeError(UpgradeErrorRollbackV2Running,
		fmt.Sprintf("the v2 stack still answers on %q, so it was not stopped; stop the v2 stack before preparing a rollback plan, or a probe of the port could be answered by it", address), nil)
}

// managerAddressPort is the port of the Manager's own listen address. An empty
// address is the documented default port: a probe that names no address must
// still be refused when it targets the port the Manager publishes on.
func managerAddressPort(address string) int {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return ManagerDefaultPort
	}
	if _, port, err := net.SplitHostPort(trimmed); err == nil {
		if value, err := strconv.Atoi(port); err == nil && value > 0 {
			return value
		}
	}
	if value, err := strconv.Atoi(trimmed); err == nil && value > 0 {
		return value
	}
	return ManagerDefaultPort
}

// managerAddressDialTarget turns a listen address into something dialable: a
// wildcard bind address is reached on loopback.
func managerAddressDialTarget(address string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		if value, convErr := strconv.Atoi(strings.TrimSpace(address)); convErr == nil {
			return net.JoinHostPort("127.0.0.1", strconv.Itoa(value))
		}
		return address
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// portFree reports whether the port can still be published on this host.
//
// A single wildcard bind is not enough: on platforms that allow a wildcard bind
// beside a specific listener the wildcard succeeds while the port is served, and
// a dial probe is unreliable when a local proxy answers connections. So the port
// is bound on every address of this host and on both wildcards, and only an
// address-in-use failure counts as occupied.
func portFree(port int) bool {
	for _, host := range localBindHosts() {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			_ = listener.Close()
			continue
		}
		if errors.Is(err, syscall.EADDRINUSE) {
			return false
		}
	}
	return true
}

// localBindHosts is every address a listener on this host could sit on: both
// wildcards, loopback and each interface address.
func localBindHosts() []string {
	hosts := []string{"0.0.0.0", "::", "127.0.0.1", "::1"}
	seen := map[string]bool{}
	for _, host := range hosts {
		seen[host] = true
	}
	if addresses, err := net.InterfaceAddrs(); err == nil {
		for _, address := range addresses {
			network, ok := address.(*net.IPNet)
			if !ok || network.IP == nil || network.IP.IsUnspecified() {
				continue
			}
			ip := network.IP.String()
			if seen[ip] {
				continue
			}
			seen[ip] = true
			hosts = append(hosts, ip)
		}
	}
	return hosts
}

// localAddressHosts is the set of host names that address this host itself.
func localAddressHosts() map[string]bool {
	hosts := map[string]bool{
		"localhost": true,
		"127.0.0.1": true,
		"::1":       true,
		"0.0.0.0":   true,
		"::":        true,
	}
	if name, err := os.Hostname(); err == nil && name != "" {
		hosts[strings.ToLower(name)] = true
		if resolved, err := net.LookupHost(name); err == nil {
			for _, address := range resolved {
				hosts[address] = true
			}
		}
	}
	if addresses, err := net.InterfaceAddrs(); err == nil {
		for _, address := range addresses {
			if network, ok := address.(*net.IPNet); ok && network.IP != nil {
				hosts[network.IP.String()] = true
			}
		}
	}
	return hosts
}

// rollbackImage resolves the exact 1.x release to pin. An explicit reference
// must name a full release, and unless the caller overrides it that release must
// belong to the recorded source's version family: a rollback restarts the
// version the operation copied, not an adjacent one.
func rollbackImage(recorded model.UpgradeOperation, override string, allowFamilyMismatch bool) (string, error) {
	sourceRelease, sourceOK := parseRelease(recorded.SourceVersion)
	image, err := rollbackImageReference(override, sourceOK, recorded.SourceVersion)
	if err != nil {
		return "", err
	}
	if allowFamilyMismatch {
		return image, nil
	}
	tag := imageTag(image)
	targetRelease, ok := parseRelease(tag)
	if !ok {
		return "", newUpgradeError(UpgradeErrorRollbackImageUnpinned,
			"the 1.x image reference does not name an exact release, so it cannot be pinned", nil)
	}
	if !sourceOK {
		return "", newUpgradeError(UpgradeErrorRollbackImageUnpinned,
			"the recorded source version is not an exact release, so no 1.x image can be pinned", nil)
	}
	if releaseFamily(targetRelease) != releaseFamily(sourceRelease) {
		return "", newUpgradeError(UpgradeErrorRollbackImageFamily,
			fmt.Sprintf("the 1.x image names release family %s but the operation copied %s; pass the family override only after confirming the v1 data is readable by that release",
				releaseFamilyText(targetRelease), releaseFamilyText(sourceRelease)), nil)
	}
	return image, nil
}

// rollbackImageReference turns the operator's reference or the recorded version
// into a pinned image. A moving tag, an empty tag and a digest-only reference
// are refused rather than turned into a reference the operator did not ask for.
func rollbackImageReference(override string, sourceOK bool, sourceVersion string) (string, error) {
	tag := strings.TrimSpace(override)
	if tag == "" {
		if !sourceOK {
			return "", newUpgradeError(UpgradeErrorRollbackImageUnpinned,
				"the recorded source version is not an exact release, so no 1.x image can be pinned", nil)
		}
		return LegacyImageRepository + ":v" + strings.TrimPrefix(strings.TrimSpace(sourceVersion), "v"), nil
	}
	if tag == "latest" || strings.HasSuffix(tag, ":latest") {
		return "", newUpgradeError(UpgradeErrorRollbackImageUnpinned,
			"a rollback may not pin a moving tag; pass an exact version", nil)
	}
	if strings.Contains(tag, "@") {
		return "", newUpgradeError(UpgradeErrorRollbackImageUnpinned,
			"a rollback pins a published version tag, not a digest reference", nil)
	}
	if strings.HasPrefix(tag, LegacyImageRepository+":") {
		if imageTag(tag) == "" {
			return "", newUpgradeError(UpgradeErrorRollbackImageUnpinned,
				"the 1.x image reference names an empty tag; pass an exact version", nil)
		}
		return tag, nil
	}
	// A reference that names a repository (a slash) without a tag of its own is
	// a floating reference, not a version.
	if strings.Contains(tag, "/") {
		if imageTag(tag) == "" {
			return "", newUpgradeError(UpgradeErrorRollbackImageUnpinned,
				"the 1.x image reference names no version tag; pass an exact version", nil)
		}
		return tag, nil
	}
	return LegacyImageRepository + ":" + tag, nil
}

// imageTag returns the tag of a reference, or "" when it names none.
func imageTag(reference string) string {
	colon := strings.LastIndex(reference, ":")
	if colon <= strings.LastIndex(reference, "/") || colon == len(reference)-1 {
		return ""
	}
	return reference[colon+1:]
}

// releaseFamily keeps only the major and minor part of a parsed release.
func releaseFamily(release int) int { return release / 1_000 * 1_000 }

// releaseFamilyText renders a parsed release as major.minor.
func releaseFamilyText(release int) string {
	return fmt.Sprintf("%d.%d", release/1_000_000, release/1_000%1_000)
}

// rollbackComposeFile is the 1.x service definition with the image pinned, the
// volume bound and the published port one the v2 stack does not use. It is fed
// to `docker compose -f -` on stdin, so no file is written outside the v2 data
// root and the operator's checkout is never edited.
func rollbackComposeFile(image, volume string, port int) string {
	published := strconv.Itoa(port)
	return strings.Join([]string{
		"services:",
		"  " + LegacyComposeService + ":",
		"    image: " + image,
		"    restart: unless-stopped",
		"    ports:",
		`      - "` + published + ":" + published + `"`,
		"    environment:",
		`      HTTP_ADDR: "0.0.0.0:` + published + `"`,
		`      USAGE_DB_PATH: "` + LegacyVolumeMountPoint + "/" + databaseFileName + `"`,
		`      CPA_MANAGER_DATA_KEY_PATH: "` + LegacyVolumeMountPoint + "/" + dataKeyFileName + `"`,
		`      USAGE_COLLECTOR_MODE: "auto"`,
		"    healthcheck:",
		`      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:` + published + LegacyHealthPath + `"]`,
		"      interval: 10s",
		"      timeout: 3s",
		"      retries: 3",
		"    volumes:",
		"      - " + volume + ":" + LegacyVolumeMountPoint,
		"volumes:",
		"  " + volume + ":",
		"    external: true",
		"    name: " + volume,
	}, "\n") + "\n"
}

// rollbackPreconditions are the checks the operator runs before applying the
// plan. The v2 stack must be down and the plan's port free, or the restarted 1.x
// cannot be the one that answers. The volume must already exist: an external
// volume makes compose refuse rather than create one. The mountpoint check is
// the binding: the volume must be the directory the operation recorded, or the
// restart is not a rollback. Every path is quoted, so a directory with a space
// is still one argument.
func rollbackPreconditions(project, volume, image, sourceRoot, backupDir string, port, managerPort int) []string {
	published := strconv.Itoa(port)
	held := strconv.Itoa(managerPort)
	return []string{
		"stop the v2 stack first: while it runs it publishes port " + held + " and holds the v2 data root this plan was derived from",
		"confirm the plan's port is free on the Docker host: docker ps --filter publish=" + published + " --format '{{.Names}}' must print nothing",
		"confirm nothing else listens on it: lsof -nP -iTCP:" + published + " -sTCP:LISTEN must print nothing",
		"docker volume inspect " + strconv.Quote(volume) + " --format '{{ .Mountpoint }}'",
		"that mountpoint must be the 1.x data directory this operation recorded: " + strconv.Quote(sourceRoot),
		"the v1 stack restarts under compose project " + project + ", because that project names the volume",
		"pull the pinned 1.x image before restarting: docker pull " + image,
		"the v1 stack publishes " + published + " rather than the v2 port " + held + ", so a /health answer can only come from v1",
		"the recorded backup this verdict is checked against is " + strconv.Quote(backupDir),
	}
}

// rollbackHealthChecks are the two proofs the operator can reproduce by hand.
// The first is the unauthenticated health endpoint and the identity it must
// state; the second is the read-only open of the volume itself.
func rollbackHealthChecks(port int) []string {
	return []string{
		"curl -fsS " + RollbackHealthURL(port),
		`the body must be {"ok":true,"service":"` + LegacyServiceID + `"}; a bare 2xx is not a health proof`,
		"open " + LegacyVolumeMountPoint + "/" + databaseFileName + " read-only and pass a SQLite quick_check",
		"the volume must still hold the rows and archived segments the recorded backup describes",
	}
}

// RollbackHealthURL is the health endpoint a 1.x stack started by a rollback plan
// answers on.
func RollbackHealthURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, LegacyHealthPath)
}

// LegacyHealthURL is the health endpoint of the default rollback plan: the
// default verify probe uses it, and it is never the port the v2 Manager holds.
func LegacyHealthURL() string {
	return RollbackHealthURL(LegacyRollbackPort)
}

// planDigest is the canonical digest of a plan: the plan without its own digest
// field, marshalled with a fixed field order, hashed once.
func planDigest(plan Plan) (string, error) {
	plan.Digest = ""
	canonical, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("canonicalize the rollback plan: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// writePlan publishes the report under the v2 data root only. The 1.x source is
// never written, not even beside.
func writePlan(plan Plan) error {
	directory := filepath.Dir(plan.ReportPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create the rollback report directory: %w", err)
	}
	encoded, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the rollback plan: %w", err)
	}
	if err := os.WriteFile(plan.ReportPath, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("write the rollback plan: %w", err)
	}
	return nil
}

// RollbackObservation is what the operator reports about the applied rollback:
// where, against which image, and when. It carries no verdict: the read-only
// open, the health identity and the retained-row proof are derived here, so a
// caller cannot assert them.
type RollbackObservation struct {
	LegacyRoot string `json:"legacyRoot"`
	TargetRoot string `json:"targetRoot"`
	// BackupDir is the recorded backup snapshot. Empty uses the v2 default
	// backup directory inside TargetRoot.
	BackupDir string `json:"backupDir,omitempty"`
	// Image is the reference the operator actually started. It must be a pinned
	// release in the recorded source family unless the family override is set.
	Image string `json:"image,omitempty"`
	// StatusURL is the 1.x health endpoint that was probed. Empty probes the
	// default 1.x `/health` endpoint.
	StatusURL string `json:"statusUrl,omitempty"`
	// ManagerAddress is the v2 Manager's own listen address (host:port). A probe
	// that addresses it is refused: the 1.x and the v2 Manager state the same
	// identity, so an answer there is the v2 stack, not a restored volume. Empty
	// assumes the documented Manager default port.
	ManagerAddress string `json:"managerAddress,omitempty"`
	// ObservedAtMS is when the operator asked for the verdict. It is only a
	// freshness claim: the observation time written with the verdict is the one
	// this service stamps when it has finished its own proofs.
	ObservedAtMS int64 `json:"observedAtMs"`
	// AllowImageFamilyMismatch is the explicit override for a 1.x image from
	// another version family.
	AllowImageFamilyMismatch bool `json:"allowImageFamilyMismatch,omitempty"`
}

// RecordRollback records the rolled_back verdict with the evidence that
// justified it. The evidence is written with the stage in one transaction, so
// the row always names the image, the health endpoint, the observed fingerprint
// and the observation time that produced the verdict. It never rewrites
// provenance: the source kind, the recorded source root, both fingerprints, the
// backup manifest digest and the completed domains survive. The observation time
// is the service's own clock reading, not the caller's label.
//
// Every refusal has its own typed code: a stage a rollback cannot start from
// (upgrade_rollback_not_applicable), a directory that is not the recorded source
// root (upgrade_rollback_volume_unbound), an observation outside the freshness
// window (upgrade_rollback_evidence_stale), an image that is unpinned or from
// another version family (upgrade_rollback_image_unpinned,
// upgrade_rollback_image_family), a probe that does not address /health
// (upgrade_rollback_probe_endpoint), a probe that addresses this Manager's own
// listener (upgrade_rollback_probe_self), a v2 stack that still answers on the
// named address (upgrade_rollback_v2_running), a volume that does not open
// read-only (upgrade_rollback_unproven) and something that answers but is not
// this install (upgrade_rollback_identity_foreign). Every refusal leaves the row
// untouched.
func RecordRollback(ctx context.Context, observation RollbackObservation) (model.UpgradeOperation, error) {
	if err := ctx.Err(); err != nil {
		return model.UpgradeOperation{}, err
	}
	sourceRoot, err := resolveLegacyRoot(observation.LegacyRoot)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if sourceRoot, err = canonicalPath(sourceRoot); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("resolve the 1.x source root: %w", err)
	}
	targetRoot, err := resolveTargetRoot(observation.TargetRoot)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if targetRoot, err = canonicalPath(targetRoot); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("resolve the v2 data root: %w", err)
	}
	if err := refuseRootOverlap(sourceRoot, targetRoot); err != nil {
		return model.UpgradeOperation{}, err
	}
	recorded, found, err := readRollbackRecord(ctx, targetRoot)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("read the recorded upgrade operation: %w", err)
	}
	if !found {
		return model.UpgradeOperation{}, newUpgradeError(UpgradeErrorRollbackNotApplicable,
			"no upgrade operation is recorded, so there is nothing to roll back", nil)
	}
	if !rollbackApplicableStages[recorded.Stage] {
		return model.UpgradeOperation{}, newUpgradeError(UpgradeErrorRollbackNotApplicable,
			fmt.Sprintf("the operation is recorded at stage %q, which a rollback cannot start from", string(recorded.Stage)), nil)
	}
	if err := requireFreshObservation(observation.ObservedAtMS, time.Now()); err != nil {
		return model.UpgradeOperation{}, err
	}
	image, err := rollbackImage(recorded, observation.Image, observation.AllowImageFamilyMismatch)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := requireRecordedBackup(recorded); err != nil {
		return model.UpgradeOperation{}, err
	}
	bundleDir, err := rollbackBundleDir(observation.BackupDir, targetRoot)
	if err != nil {
		return model.UpgradeOperation{}, err
	}

	// From here on the checks observe the install rather than the operator's
	// claim. The fingerprint is informational: the 1.x manager has been serving
	// again, so its rows may have moved.
	fingerprint, _, err := fingerprintSource(sourceRoot)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("fingerprint the 1.x source: %w", err)
	}
	// A row written before the source-root column existed is bound as well, so a
	// verdict is never recorded against a volume nobody described. The live
	// fingerprint has usually moved by now, so the backfill proof is only
	// available to the plan that ran while the volume was still stopped.
	if err := requireBoundedSourceRoot(recorded, sourceRoot, fingerprint); err != nil {
		return model.UpgradeOperation{}, err
	}
	health, err := CheckLegacyHealth(ctx, observation.StatusURL, observation.ManagerAddress)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	// The verdict says the v2 root was abandoned, so a v2 stack that still
	// answers on the address the operator named makes it untrue.
	if err := requireV2Stopped(ctx, observation.ManagerAddress); err != nil {
		return model.UpgradeOperation{}, err
	}
	if err := verifyRollbackInstall(ctx, sourceRoot, bundleDir, recorded); err != nil {
		return model.UpgradeOperation{}, err
	}
	// The binding may write a backfill, so it comes after every refusal: the only
	// step left is the verdict itself.
	recorded, err = bindSourceRoot(ctx, recorded, targetRoot, sourceRoot)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	// The observation time is stamped here, after every proof this service
	// derives itself, so the recorded moment describes these proofs rather than
	// the caller's claim about them.
	observedAtMS := time.Now().UnixMilli()

	db, err := managersqlite.Open(filepath.Join(targetRoot, databaseFileName))
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("open the v2 database to record the rollback: %w", err)
	}
	defer func() { _ = db.Close() }()
	advanced, err := upgradeoperation.New(db).RecordRollback(ctx, model.RollbackEvidence{
		Image:             image,
		StatusURL:         health.URL,
		SourceFingerprint: fingerprint,
		ObservedAtMS:      observedAtMS,
	})
	if err != nil {
		if errors.Is(err, upgradeoperation.ErrStageRegression) {
			return model.UpgradeOperation{}, newUpgradeError(UpgradeErrorRollbackNotApplicable,
				"the operation already reached a terminal stage", err)
		}
		return model.UpgradeOperation{}, fmt.Errorf("record the rolled_back stage: %w", err)
	}
	return advanced, nil
}

// requireRecordedBackup refuses a row that names no backup: without it the live
// volume has nothing to be compared against.
func requireRecordedBackup(recorded model.UpgradeOperation) error {
	if strings.TrimSpace(recorded.BackupManifestSHA256) == "" {
		return newUpgradeError(UpgradeErrorRollbackUnproven,
			"the recorded operation names no backup manifest, so the live volume cannot be compared with the copy it came from", nil)
	}
	return nil
}

// requireFreshObservation refuses an observation taken outside the freshness
// window, or dated in the future beyond a small clock difference. Evidence that
// describes a different moment cannot justify this moment's verdict.
func requireFreshObservation(observedAtMS int64, now time.Time) error {
	if observedAtMS <= 0 {
		return newUpgradeError(UpgradeErrorRollbackEvidenceStale,
			"the observation carries no time, so it cannot be shown to describe the install being recorded; re-run the probe now", nil)
	}
	observed := time.UnixMilli(observedAtMS)
	if observed.Sub(now) > rollbackEvidenceFutureSkew {
		return newUpgradeError(UpgradeErrorRollbackEvidenceStale,
			"the observation is dated in the future, so it cannot describe the install being recorded", nil)
	}
	if now.Sub(observed) > rollbackEvidenceFreshness {
		return newUpgradeError(UpgradeErrorRollbackEvidenceStale,
			fmt.Sprintf("the observation is older than %s, so it no longer describes the install being recorded; start v1, probe it and record again",
				rollbackEvidenceFreshness), nil)
	}
	return nil
}

// rollbackBundleDir resolves the recorded backup snapshot. The default is the
// v2 backup directory, which survives inside the v2 data root.
func rollbackBundleDir(override, targetRoot string) (string, error) {
	dir := strings.TrimSpace(override)
	if dir == "" {
		dir = DefaultBundleDirectory(targetRoot)
	}
	absolute, err := resolveBundleDir(dir)
	if err != nil {
		return "", err
	}
	return canonicalPath(absolute)
}

// LegacyHealth is what the 1.x manager answered on its health endpoint.
type LegacyHealth struct {
	URL        string `json:"url"`
	StatusCode int    `json:"statusCode"`
	Service    string `json:"service"`
}

// legacyHealthBody is the documented body of the 1.x health endpoint. A 2xx
// without it is an answer from something that is not the 1.x manager.
type legacyHealthBody struct {
	OK      bool   `json:"ok"`
	Service string `json:"service"`
}

// CheckLegacyHealth probes the 1.x health endpoint and requires the documented
// identity, not merely a 2xx: a stray 200 from a proxy or another service must
// not prove the volume. A different identity is refused as a foreign install,
// and a non-2xx answer as unproven. An empty URL probes the default rollback
// health endpoint.
//
// Only /health is accepted. Every other endpoint of the 1.x manager requires
// credentials, so probing one proves nothing about the volume.
//
// A probe that addresses this Manager's own listener is refused before it is
// sent (upgrade_rollback_probe_self). The 1.x manager and the v2 Manager state
// the same identity on that endpoint, so an answer from the port this Manager
// holds is the v2 stack describing itself, never a restored 1.x volume.
// managerAddress is that listener (host:port); empty means the documented
// Manager default port.
func CheckLegacyHealth(ctx context.Context, rawURL, managerAddress string) (LegacyHealth, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		trimmed = LegacyHealthURL()
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return LegacyHealth{}, fmt.Errorf("the 1.x health probe needs an absolute URL, got %q", trimmed)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return LegacyHealth{}, fmt.Errorf("the 1.x health probe needs an http or https URL, got scheme %q", parsed.Scheme)
	}
	if path := strings.TrimSuffix(parsed.Path, "/"); path != LegacyHealthPath {
		return LegacyHealth{}, newUpgradeError(UpgradeErrorRollbackProbeEndpoint,
			fmt.Sprintf("the 1.x health proof is GET %s; %q requires credentials and says nothing about the volume, so probe %s instead",
				LegacyHealthPath, parsed.Path, LegacyHealthURL()), nil)
	}
	if err := refuseSelfProbe(parsed, managerAddress); err != nil {
		return LegacyHealth{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return LegacyHealth{}, fmt.Errorf("build the 1.x health request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return LegacyHealth{}, newUpgradeError(UpgradeErrorRollbackUnproven,
			"the 1.x health probe did not answer, so the v1 manager is not proven to be serving this volume", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxHealthBodyBytes))
	observation := LegacyHealth{URL: parsed.String(), StatusCode: response.StatusCode}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := ""
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			detail = "; " + LegacyHealthPath + " must answer without credentials, so a probe that needs a key is the wrong endpoint"
		}
		return observation, newUpgradeError(UpgradeErrorRollbackUnproven,
			fmt.Sprintf("the 1.x health probe answered HTTP %d%s", response.StatusCode, detail), nil)
	}
	if readErr != nil {
		return observation, newUpgradeError(UpgradeErrorRollbackUnproven,
			"the 1.x health probe answered but its body could not be read", readErr)
	}
	var stated legacyHealthBody
	if err := json.Unmarshal(body, &stated); err != nil {
		return observation, newUpgradeError(UpgradeErrorRollbackIdentityForeign,
			"the 1.x health probe answered 2xx without the documented body, so something other than the 1.x manager is listening", nil)
	}
	observation.Service = stated.Service
	switch {
	case !stated.OK:
		return observation, newUpgradeError(UpgradeErrorRollbackIdentityForeign,
			"the 1.x health probe answered 2xx but did not report itself healthy, so it is not a health proof", nil)
	case stated.Service == "":
		return observation, newUpgradeError(UpgradeErrorRollbackIdentityForeign,
			"the 1.x health probe answered 2xx without naming a service, so something other than the 1.x manager is listening", nil)
	case stated.Service != LegacyServiceID:
		return observation, newUpgradeError(UpgradeErrorRollbackIdentityForeign,
			fmt.Sprintf("the 1.x health probe answered as service %q, not %q, so the probed port is not serving this install",
				sanitizeIdentity(stated.Service), LegacyServiceID), nil)
	}
	return observation, nil
}

// refuseSelfProbe refuses a probe that would be answered by this Manager's own
// listener. The port is what identifies it: the v2 Manager binds every local
// address of that port, and a container-local address is one of them, so any
// host that resolves to this machine on the Manager's port is this Manager.
func refuseSelfProbe(parsed *url.URL, managerAddress string) error {
	probePort := urlPort(parsed)
	managerPort := managerAddressPort(managerAddress)
	if probePort != managerPort {
		return nil
	}
	probeHost := parsed.Hostname()
	if !addressesThisHost(probeHost) && !sameHost(probeHost, managerAddress) {
		return nil
	}
	return newUpgradeError(UpgradeErrorRollbackProbeSelf,
		fmt.Sprintf("the health probe addresses this Manager's own listener on port %d; the v2 Manager answers %s with the same %q identity, so that answer proves the v2 stack is up, not that the 1.x volume is back; probe the 1.x port the rollback plan printed (%d) instead",
			probePort, LegacyHealthPath, LegacyServiceID, LegacyRollbackPort), nil)
}

// urlPort is the effective port of a parsed http(s) URL.
func urlPort(parsed *url.URL) int {
	if port := parsed.Port(); port != "" {
		if value, err := strconv.Atoi(port); err == nil {
			return value
		}
	}
	if parsed.Scheme == "https" {
		return 443
	}
	return 80
}

// addressesThisHost reports whether a probe host resolves to this machine: a
// loopback or wildcard name, an interface address, this host's own name, or any
// address that name resolves to.
func addressesThisHost(host string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(host))
	if trimmed == "" {
		return true
	}
	local := localAddressHosts()
	if local[trimmed] {
		return true
	}
	if net.ParseIP(trimmed) != nil {
		return false
	}
	resolved, err := net.LookupHost(trimmed)
	if err != nil {
		return false
	}
	for _, address := range resolved {
		if local[address] {
			return true
		}
	}
	return false
}

// sameHost reports whether a probe host is exactly the host the Manager was told
// to listen on, even when that host is not one of this machine's addresses.
func sameHost(probeHost, managerAddress string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(managerAddress))
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.Trim(host, "[]"), strings.Trim(probeHost, "[]"))
}

// sanitizeIdentity keeps an echoed identity short and free of control bytes: a
// refusal must not replay the body it refused.
func sanitizeIdentity(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) > maxReportedIdentity {
		trimmed = trimmed[:maxReportedIdentity]
	}
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, trimmed)
	if cleaned == "" {
		return "unnamed"
	}
	return cleaned
}

// retainedRowGroup is one durable population a rollback proves survived. Usage
// events are grouped with their archive projection because the v1 archive
// maintenance deletes raw rows once it has archived them: an event is retained
// as a raw row or as its archived reference, never as neither.
type retainedRowGroup struct {
	name   string
	tables []string
}

// rollbackRetainedRows are the populations the backup records and the live
// volume must still hold at least.
var rollbackRetainedRows = []retainedRowGroup{
	{name: "usage event", tables: []string{"usage_events", "usage_archive_event_refs"}},
}

// verifyRollbackInstall proves the live 1.x root is the install the recorded
// backup describes without requiring byte equality, which a v1 that kept
// serving can never satisfy. It reads the backup snapshot read-only, opens the
// live database read-only and runs a quick_check on it, then requires the live
// volume to still retain at least the rows and archived segments the backup
// holds. A different backup, a missing row population or a changed archive
// segment is a foreign install.
//
// The live database is opened immutable, exactly as the 1.x probe always was: a
// plain read-only open would create a WAL index beside the source, and a
// rollback must leave the volume untouched. Both stores it compares are the same
// install, so a row that is only committed to the live WAL is an *extra* row it
// does not count; the retained-row rule is a lower bound, never an equality.
func verifyRollbackInstall(ctx context.Context, sourceRoot, bundleDir string, recorded model.UpgradeOperation) error {
	manifest, digest, err := verifyRecordedBackup(ctx, bundleDir)
	if err != nil {
		return newUpgradeError(UpgradeErrorRollbackUnproven,
			"the recorded backup snapshot cannot be verified read-only, so there is nothing to compare the live volume with", err)
	}
	if digest != recorded.BackupManifestSHA256 {
		return newUpgradeError(UpgradeErrorRollbackIdentityForeign,
			"the backup snapshot read here is not the one this operation recorded, so it cannot describe the volume being rolled back", nil)
	}

	backupDB, _, err := openImmutableSource(ctx, maintenancebundle.BundleLayout(bundleDir).DatabasePath)
	if backupDB != nil {
		defer func() { _ = backupDB.Close() }()
	}
	if err != nil {
		return newUpgradeError(UpgradeErrorRollbackUnproven,
			"the backup snapshot database does not open read-only with a passing quick_check", err)
	}
	backupRows, err := countRetainedRows(ctx, backupDB)
	if err != nil {
		return newUpgradeError(UpgradeErrorRollbackUnproven,
			"the retained rows of the backup snapshot cannot be counted", err)
	}

	liveDB, _, err := openImmutableSource(ctx, filepath.Join(sourceRoot, databaseFileName))
	if liveDB != nil {
		defer func() { _ = liveDB.Close() }()
	}
	if err != nil {
		return newUpgradeError(UpgradeErrorRollbackUnproven,
			"the live 1.x database does not open read-only with a passing quick_check; stop the 1.x manager's writes and record again", err)
	}
	liveRows, err := countRetainedRows(ctx, liveDB)
	if err != nil {
		return newUpgradeError(UpgradeErrorRollbackUnproven,
			"the retained rows of the live 1.x database cannot be counted", err)
	}
	for _, group := range rollbackRetainedRows {
		if liveRows[group.name] < backupRows[group.name] {
			return newUpgradeError(UpgradeErrorRollbackIdentityForeign,
				fmt.Sprintf("the live 1.x volume retains %d %s rows but the recorded backup holds %d, so it is not the install this operation copied",
					liveRows[group.name], group.name, backupRows[group.name]), nil)
		}
	}
	return requireRecordedArchives(sourceRoot, manifest)
}

// requireRecordedArchives requires every archived segment and the data key the
// backup describes to still be present byte for byte beside the live database.
// The archive tree is append-only, so a new segment is fine and a changed or
// missing one is not.
func requireRecordedArchives(sourceRoot string, manifest maintenancebundle.Manifest) error {
	for name, entry := range manifest.Entries {
		if entry.Directory {
			continue
		}
		switch {
		case name == archivesDirName, strings.HasPrefix(name, archivesDirName+"/"):
			matching, err := entryMatches(filepath.Join(sourceRoot, filepath.FromSlash(name)), entry)
			if err != nil {
				return fmt.Errorf("inspect the live 1.x archive member: %w", err)
			}
			if !matching {
				return newUpgradeError(UpgradeErrorRollbackIdentityForeign,
					"the live 1.x volume no longer holds an archived segment the recorded backup describes, so it is not the install this operation copied", nil)
			}
		case name == "data-key" && entry.Present:
			matching, err := entryMatches(filepath.Join(sourceRoot, dataKeyFileName), entry)
			if err != nil {
				return fmt.Errorf("inspect the live 1.x data key: %w", err)
			}
			if !matching {
				return newUpgradeError(UpgradeErrorRollbackIdentityForeign,
					"the live 1.x volume holds a different data key than the recorded backup, so it cannot decrypt the copied data", nil)
			}
		}
	}
	return nil
}

// countRetainedRows counts every retained-row group in one store. A table the
// store does not have contributes nothing.
func countRetainedRows(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	totals := make(map[string]int64, len(rollbackRetainedRows))
	for _, group := range rollbackRetainedRows {
		var total int64
		for _, table := range group.tables {
			count, present, err := countTableRows(ctx, db, table)
			if err != nil {
				return nil, err
			}
			if present {
				total += count
			}
		}
		totals[group.name] = total
	}
	return totals, nil
}

func countTableRows(ctx context.Context, db *sql.DB, table string) (int64, bool, error) {
	var present int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master
		where type = 'table' and name = ?`, table).Scan(&present); err != nil {
		return 0, false, err
	}
	if present != 1 {
		return 0, false, nil
	}
	var count int64
	if err := db.QueryRowContext(ctx, `select count(*) from `+table).Scan(&count); err != nil {
		return 0, false, err
	}
	return count, true, nil
}
