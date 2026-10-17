package freshstrategy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

func TestRuntimeDestinationImplementationPinMatchesExecutionChain(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", "..", ".."))
	paths := []string{
		"apps/manager-server/internal/model/runtime.go",
		"apps/manager-server/internal/model/runtime_destination.go",
		"apps/manager-server/internal/service/runtime/destination.go",
		"apps/manager-server/internal/service/runtime/embedded.go",
		"apps/runtime-supervisor/cmd/cpamp-runtime-supervisor/runtime.go",
		"apps/runtime-supervisor/internal/lifecycle/destination.go",
		"apps/runtime-supervisor/internal/lifecycle/start.go",
		"apps/runtime-supervisor/internal/lifecycle/restart.go",
		"apps/runtime-supervisor/internal/lifecycle/recovery.go",
		"apps/runtime-supervisor/internal/protocol/destination.go",
		"apps/runtime-supervisor/internal/protocol/handler.go",
	}
	hash := sha256.New()
	for _, name := range paths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(hash, name+"\n")
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte("\n"))
	}
	pin, err := productionEvidence.ReadFile("qualification/runtime_destination_implementation.sha256")
	if err != nil {
		t.Fatal(err)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if strings.TrimSpace(string(pin)) != got {
		t.Fatalf("Runtime destination implementation pin=%s current=%s", strings.TrimSpace(string(pin)), got)
	}
}

func TestProductionRecordRemainsUnqualified(t *testing.T) {
	consumers, err := loadProductionQualification(productionEvidence)
	if err != nil || len(consumers) != 4 {
		t.Fatalf("compiled consumer record: %v", err)
	}
	for key, consumer := range consumers {
		if consumer.Qualification.ArtifactQualified || consumer.Qualification.RequiredProfileQualified || consumer.Qualification.StagingV8TransitionQualified || consumer.ArtifactID != "" {
			t.Fatalf("pending evidence promoted %v: %+v", key, consumer)
		}
	}
	data, err := productionEvidence.ReadFile("qualification/consumer.json")
	if err != nil {
		t.Fatal(err)
	}
	var record productionRecord
	if err = json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	for _, consumer := range record.Consumers {
		if consumer.Status != "pending" || consumer.Applicability != nil || len(consumer.Limitations) != 0 ||
			consumer.AdoptionEvidence != nil || consumer.LegacyTransition != nil {
			t.Fatalf("compiled consumer is not pending: %+v", consumer)
		}
	}
}

func TestProductionConstructorDiscardsInjectedQualification(t *testing.T) {
	var requests atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		t.Error("unqualified production consumer touched source")
	}))
	defer source.Close()
	runtime := &managedRuntime{}
	service := NewProduction(Options{Runtime: runtime, Qualification: bootstrap.CPAQualification{
		ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true, StagingV8TransitionQualified: true,
	}, QualifiedArtifactID: model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64)), AdoptionResources: acceptedKeys()})
	for _, item := range []struct {
		strategy bootstrap.CPAStrategy
		class    bootstrap.AdoptSourceClass
		blocker  string
	}{
		{bootstrap.NewManaged, "", "exact_v8_artifact_qualification_required"},
		{bootstrap.ConnectExternal, "", "required_v8_profile_qualification_required"},
		{bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting, "adoption_source_qualification_required"},
		{bootstrap.AdoptExisting, bootstrap.AdoptLegacyManaged, "adoption_source_qualification_required"},
	} {
		for attempt := 0; attempt < 2; attempt++ {
			result, err := service.Execute(context.Background(), operation(), Request{Strategy: item.strategy, AdoptSourceClass: item.class, BaseURL: source.URL, ManagementKey: "private-source-secret"})
			var blocker *Blocker
			if !errors.As(err, &blocker) || blocker.Code != item.blocker || result.Checkpoint.Strategy != "" {
				t.Fatalf("%s/%s: %+v %v", item.strategy, item.class, result, err)
			}
		}
	}
	if requests.Load() != 0 || len(runtime.starts) != 0 {
		t.Fatal("unqualified production strategy had effects")
	}
}

// This fixture is deliberately created only in test memory. Its synthetic
// review, report and trace are never embedded in a production binary.
func acceptedConsumerFixture(t *testing.T, consumerIndex int) (productionRecord, fstest.MapFS) {
	t.Helper()
	data, err := productionEvidence.ReadFile("qualification/consumer.json")
	if err != nil {
		t.Fatal(err)
	}
	var record productionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	record.ImplementationSHA256 = consumerImplementationSHA256()
	consumer := &record.Consumers[consumerIndex]
	consumer.Status = "accepted"
	consumer.AcceptanceSHA256 = storeSyntheticEvidence(files, []byte("Synthetic independent acceptance for loader tests only.\n"), ".md")
	key := consumerKey{consumer.Strategy, consumer.SourceClass}
	applicability, enforced := requiredConsumerApplicability(key)
	if !enforced {
		t.Fatalf("no enforced applicability for %v", key)
	}
	consumer.Applicability = &applicability
	consumer.Limitations = slices.Clone(requiredConsumerLimitations(key))
	for i := range consumer.Evidence {
		evidence := &consumer.Evidence[i]
		caseRef := "test-" + evidence.Profile
		evidence.CaseRefs = []string{caseRef}
		manifest := map[string]any{
			"schemaVersion": 1, "contractId": "cpamp-cpa-v8-qualification", "revision": "r-test",
			"candidate": map[string]any{"id": "candidate-test", "declaredSha256": record.ArchiveSHA256},
			"profiles":  []any{map[string]any{"id": evidence.Profile}},
			"cases":     []any{map[string]any{"id": caseRef, "profileRefs": []string{evidence.Profile}, "proofScope": "upstream-function"}},
		}
		evidence.ManifestSHA256 = storeSyntheticEvidence(files, encodeJSON(t, manifest), ".json")
		traceHash := storeSyntheticEvidence(files, encodeJSON(t, map[string]any{"schemaVersion": 1, "runId": "test-run", "caseRef": caseRef}), ".json")
		report := map[string]any{
			"schemaVersion": 1, "contractId": "cpamp-cpa-v8-qualification", "manifestRevision": "r-test", "manifestSha256": evidence.ManifestSHA256,
			"run":                 map[string]any{"id": "test-run", "runnerCommit": evidence.ImplementationHead},
			"artifactObservation": map[string]any{"candidateRef": "candidate-test", "provenance": "locally-verified", "observedArchiveSha256": record.ArchiveSHA256, "observedBinarySha256": record.BinarySHA256},
			"results":             []any{map[string]any{"caseRef": caseRef, "executionState": "completed", "assertionOutcome": "pass", "capability": "supported", "evidenceRefs": []string{"test-trace"}}},
			"evidence":            []any{map[string]any{"id": "test-trace", "kind": "sanitized-trace", "sha256": traceHash}},
		}
		evidence.ReportSHA256 = storeSyntheticEvidence(files, encodeJSON(t, report), ".json")
	}
	if consumer.SourceClass == bootstrap.AdoptLegacyManaged {
		attachSyntheticLegacyTransition(t, files, &record, consumerIndex, nil, nil)
	}
	writeSyntheticRecord(t, files, record)
	return record, files
}

