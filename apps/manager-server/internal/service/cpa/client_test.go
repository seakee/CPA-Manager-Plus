package cpa

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestObserveManagementAPI(t *testing.T) {
	tests := []struct {
		name        string
		headers     map[string]string
		wantVersion string
	}{
		{
			name:        "CPA version",
			headers:     map[string]string{"X-CPA-Version": " v7.2.130 "},
			wantVersion: "v7.2.130",
		},
		{
			name:        "legacy server version",
			headers:     map[string]string{"X-Server-Version": "v6.10.8"},
			wantVersion: "v6.10.8",
		},
		{
			name: "CPA version takes precedence",
			headers: map[string]string{
				"X-CPA-Version":    "v7.2.130",
				"X-Server-Version": "v6.10.8",
			},
			wantVersion: "v7.2.130",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotPath string
			var gotAuthorization string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuthorization = r.Header.Get("Authorization")
				for name, value := range test.headers {
					w.Header().Set(name, value)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("not decoded"))
			}))
			defer server.Close()

			version, err := ObserveManagementAPI(t.Context(), server.URL, "management-key")
			if err != nil {
				t.Fatalf("ObserveManagementAPI() error = %v", err)
			}
			if version != test.wantVersion {
				t.Fatalf("ObserveManagementAPI() version = %q, want %q", version, test.wantVersion)
			}
			if gotPath != "/v0/management/config" {
				t.Fatalf("request path = %q", gotPath)
			}
			if gotAuthorization != "Bearer management-key" {
				t.Fatalf("Authorization = %q", gotAuthorization)
			}
		})
	}
}

func TestObserveManagementAPIPreservesValidationCompatibilityWithoutVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	version, err := ObserveManagementAPI(t.Context(), server.URL, "management-key")
	if err != nil {
		t.Fatalf("ObserveManagementAPI() error = %v", err)
	}
	if version != "" {
		t.Fatalf("ObserveManagementAPI() version = %q, want empty", version)
	}
	if err := ValidateManagementAPI(t.Context(), server.URL, "management-key"); err != nil {
		t.Fatalf("ValidateManagementAPI() error = %v", err)
	}
}

func TestObserveManagementAPIReturnsErrors(t *testing.T) {
	t.Run("non-success response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		defer server.Close()

		_, err := ObserveManagementAPI(t.Context(), server.URL, "wrong-key")
		if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
			t.Fatalf("ObserveManagementAPI() error = %v", err)
		}
	})

	t.Run("caller context canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := ObserveManagementAPI(ctx, "http://127.0.0.1:1", "management-key")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ObserveManagementAPI() error = %v, want context canceled", err)
		}
	})

	t.Run("network unavailable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		baseURL := server.URL
		server.Close()

		if _, err := ObserveManagementAPI(t.Context(), baseURL, "management-key"); err == nil {
			t.Fatal("ObserveManagementAPI() error = nil")
		}
	})
}

