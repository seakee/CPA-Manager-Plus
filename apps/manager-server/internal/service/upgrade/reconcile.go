package upgrade

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/coverageincomplete"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/modelprice"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pricing"
)

// Reconcile compares a copied-in v2 store against the stopped 1.x source it was
// copied from, over the four usage domains the copy claims to preserve, and
// permanently marks the UTC days that can never be completed again.
//
// Reconcile is the only Upgrade step that reads both stores. It never writes the
// 1.x root: the source database and its sidecars are copied into this step's
// scratch area and the copy is read, because a read-only open of a WAL store
// still creates a WAL index beside the original and would change the very
// fingerprint the copy-in recorded. The source fingerprint is taken before and
// after the comparison; a source that moved is refused instead of compared.
//
// A disagreement blocks: nothing is marked and the operation row does not
// advance. A domain the 1.x source holds rows of but the v2 store cannot show
// blocks the same way, and no acknowledgement can waive that: lost data is not a
// measured difference. The one exception is a derived rollup the store cleared
// while the table is still there — its rows can be rebuilt from the retained
// events, so those days are marked under their own reason and the operator may
// acknowledge them (see reconcileDerivedDomains). Only the days the operator
// acknowledged are marked, and a marked day stays marked for the life of the
// store.

// Reconcile domain names, in report order. They are the domains the copy claims
// to preserve, not the operation's domain vocabulary.
const (
	ReconcileDomainRetained = "retained"
	ReconcileDomainPricing  = "pricing"
	ReconcileDomainCoverage = "coverage"
	ReconcileDomainHistory  = "history"
)

// Compared column names. They are stable codes and are always the name of the
// column that disagreed: a value column (count, total_tokens, cost_micros, ...)
// or an identity column (api_key_hash, provider, bucket_ms, ...), so an
// operator can find the row in the store from the report alone.
const (
	mismatchFieldPresence                = "presence"
	mismatchFieldRow                     = "row"
	mismatchFieldEvents                  = "count"
	mismatchFieldTokens                  = "total_tokens"
	mismatchFieldFailed                  = "failed"
	mismatchFieldCalls                   = "calls"
	mismatchFieldCost                    = "cost_micros"
	mismatchFieldDeleted                 = "deleted_event_count"
	mismatchFieldMinTimestamp            = "min_timestamp_ms"
	mismatchFieldMaxTimestamp            = "max_timestamp_ms"
	mismatchFieldHour                    = "hour"
	mismatchFieldBucketMS                = "bucket_ms"
	mismatchFieldModel                   = "model"
	mismatchFieldBillingModel            = "billing_model"
	mismatchFieldPricingModel            = "pricing_model"
	mismatchFieldServiceTier             = "service_tier"
	mismatchFieldThreshold               = "context_threshold_tokens"
	mismatchFieldAPIKeyHash              = "api_key_hash"
	mismatchFieldProvider                = "provider"
	mismatchFieldAccountSnapshot         = "account_snapshot"
	mismatchFieldAuthLabelSnapshot       = "auth_label_snapshot"
	mismatchFieldAuthFileSnapshot        = "auth_file_snapshot"
	mismatchFieldExecutorType            = "executor_type"
	mismatchFieldInputTokens             = "input_tokens"
	mismatchFieldOutputTokens            = "output_tokens"
	mismatchFieldCachedTokens            = "cached_tokens"
	mismatchFieldCacheReadTokens         = "cache_read_tokens"
	mismatchFieldCacheCreationTokens     = "cache_creation_tokens"
	mismatchFieldLongInputTokens         = "long_input_tokens"
	mismatchFieldLongOutputTokens        = "long_output_tokens"
	mismatchFieldLongCachedTokens        = "long_cached_tokens"
	mismatchFieldLongCacheReadTokens     = "long_cache_read_tokens"
	mismatchFieldLongCacheCreationTokens = "long_cache_creation_tokens"
)

const (
	// reconcileScratchDirectory is this step's reserved scratch area. It is the
	// only path Reconcile creates and removes: it sits inside the caller's
	// scratch root (the v2 data root by default), so the directory a caller
	// named is never the thing deleted. The copy of the 1.x database exists only
	// for the length of the comparison, and the report (which names counts and
	// days, never rows) is what survives.
	reconcileScratchDirectory = ".upgrade-reconcile"
	// utcDayMilliseconds is the width of the day buckets every compared table
	// keys on.
	utcDayMilliseconds = int64(24 * 60 * 60 * 1000)
	// costMicrosPerUnit converts a dollar cost into micro-units.
	costMicrosPerUnit = 1_000_000.0
)

// reconciledOperationDomains maps the compared domains onto the operation
// vocabulary this step establishes. The retained events, their account history
// rollups (derived from the same events) and the deleted-coverage table each
// have their own operation domain; a domain that was not applicable is never
// reported as done.
var reconciledOperationDomains = map[string][]string{
	ReconcileDomainRetained: {model.UpgradeDomainRetainedProjection},
	ReconcileDomainHistory:  {model.UpgradeDomainRetainedProjection},
	ReconcileDomainPricing:  {model.UpgradeDomainPricing},
	ReconcileDomainCoverage: {model.UpgradeDomainCoverage},
}

// reconcileDerivedDomains names the compared domains whose store table is a
// projection of the retained event log rather than data of its own. The
// boundary decides what a vanished table means:
//
//   - A derived rollup (pricing, account history) is rebuilt row by row from the
//     retained events, so a rollup table that is present but holds none of the
//     source's days is a gap a rebuild can still close. Reconcile marks those
//     days incomplete under ReasonDerivedRollupLost and lets the operator
//     acknowledge them, exactly like any other day it cannot complete, instead
//     of blocking a migration that has every event it needs.
//   - Every other compared table holds data that exists nowhere else: the
//     retained events are the log itself and the deleted coverage table records
//     which events an archive run removed. Neither can be recomputed, so a
//     missing table, or one that holds none of the source's days, stays an
//     unwaivable block.
//
// A cleared derived table is still a permanent mark, not a repair: no code in
// this step rebuilds a rollup, so the day is recorded as incomplete and stays
// marked for the life of the store.
var reconcileDerivedDomains = map[string]bool{
	ReconcileDomainPricing: true,
	ReconcileDomainHistory: true,
}

// ReconcileMismatch is one compared column that disagreed. A day present on one
// side only is reported as Field "presence" with "present"/"absent" values,
// never as a skip, and a row present on one side only as Field "row". Dimension
// names the row inside the day — the hour, model and key of a retained bucket,
// the full identity of a pricing or history rollup — or the value of the
// identity column that moved.
type ReconcileMismatch struct {
	Day       string `json:"day"`
	Field     string `json:"field"`
	Dimension string `json:"dimension,omitempty"`
	Source    string `json:"source"`
	Target    string `json:"target"`
}