type syntheticLegacyTransition struct {
	head        string
	manifest    legacyTransitionManifest
	report      evidenceReport
	observation legacyTransitionBinding
	binding     legacyTransitionEvidence
}

func syntheticLegacyTransitionDefaults(record *productionRecord) syntheticLegacyTransition {
	head := strings.Repeat("a", 40)
	return syntheticLegacyTransition{
		head: head,
		manifest: legacyTransitionManifest{
			SchemaVersion: 1,
			ContractID:    "cpamp-cpa-v8-qualification",
			Revision:      acceptedLegacyTransitionCaseRef,
			Candidate: evidenceCandidate{
				ID:             "candidate-test",
				DeclaredSHA256: record.ArchiveSHA256,
				ArtifactID:     string(record.ArtifactID),
			},
			Profiles: []evidenceProfile{{ID: "CPA8-STAGE-r1"}},
			Cases: []evidenceCase{{
				ID:          acceptedLegacyTransitionCaseRef,
				ProfileRefs: []string{"CPA8-STAGE-r1"},
				ProofScope:  "fresh-legacy-staging-transition",
			}},
			LegacySource: evidenceSource{
				Version:       cpa.AcceptedLegacyManagedVersion,
				ArchiveSHA256: cpa.AcceptedLegacyManagedArchiveSHA256,
				BinarySHA256:  cpa.AcceptedLegacyManagedBinarySHA256,
			},
		},
		report: evidenceReport{
			SchemaVersion: 1,
			ContractID:    "cpamp-cpa-v8-qualification",
			Run:           evidenceRun{ID: "test-legacy-transition-run", RunnerCommit: head},
			ArtifactObservation: evidenceArtifactObservation{
				CandidateRef:          "candidate-test",
				Provenance:            "locally-verified",
				ObservedArchiveSHA256: record.ArchiveSHA256,
				ObservedBinarySHA256:  record.BinarySHA256,
			},
			Results: []evidenceResult{{
				CaseRef:          acceptedLegacyTransitionCaseRef,
				ExecutionState:   "completed",
				AssertionOutcome: "pass",
				Capability:       "supported",
				EvidenceRefs:     []string{acceptedLegacyTransitionCaseRef},
			}},
			Evidence: []evidenceTraceRef{{ID: acceptedLegacyTransitionCaseRef, Kind: "sanitized-trace"}},
		},
		observation: legacyTransitionBinding{
			SchemaVersion:                1,
			CaseRef:                      acceptedLegacyTransitionCaseRef,
			SourceClass:                  string(bootstrap.AdoptLegacyManaged),
			LegacyVersion:                cpa.AcceptedLegacyManagedVersion,
			LegacyArchiveSHA256:          cpa.AcceptedLegacyManagedArchiveSHA256,
			LegacyBinarySHA256:           cpa.AcceptedLegacyManagedBinarySHA256,
			TargetArtifactID:             string(record.ArtifactID),
			TargetArchiveSHA256:          record.ArchiveSHA256,
			TargetBinarySHA256:           record.BinarySHA256,
			ConsumerImplementationSHA256: consumerImplementationSHA256(),
			SourcePreserved:              true,
			LegacyReadVerified:           true,
			StagingV8Verified:            true,
			DestinationVerified:          true,
			GatewayPending:               true,
		},
	}
}

