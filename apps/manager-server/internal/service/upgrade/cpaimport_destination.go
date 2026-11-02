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
	"os"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

// cpaImportDestinationRecord is the settings row that carries the destination
// attempt this step already submitted.
//
// BaseURL binds the attempt to the destination it was staged for: a Manager
// pointed at another CPA must never present a secret minted for a different one.
// Intent is the whole request the Supervisor published, minus its operation ID,
// and ManagementKey is the secret that request carried. OperationID and Attempt
// are the Supervisor's replay identity. TerminalFailure records that the attempt
// reached a terminal failure, which is the only condition under which the next
// run may submit a new attempt ID.
type cpaImportDestinationRecord struct {
	BaseURL         string                     `json:"baseUrl"`
	ManagementKey   string                     `json:"managementKey"`
	Intent          cpaImportDestinationIntent `json:"intent"`
	OperationID     string                     `json:"operationId"`
	Attempt         int                        `json:"attempt"`
	TerminalFailure bool                       `json:"terminalFailure"`
	UpdatedAtMS     int64                      `json:"updatedAtMs"`
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
	PluginsEnabled            bool                    `json:"pluginsEnabled"`
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
func saveCPAImportDestinationRecord(ctx context.Context, databasePath, dataKeyPath string, record cpaImportDestinationRecord, managementKey string) error {
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
	record.UpdatedAtMS = time.Now().UnixMilli()
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode the destination attempt: %w", err)
	}
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

// loadCPAImportDestinationRecord reads the attempt an earlier run recorded,
// including the decrypted secret. The caller decides whether the recorded
// destination is the one this run is about: returning a record for another URL as
// "not found" would let this run mint a second secret and overwrite the record,
// which is how the staged secret was lost before.
func loadCPAImportDestinationRecord(ctx context.Context, databasePath, dataKeyPath string) (cpaImportDestinationRecord, string, bool, error) {
	db, err := managersqlite.Open(databasePath)
	if err != nil {
		return cpaImportDestinationRecord{}, "", false, fmt.Errorf("open the v2 database to read the destination attempt: %w", err)
	}
	defer func() { _ = db.Close() }()
	var raw string
	err = db.QueryRowContext(ctx, `select value from settings where key = ?`, cpaImportDestinationKeySetting).Scan(&raw)
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
	if _, err := os.Stat(dataKeyPath); err != nil {
		return cpaImportDestinationRecord{}, "", false, fmt.Errorf("read the Manager data key: %w", err)
	}
	key, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		return cpaImportDestinationRecord{}, "", false, fmt.Errorf("load the Manager data key: %w", err)
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