// ReconcileDomainReport is one domain's comparison. Applicable is false when a
// side does not hold the table at all, and Reason says which side that is.
// Table names the compared store table, so a blocked domain is actionable.
// SourceDays counts the UTC days the 1.x source holds for the domain and
// ComparedDays the days both sides hold: a day only one side holds is a
// mismatch, never a compared day.
//
// Recoverable marks a derived domain whose table is there but holds none of the
// days the source holds. The rows of a derived rollup are rebuildable from the
// retained event log, so a cleared rollup is a gap the operator may
// acknowledge instead of a store that lost data with no way back. A missing
// table is never recoverable: nothing proves the rows can be rebuilt from a
// table that is not there. See reconcileDerivedDomains for the boundary.
type ReconcileDomainReport struct {
	Domain       string              `json:"domain"`
	Table        string              `json:"table"`
	Applicable   bool                `json:"applicable"`
	Recoverable  bool                `json:"recoverable,omitempty"`
	Reason       string              `json:"reason,omitempty"`
	SourceDays   int                 `json:"sourceDays"`
	ComparedDays int                 `json:"comparedDays"`
	Mismatches   []ReconcileMismatch `json:"mismatches"`
}

// ReconcileMarkedDay is one day this reconciliation concluded is permanently
// incomplete, with the reason it is stored under.
type ReconcileMarkedDay struct {
	Day    string `json:"day"`
	Reason string `json:"reason"`
}

// ReconcileEvidence is the durable half of a reconciliation: what was compared,
// which days cannot be completed again, and what the operator acknowledged.
// This is exactly what the operation row stores.
type ReconcileEvidence struct {
	SourceFingerprint  string                  `json:"sourceFingerprint"`
	Domains            []ReconcileDomainReport `json:"domains"`
	ParityGapDays      []string                `json:"parityGapDays"`
	MismatchDays       []string                `json:"mismatchDays"`
	AcknowledgedDays   []string                `json:"acknowledgedDays"`
	UnacknowledgedDays []string                `json:"unacknowledgedDays"`
	IncompleteDays     []ReconcileMarkedDay    `json:"incompleteDays"`
	NewlyMarkedDays    int                     `json:"newlyMarkedDays"`
}

// ReconcileReport is the evidence plus what the operation row now says. The row
// description is not stored with the evidence: the stage and the completed
// domains live in their own columns.
type ReconcileReport struct {
	ReconcileEvidence
	Recorded    bool               `json:"recorded"`
	Stage       model.UpgradeStage `json:"stage,omitempty"`
	DomainsDone []string           `json:"domainsDone,omitempty"`
	UpdatedAtMS int64              `json:"updatedAtMs,omitempty"`
}

// Reconcile compares the copied-in v2 store with the 1.x source it came from.
//
// A clean comparison marks nothing, records the evidence and advances the
// operation to "reconcile"; the marks, the evidence and the stage are one
// transaction, so an interruption can never leave one without the others. A
// disagreement, or a day the source can never complete again, blocks with
// ErrReconcileMismatch and leaves the store untouched; the returned report still
// names every day that needs an explicit acknowledgement. A run that compared no
// day of any domain established nothing to record, so it refuses with
// ErrReconcileNothingCompared rather than reporting a stage that names no
// domain.
func Reconcile(ctx context.Context, options Options) (ReconcileReport, error) {
	if err := ctx.Err(); err != nil {
		return ReconcileReport{}, err
	}
	sourceRoot, targetRoot, bundleDir, err := reconcileRoots(options)
	if err != nil {
		return ReconcileReport{}, err
	}
	acknowledged, err := acknowledgedDays(options.Acknowledged)
	if err != nil {
		return ReconcileReport{}, err
	}
	paths := newTargetPaths(targetRoot)

	recorded, found, err := readRecordedCopyIn(ctx, paths.Database)
	if err != nil {
		return ReconcileReport{}, err
	}
	if !found {
		return ReconcileReport{}, errors.New("the v2 data root holds no recorded copy-in, so there is nothing to reconcile against")
	}
	// The step may only run where it could still record its result. A store the
	// operation already carried past "reconcile", or one a rollback closed, can
	// never take this stage, so comparing it would write permanent marks for
	// numbers that no longer match by design and report a success it cannot
	// record. This is the same monotonicity rule Advance enforces, asked before
	// anything is read or written.
	if recorded.Stage != "" && !recorded.Stage.CanAdvanceTo(model.UpgradeStageReconcile) {
		return ReconcileReport{}, fmt.Errorf(
			"the recorded operation is at stage %q, which cannot enter the reconcile step; nothing was compared and nothing was marked",
			recorded.Stage)
	}
	if err := verifyCompletedTarget(ctx, paths, bundleDir, recorded); err != nil {
		return ReconcileReport{}, err
	}

	before, _, err := fingerprintSource(sourceRoot)
	if err != nil {
		return ReconcileReport{}, err
	}
	if strings.TrimSpace(recorded.SourceFingerprintAfter) == "" {
		return ReconcileReport{}, errors.New("the recorded copy-in names no source fingerprint, so the 1.x source cannot be proven to be the one it copied")
	}
	if before != recorded.SourceFingerprintAfter {
		return ReconcileReport{}, newUpgradeError(UpgradeErrorSourceChanged,
			"the 1.x source is not the volume the recorded copy-in copied; nothing was compared and nothing was marked", nil)
	}

	scratch, err := reconcileScratchRoot(targetRoot, sourceRoot, options.ScratchDir)
	if err != nil {
		return ReconcileReport{}, err
	}
	// The copy of the 1.x database is scratch, never evidence: whoever leaves
	// this function, the area is gone and only the sanitized report survives.
	defer func() { _ = os.RemoveAll(scratch) }()
	domains, parityDays, compareErr := compareStores(ctx, sourceRoot, paths.Database, scratch)
	if compareErr != nil {
		return ReconcileReport{ReconcileEvidence: newReconcileEvidence(before, domains, parityDays)}, compareErr
	}

	after, _, err := fingerprintSource(sourceRoot)
	if err != nil {
		return ReconcileReport{}, err
	}
	if after != before {
		return ReconcileReport{}, newUpgradeError(UpgradeErrorSourceChanged,
			"the 1.x source changed while it was being compared; nothing was compared further and nothing was marked", nil)
	}

	evidence := newReconcileEvidence(before, domains, parityDays)
	// A run that established no operation domain compared nothing a result
	// could describe: either the 1.x source holds none of the compared tables, or
	// every table it holds was derived and cleared in the store, so not one day
	// could be compared. Recording it would advance no domain while claiming the
	// step succeeded, so it stops with its own code and leaves the row alone; the
	// report still names the domains and the days that were seen.
	established := establishedDomains(evidence.Domains)
	if len(established) == 0 {
		return ReconcileReport{ReconcileEvidence: evidence}, newUpgradeError(UpgradeErrorReconcileNothingCompared,
			"no compared domain could be established: this run compared no day of any domain, so nothing was marked and the operation did not advance", nil)
	}
	evidence.AcknowledgedDays, evidence.UnacknowledgedDays = decideAcknowledged(
		unionDays(evidence.MismatchDays, parityDays), acknowledged, options.AcknowledgeParityGaps, parityDays)
	if len(evidence.UnacknowledgedDays) > 0 {
		return ReconcileReport{ReconcileEvidence: evidence}, newUpgradeError(UpgradeErrorReconcileMismatch,
			fmt.Sprintf("%s must be acknowledged before they can be marked incomplete; nothing was marked: %s",
				plural(len(evidence.UnacknowledgedDays), "day", "days"), strings.Join(evidence.UnacknowledgedDays, ", ")), nil)
	}
	evidence.IncompleteDays = incompleteDays(evidence.AcknowledgedDays, measuredMismatchDays(domains), derivedLossDays(domains))

	operation, inserted, err := recordReconciliation(ctx, paths.Database, evidence, established)
	if err != nil {
		return ReconcileReport{ReconcileEvidence: evidence}, err
	}
	evidence.NewlyMarkedDays = inserted
	return ReconcileReport{
		ReconcileEvidence: evidence,
		Recorded:          true,
		Stage:             operation.Stage,
		DomainsDone:       operation.DomainsDone,
		UpdatedAtMS:       operation.UpdatedAtMS,
	}, nil
}