// attachSyntheticLegacyTransition creates the separate manifest -> report ->
// case -> head -> observation chain in test memory only. It is never compiled
// authority; mutate rewrites content before content-addressing, while corrupt
// deletes or alters already-addressed bytes.
func attachSyntheticLegacyTransition(t *testing.T, files fstest.MapFS, record *productionRecord, index int, mutate func(*syntheticLegacyTransition), corrupt func(fstest.MapFS, syntheticLegacyTransition)) {
	t.Helper()
	s := syntheticLegacyTransitionDefaults(record)
	if mutate != nil {
		mutate(&s)
	}
	observationData := encodeJSON(t, s.observation)
	if s.binding.EvidenceSHA256 == "" {
		s.binding.EvidenceSHA256 = sha256Hex(observationData)
	}
	if len(s.report.Evidence) > 0 && s.report.Evidence[0].SHA256 == "" {
		s.report.Evidence[0].SHA256 = s.binding.EvidenceSHA256
	}
	manifestData := encodeJSON(t, s.manifest)
	if s.binding.ManifestSHA256 == "" {
		s.binding.ManifestSHA256 = sha256Hex(manifestData)
	}
	if s.report.ManifestSHA256 == "" {
		s.report.ManifestSHA256 = s.binding.ManifestSHA256
	}
	if s.report.ManifestRevision == "" {
		s.report.ManifestRevision = s.manifest.Revision
	}
	reportData := encodeJSON(t, s.report)
	if s.binding.ReportSHA256 == "" {
		s.binding.ReportSHA256 = sha256Hex(reportData)
	}
	if s.binding.Profile == "" {
		s.binding.Profile = "CPA8-STAGE-r1"
	}
	if s.binding.CaseRef == "" {
		s.binding.CaseRef = acceptedLegacyTransitionCaseRef
	}
	if s.binding.ImplementationHead == "" {
		s.binding.ImplementationHead = s.head
	}
	if s.binding.LegacyVersion == "" {
		s.binding.LegacyVersion = cpa.AcceptedLegacyManagedVersion
	}
	if s.binding.LegacyArchiveSHA256 == "" {
		s.binding.LegacyArchiveSHA256 = cpa.AcceptedLegacyManagedArchiveSHA256
	}
	if s.binding.LegacyBinarySHA256 == "" {
		s.binding.LegacyBinarySHA256 = cpa.AcceptedLegacyManagedBinarySHA256
	}
	files["qualification/evidence/"+s.binding.ManifestSHA256+".json"] = &fstest.MapFile{Data: manifestData}
	files["qualification/evidence/"+s.binding.ReportSHA256+".json"] = &fstest.MapFile{Data: reportData}
	files["qualification/evidence/"+s.binding.EvidenceSHA256+".json"] = &fstest.MapFile{Data: observationData}
	if corrupt != nil {
		corrupt(files, s)
	}
	record.Consumers[index].LegacyTransition = &s.binding
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func encodeJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func writeSyntheticRecord(t *testing.T, files fstest.MapFS, record productionRecord) {
	t.Helper()
	files["qualification/consumer.json"] = &fstest.MapFile{Data: encodeJSON(t, record)}
}

func storeSyntheticEvidence(files fstest.MapFS, data []byte, extension string) string {
	digest := sha256.Sum256(data)
	id := hex.EncodeToString(digest[:])
	files["qualification/evidence/"+id+extension] = &fstest.MapFile{Data: data}
	return id
}

func TestProductionLoaderRejectsMalformedAuthority(t *testing.T) {
	base, err := fs.ReadFile(productionEvidence, "qualification/consumer.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"missing":       nil,
		"invalid":       []byte("{"),
		"unknown":       bytes.Replace(base, []byte(`"schemaVersion": 1,`), []byte(`"schemaVersion": 1, "unknown": true,`), 1),
		"duplicate":     bytes.Replace(base, []byte(`"schemaVersion": 1,`), []byte(`"schemaVersion": 1, "schemaVersion": 1,`), 1),
		"missing field": bytes.Replace(base, []byte("  \"managementGeneration\": \"v8\",\n"), nil, 1),
		"case folded":   bytes.Replace(base, []byte("schemaVersion"), []byte("SchemaVersion"), 1),
		"trailing":      append(append([]byte{}, base...), []byte("{}")...),
	} {
		t.Run(name, func(t *testing.T) {
			files := fstest.MapFS{}
			if data != nil {
				if name != "invalid" && name != "trailing" && bytes.Equal(base, data) {
					t.Fatal("malformed control did not change source")
				}
				files["qualification/consumer.json"] = &fstest.MapFile{Data: data}
			}
			if consumers, err := loadProductionQualification(files); err == nil || consumers != nil {
				t.Fatal("malformed authority granted qualification")
			}
		})
	}
}

func TestProductionLoaderBindsArtifactProfileEvidenceAndHead(t *testing.T) {
	for name, mutate := range map[string]func(*productionRecord, fstest.MapFS){
		"consumer implementation drift": func(r *productionRecord, _ fstest.MapFS) { r.ImplementationSHA256 = strings.Repeat("a", 64) },
		"artifact identity": func(r *productionRecord, _ fstest.MapFS) {
			r.ArtifactID = model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
		},
		"archive digest": func(r *productionRecord, _ fstest.MapFS) { r.ArchiveSHA256 = strings.Repeat("a", 64) },
		"binary digest": func(r *productionRecord, _ fstest.MapFS) {
			r.BinarySHA256 = strings.Repeat("a", 64)
			r.ArtifactID = model.RuntimeArtifactID("sha256:" + r.BinarySHA256)
		},
		"profile": func(r *productionRecord, _ fstest.MapFS) { r.Consumers[0].Evidence[0].Profile = "CPA8-STAGE-r1" },
		"execution head": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Evidence[0].ImplementationHead = strings.Repeat("a", 40)
		},
		"empty cases": func(r *productionRecord, _ fstest.MapFS) { r.Consumers[0].Evidence[0].CaseRefs = []string{} },
		"different case": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Evidence[0].CaseRefs = []string{"not-observed"}
		},
		"duplicate case": func(r *productionRecord, _ fstest.MapFS) {
			e := &r.Consumers[0].Evidence[0]
			e.CaseRefs = append(e.CaseRefs, e.CaseRefs[0])
		},
		"missing acceptance":   func(r *productionRecord, _ fstest.MapFS) { r.Consumers[0].AcceptanceSHA256 = "" },
		"missing limitations":  func(r *productionRecord, _ fstest.MapFS) { r.Consumers[0].Limitations = []string{} },
		"missing invalidation": func(r *productionRecord, _ fstest.MapFS) { r.Consumers[0].InvalidatedBy = []string{} },
		"missing applicability": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Applicability = nil
		},
		"applicability target": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Applicability.Target = applicabilityTargetBoundedV8Read
		},
		"applicability source transport": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Applicability.SourceTransport = applicabilitySourceV8Read
		},
		"applicability destination transport": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Applicability.DestinationTransport = applicabilityDestinationExternal
		},
		"applicability config": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Applicability.Config = applicabilityConfigLegacyToV8
		},
		"applicability mode": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Applicability.Mode = applicabilityModeAdoptNetwork
		},
		"limitations free text": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Limitations = append(r.Consumers[0].Limitations, "only supported on linux amd64")
		},
		"limitations duplicate": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Limitations = append(r.Consumers[0].Limitations, r.Consumers[0].Limitations[0])
		},
		"limitations incomplete": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Limitations = r.Consumers[0].Limitations[1:]
		},
		"limitations unenforceable": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[0].Limitations = []string{limitationGatewayPending, "private_network_targets_only"}
		},
		"duplicate consumer":     func(r *productionRecord, _ fstest.MapFS) { r.Consumers[1] = r.Consumers[0] },
		"missing legacy profile": func(r *productionRecord, _ fstest.MapFS) { r.Consumers[3].Evidence = r.Consumers[3].Evidence[1:] },
		"missing original manifest": func(r *productionRecord, f fstest.MapFS) {
			delete(f, "qualification/evidence/"+r.Consumers[0].Evidence[0].ManifestSHA256+".json")
		},
		"report drift": func(r *productionRecord, f fstest.MapFS) {
			f["qualification/evidence/"+r.Consumers[0].Evidence[0].ReportSHA256+".json"].Data = []byte("{}")
		},
		"review drift": func(r *productionRecord, f fstest.MapFS) {
			f["qualification/evidence/"+r.Consumers[0].AcceptanceSHA256+".md"].Data = []byte("different review")
		},
		"trace missing": func(r *productionRecord, f fstest.MapFS) {
			var report struct{ Evidence []struct{ SHA256 string } }
			_ = json.Unmarshal(f["qualification/evidence/"+r.Consumers[0].Evidence[0].ReportSHA256+".json"].Data, &report)
			delete(f, "qualification/evidence/"+report.Evidence[0].SHA256+".json")
		},
	} {
		t.Run(name, func(t *testing.T) {
			record, files := acceptedConsumerFixture(t, 0)
			if consumers, err := loadProductionQualification(files); err != nil || !consumers[consumerKey{bootstrap.NewManaged, ""}].Qualification.ArtifactQualified {
				t.Fatalf("positive synthetic control: %v", err)
			}
			mutate(&record, files)
			writeSyntheticRecord(t, files, record)
			if consumers, err := loadProductionQualification(files); err == nil || consumers != nil {
				t.Fatal("mismatched evidence granted qualification")
			}
		})
	}
}

