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
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
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
	CodeSourceMissing            = "upgrade_source_missing"
	CodeSourceBusy               = "upgrade_source_busy"
	CodeSourceLockUnavailable    = "upgrade_source_lock_unavailable"
	CodeSourceSymlinked          = "upgrade_source_symlinked"
	CodeSourceUnreadable         = "upgrade_source_unreadable"
	CodeSourceVersionUnsupported = "upgrade_source_version_unsupported"
	CodeDiskInsufficient         = "upgrade_disk_insufficient"
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
	// ScratchDir is the directory Reconcile may work in. It copies the stopped
	// 1.x database into <ScratchDir>/.upgrade-reconcile and reads the copy, so
	// the source root is never opened as a database and the directory named here
	// is never itself removed. Unset uses the v2 data root. The named directory
	// must not be, sit inside, or contain the 1.x source root. Reconcile only.
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
	// A read-only preflight observes the source's ownership marker instead of
	// taking the writer fence. Every later read happens on a bounded read-only
	// snapshot, and the copy-in re-verifies the before/after fingerprint under
	// its own exclusive lock. Taking the lock here would demand write access to
	// a source this step must be able to inspect as read-only media, and would
	// report a read-only mount as a busy source.
	lockErr := processlock.ProbeExisting(databasePath)
	db, schema, openErr := openReadOnlySource(ctx, databasePath)
	if db != nil {
		defer func() { _ = db.Close() }()
	}
	if openErr == nil {
		report.SourceKind = schema.sourceKind()
	} else if errors.Is(openErr, ErrSourceDatabaseMissing) {
		report.SourceKind = SourceKindMissingDatabase
	}

	// 1. No live source writer, plus read-only integrity. A source with no
	// database at all is named as exactly that: reporting the ownership marker
	// would describe a file the mount does not have, and the operator has to
	// mount the 1.x data directory before any lock question exists.
	switch {
	case errors.Is(openErr, ErrSourceDatabaseMissing):
		report.Checks = append(report.Checks, Check{
			Name: CheckSourceOwnership, Status: StatusBlocked, Code: CodeSourceMissing, Detail: unreadableDetail(openErr),
		})
		block(CodeSourceMissing)
	case errors.Is(lockErr, processlock.ErrSymlinkedDatabase), errors.Is(openErr, ErrSourceSymlinked):
		// The same file policy the credential read applies: a link is refused on
		// both paths, with the same code, because the file that is inspected and
		// the file that is read have to be the same one.
		report.Checks = append(report.Checks, Check{
			Name: CheckSourceOwnership, Status: StatusBlocked, Code: CodeSourceSymlinked, Detail: symlinkedSourceDetail(),
		})
		block(CodeSourceSymlinked)
	case errors.Is(lockErr, processlock.ErrLocked):
		report.Checks = append(report.Checks, Check{
			Name: CheckSourceOwnership, Status: StatusBlocked, Code: CodeSourceBusy, Detail: ownershipDetail(lockErr),
		})
		block(CodeSourceBusy)
	case lockErr != nil:
		report.Checks = append(report.Checks, Check{
			Name: CheckSourceOwnership, Status: StatusBlocked, Code: CodeSourceLockUnavailable, Detail: lockUnavailableDetail(lockErr),
		})
		block(CodeSourceLockUnavailable)
	case openErr != nil:
		report.Checks = append(report.Checks, Check{
			Name: CheckSourceOwnership, Status: StatusBlocked, Code: CodeSourceUnreadable, Detail: unreadableDetail(openErr),
		})
		block(CodeSourceUnreadable)
	default:
		report.Checks = append(report.Checks, Check{
			Name: CheckSourceOwnership, Status: StatusPass, Detail: "no live source writer; read-only snapshot; quick_check ok",
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

// ErrAdminUnauthorized is VerifyAdmin's negative verdict: the source was
// readable and its administrator credential rejected the presented key.
var ErrAdminUnauthorized = errors.New("source administrator credential could not be verified")

// ErrCredentialVerifyTimeout is the deadline verdict: the source did not answer
// within CredentialVerifyTimeout, or the caller's own deadline had already
// passed. It is never a verdict on the presented key and never a statement that
// the source is unreadable: a healthy source on a slow volume looks exactly like
// this.
var ErrCredentialVerifyTimeout = errors.New("source credential verification timed out")

// CredentialVerifyTimeout bounds the credential read. It is separate from the
// preflight budget on purpose: authentication is the slow path of every
// authenticated request, so it must stay far below a source qualification.
const CredentialVerifyTimeout = 2 * time.Second

// credentialVerifyTimeout is the bound VerifyAdmin applies. It is
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
func VerifyAdminSource(ctx context.Context, databasePath, key string) error {
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
	ctx, cancel := context.WithTimeout(ctx, credentialVerifyTimeout)
	defer cancel()
	source, openErr := openCredentialSource(databasePath)
	if openErr != nil {
		return openErr
	}
	defer func() { _ = source.close() }()
	return verifyAdminCredential(ctx, source, key)
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

// verifyAdminCredential reads the one credential row from an opened source,
// proves the read came from the file that was inspected, and compares the key.
// The read and the identity check are one function so nothing can be compared
// from an uninspected file even if the caller's open raced a swap.
func verifyAdminCredential(ctx context.Context, source *credentialSource, key string) error {
	credential, found, err := setting.New(source.db).LoadAdminCredential(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return credentialTimeout(err)
		}
		return err
	}
	if err := source.verifyUnchanged(); err != nil {
		return err
	}
	if !found || !security.VerifyAdminKey(credential, key) {
		return ErrAdminUnauthorized
	}
	return nil
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
