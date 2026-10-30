// Package upgrade is the business layer of the v2 Upgrade operation.
//
// Preflight is strictly read-only. It inspects a stopped 1.x source root,
// reports what the operator would be copying, and never creates, migrates,
// locks in place or otherwise writes anything under that root. A report is
// evidence for a later authorized step, not an authorization: no flag in it
// permits a write.
//
// The source database is never opened in place. A plain read-only open of a WAL
// database creates -shm/-wal files beside it, which would write into a source
// that is supposed to be preserved. Preflight copies usage.sqlite and its WAL
// sidecars into private scratch space, proves that the source did not change
// while the copy was taken, and reads only that copy.
package upgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
)

// Check names, in report order.
const (
	CheckSourceOwnership   = "source_ownership"
	CheckSourceVersion     = "source_version"
	CheckFingerprint       = "fingerprint"
	CheckDisk              = "disk"
	CheckCPAClassification = "cpa_classification"
	CheckParity            = "parity"
)

// Check statuses. Only StatusBlocked contributes to Report.Blocked.
const (
	StatusPass    = "pass"
	StatusBlocked = "blocked"
	StatusSkipped = "skipped"
	StatusGap     = "gap"
	// StatusNotApplicable marks a check the source cannot answer, such as parity
	// on a source that predates the retained projection. It is not a pass: an
	// unanswerable check never reports a clean zero.
	StatusNotApplicable = "not-applicable"
)

// Blocked codes. Parity gaps are never codes: they are reported for explicit
// operator confirmation instead of blocking.
const (
	// CodeSourceBusy means the 1.x Manager still owns the source.
	CodeSourceBusy = "upgrade_source_busy"
	// CodeSourceUnreadable means the source cannot be used as a 1.x Manager
	// database: missing, broken, or not a Manager database at all.
	CodeSourceUnreadable = "upgrade_source_unreadable"
	// CodeSourceVersionUnsupported means the declared release differs from the
	// pinned one. Both values are in the check detail.
	CodeSourceVersionUnsupported = "upgrade_source_version_unsupported"
	// CodeSourceVersionUndeclared means no release was declared. The version is
	// not recoverable from the database, so a declaration is required.
	CodeSourceVersionUndeclared = "upgrade_source_version_undeclared"
	// CodeSourceAlreadyV2 means the source already carries v2-only tables.
	CodeSourceAlreadyV2 = "upgrade_source_already_v2"
	// CodeSourceChanged means the source was written while it was being copied.
	CodeSourceChanged = "upgrade_source_changed"
	// CodeSnapshotFailed means the private copy could not be made.
	CodeSnapshotFailed = "upgrade_snapshot_failed"
	// CodeDiskInsufficient means the target volume cannot hold the copy.
	CodeDiskInsufficient = "upgrade_disk_insufficient"
	// CodeDiskProbeFailed means free space could not be measured. An unknown
	// value is never treated as enough.
	CodeDiskProbeFailed = "upgrade_disk_probe_failed"
)

// Source kinds reuse the vocabulary of the existing legacy preflight.
const (
	SourceKindLegacyManager   = "legacy-manager"
	SourceKindNativeV2        = "native-v2"
	SourceKindMissingDatabase = "missing-database"
	SourceKindUnknown         = "unknown"
)

const (
	databaseFileName = "usage.sqlite"
	dataKeyFileName  = "data.key"
	archivesDirName  = "usage-archives"
	// The v1 Docker stack mounts the managed CPA next to the Manager data
	// directory, never inside it.
	managedCPAConfigRelativePath = "../legacy-cpa/config.yaml"
)

type Options struct {
	// LegacyRoot is the v1 data directory: usage.sqlite, data.key and
	// usage-archives/.
	LegacyRoot string
	// DeclaredVersion is the 1.x release the operator declares the source was
	// running. A 1.x database does not record its release, so this declaration
	// is the only exact version signal; it must equal the pinned release. An
	// empty value blocks the source.
	DeclaredVersion string
	// ScratchDir is the base directory for the private snapshot copy. It has to
	// be writable and big enough to hold the source database. Empty uses the OS
	// temporary directory. A scratch directory inside the legacy source root or
	// inside TargetDir is refused with upgrade_snapshot_failed before anything
	// is created: the transient copy must never land in a tree that is supposed
	// to stay untouched or that will receive the upgrade.
	ScratchDir string
	// TargetDir is the directory on the volume that will receive the upgrade
	// copy. The disk probe measures free space there. Empty probes the scratch
	// volume; it never measures the working directory, which says nothing about
	// either volume.
	TargetDir string
	// AvailableBytes overrides the free-space probe. Tests inject a requirement
	// without depending on the host disk. Nil uses the platform probe.
	AvailableBytes func(targetDir string) (int64, error)
	// afterCopy runs after the snapshot copy and before the second source
	// fingerprint. Tests use it to simulate a source that was not stopped.
	afterCopy func()
}

