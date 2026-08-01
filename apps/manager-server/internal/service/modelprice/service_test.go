package modelprice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type staticSetupResolver struct {
	setup store.Setup
}

func (r staticSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return r.setup, true, nil
}

func TestFetchModelsDevModelPrices(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"provider-a": {"models": {
					"shared-model": {"name":"Shared A", "cost":{"input":1,"output":2,"cache_read":0.1,"cache_write":0.2,"tiers":[{"input":3,"output":4,"tier":{"type":"context","size":200000}}]},"experimental":{"modes":{"fast":{"cost":{"input":2.5,"output":5,"cache_read":0}}}}},
				"unique-model": {"cost":{"input":3,"output":4}}
			}},
			"provider-b": {"models": {
				"shared-model": {"cost":{"input":1.5,"output":2.5,"cache_read":0.15,"cache_write":0.25}},
				"same-rule": {"cost":{"input":5,"output":6}}
			}},
			"provider-c": {"models": {
				"same-rule": {"cost":{"output":6,"input":5}}
			}},
			"provider-empty": {"models": {"uncosted": {"limit":{"context":1000}}}}
		}`))
	}))
	t.Cleanup(source.Close)

	prices, skipped, err := fetchModelsDevModelPrices(context.Background(), source.URL, source.Client())
	if err != nil {
		t.Fatalf("fetch models.dev prices: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d", skipped)
	}

	shared, ok := prices["provider-a/shared-model"]
	if !ok {
		t.Fatalf("missing provider-scoped model: %#v", prices)
	}
	if shared.Prompt != 1 || shared.Completion != 2 || shared.CacheRead != 0.1 || shared.CacheCreation != 0.2 ||
		!shared.PromptConfigured || !shared.CompletionConfigured || !shared.CacheReadConfigured || !shared.CacheCreationConfigured {
		t.Fatalf("base price mapping = %#v", shared)
	}
	if shared.Source != SyncSourceModelsDev || shared.SourceModelID != "provider-a/shared-model" {
		t.Fatalf("source metadata = %#v", shared)
	}
	if !strings.Contains(shared.RawJSON, `"tiers"`) {
		t.Fatalf("raw model metadata was not retained: %s", shared.RawJSON)
	}
	if len(shared.ContextTiers) != 1 || shared.ContextTiers[0].ThresholdTokens != 200_000 ||
		shared.ContextTiers[0].Prompt != 3 || shared.ContextTiers[0].Completion != 4 ||
		!shared.ContextTiers[0].PromptConfigured || !shared.ContextTiers[0].CompletionConfigured {
		t.Fatalf("context tiers = %#v", shared.ContextTiers)
	}
	if len(shared.ServiceTiers) != 1 || shared.ServiceTiers[0].Mode != "fast" ||
		shared.ServiceTiers[0].ServiceTier != "priority" || shared.ServiceTiers[0].Prompt != 2.5 ||
		shared.ServiceTiers[0].Completion != 5 || !shared.ServiceTiers[0].CacheReadConfigured ||
		shared.ServiceTiers[0].CacheRead != 0 {
		t.Fatalf("service tiers = %#v", shared.ServiceTiers)
	}

	for _, alias := range []string{"shared-model", "unique-model", "same-rule"} {
		if _, ok := prices[alias]; ok {
			t.Fatalf("fetch catalog unexpectedly materialized alias %q: %#v", alias, prices[alias])
		}
	}
	selection := selectModelPrices(prices, []string{"unique-model", "same-rule"})
	unique, ok := selection.Prices["unique-model"]
	if !ok || unique.SourceModelID != "provider-a/unique-model" {
		t.Fatalf("unique alias = %#v", unique)
	}
	if _, ok := selection.Prices["same-rule"]; ok ||
		!hasCandidate(selection, "same-rule", "provider-b/same-rule") ||
		!hasCandidate(selection, "same-rule", "provider-c/same-rule") {
		t.Fatalf("same-rule ambiguity = %#v", selection)
	}
	if len(selection.Candidates) != 1 || len(selection.Unmatched) != 0 {
		t.Fatalf("unexpected selection result = %#v", selection)
	}
}

func TestDecodeModelsDevCatalogSelectsCanonicalOfficialPrice(t *testing.T) {
	fetched, skipped, err := decodeModelsDevPriceSource(strings.NewReader(`{
		"models": {
			"openai/gpt-5.5": {"name":"GPT 5.5"}
		},
		"providers": {
			"abacus": {"models": {"gpt-5.5": {"cost":{"input":9,"output":18}}}},
			"openai": {"models": {"gpt-5.5": {"cost":{"input":1,"output":2}}}},
			"third-party": {"models": {"gpt-5.5": {"cost":{"input":7,"output":14}}}}
		}
	}`))
	if err != nil {
		t.Fatalf("decode models.dev catalog: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d", skipped)
	}

	collection := collectionFromFetchedSource(fetched)
	selection := selectModelPriceCollection(collection, []string{"gpt-5.5"})
	price, ok := selection.Prices["gpt-5.5"]
	if !ok || price.Source != SyncSourceModelsDev || price.SourceModelID != "openai/gpt-5.5" || price.Prompt != 1 {
		t.Fatalf("official selection = %#v", selection)
	}
	if len(selection.Candidates) != 0 || len(selection.Unmatched) != 0 {
		t.Fatalf("official selection required confirmation: %#v", selection)
	}

	scoped := selectModelPriceCollection(collection, []string{"abacus/gpt-5.5"})
	price, ok = scoped.Prices["abacus/gpt-5.5"]
	if !ok || price.SourceModelID != "abacus/gpt-5.5" || price.Prompt != 9 {
		t.Fatalf("explicit provider selection = %#v", scoped)
	}
}

func TestDecodeModelsDevContextTiersPreservesConfiguredZerosAndIgnoresUnsafeRules(t *testing.T) {
	prices, skipped, err := decodeModelsDevModelPrices(strings.NewReader(`{
		"provider-a":{"models":{
			"tiered":{"cost":{"input":1,"output":2,"tiers":[
				{"input":0,"output":8,"cache_read":0,"tier":{"type":"context","size":200000}},
				{"input":3,"output":4,"cache_write":0.5,"tier":{"type":"context","size":32000}},
				{"input":99,"output":99,"tier":{"type":"future-mode","size":1}}
			]}},
			"duplicate":{"cost":{"input":1,"tiers":[
				{"input":2,"tier":{"type":"context","size":32000}},
				{"input":3,"tier":{"type":"context","size":32000}}
			]}},
			"invalid":{"cost":{"input":1,"tiers":[
				{"input":2,"tier":{"type":"context","size":0}}
			]}},
			"unknown-only":{"cost":{"input":1,"tiers":[
				{"input":2,"tier":{"type":"requests","size":10}}
			]}}
		}}
	}`))
	if err != nil {
		t.Fatalf("decode models.dev prices: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("skipped = %d", skipped)
	}

	tiers := prices["provider-a/tiered"].ContextTiers
	if len(tiers) != 2 || tiers[0].ThresholdTokens != 32_000 || tiers[1].ThresholdTokens != 200_000 {
		t.Fatalf("sorted tiers = %#v", tiers)
	}
	if !tiers[1].PromptConfigured || tiers[1].Prompt != 0 || !tiers[1].CacheReadConfigured || tiers[1].CacheRead != 0 ||
		tiers[1].CacheCreationConfigured {
		t.Fatalf("explicit zero and missing flags = %#v", tiers[1])
	}
	if tiers[0].CacheReadConfigured || !tiers[0].CacheCreationConfigured || tiers[0].CacheCreation != 0.5 {
		t.Fatalf("optional cache fields = %#v", tiers[0])
	}
	for _, modelID := range []string{"provider-a/duplicate", "provider-a/invalid", "provider-a/unknown-only"} {
		if len(prices[modelID].ContextTiers) != 0 {
			t.Fatalf("unsafe tiers activated for %s: %#v", modelID, prices[modelID].ContextTiers)
		}
		if !strings.Contains(prices[modelID].RawJSON, `"tiers"`) {
			t.Fatalf("raw tiers missing for %s: %s", modelID, prices[modelID].RawJSON)
		}
	}
}

func TestDecodeModelsDevRejectsCatalogWithoutUsablePrices(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "empty object", payload: `{}`},
		{name: "null", payload: `null`},
		{name: "empty provider catalog", payload: `{"provider-a":{"models":{}}}`},
		{name: "all models skipped", payload: `{"provider-a":{"models":{"uncosted":{"limit":{"context":1000}}}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prices, _, err := decodeModelsDevModelPrices(strings.NewReader(tt.payload))
			if err == nil || !strings.Contains(err.Error(), "no usable prices") {
				t.Fatalf("decode error = %v, prices = %#v", err, prices)
			}
		})
	}
}

