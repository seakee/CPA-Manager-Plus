package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
)

const authSnapshotCacheTTL = 30 * time.Second

type authSnapshot struct {
	AccountSnapshotInvalid bool
	AccountIDInvalid       bool
	Account                string
	AccountID              string
	Label                  string
	FileName               string
	Provider               string
	ProjectID              string
	CapturedAtMS           int64
}

type authSnapshotResolver struct {
	mu            sync.Mutex
	client        *http.Client
	baseURL       string
	managementKey string
	expiresAt     time.Time
	snapshots     map[string]authSnapshot
	ambiguous     map[string]struct{}
}

func newAuthSnapshotResolver() *authSnapshotResolver {
	return &authSnapshotResolver{client: &http.Client{Timeout: 5 * time.Second}}
}

func (r *authSnapshotResolver) clear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.baseURL = ""
	r.managementKey = ""
	r.expiresAt = time.Time{}
	r.snapshots = nil
	r.ambiguous = nil
}

func (r *authSnapshotResolver) lookup(ctx context.Context, cfg RuntimeConfig, authIndices map[string]struct{}) map[string]authSnapshot {
	if r == nil || len(authIndices) == 0 {
		return nil
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.CPAUpstreamURL), "/")
	managementKey := strings.TrimSpace(cfg.ManagementKey)
	if baseURL == "" || managementKey == "" {
		return nil
	}

	now := time.Now()
	r.mu.Lock()
	sameSource := r.baseURL == baseURL && r.managementKey == managementKey
	if sameSource && now.Before(r.expiresAt) && r.hasAllLocked(authIndices) {
		result := r.lookupLocked(authIndices)
		r.mu.Unlock()
		return result
	}
	r.mu.Unlock()

	snapshots, ambiguous, err := r.fetch(ctx, baseURL, managementKey)
	if err != nil {
		r.mu.Lock()
		var result map[string]authSnapshot
		if sameSource {
			result = r.lookupLocked(authIndices)
		}
		r.mu.Unlock()
		return result
	}

	r.mu.Lock()
	r.baseURL = baseURL
	r.managementKey = managementKey
	r.expiresAt = now.Add(authSnapshotCacheTTL)
	r.snapshots = snapshots
	r.ambiguous = ambiguous
	result := r.lookupLocked(authIndices)
	r.mu.Unlock()
	return result
}

func (r *authSnapshotResolver) hasAllLocked(authIndices map[string]struct{}) bool {
	if len(r.snapshots) == 0 && len(r.ambiguous) == 0 {
		return false
	}
	for authIndex := range authIndices {
		if _, ambiguous := r.ambiguous[authIndex]; ambiguous {
			continue
		}
		if _, ok := r.snapshots[authIndex]; !ok {
			return false
		}
	}
	return true
}

func (r *authSnapshotResolver) lookupLocked(authIndices map[string]struct{}) map[string]authSnapshot {
	if len(r.snapshots) == 0 {
		return nil
	}
	result := make(map[string]authSnapshot, len(authIndices))
	for authIndex := range authIndices {
		if _, ambiguous := r.ambiguous[authIndex]; ambiguous {
			continue
		}
		if snapshot, ok := r.snapshots[authIndex]; ok {
			result[authIndex] = snapshot
		}
	}
	return result
}

func (r *authSnapshotResolver) fetch(ctx context.Context, baseURL string, managementKey string) (map[string]authSnapshot, map[string]struct{}, error) {
	client := r.client
	if client == nil {
		client = http.DefaultClient
	}
	capturedAt := time.Now().UnixMilli()
	snapshots := make(map[string]authSnapshot)
	ambiguous := make(map[string]struct{})
	err := cpaauthfiles.New(client).Visit(ctx, baseURL, managementKey, func(item cpaauthfiles.File) (bool, error) {
		file := item.Raw
		authIndex := item.AuthIndex
		if authIndex == "" {
			return false, nil
		}
		if _, alreadyAmbiguous := ambiguous[authIndex]; alreadyAmbiguous {
			return false, nil
		}
		if _, duplicate := snapshots[authIndex]; duplicate {
			delete(snapshots, authIndex)
			ambiguous[authIndex] = struct{}{}
			return false, nil
		}
		account := ""
		fileName := item.Name
		provider := item.Provider
		accountID := ""
		projectID := firstNonEmpty(readAuthFileString(file, "project_id"), readAuthFileString(file, "projectId"), readAuthFileString(file, "gemini_virtual_project"), readAuthFileString(file, "geminiVirtualProject"))
		if strings.EqualFold(provider, "codex") {
			if !item.AccountIDInvalid {
				accountID = item.AccountID
			}
			account = item.AccountSnapshot
		} else {
			account = firstSafeAccount(readAuthFileString(file, "account"), readAuthFileString(file, "email"))
		}
		// Keep the pre-existing non-Codex display fallback: when CPA exposes an
		// account value but no explicit label/name/email, label still carries that
		// account. For Codex, item.AccountSnapshot is already limited to strong
		// member evidence, so it is also safe as a display label; it is never used
		// as a member identity unless the shared identity normalizer accepts it.
		label := firstNonEmpty(readAuthFileString(file, "label"), readAuthFileString(file, "name"), readAuthFileString(file, "email"), account)
		if account == "" && !strings.EqualFold(provider, "codex") {
			account = firstNonEmpty(label, fileName)
		}
		snapshots[authIndex] = authSnapshot{
			AccountSnapshotInvalid: item.AccountSnapshotInvalid,
			AccountIDInvalid:       item.AccountIDInvalid,
			Account:                account,
			AccountID:              accountID,
			Label:                  label,
			FileName:               fileName,
			Provider:               provider,
			ProjectID:              projectID,
			CapturedAtMS:           capturedAt,
		}
		return false, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return snapshots, ambiguous, nil
}

func readAuthFileString(file map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := file[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(toString(value))
		if text != "" {
			return text
		}
	}
	return ""
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(value)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstSafeAccount(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || looksLikeSecret(trimmed) {
			continue
		}
		return trimmed
	}
	return ""
}

func looksLikeSecret(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.Contains(trimmed, "@") {
		return false
	}
	if strings.ContainsAny(trimmed, " /\\") {
		return false
	}
	return strings.HasPrefix(trimmed, "sk-") || strings.HasPrefix(trimmed, "AIza") || (len(trimmed) >= 32 && len(trimmed) <= 512)
}