func TestProductionLoaderRequiresIndependentLegacyTransitionBinding(t *testing.T) {
	legacy := consumerKey{bootstrap.AdoptExisting, bootstrap.AdoptLegacyManaged}
	network := consumerKey{bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting}
	_, files := acceptedConsumerFixture(t, 3)
	consumers, err := loadProductionQualification(files)
	if err != nil || !consumers[legacy].Qualification.StagingV8TransitionQualified || consumers[legacy].Qualification.ValidateForStrategy(legacy.Strategy, legacy.Class) != nil {
		t.Fatalf("positive synthetic legacy transition control: %v", err)
	}
	_, files = acceptedConsumerFixture(t, 2)
	consumers, err = loadProductionQualification(files)
	if err != nil || consumers[network].Qualification.StagingV8TransitionQualified || consumers[network].Qualification.ValidateForStrategy(network.Strategy, network.Class) != nil {
		t.Fatalf("network adoption must not require or gain a legacy transition: %v", err)
	}
	chain := func(mutate func(*syntheticLegacyTransition), corrupt func(fstest.MapFS, syntheticLegacyTransition)) func(*productionRecord, fstest.MapFS) {
		return func(record *productionRecord, files fstest.MapFS) {
			attachSyntheticLegacyTransition(t, files, record, 3, mutate, corrupt)
		}
	}
	corruptBytes := func(pick func(syntheticLegacyTransition) string) func(fstest.MapFS, syntheticLegacyTransition) {
		return func(files fstest.MapFS, s syntheticLegacyTransition) {
			files["qualification/evidence/"+pick(s)+".json"].Data = []byte("{}\n")
		}
	}
	deleteEntry := func(pick func(syntheticLegacyTransition) string) func(fstest.MapFS, syntheticLegacyTransition) {
		return func(files fstest.MapFS, s syntheticLegacyTransition) {
			delete(files, "qualification/evidence/"+pick(s)+".json")
		}
	}
	for name, mutate := range map[string]func(*productionRecord, fstest.MapFS){
		"legacy accepted without transition binding": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[3].LegacyTransition = nil
		},
		"network STAGE evidence reused without binding": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[3].LegacyTransition = nil
		},
		"transition carried by network consumer": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[2] = r.Consumers[3]
		},
		"transition on pending legacy": func(r *productionRecord, _ fstest.MapFS) {
			r.Consumers[3].Status = "pending"
			r.Consumers[3].AcceptanceSHA256 = ""
		},
		"consumer implementation drift": func(r *productionRecord, _ fstest.MapFS) {
			r.ImplementationSHA256 = strings.Repeat("a", 64)
		},
		"bootstrap semantic digest drift": func(r *productionRecord, _ fstest.MapFS) {
			r.ImplementationSHA256 = consumerImplementationDigest(cpa.SafeReadImplementationSHA256(), strings.Repeat("b", 64))
		},
		"safe-read semantic digest drift": func(r *productionRecord, _ fstest.MapFS) {
			r.ImplementationSHA256 = consumerImplementationDigest(strings.Repeat("b", 64), bootstrap.FreshStrategyImplementationSHA256())
		},
		"transition profile":                   chain(func(s *syntheticLegacyTransition) { s.binding.Profile = "CPA8-EXTERNAL-SAFETY-r1" }, nil),
		"transition case ref mismatch":         chain(func(s *syntheticLegacyTransition) { s.binding.CaseRef = "other-case" }, nil),
		"transition manifest digest malformed": chain(func(s *syntheticLegacyTransition) { s.binding.ManifestSHA256 = "not-a-digest" }, nil),
		"transition report digest malformed":   chain(func(s *syntheticLegacyTransition) { s.binding.ReportSHA256 = "not-a-digest" }, nil),
		"transition evidence digest malformed": chain(func(s *syntheticLegacyTransition) { s.binding.EvidenceSHA256 = "not-a-digest" }, nil),
		"transition implementation head malformed": chain(func(s *syntheticLegacyTransition) {
			s.binding.ImplementationHead = "not-a-head"
		}, nil),
		"transition legacy version":             chain(func(s *syntheticLegacyTransition) { s.binding.LegacyVersion = "v7.0.13" }, nil),
		"transition legacy archive":             chain(func(s *syntheticLegacyTransition) { s.binding.LegacyArchiveSHA256 = strings.Repeat("a", 64) }, nil),
		"transition legacy binary":              chain(func(s *syntheticLegacyTransition) { s.binding.LegacyBinarySHA256 = strings.Repeat("a", 64) }, nil),
		"transition source class":               chain(func(s *syntheticLegacyTransition) { s.observation.SourceClass = string(bootstrap.AdoptNetworkExisting) }, nil),
		"transition observation case ref":       chain(func(s *syntheticLegacyTransition) { s.observation.CaseRef = "other-case" }, nil),
		"transition observation schema":         chain(func(s *syntheticLegacyTransition) { s.observation.SchemaVersion = 2 }, nil),
		"transition observation legacy version": chain(func(s *syntheticLegacyTransition) { s.observation.LegacyVersion = "v7.0.13" }, nil),
		"transition observation legacy archive": chain(func(s *syntheticLegacyTransition) { s.observation.LegacyArchiveSHA256 = strings.Repeat("a", 64) }, nil),
		"transition observation legacy binary":  chain(func(s *syntheticLegacyTransition) { s.observation.LegacyBinarySHA256 = strings.Repeat("a", 64) }, nil),
		"transition artifact": chain(func(s *syntheticLegacyTransition) {
			s.observation.TargetArtifactID = "sha256:" + strings.Repeat("a", 64)
		}, nil),
		"transition archive": chain(func(s *syntheticLegacyTransition) { s.observation.TargetArchiveSHA256 = strings.Repeat("a", 64) }, nil),
		"transition binary":  chain(func(s *syntheticLegacyTransition) { s.observation.TargetBinarySHA256 = strings.Repeat("a", 64) }, nil),
		"transition consumer implementation": chain(func(s *syntheticLegacyTransition) {
			s.observation.ConsumerImplementationSHA256 = strings.Repeat("a", 64)
		}, nil),
		"transition source not preserved":   chain(func(s *syntheticLegacyTransition) { s.observation.SourcePreserved = false }, nil),
		"transition legacy read unverified": chain(func(s *syntheticLegacyTransition) { s.observation.LegacyReadVerified = false }, nil),
		"transition staging unverified":     chain(func(s *syntheticLegacyTransition) { s.observation.StagingV8Verified = false }, nil),
		"transition destination unverified": chain(func(s *syntheticLegacyTransition) { s.observation.DestinationVerified = false }, nil),
		"transition gateway not pending":    chain(func(s *syntheticLegacyTransition) { s.observation.GatewayPending = false }, nil),
		"manifest missing":                  chain(nil, deleteEntry(func(s syntheticLegacyTransition) string { return s.binding.ManifestSHA256 })),
		"manifest digest mismatch":          chain(nil, corruptBytes(func(s syntheticLegacyTransition) string { return s.binding.ManifestSHA256 })),
		"manifest schema":                   chain(func(s *syntheticLegacyTransition) { s.manifest.SchemaVersion = 2 }, nil),
		"manifest contract":                 chain(func(s *syntheticLegacyTransition) { s.manifest.ContractID = "other-contract" }, nil),
		"manifest revision missing":         chain(func(s *syntheticLegacyTransition) { s.manifest.Revision = "" }, nil),
		"manifest candidate archive":        chain(func(s *syntheticLegacyTransition) { s.manifest.Candidate.DeclaredSHA256 = strings.Repeat("a", 64) }, nil),
		"manifest candidate artifact": chain(func(s *syntheticLegacyTransition) {
			s.manifest.Candidate.ArtifactID = "sha256:" + strings.Repeat("a", 64)
		}, nil),
		"manifest profile missing": chain(func(s *syntheticLegacyTransition) { s.manifest.Profiles = nil }, nil),
		"manifest case missing":    chain(func(s *syntheticLegacyTransition) { s.manifest.Cases = nil }, nil),
		"manifest case profile mismatch": chain(func(s *syntheticLegacyTransition) {
			s.manifest.Cases[0].ProfileRefs = []string{"CPA8-EXTERNAL-SAFETY-r1"}
		}, nil),
		"manifest case proof scope missing": chain(func(s *syntheticLegacyTransition) { s.manifest.Cases[0].ProofScope = "" }, nil),
		"manifest legacy version":           chain(func(s *syntheticLegacyTransition) { s.manifest.LegacySource.Version = "v7.0.13" }, nil),
		"manifest legacy archive":           chain(func(s *syntheticLegacyTransition) { s.manifest.LegacySource.ArchiveSHA256 = strings.Repeat("a", 64) }, nil),
		"manifest legacy binary":            chain(func(s *syntheticLegacyTransition) { s.manifest.LegacySource.BinarySHA256 = strings.Repeat("a", 64) }, nil),
		"report missing":                    chain(nil, deleteEntry(func(s syntheticLegacyTransition) string { return s.binding.ReportSHA256 })),
		"report digest mismatch":            chain(nil, corruptBytes(func(s syntheticLegacyTransition) string { return s.binding.ReportSHA256 })),
		"report manifest mismatch":          chain(func(s *syntheticLegacyTransition) { s.report.ManifestSHA256 = strings.Repeat("b", 64) }, nil),
		"report manifest revision":          chain(func(s *syntheticLegacyTransition) { s.report.ManifestRevision = "other-revision" }, nil),
		"report runner head mismatch":       chain(func(s *syntheticLegacyTransition) { s.report.Run.RunnerCommit = strings.Repeat("b", 40) }, nil),
		"report run id missing":             chain(func(s *syntheticLegacyTransition) { s.report.Run.ID = "" }, nil),
		"report case missing":               chain(func(s *syntheticLegacyTransition) { s.report.Results[0].CaseRef = "other-case" }, nil),
		"report execution state":            chain(func(s *syntheticLegacyTransition) { s.report.Results[0].ExecutionState = "running" }, nil),
		"report assertion outcome":          chain(func(s *syntheticLegacyTransition) { s.report.Results[0].AssertionOutcome = "fail" }, nil),
		"report capability":                 chain(func(s *syntheticLegacyTransition) { s.report.Results[0].Capability = "unknown" }, nil),
		"report evidence refs missing":      chain(func(s *syntheticLegacyTransition) { s.report.Results[0].EvidenceRefs = nil }, nil),
		"report evidence link missing":      chain(func(s *syntheticLegacyTransition) { s.report.Evidence = nil }, nil),
		"report artifact candidate":         chain(func(s *syntheticLegacyTransition) { s.report.ArtifactObservation.CandidateRef = "other" }, nil),
		"report provenance":                 chain(func(s *syntheticLegacyTransition) { s.report.ArtifactObservation.Provenance = "unverified" }, nil),
		"report artifact archive": chain(func(s *syntheticLegacyTransition) {
			s.report.ArtifactObservation.ObservedArchiveSHA256 = strings.Repeat("a", 64)
		}, nil),
		"report artifact binary": chain(func(s *syntheticLegacyTransition) {
			s.report.ArtifactObservation.ObservedBinarySHA256 = strings.Repeat("a", 64)
		}, nil),
		"observation missing":         chain(nil, deleteEntry(func(s syntheticLegacyTransition) string { return s.binding.EvidenceSHA256 })),
		"observation digest mismatch": chain(nil, corruptBytes(func(s syntheticLegacyTransition) string { return s.binding.EvidenceSHA256 })),
	} {
		t.Run(name, func(t *testing.T) {
			record, files := acceptedConsumerFixture(t, 3)
			if consumers, err := loadProductionQualification(files); err != nil || !consumers[legacy].Qualification.StagingV8TransitionQualified {
				t.Fatalf("positive synthetic control: %v", err)
			}
			mutate(&record, files)
			writeSyntheticRecord(t, files, record)
			if consumers, err := loadProductionQualification(files); err == nil || consumers != nil {
				t.Fatal("missing or mismatched legacy transition granted qualification")
			}
		})
	}
}

