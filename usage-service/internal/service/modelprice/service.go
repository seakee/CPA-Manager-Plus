package modelprice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/store"
)

const SyncSource = "litellm"

type UpdateRequest struct {
	Prices map[string]store.ModelPrice `json:"prices"`
}

type SyncRequest struct {
	Models []string `json:"models"`
}

type SyncResult struct {
	Source   string                      `json:"source"`
	Imported int                         `json:"imported"`
	Skipped  int                         `json:"skipped"`
	Prices   map[string]store.ModelPrice `json:"prices"`
}

type Service struct {
	store   *store.Store
	syncURL *string
}

func New(store *store.Store, syncURL *string) *Service {
	return &Service{store: store, syncURL: syncURL}
}

func (s *Service) List(ctx context.Context) (map[string]store.ModelPrice, error) {
	return s.store.LoadModelPrices(ctx)
}

func (s *Service) Replace(ctx context.Context, prices map[string]store.ModelPrice) (map[string]store.ModelPrice, error) {
	if prices == nil {
		return nil, errors.New("prices are required")
	}
	if err := s.store.SaveModelPrices(ctx, prices); err != nil {
		return nil, err
	}
	return s.store.LoadModelPrices(ctx)
}

func (s *Service) SyncFromLiteLLM(ctx context.Context, req SyncRequest) (SyncResult, error) {
	remotePrices, skipped, err := fetchLiteLLMModelPrices(ctx, s.currentSyncURL())
	if err != nil {
		return SyncResult{}, err
	}
	selectedPrices := selectModelPrices(remotePrices, req.Models)
	result, err := s.store.UpsertSyncedModelPrices(ctx, selectedPrices)
	if err != nil {
		return SyncResult{}, err
	}
	prices, err := s.store.LoadModelPrices(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{
		Source:   SyncSource,
		Imported: result.Imported,
		Skipped:  result.Skipped + skipped,
		Prices:   prices,
	}, nil
}

func (s *Service) currentSyncURL() string {
	if s.syncURL == nil {
		return ""
	}
	return *s.syncURL
}

func fetchLiteLLMModelPrices(ctx context.Context, syncURL string) (map[string]store.ModelPrice, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, syncURL, nil)
	if err != nil {
		return nil, 0, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, errors.New("model price sync failed: " + err.Error())
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, 0, errors.New("model price sync failed: " + res.Status)
	}
	var raw map[string]map[string]any
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, 0, err
	}
	now := time.Now().UnixMilli()
	prices := map[string]store.ModelPrice{}
	skipped := 0
	for modelID, entry := range raw {
		promptCost, hasPrompt := readFloat(entry, "input_cost_per_token")
		completionCost, hasCompletion := readFloat(entry, "output_cost_per_token")
		cacheCost, hasCache := readFloat(entry, "cache_read_input_token_cost")
		if !hasPrompt && !hasCompletion && !hasCache {
			skipped++
			continue
		}
		rawEntry, _ := json.Marshal(entry)
		prices[modelID] = store.ModelPrice{
			Prompt:        promptCost * 1_000_000,
			Completion:    completionCost * 1_000_000,
			Cache:         cacheCost * 1_000_000,
			Source:        SyncSource,
			SourceModelID: modelID,
			RawJSON:       string(rawEntry),
			UpdatedAtMS:   now,
			SyncedAtMS:    &now,
		}
	}
	return prices, skipped, nil
}

func selectModelPrices(prices map[string]store.ModelPrice, models []string) map[string]store.ModelPrice {
	if len(models) == 0 {
		return prices
	}
	selected := map[string]store.ModelPrice{}
	for _, modelID := range models {
		normalized := strings.TrimSpace(modelID)
		if normalized == "" {
			continue
		}
		if price, ok := prices[normalized]; ok {
			selected[normalized] = price
			continue
		}
		if price, ok := findSuffixModelPrice(prices, normalized); ok {
			selected[normalized] = price
		}
	}
	return selected
}

func findSuffixModelPrice(prices map[string]store.ModelPrice, modelID string) (store.ModelPrice, bool) {
	suffix := "/" + modelID
	var match store.ModelPrice
	matchedKey := ""
	for key, price := range prices {
		if !strings.HasSuffix(key, suffix) {
			continue
		}
		if matchedKey == "" || len(key) < len(matchedKey) {
			matchedKey = key
			match = price
		}
	}
	return match, matchedKey != ""
}

func readFloat(entry map[string]any, key string) (float64, bool) {
	value, ok := entry[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