// reconcileRoots resolves and fences the three roots. The 1.x volume is a
// source, never a destination, so no root may contain another.
func reconcileRoots(options Options) (string, string, string, error) {
	sourceRoot, err := resolveLegacyRoot(options.LegacyRoot)
	if err != nil {
		return "", "", "", err
	}
	targetRoot, err := resolveTargetRoot(options.TargetRoot)
	if err != nil {
		return "", "", "", err
	}
	bundleDir := DefaultBundleDirectory(targetRoot)
	if strings.TrimSpace(options.BundleDir) != "" {
		if bundleDir, err = resolveBundleDir(options.BundleDir); err != nil {
			return "", "", "", err
		}
	}
	canonicalSource, err := canonicalPath(sourceRoot)
	if err != nil {
		return "", "", "", err
	}
	canonicalTarget, err := canonicalPath(targetRoot)
	if err != nil {
		return "", "", "", err
	}
	canonicalBundle, err := canonicalPath(bundleDir)
	if err != nil {
		return "", "", "", err
	}
	if err := rejectOverlap(canonicalSource, canonicalTarget, canonicalBundle); err != nil {
		return "", "", "", err
	}
	return sourceRoot, targetRoot, bundleDir, nil
}

// reconcileScratchRoot resolves this step's scratch area. The caller names a
// directory it owns; the tool-owned subdirectory below it is the only path this
// step ever removes, so a mistaken configuration can never delete a caller's
// tree. The named root must not be the 1.x source root, sit inside it or
// contain it: anything else would let the working copy of the source land where
// the source itself lives.
func reconcileScratchRoot(targetRoot, sourceRoot, configured string) (string, error) {
	callerRoot := strings.TrimSpace(configured)
	if callerRoot == "" {
		callerRoot = targetRoot
	}
	absolute, err := filepath.Abs(callerRoot)
	if err != nil {
		return "", fmt.Errorf("resolve the reconciliation scratch area: %w", err)
	}
	canonicalRoot, err := canonicalPath(absolute)
	if err != nil {
		return "", err
	}
	canonicalSource, err := canonicalPath(sourceRoot)
	if err != nil {
		return "", err
	}
	if within(canonicalSource, canonicalRoot) || within(canonicalRoot, canonicalSource) {
		return "", errors.New("the reconciliation scratch area must not be the 1.x source root, sit inside it or contain it")
	}
	return filepath.Join(absolute, reconcileScratchDirectory), nil
}

// acknowledgedDays validates the operator's acknowledgement list. Anything that
// is not a calendar day is refused, so a typo cannot acknowledge another day.
func acknowledgedDays(values []string) (map[string]bool, error) {
	days := make(map[string]bool, len(values))
	for _, value := range values {
		day, err := coverageincomplete.ParseDay(value)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUsage, err)
		}
		days[day] = true
	}
	return days, nil
}

// ErrUsage reports a caller mistake: the request itself is malformed, so
// nothing was read and nothing was written.
var ErrUsage = errors.New("invalid upgrade request")

// compareStores reads both stores and returns the per-domain comparison plus
// the days the 1.x source can never complete again.
func compareStores(ctx context.Context, sourceRoot, targetDatabase, scratchRoot string) ([]ReconcileDomainReport, []string, error) {
	snapshot, err := snapshotSourceDatabase(ctx, sourceRoot, scratchRoot)
	if err != nil {
		return nil, nil, err
	}
	sourceDB, sourceTables, err := openScratchSnapshot(ctx, snapshot)
	if err != nil {
		return nil, nil, fmt.Errorf("read the copied 1.x database: %w", err)
	}
	defer func() { _ = sourceDB.Close() }()
	gaps, err := detectParityGaps(ctx, sourceDB, sourceTables)
	if err != nil {
		return nil, nil, fmt.Errorf("detect the parity gaps of the 1.x source: %w", err)
	}
	source, err := observeSide(ctx, sourceDB, sourceTables)
	if err != nil {
		return nil, nil, fmt.Errorf("read the 1.x source: %w", err)
	}

	targetDB, targetTables, err := openTargetSnapshot(ctx, targetDatabase)
	if err != nil {
		return nil, nil, fmt.Errorf("read the copied v2 database: %w", err)
	}
	defer func() { _ = targetDB.Close() }()
	target, err := observeSide(ctx, targetDB, targetTables)
	if err != nil {
		return nil, nil, fmt.Errorf("read the v2 store: %w", err)
	}
	domains, err := compareObservations(source, target)
	return domains, gaps.AffectedDates, err
}

// snapshotSourceDatabase copies the stopped 1.x database and its sidecars into
// the scratch area. The copy is what gets read: opening the original, even
// read-only, makes SQLite create a WAL index beside it and change the source.
func snapshotSourceDatabase(ctx context.Context, sourceRoot, scratchRoot string) (string, error) {
	if err := os.RemoveAll(scratchRoot); err != nil {
		return "", fmt.Errorf("clear the reconciliation scratch area: %w", err)
	}
	if err := os.MkdirAll(scratchRoot, 0o700); err != nil {
		return "", fmt.Errorf("create the reconciliation scratch area: %w", err)
	}
	for _, name := range []string{
		databaseFileName,
		databaseFileName + "-wal",
		databaseFileName + "-shm",
		databaseFileName + "-journal",
	} {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		source := filepath.Join(sourceRoot, name)
		info, statErr := os.Lstat(source)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect the 1.x source member %s: %w", name, statErr)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("the 1.x source member %s is not a regular file", name)
		}
		if err := copyRegularFile(source, filepath.Join(scratchRoot, name)); err != nil {
			return "", fmt.Errorf("copy the 1.x source member %s into the scratch area: %w", name, err)
		}
	}
	return filepath.Join(scratchRoot, databaseFileName), nil
}

func copyRegularFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// openScratchSnapshot reads this step's own copy of the 1.x database. The copy
// can carry a WAL, so it is opened read-write with query_only rather than
// immutable: SQLite then joins the WAL, and any index it writes lands in the
// scratch area this step removes.
func openScratchSnapshot(ctx context.Context, path string) (*sql.DB, sourceSchema, error) {
	return openSourceWith(ctx, path, scratchDataSourceName(path))
}

func scratchDataSourceName(path string) string {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	query := dsn.Query()
	query.Set("mode", "rw")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

// sideObservation is one store read as a single snapshot. Every domain keeps the
// granularity its read path exposes: the retained log and the pricing rollups
// per UTC hour and per row identity, the account history per day and per
// account identity, the deleted coverage per UTC day. A day total alone cannot
// see two rows of the same day trade places.
type sideObservation struct {
	tables   sourceSchema
	retained map[retainedCellKey]retainedCell
	pricing  map[pricingCellKey]pricingCell
	coverage map[string]coverageCell
	history  map[historyCellKey]historyCell
}

// observeSide reads every comparable domain inside one read-only transaction,
// so a domain cannot disagree with itself across queries.
func observeSide(ctx context.Context, db *sql.DB, schema sourceSchema) (sideObservation, error) {
	observation := sideObservation{tables: schema}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return observation, err
	}
	defer func() { _ = tx.Rollback() }()
	if schema.tables["usage_events"] {
		if observation.retained, err = readRetainedCells(ctx, tx); err != nil {
			return observation, err
		}
	}
	if schema.tables["usage_pricing_hourly_rollups_v1"] {
		prices := map[string]model.ModelPrice{}
		if schema.tables["model_prices"] {
			if prices, err = modelprice.New(db).LoadAllTx(ctx, tx); err != nil {
				return observation, fmt.Errorf("read the model prices: %w", err)
			}
		}
		if observation.pricing, err = readPricingCells(ctx, tx, prices); err != nil {
			return observation, err
		}
	}
	if schema.tables["usage_archive_deleted_coverage_daily"] {
		if observation.coverage, err = readCoverageCells(ctx, tx); err != nil {
			return observation, err
		}
	}
	if schema.tables["usage_monitoring_account_daily_rollups_v1"] {
		if observation.history, err = readHistoryCells(ctx, tx); err != nil {
			return observation, err
		}
	}
	return observation, tx.Commit()
}

// retainedCellKey is one (UTC hour, model, API key) bucket of the retained event
// log. The identity includes the hour and the model because a key that moved to
// another hour or another model of the same day changes the bucket it belongs
// to, and a day total would not show it.
type retainedCellKey struct {
	hourMS int64
	model  string
	apiKey string
}

type retainedCell struct {
	events int64
	tokens int64
	failed int64
}

// pricingCellKey is one stored pricing rollup row: its hour bucket and the whole
// identity the table keys on. The failed flag is part of it, so two rows that
// differ only by outcome cannot trade their numbers unnoticed.
type pricingCellKey struct {
	hourMS    int64
	model     string
	billing   string
	pricing   string
	tier      string
	threshold int64
	failed    int64
}

type pricingCell struct {
	calls             int64
	tokens            int64
	input             int64
	output            int64
	cached            int64
	cacheRead         int64
	cacheCreation     int64
	longInput         int64
	longOutput        int64
	longCached        int64
	longCacheRead     int64
	longCacheCreation int64
	costMicros        int64
}

// historyCellKey is one account history rollup: the hour bucket and the identity
// columns the history read path exposes. The credential identity is part of it
// because two accounts can run the same model under the same key with different
// labels, files or executors, and a key that ignored them would let one row's
// totals answer for another's.
type historyCellKey struct {
	hourMS   int64
	account  string
	label    string
	file     string
	provider string
	apiKey   string
	executor string
	model    string
}

type historyCell struct {
	calls  int64
	tokens int64
}

// coverageCell is one day of the deleted-coverage table, including the edges the
// read path reports beside the count.
type coverageCell struct {
	deleted int64
	minMS   int64
	maxMS   int64
}