func TestProductionConsumerQualificationDoesNotCrossStrategiesOrSourceClasses(t *testing.T) {
	keys := []consumerKey{{bootstrap.NewManaged, ""}, {bootstrap.ConnectExternal, ""}, {bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting}, {bootstrap.AdoptExisting, bootstrap.AdoptLegacyManaged}}
	for i, key := range keys {
		t.Run(string(key.Strategy)+"/"+string(key.Class), func(t *testing.T) {
			record, files := acceptedConsumerFixture(t, i)
			consumers, err := loadProductionQualification(files)
			if err != nil {
				t.Fatal(err)
			}
			for _, other := range keys {
				consumer := consumers[other]
				accepted := consumer.Qualification.ValidateForStrategy(other.Strategy, other.Class) == nil
				if accepted != (other == key) {
					t.Fatalf("qualification crossed consumers: accepted %v from %v", other, key)
				}
				if other != key {
					service := &Service{options: Options{Qualification: qualified()}, production: true, consumers: consumers}
					result, err := service.Execute(context.Background(), operation(), Request{Strategy: other.Strategy, AdoptSourceClass: other.Class, BaseURL: "invalid-source"})
					var blocker *Blocker
					want := "adoption_source_qualification_required"
					if other.Strategy == bootstrap.NewManaged {
						want = "exact_v8_artifact_qualification_required"
					}
					if other.Strategy == bootstrap.ConnectExternal {
						want = "required_v8_profile_qualification_required"
					}
					if !errors.As(err, &blocker) || blocker.Code != want || result.Checkpoint.Strategy != "" {
						t.Fatalf("execution crossed consumers: %v from %v: %+v %v", other, key, result, err)
					}
				}
			}
			// An unchanged PASS report with acceptance withdrawn grants nothing.
			record.Consumers[i].Status = "pending"
			record.Consumers[i].AcceptanceSHA256 = ""
			record.Consumers[i].LegacyTransition = nil
			record.Consumers[i].Applicability = nil
			record.Consumers[i].Limitations = []string{}
			writeSyntheticRecord(t, files, record)
			consumers, err = loadProductionQualification(files)
			if err != nil || consumers[key].Qualification.ValidateForStrategy(key.Strategy, key.Class) == nil {
				t.Fatalf("AQ PASS restored withdrawn acceptance: %v", err)
			}
		})
	}
}