func TestDecodeModelsDevRejectsCatalogWithoutCanonicalModels(t *testing.T) {
	for _, payload := range []string{
		`{"providers":{"abacus":{"models":{"gpt-test":{"cost":{"input":1}}}}}}`,
		`{"models":null,"providers":{"abacus":{"models":{"gpt-test":{"cost":{"input":1}}}}}}`,
	} {
		fetched, _, err := decodeModelsDevPriceSource(strings.NewReader(payload))
		if err == nil || !strings.Contains(err.Error(), "no canonical models") {
			t.Fatalf("decode error = %v, fetched = %#v", err, fetched)
		}
	}
}

func TestPriceMutationsNotifyPricingRollup(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t, testutil.NewConfig(t))
	modelsDev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider-a":{"models":{"synced":{"cost":{"input":3,"output":4}}}}}`))
	}))
	t.Cleanup(modelsDev.Close)

	modelsDevURL := modelsDev.URL
	service := NewMultiSourceWithModelsDev(st, &modelsDevURL, nil, nil)
	var notifications atomic.Int32
	service.SetPricesChangedNotifier(func() {
		notifications.Add(1)
	})

	if _, err := service.Replace(ctx, map[string]store.ModelPrice{
		"manual": {Prompt: 1},
	}); err != nil {
		t.Fatalf("replace prices: %v", err)
	}
	if got := notifications.Load(); got != 1 {
		t.Fatalf("replace notifications = %d, want 1", got)
	}
	if _, err := service.Replace(ctx, map[string]store.ModelPrice{
		"": {Prompt: 1},
	}); err == nil {
		t.Fatal("invalid replace error = nil")
	}
	if got := notifications.Load(); got != 1 {
		t.Fatalf("failed replace notifications = %d, want 1", got)
	}

	result, err := service.Sync(ctx, SyncRequest{Models: []string{"synced"}})
	if err != nil {
		t.Fatalf("sync prices: %v", err)
	}
	if result.Imported != 1 {
		t.Fatalf("sync result = %#v", result)
	}
	if got := notifications.Load(); got != 2 {
		t.Fatalf("sync notifications = %d, want 2", got)
	}
}

func TestModelsDevPriceCacheReusesETagConcurrently(t *testing.T) {
	const etag = `"catalog-v1"`
	var requestCount atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requestCount.Add(1)
		if requestNumber == 1 {
			if received := r.Header.Get("If-None-Match"); received != "" {
				http.Error(w, "unexpected conditional request", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", etag)
			_, _ = w.Write([]byte(`{
				"provider-a":{"models":{
					"cached":{"cost":{"input":1,"output":2}},
					"uncosted":{"limit":{"context":1000}}
				}}
			}`))
			return
		}
		if received := r.Header.Get("If-None-Match"); received != etag {
			http.Error(w, "missing cache validator", http.StatusPreconditionFailed)
			return
		}
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(source.Close)

	cache := &modelsDevPriceCache{}
	prices, skipped, err := cache.fetch(context.Background(), source.URL, source.Client())
	if err != nil {
		t.Fatalf("prime models.dev cache: %v", err)
	}
	if skipped != 1 || prices["provider-a/cached"].Prompt != 1 {
		t.Fatalf("primed prices = %#v, skipped = %d", prices, skipped)
	}

	const workers = 8
	type fetchResult struct {
		prices  map[string]store.ModelPrice
		skipped int
		err     error
	}
	results := make(chan fetchResult, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cachedPrices, cachedSkipped, fetchErr := cache.fetch(context.Background(), source.URL, source.Client())
			results <- fetchResult{prices: cachedPrices, skipped: cachedSkipped, err: fetchErr}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			t.Fatalf("reuse models.dev cache: %v", result.err)
		}
		if result.skipped != 1 || result.prices["provider-a/cached"].Completion != 2 {
			t.Fatalf("cached prices = %#v, skipped = %d", result.prices, result.skipped)
		}
	}
	if got := requestCount.Load(); got != workers+1 {
		t.Fatalf("request count = %d", got)
	}
}

func TestModelsDevPriceCacheKeepsLastKnownGoodAfterUnusableResponse(t *testing.T) {
	const initialETag = `"catalog-v1"`
	var requestCount atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requestCount.Add(1) {
		case 1:
			w.Header().Set("ETag", initialETag)
			_, _ = w.Write([]byte(`{"provider-a":{"models":{"cached":{"cost":{"input":1,"output":2}}}}}`))
		case 2:
			if received := r.Header.Get("If-None-Match"); received != initialETag {
				http.Error(w, "missing initial cache validator", http.StatusPreconditionFailed)
				return
			}
			w.Header().Set("ETag", `"catalog-v2"`)
			_, _ = w.Write([]byte(`{"provider-a":{"models":{"uncosted":{"limit":{"context":1000}}}}}`))
		default:
			if received := r.Header.Get("If-None-Match"); received != initialETag {
				http.Error(w, "unusable response replaced cache validator", http.StatusPreconditionFailed)
				return
			}
			w.Header().Set("ETag", initialETag)
			w.WriteHeader(http.StatusNotModified)
		}
	}))
	t.Cleanup(source.Close)

	cache := &modelsDevPriceCache{}
	prices, _, err := cache.fetch(context.Background(), source.URL, source.Client())
	if err != nil || prices["provider-a/cached"].Prompt != 1 {
		t.Fatalf("prime cache: prices=%#v err=%v", prices, err)
	}
	if _, skipped, err := cache.fetch(context.Background(), source.URL, source.Client()); err == nil ||
		!strings.Contains(err.Error(), "no usable prices") {
		t.Fatalf("unusable response error = %v", err)
	} else if skipped != 1 {
		t.Fatalf("unusable response skipped = %d, want 1", skipped)
	}
	prices, _, err = cache.fetch(context.Background(), source.URL, source.Client())
	if err != nil || prices["provider-a/cached"].Prompt != 1 {
		t.Fatalf("reuse last-known-good cache: prices=%#v err=%v", prices, err)
	}
}

