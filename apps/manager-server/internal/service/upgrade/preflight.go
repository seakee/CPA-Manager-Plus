// Package upgrade is the business layer of the v2 Upgrade operation.
//
// Preflight is strictly read-only. It inspects a stopped 1.x source root,
// reports what the operator would be copying, and never creates, migrates,
// locks in place or otherwise writes anything under that root. A report is
// evidence for a later authorized step, not an authorization: no flag in it
// permits a write.
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
)

// Blocked codes. Parity gaps are never codes: they are reported for explicit
// operator confirmation instead of blocking.
const (
	CodeSourceBusy               = "upgrade_source_busy"
	CodeSourceUnreadable         = "upgrade_source_unreadable"
	CodeSourceVersionUnsupported = "upgrade_source_version_unsupported"
	CodeDiskInsufficient         = "upgrade_disk_insufficient"
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
	// usage-archives/. Preflight reads it; CopyIn copies out of it.
	LegacyRoot string
	// AvailableBytes overrides the free-space probe. Tests inject a requirement
	// without depending on the host disk. Nil uses the platform probe.
	AvailableBytes func(legacyRoot string) (int64, error)
	// TargetRoot is the v2 data directory that receives usage.sqlite, data.key
	// and usage-archives/. CopyIn only.
	TargetRoot string
	// BundleDir is the backup destination. CopyIn only; an existing verified
	// bundle is reused instead of recreated.
	BundleDir string
	// Lock is the caller's held 1.x ownership fence. CopyIn requires the lock
	// bound to the source database; Preflight acquires its own.
	Lock *processlock.Lock
	// ScratchDir is this step's private working area. Reconcile copies the
	// stopped 1.x database here and reads the copy, so the source root is never
	// opened as a database. Unset uses <TargetRoot>/.upgrade-reconcile and the
	// directory is removed before Reconcile returns. Reconcile only.
	ScratchDir string
	// Acknowledged lists the UTC days (YYYY-MM-DD) the operator explicitly
	// accepts as permanently incomplete. Reconcile only.
	Acknowledged []string
	// AcknowledgeParityGaps accepts every day the parity detection reports as
	// lost before v1.14.6, without naming each one. The days actually accepted
	// are still written into the report, so the record names them. Reconcile
	// only.
	AcknowledgeParityGaps bool
	// checkpoint is a package-private synchronous test seam. Production never
	// sets it; every returned error aborts the copy-in at that phase.
	checkpoint func(phase string) error
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
// mismatched or past the projection watermark, plus the days where the deleted
// coverage table disagrees with the archive refs. Counts and dates only: a gap
// does not block, it asks the operator for a later explicit decision.
type ParityGaps struct {
	DeletedEventRefs      int      `json:"deletedEventRefs"`
	MissingProjection     int      `json:"missingProjection"`
	MismatchedProjection  int      `json:"mismatchedProjection"`
	PastWatermark         int      `json:"pastWatermark"`
	ProjectionStateUsable bool     `json:"projectionStateUsable"`
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
	Blocked       []string   `json:"blocked"`
	Ready         bool       `json:"ready"`
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
	report := Report{
		SourceKind:    SourceKindUnknown,
		SourceVersion: VersionUnknown,
		Checks:        []Check{},
		ParityGaps:    ParityGaps{AffectedDates: []string{}},
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

	databasePath := filepath.Join(root, databaseFileName)
	// The fence is taken first and held for the whole inspection; every later
	// read happens while this process exclusively owns the source.
	lock, lockErr := processlock.AcquireExisting(databasePath)
	if lockErr == nil {
		defer func() { _ = lock.Close() }()
	}
	db, schema, openErr := openReadOnlySource(ctx, databasePath)
	if db != nil {
		defer func() { _ = db.Close() }()
	}
	if openErr == nil {
		report.SourceKind = schema.sourceKind()
	} else if errors.Is(openErr, errSourceDatabaseMissing) {
		report.SourceKind = SourceKindMissingDatabase
	}

	// 1. Source stopped and exclusively ours, plus read-only integrity.
	switch {
	case lockErr != nil:
		report.Checks = append(report.Checks, Check{
			Name: CheckSourceOwnership, Status: StatusBlocked, Code: CodeSourceBusy, Detail: ownershipDetail(lockErr),
		})
		block(CodeSourceBusy)
	case openErr != nil:
		report.Checks = append(report.Checks, Check{
			Name: CheckSourceOwnership, Status: StatusBlocked, Code: CodeSourceUnreadable, Detail: unreadableDetail(openErr),
		})
		block(CodeSourceUnreadable)
	default:
		report.Checks = append(report.Checks, Check{
			Name: CheckSourceOwnership, Status: StatusPass, Detail: "exclusive source lock held; quick_check ok",
		})
	}

	// 2. Source version against the pinned contract.
	if openErr != nil {
		report.Checks = append(report.Checks, Check{
			Name: CheckSourceVersion, Status: StatusSkipped, Detail: "source database was not readable",
		})
	} else {
		version := detectSourceVersion(schema, pinnedSourceVersion())
		report.SourceVersion = version.Release
		check := Check{Name: CheckSourceVersion, Status: StatusPass, Detail: version.detail()}
		if !version.compatible() {
			check.Status = StatusBlocked
			check.Code = CodeSourceVersionUnsupported
			block(CodeSourceVersionUnsupported)
		}
		report.Checks = append(report.Checks, check)
	}

	// 3. Deterministic source fingerprint.
	fingerprint, sourceBytes, fingerprintErr := fingerprintSource(root)
	if fingerprintErr != nil {
		report.Checks = append(report.Checks, Check{
			Name: CheckFingerprint, Status: StatusBlocked, Code: CodeSourceUnreadable,
			Detail: "source files could not be read: " + rootCause(fingerprintErr),
		})
		block(CodeSourceUnreadable)
	} else {
		report.Fingerprint = fingerprint
		report.Checks = append(report.Checks, Check{
			Name: CheckFingerprint, Status: StatusPass, Detail: fmt.Sprintf("sha256 over source manifest; sourceBytes=%d", sourceBytes),
		})
	}

	// 4. Disk space for the copy plus 20% headroom.
	check, diskBlocked := diskCheck(root, sourceBytes, options)
	if diskBlocked {
		block(CodeDiskInsufficient)
	}
	report.Checks = append(report.Checks, check)

	// 5. CPA classification. Never an import or a version qualification.
	report.Checks = append(report.Checks, classifyCPA(root))

	// 6. Parity gaps. Reported for confirmation; never blocking.
	if openErr != nil {
		report.Checks = append(report.Checks, Check{
			Name: CheckParity, Status: StatusSkipped, Detail: "source database was not readable",
		})
	} else {
		gaps, parityErr := detectParityGaps(ctx, db, schema)
		if parityErr != nil {
			report.Checks = append(report.Checks, Check{
				Name: CheckParity, Status: StatusSkipped, Detail: "parity detection unavailable: " + rootCause(parityErr),
			})
		} else {
			report.ParityGaps = gaps
			status := StatusPass
			if gaps.any() {
				status = StatusGap
			}
			report.Checks = append(report.Checks, Check{
				Name: CheckParity, Status: status,
				Detail: fmt.Sprintf("deletedEventRefs=%d missingProjection=%d mismatchedProjection=%d pastWatermark=%d coverageDaysWithGaps=%d",
					gaps.DeletedEventRefs, gaps.MissingProjection, gaps.MismatchedProjection, gaps.PastWatermark, gaps.CoverageDaysWithGaps),
			})
		}
	}

	report.Ready = len(report.Blocked) == 0
	return report, nil
}

func (g ParityGaps) any() bool {
	return g.MissingProjection > 0 || g.MismatchedProjection > 0 || g.PastWatermark > 0 || g.CoverageDaysWithGaps > 0
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

func unreadableDetail(err error) string {
	if errors.Is(err, errSourceDatabaseMissing) {
		return "source database is missing"
	}
	if errors.Is(err, errSourceIntegrity) {
		return "source database failed quick_check"
	}
	return "source database could not be opened read-only"
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
