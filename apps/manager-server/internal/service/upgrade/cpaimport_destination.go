package upgrade

// This file is the durable half of the Managed CPA import's destination attempt:
// the record of what this step already submitted, and the identity a retry has to
// reuse.
//
// # Why the record exists
//
// The Supervisor journals a destination stage and start by operation ID and never
// resumes a terminal operation: a second submission under the same ID returns the
// recorded result instead of applying the new request. A retry that regenerated
// the destination secret therefore handed the destination nothing (the replay
// skipped it) while overwriting the only copy of the secret that worked. The
// record keeps the secret, the plugin choice, the client key set and the operation
// ID that were actually submitted, so a retry replays the same request and only a
// recorded terminal failure moves it to a new attempt ID.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// cpaImportDestinationRecord is the settings row that carries the destination
// attempt this step already submitted.
//
// BaseURL binds the attempt to the destination it was staged for: a Manager
// pointed at another CPA must never present a secret minted for a different one.
// It is written only by a run whose authenticated proof passed, and URLProved is
// that statement: a URL the caller supplied but no run proved is never recorded,
// because it is not part of the Supervisor's request and one mistyped address
// would otherwise refuse every later run that names the staged destination. A
// record written before URLProved existed carries a URL but not the proof, so it
// is treated as unproved.
// Intent is the whole request the Supervisor published, minus its operation ID,
// and ManagementKey is the secret that request carried. OperationID and Attempt
// are the Supervisor's replay identity. TerminalFailure records that the attempt
// reached a terminal failure, which is the only condition under which the next
// run may submit a new attempt ID.
//
// ServerFingerprint is the digest of the destination's build version and v8
// configuration body a run proved with the authenticated proof. It is written
// only after that proof and is what the completion gate re-derives from the live
// CPA, so a destination that changed underneath the handoff is refused instead of
// completed.
//
// SourceVersion, SourceLayout and ManualItems are what the completed step
// reported about the source it read. They are the replay's only record of them:
// the operation row keeps the manual items as bounded one-line text, which cannot
// be turned back into an item, a reason and an instruction without guessing, so a
// replay that reported zero manual items would tell the operator the opposite of
// what the import found. They are written by the same statement that records the
// completion, so a row that carries `cpa_config` carries them too.
type cpaImportDestinationRecord struct {
	BaseURL           string                     `json:"baseUrl"`
	URLProved         bool                       `json:"urlProved"`
	ServerFingerprint string                     `json:"serverFingerprint"`
	ManagementKey     string                     `json:"managementKey"`
	Intent            cpaImportDestinationIntent `json:"intent"`
	OperationID       string                     `json:"operationId"`
	Attempt           int                        `json:"attempt"`
	TerminalFailure   bool                       `json:"terminalFailure"`
	SourceVersion     string                     `json:"sourceVersion,omitempty"`
	SourceLayout      string                     `json:"sourceLayout,omitempty"`
	ManualItems       []CPAImportManualItem      `json:"manualItems,omitempty"`
	UpdatedAtMS       int64                      `json:"updatedAtMs"`
}

// urlProved reports whether an earlier run proved BaseURL to be the destination
// this attempt staged. Only a proved URL is immutable across retries.
func (r cpaImportDestinationRecord) urlProved() bool {
	return r.URLProved && r.BaseURL != ""
}

// bindsURL reports whether this record already proved baseURL to be the
// destination the attempt staged, which is the only state in which the URL is no
// longer replaceable: requireSameDestinationRequest refuses a different one once
// this is true. A record that proved another URL, or none, still accepts a
// different URL, so an operator instruction has to distinguish the two.
func (r cpaImportDestinationRecord) bindsURL(baseURL string) bool {
	return r.urlProved() && r.BaseURL == baseURL
}

