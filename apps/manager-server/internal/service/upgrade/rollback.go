package upgrade

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
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
	// LegacyVolumeName is the volume the v1 stack created under the compose
	// project above. It is referenced as an external volume so compose refuses a
	// rollback against a volume that does not exist instead of creating an empty
	// one and starting v1 against no data.
	LegacyVolumeName  = "cpamp_cpa-manager-plus-data"
	LegacyManagerPort = 18317
)

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
	// TagOverride replaces the derived v1 image tag. An empty value derives
	// v<source_version> from the recorded row.
	TagOverride string
}

// Plan is the rollback instruction set. It names the exact image to pin and the
// exact compose invocation, so the operator never has to guess a tag. It grants
// nothing: only RecordRollback changes the operation.
type Plan struct {
	LegacyRoot           string             `json:"legacyRoot"`
	TargetRoot           string             `json:"targetRoot"`
	SourceVersion        string             `json:"sourceVersion"`
	Image                string             `json:"image"`
	Volume               string             `json:"volume"`
	ComposeProject       string             `json:"composeProject"`
	ComposeService       string             `json:"composeService"`
	ComposeInvocation    string             `json:"composeInvocation"`
	ComposeFile          string             `json:"composeFile"`
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
// (upgrade_rollback_not_applicable), the source is still the volume the
// operation described (upgrade_source_changed), the evidence the plan rests on
// still holds (upgrade_rollback_unproven) and the 1.x image resolves to an exact
// version (upgrade_rollback_image_unpinned). A refusal writes nothing.
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
	if fingerprint != recorded.SourceFingerprintBefore {
		return Plan{}, newUpgradeError(UpgradeErrorSourceChanged,
			"the 1.x source is not the volume this operation copied; a v1 restart would not restore the recorded state", nil)
	}
	if strings.TrimSpace(recorded.BackupManifestSHA256) == "" {
		return Plan{}, newUpgradeError(UpgradeErrorRollbackUnproven,
			"the recorded operation names no backup manifest, so the copied-out state cannot be re-verified", nil)
	}
	image, err := rollbackImage(recorded, options.TagOverride)
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{
		LegacyRoot:           sourceRoot,
		TargetRoot:           targetRoot,
		SourceVersion:        recorded.SourceVersion,
		Image:                image,
		Volume:               LegacyVolumeName,
		ComposeProject:       LegacyComposeProject,
		ComposeService:       LegacyComposeService,
		ComposeInvocation:    fmt.Sprintf("docker compose -p %s -f - up -d %s", LegacyComposeProject, LegacyComposeService),
		ComposeFile:          rollbackComposeFile(image),
		Preconditions:        rollbackPreconditions(),
		HealthChecks:         rollbackHealthChecks(),
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

// rollbackImage resolves the exact 1.x tag. A recorded version that is not a
// full release cannot be pinned, and the 1.x compose file's moving tag is never
// an acceptable rollback target.
func rollbackImage(recorded model.UpgradeOperation, override string) (string, error) {
	if tag := strings.TrimSpace(override); tag != "" {
		if tag == "latest" || strings.HasSuffix(tag, ":latest") {
			return "", newUpgradeError(UpgradeErrorRollbackImageUnpinned,
				"a rollback may not pin a moving tag; pass an exact version", nil)
		}
		if strings.HasPrefix(tag, LegacyImageRepository+":") {
			return tag, nil
		}
		return LegacyImageRepository + ":" + tag, nil
	}
	if _, ok := parseRelease(recorded.SourceVersion); !ok {
		return "", newUpgradeError(UpgradeErrorRollbackImageUnpinned,
			"the recorded source version is not an exact release, so no 1.x image can be pinned", nil)
	}
	return LegacyImageRepository + ":v" + strings.TrimPrefix(strings.TrimSpace(recorded.SourceVersion), "v"), nil
}

// rollbackComposeFile is the 1.x service definition with the image pinned. It is
// fed to `docker compose -f -` on stdin, so no file is written outside the v2
// data root and the operator's checkout is never edited.
func rollbackComposeFile(image string) string {
	port := strconv.Itoa(LegacyManagerPort)
	return strings.Join([]string{
		"services:",
		"  " + LegacyComposeService + ":",
		"    image: " + image,
		"    restart: unless-stopped",
		"    ports:",
		`      - "` + port + ":" + port + `"`,
		"    environment:",
		`      HTTP_ADDR: "0.0.0.0:` + port + `"`,
		`      USAGE_DB_PATH: "/data/` + databaseFileName + `"`,
		`      CPA_MANAGER_DATA_KEY_PATH: "/data/` + dataKeyFileName + `"`,
		`      USAGE_COLLECTOR_MODE: "auto"`,
		"    healthcheck:",
		`      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:` + port + `/status"]`,
		"      interval: 10s",
		"      timeout: 3s",
		"      retries: 3",
		"    volumes:",
		"      - " + LegacyVolumeName + ":/data",
		"volumes:",
		"  " + LegacyVolumeName + ":",
		"    external: true",
		"    name: " + LegacyVolumeName,
	}, "\n") + "\n"
}

// rollbackPreconditions are the checks the operator runs before applying the
// plan. The volume must already exist: an external volume makes compose refuse
// rather than create one, but the operator should see that before the restart.
func rollbackPreconditions() []string {
	return []string{
		"docker volume inspect " + LegacyVolumeName,
		"the v1 manager image must already be pulled: docker pull <image>",
	}
}

func rollbackHealthChecks() []string {
	return []string{
		fmt.Sprintf("curl -fsS http://127.0.0.1:%d/status", LegacyManagerPort),
		fmt.Sprintf("open %s read-only and pass a SQLite quick_check", filepath.Join("/data", databaseFileName)),
	}
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

// RollbackEvidence is what the operator observed after applying the plan. It is
// the only input RecordRollback trusts, and it is checked rather than believed:
// a read-only open of the 1.x volume, the status probe when one was run, and the
// source fingerprint that proves this is still the original volume.
type RollbackEvidence struct {
	LegacyRoot string `json:"legacyRoot"`
	TargetRoot string `json:"targetRoot"`
	// Image is the reference the operator actually started. It must be pinned.
	Image string `json:"image"`
	// ReadOnlyOpen reports that the 1.x database opened read-only and passed a
	// quick_check.
	ReadOnlyOpen bool `json:"readOnlyOpen"`
	// StatusURL is the v1 status endpoint the operator probed, if any.
	StatusURL string `json:"statusUrl,omitempty"`
	// StatusOK reports that the probe answered with a 2xx.
	StatusOK bool `json:"statusOk"`
	// SourceFingerprint is the fingerprint the operator observed.
	SourceFingerprint string `json:"sourceFingerprint"`
	ObservedAtMS      int64  `json:"observedAtMs"`
}

// RecordRollback records the rolled_back verdict through Advance and nothing
// else: it never rewrites provenance columns, so the source kind, both
// fingerprints and the backup manifest digest survive the rollback. It refuses
// evidence that does not prove a read-only open and a healthy probe
// (upgrade_rollback_unproven), a source that is not the recorded volume
// (upgrade_source_changed), an unpinned image and a stage a rollback cannot
// start from. Every refusal leaves the row untouched.
func RecordRollback(ctx context.Context, evidence RollbackEvidence) (model.UpgradeOperation, error) {
	if err := ctx.Err(); err != nil {
		return model.UpgradeOperation{}, err
	}
	sourceRoot, err := resolveLegacyRoot(evidence.LegacyRoot)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if sourceRoot, err = canonicalPath(sourceRoot); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("resolve the 1.x source root: %w", err)
	}
	targetRoot, err := resolveTargetRoot(evidence.TargetRoot)
	if err != nil {
		return model.UpgradeOperation{}, err
	}
	if targetRoot, err = canonicalPath(targetRoot); err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("resolve the v2 data root: %w", err)
	}
	if !evidence.ReadOnlyOpen {
		return model.UpgradeOperation{}, newUpgradeError(UpgradeErrorRollbackUnproven,
			"the 1.x volume was not proven to open read-only, so a rollback is not observed", nil)
	}
	// Both observations are required: a read-only open with a passing
	// quick_check proves the 1.x volume still opens, and the status probe proves
	// the 1.x manager is actually serving it again.
	if strings.TrimSpace(evidence.StatusURL) == "" {
		return model.UpgradeOperation{}, newUpgradeError(UpgradeErrorRollbackUnproven,
			"no 1.x status probe was recorded, so a rollback is not observed", nil)
	}
	if !evidence.StatusOK {
		return model.UpgradeOperation{}, newUpgradeError(UpgradeErrorRollbackUnproven,
			"the 1.x status probe answered unhealthy, so a rollback is not observed", nil)
	}
	if evidence.ObservedAtMS <= 0 {
		return model.UpgradeOperation{}, newUpgradeError(UpgradeErrorRollbackUnproven,
			"the rollback evidence carries no observation time", nil)
	}
	databasePath := filepath.Join(targetRoot, databaseFileName)
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
	if _, err := rollbackImage(recorded, evidence.Image); err != nil {
		return model.UpgradeOperation{}, err
	}
	fingerprint, _, err := fingerprintSource(sourceRoot)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("fingerprint the 1.x source: %w", err)
	}
	if recorded.SourceFingerprintBefore == "" || fingerprint != recorded.SourceFingerprintBefore ||
		evidence.SourceFingerprint != recorded.SourceFingerprintBefore {
		return model.UpgradeOperation{}, newUpgradeError(UpgradeErrorSourceChanged,
			"the observed 1.x source is not the volume this operation copied", nil)
	}

	db, err := managersqlite.Open(databasePath)
	if err != nil {
		return model.UpgradeOperation{}, fmt.Errorf("open the v2 database to record the rollback: %w", err)
	}
	defer func() { _ = db.Close() }()
	advanced, err := upgradeoperation.New(db).Advance(ctx, "", model.UpgradeStageRolledBack)
	if err != nil {
		if errors.Is(err, upgradeoperation.ErrStageRegression) {
			return model.UpgradeOperation{}, newUpgradeError(UpgradeErrorRollbackNotApplicable,
				"the operation already reached a terminal stage", err)
		}
		return model.UpgradeOperation{}, fmt.Errorf("record the rolled_back stage: %w", err)
	}
	return advanced, nil
}

