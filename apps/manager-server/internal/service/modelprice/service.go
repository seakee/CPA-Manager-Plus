package modelprice

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	SyncSourceLiteLLM    = "litellm"
	SyncSourceOpenRouter = "openrouter"
	SyncSourceMulti      = "multi"

	// SyncSource is kept for existing tests and callers that still refer to the
	// original single-source LiteLLM sync constant.
	SyncSource = SyncSourceLiteLLM
)

const maxSyncCandidates = 8
const minCandidateScore = 0.55
const minWeakCandidateScore = 0.34

type UpdateRequest struct {
	Prices map[string]store.ModelPrice `json:"prices"`
}

type SyncRequest struct {
	Models []string `json:"models"`
}

type SyncResult struct {
	Source        string                      `json:"source"`
	Sources       []string                    `json:"sources,omitempty"`
	Imported      int                         `json:"imported"`
	Skipped       int                         `json:"skipped"`
	Matched       map[string]store.ModelPrice `json:"matched,omitempty"`
	Candidates    []SyncCandidateSet          `json:"candidates,omitempty"`
	Unmatched     []string                    `json:"unmatched,omitempty"`
	ProxyUsed     bool                        `json:"proxyUsed,omitempty"`
	SourceResults []SyncSourceResult          `json:"sourceResults,omitempty"`
	Prices        map[string]store.ModelPrice `json:"prices"`
}

type SyncSourceResult struct {
	Source  string `json:"source"`
	Models  int    `json:"models"`
	Skipped int    `json:"skipped"`
	Error   string `json:"error,omitempty"`
}

type SyncCandidateSet struct {
	Model      string          `json:"model"`
	Candidates []SyncCandidate `json:"candidates"`
}

type SyncCandidate struct {
	SourceModelID string           `json:"sourceModelId"`
	Score         float64          `json:"score"`
	Reason        string           `json:"reason"`
	Price         store.ModelPrice `json:"price"`
}

type SetupResolver interface {
	ResolveSetup(ctx context.Context) (store.Setup, bool, error)
}

type Service struct {
	store         *store.Store
	syncSources   []priceSyncSource
	setupResolver SetupResolver
}

type fetchModelPricesFunc func(context.Context, string, *http.Client) (map[string]store.ModelPrice, int, error)

type priceSyncSource struct {
	Source string
	URL    *string
	Fetch  fetchModelPricesFunc
}

func New(store *store.Store, syncURL *string, setupResolver ...SetupResolver) *Service {
	return NewMultiSource(store, syncURL, nil, setupResolver...)
}

func NewMultiSource(store *store.Store, liteLLMSyncURL *string, openRouterSyncURL *string, setupResolver ...SetupResolver) *Service {
	var resolver SetupResolver
	if len(setupResolver) > 0 {
		resolver = setupResolver[0]
	}
	sources := []priceSyncSource{
		{Source: SyncSourceLiteLLM, URL: liteLLMSyncURL, Fetch: fetchLiteLLMModelPrices},
	}
	if openRouterSyncURL != nil {
		sources = append(sources, priceSyncSource{
			Source: SyncSourceOpenRouter,
			URL:    openRouterSyncURL,
			Fetch:  fetchOpenRouterModelPrices,
		})
	}
	return &Service{store: store, syncSources: sources, setupResolver: resolver}
}

func (s *Service) List(ctx context.Context) (map[string]store.ModelPrice, error) {
	return s.store.LoadModelPrices(ctx)
}