func TestLoadedArtifactUsesExistingExactRuntimeFence(t *testing.T) {
	record, files := acceptedConsumerFixture(t, 0)
	consumers, err := loadProductionQualification(files)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &managedRuntime{status: model.RuntimeObservedStatus{Identity: "destination", Generation: 7, ProtocolVersion: "v1", State: model.RuntimeStateOffline,
		Capabilities: model.RuntimeCapabilities{model.RuntimeCapabilityStart}, ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: record.ArtifactID}}}
	service := &Service{options: Options{Runtime: runtime}, production: true, consumers: consumers}
	op := operation()
	request := Request{Strategy: bootstrap.NewManaged, ExpectedRuntimeIdentity: "destination", ExpectedRuntimeGeneration: 7, ExpectedArtifactID: record.ArtifactID}
	staged, err := service.Execute(context.Background(), op, request)
	if err != nil || staged.Checkpoint.Stage != "staged" || len(runtime.starts) != 0 {
		t.Fatalf("stage: %+v %v", staged, err)
	}
	op.Strategy = staged.Checkpoint
	committed, err := service.Execute(context.Background(), op, request)
	if err != nil || committed.Checkpoint.Stage != "committed" || len(runtime.starts) != 1 {
		t.Fatalf("commit: %+v %v", committed, err)
	}
	runtime.status.ActiveGatewayArtifact.ArtifactID = model.RuntimeArtifactID("sha256:" + strings.Repeat("c", 64))
	before := op
	if result, err := service.Execute(context.Background(), op, request); err == nil || result.Checkpoint.Stage == "committed" || len(runtime.starts) != 1 || !reflect.DeepEqual(op, before) {
		t.Fatal("changed runtime artifact accepted")
	}
}

