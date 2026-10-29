package codexinspection

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestXAIValidatedZero(t *testing.T) {
	data, err := os.ReadFile("../../../../../tests/fixtures/xai-billing-zero.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Text, Start, End, Now string
		Want                        bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tt.Now)
			if err != nil {
				t.Fatal(err)
			}
			if got := isXAIValidatedZero(tt.Text, tt.Start, tt.End, now); got != tt.Want {
				t.Fatalf("got %v want %v", got, tt.Want)
			}
		})
	}
}

func TestRequestXAIBillingZeroEnrichment(t *testing.T) {
	for _, tier := range []string{"SuperGrok Heavy", "Free", "unknown"} {
		t.Run(tier, func(t *testing.T) {
			start := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
			end := start.Add(7 * 24 * time.Hour)
			field := func(n byte, b []byte) []byte { return append([]byte{n*8 + 2, byte(len(b))}, b...) }
			stamp := func(v int64) []byte { return binary.AppendUvarint([]byte{8}, uint64(v)) }
			period := append([]byte{8, 2}, field(2, stamp(start.Unix()))...)
			period = append(period, field(3, stamp(end.Unix()))...)
			payload := field(1, field(8, period))
			frame := func(flag byte, b []byte) string {
				return base64.StdEncoding.EncodeToString(append(binary.BigEndian.AppendUint32([]byte{flag}, uint32(len(b))), b...))
			}
			grpc := frame(0, payload) + frame(128, []byte("grpc-status:0\r\n"))
			grpcCalls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					URL, AuthIndex, Method, Data string
					Header                       map[string]string
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if req.AuthIndex != "test-account" || req.Header["Authorization"] != "Bearer $TOKEN$" {
					t.Error("credential scope not preserved")
				}
				var body any = map[string]any{"config": map[string]any{"isUnifiedBillingUser": true, "currentPeriod": map[string]any{"type": "weekly", "start": start.Format(time.RFC3339), "end": end.Format(time.RFC3339)}}}
				if strings.HasSuffix(req.URL, "/settings") {
					body = map[string]any{"subscription_tier_display": tier}
				}
				if req.URL == xaiWebBillingURL {
					grpcCalls++
					body = grpc
					if req.Method != "POST" || req.Data != "AAAAAAIIAA==" {
						t.Error("invalid grpc request")
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "body": body})
			}))
			defer upstream.Close()
			settings := model.DefaultCodexInspectionConfig()
			probe, err := New(nil, nil, upstream.Client()).requestXAIBilling(context.Background(), store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "test"}, settings, account{AuthIndex: "test-account", File: authFile{}})
			if err != nil || !probe.Healthy || probe.Partial {
				t.Fatalf("unexpected health: %+v %v", probe, err)
			}
			if tier == "SuperGrok Heavy" {
				if grpcCalls != 1 || probe.Summary.UsagePercent == nil || *probe.Summary.UsagePercent != 0 || probe.Summary.UsagePercentSource != "grpc-implicit-zero" {
					t.Fatalf("zero not adopted: %+v", probe.Summary)
				}
			} else if grpcCalls != 0 || probe.Summary.UsagePercent != nil {
				t.Fatal("unconfirmed/free usage must stay unknown")
			}
		})
	}
}