func TestModelsDevPriceCacheDoesNotServeStaleDataAndInvalidatesURL(t *testing.T) {
	var invalidResponse atomic.Bool
	firstSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if invalidResponse.Load() {
			w.Header().Set("ETag", `"catalog-v2"`)
			_, _ = w.Write([]byte(`{"provider-a":`))
			return
		}
		w.Header().Set("ETag", `"catalog-v1"`)
		_, _ = w.Write([]byte(`{"provider-a":{"models":{"cached":{"cost":{"input":1}}}}}`))
	}))
	t.Cleanup(firstSource.Close)

	cache := &modelsDevPriceCache{}
	if _, _, err := cache.fetch(context.Background(), firstSource.URL, firstSource.Client()); err != nil {
		t.Fatalf("prime models.dev cache: %v", err)
	}
	invalidResponse.Store(true)
	prices, skipped, err := cache.fetch(context.Background(), firstSource.URL, firstSource.Client())
	if err == nil || prices != nil || skipped != 0 {
		t.Fatalf("stale cache served after parse failure: prices=%#v skipped=%d err=%v", prices, skipped, err)
	}

	secondSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if received := r.Header.Get("If-None-Match"); received != "" {
			http.Error(w, "etag leaked across URLs", http.StatusPreconditionFailed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `"other-catalog"`)
		_, _ = w.Write([]byte(`{"provider-b":{"models":{"fresh":{"cost":{"input":9}}}}}`))
	}))
	t.Cleanup(secondSource.Close)

	prices, skipped, err = cache.fetch(context.Background(), secondSource.URL, secondSource.Client())
	if err != nil {
		t.Fatalf("fetch changed models.dev URL: %v", err)
	}
	if skipped != 0 || prices["provider-b/fresh"].Prompt != 9 {
		t.Fatalf("changed URL prices = %#v, skipped = %d", prices, skipped)
	}
}

func TestModelsDevCacheFailureFallsBackWithoutStalePrices(t *testing.T) {
	var invalidResponse atomic.Bool
	modelsDev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if invalidResponse.Load() {
			w.Header().Set("ETag", `"catalog-v2"`)
			_, _ = w.Write([]byte(`{"openai":`))
			return
		}
		w.Header().Set("ETag", `"catalog-v1"`)
		_, _ = w.Write([]byte(`{"openai":{"models":{"gpt-test":{"cost":{"input":9,"output":10}}}}}`))
	}))
	t.Cleanup(modelsDev.Close)

	var liteLLMRequests atomic.Int32
	liteLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		liteLLMRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gpt-test":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`))
	}))
	t.Cleanup(liteLLM.Close)

	modelsDevURL := modelsDev.URL
	liteLLMURL := liteLLM.URL
	service := NewMultiSourceWithModelsDev(nil, &modelsDevURL, &liteLLMURL, nil)
	prices, _, sources, _, err := service.fetchAllModelPrices(context.Background(), modelsDev.Client(), []string{"gpt-test"})
	if err != nil {
		t.Fatalf("prime models.dev source: %v", err)
	}
	price, ok := collectionPrice(prices, SyncSourceModelsDev, "openai/gpt-test")
	if len(sources) != 1 || sources[0] != SyncSourceModelsDev || !ok || price.Prompt != 9 {
		t.Fatalf("primed sources = %#v, prices = %#v", sources, prices)
	}
	if got := liteLLMRequests.Load(); got != 0 {
		t.Fatalf("LiteLLM requests during prime = %d", got)
	}

	invalidResponse.Store(true)
	prices, _, sources, sourceResults, err := service.fetchAllModelPrices(context.Background(), modelsDev.Client(), []string{"gpt-test"})
	if err != nil {
		t.Fatalf("fallback after models.dev failure: %v", err)
	}
	if len(sources) != 1 || sources[0] != SyncSourceLiteLLM {
		t.Fatalf("fallback sources = %#v", sources)
	}
	if len(sourceResults) != 2 || sourceResults[0].Source != SyncSourceModelsDev || sourceResults[0].Error == "" || sourceResults[1].Source != SyncSourceLiteLLM {
		t.Fatalf("fallback source results = %#v", sourceResults)
	}
	price, ok = collectionPrice(prices, SyncSourceLiteLLM, "gpt-test")
	if !ok || price.Source != SyncSourceLiteLLM || price.Prompt != 1 {
		t.Fatalf("fallback price = %#v", price)
	}
	if _, exists := collectionPrice(prices, SyncSourceModelsDev, "openai/gpt-test"); exists {
		t.Fatalf("stale models.dev price was reused: %#v", prices)
	}
}