func TestPendingConsumerCannotClaimApplicabilityOrLimitations(t *testing.T) {
	data, err := productionEvidence.ReadFile("qualification/consumer.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*productionRecord){
		"applicability": func(r *productionRecord) {
			applicability, _ := requiredConsumerApplicability(consumerKey{bootstrap.NewManaged, bootstrap.AdoptSourceNone})
			r.Consumers[0].Applicability = &applicability
		},
		"limitations": func(r *productionRecord) {
			r.Consumers[0].Limitations = requiredConsumerLimitations(consumerKey{bootstrap.NewManaged, bootstrap.AdoptSourceNone})
		},
	} {
		t.Run(name, func(t *testing.T) {
			var record productionRecord
			if json.Unmarshal(data, &record) != nil {
				t.Fatal("compiled pending record is not decodable")
			}
			mutate(&record)
			files := fstest.MapFS{"qualification/consumer.json": &fstest.MapFile{Data: encodeJSON(t, record)}}
			if consumers, err := loadProductionQualification(files); err == nil || consumers != nil {
				t.Fatal("pending consumer claimed accepted applicability")
			}
		})
	}
}

func TestAcceptedConsumerScopeCannotCrossModesOrTargets(t *testing.T) {
	scenarios := []struct {
		name    string
		fixture func(*testing.T, int) (productionRecord, fstest.MapFS)
		index   int
		request func(string) Request
		blocker string
	}{
		{
			name: "new_managed_cannot_connect_external", fixture: acceptedConsumerFixture, index: 0,
			request: func(source string) Request {
				return Request{Strategy: bootstrap.ConnectExternal, BaseURL: source, ManagementKey: "private-source"}
			},
			blocker: "required_v8_profile_qualification_required",
		},
		{
			name: "connect_external_cannot_adopt_network", fixture: acceptedConsumerFixture, index: 1,
			request: func(source string) Request {
				return Request{Strategy: bootstrap.AdoptExisting, AdoptSourceClass: bootstrap.AdoptNetworkExisting, BaseURL: source, ManagementKey: "private-source"}
			},
			blocker: "adoption_source_qualification_required",
		},
		{
			name: "connect_external_cannot_adopt_legacy", fixture: acceptedConsumerFixture, index: 1,
			request: func(source string) Request {
				return Request{Strategy: bootstrap.AdoptExisting, AdoptSourceClass: bootstrap.AdoptLegacyManaged, BaseURL: source, ManagementKey: "private-source"}
			},
			blocker: "adoption_source_qualification_required",
		},
		{
			name: "network_cannot_adopt_legacy", fixture: acceptedConsumerFixture, index: 2,
			request: func(source string) Request {
				return Request{Strategy: bootstrap.AdoptExisting, AdoptSourceClass: bootstrap.AdoptLegacyManaged, BaseURL: source, ManagementKey: "private-source"}
			},
			blocker: "adoption_source_qualification_required",
		},
		{
			name: "legacy_cannot_adopt_network", fixture: acceptedConsumerFixture, index: 3,
			request: func(source string) Request {
				return Request{Strategy: bootstrap.AdoptExisting, AdoptSourceClass: bootstrap.AdoptNetworkExisting, BaseURL: source, ManagementKey: "private-source"}
			},
			blocker: "adoption_source_qualification_required",
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			record, files := scenario.fixture(t, scenario.index)
			consumers, err := loadProductionQualification(files)
			if err != nil {
				t.Fatalf("accepted fixture: %v", err)
			}
			var sourceCalls atomic.Int32
			source := newScopeTestServer(t, &sourceCalls)
			runtime, target := newScopeRuntime(t, record.ArtifactID)
			protector, _ := security.NewProtector([]byte("test-only-applicability-key"))
			service := &Service{options: Options{Runtime: runtime, Protector: protector, DestinationBaseURL: target.URL}, production: true, consumers: consumers}
			request := scenario.request(source.URL)
			request.ExpectedRuntimeIdentity, request.ExpectedRuntimeGeneration, request.ExpectedArtifactID = runtime.status.Identity, runtime.status.Generation, record.ArtifactID
			assertScopeBlocked(t, service, request, &sourceCalls, runtime, scenario.blocker)
		})
	}
	// A matching mode with a mismatched target artifact must fail before the
	// source read, destination staging or Runtime mutation.
	t.Run("adoption_target_mismatch", func(t *testing.T) {
		record, files := resourceConsumerFixture(t, 2)
		consumers, err := loadProductionQualification(files)
		if err != nil {
			t.Fatalf("accepted fixture: %v", err)
		}
		var sourceCalls atomic.Int32
		source := newScopeTestServer(t, &sourceCalls)
		runtime, target := newScopeRuntime(t, record.ArtifactID)
		protector, _ := security.NewProtector([]byte("test-only-applicability-key"))
		service := &Service{options: Options{Runtime: runtime, Protector: protector, DestinationBaseURL: target.URL}, production: true, consumers: consumers}
		request := Request{Strategy: bootstrap.AdoptExisting, AdoptSourceClass: bootstrap.AdoptNetworkExisting,
			BaseURL: source.URL, ManagementKey: "private-source",
			ExpectedRuntimeIdentity: runtime.status.Identity, ExpectedRuntimeGeneration: runtime.status.Generation,
			ExpectedArtifactID: model.RuntimeArtifactID("sha256:" + strings.Repeat("b", 64))}
		assertScopeBlocked(t, service, request, &sourceCalls, runtime, "managed_target_identity_mismatch")
	})
}