func (s *Service) UsageSummary(ctx context.Context, limit int) (store.ModelUsageSummary, error) {
	return s.store.ModelUsageSummary(ctx, limit)
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

func (s *Service) Sync(ctx context.Context, req SyncRequest) (SyncResult, error) {
	client, proxyUsed, err := s.syncHTTPClient(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	remotePrices, skipped, sources, sourceResults, err := s.fetchAllModelPrices(ctx, client)
	if err != nil {
		return SyncResult{}, err
	}
	selection := selectModelPrices(remotePrices, req.Models)
	result, err := s.store.UpsertSyncedModelPrices(ctx, selection.Prices)
	if err != nil {
		return SyncResult{}, err
	}
	prices, err := s.store.LoadModelPrices(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{
		Source:        syncResultSource(sources),
		Sources:       sources,
		Imported:      result.Imported,
		Skipped:       result.Skipped + skipped,
		Matched:       selection.Matched,
		Candidates:    selection.Candidates,
		Unmatched:     selection.Unmatched,
		ProxyUsed:     proxyUsed,
		SourceResults: sourceResults,
		Prices:        prices,
	}, nil
}

func (s *Service) SyncFromLiteLLM(ctx context.Context, req SyncRequest) (SyncResult, error) {
	return s.Sync(ctx, req)
}

func (s *Service) fetchAllModelPrices(ctx context.Context, client *http.Client) (map[string]store.ModelPrice, int, []string, []SyncSourceResult, error) {
	remotePrices := map[string]store.ModelPrice{}
	selectedPriorities := map[string]int{}
	sources := make([]string, 0, len(s.syncSources))
	sourceResults := make([]SyncSourceResult, 0, len(s.syncSources))
	failures := []string{}
	totalSkipped := 0

	for priority, source := range s.syncSources {
		syncURL := source.currentURL()
		result := SyncSourceResult{Source: source.Source}
		if syncURL == "" {
			result.Error = "model price sync failed: missing source URL"
			sourceResults = append(sourceResults, result)
			failures = append(failures, source.Source+": "+result.Error)
			continue
		}
		prices, skipped, err := source.Fetch(ctx, syncURL, client)
		result.Skipped = skipped
		if err != nil {
			result.Error = err.Error()
			sourceResults = append(sourceResults, result)
			failures = append(failures, source.Source+": "+err.Error())
			continue
		}
		result.Models = len(prices)
		sourceResults = append(sourceResults, result)
		sources = append(sources, source.Source)
		totalSkipped += skipped

		for modelID, price := range prices {
			if price.Source == "" {
				price.Source = source.Source
			}
			if price.SourceModelID == "" {
				price.SourceModelID = modelID
			}
			if _, exists := remotePrices[modelID]; exists && selectedPriorities[modelID] <= priority {
				continue
			}
			remotePrices[modelID] = price
			selectedPriorities[modelID] = priority
		}
	}

	if len(sources) == 0 {
		if len(failures) == 0 {
			failures = append(failures, "no price sync sources configured")
		}
		return nil, 0, nil, sourceResults, errors.New("model price sync failed: " + strings.Join(failures, "; "))
	}
	return remotePrices, totalSkipped, sources, sourceResults, nil
}

func (source priceSyncSource) currentURL() string {
	if source.URL == nil {
		return ""
	}
	return strings.TrimSpace(*source.URL)
}

func syncResultSource(sources []string) string {
	if len(sources) == 1 {
		return sources[0]
	}
	if len(sources) > 1 {
		return SyncSourceMulti
	}
	return ""
}

func (s *Service) syncHTTPClient(ctx context.Context) (*http.Client, bool, error) {
	proxyURL := s.resolveCPAProxyURL(ctx)
	if proxyURL == "" {
		return defaultSyncHTTPClient(), false, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, false, errors.New("model price sync failed: invalid proxy URL")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(parsed)
	return &http.Client{Timeout: 30 * time.Second, Transport: transport}, true, nil
}

func (s *Service) resolveCPAProxyURL(ctx context.Context) string {
	if s.setupResolver == nil {
		return ""
	}
	setup, ok, err := s.setupResolver.ResolveSetup(ctx)
	if err != nil || !ok || setup.CPAUpstreamURL == "" || setup.ManagementKey == "" {
		return ""
	}
	cfg, err := cpa.FetchManagementConfig(ctx, setup.CPAUpstreamURL, setup.ManagementKey)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.ProxyURL)
}

func defaultSyncHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func fetchLiteLLMModelPrices(ctx context.Context, syncURL string, client *http.Client) (map[string]store.ModelPrice, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, syncURL, nil)
	if err != nil {
		return nil, 0, errors.New("model price sync failed: " + err.Error())
	}
	if client == nil {
		client = defaultSyncHTTPClient()
	}
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
		cacheReadCost, hasCacheRead := readFirstFloat(entry, "cache_read_input_token_cost", "input_cache_read")
		cacheCreationCost, hasCacheCreation := readFirstFloat(entry, "cache_creation_input_token_cost", "cache_write_input_token_cost", "input_cache_write", "input_cache_creation")
		if !hasPrompt && !hasCompletion && !hasCacheRead && !hasCacheCreation {
			skipped++
			continue
		}
		rawEntry, _ := json.Marshal(entry)
		prices[modelID] = store.ModelPrice{
			Prompt:                  promptCost * 1_000_000,
			Completion:              completionCost * 1_000_000,
			Cache:                   cacheReadCost * 1_000_000,
			CacheRead:               cacheReadCost * 1_000_000,
			CacheCreation:           cacheCreationCost * 1_000_000,
			PromptConfigured:        hasPrompt,
			CompletionConfigured:    hasCompletion,
			CacheReadConfigured:     hasCacheRead,
			CacheCreationConfigured: hasCacheCreation,
			Source:                  SyncSourceLiteLLM,
			SourceModelID:           modelID,
			RawJSON:                 string(rawEntry),
			UpdatedAtMS:             now,
			SyncedAtMS:              &now,
		}
	}
	return prices, skipped, nil
}

func fetchOpenRouterModelPrices(ctx context.Context, syncURL string, client *http.Client) (map[string]store.ModelPrice, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, syncURL, nil)
	if err != nil {
		return nil, 0, errors.New("model price sync failed: " + err.Error())
	}
	if client == nil {
		client = defaultSyncHTTPClient()
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, errors.New("model price sync failed: " + err.Error())
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, 0, errors.New("model price sync failed: " + res.Status)
	}

	var raw struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, 0, err
	}

	now := time.Now().UnixMilli()
	prices := map[string]store.ModelPrice{}
	skipped := 0
	for _, entry := range raw.Data {
		modelID := readString(entry, "id")
		pricing, ok := entry["pricing"].(map[string]any)
		if modelID == "" || !ok {
			skipped++
			continue
		}
		promptCost, hasPrompt := readFloat(pricing, "prompt")
		completionCost, hasCompletion := readFloat(pricing, "completion")
		cacheReadCost, hasCacheRead := readFirstFloat(pricing, "input_cache_read", "cache_read_input_token_cost")
		cacheCreationCost, hasCacheCreation := readFirstFloat(pricing, "input_cache_write", "input_cache_creation", "cache_creation_input_token_cost", "cache_write_input_token_cost")
		if !hasPrompt && !hasCompletion && !hasCacheRead && !hasCacheCreation {
			skipped++
			continue
		}
		rawEntry, _ := json.Marshal(entry)
		prices[modelID] = store.ModelPrice{
			Prompt:                  promptCost * 1_000_000,
			Completion:              completionCost * 1_000_000,
			Cache:                   cacheReadCost * 1_000_000,
			CacheRead:               cacheReadCost * 1_000_000,
			CacheCreation:           cacheCreationCost * 1_000_000,
			PromptConfigured:        hasPrompt,
			CompletionConfigured:    hasCompletion,
			CacheReadConfigured:     hasCacheRead,
			CacheCreationConfigured: hasCacheCreation,
			Source:                  SyncSourceOpenRouter,
			SourceModelID:           modelID,
			RawJSON:                 string(rawEntry),
			UpdatedAtMS:             now,
			SyncedAtMS:              &now,
		}
	}
	return prices, skipped, nil
}