func TestFetchAllModelPricesFallsBackWhenPreferredSourceHangs(t *testing.T) {
	modelsDev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(modelsDev.Close)
	liteLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gpt-test":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`))
	}))
	t.Cleanup(liteLLM.Close)

	modelsDevURL := modelsDev.URL
	liteLLMURL := liteLLM.URL
	service := NewMultiSourceWithModelsDev(nil, &modelsDevURL, &liteLLMURL, nil)
	service.syncSourceTimeout = 25 * time.Millisecond

	startedAt := time.Now()
	prices, _, sources, sourceResults, err := service.fetchAllModelPrices(
		context.Background(),
		modelsDev.Client(),
		[]string{"gpt-test"},
	)
	if err != nil {
		t.Fatalf("fallback after preferred source timeout: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("fallback elapsed = %s, want under 1s", elapsed)
	}
	if len(sources) != 1 || sources[0] != SyncSourceLiteLLM {
		t.Fatalf("fallback sources = %#v", sources)
	}
	if len(sourceResults) != 2 || sourceResults[0].Source != SyncSourceModelsDev ||
		sourceResults[0].Error == "" || sourceResults[1].Source != SyncSourceLiteLLM {
		t.Fatalf("fallback source results = %#v", sourceResults)
	}
	if price, ok := collectionPrice(prices, SyncSourceLiteLLM, "gpt-test"); !ok || price.Source != SyncSourceLiteLLM || price.Prompt != 1 {
		t.Fatalf("fallback price = %#v", price)
	}
}

func TestSyncHTTPClientBoundsProxyResolution(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)

	service := NewMultiSource(nil, nil, nil, staticSetupResolver{setup: store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "test-key",
	}})
	service.syncProxyTimeout = 25 * time.Millisecond

	startedAt := time.Now()
	client, proxyUsed, err := service.syncHTTPClient(context.Background())
	if err != nil {
		t.Fatalf("resolve sync client: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("proxy resolution elapsed = %s, want under 1s", elapsed)
	}
	if client == nil || proxyUsed {
		t.Fatalf("client = %#v, proxy used = %v", client, proxyUsed)
	}
}

func TestSyncPreservesLastKnownModelsDevPriceDuringPreferredSourceFailure(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t, testutil.NewConfig(t))
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-test": {
			Prompt: 9, Completion: 18, PromptConfigured: true, CompletionConfigured: true,
			Source: SyncSourceModelsDev, SourceModelID: "openai/gpt-test",
			ContextTiers: []store.ModelPriceContextTier{{
				ThresholdTokens: 200_000, Prompt: 12, PromptConfigured: true,
			}},
			ServiceTiers: []store.ModelPriceServiceTier{{
				Mode: "fast", ServiceTier: "priority", Prompt: 20, PromptConfigured: true,
			}},
		},
	}); err != nil {
		t.Fatalf("save last-known models.dev price: %v", err)
	}

	var modelsDevAvailable atomic.Bool
	modelsDev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !modelsDevAvailable.Load() {
			http.Error(w, "temporary outage", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"openai":{"models":{"gpt-test":{"cost":{"input":7,"output":14},"experimental":{"modes":{"fast":{"cost":{"input":17.5,"output":35}}}}}}}}`))
	}))
	t.Cleanup(modelsDev.Close)
	liteLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"gpt-test":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002},
			"fallback-only":{"input_cost_per_token":0.000003,"output_cost_per_token":0.000004}
		}`))
	}))
	t.Cleanup(liteLLM.Close)

	modelsDevURL := modelsDev.URL
	liteLLMURL := liteLLM.URL
	service := NewMultiSourceWithModelsDev(st, &modelsDevURL, &liteLLMURL, nil)
	result, err := service.Sync(ctx, SyncRequest{Models: []string{"gpt-test", "fallback-only"}})
	if err != nil {
		t.Fatalf("sync with preferred source outage: %v", err)
	}
	if result.Imported != 1 || len(result.Preserved) != 1 || result.Preserved[0] != "gpt-test" {
		t.Fatalf("outage sync result = %#v", result)
	}
	if price := result.Prices["gpt-test"]; price.Source != SyncSourceModelsDev || price.Prompt != 9 ||
		len(price.ContextTiers) != 1 || len(price.ServiceTiers) != 1 {
		t.Fatalf("last-known price was not preserved: %#v", price)
	}
	if price := result.Prices["fallback-only"]; price.Source != SyncSourceLiteLLM || price.Prompt != 3 {
		t.Fatalf("fallback model was not imported: %#v", price)
	}

	modelsDevAvailable.Store(true)
	result, err = service.Sync(ctx, SyncRequest{Models: []string{"gpt-test"}})
	if err != nil {
		t.Fatalf("sync after preferred source recovery: %v", err)
	}
	if result.Imported != 1 || len(result.Preserved) != 0 {
		t.Fatalf("recovery sync result = %#v", result)
	}
	price := result.Prices["gpt-test"]
	if price.Source != SyncSourceModelsDev || price.Prompt != 7 || len(price.ServiceTiers) != 1 ||
		price.ServiceTiers[0].Prompt != 17.5 {
		t.Fatalf("recovered models.dev price = %#v", price)
	}
}

