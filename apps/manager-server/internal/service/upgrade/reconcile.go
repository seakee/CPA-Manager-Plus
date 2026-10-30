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
// advance. Only the days the operator acknowledged are marked, and a marked day
// stays marked for the life of the store.

// Reconcile domain names, in report order. They are the domains the copy claims
// to preserve, not the operation's domain vocabulary.
const (
	ReconcileDomainRetained = "retained"
	ReconcileDomainPricing  = "pricing"
	ReconcileDomainCoverage = "coverage"
	ReconcileDomainHistory  = "history"
)

// Compared measure names. They are stable codes; the operator text is the
// surrounding report.
const (
	mismatchFieldPresence  = "presence"
	mismatchFieldEvents    = "count"
	mismatchFieldTokens    = "total_tokens"
	mismatchFieldFailed    = "failed"
	mismatchFieldKeyHashes = "api_key_hashes"
	mismatchFieldRequests  = "requests"
	mismatchFieldCost      = "cost_micros"
	mismatchFieldDeleted   = "deleted_event_count"
	mismatchFieldCalls     = "calls"
)

const (
	// reconcileScratchDirectory is this step's reserved scratch area inside the
	// v2 data root. It is removed before Reconcile returns.
	reconcileScratchDirectory = ".upgrade-reconcile"
	// utcDayMilliseconds is the width of the day buckets every compared table
	// keys on.
	utcDayMilliseconds = int64(24 * 60 * 60 * 1000)
	// costMicrosPerUnit converts a dollar cost into micro-units. The compared
	// cost tolerance is relative, because the two sides run the same floating
	// point arithmetic over the same token sums and prices.
	costMicrosPerUnit = 1_000_000.0
	// costTolerance is the relative difference two cost totals may carry and
	// still count as equal.
	costTolerance = 1e-6
)

// reconcileDomains is the operation vocabulary the reconciliation establishes.
// The four compared domains map onto it: the retained events, their pricing
// rollups and the deleted-coverage table. The account history rollups are
// derived from the same retained events, so they are covered by the retained
// projection domain as well.
var reconciledDomains = []string{
	model.UpgradeDomainRetainedProjection,
	model.UpgradeDomainPricing,
	model.UpgradeDomainCoverage,
}

// ReconcileMismatch is one compared measure that disagreed. A day present on
// one side only is reported as Field "presence" with "present"/"absent"
// values, never as a skip. Dimension names the row inside the day when the
// compared measure is per account.
type ReconcileMismatch struct {
	Day       string `json:"day"`
	Field     string `json:"field"`
	Dimension string `json:"dimension,omitempty"`
	Source    string `json:"source"`
	Target    string `json:"target"`
}