type priceSelectionResult struct {
	Prices     map[string]store.ModelPrice
	Matched    map[string]store.ModelPrice
	Candidates []SyncCandidateSet
	Unmatched  []string
}

func selectModelPrices(prices map[string]store.ModelPrice, models []string) priceSelectionResult {
	result := priceSelectionResult{
		Prices:  map[string]store.ModelPrice{},
		Matched: map[string]store.ModelPrice{},
	}
	if len(models) == 0 {
		result.Prices = prices
		result.Matched = prices
		return result
	}
	seen := map[string]bool{}
	for _, modelID := range models {
		normalized := strings.TrimSpace(modelID)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		price, _, ok := findAutomaticModelPrice(prices, normalized)
		if ok {
			result.Prices[normalized] = price
			result.Matched[normalized] = price
			continue
		}
		candidates := findCandidateModelPrices(prices, normalized)
		if len(candidates) > 0 {
			result.Candidates = append(result.Candidates, SyncCandidateSet{
				Model:      normalized,
				Candidates: candidates,
			})
			continue
		}
		result.Unmatched = append(result.Unmatched, normalized)
	}
	return result
}

func findAutomaticModelPrice(prices map[string]store.ModelPrice, modelID string) (store.ModelPrice, string, bool) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return store.ModelPrice{}, "", false
	}
	if price, ok := prices[modelID]; ok {
		return price, "exact", true
	}
	keys := sortedPriceKeys(prices)
	if key, ok := uniqueMatch(keys, func(key string) bool {
		return strings.EqualFold(key, modelID)
	}); ok {
		return prices[key], "case-insensitive", true
	}
	modelTail := canonicalModelTail(modelID)
	if modelTail != "" {
		if key, ok := uniqueMatch(keys, func(key string) bool {
			return canonicalModelTail(key) == modelTail
		}); ok {
			return prices[key], "provider-prefix", true
		}
	}
	modelCanonical := canonicalModelID(modelID)
	if modelCanonical != "" {
		if key, ok := uniqueMatch(keys, func(key string) bool {
			return canonicalModelID(key) == modelCanonical
		}); ok {
			return prices[key], "normalized", true
		}
	}
	return store.ModelPrice{}, "", false
}

