package modelprice

import (
	"fmt"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

var benchmarkPriceSelection priceSelectionResult

func BenchmarkSelectModelPrices(b *testing.B) {
	prices := benchmarkModelPrices(7_500)
	for _, modelCount := range []int{100, 1_000} {
		models := benchmarkRequestedModels(7_500, modelCount)
		b.Run(fmt.Sprintf("Candidates7500/Models%d", modelCount), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				benchmarkPriceSelection = selectModelPrices(prices, models)
			}
		})
	}
}

func benchmarkModelPrices(count int) map[string]store.ModelPrice {
	prices := make(map[string]store.ModelPrice, count)
	for i := range count {
		modelID := fmt.Sprintf("model-%05d", i)
		providerID := fmt.Sprintf("provider-%02d", i%32)
		sourceModelID := providerID + "/" + modelID
		price := store.ModelPrice{
			Prompt:               float64(i%11) + 0.25,
			Completion:           float64(i%17) + 0.5,
			PromptConfigured:     true,
			CompletionConfigured: true,
			SourceModelID:        sourceModelID,
		}
		if i < 5_400 {
			price.Source = SyncSourceModelsDev
			price.RawJSON = fmt.Sprintf(`{"cost":{"input":%g,"output":%g}}`, price.Prompt, price.Completion)
		} else {
			price.Source = SyncSourceLiteLLM
		}
		prices[sourceModelID] = price
	}
	return prices
}

func benchmarkRequestedModels(candidateCount int, modelCount int) []string {
	models := make([]string, 0, modelCount)
	for i := range modelCount {
		models = append(models, fmt.Sprintf("model-%05d", (i*7)%candidateCount))
	}
	return models
}