func TestSyncTreatsUnusableModelsDevResponseAsFailureAndReportsAllPreservedPrices(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t, testutil.NewConfig(t))
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-test": {
			Prompt: 9, Completion: 18, PromptConfigured: true, CompletionConfigured: true,
			Source: SyncSourceModelsDev, SourceModelID: "openai/gpt-test",
		},
		"rare-model": {
			Prompt: 11, Completion: 22, PromptConfigured: true, CompletionConfigured: true,
			Source: SyncSourceModelsDev, SourceModelID: "rare/rare-model",
		},
	}); err != nil {
		t.Fatalf("save last-known models.dev prices: %v", err)
	}

	modelsDev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(modelsDev.Close)
	liteLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"gpt-test":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`))
	}))
	t.Cleanup(liteLLM.Close)

	modelsDevURL := modelsDev.URL
	liteLLMURL := liteLLM.URL
	service := NewMultiSourceWithModelsDev(st, &modelsDevURL, &liteLLMURL, nil)
	result, err := service.Sync(ctx, SyncRequest{Models: []string{"gpt-test", "rare-model"}})
	if err != nil {
		t.Fatalf("sync with unusable preferred response: %v", err)
	}
	if len(result.Preserved) != 2 || result.Preserved[0] != "gpt-test" || result.Preserved[1] != "rare-model" {
		t.Fatalf("preserved prices = %#v, want both existing models", result.Preserved)
	}
	if len(result.SourceResults) < 1 || result.SourceResults[0].Source != SyncSourceModelsDev ||
		!strings.Contains(result.SourceResults[0].Error, "no usable prices") {
		t.Fatalf("models.dev source result = %#v", result.SourceResults)
	}
	if price := result.Prices["gpt-test"]; price.Source != SyncSourceModelsDev || price.Prompt != 9 {
		t.Fatalf("fallback replaced last-known gpt-test price: %#v", price)
	}
	if price := result.Prices["rare-model"]; price.Source != SyncSourceModelsDev || price.Prompt != 11 {
		t.Fatalf("rare model was not retained: %#v", price)
	}
}

func TestSyncAllSourceFailuresLeaveExistingPricesUnchanged(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewStore(t, testutil.NewConfig(t))
	if err := st.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"existing": {Prompt: 4, Source: SyncSourceModelsDev, SourceModelID: "openai/existing"},
	}); err != nil {
		t.Fatalf("save existing price: %v", err)
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	t.Cleanup(failing.Close)
	modelsDevURL := failing.URL
	liteLLMURL := failing.URL
	service := NewMultiSourceWithModelsDev(st, &modelsDevURL, &liteLLMURL, nil)
	if _, err := service.Sync(ctx, SyncRequest{Models: []string{"existing"}}); err == nil ||
		!strings.Contains(err.Error(), "existing prices were not changed") {
		t.Fatalf("all-source failure error = %v", err)
	}
	prices, err := st.LoadModelPrices(ctx)
	if err != nil {
		t.Fatalf("load existing prices: %v", err)
	}
	if len(prices) != 1 || prices["existing"].Prompt != 4 || prices["existing"].Source != SyncSourceModelsDev {
		t.Fatalf("existing prices changed after failure: %#v", prices)
	}
}

func TestPreferredSourceSuccessAllowsFallbackReplacementForMissingModel(t *testing.T) {
	selection := priceSelectionResult{
		Prices: map[string]store.ModelPrice{
			"gpt-test": {Prompt: 1, Source: SyncSourceLiteLLM},
		},
		Matched: map[string]store.ModelPrice{
			"gpt-test": {Prompt: 1, Source: SyncSourceLiteLLM},
		},
	}
	existing := map[string]store.ModelPrice{
		"gpt-test": {Prompt: 9, Source: SyncSourceModelsDev},
	}
	filtered, preserved := preserveFailedSourcePrices(selection, existing, []SyncSourceResult{
		{Source: SyncSourceModelsDev, Models: 1},
		{Source: SyncSourceLiteLLM, Models: 1},
	}, []string{"gpt-test"})
	if len(preserved) != 0 || filtered.Prices["gpt-test"].Source != SyncSourceLiteLLM {
		t.Fatalf("successful preferred-source omission did not allow fallback: selection=%#v preserved=%#v", filtered, preserved)
	}
}

func TestPreserveFailedSourcePricesReportsOnlyRequestedModels(t *testing.T) {
	selection := priceSelectionResult{
		Prices:  map[string]store.ModelPrice{},
		Matched: map[string]store.ModelPrice{},
	}
	existing := map[string]store.ModelPrice{
		"requested":   {Prompt: 1, Source: SyncSourceModelsDev},
		"unrequested": {Prompt: 2, Source: SyncSourceModelsDev},
	}
	_, preserved := preserveFailedSourcePrices(selection, existing, []SyncSourceResult{
		{Source: SyncSourceModelsDev, Error: "offline"},
	}, []string{" requested "})
	if len(preserved) != 1 || preserved[0] != "requested" {
		t.Fatalf("preserved prices = %#v, want requested model only", preserved)
	}
}

func TestSelectModelPricesPrefersDirectScopedIdentity(t *testing.T) {
	prices := map[string]store.ModelPrice{
		"openai/gpt-test": {
			Prompt:           1,
			Completion:       2,
			Source:           SyncSourceModelsDev,
			SourceModelID:    "openai/gpt-test",
			RawJSON:          `{"cost":{"input":1,"output":2}}`,
			PromptConfigured: true,
		},
		"crossmodel/openai/gpt-test": {
			Prompt:           3,
			Completion:       4,
			Source:           SyncSourceModelsDev,
			SourceModelID:    "crossmodel/openai/gpt-test",
			RawJSON:          `{"cost":{"input":3,"output":4}}`,
			PromptConfigured: true,
		},
	}

	selection := selectModelPrices(prices, []string{"openai/gpt-test"})
	if selection.Prices["openai/gpt-test"].SourceModelID != "openai/gpt-test" || len(selection.Candidates) != 0 {
		t.Fatalf("collision selection = %#v", selection)
	}

	scoped := selectModelPrices(prices, []string{"crossmodel/openai/gpt-test"})
	if scoped.Prices["crossmodel/openai/gpt-test"].SourceModelID != "crossmodel/openai/gpt-test" {
		t.Fatalf("scoped selection = %#v", scoped)
	}

	all := selectModelPrices(prices, nil)
	if all.Prices["openai/gpt-test"].SourceModelID != "openai/gpt-test" {
		t.Fatalf("direct scoped identity missing from empty sync: %#v", all.Prices)
	}
	if all.Prices["gpt-test"].SourceModelID != "openai/gpt-test" {
		t.Fatalf("safe direct alias missing from empty sync: %#v", all.Prices)
	}
}

func TestSelectModelPricesTreatsAdvancedPricingDifferencesAsAmbiguous(t *testing.T) {
	prices := map[string]store.ModelPrice{
		"provider-a/shared": {
			Prompt:               1,
			Completion:           2,
			PromptConfigured:     true,
			CompletionConfigured: true,
			Source:               SyncSourceModelsDev,
			SourceModelID:        "provider-a/shared",
			RawJSON:              `{"cost":{"input":1,"output":2,"tiers":[{"input":2,"output":4,"tier":{"type":"context","size":200000}}]}}`,
		},
		"provider-b/shared": {
			Prompt:               1,
			Completion:           2,
			PromptConfigured:     true,
			CompletionConfigured: true,
			Source:               SyncSourceModelsDev,
			SourceModelID:        "provider-b/shared",
			RawJSON:              `{"cost":{"input":1,"output":2}}`,
		},
	}

	selection := selectModelPrices(prices, []string{"shared"})
	if len(selection.Prices) != 0 || len(selection.Candidates) != 1 ||
		!hasCandidate(selection, "shared", "provider-a/shared") ||
		!hasCandidate(selection, "shared", "provider-b/shared") {
		t.Fatalf("advanced price conflict = %#v", selection)
	}
}

func TestModelsDevAmbiguityContinuesToLowerPriorityFallback(t *testing.T) {
	modelsDev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"provider-a":{"models":{"shared":{"cost":{"input":1,"output":2}}}},
			"provider-b":{"models":{"shared":{"cost":{"input":3,"output":4}}}}
		}`))
	}))
	t.Cleanup(modelsDev.Close)
	liteLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"shared":{"input_cost_per_token":0.000009,"output_cost_per_token":0.000009},"fallback-only":{"input_cost_per_token":0.000001}}`))
	}))
	t.Cleanup(liteLLM.Close)

	modelsDevURL := modelsDev.URL
	liteLLMURL := liteLLM.URL
	service := NewMultiSourceWithModelsDev(nil, &modelsDevURL, &liteLLMURL, nil)
	prices, _, sources, _, err := service.fetchAllModelPrices(context.Background(), modelsDev.Client(), nil)
	if err != nil {
		t.Fatalf("fetch all prices: %v", err)
	}
	if len(sources) != 2 || sources[0] != SyncSourceModelsDev || sources[1] != SyncSourceLiteLLM {
		t.Fatalf("sources = %#v", sources)
	}
	selection := selectModelPriceCollection(prices, []string{"shared"})
	if price, ok := selection.Prices["shared"]; !ok || price.Source != SyncSourceLiteLLM || price.Prompt != 9 {
		t.Fatalf("lower-priority fallback selection = %#v", selection)
	}
	if _, ok := collectionPrice(prices, SyncSourceLiteLLM, "fallback-only"); !ok {
		t.Fatalf("unrelated fallback model missing: %#v", prices)
	}
}

func TestLiteLLMAmbiguityContinuesToOpenRouter(t *testing.T) {
	syncURL := "https://example.test/prices"
	service := &Service{syncSources: []priceSyncSource{
		{
			Source: SyncSourceModelsDev,
			URL:    &syncURL,
			Fetch: wrapModelPriceMapFetcher(func(context.Context, string, *http.Client) (map[string]store.ModelPrice, int, error) {
				return map[string]store.ModelPrice{}, 0, nil
			}),
		},
		{
			Source: SyncSourceLiteLLM,
			URL:    &syncURL,
			Fetch: wrapModelPriceMapFetcher(func(context.Context, string, *http.Client) (map[string]store.ModelPrice, int, error) {
				return map[string]store.ModelPrice{
					"provider-a/shared": {Prompt: 1, SourceModelID: "provider-a/shared"},
					"provider-b/shared": {Prompt: 2, SourceModelID: "provider-b/shared"},
				}, 0, nil
			}),
		},
		{
			Source: SyncSourceOpenRouter,
			URL:    &syncURL,
			Fetch: wrapModelPriceMapFetcher(func(context.Context, string, *http.Client) (map[string]store.ModelPrice, int, error) {
				return map[string]store.ModelPrice{
					"shared": {Prompt: 3, SourceModelID: "shared"},
				}, 0, nil
			}),
		},
	}}

	prices, _, sources, _, err := service.fetchAllModelPrices(context.Background(), nil, []string{"shared"})
	if err != nil {
		t.Fatalf("fetch model prices: %v", err)
	}
	if got := strings.Join(sources, ","); got != "models.dev,litellm,openrouter" {
		t.Fatalf("sources = %q", got)
	}
	selection := selectModelPriceCollection(prices, []string{"shared"})
	price, ok := selection.Prices["shared"]
	if !ok || price.Source != SyncSourceOpenRouter || price.SourceModelID != "shared" || price.Prompt != 3 {
		t.Fatalf("OpenRouter fallback selection = %#v", selection)
	}
}

func TestSelectModelPriceCollectionKeepsCandidatesFromEverySource(t *testing.T) {
	metadata := newModelsDevMatchMetadata(map[string]json.RawMessage{
		"openai/gpt-5.5": json.RawMessage(`{}`),
	})
	metadata.modelsDevOfficialSourceModelIDs = map[string]struct{}{
		normalizeModelPriceIdentity("openai/gpt-5.5"): {},
	}
	collection := modelPriceCollection{
		Metadata: metadata,
		Entries: []modelPriceSourceEntry{
			{
				Key: "openai/gpt-5.5",
				Price: store.ModelPrice{
					Prompt: 2, Source: SyncSourceLiteLLM, SourceModelID: "openai/gpt-5.5",
				},
			},
			{
				Key: "openai/gpt-5.5",
				Price: store.ModelPrice{
					Prompt: 3, Source: SyncSourceOpenRouter, SourceModelID: "openai/gpt-5.5",
				},
			},
		},
	}
	for index := range 10 {
		sourceModelID := fmt.Sprintf("provider-%02d/gpt-5.5", index)
		collection.Entries = append(collection.Entries, modelPriceSourceEntry{
			Key: sourceModelID,
			Price: store.ModelPrice{
				Prompt: float64(index + 10), Source: SyncSourceModelsDev, SourceModelID: sourceModelID,
			},
		})
	}
	collection.Entries = append(collection.Entries, modelPriceSourceEntry{
		Key: "openai/gpt-5.5",
		Price: store.ModelPrice{
			Prompt: 1, Source: SyncSourceModelsDev, SourceModelID: "openai/gpt-5.5",
		},
	})

	selection := selectModelPriceCollection(collection, []string{"gpt-5.5-latest"})
	if len(selection.Prices) != 0 || len(selection.Candidates) != 1 || len(selection.Unmatched) != 0 {
		t.Fatalf("fuzzy selection = %#v", selection)
	}
	candidates := selection.Candidates[0].Candidates
	if len(candidates) != 10 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0].Price.Source != SyncSourceModelsDev || candidates[0].SourceModelID != "openai/gpt-5.5" {
		t.Fatalf("official models.dev candidate was not prioritized: %#v", candidates)
	}
	if got := candidateSources(candidates, "openai/gpt-5.5"); strings.Join(got, ",") != "models.dev,litellm,openrouter" {
		t.Fatalf("cross-source candidate identities = %#v", candidates)
	}
	if count := candidateSourceCount(candidates, SyncSourceModelsDev); count != maxSyncCandidates {
		t.Fatalf("models.dev candidate count = %d, want %d: %#v", count, maxSyncCandidates, candidates)
	}
}

func TestFetchAllModelPricesStopsAfterRequestedModelsAreCovered(t *testing.T) {
	modelsDevPrices := map[string]store.ModelPrice{
		"provider-a/primary": {
			Prompt:           1,
			PromptConfigured: true,
			Source:           SyncSourceModelsDev,
			SourceModelID:    "provider-a/primary",
		},
		"provider-a/shared": {
			Prompt:           2,
			PromptConfigured: true,
			Source:           SyncSourceModelsDev,
			SourceModelID:    "provider-a/shared",
			RawJSON:          `{"cost":{"input":2}}`,
		},
		"provider-b/shared": {
			Prompt:           3,
			PromptConfigured: true,
			Source:           SyncSourceModelsDev,
			SourceModelID:    "provider-b/shared",
			RawJSON:          `{"cost":{"input":3}}`,
		},
	}
	liteLLMPrices := map[string]store.ModelPrice{
		"lite-only": {
			Prompt:           4,
			PromptConfigured: true,
			Source:           SyncSourceLiteLLM,
			SourceModelID:    "lite-only",
		},
	}
	openRouterPrices := map[string]store.ModelPrice{
		"router-only": {
			Prompt:           5,
			PromptConfigured: true,
			Source:           SyncSourceOpenRouter,
			SourceModelID:    "router-only",
		},
	}

	tests := []struct {
		name        string
		models      []string
		wantSources string
		wantCalls   [3]int32
	}{
		{name: "models.dev coverage", models: []string{"primary"}, wantSources: SyncSourceModelsDev, wantCalls: [3]int32{1, 0, 0}},
		{name: "models.dev ambiguity", models: []string{"shared"}, wantSources: SyncSourceModelsDev + "," + SyncSourceLiteLLM + "," + SyncSourceOpenRouter, wantCalls: [3]int32{1, 1, 1}},
		{name: "LiteLLM completes coverage", models: []string{"primary", "lite-only"}, wantSources: SyncSourceModelsDev + "," + SyncSourceLiteLLM, wantCalls: [3]int32{1, 1, 0}},
		{name: "OpenRouter still required", models: []string{"router-only"}, wantSources: SyncSourceModelsDev + "," + SyncSourceLiteLLM + "," + SyncSourceOpenRouter, wantCalls: [3]int32{1, 1, 1}},
		{name: "empty request fetches all", models: nil, wantSources: SyncSourceModelsDev + "," + SyncSourceLiteLLM + "," + SyncSourceOpenRouter, wantCalls: [3]int32{1, 1, 1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls [3]atomic.Int32
			syncURL := "https://example.test/prices"
			service := &Service{syncSources: []priceSyncSource{
				{
					Source: SyncSourceModelsDev,
					URL:    &syncURL,
					Fetch: wrapModelPriceMapFetcher(func(context.Context, string, *http.Client) (map[string]store.ModelPrice, int, error) {
						calls[0].Add(1)
						return modelsDevPrices, 0, nil
					}),
				},
				{
					Source: SyncSourceLiteLLM,
					URL:    &syncURL,
					Fetch: wrapModelPriceMapFetcher(func(context.Context, string, *http.Client) (map[string]store.ModelPrice, int, error) {
						calls[1].Add(1)
						return liteLLMPrices, 0, nil
					}),
				},
				{
					Source: SyncSourceOpenRouter,
					URL:    &syncURL,
					Fetch: wrapModelPriceMapFetcher(func(context.Context, string, *http.Client) (map[string]store.ModelPrice, int, error) {
						calls[2].Add(1)
						return openRouterPrices, 0, nil
					}),
				},
			}}

			prices, _, sources, sourceResults, err := service.fetchAllModelPrices(context.Background(), nil, test.models)
			if err != nil {
				t.Fatalf("fetch model prices: %v", err)
			}
			if got := strings.Join(sources, ","); got != test.wantSources {
				t.Fatalf("sources = %q", got)
			}
			if len(sourceResults) != len(sources) {
				t.Fatalf("source results = %#v", sourceResults)
			}
			for index, want := range test.wantCalls {
				if got := calls[index].Load(); got != want {
					t.Fatalf("source %d calls = %d, want %d", index, got, want)
				}
			}
			if test.name == "models.dev ambiguity" {
				selection := selectModelPriceCollection(prices, test.models)
				if len(selection.Prices) != 0 || len(selection.Candidates) != 1 {
					t.Fatalf("ambiguity selection = %#v", selection)
				}
			}
		})
	}
}

func TestUsageSummaryUsesConfiguredRecentLimit(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	if _, err := st.UsageEvents.InsertBatch(context.Background(), []usage.Event{
		{EventHash: "older", TimestampMS: 100, Timestamp: "2026-01-01T00:00:00Z", Model: "gpt-old", CreatedAtMS: 100},
		{EventHash: "newer", TimestampMS: 200, Timestamp: "2026-01-01T00:00:01Z", Model: "gpt-new", ResolvedModel: "gpt-resolved", CreatedAtMS: 200},
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	summary, err := New(st, nil).UsageSummary(context.Background(), 1)
	if err != nil {
		t.Fatalf("usage summary: %v", err)
	}
	if summary.SampledEvents != 1 || summary.TotalEvents != 2 || !summary.Truncated {
		t.Fatalf("summary metadata = %#v", summary)
	}
	if len(summary.Models) != 2 || summary.Models[0].Model != "gpt-new" || summary.Models[1].Model != "gpt-resolved" {
		t.Fatalf("models = %#v", summary.Models)
	}
}

func TestSelectModelPricesIncludesResolvedAndProviderVariants(t *testing.T) {
	remote := map[string]store.ModelPrice{
		"anthropic/claude-sonnet-4-5": {
			Prompt:        3,
			Completion:    15,
			Cache:         0.3,
			Source:        SyncSource,
			SourceModelID: "anthropic/claude-sonnet-4-5",
		},
		"openai/GPT-4.1": {
			Prompt:        2,
			Completion:    8,
			Source:        SyncSource,
			SourceModelID: "openai/GPT-4.1",
		},
	}

	selection := selectModelPrices(remote, []string{"claude-sonnet-4-5", "gpt-4.1"})

	if len(selection.Prices) != 2 {
		t.Fatalf("selected prices = %#v", selection.Prices)
	}
	if selection.Prices["claude-sonnet-4-5"].SourceModelID != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("claude source = %#v", selection.Prices["claude-sonnet-4-5"])
	}
	if selection.Prices["gpt-4.1"].SourceModelID != "openai/GPT-4.1" {
		t.Fatalf("gpt source = %#v", selection.Prices["gpt-4.1"])
	}
	if len(selection.Candidates) != 0 || len(selection.Unmatched) != 0 {
		t.Fatalf("unexpected candidates/unmatched = %#v %#v", selection.Candidates, selection.Unmatched)
	}
}

func TestFetchOpenRouterModelPrices(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": "openai/gpt-test",
					"pricing": {
						"prompt": "0.000001",
						"completion": "0.000002",
						"input_cache_read": "0.00000025"
					}
				},
				{"id": "skip-no-pricing"}
			]
		}`))
	}))
	t.Cleanup(source.Close)

	prices, skipped, err := fetchOpenRouterModelPrices(context.Background(), source.URL, source.Client())
	if err != nil {
		t.Fatalf("fetch openrouter prices: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d", skipped)
	}
	price := prices["openai/gpt-test"]
	if price.Source != SyncSourceOpenRouter || price.SourceModelID != "openai/gpt-test" {
		t.Fatalf("source metadata = %#v", price)
	}
	if !closePrice(price.Prompt, 1) || !closePrice(price.Completion, 2) || !closePrice(price.Cache, 0.25) ||
		!price.PromptConfigured || !price.CompletionConfigured || !price.CacheReadConfigured || price.CacheCreationConfigured {
		t.Fatalf("price = %#v", price)
	}
}