func findCandidateModelPrices(prices map[string]store.ModelPrice, modelID string) []SyncCandidate {
	candidates := make([]SyncCandidate, 0, maxSyncCandidates)
	for _, key := range sortedPriceKeys(prices) {
		score, reason := modelSimilarity(modelID, key)
		if score < minCandidateScore && !(score >= minWeakCandidateScore && isWeakRecallReason(reason)) {
			continue
		}
		candidates = append(candidates, SyncCandidate{
			SourceModelID: key,
			Score:         math.Round(score*100) / 100,
			Reason:        reason,
			Price:         prices[key],
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].SourceModelID < candidates[j].SourceModelID
		}
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > maxSyncCandidates {
		return candidates[:maxSyncCandidates]
	}
	return candidates
}

func sortedPriceKeys(prices map[string]store.ModelPrice) []string {
	keys := make([]string, 0, len(prices))
	for key := range prices {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueMatch(keys []string, match func(string) bool) (string, bool) {
	matchedKey := ""
	for _, key := range keys {
		if !match(key) {
			continue
		}
		if matchedKey != "" {
			return "", false
		}
		matchedKey = key
	}
	return matchedKey, matchedKey != ""
}

func modelSimilarity(left string, right string) (float64, string) {
	leftTail := canonicalModelTail(left)
	rightTail := canonicalModelTail(right)
	if leftTail != "" && rightTail != "" {
		if leftTail == rightTail {
			return 0.94, "same-model-with-provider-prefix"
		}
		if strings.Contains(leftTail, rightTail) || strings.Contains(rightTail, leftTail) {
			return 0.78, "model-name-contains"
		}
	}

	leftCanonical := canonicalModelID(left)
	rightCanonical := canonicalModelID(right)
	if leftCanonical != "" && rightCanonical != "" {
		if leftCanonical == rightCanonical {
			return 0.9, "normalized-model-name"
		}
		if strings.Contains(leftCanonical, rightCanonical) || strings.Contains(rightCanonical, leftCanonical) {
			return 0.74, "normalized-name-contains"
		}
	}

	leftTokens := similarityTokens(left)
	rightTokens := similarityTokens(right)
	tokenScore := tokenJaccard(leftTokens, rightTokens)
	editScore := normalizedEditSimilarity(leftTail, rightTail)
	score := math.Max(tokenScore*0.86, editScore*0.82)
	switch {
	case tokenScore >= 0.65:
		return math.Max(score, 0.72), "shared-model-tokens"
	case tokenScore >= 0.4:
		return math.Max(score, 0.58), "shared-model-tokens"
	case sameModelFamily(leftTokens, rightTokens):
		return math.Max(score, 0.46), "same-model-family"
	case editScore >= 0.68:
		return score, "similar-model-name"
	default:
		return score, "weak-similarity"
	}
}

func isWeakRecallReason(reason string) bool {
	return reason == "same-model-family"
}

func canonicalModelID(value string) string {
	return strings.Join(modelTokens(value), "")
}

func canonicalModelTail(value string) string {
	return strings.Join(modelTokens(lastModelSegment(value)), "")
}

func lastModelSegment(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" || strings.EqualFold(part, "models") {
			continue
		}
		return part
	}
	return strings.TrimSpace(value)
}

func modelTokens(value string) []string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	tokens := make([]string, 0, 8)
	var builder strings.Builder
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		token := builder.String()
		if token != "models" {
			tokens = append(tokens, token)
		}
		builder.Reset()
	}
	for _, r := range normalized {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

func similarityTokens(value string) []string {
	seen := map[string]bool{}
	tokens := make([]string, 0, 12)
	add := func(token string) {
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" || token == "models" || isLowSignalToken(token) || seen[token] {
			return
		}
		seen[token] = true
		tokens = append(tokens, token)
	}

	for _, token := range modelTokens(value) {
		add(token)
		for _, split := range splitAlphaNumericToken(token) {
			add(split)
		}
		for _, alias := range tokenAliases(token) {
			add(alias)
		}
		for _, split := range splitAlphaNumericToken(token) {
			for _, alias := range tokenAliases(split) {
				add(alias)
			}
		}
	}
	return tokens
}

