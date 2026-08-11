package modelprice

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestRepositoryRoundTripsBillingMetadata(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "model-prices.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repository := New(db)
	ctx := context.Background()
	prices := map[string]model.ModelPrice{
		"grok-imagine-image": {
			Prompt:               0,
			Completion:           0,
			Cache:                0,
			PromptConfigured:     true,
			CompletionConfigured: true,
			Source:               "xAI official",
			SourceModelID:        "$0.02/image",
			BillingUnit:          "image",
			BillingRate:          "$0.02/image",
		},
	}
	if err := repository.ReplaceAll(ctx, prices); err != nil {
		t.Fatalf("replace model prices: %v", err)
	}

	loaded, err := repository.LoadAll(ctx)
	if err != nil {
		t.Fatalf("load model prices: %v", err)
	}
	got, ok := loaded["grok-imagine-image"]
	if !ok {
		t.Fatalf("loaded prices missing grok-imagine-image")
	}
	if got.BillingUnit != "image" || got.BillingRate != "$0.02/image" {
		t.Fatalf("billing metadata = %q / %q, want image / $0.02/image", got.BillingUnit, got.BillingRate)
	}
	if got.Source != "xAI official" || got.SourceModelID != "$0.02/image" {
		t.Fatalf("source metadata = %q / %q, want xAI official / $0.02/image", got.Source, got.SourceModelID)
	}
	if got.PromptConfigured != true || got.CompletionConfigured != true {
		t.Fatalf("configured flags = %t / %t, want true/true", got.PromptConfigured, got.CompletionConfigured)
	}
}
