package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const accountActionCandidateQueueSize = 256

type AccountActionCandidateWorker struct {
	store *store.Store
	jobs  chan accountActionCandidate
}

type accountActionCandidate struct {
	FileName       string
	AuthIndex      string
	DisplayAccount string
	AccountID      string
	AuthLabel      string
	Provider       string
	ActionType     string
	Reason         string
	EvidenceJSON   string
	EventHash      string
	SeenAtMS       int64
}

func NewAccountActionCandidateWorker(st *store.Store) *AccountActionCandidateWorker {
	return &AccountActionCandidateWorker{
		store: st,
		jobs:  make(chan accountActionCandidate, accountActionCandidateQueueSize),
	}
}

func (w *AccountActionCandidateWorker) Start(ctx context.Context) {
	go w.run(ctx)
}

func (w *AccountActionCandidateWorker) HandleUsageEvents(ctx context.Context, _ collectorpkg.RuntimeConfig, events []usage.Event) {
	if w == nil || len(events) == 0 {
		return
	}
	now := time.Now()
	for _, event := range events {
		candidate, ok := accountActionCandidateFromEvent(event, now)
		if !ok {
			continue
		}
		select {
		case w.jobs <- candidate:
		case <-ctx.Done():
			return
		default:
			log.Printf("[account-action] job queue full, dropped auth file %q event=%q", candidate.FileName, candidate.EventHash)
		}
	}
}

func (w *AccountActionCandidateWorker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case candidate := <-w.jobs:
			w.handleCandidate(ctx, candidate)
		}
	}
}

func (w *AccountActionCandidateWorker) handleCandidate(ctx context.Context, candidate accountActionCandidate) {
	if candidate.FileName == "" {
		return
	}
	if w == nil || w.store == nil || w.store.AccountActions == nil {
		log.Printf("[account-action] store is not configured, skip auth file %q", candidate.FileName)
		return
	}
	item, err := w.store.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
		ActionType:        candidate.ActionType,
		Provider:          candidate.Provider,
		AuthFileName:      candidate.FileName,
		AuthIndex:         candidate.AuthIndex,
		AccountSnapshot:   candidate.DisplayAccount,
		AccountIDSnapshot: candidate.AccountID,
		AuthLabel:         candidate.AuthLabel,
		Reason:            candidate.Reason,
		EvidenceJSON:      candidate.EvidenceJSON,
		SeenAtMS:          candidate.SeenAtMS,
	})
	if err != nil {
		log.Printf("[account-action] failed to upsert pending candidate for auth file %q: %v", candidate.FileName, err)
		return
	}
	log.Printf("[account-action] saved pending %s candidate %d for auth file %q", candidate.ActionType, item.ID, candidate.FileName)
}

func accountActionCandidateFromEvent(event usage.Event, now time.Time) (accountActionCandidate, bool) {
	actionType, reason, ok := classifyAccountActionEvent(event)
	if !ok {
		return accountActionCandidate{}, false
	}
	fileName := strings.TrimSpace(event.AuthFileSnapshot)
	if fileName == "" {
		log.Printf("[account-action] auth failure event %q has no auth file snapshot, skip pending candidate", event.EventHash)
		return accountActionCandidate{}, false
	}
	seenAtMS := event.TimestampMS
	if seenAtMS <= 0 {
		seenAtMS = now.UnixMilli()
	}
	return accountActionCandidate{
		FileName:       fileName,
		AuthIndex:      strings.TrimSpace(event.AuthIndex),
		DisplayAccount: firstNonEmpty(event.AccountSnapshot, event.AuthLabelSnapshot, event.Source, fileName),
		AccountID:      strings.TrimSpace(event.AuthProjectIDSnapshot),
		AuthLabel:      event.AuthLabelSnapshot,
		Provider:       strings.ToLower(strings.TrimSpace(firstNonEmpty(event.Provider, event.AuthProviderSnapshot))),
		ActionType:     actionType,
		Reason:         reason,
		EvidenceJSON:   buildAccountActionEvidenceJSON(event, actionType, reason),
		EventHash:      event.EventHash,
		SeenAtMS:       seenAtMS,
	}, true
}

