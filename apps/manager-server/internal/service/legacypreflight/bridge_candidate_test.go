package legacypreflight

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacyartifact"
)

func TestPlannedBridgeCandidateDoesNotIdentifyHistoricalSource(t *testing.T) {
	for _, fixture := range loadLegacyProfileFixtures(t) {
		t.Run(fixture.Ref, func(t *testing.T) {
			paths := buildLegacyProfileFixture(t, fixture)
			before := sourceState(t, filepath.Dir(paths.Database))
			report, err := Inspect(context.Background(), paths, Options{SourceStopped: true})
			if err != nil {
				t.Fatal(err)
			}
			candidate := report.PlannedBridgeCandidate
			if candidate == nil || candidate.Status != "planned" || candidate.Release != "v1.14.4" || candidate.SourceCommit != "166262365d506157b99968105c890f480cc7c07c" {
				t.Fatalf("missing fixed planned target: %+v", candidate)
			}
			if report.CompatibilityProfile.Family != fixture.Family || report.ExactSourceRelease.Status != "unknown" || !slices.Contains(report.Unknown, "exact-source-release") || !slices.Contains(report.Unknown, "forward-hop-evidence") || report.UpgradeAuthorized || startupAllowed(report, UpgradeStageNone) {
				t.Fatalf("planned target became source identity/authority: %+v", report)
			}
			if len(report.Blocked) != 0 || report.BridgeReadiness.Status != "unknown" {
				t.Fatal("planned target changed existing diagnostic disposition")
			}
			if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatal("planned target inspection changed source")
			}
		})
	}
}

func TestPlannedBridgeCandidateOmittedWithoutRecognizedLegacy(t *testing.T) {
	fixture := loadLegacyProfileFixtures(t)[6]
	for _, test := range []struct{ name, query, kind, status string }{
		{"partial", "alter table usage_events drop column request_id", "legacy-manager", "partial"},
		{"conflicting", "create table gateway_api_key_identities(id text primary key)", "legacy-manager", "conflicting"},
		{"unknown-shape", "alter table usage_events add column unreviewed text", "legacy-manager", "unknown"},
		{"unknown-source", "drop table settings; drop table usage_events", "unknown", "unknown"},
		{"fresh", "", "fresh", "not-applicable"},
		{"native", "", "native-v2", "not-applicable"},
		{"cancelled", "", "unknown", "unknown"},
		{"unsafe-source", "", "unknown", "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := fixturePaths(t)
			if test.name == "native" {
				db, err := sqlite.Open(paths.Database)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			} else if test.name != "fresh" {
				paths = buildLegacyProfileFixture(t, fixture)
			}
			if test.query != "" {
				execLegacyFixture(t, paths.Database, test.query)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.name == "cancelled" {
				cancel()
			}
			before := sourceState(t, filepath.Dir(paths.Database))
			report, err := Inspect(ctx, paths, Options{SourceStopped: test.name != "unsafe-source"})
			if err != nil && test.name != "cancelled" {
				t.Fatal(err)
			}
			if report.SourceKind != test.kind || report.CompatibilityProfile.Status != test.status || report.PlannedBridgeCandidate != nil || slices.Contains(report.Unknown, "forward-hop-evidence") || report.UpgradeAuthorized {
				t.Fatalf("unexpected target or existing classification: %+v", report)
			}
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(data, &object); err != nil {
				t.Fatal(err)
			}
			if _, exists := object["plannedBridgeCandidate"]; exists {
				t.Fatal("inapplicable candidate was serialized")
			}
			if startupAllowed(report, UpgradeStageNone) != (test.name == "fresh" || test.name == "native") {
				t.Fatal("planned target changed startup authority")
			}
			if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatal("omitted target inspection changed source")
			}
		})
	}
}

func TestCallerOwnedReceiptCannotMintProvenance(t *testing.T) {
	paths := buildLegacyProfileFixture(t, loadLegacyProfileFixtures(t)[6])
	receipt := &legacyartifact.ExecutionReceipt{
		Version: legacyartifact.ReceiptVersion, OperationID: "caller-owned",
		Release: legacyartifact.Release, SourceCommit: legacyartifact.SourceCommit,
		Asset:               "cpa-manager-plus_v1.14.4_darwin_arm64.tar.gz",
		ExecutableSHA256:    "a91ae73b46e0db6b953031ba94c6c4d404e26d438aca6854a8a7855e84011ecc",
		InputManifestDigest: "input", OutputManifestDigest: "output",
		InputFamily: "LegacySchemaA", OutputFamily: "LegacySchemaC", IssuedAtMS: 1500,
		ProcessEvidence: legacyartifact.ProcessEvidence{PID: 1234, StartedAtMS: 1000, FinishedAtMS: 1500, DurationMS: 500},
	}
	// HMAC bytes are public mathematics. Even a correctly self-signed receipt
	// plus caller-owned private files cannot establish a Manager owner.
	key := bytes.Repeat([]byte{42}, 32)
	payload, err := receipt.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	receipt.Signature = hex.EncodeToString(mac.Sum(nil))
	dir := filepath.Join(t.TempDir(), ".legacy-hop")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(dir, "receipt.json")
	canonicalDigest := sha256.Sum256(payload)
	record, err := json.Marshal([]map[string]any{{"operationId": receipt.OperationID, "receiptVersion": receipt.Version, "canonicalReceiptSHA256": hex.EncodeToString(canonicalDigest[:])}})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"authority.key": key, "journal.json": record, "receipt.json": data} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, opts := range []legacyartifact.ValidateOptions{{Receipt: receipt}, {ReceiptPath: receiptPath}} {
		result, err := legacyartifact.ValidateHop(context.Background(), opts)
		if !errors.Is(err, legacyartifact.ErrValidationNotQualified) || result != nil {
			t.Fatalf("caller minted evidence: %+v %v", result, err)
		}
	}
	for _, evidence := range []*legacyartifact.ValidatedHopEvidence{nil, {}} {
		report, err := Inspect(context.Background(), paths, Options{SourceStopped: true, ValidatedHop: evidence})
		if err != nil {
			t.Fatal(err)
		}
		if report.ForwardHopEvidence != nil || report.UpgradeAuthorized || startupAllowed(report, UpgradeStageNone) || !slices.Contains(report.Unknown, "forward-hop-evidence") {
			t.Fatalf("unqualified authority attached: %+v", report)
		}
	}
}