// Check is one preflight observation. Detail is free text for the operator and
// never carries a credential or a raw source path.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ParityGaps counts raw-deleted events whose retained projection is missing,
// mismatched, stale or past the projection watermark, plus the days where the
// deleted coverage table disagrees with the archive refs. Counts and dates only:
// a gap does not block, it asks the operator for a later explicit decision.
//
// Applicable is false when the source cannot answer the question at all, for
// example because it predates usage_archive_event_refs or the deleted coverage
// table. Reason then names the missing evidence, and every count stays zero
// because it was never measured — a zero count with Applicable false is not a
// clean result.
type ParityGaps struct {
	Applicable            bool     `json:"applicable"`
	Reason                string   `json:"reason,omitempty"`
	DeletedEventRefs      int      `json:"deletedEventRefs"`
	MissingProjection     int      `json:"missingProjection"`
	MismatchedProjection  int      `json:"mismatchedProjection"`
	StaleProjection       int      `json:"staleProjection"`
	PastWatermark         int      `json:"pastWatermark"`
	ProjectionStateUsable bool     `json:"projectionStateUsable"`
	ProjectionStateReason string   `json:"projectionStateReason,omitempty"`
	CoverageDaysWithGaps  int      `json:"coverageDaysWithGaps"`
	AffectedDates         []string `json:"affectedDates"`
}

// Report is the whole preflight result. Ready is true only when Blocked is
// empty; nothing else in the report can make a source ready.
type Report struct {
	SourceKind    string     `json:"sourceKind"`
	SourceVersion string     `json:"sourceVersion"`
	Fingerprint   string     `json:"fingerprint"`
	Checks        []Check    `json:"checks"`
	ParityGaps    ParityGaps `json:"parityGaps"`
	// MissingTables is the structural lower bound the source failed to meet. A
	// declaration that matches the pinned release never clears this list.
	MissingTables []string `json:"missingTables"`
	Blocked       []string `json:"blocked"`
	Ready         bool     `json:"ready"`
}