func TestSelectModelPricesReturnsCandidatesForAmbiguousModels(t *testing.T) {
	remote := map[string]store.ModelPrice{
		"anthropic/claude-sonnet-4-20250514": {
			Prompt:        3,
			Completion:    15,
			SourceModelID: "anthropic/claude-sonnet-4-20250514",
		},
		"anthropic/claude-sonnet-4-20250929": {
			Prompt:        3,
			Completion:    15,
			SourceModelID: "anthropic/claude-sonnet-4-20250929",
		},
		"openai/gpt-4.1": {
			Prompt:        2,
			Completion:    8,
			SourceModelID: "openai/gpt-4.1",
		},
	}

	selection := selectModelPrices(remote, []string{"claude-sonnet-4-latest", "unknown-model"})

	if len(selection.Prices) != 0 {
		t.Fatalf("auto matched prices = %#v", selection.Prices)
	}
	if len(selection.Candidates) != 1 {
		t.Fatalf("candidates = %#v", selection.Candidates)
	}
	if selection.Candidates[0].Model != "claude-sonnet-4-latest" || len(selection.Candidates[0].Candidates) == 0 {
		t.Fatalf("candidate set = %#v", selection.Candidates[0])
	}
	if selection.Candidates[0].Candidates[0].Score < minCandidateScore {
		t.Fatalf("candidate score = %#v", selection.Candidates[0].Candidates[0])
	}
	if len(selection.Unmatched) != 1 || selection.Unmatched[0] != "unknown-model" {
		t.Fatalf("unmatched = %#v", selection.Unmatched)
	}
}

