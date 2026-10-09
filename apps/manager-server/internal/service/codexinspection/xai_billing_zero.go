package codexinspection

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const xaiWebBillingURL = "https://grok.com/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig"

// An omitted REST percentage alone remains unknown (#710). Only a complete,
// successful gRPC response for the same active period establishes implicit zero.
func isXAIValidatedZero(text, start, end string, now time.Time) bool {
	from, err := time.Parse(time.RFC3339Nano, start)
	if err != nil {
		return false
	}
	until, err := time.Parse(time.RFC3339Nano, end)
	if err != nil || now.Before(from) || !now.Before(until) || len(text) > 64*1024 {
		return false
	}
	encoded := strings.Join(strings.Fields(text), "")
	if len(encoded) == 0 || len(encoded)%4 != 0 {
		return false
	}
	// grpc-web-text can flush independently padded chunks, including trailers.
	var data []byte
	for len(encoded) > 0 {
		n := len(encoded)
		if i := strings.IndexByte(encoded, '='); i >= 0 {
			n = ((i / 4) + 1) * 4
		}
		chunk, err := base64.StdEncoding.Strict().DecodeString(encoded[:n])
		if err != nil {
			return false
		}
		data = append(data, chunk...)
		encoded = encoded[n:]
	}
	var payload []byte
	done := false
	for len(data) > 0 {
		if done || len(data) < 5 {
			return false
		}
		flag := data[0]
		size := uint64(binary.BigEndian.Uint32(data[1:5]))
		data = data[5:]
		if size > uint64(len(data)) {
			return false
		}
		frame := data[:int(size)]
		data = data[int(size):]
		if flag == 0 && payload == nil {
			payload = frame
		} else if flag == 128 && payload != nil {
			statuses := 0
			for _, line := range strings.Split(string(frame), "\r\n") {
				if strings.HasPrefix(strings.ToLower(line), "grpc-status:") {
					statuses++
					if strings.TrimSpace(line[len("grpc-status:"):]) != "0" {
						return false
					}
				}
			}
			if statuses != 1 {
				return false
			}
			done = true
		} else {
			return false
		}
	}
	if payload == nil || !done {
		return false
	}
	messages := map[string]bool{"1": true, "1.2": true, "1.3": true, "1.4": true, "1.5": true, "1.6": true, "1.7": true, "1.8": true, "1.12": true, "1.6.1": true, "1.6.2": true, "1.6.3": true, "1.8.2": true, "1.8.3": true, "1.6.3.2": true, "1.6.3.3": true}
	structural := map[string]bool{"1": true, "1.8": true, "1.8.1": true, "1.8.2": true, "1.8.3": true, "1.8.2.1": true, "1.8.3.1": true}
	seen := map[string]bool{}
	values := map[string]uint64{}
	var scan func([]byte, string) bool
	scan = func(b []byte, path string) bool {
		for len(b) > 0 {
			tag, n := binary.Uvarint(b)
			if n <= 0 {
				return false
			}
			b = b[n:]
			field, wire := tag>>3, tag&7
			if field == 0 || field > 536870911 {
				return false
			}
			key := strconv.FormatUint(field, 10)
			if path != "" {
				key = path + "." + key
			}
			if key == "1.1" || (messages[key] && wire != 2) {
				return false
			}
			if structural[key] {
				if seen[key] {
					return false
				}
				seen[key] = true
			}
			switch wire {
			case 0:
				value, n := binary.Uvarint(b)
				if n <= 0 {
					return false
				}
				b = b[n:]
				values[key] = value
			case 2:
				size, n := binary.Uvarint(b)
				if n <= 0 {
					return false
				}
				b = b[n:]
				if size > uint64(len(b)) {
					return false
				}
				if messages[key] && !scan(b[:int(size)], key) {
					return false
				}
				b = b[int(size):]
			case 1:
				if len(b) < 8 {
					return false
				}
				b = b[8:]
			default:
				// Any float could contradict implicit zero; never infer from it.
				return false
			}
		}
		return true
	}
	return scan(payload, "") && values["1.8.1"] == 2 && values["1.8.2.1"] == uint64(from.Unix()) && values["1.8.3.1"] == uint64(until.Unix())
}

func (s *Service) enrichXAIBillingZero(ctx context.Context, setup store.Setup, settings model.ManagerCodexInspectionConfig, item account, summary *xaiBillingSummary) {
	if summary == nil || summary.UsagePercent != nil || !summary.HasWeeklyData || len(summary.ProductUsage) > 0 {
		return
	}
	start, err := time.Parse(time.RFC3339Nano, summary.PeriodStart)
	if err != nil {
		return
	}
	end, err := time.Parse(time.RFC3339Nano, summary.PeriodEnd)
	if err != nil {
		return
	}
	now := time.Now()
	if now.Before(start) || !now.Before(end) {
		return
	}
	bounded, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	settingsResponse, _, err := s.requestProviderBillingAt(bounded, setup, settings, item, xaiCLIChatProxyBaseURL+"/settings", map[string]string{"Authorization": "Bearer $TOKEN$", "x-xai-token-auth": "xai-grok-cli", "x-grok-client-version": xaiGrokVersion})
	if err != nil || settingsResponse.StatusCode != 200 {
		return
	}
	tier := readString(parseRecord(settingsResponse.Body), "subscription_tier_display")
	if tier != "SuperGrok" && tier != "SuperGrok Heavy" {
		return
	}
	response, _, err := s.requestProviderAPICallAt(bounded, setup, settings, item, "POST", xaiWebBillingURL, map[string]string{
		"Authorization": "Bearer $TOKEN$", "Content-Type": "application/grpc-web-text+proto", "Accept": "application/grpc-web-text+proto", "x-grpc-web": "1", "Origin": "https://grok.com",
	}, "AAAAAAIIAA==")
	if err == nil && response.StatusCode == 200 && isXAIValidatedZero(response.BodyText, summary.PeriodStart, summary.PeriodEnd, time.Now()) {
		zero := float64(0)
		summary.UsagePercent = &zero
		summary.UsagePercentSource = "grpc-implicit-zero"
	}
}