// cpaImportDestinationIntent is the Supervisor's destination request without its
// operation ID.
//
// The Supervisor admits a replacement attempt only when it differs from the
// published destination in that ID and nothing else: supersedeDestination
// substitutes the new ID into the stored request and requires reflect.DeepEqual
// against the new one (apps/runtime-supervisor/internal/lifecycle/
// destination.go:237-247,314-345). Every field it compares is held here, so a
// retry can prove it is submitting the identical request instead of discovering
// the difference at the Runtime. The management secret travels beside the
// intent, encrypted, because the published request carries it too.
type cpaImportDestinationIntent struct {
	ExpectedRuntimeIdentity   model.RuntimeIdentity   `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration model.RuntimeGeneration `json:"expectedRuntimeGeneration"`
	ExpectedActiveArtifactID  model.RuntimeArtifactID `json:"expectedActiveArtifactId"`
	SourceIdentity            string                  `json:"sourceIdentity"`
	SourceClass               string                  `json:"sourceClass"`
	APIKeys                   []string                `json:"apiKeys"`
	// PluginsEnabled is v2's explicit plugin-runtime choice as it was submitted.
	// It is a pointer so a row that carries no choice is distinguishable from a
	// row that chose false: the step never defaults the choice, and a record that
	// does not hold the one the destination was staged with cannot be replayed.
	PluginsEnabled *bool `json:"pluginsEnabled,omitempty"`
}

// pluginsChoiceEqual compares two recorded plugin choices by value. A nil choice
// equals nothing, because a record that holds none never submitted one.
func pluginsChoiceEqual(left, right *bool) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

// complete reports whether this record holds the request it submitted. A record
// written before the full intent was kept has only its secret and operation ID,
// and can be replayed under that ID but never superseded: superseding it would
// have to assert an equality nothing recorded.
func (r cpaImportDestinationRecord) complete() bool {
	return r.Intent.SourceIdentity != ""
}

// cpaImportDestinationOperationID is the Supervisor operation ID of one attempt.
// Attempt 1 keeps the original derivation, so a run that wrote a record before
// attempts existed still resolves the same journalled operation; a later attempt
// names its number, and the Supervisor treats it as a supersede request whose
// every other field is identical.
func cpaImportDestinationOperationID(sourceIdentity string, attempt int) string {
	base := "upgrade-cpa-import:" + digestBytes([]byte(sourceIdentity))[:32]
	if attempt <= 1 {
		return base
	}
	return fmt.Sprintf("%s:attempt:%d", base, attempt)
}

// nextCPAImportDestinationAttempt returns the same request under the next attempt
// identity. It is only called after a recorded terminal failure: the Supervisor
// refuses to supersede a stage or start that is still live, and it requires the
// replacement request to differ from the stored one only in its operation ID. The
// intent is copied unchanged, and the ID keeps the source identity the request was
// published under, so the derivation itself cannot drift.
func nextCPAImportDestinationAttempt(record cpaImportDestinationRecord) cpaImportDestinationRecord {
	record.Attempt++
	record.OperationID = cpaImportDestinationOperationID(record.Intent.SourceIdentity, record.Attempt)
	record.TerminalFailure = false
	return record
}

// saveCPAImportDestinationRecord writes the attempt before it is submitted,
// protecting the management secret with the store's data key like every other
// persisted secret.
//
// The timestamp is owned here, like the operation row's: the wall clock and the
// stored value are the only inputs, so a host clock that moved back or a store
// restored from a backup written later cannot move the attempt backwards. The
// JSON field and the settings column are written from that one value, so they
// always agree, and a caller's proposed time is discarded.
func saveCPAImportDestinationRecord(ctx context.Context, databasePath, dataKeyPath string, record cpaImportDestinationRecord, managementKey string) error {
	if err := requireRecoveredCopyIn(databasePath); err != nil {
		return err
	}
	key, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		return fmt.Errorf("load the Manager data key: %w", err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		return fmt.Errorf("initialize the secret protector: %w", err)
	}
	protected, err := protector.ProtectString(managementKey)
	if err != nil {
		return fmt.Errorf("protect the destination management secret: %w", err)
	}
	record.ManagementKey = protected
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		return fmt.Errorf("open the v2 database to record the destination attempt: %w", err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin the destination attempt transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var storedValue string
	var storedColumnMS int64
	switch err := tx.QueryRowContext(ctx,
		`select value, updated_at_ms from settings where key = ?`, cpaImportDestinationKeySetting).Scan(&storedValue, &storedColumnMS); {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		storedColumnMS = 0
	default:
		return fmt.Errorf("read the recorded destination attempt timestamp: %w", err)
	}
	var storedJSONMS int64
	if storedValue != "" {
		var previous cpaImportDestinationRecord
		if err := json.Unmarshal([]byte(storedValue), &previous); err == nil {
			storedJSONMS = previous.UpdatedAtMS
		}
	}
	record.UpdatedAtMS = nextDestinationUpdatedAtMS(time.Now().UnixMilli(), storedColumnMS, storedJSONMS)
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode the destination attempt: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`insert into settings (key, value, updated_at_ms) values (?, ?, ?)
		 on conflict (key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		cpaImportDestinationKeySetting, string(payload), record.UpdatedAtMS); err != nil {
		return fmt.Errorf("record the destination attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record the destination attempt: %w", err)
	}
	return nil
}