// ReconcileDomainReport is one domain's comparison. Applicable is false when a
// side does not hold the table at all, and Reason says which side that is.
type ReconcileDomainReport struct {
	Domain       string              `json:"domain"`
	Applicable   bool                `json:"applicable"`
	Reason       string              `json:"reason,omitempty"`
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
// operation to "reconcile". A disagreement, or a day the source can never
// complete again, blocks with ErrReconcileMismatch and leaves the store
// untouched; the returned report still names every day that needs an explicit
// acknowledgement.
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
	domains, parityDays, err := compareStores(ctx, sourceRoot, paths.Database, scratch)
	if err != nil {
		return ReconcileReport{}, err
	}

	after, _, err := fingerprintSource(sourceRoot)
	if err != nil {
		return ReconcileReport{}, err
	}
	if after != before {
		return ReconcileReport{}, newUpgradeError(UpgradeErrorSourceChanged,
			"the 1.x source changed while it was being compared; nothing was compared further and nothing was marked", nil)
	}

	mismatchDays := mismatchDays(domains)
	evidence := ReconcileEvidence{
		SourceFingerprint: before,
		Domains:           domains,
		ParityGapDays:     parityDays,
		MismatchDays:      mismatchDays,
		IncompleteDays:    []ReconcileMarkedDay{},
	}
	evidence.AcknowledgedDays, evidence.UnacknowledgedDays = decideAcknowledged(
		unionDays(mismatchDays, parityDays), acknowledged, options.AcknowledgeParityGaps, parityDays)
	if len(evidence.UnacknowledgedDays) > 0 {
		return ReconcileReport{ReconcileEvidence: evidence}, newUpgradeError(UpgradeErrorReconcileMismatch,
			fmt.Sprintf("%s must be acknowledged before they can be marked incomplete; nothing was marked: %s",
				plural(len(evidence.UnacknowledgedDays), "day", "days"), strings.Join(evidence.UnacknowledgedDays, ", ")), nil)
	}
	evidence.IncompleteDays = incompleteDays(evidence.AcknowledgedDays, mismatchDays)

	operation, inserted, err := recordReconciliation(ctx, paths.Database, recorded, evidence)
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

// reconcileScratchRoot resolves this step's scratch area. It must not sit
// inside the 1.x source root: the point of the copy is that nothing this step
// does can reach the source.
func reconcileScratchRoot(targetRoot, sourceRoot, configured string) (string, error) {
	scratchRoot := strings.TrimSpace(configured)
	if scratchRoot == "" {
		scratchRoot = filepath.Join(targetRoot, reconcileScratchDirectory)
	}
	absolute, err := filepath.Abs(scratchRoot)
	if err != nil {
		return "", fmt.Errorf("resolve the reconciliation scratch area: %w", err)
	}
	canonicalScratch, err := canonicalPath(absolute)
	if err != nil {
		return "", err
	}
	canonicalSource, err := canonicalPath(sourceRoot)
	if err != nil {
		return "", err
	}
	if within(canonicalSource, canonicalScratch) {
		return "", errors.New("the reconciliation scratch area must not sit inside or above the 1.x source root")
	}
	return absolute, nil
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
	return compareObservations(source, target), gaps.AffectedDates, nil
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

// sideObservation is one store read as a single snapshot.
type sideObservation struct {
	tables   sourceSchema
	retained map[string]retainedDay
	pricing  map[string]pricingDay
	coverage map[string]int64
	history  map[string]map[string]historyDay
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
		if observation.retained, err = readRetainedDays(ctx, tx); err != nil {
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
		if observation.pricing, err = readPricingDays(ctx, tx, prices); err != nil {
			return observation, err
		}
	}
	if schema.tables["usage_archive_deleted_coverage_daily"] {
		if observation.coverage, err = readCoverageDays(ctx, tx); err != nil {
			return observation, err
		}
	}
	if schema.tables["usage_monitoring_account_daily_rollups_v1"] {
		if observation.history, err = readHistoryDays(ctx, tx); err != nil {
			return observation, err
		}
	}
	return observation, tx.Commit()
}

type retainedDay struct {
	events    int64
	tokens    int64
	failed    int64
	keyHashes int64
}

type pricingDay struct {
	requests   int64
	tokens     int64
	costMicros int64
}

type historyDay struct {
	calls  int64
	tokens int64
}

// readRetainedDays summarises the retained event log per UTC day: how many
// events, how many tokens, how many failures and how many distinct keys.
func readRetainedDays(ctx context.Context, tx *sql.Tx) (map[string]retainedDay, error) {
	rows, err := tx.QueryContext(ctx, `select
		timestamp_ms / 86400000 as utc_day,
		count(*),
		coalesce(sum(total_tokens), 0),
		coalesce(sum(failed), 0),
		count(distinct api_key_hash)
		from usage_events
		group by utc_day`)
	if err != nil {
		return nil, fmt.Errorf("read the retained events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	days := map[string]retainedDay{}
	for rows.Next() {
		var day int64
		var summary retainedDay
		if err := rows.Scan(&day, &summary.events, &summary.tokens, &summary.failed, &summary.keyHashes); err != nil {
			return nil, err
		}
		days[utcDayLabel(day)] = summary
	}
	return days, rows.Err()
}

// readPricingDays summarises the pricing rollups per UTC day: requests, tokens
// and cost. Cost is not stored: it is recomputed from each side's own price
// table, which is exactly what makes a price change visible here.
func readPricingDays(ctx context.Context, tx *sql.Tx, prices map[string]model.ModelPrice) (map[string]pricingDay, error) {
	rows, err := tx.QueryContext(ctx, `select
		bucket_ms / 86400000 as utc_day,
		model, billing_model, pricing_model, service_tier, context_threshold_tokens,
		sum(calls), sum(total_tokens),
		sum(input_tokens), sum(output_tokens), sum(cached_tokens),
		sum(cache_read_tokens), sum(cache_creation_tokens),
		sum(long_input_tokens), sum(long_output_tokens), sum(long_cached_tokens),
		sum(long_cache_read_tokens), sum(long_cache_creation_tokens)
		from usage_pricing_hourly_rollups_v1
		group by utc_day, model, billing_model, pricing_model, service_tier, context_threshold_tokens
		order by utc_day, model, billing_model, pricing_model, service_tier, context_threshold_tokens`)
	if err != nil {
		return nil, fmt.Errorf("read the pricing rollups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	days := map[string]pricingDay{}
	for rows.Next() {
		var (
			day                                                       int64
			modelName, billingModel, pricingModel, serviceTier        string
			threshold, calls, tokens                                  int64
			input, output, cached, cacheRead, cacheCreation           int64
			longInput, longOutput, longCached, longCacheRead, longCre int64
		)
		if err := rows.Scan(&day, &modelName, &billingModel, &pricingModel, &serviceTier, &threshold,
			&calls, &tokens, &input, &output, &cached, &cacheRead, &cacheCreation,
			&longInput, &longOutput, &longCached, &longCacheRead, &longCre); err != nil {
			return nil, err
		}
		cost := pricing.CostForModelCandidatesWithServiceTier([]string{billingModel, modelName}, serviceTier, pricing.ModelTokens{
			PricingModel:            pricingModel,
			ContextThresholdTokens:  threshold,
			InputTokens:             input,
			OutputTokens:            output,
			CachedTokens:            cached,
			CacheReadTokens:         cacheRead,
			CacheCreationTokens:     cacheCreation,
			LongInputTokens:         longInput,
			LongOutputTokens:        longOutput,
			LongCachedTokens:        longCached,
			LongCacheReadTokens:     longCacheRead,
			LongCacheCreationTokens: longCre,
		}, prices)
		label := utcDayLabel(day)
		summary := days[label]
		summary.requests += calls
		summary.tokens += tokens
		summary.costMicros += int64(math.Round(cost * costMicrosPerUnit))
		days[label] = summary
	}
	return days, rows.Err()
}

// readCoverageDays reads the per-day deleted event counts the archive kept.
func readCoverageDays(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`select utc_day, deleted_event_count from usage_archive_deleted_coverage_daily`)
	if err != nil {
		return nil, fmt.Errorf("read the deleted coverage table: %w", err)
	}
	defer func() { _ = rows.Close() }()
	days := map[string]int64{}
	for rows.Next() {
		var day, deleted int64
		if err := rows.Scan(&day, &deleted); err != nil {
			return nil, err
		}
		days[utcDayLabel(day)] = deleted
	}
	return days, rows.Err()
}

// readHistoryDays reads the per-account monitoring history per UTC day.
func readHistoryDays(ctx context.Context, tx *sql.Tx) (map[string]map[string]historyDay, error) {
	rows, err := tx.QueryContext(ctx, `select
		bucket_ms / 86400000 as utc_day,
		account_snapshot,
		sum(calls),
		sum(total_tokens)
		from usage_monitoring_account_daily_rollups_v1
		group by utc_day, account_snapshot`)
	if err != nil {
		return nil, fmt.Errorf("read the account history rollups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	days := map[string]map[string]historyDay{}
	for rows.Next() {
		var day, calls, tokens int64
		var account string
		if err := rows.Scan(&day, &account, &calls, &tokens); err != nil {
			return nil, err
		}
		label := utcDayLabel(day)
		if days[label] == nil {
			days[label] = map[string]historyDay{}
		}
		days[label][account] = historyDay{calls: calls, tokens: tokens}
	}
	return days, rows.Err()
}

// compareObservations compares the four domains in report order.
func compareObservations(source, target sideObservation) []ReconcileDomainReport {
	return []ReconcileDomainReport{
		compareRetainedDomain(source, target),
		comparePricingDomain(source, target),
		compareCoverageDomain(source, target),
		compareHistoryDomain(source, target),
	}
}

func compareRetainedDomain(source, target sideObservation) ReconcileDomainReport {
	report := newDomainReport(ReconcileDomainRetained, source, target, "usage_events")
	if !report.Applicable {
		return report
	}
	for _, day := range unionDays(keys(source.retained), keys(target.retained)) {
		sourceDay, inSource := source.retained[day]
		targetDay, inTarget := target.retained[day]
		if !inSource || !inTarget {
			report.Mismatches = append(report.Mismatches, presenceMismatch(day, inSource))
			continue
		}
		report.Mismatches = append(report.Mismatches, compareMeasures(day, "", map[string][2]int64{
			mismatchFieldEvents:    {sourceDay.events, targetDay.events},
			mismatchFieldTokens:    {sourceDay.tokens, targetDay.tokens},
			mismatchFieldFailed:    {sourceDay.failed, targetDay.failed},
			mismatchFieldKeyHashes: {sourceDay.keyHashes, targetDay.keyHashes},
		}, []string{mismatchFieldEvents, mismatchFieldTokens, mismatchFieldFailed, mismatchFieldKeyHashes})...)
	}
	report.ComparedDays = len(unionDays(keys(source.retained), keys(target.retained)))
	return report
}

func comparePricingDomain(source, target sideObservation) ReconcileDomainReport {
	report := newDomainReport(ReconcileDomainPricing, source, target, "usage_pricing_hourly_rollups_v1")
	if !report.Applicable {
		return report
	}
	days := unionDays(keys(source.pricing), keys(target.pricing))
	for _, day := range days {
		sourceDay, inSource := source.pricing[day]
		targetDay, inTarget := target.pricing[day]
		if !inSource || !inTarget {
			report.Mismatches = append(report.Mismatches, presenceMismatch(day, inSource))
			continue
		}
		report.Mismatches = append(report.Mismatches, compareMeasures(day, "", map[string][2]int64{
			mismatchFieldRequests: {sourceDay.requests, targetDay.requests},
			mismatchFieldTokens:   {sourceDay.tokens, targetDay.tokens},
		}, []string{mismatchFieldRequests, mismatchFieldTokens})...)
		if !costsAgree(sourceDay.costMicros, targetDay.costMicros) {
			report.Mismatches = append(report.Mismatches, ReconcileMismatch{
				Day:    day,
				Field:  mismatchFieldCost,
				Source: formatMicros(sourceDay.costMicros),
				Target: formatMicros(targetDay.costMicros),
			})
		}
	}
	report.ComparedDays = len(days)
	return report
}

func compareCoverageDomain(source, target sideObservation) ReconcileDomainReport {
	report := newDomainReport(ReconcileDomainCoverage, source, target, "usage_archive_deleted_coverage_daily")
	if !report.Applicable {
		return report
	}
	days := unionDays(keys(source.coverage), keys(target.coverage))
	for _, day := range days {
		sourceDeleted, inSource := source.coverage[day]
		targetDeleted, inTarget := target.coverage[day]
		if !inSource || !inTarget {
			report.Mismatches = append(report.Mismatches, presenceMismatch(day, inSource))
			continue
		}
		report.Mismatches = append(report.Mismatches, compareMeasures(day, "", map[string][2]int64{
			mismatchFieldDeleted: {sourceDeleted, targetDeleted},
		}, []string{mismatchFieldDeleted})...)
	}
	report.ComparedDays = len(days)
	return report
}

func compareHistoryDomain(source, target sideObservation) ReconcileDomainReport {
	report := newDomainReport(ReconcileDomainHistory, source, target, "usage_monitoring_account_daily_rollups_v1")
	if !report.Applicable {
		return report
	}
	days := unionDays(keys(source.history), keys(target.history))
	for _, day := range days {
		sourceDay, inSource := source.history[day]
		targetDay, inTarget := target.history[day]
		if !inSource || !inTarget {
			report.Mismatches = append(report.Mismatches, presenceMismatch(day, inSource))
			continue
		}
		for _, account := range unionDays(keys(sourceDay), keys(targetDay)) {
			sourceAccount, accountInSource := sourceDay[account]
			targetAccount, accountInTarget := targetDay[account]
			if !accountInSource || !accountInTarget {
				report.Mismatches = append(report.Mismatches, ReconcileMismatch{
					Day:       day,
					Field:     mismatchFieldPresence,
					Dimension: account,
					Source:    presenceValue(accountInSource),
					Target:    presenceValue(accountInTarget),
				})
				continue
			}
			report.Mismatches = append(report.Mismatches, compareMeasures(day, account, map[string][2]int64{
				mismatchFieldCalls:  {sourceAccount.calls, targetAccount.calls},
				mismatchFieldTokens: {sourceAccount.tokens, targetAccount.tokens},
			}, []string{mismatchFieldCalls, mismatchFieldTokens})...)
		}
	}
	report.ComparedDays = len(days)
	return report
}

// newDomainReport decides whether a domain can be compared at all. A side that
// does not hold the table says so instead of passing silently.
func newDomainReport(domain string, source, target sideObservation, table string) ReconcileDomainReport {
	report := ReconcileDomainReport{Domain: domain, Mismatches: []ReconcileMismatch{}}
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

// compareMeasures compares the named integer measures in a fixed order, so two
// runs over the same state always report the same list.
func compareMeasures(day, dimension string, values map[string][2]int64, fields []string) []ReconcileMismatch {
	mismatches := []ReconcileMismatch{}
	for _, field := range fields {
		pair := values[field]
		if pair[0] == pair[1] {
			continue
		}
		mismatches = append(mismatches, ReconcileMismatch{
			Day:       day,
			Field:     field,
			Dimension: dimension,
			Source:    strconv.FormatInt(pair[0], 10),
			Target:    strconv.FormatInt(pair[1], 10),
		})
	}
	return mismatches
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

// costsAgree compares two daily cost totals in micro-units. The tolerance is
// relative to the larger side, because the two sides differ only by the
// floating point rounding of the same arithmetic.
func costsAgree(source, target int64) bool {
	if source == target {
		return true
	}
	largest := math.Max(math.Abs(float64(source)), math.Abs(float64(target)))
	return math.Abs(float64(source)-float64(target)) <= largest*costTolerance
}

func formatMicros(value int64) string {
	return strconv.FormatInt(value, 10)
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

// incompleteDays maps each accepted day to the reason it is stored under. A day
// whose measures disagreed keeps the stronger reason even when the parity
// detection also flagged it: a value disagreement is not explained by the
// projection loss alone.
func incompleteDays(accepted, mismatchDays []string) []ReconcileMarkedDay {
	disagreed := make(map[string]bool, len(mismatchDays))
	for _, day := range mismatchDays {
		disagreed[day] = true
	}
	days := make([]ReconcileMarkedDay, 0, len(accepted))
	for _, day := range accepted {
		reason := coverageincomplete.ReasonProjectionLostBeforeV1146
		if disagreed[day] {
			reason = coverageincomplete.ReasonReconcileMismatch
		}
		days = append(days, ReconcileMarkedDay{Day: day, Reason: reason})
	}
	return days
}

// recordReconciliation writes the marks, records the evidence and advances the
// operation, in that order. Each step is idempotent, and the stage is only ever
// moved after the evidence it describes is durable.
func recordReconciliation(ctx context.Context, databasePath string, recorded model.UpgradeOperation, evidence ReconcileEvidence) (model.UpgradeOperation, int, error) {
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		return model.UpgradeOperation{}, 0, fmt.Errorf("open the v2 database to record the reconciliation: %w", err)
	}
	defer func() { _ = db.Close() }()
	marks := make([]coverageincomplete.Day, 0, len(evidence.IncompleteDays))
	for _, day := range evidence.IncompleteDays {
		marks = append(marks, coverageincomplete.Day{Day: day.Day, Reason: day.Reason})
	}
	inserted, err := coverageincomplete.New(db).Mark(ctx, marks, time.Now().UnixMilli())
	if err != nil {
		return model.UpgradeOperation{}, 0, err
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		return model.UpgradeOperation{}, inserted, err
	}
	repository := upgradeoperation.New(db)
	if err := repository.RecordReconciliation(ctx, payload); err != nil {
		return model.UpgradeOperation{}, inserted, err
	}
	operation, err := advanceReconciledDomains(ctx, repository, recorded.Stage)
	return operation, inserted, err
}

// advanceReconciledDomains records the domains this step established. A store
// that already moved past "reconcile" keeps its stage: the evidence is recorded
// again, the operation never moves backwards.
func advanceReconciledDomains(ctx context.Context, repository *upgradeoperation.Repository, current model.UpgradeStage) (model.UpgradeOperation, error) {
	if current != "" && current.Valid() && !current.CanAdvanceTo(model.UpgradeStageReconcile) {
		recorded, found, err := repository.Get(ctx)
		if err != nil {
			return model.UpgradeOperation{}, err
		}
		if !found {
			return model.UpgradeOperation{}, fmt.Errorf("%w: no operation row to advance", upgradeoperation.ErrInvalidRow)
		}
		return recorded, nil
	}
	var operation model.UpgradeOperation
	for _, domain := range reconciledDomains {
		advanced, err := repository.Advance(ctx, domain, model.UpgradeStageReconcile)
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