// CheckLegacyStatus probes the 1.x status endpoint. It is exported so the CLI
// and the U1-8 harness observe the same signal.
func CheckLegacyStatus(ctx context.Context, url string) (bool, error) {
	trimmed := strings.TrimSpace(url)
	if trimmed == "" {
		return false, errors.New("a 1.x status probe needs a URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, trimmed, nil)
	if err != nil {
		return false, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode >= 200 && response.StatusCode < 300, nil
}

// ProbeLegacyReadOnly opens the 1.x database without writing anything beside it
// and runs a quick_check. It is the machine half of "the v1 volume still
// opens": immutable keeps SQLite from creating a WAL index next to the source,
// which a rollback probe must never do.
func ProbeLegacyReadOnly(ctx context.Context, legacyRoot string) (bool, error) {
	root, err := resolveLegacyRoot(legacyRoot)
	if err != nil {
		return false, err
	}
	path := filepath.Join(root, databaseFileName)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return false, nil
	}
	db, err := sql.Open("sqlite", readOnlyDataSourceName(path, true))
	if err != nil {
		return false, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer func() { _ = db.Close() }()
	var result string
	if err := db.QueryRowContext(ctx, "pragma quick_check").Scan(&result); err != nil {
		return false, nil
	}
	return result == "ok", nil
}