func TestNormalizeBaseURLCaseInsensitiveScheme(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "HTTP://127.0.0.1:8317", want: "http://127.0.0.1:8317"},
		{raw: "HTTPS://127.0.0.1:8317/v0/management/", want: "https://127.0.0.1:8317"},
		{raw: "Http://CPA.Example:8317/v0/", want: "http://CPA.Example:8317"},
		{raw: "127.0.0.1:8317/v0/management", want: "http://127.0.0.1:8317"},
		{raw: "   ", want: ""},
	}
	for _, tc := range cases {
		if got := NormalizeBaseURL(tc.raw); got != tc.want {
			t.Fatalf("NormalizeBaseURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestObserveUsageAuthorityConfigParsesRetentionAndFailsClosedOnNonJSON(t *testing.T) {
	t.Run("reads top-level and nested v8 retention", func(t *testing.T) {
		topSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"redis-usage-queue-retention-seconds":10}`))
		}))
		defer topSrv.Close()
		cfg, err := ObserveUsageAuthorityConfig(t.Context(), topSrv.URL, "k")
		if err != nil || !cfg.RetentionVerified || cfg.RedisUsageQueueRetentionSeconds != 10 || cfg.RetentionSourceDefault {
			t.Fatalf("top-level = (%+v, %v)", cfg, err)
		}

		nestedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"observability":{"usage":{"redis-usage-queue-retention-seconds":15,"usage-statistics-enabled":true}}}`))
		}))
		defer nestedSrv.Close()
		nestedCfg, err := ObserveUsageAuthorityConfig(t.Context(), nestedSrv.URL, "k")
		if err != nil || !nestedCfg.RetentionVerified || nestedCfg.RedisUsageQueueRetentionSeconds != 15 || !nestedCfg.UsageStatisticsEnabled || nestedCfg.RetentionSourceDefault {
			t.Fatalf("nested = (%+v, %v)", nestedCfg, err)
		}
	})

	t.Run("defaults omitted retention in valid JSON and unverified on non-JSON or zero", func(t *testing.T) {
		emptySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		}))
		defer emptySrv.Close()
		emptyCfg, err := ObserveUsageAuthorityConfig(t.Context(), emptySrv.URL, "k")
		if err != nil || !emptyCfg.RetentionVerified || emptyCfg.RedisUsageQueueRetentionSeconds != 60 || !emptyCfg.RetentionSourceDefault {
			t.Fatalf("empty JSON = (%+v, %v)", emptyCfg, err)
		}

		nonJSONSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`not json`))
		}))
		defer nonJSONSrv.Close()
		nonJSONCfg, err := ObserveUsageAuthorityConfig(t.Context(), nonJSONSrv.URL, "k")
		if err != nil || nonJSONCfg.RetentionVerified {
			t.Fatalf("non-JSON = (%+v, %v), want RetentionVerified=false", nonJSONCfg, err)
		}

		zeroSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"redis-usage-queue-retention-seconds":0}`))
		}))
		defer zeroSrv.Close()
		zeroCfg, err := ObserveUsageAuthorityConfig(t.Context(), zeroSrv.URL, "k")
		if err != nil || zeroCfg.RetentionVerified {
			t.Fatalf("zero retention = (%+v, %v), want RetentionVerified=false", zeroCfg, err)
		}
	})
	t.Run("fails closed on string, null, fractional, non-boolean, or conflicting config fields", func(t *testing.T) {
		validStringSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":"true","redis-usage-queue-retention-seconds":"12"}`))
		}))
		defer validStringSrv.Close()
		validStringCfg, err := ObserveUsageAuthorityConfig(t.Context(), validStringSrv.URL, "k")
		if err != nil || !validStringCfg.RetentionVerified || validStringCfg.RedisUsageQueueRetentionSeconds != 12 || validStringCfg.RetentionSourceDefault {
			t.Fatalf("valid string config = (%+v, %v), want verified 12s non-default", validStringCfg, err)
		}
		fetchedStringCfg, err := FetchUsageConfig(t.Context(), validStringSrv.URL, "k")
		if err != nil || !fetchedStringCfg.UsageStatisticsEnabled || fetchedStringCfg.RedisUsageQueueRetentionSeconds != 12 {
			t.Fatalf("FetchUsageConfig string = (%+v, %v), want enabled=true retention=12", fetchedStringCfg, err)
		}

		for _, tc := range []struct {
			name string
			body string
		}{
			{name: "json null", body: `null`},
			{name: "string invalid retention", body: `{"redis-usage-queue-retention-seconds":"abc"}`},
			{name: "null retention", body: `{"redis-usage-queue-retention-seconds":null}`},
			{name: "bool retention", body: `{"redis-usage-queue-retention-seconds":true}`},
			{name: "fractional retention", body: `{"redis-usage-queue-retention-seconds":10.5}`},
			{name: "conflicting retention keys", body: `{"redis-usage-queue-retention-seconds":10,"redisUsageQueueRetentionSeconds":30}`},
			{name: "conflicting top and nested retention", body: `{"redis-usage-queue-retention-seconds":10,"observability":{"usage":{"redis-usage-queue-retention-seconds":60}}}`},
			{name: "corrupt observability node", body: `{"observability":"broken"}`},
			{name: "non-boolean usage-statistics-enabled string", body: `{"usage-statistics-enabled":"maybe","redis-usage-queue-retention-seconds":20}`},
			{name: "numeric usage-statistics-enabled", body: `{"usage-statistics-enabled":1,"redis-usage-queue-retention-seconds":20}`},
			{name: "conflicting usage-statistics-enabled", body: `{"usage-statistics-enabled":true,"usageStatisticsEnabled":false,"redis-usage-queue-retention-seconds":20}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(tc.body))
				}))
				defer srv.Close()

				obsCfg, err := ObserveUsageAuthorityConfig(t.Context(), srv.URL, "k")
				if err != nil || obsCfg.RetentionVerified {
					t.Fatalf("ObserveUsageAuthorityConfig(%s) = (%+v, %v), want RetentionVerified=false err=nil", tc.name, obsCfg, err)
				}
				if _, err := FetchUsageConfig(t.Context(), srv.URL, "k"); err == nil {
					t.Fatalf("FetchUsageConfig(%s) succeeded, want error", tc.name)
				}
			})
		}
	})
}

func TestFetchExternalUsageStatisticsRejectsNonBooleanOrConflictingPayloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    bool
		wantErr bool
	}{
		{name: "valid bool string false", body: `{"usage-statistics-enabled":"false"}`, want: false},
		{name: "valid bool string true", body: `{"usageStatisticsEnabled":"on"}`, want: true},
		{name: "invalid string", body: `{"usage-statistics-enabled":"invalid"}`, wantErr: true},
		{name: "numeric value", body: `{"usage-statistics-enabled":1}`, wantErr: true},
		{name: "conflicting keys", body: `{"usage-statistics-enabled":true,"usageStatisticsEnabled":false}`, wantErr: true},
		{name: "json null", body: `null`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := FetchExternalUsageStatistics(t.Context(), srv.URL, "valid-mgmt-key")
			if tc.wantErr {
				if !errors.Is(err, ErrProofUnavailable) {
					t.Fatalf("FetchExternalUsageStatistics(%s) err = %v, want ErrProofUnavailable", tc.name, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("FetchExternalUsageStatistics(%s) = (%v, %v), want (%v, nil)", tc.name, got, err, tc.want)
			}
		})
	}
}

func TestParseManagementConfigUsageStatisticsEnabled(t *testing.T) {
	disabledCfg, err := parseManagementConfigPayload([]byte(`{"usage-statistics-enabled":false,"redis-usage-queue-retention-seconds":60}`))
	if err != nil {
		t.Fatalf("parseManagementConfigPayload disabled err = %v", err)
	}
	if !disabledCfg.HasUsageStatisticsEnabled || disabledCfg.UsageStatisticsEnabled || !disabledCfg.RetentionConfigValid || disabledCfg.RetentionVerified {
		t.Fatalf("disabledCfg = %+v, want HasUsageStatisticsEnabled=true UsageStatisticsEnabled=false RetentionConfigValid=true RetentionVerified=false", disabledCfg)
	}

	disabledDefaultCfg, err := parseManagementConfigPayload([]byte(`{"usage-statistics-enabled":false}`))
	if err != nil {
		t.Fatalf("parseManagementConfigPayload disabled default err = %v", err)
	}
	if !disabledDefaultCfg.HasUsageStatisticsEnabled || disabledDefaultCfg.UsageStatisticsEnabled || !disabledDefaultCfg.RetentionConfigValid || disabledDefaultCfg.RetentionVerified || !disabledDefaultCfg.RetentionSourceDefault {
		t.Fatalf("disabledDefaultCfg = %+v, want HasUsageStatisticsEnabled=true UsageStatisticsEnabled=false RetentionConfigValid=true RetentionVerified=false RetentionSourceDefault=true", disabledDefaultCfg)
	}

	enabledCfg, err := parseManagementConfigPayload([]byte(`{"usage-statistics-enabled":true,"redis-usage-queue-retention-seconds":60}`))
	if err != nil {
		t.Fatalf("parseManagementConfigPayload enabled err = %v", err)
	}
	if !enabledCfg.HasUsageStatisticsEnabled || !enabledCfg.UsageStatisticsEnabled || !enabledCfg.RetentionConfigValid || !enabledCfg.RetentionVerified {
		t.Fatalf("enabledCfg = %+v, want HasUsageStatisticsEnabled=true UsageStatisticsEnabled=true RetentionConfigValid=true RetentionVerified=true", enabledCfg)
	}

	zeroRetentionDisabledCfg, err := parseManagementConfigPayload([]byte(`{"usage-statistics-enabled":false,"redis-usage-queue-retention-seconds":0}`))
	if err != nil {
		t.Fatalf("parseManagementConfigPayload zero retention disabled err = %v", err)
	}
	if !zeroRetentionDisabledCfg.HasUsageStatisticsEnabled || zeroRetentionDisabledCfg.UsageStatisticsEnabled || zeroRetentionDisabledCfg.RetentionConfigValid || zeroRetentionDisabledCfg.RetentionVerified {
		t.Fatalf("zeroRetentionDisabledCfg = %+v, want RetentionConfigValid=false RetentionVerified=false", zeroRetentionDisabledCfg)
	}
}

func TestValidateCollectorConfigGuardsHugeRetentionOverflow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"usage-statistics-enabled":true,"redis-usage-queue-retention-seconds":%d}`, math.MaxInt)
	}))
	defer srv.Close()

	if err := ValidateCollectorConfig(t.Context(), srv.URL, "mgmt-key", 5_000); err != nil {
		t.Fatalf("ValidateCollectorConfig huge retention err = %v, want nil", err)
	}
}