// Preflight inspects the source without writing to it. A returned error is
// reserved for unusable input (missing root, cancelled context); every source
// condition is expressed as a blocked report.
func Preflight(ctx context.Context, options Options) (Report, error) {
	root, err := resolveLegacyRoot(options.LegacyRoot)
	if err != nil {
		return Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	// The version ladder is built before anything is inspected. A malformed
	// release string is a broken build contract, not a source condition: it
	// must fail the preflight outright instead of degrading the newer-marker
	// rule to an empty list that accepts everything.
	ladder, err := newVersionLadder(pinnedSourceVersion())
	if err != nil {
		return Report{}, fmt.Errorf("upgrade preflight: %w", err)
	}
	report := Report{
		SourceKind:    SourceKindUnknown,
		SourceVersion: VersionUnknown,
		Checks:        []Check{},
		ParityGaps:    ParityGaps{AffectedDates: []string{}},
		MissingTables: []string{},
		Blocked:       []string{},
	}
	block := func(code string) {
		for _, existing := range report.Blocked {
			if existing == code {
				return
			}
		}
		report.Blocked = append(report.Blocked, code)
	}

	scratchBase := strings.TrimSpace(options.ScratchDir)
	targetDir := strings.TrimSpace(options.TargetDir)

	databasePath := filepath.Join(root, databaseFileName)
	if _, statErr := os.Lstat(databasePath); os.IsNotExist(statErr) {
		report.SourceKind = SourceKindMissingDatabase
	}

	// A scratch copy inside the source root would write the whole database back
	// into the tree this command promises to preserve, and one inside the target
	// root would leave upgrade scratch in the volume the copy is measured
	// against. Both are refused before a single directory is created.
	scratchErr := ValidateScratchDir(scratchBase, root, targetDir)

	// The fingerprint is the state the snapshot has to preserve. It is taken
	// before the copy and repeated afterwards, so a source that is still being
	// written is refused instead of half-imported.
	before, fingerprintErr := fingerprintSource(root)

	// The fence is taken first and held for the whole inspection. It is opened
	// read-only: a stopped source is normally preserved without write access.
	lock, lockErr := processlock.AcquireExistingReadOnly(databasePath)
	if lockErr == nil {
		defer func() { _ = lock.Close() }()
	}
	var snapshot *Snapshot
	var openErr error
	var afterFingerprintErr error
	switch {
	case scratchErr != nil:
		openErr = scratchErr
	case lockErr == nil:
		snapshot, openErr = OpenSnapshot(ctx, databasePath, scratchBase)
	}
	if snapshot != nil {
		defer func() { _ = snapshot.Close() }()
		if options.afterCopy != nil {
			options.afterCopy()
		}
		if fingerprintErr == nil {
			// A second fingerprint that cannot be taken is as unusable as a
			// source that changed: without it there is no proof the copy matches
			// one stable state, so it blocks instead of being ignored.
			after, afterErr := fingerprintSource(root)
			switch {
			case afterErr != nil:
				afterFingerprintErr = afterErr
			case after.Digest != before.Digest:
				_ = snapshot.Close()
				snapshot = nil
				openErr = ErrSourceChanged
			}
		}
	}

	var schema sourceSchema
	if snapshot != nil {
		if err := quickCheckSource(ctx, snapshot.DB()); err != nil {
			openErr = err
		} else if schema, err = readSourceSchema(ctx, snapshot.DB()); err != nil {
			openErr = err
		} else {
			report.SourceKind = schema.sourceKind()
		}
	}

	// Nothing below reads the source when the fence could not be taken: a live
	// source is never inspected, not even for a version string.
	readable := lockErr == nil && openErr == nil

	// 1. Source stopped and exclusively ours, plus read-only integrity.
	report.Checks = append(report.Checks, ownershipCheck(lockErr, openErr))
	if code := ownershipCode(lockErr, openErr); code != "" {
		block(code)
	}

	// 2. Declared source version against the pinned contract and the v1.14
	// structural lower bound, plus the v2-only table guard and the markers the
	// pinned release genuinely lacks.
	if !readable {
		report.Checks = append(report.Checks, Check{
			Name: CheckSourceVersion, Status: StatusSkipped, Detail: "source database was not readable",
		})
	} else {
		report.SourceVersion = declaredVersion(options.DeclaredVersion)
		report.MissingTables = schema.missingTables(requiredLegacyTables)
		check := versionCheck(
			report.SourceKind, schema.v2OnlyTables(), report.MissingTables, ladder.newer(schema),
			options.DeclaredVersion, pinnedSourceVersion(),
		)
		if check.Status == StatusBlocked {
			block(check.Code)
		}
		report.Checks = append(report.Checks, check)
	}

	// 3. Deterministic source fingerprint. The reported value is the state the
	// snapshot was taken from.
	if fingerprintErr != nil {
		report.Checks = append(report.Checks, Check{
			Name: CheckFingerprint, Status: StatusBlocked, Code: CodeSourceUnreadable,
			Detail: "source files could not be read: " + rootCause(fingerprintErr),
		})
		block(CodeSourceUnreadable)
	} else {
		report.Fingerprint = before.Digest
		check := Check{
			Name: CheckFingerprint, Status: StatusPass,
			Detail: fmt.Sprintf("sha256 over source manifest; sourceBytes=%d", before.Bytes),
		}
		switch {
		case errors.Is(openErr, ErrSourceChanged):
			check.Status = StatusBlocked
			check.Code = CodeSourceChanged
			check.Detail = fmt.Sprintf("sha256 of the pre-copy state; sourceBytes=%d", before.Bytes)
			block(CodeSourceChanged)
		case afterFingerprintErr != nil:
			// The post-copy fingerprint is the proof the copy matches the
			// reported state. Without it there is no evidence, so it blocks
			// instead of passing on the pre-copy value alone.
			check.Status = StatusBlocked
			check.Code = CodeSourceUnreadable
			check.Detail = "source files could not be read after the snapshot: " + rootCause(afterFingerprintErr)
			block(CodeSourceUnreadable)
		}
		report.Checks = append(report.Checks, check)
	}

	// 4. Disk space for the copy plus 20% headroom, on the volume that will
	// receive it.
	check, diskBlocked := diskCheck(before.Bytes, targetDir, options)
	if diskBlocked {
		block(check.Code)
	}
	report.Checks = append(report.Checks, check)

	// 5. CPA classification. Never an import or a version qualification.
	report.Checks = append(report.Checks, classifyCPA(root))

	// 6. Parity gaps. Reported for confirmation; never blocking. A source that
	// cannot answer the question says so instead of reporting a clean zero.
	if !readable {
		report.Checks = append(report.Checks, Check{
			Name: CheckParity, Status: StatusSkipped, Detail: "source database was not readable",
		})
	} else {
		gaps, parityErr := detectParityGaps(ctx, snapshot.DB(), schema)
		switch {
		case parityErr != nil:
			report.Checks = append(report.Checks, Check{
				Name: CheckParity, Status: StatusSkipped, Detail: "parity detection unavailable: " + rootCause(parityErr),
			})
		case !gaps.Applicable:
			report.ParityGaps = gaps
			report.Checks = append(report.Checks, Check{
				Name: CheckParity, Status: StatusNotApplicable,
				Detail: "parity not applicable: " + gaps.Reason,
			})
		default:
			report.ParityGaps = gaps
			status := StatusPass
			if gaps.any() {
				status = StatusGap
			}
			detail := fmt.Sprintf("deletedEventRefs=%d missingProjection=%d mismatchedProjection=%d staleProjection=%d pastWatermark=%d coverageDaysWithGaps=%d",
				gaps.DeletedEventRefs, gaps.MissingProjection, gaps.MismatchedProjection, gaps.StaleProjection,
				gaps.PastWatermark, gaps.CoverageDaysWithGaps)
			if !gaps.ProjectionStateUsable && gaps.ProjectionStateReason != "" {
				detail += "; projection state unusable: " + gaps.ProjectionStateReason
			}
			report.Checks = append(report.Checks, Check{Name: CheckParity, Status: status, Detail: detail})
		}
	}

	report.Ready = len(report.Blocked) == 0
	return report, nil
}

func (g ParityGaps) any() bool {
	return g.MissingProjection > 0 || g.MismatchedProjection > 0 || g.StaleProjection > 0 ||
		g.PastWatermark > 0 || g.CoverageDaysWithGaps > 0
}

// ownershipCheck reports the exclusivity and integrity observation.
func ownershipCheck(lockErr, openErr error) Check {
	check := Check{Name: CheckSourceOwnership, Status: StatusPass}
	switch {
	case lockErr != nil:
		check.Status = StatusBlocked
		check.Code = CodeSourceBusy
		check.Detail = ownershipDetail(lockErr)
	case openErr != nil:
		check.Status = StatusBlocked
		check.Code, check.Detail = ownershipFailure(openErr)
	default:
		check.Detail = "source lock held read-only; private snapshot verified; quick_check ok"
	}
	return check
}

func ownershipCode(lockErr, openErr error) string {
	switch {
	case lockErr != nil:
		return CodeSourceBusy
	case openErr != nil:
		code, _ := ownershipFailure(openErr)
		return code
	default:
		return ""
	}
}

func ownershipFailure(err error) (string, string) {
	switch {
	case errors.Is(err, errDatabaseMissing):
		return CodeSourceUnreadable, "source database is missing"
	case errors.Is(err, errSourceIntegrity):
		return CodeSourceUnreadable, "source database failed quick_check"
	case errors.Is(err, ErrSourceChanged):
		return CodeSourceChanged, "source files changed while the snapshot was taken"
	case errors.Is(err, ErrSnapshotFailed):
		return CodeSnapshotFailed, rootCause(err)
	default:
		return CodeSourceUnreadable, "source database could not be read through a private snapshot"
	}
}

func resolveLegacyRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("legacy source root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve legacy source root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("legacy source root %s does not exist", absolute)
		}
		return "", fmt.Errorf("inspect legacy source root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("legacy source root %s is not a directory", absolute)
	}
	return absolute, nil
}

// ownershipDetail names the failure without leaking the source path.
func ownershipDetail(err error) string {
	switch {
	case errors.Is(err, processlock.ErrLocked):
		return "source lock is held by another process; stop the 1.x Manager first"
	case errors.Is(err, processlock.ErrNoExistingLock):
		return "source lock file is missing; the source ownership marker cannot prove exclusivity"
	case errors.Is(err, processlock.ErrHardLinked):
		return "source database has multiple hard links"
	default:
		return "source lock could not be taken"
	}
}

// rootCause is the shortest stable description of a wrapped error.
func rootCause(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	// Keep the report free of full host paths.
	if index := strings.LastIndex(text, ": "); index >= 0 {
		text = text[index+2:]
	}
	return text
}
