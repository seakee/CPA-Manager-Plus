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
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
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
	CodeSourceMissing = "upgrade_source_missing"
	// CodeSourceBusy means the 1.x Manager still owns the source.
	CodeSourceBusy            = "upgrade_source_busy"
	CodeSourceLockUnavailable = "upgrade_source_lock_unavailable"
	CodeSourceSymlinked       = "upgrade_source_symlinked"
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
	// CodeTargetLayoutMismatch means the Manager configuration does not put a
	// member the copy-in writes where the service reads it: a data key, an
	// archive tree or the database itself resolves somewhere else, or an
	// environment key would override the copied key. The default data root is
	// refused; an explicit --target states the layout.
	CodeTargetLayoutMismatch = "upgrade_target_layout_mismatch"
)

// CodeCredentialTimeout is the route-level code of ErrCredentialVerifyTimeout.
// It is not a preflight blocked code: authentication fails before the preflight
// runs, and the source itself may be perfectly healthy — only slow.
const CodeCredentialTimeout = "upgrade_credential_timeout"

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
	// TargetRoot is the v2 data directory that receives usage.sqlite, data.key
	// and usage-archives/. CopyIn only.
	TargetRoot string
	// BundleDir is the backup destination. CopyIn only; an existing verified
	// bundle is reused instead of recreated.
	BundleDir string
	// Lock is the caller's held 1.x ownership fence. CopyIn requires the lock
	// bound to the source database; Preflight acquires its own.
	Lock *processlock.Lock
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
	// An absent database and an empty one are the same operator condition: the
	// file the credential read applies its policy to holds nothing to read.
	databaseMissing := false
	switch info, statErr := os.Lstat(databasePath); {
	case os.IsNotExist(statErr):
		databaseMissing = true
	case statErr == nil && info.Mode().IsRegular() && info.Size() == 0:
		databaseMissing = true
	}
	if databaseMissing {
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
	case databaseMissing:
		// There is no database to copy; the ownership check names the condition.
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

	// 1. Source stopped and exclusively ours, plus read-only integrity. One
	// condition, one code: a missing database, a linked one, a live writer and an
	// ownership marker that cannot be inspected are four different things for the
	// operator, and the credential read names the same two file conditions.
	report.Checks = append(report.Checks, ownershipCheck(databaseMissing, lockErr, openErr))
	if code := ownershipCode(databaseMissing, lockErr, openErr); code != "" {
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
func ownershipCheck(databaseMissing bool, lockErr, openErr error) Check {
	check := Check{Name: CheckSourceOwnership, Status: StatusPass}
	if code, detail := ownershipFailureOf(databaseMissing, lockErr, openErr); code != "" {
		check.Status = StatusBlocked
		check.Code = code
		check.Detail = detail
		return check
	}
	check.Detail = "source lock held read-only; private snapshot verified; quick_check ok"
	return check
}

func ownershipCode(databaseMissing bool, lockErr, openErr error) string {
	code, _ := ownershipFailureOf(databaseMissing, lockErr, openErr)
	return code
}

// ownershipFailureOf names the one condition that kept the source from being
// read, in the operator's terms. The credential read answers a mount's file
// conditions with the same two codes, so a linked or absent database reads the
// same way on both paths.
func ownershipFailureOf(databaseMissing bool, lockErr, openErr error) (string, string) {
	switch {
	case databaseMissing, errors.Is(openErr, ErrSourceDatabaseMissing), errors.Is(lockErr, os.ErrNotExist):
		return CodeSourceMissing, "source database is missing"
	case errors.Is(lockErr, processlock.ErrSymlinkedDatabase), errors.Is(openErr, ErrSourceSymlinked):
		return CodeSourceSymlinked, symlinkedSourceDetail()
	case errors.Is(lockErr, processlock.ErrLocked):
		return CodeSourceBusy, ownershipDetail(lockErr)
	case errors.Is(lockErr, processlock.ErrNoExistingLock), errors.Is(lockErr, processlock.ErrHardLinked):
		return CodeSourceLockUnavailable, lockUnavailableDetail(lockErr)
	case lockErr != nil:
		return CodeSourceLockUnavailable, lockUnavailableDetail(lockErr)
	case openErr != nil:
		return ownershipFailure(openErr)
	default:
		return "", ""
	}
}

func ownershipFailure(err error) (string, string) {
	switch {
	case errors.Is(err, errDatabaseMissing), errors.Is(err, ErrSourceDatabaseMissing):
		return CodeSourceMissing, "source database is missing"
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

// ErrAdminUnauthorized is VerifyAdmin's negative verdict: the source was
// readable and its administrator credential rejected the presented key.
var ErrAdminUnauthorized = errors.New("source administrator credential could not be verified")

// ErrCredentialVerifyTimeout is the deadline verdict: the source did not answer
// within the read's budget, or the caller's own deadline had already passed. It
// is never a verdict on the presented key and never a statement that the source
// is unreadable: a healthy source on a slow volume looks exactly like this.
var ErrCredentialVerifyTimeout = errors.New("source credential verification timed out")

// CredentialVerifyTimeout is the base budget of one uncached credential read.
// The read itself is bounded by credentialVerifyBudget, which adds time in
// proportion to the source size and caps the total, because the source is read
// through a private copy whose cost grows with the file. This base stays far
// below a source qualification on purpose, and every unchanged source is
// answered from the credential cache instead of being read again.
const CredentialVerifyTimeout = 2 * time.Second

// credentialVerifyTimeout is the base budget VerifyAdmin applies. It is
// CredentialVerifyTimeout in production; a test shortens it to place the
// deadline inside the read rather than before it.
var credentialVerifyTimeout = CredentialVerifyTimeout

// VerifyAdmin proves the source administrator credential — the key the
// bootstrap Upgrade entry already verified — through the read-only credential
// open and the same credential load and comparison that entry consumes
// (setting.LoadAdminCredential plus security.VerifyAdminKey).
//
// It reads one row and nothing else: no quick_check, no schema vocabulary, no
// parity. Every authenticated request runs this, including every status poll,
// and a whole-file integrity scan here would make a large source answer 503 on
// a route that never reads the database. The integrity check stays in Preflight,
// where the source is actually being qualified.
//
// It applies the same database file policy as the preflight — the database
// itself, never a symbolic or hard link — without taking the source's ownership
// fence: a reader must not exclude the 1.x writer it only observes, and a source
// mounted read-only cannot hand out a writable marker. The copy-in re-verifies
// the source under its own exclusive lock before it writes anything.
func VerifyAdmin(ctx context.Context, legacyRoot, key string) error {
	if strings.TrimSpace(key) == "" {
		return ErrAdminUnauthorized
	}
	root, err := resolveLegacyRoot(legacyRoot)
	if err != nil {
		return err
	}
	return VerifyAdminSource(ctx, filepath.Join(root, databaseFileName), key)
}

// VerifyAdminSource is VerifyAdmin against an explicit source database file.
// The Upgrade surface resolves one credential source, and that source may be the
// Manager's own store rather than a 1.x data directory: the copied-in store
// carries the same administrator credential, and it is the only file left when
// the operator did not mount the source. Naming the file directly keeps the
// verified file and the resolved file the same one.
//
// It reads a 1.x source without ever opening it in place: the database and its
// sidecars are copied into private scratch space by OpenSnapshot, the source is
// digested before and after the copy, and only the copy is opened. `mode=ro`
// would still create a -wal/-shm beside a preserved source, and an immutable
// handle on a source that was cold when it was inspected would silently miss a
// WAL that appears while the read is being set up. Callers passing the Manager's
// own store use VerifyManagerCredential instead: that store is this process's
// live database, not a source volume to preserve.
//
// A proved credential is remembered for the source state it was proved against
// (see credential_cache.go). A source is preserved, so identical bytes carry the
// identical credential row; any change — a WAL that appears, a checkpoint, a
// rewrite, a replaced file — produces a different identity and a fresh read.
func VerifyAdminSource(ctx context.Context, databasePath, key string) error {
	return verifyPreservedCredential(ctx, databasePath, key)
}

// verifyPreservedCredential is the read behind VerifyAdminSource and behind the
// Manager-store fallback when its store is a copy-in target that was not
// materialized completely. extraRoots names directories the private copy must
// not land in besides the database's own directory: the Manager store's data
// root always, and the configured 1.x source root when the caller knows it, so a
// host whose temporary directory points into either tree cannot hide a full
// database copy there.
func verifyPreservedCredential(ctx context.Context, databasePath, key string, extraRoots ...string) error {
	if strings.TrimSpace(key) == "" {
		return ErrAdminUnauthorized
	}
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return ErrSourceDatabaseMissing
	}
	if err := ctx.Err(); err != nil {
		return credentialTimeout(err)
	}
	if _, err := inspectSourceDatabase(databasePath); err != nil {
		return credentialSourceError(err)
	}
	// Every request describes the source it is asking about. An unchanged
	// description reuses a proved verdict — the source is preserved, so the same
	// bytes hold the same credential row — and any change falls through to a full
	// read. The stat is the only cost of a poll that hits the cache.
	identity, err := identifySourceCredential(databasePath)
	if err != nil {
		return err
	}
	cacheKey := credentialCacheKey(key, canonicalPath(databasePath), identity)
	return sourceCredentialCache.verify(ctx, cacheKey, func() error {
		// The first read of a source state pays for the copy and its digests, so
		// its budget scales with the source and is capped; a later poll is served
		// by the cache instead of repeating that cost.
		ctx, cancel := context.WithTimeout(context.Background(), credentialVerifyBudget(identity.database.size))
		defer cancel()
		source, openErr := openCredentialSource(ctx, databasePath, extraRoots...)
		if openErr != nil {
			// The bound is placed before and during the snapshot, so a read that
			// outlives it fails here instead of in the query. That is a deadline,
			// not an unreadable source and never a verdict on the key.
			if ctx.Err() != nil {
				return credentialTimeout(openErr)
			}
			return openErr
		}
		defer func() { _ = source.close() }()
		return verifyAdminCredential(ctx, source, key)
	})
}

// MountDatabase answers the one question the whole Upgrade surface asks about
// the operator's mount: does the configured 1.x data directory hold a database
// this build may read? It applies the same database file policy the credential
// read applies — the path must name the file itself, never a symbolic or hard
// link, with content — so a mount it refuses is exactly a mount that read would
// refuse. A mount that fails the policy therefore holds no database as far as
// the surface is concerned, and the second result names why with the code the
// preflight reports for the file: source_missing for an absent or empty file and
// source_symlinked for a link. Both results are empty when no mount is
// configured at all.
//
// This is the file policy only. It is deliberately weaker than the preflight: an
// absent ownership marker, a live writer or a database that fails its integrity
// check still pass here, because a reader must not exclude the writer it only
// observes and must not scan the whole file on every status poll. Those
// conditions are the preflight's verdict, not this lookup's.
//
// The lookup is a few filesystem calls with no internal deadline: a path on a
// hung network mount answers whenever the filesystem does. A caller that must
// stay responsive — every request of the wizard, including every status poll —
// runs it under its own bound; see upgradewizard.Handler.MountTimeout.
//
// A mount-level refusal must never be reported as a credential verdict. Counting
// a symlinked usage.sqlite as "the mount holds a database" would make the
// credential read refuse the file it was pointed at, so the entry page and both
// reads would answer 503 and the operator could not even reach the page that
// names the condition.
func MountDatabase(legacyRoot string) (string, string) {
	root := strings.TrimSpace(legacyRoot)
	if root == "" {
		return "", ""
	}
	database := filepath.Join(root, databaseFileName)
	// inspectSourceDatabase is the credential read's own inspection: the shared
	// file policy plus the content requirement. Asking any other question here
	// is how the two decisions drift apart.
	if _, err := inspectSourceDatabase(database); err != nil {
		switch {
		case errors.Is(err, ErrSourceDatabaseMissing):
			return "", CodeSourceMissing
		case errors.Is(err, ErrSourceSymlinked):
			return "", CodeSourceSymlinked
		default:
			// A hard-linked, non-regular or unreadable database is refused by the
			// ownership probe before anything is opened, which is the preflight's
			// upgrade_source_lock_unavailable for the same mount.
			return "", CodeSourceLockUnavailable
		}
	}
	return database, ""
}

// CredentialDatabaseFrom applies the surface's one credential rule to a mount
// that has already been inspected: the mount's database when it holds one the
// file policy accepts, otherwise the Manager's own store. The wizard routes
// inspect the mount once per request and cannot afford to ask the filesystem
// again, so the rule is stated here once instead of being restated at each
// caller; CredentialDatabase below is the same rule with the lookup included.
func CredentialDatabaseFrom(mountDatabase, managerDatabase string) string {
	if database := strings.TrimSpace(mountDatabase); database != "" {
		return database
	}
	return strings.TrimSpace(managerDatabase)
}

// CredentialDatabase resolves the one administrator credential of the whole
// Upgrade surface. A configured 1.x data directory that actually holds its
// database — the shared file policy accepts it — is the source; otherwise the
// Manager's own store is, because a v2 root a copy-in produced carries the same
// administrator credential and is the only file left when the operator did not
// mount the source, and because a mount the policy refuses is a mount no read
// can use. The entry leaf and both wizard reads resolve the credential this way
// and nowhere else, so a key accepted at the entry page is accepted by the reads
// and a key kept in the other file is refused by all of them — except across a
// mount change, which is why the entry leaf resolves at startup and the reads
// resolve per request: a mount that appears, disappears or is repointed after
// startup is seen only by the reads, and the entry page keeps the resolution it
// admitted the operator with until it is rebuilt.
func CredentialDatabase(legacyRoot, managerDatabase string) string {
	database, _ := MountDatabase(legacyRoot)
	return CredentialDatabaseFrom(database, managerDatabase)
}

// verifyAdminCredential reads the one credential row from the private copy,
// proves the copy was taken from one stable source state (OpenSnapshot does that
// while it copies), and compares the key. The copy and the comparison are one
// path so nothing can be compared from a file this step never inspected.
func verifyAdminCredential(ctx context.Context, source *credentialSource, key string) error {
	credential, found, err := setting.New(source.database()).LoadAdminCredential(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return credentialTimeout(err)
		}
		return err
	}
	if !found || !security.VerifyAdminKey(credential, key) {
		return ErrAdminUnauthorized
	}
	return nil
}

// VerifyManagerCredential is VerifyAdminSource against the Manager's own store
// rather than a 1.x source volume.
//
// The two files need different reads because the invariants differ. A 1.x source
// is preserved byte for byte, so its handle is immutable or a private copy
// (VerifyAdminSource). The Manager store is this process's own live database: it
// is opened in place read-only, and immutable only when no live WAL holds rows,
// exactly as the admission and status reads of the same store already do. A
// snapshot of it would copy a database that changes under it and answer 503 on
// the copy race, which is the wrong verdict for a poll.
//
// The one exception is a target that an unfinished copy-in has not materialized
// completely. The marker is read before any open: in the `materializing` phase
// the target still holds the members the resumed move compares with its backup,
// and a read-only open of a store holding a copied WAL joins that WAL and
// rewrites `-shm`. This route authenticates every status poll, so it is the
// first reader to reach the target; it must leave it exactly as it found it and
// still prove the administrator key. The key is therefore verified against a
// private snapshot outside both the target's data root and, when the caller
// names it, the configured 1.x source root. sourceRoot is that second root; an
// empty value means the caller knows of none.
//
// The Manager store's credential is not cached either. It is the live database:
// ordinary Manager traffic appends to its WAL and checkpoints it, so a cached
// verdict would either miss on almost every poll or, on a filesystem with a
// coarse modification time, answer with a credential row that was already
// rotated. The read itself is one indexed row in the process's own open store,
// not the whole-file copy that makes a preserved source expensive. Only the
// materializing target is read as a preserved source, and only it may be
// answered from that state's remembered verdict.
func VerifyManagerCredential(ctx context.Context, databasePath, sourceRoot, key string) error {
	if strings.TrimSpace(key) == "" {
		return ErrAdminUnauthorized
	}
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return ErrSourceDatabaseMissing
	}
	if err := ctx.Err(); err != nil {
		return credentialTimeout(err)
	}
	materializing, err := materializingManagerTarget(databasePath)
	if err != nil {
		return err
	}
	if materializing {
		return verifyPreservedCredential(ctx, databasePath, key, sourceRoot)
	}
	ctx, cancel := context.WithTimeout(ctx, credentialVerifyTimeout)
	defer cancel()
	db, err := openManagerStore(databasePath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	credential, found, err := setting.New(db).LoadAdminCredential(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return credentialTimeout(err)
		}
		return err
	}
	if !found || !security.VerifyAdminKey(credential, key) {
		return ErrAdminUnauthorized
	}
	return nil
}

// materializingManagerTarget reports whether databasePath is the target of a
// copy-in that has not finished moving its members. The marker is read before
// the store is opened, the same order the admission and the status projection
// use; a marker that names another database file describes another run and is
// not this store. A marker that cannot be read fails closed: an unreadable
// provenance record could hide exactly the phase this check exists for.
func materializingManagerTarget(databasePath string) (bool, error) {
	marker, inFlight, err := copyinmarker.Read(filepath.Dir(databasePath))
	if err != nil {
		return false, err
	}
	if !inFlight || marker.Phase != copyinmarker.PhaseMaterializing {
		return false, nil
	}
	return copyinmarker.SameTarget(marker.TargetDatabase, databasePath), nil
}

// openManagerStore opens the Manager's own database read-only. immutable is used
// only when no live WAL can hold rows: an immutable handle ignores the WAL, so
// joining a live one would read a stale row instead of failing.
func openManagerStore(databasePath string) (*sql.DB, error) {
	info, err := os.Lstat(databasePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrSourceDatabaseMissing
		}
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, ErrSourceDatabaseMissing
	}
	immutable := true
	if wal, walErr := os.Lstat(databasePath + "-wal"); walErr == nil && wal.Size() > 0 {
		immutable = false
	}
	db, err := sql.Open("sqlite", readOnlyDataSourceName(databasePath, immutable))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// credentialTimeout names the deadline verdict while keeping the cause for logs.
func credentialTimeout(cause error) error {
	if cause == nil {
		return ErrCredentialVerifyTimeout
	}
	return fmt.Errorf("%w: %v", ErrCredentialVerifyTimeout, cause)
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

// lockUnavailableDetail reports an ownership marker a read-only observation
// cannot inspect at all. It is separate from a busy source: nothing is holding
// the source, but this step cannot prove that from the source itself.
func lockUnavailableDetail(err error) string {
	switch {
	case errors.Is(err, processlock.ErrNoExistingLock):
		return "source has no ownership marker to inspect; the ownership of the source cannot be established"
	case errors.Is(err, processlock.ErrHardLinked):
		return "source database has multiple hard links"
	default:
		return "source ownership marker could not be opened read-only"
	}
}

// symlinkedSourceDetail names the one condition the shared database file policy
// refuses before any read: the configured database is a link, so the file that
// would be inspected is not the file that would be read.
func symlinkedSourceDetail() string {
	return "source database is a symbolic link; the upgrade reads the file itself, not a link"
}

func unreadableDetail(err error) string {
	if errors.Is(err, ErrSourceDatabaseMissing) {
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