// readRetainedCells summarises the retained event log per UTC hour, model and
// API key: how many events, how many tokens and how many failures.
//
// The grouping key must be the selected key, not the column: a NULL and an
// empty api_key_hash are one reported key, "no key", so grouping on the raw
// column would return them as two groups that both land on the same cell and
// silently overwrite each other.
func readRetainedCells(ctx context.Context, tx *sql.Tx) (map[retainedCellKey]retainedCell, error) {
	rows, err := tx.QueryContext(ctx, `select
		timestamp_ms / 3600000 * 3600000 as utc_hour_ms,
		model,
		coalesce(api_key_hash, ''),
		count(*),
		coalesce(sum(total_tokens), 0),
		coalesce(sum(failed), 0)
		from usage_events
		group by timestamp_ms / 3600000, model, coalesce(api_key_hash, '')`)
	if err != nil {
		return nil, fmt.Errorf("read the retained events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	cells := map[retainedCellKey]retainedCell{}
	for rows.Next() {
		var key retainedCellKey
		var cell retainedCell
		if err := rows.Scan(&key.hourMS, &key.model, &key.apiKey, &cell.events, &cell.tokens, &cell.failed); err != nil {
			return nil, err
		}
		cells[key] = cell
	}
	return cells, rows.Err()
}

// readPricingCells reads every stored pricing rollup row with its token classes
// and its cost. Cost is not stored: it is recomputed from each side's own price
// table, which is exactly what makes a price change visible here.
func readPricingCells(ctx context.Context, tx *sql.Tx, prices map[string]model.ModelPrice) (map[pricingCellKey]pricingCell, error) {
	rows, err := tx.QueryContext(ctx, `select
		bucket_ms,
		model, billing_model, pricing_model, service_tier, context_threshold_tokens, failed,
		sum(calls), sum(total_tokens),
		sum(input_tokens), sum(output_tokens), sum(cached_tokens),
		sum(cache_read_tokens), sum(cache_creation_tokens),
		sum(long_input_tokens), sum(long_output_tokens), sum(long_cached_tokens),
		sum(long_cache_read_tokens), sum(long_cache_creation_tokens)
		from usage_pricing_hourly_rollups_v1
		group by bucket_ms, model, billing_model, pricing_model, service_tier, context_threshold_tokens, failed`)
	if err != nil {
		return nil, fmt.Errorf("read the pricing rollups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	cells := map[pricingCellKey]pricingCell{}
	for rows.Next() {
		var key pricingCellKey
		var cell pricingCell
		if err := rows.Scan(&key.hourMS, &key.model, &key.billing, &key.pricing, &key.tier, &key.threshold, &key.failed,
			&cell.calls, &cell.tokens,
			&cell.input, &cell.output, &cell.cached,
			&cell.cacheRead, &cell.cacheCreation,
			&cell.longInput, &cell.longOutput, &cell.longCached,
			&cell.longCacheRead, &cell.longCacheCreation); err != nil {
			return nil, err
		}
		cost := pricing.CostForModelCandidatesWithServiceTier([]string{key.billing, key.model}, key.tier, pricing.ModelTokens{
			PricingModel:            key.pricing,
			ContextThresholdTokens:  key.threshold,
			InputTokens:             cell.input,
			OutputTokens:            cell.output,
			CachedTokens:            cell.cached,
			CacheReadTokens:         cell.cacheRead,
			CacheCreationTokens:     cell.cacheCreation,
			LongInputTokens:         cell.longInput,
			LongOutputTokens:        cell.longOutput,
			LongCachedTokens:        cell.longCached,
			LongCacheReadTokens:     cell.longCacheRead,
			LongCacheCreationTokens: cell.longCacheCreation,
		}, prices)
		cell.costMicros = int64(math.Round(cost * costMicrosPerUnit))
		cells[key] = cell
	}
	return cells, rows.Err()
}

// readCoverageCells reads the per-day deleted event counts and the edges the
// archive kept beside them.
func readCoverageCells(ctx context.Context, tx *sql.Tx) (map[string]coverageCell, error) {
	rows, err := tx.QueryContext(ctx,
		`select utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms
		from usage_archive_deleted_coverage_daily`)
	if err != nil {
		return nil, fmt.Errorf("read the deleted coverage table: %w", err)
	}
	defer func() { _ = rows.Close() }()
	cells := map[string]coverageCell{}
	for rows.Next() {
		var day int64
		var cell coverageCell
		if err := rows.Scan(&day, &cell.deleted, &cell.minMS, &cell.maxMS); err != nil {
			return nil, err
		}
		cells[utcDayLabel(day)] = cell
	}
	return cells, rows.Err()
}

// readHistoryCells reads the account history rollups per day and per identity
// column the read path exposes. The grouping key repeats the selected key
// exactly: a NULL and an empty value are one reported value, so grouping on the
// raw column would return two groups that overwrite each other in the cell map.
// The credential columns are coalesced because a 1.x-era table keeps its
// original nullable shape, so a value the current schema requires can still be
// absent in the copied source.
func readHistoryCells(ctx context.Context, tx *sql.Tx) (map[historyCellKey]historyCell, error) {
	rows, err := tx.QueryContext(ctx, `select
		bucket_ms,
		account_snapshot, coalesce(auth_label_snapshot, ''), coalesce(auth_file_snapshot, ''),
		provider, coalesce(api_key_hash, ''), coalesce(executor_type, ''), model,
		sum(calls),
		sum(total_tokens)
		from usage_monitoring_account_daily_rollups_v1
		group by bucket_ms, account_snapshot, coalesce(auth_label_snapshot, ''),
			coalesce(auth_file_snapshot, ''), provider, coalesce(api_key_hash, ''),
			coalesce(executor_type, ''), model`)
	if err != nil {
		return nil, fmt.Errorf("read the account history rollups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	cells := map[historyCellKey]historyCell{}
	for rows.Next() {
		var key historyCellKey
		var cell historyCell
		if err := rows.Scan(&key.hourMS, &key.account, &key.label, &key.file, &key.provider,
			&key.apiKey, &key.executor, &key.model,
			&cell.calls, &cell.tokens); err != nil {
			return nil, err
		}
		cells[key] = cell
	}
	return cells, rows.Err()
}

// compareObservations compares the four domains in report order. A domain the
// source holds days of but the store cannot show is not a disagreement to
// acknowledge: it is unreconcilable, so the comparison stops before anything can
// be marked. The one exception is a derived rollup that was cleared rather than
// dropped: recoverableDerivedLoss recognises it and leaves the decision to the
// operator's acknowledgement.
func compareObservations(source, target sideObservation) ([]ReconcileDomainReport, error) {
	domains := []ReconcileDomainReport{
		compareRetainedDomain(source, target),
		comparePricingDomain(source, target),
		compareCoverageDomain(source, target),
		compareHistoryDomain(source, target),
	}
	for index := range domains {
		domains[index].Recoverable = recoverableDerivedLoss(domains[index])
	}
	for _, domain := range domains {
		if err := domain.targetIncompleteError(); err != nil {
			return domains, err
		}
	}
	return domains, nil
}

// recoverableDerivedLoss reports whether a domain is a derived rollup whose
// table is present but holds none of the days the source holds: the rollup was
// cleared, its rows can be rebuilt from the retained events, and the domain was
// therefore never compared (see reconcileDerivedDomains for the boundary).
// Nothing here rebuilds anything, so the lost days are still marked; the flag
// only decides that the mark may be acknowledged instead of blocking the step.
func recoverableDerivedLoss(report ReconcileDomainReport) bool {
	return reconcileDerivedDomains[report.Domain] && report.Applicable &&
		report.SourceDays > 0 && report.ComparedDays == 0
}

// targetIncompleteError reports a domain the 1.x source holds days of but the v2
// store cannot show at all. That is lost data, not a measured disagreement, so
// no acknowledgement may waive it. The one exception is a derived domain whose
// table is still there: a cleared rollup is rebuildable from the retained
// events, so its lost days are marked under their own reason and may be
// acknowledged. A missing table has no such way back and stays unwaivable.
func (r ReconcileDomainReport) targetIncompleteError() error {
	if r.SourceDays == 0 || r.Recoverable {
		return nil
	}
	if !r.Applicable {
		return newUpgradeError(UpgradeErrorReconcileTargetIncomplete, fmt.Sprintf(
			"the v2 store holds no %s table, so the %q domain cannot show the %s the 1.x source holds; nothing was compared and nothing was marked",
			r.Table, r.Domain, plural(r.SourceDays, "day", "days")), nil)
	}
	if r.ComparedDays == 0 {
		return newUpgradeError(UpgradeErrorReconcileTargetIncomplete, fmt.Sprintf(
			"the v2 store compared no day of %s, so the %q domain cannot show the %s the 1.x source holds; nothing was compared and nothing was marked",
			r.Table, r.Domain, plural(r.SourceDays, "day", "days")), nil)
	}
	return nil
}

// identityValue is one identity column of a compared row: the column name and
// the value this row holds in it.
type identityValue struct {
	field string
	value string
}

// keyedSpec describes one keyed comparison: the UTC day a cell belongs to, how
// the operator names it, its identity columns and its numeric columns, in the
// order the values are read.
type keyedSpec[K comparable, V any] struct {
	day        func(K) string
	render     func(K) string
	identities func(K) []identityValue
	fields     []string
	values     func(V) []int64
}

var retainedSpec = keyedSpec[retainedCellKey, retainedCell]{
	day: func(key retainedCellKey) string { return utcDayOfHour(key.hourMS) },
	render: func(key retainedCellKey) string {
		return utcHourLabel(key.hourMS) + " model=" + key.model + " key=" + key.apiKey
	},
	identities: func(key retainedCellKey) []identityValue {
		return []identityValue{
			{mismatchFieldHour, utcHourLabel(key.hourMS)},
			{mismatchFieldModel, key.model},
			{mismatchFieldAPIKeyHash, key.apiKey},
		}
	},
	fields: []string{mismatchFieldEvents, mismatchFieldTokens, mismatchFieldFailed},
	values: func(cell retainedCell) []int64 { return []int64{cell.events, cell.tokens, cell.failed} },
}

var pricingSpec = keyedSpec[pricingCellKey, pricingCell]{
	day: func(key pricingCellKey) string { return utcDayOfHour(key.hourMS) },
	render: func(key pricingCellKey) string {
		return utcHourLabel(key.hourMS) + " model=" + key.model + " billing=" + key.billing +
			" pricing=" + key.pricing + " tier=" + key.tier +
			" threshold=" + strconv.FormatInt(key.threshold, 10) +
			" failed=" + strconv.FormatInt(key.failed, 10)
	},
	identities: func(key pricingCellKey) []identityValue {
		return []identityValue{
			{mismatchFieldBucketMS, utcHourLabel(key.hourMS)},
			{mismatchFieldModel, key.model},
			{mismatchFieldBillingModel, key.billing},
			{mismatchFieldPricingModel, key.pricing},
			{mismatchFieldServiceTier, key.tier},
			{mismatchFieldThreshold, strconv.FormatInt(key.threshold, 10)},
			{mismatchFieldFailed, strconv.FormatInt(key.failed, 10)},
		}
	},
	fields: []string{
		mismatchFieldCalls, mismatchFieldTokens,
		mismatchFieldInputTokens, mismatchFieldOutputTokens, mismatchFieldCachedTokens,
		mismatchFieldCacheReadTokens, mismatchFieldCacheCreationTokens,
		mismatchFieldLongInputTokens, mismatchFieldLongOutputTokens, mismatchFieldLongCachedTokens,
		mismatchFieldLongCacheReadTokens, mismatchFieldLongCacheCreationTokens,
		mismatchFieldCost,
	},
	values: func(cell pricingCell) []int64 {
		return []int64{
			cell.calls, cell.tokens,
			cell.input, cell.output, cell.cached,
			cell.cacheRead, cell.cacheCreation,
			cell.longInput, cell.longOutput, cell.longCached,
			cell.longCacheRead, cell.longCacheCreation,
			cell.costMicros,
		}
	},
}

var coverageSpec = keyedSpec[string, coverageCell]{
	day:        func(day string) string { return day },
	render:     func(day string) string { return day },
	identities: func(string) []identityValue { return nil },
	fields:     []string{mismatchFieldDeleted, mismatchFieldMinTimestamp, mismatchFieldMaxTimestamp},
	values: func(cell coverageCell) []int64 {
		return []int64{cell.deleted, cell.minMS, cell.maxMS}
	},
}

var historySpec = keyedSpec[historyCellKey, historyCell]{
	day: func(key historyCellKey) string { return utcDayOfHour(key.hourMS) },
	render: func(key historyCellKey) string {
		return utcHourLabel(key.hourMS) + " account=" + key.account +
			" label=" + key.label + " provider=" + key.provider + " key=" + key.apiKey +
			" file=" + key.file + " executor=" + key.executor + " model=" + key.model
	},
	identities: func(key historyCellKey) []identityValue {
		return []identityValue{
			{mismatchFieldAccountSnapshot, key.account},
			{mismatchFieldAuthLabelSnapshot, key.label},
			{mismatchFieldAuthFileSnapshot, key.file},
			{mismatchFieldProvider, key.provider},
			{mismatchFieldAPIKeyHash, key.apiKey},
			{mismatchFieldExecutorType, key.executor},
			{mismatchFieldModel, key.model},
		}
	},
	fields: []string{mismatchFieldCalls, mismatchFieldTokens},
	values: func(cell historyCell) []int64 { return []int64{cell.calls, cell.tokens} },
}

func compareRetainedDomain(source, target sideObservation) ReconcileDomainReport {
	report := newDomainReport(ReconcileDomainRetained, source, target, "usage_events",
		len(daysOf(source.retained, retainedSpec.day)))
	if report.Applicable {
		compareKeyed(&report, source.retained, target.retained, retainedSpec)
	}
	return report
}

func comparePricingDomain(source, target sideObservation) ReconcileDomainReport {
	report := newDomainReport(ReconcileDomainPricing, source, target, "usage_pricing_hourly_rollups_v1",
		len(daysOf(source.pricing, pricingSpec.day)))
	if report.Applicable {
		compareKeyed(&report, source.pricing, target.pricing, pricingSpec)
	}
	return report
}

func compareCoverageDomain(source, target sideObservation) ReconcileDomainReport {
	report := newDomainReport(ReconcileDomainCoverage, source, target, "usage_archive_deleted_coverage_daily",
		len(source.coverage))
	if report.Applicable {
		compareKeyed(&report, source.coverage, target.coverage, coverageSpec)
	}
	return report
}

func compareHistoryDomain(source, target sideObservation) ReconcileDomainReport {
	report := newDomainReport(ReconcileDomainHistory, source, target, "usage_monitoring_account_daily_rollups_v1",
		len(daysOf(source.history, historySpec.day)))
	if report.Applicable {
		compareKeyed(&report, source.history, target.history, historySpec)
	}
	return report
}

// compareKeyed compares one domain at row granularity: the days only one side
// holds, the per-day distribution of every identity column, and every numeric
// column of a row both sides hold. ComparedDays only counts days both sides
// hold, because a day only one side holds was never compared.
func compareKeyed[K comparable, V any](report *ReconcileDomainReport, source, target map[K]V, spec keyedSpec[K, V]) {
	sourceDays := daysOf(source, spec.day)
	targetDays := daysOf(target, spec.day)
	for _, day := range unionDays(keys(sourceDays), keys(targetDays)) {
		sourceKeys, inSource := sourceDays[day]
		targetKeys, inTarget := targetDays[day]
		if !inSource || !inTarget {
			report.Mismatches = append(report.Mismatches, presenceMismatch(day, inSource))
			continue
		}
		report.Mismatches = append(report.Mismatches, compareIdentities(day, sourceKeys, targetKeys, spec)...)
		report.Mismatches = append(report.Mismatches, compareCells(day, sourceKeys, targetKeys, source, target, spec)...)
	}
	report.SourceDays = len(sourceDays)
	report.ComparedDays = comparedDays(sourceDays, targetDays)
}

// daysOf groups a keyed observation by the UTC day each cell belongs to.
func daysOf[K comparable, V any](cells map[K]V, day func(K) string) map[string][]K {
	days := make(map[string][]K, len(cells))
	for key := range cells {
		label := day(key)
		days[label] = append(days[label], key)
	}
	return days
}

// comparedDays counts the days both sides hold.
func comparedDays[V any](source, target map[string]V) int {
	compared := 0
	for day := range source {
		if _, held := target[day]; held {
			compared++
		}
	}
	return compared
}

// compareIdentities compares each identity column as the per-day distribution of
// its values, so a row that moved to another hour, model or key is reported
// against the column that moved instead of cancelling out inside a day total.
func compareIdentities[K comparable, V any](day string, sourceKeys, targetKeys []K, spec keyedSpec[K, V]) []ReconcileMismatch {
	mismatches := []ReconcileMismatch{}
	if len(sourceKeys) == 0 {
		return mismatches
	}
	width := len(spec.identities(sourceKeys[0]))
	for column := range width {
		sourceCounts := make(map[string]int64, len(sourceKeys))
		for _, key := range sourceKeys {
			sourceCounts[spec.identities(key)[column].value]++
		}
		targetCounts := make(map[string]int64, len(targetKeys))
		for _, key := range targetKeys {
			targetCounts[spec.identities(key)[column].value]++
		}
		field := spec.identities(sourceKeys[0])[column].field
		for _, value := range unionDays(keys(sourceCounts), keys(targetCounts)) {
			sourceCount, targetCount := sourceCounts[value], targetCounts[value]
			if sourceCount == targetCount {
				continue
			}
			mismatches = append(mismatches, ReconcileMismatch{
				Day:       day,
				Field:     field,
				Dimension: value,
				Source:    strconv.FormatInt(sourceCount, 10),
				Target:    strconv.FormatInt(targetCount, 10),
			})
		}
	}
	return mismatches
}

// compareCells compares every numeric column of the rows both sides hold, in a
// fixed order so two runs over the same state report the same list.
func compareCells[K comparable, V any](day string, sourceKeys, targetKeys []K, source, target map[K]V, spec keyedSpec[K, V]) []ReconcileMismatch {
	mismatches := []ReconcileMismatch{}
	for _, key := range unionKeys(sourceKeys, targetKeys, spec.render) {
		dimension := spec.render(key)
		sourceCell, inSource := source[key]
		targetCell, inTarget := target[key]
		if !inSource || !inTarget {
			mismatches = append(mismatches, ReconcileMismatch{
				Day:       day,
				Field:     mismatchFieldRow,
				Dimension: dimension,
				Source:    presenceValue(inSource),
				Target:    presenceValue(inTarget),
			})
			continue
		}
		sourceValues, targetValues := spec.values(sourceCell), spec.values(targetCell)
		for index, field := range spec.fields {
			if sourceValues[index] == targetValues[index] {
				continue
			}
			mismatches = append(mismatches, ReconcileMismatch{
				Day:       day,
				Field:     field,
				Dimension: dimension,
				Source:    strconv.FormatInt(sourceValues[index], 10),
				Target:    strconv.FormatInt(targetValues[index], 10),
			})
		}
	}
	return mismatches
}

// unionKeys merges two key lists into one sorted list, named the way the
// operator reads it.
func unionKeys[K comparable](left, right []K, render func(K) string) []K {
	seen := make(map[K]bool, len(left)+len(right))
	merged := make([]K, 0, len(left)+len(right))
	for _, key := range append(append([]K{}, left...), right...) {
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, key)
	}
	sort.Slice(merged, func(left, right int) bool { return render(merged[left]) < render(merged[right]) })
	return merged
}

// utcHourLabel names one UTC hour in full, so every mismatch names the hour it
// sits in.
func utcHourLabel(hourMS int64) string {
	return time.UnixMilli(hourMS).UTC().Format("2006-01-02T15:00")
}

// utcDayOfHour labels the UTC day an hour bucket belongs to.
func utcDayOfHour(hourMS int64) string {
	return utcDayLabel(floorDiv(hourMS, utcDayMilliseconds))
}

// floorDiv divides towards negative infinity, so hours before the epoch keep
// belonging to the day they were in.
func floorDiv(value, divisor int64) int64 {
	quotient := value / divisor
	if value%divisor != 0 && (value < 0) != (divisor < 0) {
		quotient--
	}
	return quotient
}

// newDomainReport decides whether a domain can be compared at all. A side that
// does not hold the table says so instead of passing silently; sourceDays is
// what the 1.x source holds, which is what a missing store table is measured
// against.
func newDomainReport(domain string, source, target sideObservation, table string, sourceDays int) ReconcileDomainReport {
	report := ReconcileDomainReport{Domain: domain, Table: table, SourceDays: sourceDays, Mismatches: []ReconcileMismatch{}}
	sourceHolds, targetHolds := source.tables.tables[table], target.tables.tables[table]
	if sourceHolds && targetHolds {
		report.Applicable = true
		return report
	}
	switch {
	case !sourceHolds && !targetHolds:
		report.Reason = "neither side holds " + table
	case !sourceHolds:
		report.Reason = "the 1.x source holds no " + table
	default:
		report.Reason = "the v2 store holds no " + table
	}
	return report
}

func presenceMismatch(day string, inSource bool) ReconcileMismatch {
	return ReconcileMismatch{
		Day:    day,
		Field:  mismatchFieldPresence,
		Source: presenceValue(inSource),
		Target: presenceValue(!inSource),
	}
}

func presenceValue(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}

// keys returns the sorted keys of any map keyed by UTC day.
func keys[T any](values map[string]T) []string {
	days := make([]string, 0, len(values))
	for day := range values {
		days = append(days, day)
	}
	sort.Strings(days)
	return days
}

func unionDays(left, right []string) []string {
	seen := make(map[string]bool, len(left)+len(right))
	days := make([]string, 0, len(left)+len(right))
	for _, day := range append(append([]string{}, left...), right...) {
		if seen[day] {
			continue
		}
		seen[day] = true
		days = append(days, day)
	}
	sort.Strings(days)
	return days
}

// mismatchDays names every day with at least one disagreement, sorted.
func mismatchDays(domains []ReconcileDomainReport) []string {
	seen := map[string]bool{}
	days := []string{}
	for _, domain := range domains {
		for _, mismatch := range domain.Mismatches {
			if seen[mismatch.Day] {
				continue
			}
			seen[mismatch.Day] = true
			days = append(days, mismatch.Day)
		}
	}
	sort.Strings(days)
	return days
}

// newReconcileEvidence assembles the durable half of a run from what was
// compared, with every decision still open: the acknowledgement lists stay
// empty until decideAcknowledged fills them, and a run that returns before that
// point reports empty lists rather than a decision it never took.
func newReconcileEvidence(sourceFingerprint string, domains []ReconcileDomainReport, parityDays []string) ReconcileEvidence {
	return ReconcileEvidence{
		SourceFingerprint:  sourceFingerprint,
		Domains:            domains,
		ParityGapDays:      parityDays,
		MismatchDays:       mismatchDays(domains),
		AcknowledgedDays:   []string{},
		UnacknowledgedDays: []string{},
		IncompleteDays:     []ReconcileMarkedDay{},
	}
}

// decideAcknowledged splits the days that need a decision into the ones the
// operator accepted and the ones still blocking. An acknowledgement of a day
// with nothing wrong is not an error and marks nothing: the returned accepted
// set only ever names days that need it.
func decideAcknowledged(candidates []string, acknowledged map[string]bool, acknowledgeParityGaps bool, parityDays []string) ([]string, []string) {
	parity := make(map[string]bool, len(parityDays))
	for _, day := range parityDays {
		parity[day] = true
	}
	accepted := []string{}
	blocked := []string{}
	for _, day := range candidates {
		if acknowledged[day] || (acknowledgeParityGaps && parity[day]) {
			accepted = append(accepted, day)
			continue
		}
		blocked = append(blocked, day)
	}
	return accepted, blocked
}

// measuredMismatchDays names every day a domain reported a disagreement on,
// excluding the derived rollups this run could not compare. A day lost with a
// cleared rollup is marked as such: it is not a measured disagreement between
// two readings, and naming it one would hide that a rebuild can still close it.
func measuredMismatchDays(domains []ReconcileDomainReport) []string {
	measured := make([]ReconcileDomainReport, 0, len(domains))
	for _, domain := range domains {
		if domain.Recoverable {
			continue
		}
		measured = append(measured, domain)
	}
	return mismatchDays(measured)
}

// derivedLossDays names every day a cleared derived rollup lost: a day the 1.x
// source holds and the store's rollup does not.
func derivedLossDays(domains []ReconcileDomainReport) []string {
	days := []string{}
	for _, domain := range domains {
		if !domain.Recoverable {
			continue
		}
		for _, mismatch := range domain.Mismatches {
			if mismatch.Field == mismatchFieldPresence && mismatch.Source == presenceValue(true) {
				days = append(days, mismatch.Day)
			}
		}
	}
	return unionDays(days, nil)
}

// incompleteDays maps each accepted day to the reason it is stored under. A day
// whose measures disagreed keeps the stronger reason even when the parity
// detection also flagged it: a value disagreement is not explained by the
// projection loss alone. A day only a cleared derived rollup lost is named after
// that loss, because a rebuild can still close it; only a day the 1.x source
// itself lost keeps the projection reason.
func incompleteDays(accepted, measured, derived []string) []ReconcileMarkedDay {
	disagreed := make(map[string]bool, len(measured))
	for _, day := range measured {
		disagreed[day] = true
	}
	lost := make(map[string]bool, len(derived))
	for _, day := range derived {
		lost[day] = true
	}
	days := make([]ReconcileMarkedDay, 0, len(accepted))
	for _, day := range accepted {
		reason := coverageincomplete.ReasonProjectionLostBeforeV1146
		switch {
		case disagreed[day]:
			reason = coverageincomplete.ReasonReconcileMismatch
		case lost[day]:
			reason = coverageincomplete.ReasonDerivedRollupLost
		}
		days = append(days, ReconcileMarkedDay{Day: day, Reason: reason})
	}
	return days
}

// reconcileRecordingCut is a test seam. A test cuts the recording at a named
// instant to prove what an interruption there leaves behind; production leaves
// it nil and never runs a cut.
var reconcileRecordingCut func(point string) error

// The instants the recording can be cut at: the marks are written, the evidence
// is written, and the stage has not moved yet.
const (
	reconcileRecordingAfterMarks    = "after-marks"
	reconcileRecordingAfterEvidence = "after-evidence"
)

func cutReconcileRecording(point string) error {
	if reconcileRecordingCut == nil {
		return nil
	}
	return reconcileRecordingCut(point)
}

// recordReconciliation writes the marks, the evidence that explains them and
// the stage that authorizes them in one transaction, so a reader never sees one
// without the others. An interruption leaves the store exactly as it was and
// the error says so; because every write is idempotent (a mark keeps its first
// reason, the evidence overwrites itself, the stage is forward-only), running
// the step again writes the whole result, which is also how a store torn by an
// earlier release that wrote the three steps separately repairs itself.
func recordReconciliation(ctx context.Context, databasePath string, evidence ReconcileEvidence, established []string) (model.UpgradeOperation, int, error) {
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		return model.UpgradeOperation{}, 0, fmt.Errorf("open the v2 database to record the reconciliation: %w", err)
	}
	defer func() { _ = db.Close() }()
	payload, err := json.Marshal(evidence)
	if err != nil {
		return model.UpgradeOperation{}, 0, err
	}
	marks := make([]coverageincomplete.Day, 0, len(evidence.IncompleteDays))
	for _, day := range evidence.IncompleteDays {
		marks = append(marks, coverageincomplete.Day{Day: day.Day, Reason: day.Reason})
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return model.UpgradeOperation{}, 0, fmt.Errorf("begin the reconciliation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	inserted, err := coverageincomplete.MarkTx(ctx, tx, marks, time.Now().UnixMilli())
	if err != nil {
		return model.UpgradeOperation{}, 0, recordInterrupted(err)
	}
	if err := cutReconcileRecording(reconcileRecordingAfterMarks); err != nil {
		return model.UpgradeOperation{}, 0, recordInterrupted(err)
	}
	if err := upgradeoperation.RecordReconciliationTx(ctx, tx, payload); err != nil {
		return model.UpgradeOperation{}, 0, recordInterrupted(err)
	}
	if err := cutReconcileRecording(reconcileRecordingAfterEvidence); err != nil {
		return model.UpgradeOperation{}, 0, recordInterrupted(err)
	}
	operation, err := advanceReconciledDomains(ctx, tx, established)
	if err != nil {
		return model.UpgradeOperation{}, 0, recordInterrupted(err)
	}
	if err := tx.Commit(); err != nil {
		return model.UpgradeOperation{}, 0, recordInterrupted(err)
	}
	return operation, inserted, nil
}

// recordInterrupted says what an interrupted recording left behind. The marks,
// the evidence and the stage travel in one transaction, so the answer is always
// the same: nothing.
func recordInterrupted(err error) error {
	return fmt.Errorf("record the reconciliation: %w; the marks, the evidence and the stage commit together, so the store is unchanged and a rerun records all of them", err)
}

// establishedDomains names the operation domains the compared domains actually
// established, in operation order. A domain that was not applicable, or that
// the store could not show, never appears: recording it would claim work this
// step did not do.
func establishedDomains(domains []ReconcileDomainReport) []string {
	established := make([]string, 0, len(domains))
	for _, domain := range domains {
		if !domain.Applicable || domain.SourceDays > 0 && domain.ComparedDays == 0 {
			continue
		}
		for _, candidate := range reconciledOperationDomains[domain.Domain] {
			if !slices.Contains(established, candidate) {
				established = append(established, candidate)
			}
		}
	}
	return established
}

// advanceReconciledDomains records the domains this step established inside the
// caller's transaction, so the stage can only commit beside the evidence that
// justifies it. The stage is forward-only: a store the operation already carried
// past "reconcile" was refused before the comparison, so this never has to undo
// one.
func advanceReconciledDomains(ctx context.Context, tx upgradeoperation.Tx, domains []string) (model.UpgradeOperation, error) {
	var operation model.UpgradeOperation
	for _, domain := range domains {
		advanced, err := upgradeoperation.AdvanceTx(ctx, tx, domain, model.UpgradeStageReconcile)
		if err != nil {
			return model.UpgradeOperation{}, err
		}
		operation = advanced
	}
	return operation, nil
}

func plural(count int, singular, pluralForm string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, pluralForm)
}