func TestPersistedAdoptionOperationRechecksApplicability(t *testing.T) {
	record, files := resourceConsumerFixture(t, 2)
	consumers, err := loadProductionQualification(files)
	if err != nil {
		t.Fatalf("accepted fixture: %v", err)
	}
	var sourceCalls atomic.Int32
	source := newScopeTestServer(t, &sourceCalls)
	runtime, target := newScopeRuntime(t, record.ArtifactID)
	protector, _ := security.NewProtector([]byte("test-only-applicability-key"))
	options := Options{Runtime: runtime, Protector: protector, DestinationBaseURL: target.URL}
	request := Request{Strategy: bootstrap.AdoptExisting, AdoptSourceClass: bootstrap.AdoptNetworkExisting,
		BaseURL: source.URL, ManagementKey: "private-source",
		ExpectedRuntimeIdentity: runtime.status.Identity, ExpectedRuntimeGeneration: runtime.status.Generation,
		ExpectedArtifactID: record.ArtifactID}
	service := &Service{options: options, production: true, consumers: consumers}
	staged, err := service.Execute(t.Context(), operation(), request)
	if err != nil || staged.Checkpoint.Stage != "staged" || sourceCalls.Load() != 1 || len(runtime.stages) != 0 || len(runtime.starts) != 0 {
		t.Fatalf("stage: %+v %v source=%d", staged, err, sourceCalls.Load())
	}
	op := operation()
	op.Strategy = staged.Checkpoint
	request.AdoptionCiphertext = staged.AdoptionCiphertext
	// A restart that no longer grants this consumer must re-check the persisted
	// operation instead of trusting its staged checkpoint.
	withdrawn := make(map[consumerKey]qualifiedConsumer)
	restarted := &Service{options: options, production: true, consumers: withdrawn}
	result, err := restarted.Execute(t.Context(), op, request)
	var blocker *Blocker
	if !errors.As(err, &blocker) || blocker.Code != "adoption_source_qualification_required" || result.Checkpoint.Strategy != "" ||
		sourceCalls.Load() != 1 || len(runtime.stages) != 0 || len(runtime.starts) != 0 {
		t.Fatalf("persisted scope bypassed recheck: result=%+v err=%v source=%d", result, err, sourceCalls.Load())
	}
}

func newScopeTestServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v8/management/config":
			_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":["source-client"]}}`))
		case "/v0/management/config":
			w.Header().Set("X-CPA-VERSION", "7.0.12")
			_, _ = w.Write([]byte(`{"tls":{"enable":false},"api-keys":["source-client"]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func newScopeRuntime(t *testing.T, artifact model.RuntimeArtifactID) (*adoptionRuntime, *httptest.Server) {
	t.Helper()
	runtime := &adoptionRuntime{managedRuntime: managedRuntime{status: model.RuntimeObservedStatus{
		Identity: "destination-runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady,
		Capabilities:          model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination},
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact},
	}}}
	target := httptest.NewServer(http.HandlerFunc(runtime.target))
	t.Cleanup(target.Close)
	return runtime, target
}

func assertScopeBlocked(t *testing.T, service *Service, request Request, sourceCalls *atomic.Int32, runtime *adoptionRuntime, want string) {
	t.Helper()
	result, err := service.Execute(t.Context(), operation(), request)
	var blocker *Blocker
	if !errors.As(err, &blocker) || blocker.Code != want || result.Checkpoint.Strategy != "" ||
		sourceCalls.Load() != 0 || len(runtime.stages) != 0 || len(runtime.starts) != 0 {
		t.Fatalf("scope mismatch %s: result=%+v err=%v source=%d stage=%d start=%d", want, result, err, sourceCalls.Load(), len(runtime.stages), len(runtime.starts))
	}
}

func TestProductionLoaderRequiresExactApplicabilityForEveryConsumer(t *testing.T) {
	alternatives := map[string][]string{
		"target":               {applicabilityTargetExactV8Artifact, applicabilityTargetBoundedV8Read, applicabilityTargetManagerOwnedV8Destination},
		"sourceTransport":      {applicabilitySourceNone, applicabilitySourceV8Read, applicabilitySourceLegacyRead},
		"destinationTransport": {applicabilityDestinationExternal, applicabilityDestinationRuntimeV1},
		"config":               {applicabilityConfigV8, applicabilityConfigLegacyToV8},
		"mode":                 {applicabilityModeNewManaged, applicabilityModeExternal, applicabilityModeAdoptNetwork, applicabilityModeAdoptLegacy},
	}
	other := func(values []string, expected string) string {
		for _, value := range values {
			if value != expected {
				return value
			}
		}
		return expected + "-unexpected"
	}
	for index := 0; index < 4; index++ {
		for field, values := range alternatives {
			t.Run(fmt.Sprintf("consumer-%d/%s", index, field), func(t *testing.T) {
				record, files := acceptedConsumerFixture(t, index)
				consumer := &record.Consumers[index]
				switch field {
				case "target":
					consumer.Applicability.Target = other(values, consumer.Applicability.Target)
				case "sourceTransport":
					consumer.Applicability.SourceTransport = other(values, consumer.Applicability.SourceTransport)
				case "destinationTransport":
					consumer.Applicability.DestinationTransport = other(values, consumer.Applicability.DestinationTransport)
				case "config":
					consumer.Applicability.Config = other(values, consumer.Applicability.Config)
				case "mode":
					consumer.Applicability.Mode = other(values, consumer.Applicability.Mode)
				}
				writeSyntheticRecord(t, files, record)
				if consumers, err := loadProductionQualification(files); err == nil || consumers != nil {
					t.Fatalf("consumer %d accepted a mismatched %s scope", index, field)
				}
			})
		}
		t.Run(fmt.Sprintf("consumer-%d/limitations", index), func(t *testing.T) {
			record, files := acceptedConsumerFixture(t, index)
			consumer := &record.Consumers[index]
			consumer.Limitations = append(consumer.Limitations, "review-only-restriction")
			writeSyntheticRecord(t, files, record)
			if consumers, err := loadProductionQualification(files); err == nil || consumers != nil {
				t.Fatalf("consumer %d accepted an unenforced limitation", index)
			}
		})
	}
}