func classifyAccountActionEvent(event usage.Event) (string, string, bool) {
	if !event.Failed {
		return "", "", false
	}
	code, typ := accountActionErrorCodeAndType(event)
	code = strings.ToLower(strings.TrimSpace(code))
	typ = strings.ToLower(strings.TrimSpace(typ))
	text := strings.ToLower(strings.Join([]string{event.FailSummary, code, typ}, "\n"))

	if event.FailStatusCode == http.StatusPaymentRequired {
		if strings.Contains(text, "deactivated_workspace") {
			return model.AccountActionTypeDelete, "Workspace is deactivated; review and delete the stale auth file if appropriate", true
		}
		return "", "", false
	}
	if event.FailStatusCode != http.StatusUnauthorized && event.FailStatusCode != http.StatusForbidden {
		return "", "", false
	}

	if strings.Contains(text, "token_revoked") || strings.Contains(text, "invalidated_oauth_token") || strings.Contains(text, "invalidated oauth token") || strings.Contains(text, "oauth token revoked") {
		return model.AccountActionTypeDelete, "OAuth token revoked / invalidated; review and delete the stale auth file if appropriate", true
	}
	if strings.Contains(text, "invalid_grant") || strings.Contains(text, "reauth") || strings.Contains(text, "auth_unavailable") {
		return model.AccountActionTypeReauth, "Authentication is unavailable or requires reauthorization", true
	}
	if typ == "authentication_error" || strings.Contains(text, "authentication_error") || strings.Contains(text, "unauthorized") || strings.Contains(text, "forbidden") {
		return model.AccountActionTypeReview, "Authentication failure requires manual review", true
	}
	return "", "", false
}

func accountActionErrorCodeAndType(event usage.Event) (string, string) {
	for _, text := range []string{event.FailBody, event.RawJSON, event.FailSummary} {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			continue
		}
		if code, typ, ok := accountActionErrorCodeAndTypeFromJSON(decoded); ok {
			return code, typ
		}
	}
	return "", ""
}

func accountActionErrorCodeAndTypeFromJSON(value any) (string, string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		code := strings.TrimSpace(firstNonEmpty(anyToString(typed["code"]), anyToString(typed["error_code"]), anyToString(typed["errorCode"])))
		typ := strings.TrimSpace(firstNonEmpty(anyToString(typed["type"]), anyToString(typed["error_type"]), anyToString(typed["errorType"])))
		if rawError, ok := typed["error"]; ok {
			if childCode, childType, childOK := accountActionErrorCodeAndTypeFromJSON(rawError); childOK {
				code = firstNonEmpty(childCode, code)
				typ = firstNonEmpty(childType, typ)
			}
		}
		if code != "" || typ != "" {
			return code, typ, true
		}
		for _, child := range typed {
			if childCode, childType, ok := accountActionErrorCodeAndTypeFromJSON(child); ok {
				return childCode, childType, true
			}
		}
	case []any:
		for _, child := range typed {
			if code, typ, ok := accountActionErrorCodeAndTypeFromJSON(child); ok {
				return code, typ, true
			}
		}
	}
	return "", "", false
}

func buildAccountActionEvidenceJSON(event usage.Event, actionType string, reason string) string {
	code, typ := accountActionErrorCodeAndType(event)
	evidence := map[string]any{
		"eventHash":         event.EventHash,
		"requestId":         event.RequestID,
		"timestamp":         event.Timestamp,
		"timestampMs":       event.TimestampMS,
		"statusCode":        event.FailStatusCode,
		"failSummary":       event.FailSummary,
		"errorCode":         code,
		"errorType":         typ,
		"authIndex":         event.AuthIndex,
		"authFileName":      event.AuthFileSnapshot,
		"accountSnapshot":   event.AccountSnapshot,
		"accountIdSnapshot": event.AuthProjectIDSnapshot,
		"authLabel":         event.AuthLabelSnapshot,
		"provider":          firstNonEmpty(event.Provider, event.AuthProviderSnapshot),
		"model":             event.Model,
		"endpoint":          event.Endpoint,
		"actionType":        actionType,
		"reason":            reason,
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return ""
	}
	return string(data)
}

func anyToString(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}
