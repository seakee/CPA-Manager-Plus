package usage

import (
	"strconv"
	"testing"
	"time"
)

func TestProviderUsageMetadataFromXAIExhaustion(t *testing.T) {
	base := time.Unix(1_784_543_105, 0)
	metadata := ProviderUsageMetadataFromRecord(map[string]any{
		"provider": "xai",
		"fail": map[string]any{
			"status_code": 429,
			"body":        `{"code":"subscription:free-usage-exhausted","error":"You've used all the included free usage for model grok-4.5-build-free for now. Usage resets over a rolling 24-hour window — tokens (actual/limit): 1024413/1000000."}`,
		},
	}, base)
	if metadata == nil {
		t.Fatal("provider usage metadata missing")
	}
	if metadata.Provider != "xai" || metadata.Code != xaiFreeUsageExhaustedCode || metadata.Model != "grok-4.5-build-free" {
		t.Fatalf("identity metadata = %#v", metadata)
	}
	if metadata.Actual == nil || *metadata.Actual != 1_024_413 || metadata.Limit == nil || *metadata.Limit != 1_000_000 {
		t.Fatalf("usage counts = %#v", metadata)
	}
	if metadata.Remaining == nil || *metadata.Remaining != 0 || metadata.Overage == nil || *metadata.Overage != 24_413 {
		t.Fatalf("derived usage counts = %#v", metadata)
	}
	if metadata.WindowKind != ProviderUsageWindowRolling24H || metadata.RecoverAtMS != base.Add(24*time.Hour).UnixMilli() || !metadata.RecoverAtEstimated {
		t.Fatalf("recovery metadata = %#v", metadata)
	}
}

func TestProviderUsageMetadataRequiresExactStructuredCode(t *testing.T) {
	metadata := ProviderUsageMetadataFromRecord(map[string]any{
		"provider": "xai",
		"fail": map[string]any{
			"status_code": 429,
			"body":        `{"code":"rate-limited","error":"This is not subscription:free-usage-exhausted."}`,
		},
	}, time.Unix(1_700_000_000, 0))
	if metadata != nil {
		t.Fatalf("unexpected metadata = %#v", metadata)
	}
}

func TestProviderUsageMetadataPrefersExplicitReset(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	metadata := ProviderUsageMetadataFromRecord(map[string]any{
		"provider": "xai",
		"fail": map[string]any{
			"status_code": 429,
			"body":        `{"code":"subscription:free-usage-exhausted","billing_period_end":1700021600}`,
		},
	}, base)
	if metadata == nil || metadata.RecoverAtMS != base.Add(6*time.Hour).UnixMilli() || metadata.RecoverAtEstimated {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestProviderUsageMetadataIgnoresRetryAfterBodyBackoff(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	metadata := ProviderUsageMetadataFromRecord(map[string]any{
		"provider": "xai",
		"fail": map[string]any{
			"status_code": 429,
			"body":        `{"code":"subscription:free-usage-exhausted","retry_after":60}`,
		},
	}, base)
	if metadata == nil || metadata.RecoverAtMS != base.Add(24*time.Hour).UnixMilli() || !metadata.RecoverAtEstimated {
		t.Fatalf("transport-style body retry_after changed free-usage recovery: %#v", metadata)
	}
}

func TestProviderUsageMetadataPrefersAbsoluteResetAcrossNestedObjects(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	want := base.Add(6 * time.Hour)
	metadata := ProviderUsageMetadataFromRecord(map[string]any{
		"provider": "xai",
		"fail": map[string]any{
			"status_code": 429,
			"body": `{"code":"subscription:free-usage-exhausted","a":{"reset_after_seconds":60},"z":{"billing_period_end":` +
				strconv.FormatInt(want.Unix(), 10) + `}}`,
		},
	}, base)
	if metadata == nil || metadata.RecoverAtMS != want.UnixMilli() || metadata.RecoverAtEstimated {
		t.Fatalf("absolute reset did not win over nested relative reset: %#v", metadata)
	}
}

func TestProviderUsageMetadataRejectsCompatibleProvider(t *testing.T) {
	metadata := ProviderUsageMetadataFromRecord(map[string]any{
		"provider": "openai-compatible-example",
		"fail": map[string]any{
			"status_code": 429,
			"body":        `{"code":"subscription:free-usage-exhausted"}`,
		},
	}, time.Unix(1_700_000_000, 0))
	if metadata != nil {
		t.Fatalf("unexpected metadata = %#v", metadata)
	}
}

func TestProviderUsageMetadataRejectsBareGrokProviderWithoutXAIExecutor(t *testing.T) {
	metadata := ProviderUsageMetadataFromRecord(map[string]any{
		"provider": "grok",
		"fail": map[string]any{
			"status_code": 429,
			"body":        `{"code":"subscription:free-usage-exhausted","error":"tokens (actual/limit): 10/10"}`,
		},
	}, time.Unix(1_700_000_000, 0))
	if metadata != nil {
		t.Fatalf("bare grok provider should not parse as xAI free usage: %#v", metadata)
	}
}

func TestProviderUsageMetadataAcceptsGrokWithXAIExecutor(t *testing.T) {
	metadata := ProviderUsageMetadataFromRecord(map[string]any{
		"provider":      "grok",
		"executor_type": "XAIExecutor",
		"fail": map[string]any{
			"status_code": 429,
			"body":        `{"code":"subscription:free-usage-exhausted","error":"tokens (actual/limit): 10/10"}`,
		},
	}, time.Unix(1_700_000_000, 0))
	if metadata == nil || metadata.Provider != "xai" {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestProviderUsageMetadataAcceptsNativeSnapshotAndRejectsExecutorSubstring(t *testing.T) {
	body := `{"code":"subscription:free-usage-exhausted"}`
	accepted := ProviderUsageMetadataFromRecord(map[string]any{
		"provider":               "grok",
		"auth_provider_snapshot": "xai",
		"fail":                   map[string]any{"status_code": 429, "body": body},
	}, time.Unix(1_700_000_000, 0))
	if accepted == nil {
		t.Fatal("native xAI auth snapshot was ignored")
	}

	rejected := ProviderUsageMetadataFromRecord(map[string]any{
		"provider":      "grok",
		"executor_type": "NotXAIExecutor",
		"fail":          map[string]any{"status_code": 429, "body": body},
	}, time.Unix(1_700_000_000, 0))
	if rejected != nil {
		t.Fatalf("executor substring caused false native xAI match: %#v", rejected)
	}
}

func TestProviderUsageMetadataRejectsConflictingIdentityWithXAIExecutor(t *testing.T) {
	metadata := ProviderUsageMetadataFromRecord(map[string]any{
		"provider":      "openai-compatible-example",
		"executor_type": "XAIExecutor",
		"fail": map[string]any{
			"status_code": 429,
			"body":        `{"code":"subscription:free-usage-exhausted"}`,
		},
	}, time.Unix(1_700_000_000, 0))
	if metadata != nil {
		t.Fatalf("conflicting provider identity was treated as native xAI: %#v", metadata)
	}
}