// recordCPAImportDestinationEvidenceTx merges the proved destination URL, the
// server fingerprint this run observed and the source facts it reported into the
// attempt row, inside the caller's transaction.
//
// The row is the same one the stage recorded, so the management secret, the
// request and the replay identity are preserved byte for byte; only the evidence
// a run actually observed is added. A missing row is a refusal: the completion
// gate reads this row, and writing a null entry would report a handoff the store
// never held.
//
// The source facts are written by this statement rather than by a second one so a
// row carrying `cpa_config` always carries the manual items the step reported:
// the replay answers from this row, and a completed import whose manual items were
// dropped here would be reported back to the operator as "manual items: 0".
func recordCPAImportDestinationEvidenceTx(ctx context.Context, tx *sql.Tx, result CPAImportResult) error {
	destinationURL, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(result.DestinationURL))
	if err != nil {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the import proved no usable destination URL, so it cannot claim the destination holds the copied state", err)
	}
	fingerprint := strings.TrimSpace(result.ServerFingerprint)
	if fingerprint == "" {
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the import proved no destination server fingerprint, so the completion gate could not re-prove the target it copied into", nil)
	}
	var storedValue string
	var storedColumnMS int64
	switch err := tx.QueryRowContext(ctx,
		`select value, updated_at_ms from settings where key = ?`, cpaImportDestinationKeySetting).Scan(&storedValue, &storedColumnMS); {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		return newUpgradeError(UpgradeErrorCPAImportUnproven,
			"the destination attempt this run proved is no longer recorded, so the import cannot hand a destination the store does not describe", nil)
	default:
		return fmt.Errorf("read the recorded destination attempt: %w", err)
	}
	var record cpaImportDestinationRecord
	if err := json.Unmarshal([]byte(storedValue), &record); err != nil {
		return fmt.Errorf("decode the recorded destination attempt: %w", err)
	}
	record.BaseURL = destinationURL
	record.URLProved = true
	record.ServerFingerprint = fingerprint
	record.SourceVersion = strings.TrimSpace(result.SourceVersion)
	record.SourceLayout = strings.TrimSpace(result.SourceLayout)
	record.ManualItems = slices.Clone(result.ManualItems)
	record.UpdatedAtMS = nextDestinationUpdatedAtMS(time.Now().UnixMilli(), storedColumnMS, record.UpdatedAtMS)
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode the destination evidence: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`insert into settings (key, value, updated_at_ms) values (?, ?, ?)
		 on conflict (key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		cpaImportDestinationKeySetting, string(payload), record.UpdatedAtMS); err != nil {
		return fmt.Errorf("record the destination evidence: %w", err)
	}
	return nil
}

// nextDestinationUpdatedAtMS returns the timestamp the attempt's next write must
// store. It is the operation row's rule without a creation time, because the
// settings row has none: never lower than the wall clock, the stored column or
// the stored JSON value, so the two representations cannot disagree and a clock
// that moved backwards cannot make the attempt older than what was already
// recorded.
func nextDestinationUpdatedAtMS(nowMS, storedColumnMS, storedJSONMS int64) int64 {
	next := nowMS
	for _, candidate := range [...]int64{storedColumnMS, storedJSONMS} {
		if candidate > next {
			next = candidate
		}
	}
	return next
}

// loadCPAImportDestinationRecord reads the attempt an earlier run recorded,
// including the decrypted secret. The caller decides whether the recorded
// destination is the one this run is about: returning a record for another URL as
// "not found" would let this run mint a second secret and overwrite the record,
// which is how the staged secret was lost before.
func loadCPAImportDestinationRecord(ctx context.Context, databasePath, dataKeyPath string) (cpaImportDestinationRecord, string, bool, error) {
	if err := requireRecoveredCopyIn(databasePath); err != nil {
		return cpaImportDestinationRecord{}, "", false, err
	}
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		return cpaImportDestinationRecord{}, "", false, fmt.Errorf("open the v2 database to read the destination attempt: %w", err)
	}
	defer func() { _ = db.Close() }()
	return loadCPAImportDestinationRecordFrom(ctx, db, dataKeyPath)
}

// loadCPAImportDestinationRecordFrom is the same read through a connection the
// caller already holds. A step that owns the v2 data root fence already has the
// database open and must not open it a second time: the store it manages is the
// one it reads.
func loadCPAImportDestinationRecordFrom(ctx context.Context, db *sql.DB, dataKeyPath string) (cpaImportDestinationRecord, string, bool, error) {
	var raw string
	err := db.QueryRowContext(ctx, `select value from settings where key = ?`, cpaImportDestinationKeySetting).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return cpaImportDestinationRecord{}, "", false, nil
	}
	if err != nil {
		return cpaImportDestinationRecord{}, "", false, fmt.Errorf("read the destination attempt: %w", err)
	}
	var record cpaImportDestinationRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return cpaImportDestinationRecord{}, "", false, fmt.Errorf("decode the destination attempt: %w", err)
	}
	// The key is never created here: a read that minted a data key could not
	// decrypt the envelope it is looking at, and would leave a second key behind.
	// LoadExistingDataKey is that read in one step, so the absence check and the
	// load cannot disagree with a key another writer created between them.
	key, err := security.LoadExistingDataKey("", dataKeyPath)
	if err != nil {
		return cpaImportDestinationRecord{}, "", false, fmt.Errorf("load the Manager data key: %w", err)
	}
	// The recorded secret is only a secret while it is sealed. UnprotectString
	// hands back a value that is not an envelope unchanged, so an unsealed key
	// written by an older build would read as a decrypted credential; the shape is
	// checked before anything decrypts it, exactly as the admission does.
	if !security.IsValidProtectedEnvelope(strings.TrimSpace(record.ManagementKey)) {
		return cpaImportDestinationRecord{}, "", false, errors.New("the recorded destination attempt carries no sealed management secret")
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		return cpaImportDestinationRecord{}, "", false, fmt.Errorf("initialize the secret protector: %w", err)
	}
	secret, err := protector.UnprotectString(record.ManagementKey)
	if err != nil {
		return cpaImportDestinationRecord{}, "", false, fmt.Errorf("decrypt the destination management secret: %w", err)
	}
	if strings.TrimSpace(secret) == "" {
		return cpaImportDestinationRecord{}, "", false, nil
	}
	return record, secret, true, nil
}