func TestSelectModelPricesReturnsWeakFamilyCandidates(t *testing.T) {
	remote := map[string]store.ModelPrice{
		"google/gemini-2.5-flash-lite": {
			Prompt:        0.3,
			Completion:    2.5,
			Source:        SyncSourceOpenRouter,
			SourceModelID: "google/gemini-2.5-flash-lite",
		},
		"qwen/qwen3.5-flash": {
			Prompt:        0.2,
			Completion:    0.8,
			Source:        SyncSourceOpenRouter,
			SourceModelID: "qwen/qwen3.5-flash",
		},
		"minimax/m2.5": {
			Prompt:        0.4,
			Completion:    1.6,
			Source:        SyncSourceOpenRouter,
			SourceModelID: "minimax/m2.5",
		},
		"openai/codex-mini": {
			Prompt:        1.5,
			Completion:    6,
			Source:        SyncSourceOpenRouter,
			SourceModelID: "openai/codex-mini",
		},
	}

	selection := selectModelPrices(remote, []string{
		"gemini-3.5-flash-low",
		"qwen3.6-plus-preview",
		"mimo-v2.5",
		"codex-auto-review",
	})

	if !hasCandidate(selection, "gemini-3.5-flash-low", "google/gemini-2.5-flash-lite") {
		t.Fatalf("gemini candidates = %#v", selection.Candidates)
	}
	if !hasCandidate(selection, "qwen3.6-plus-preview", "qwen/qwen3.5-flash") {
		t.Fatalf("qwen candidates = %#v", selection.Candidates)
	}
	if !hasCandidate(selection, "mimo-v2.5", "minimax/m2.5") {
		t.Fatalf("mimo candidates = %#v", selection.Candidates)
	}
	if len(selection.Unmatched) != 1 || selection.Unmatched[0] != "codex-auto-review" {
		t.Fatalf("unmatched = %#v", selection.Unmatched)
	}
}