func splitAlphaNumericToken(token string) []string {
	if token == "" {
		return nil
	}
	parts := []string{}
	var builder strings.Builder
	var previousClass int
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		parts = append(parts, builder.String())
		builder.Reset()
	}
	for _, r := range token {
		class := 0
		switch {
		case unicode.IsLetter(r):
			class = 1
		case unicode.IsDigit(r):
			class = 2
		default:
			flush()
			previousClass = 0
			continue
		}
		if previousClass != 0 && previousClass != class {
			flush()
		}
		builder.WriteRune(r)
		previousClass = class
	}
	flush()
	return parts
}

func tokenAliases(token string) []string {
	switch token {
	case "mimo":
		return []string{"minimax", "m2", "m25"}
	case "minimax":
		return []string{"mimo"}
	case "m2", "m25":
		return []string{"mimo", "minimax"}
	case "low":
		return []string{"lite"}
	case "lite":
		return []string{"low"}
	case "flashlow":
		return []string{"flashlite", "flash", "lite"}
	case "flashlite":
		return []string{"flashlow", "flash", "low"}
	default:
		return nil
	}
}

func isLowSignalToken(token string) bool {
	switch token {
	case "latest", "preview", "free":
		return true
	default:
		return false
	}
}

func sameModelFamily(left []string, right []string) bool {
	for _, family := range []string{"qwen", "gemini", "minimax", "mimo"} {
		if containsToken(left, family) && containsToken(right, family) {
			return true
		}
	}
	return false
}

func containsToken(tokens []string, target string) bool {
	for _, token := range tokens {
		if token == target {
			return true
		}
	}
	return false
}

func tokenJaccard(left []string, right []string) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	leftSet := map[string]bool{}
	for _, token := range left {
		leftSet[token] = true
	}
	rightSet := map[string]bool{}
	for _, token := range right {
		rightSet[token] = true
	}
	intersection := 0
	for token := range leftSet {
		if rightSet[token] {
			intersection++
		}
	}
	union := len(leftSet) + len(rightSet) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func normalizedEditSimilarity(left string, right string) float64 {
	if left == "" || right == "" {
		return 0
	}
	distance := levenshteinDistance(left, right)
	maxLen := max(len([]rune(left)), len([]rune(right)))
	if maxLen == 0 {
		return 0
	}
	return 1 - float64(distance)/float64(maxLen)
}

func levenshteinDistance(left string, right string) int {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	if len(leftRunes) == 0 {
		return len(rightRunes)
	}
	if len(rightRunes) == 0 {
		return len(leftRunes)
	}
	prev := make([]int, len(rightRunes)+1)
	curr := make([]int, len(rightRunes)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, leftRune := range leftRunes {
		curr[0] = i + 1
		for j, rightRune := range rightRunes {
			cost := 1
			if leftRune == rightRune {
				cost = 0
			}
			curr[j+1] = min(
				curr[j]+1,
				min(prev[j+1]+1, prev[j]+cost),
			)
		}
		prev, curr = curr, prev
	}
	return prev[len(rightRunes)]
}

func readFloat(entry map[string]any, key string) (float64, bool) {
	value, ok := entry[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func readFirstFloat(entry map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := readFloat(entry, key); ok {
			return value, true
		}
	}
	return 0, false
}

func readString(entry map[string]any, key string) string {
	value, ok := entry[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}