func hasCandidate(selection priceSelectionResult, model string, sourceModelID string) bool {
	for _, set := range selection.Candidates {
		if set.Model != model {
			continue
		}
		for _, candidate := range set.Candidates {
			if candidate.SourceModelID == sourceModelID {
				return true
			}
		}
	}
	return false
}

func collectionPrice(collection modelPriceCollection, source string, sourceModelID string) (store.ModelPrice, bool) {
	for _, entry := range collection.Entries {
		if entry.Price.Source == source && entry.Price.SourceModelID == sourceModelID {
			return entry.Price, true
		}
	}
	return store.ModelPrice{}, false
}

func collectionFromFetchedSource(fetched fetchedModelPriceSource) modelPriceCollection {
	collection := modelPriceCollection{Metadata: fetched.Metadata}
	for _, key := range sortedPriceKeys(fetched.Prices) {
		collection.Entries = append(collection.Entries, modelPriceSourceEntry{
			Key:   key,
			Price: fetched.Prices[key],
		})
	}
	return collection
}

func candidateSources(candidates []SyncCandidate, sourceModelID string) []string {
	sources := make([]string, 0)
	for _, candidate := range candidates {
		if candidate.SourceModelID == sourceModelID {
			sources = append(sources, candidate.Price.Source)
		}
	}
	return sources
}

func candidateSourceCount(candidates []SyncCandidate, source string) int {
	count := 0
	for _, candidate := range candidates {
		if candidate.Price.Source == source {
			count++
		}
	}
	return count
}

func closePrice(left float64, right float64) bool {
	if left > right {
		return left-right < 0.0000001
	}
	return right-left < 0.0000001
}
