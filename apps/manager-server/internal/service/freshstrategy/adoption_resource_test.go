package freshstrategy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

const syntheticAdoptionResourceRunID = "test-adoption-resource-run"

func embeddedAdoptionResourceManifest(t *testing.T, class bootstrap.AdoptSourceClass) (string, []byte, adoptionResourceManifest) {
	t.Helper()
	names, err := fs.Glob(productionEvidence, "qualification/evidence/*.json")
	if err != nil {
		t.Fatal(err)
	}
	var foundName string
	var foundData []byte
	var found adoptionResourceManifest
	for _, name := range names {
		data, err := fs.ReadFile(productionEvidence, name)
		if err != nil {
			t.Fatal(err)
		}
		var manifest adoptionResourceManifest
		if json.Unmarshal(data, &manifest) != nil || manifest.ContractID != adoptionResourceContract || manifest.SourceClass != class {
			continue
		}
		if foundName != "" {
			t.Fatalf("multiple adoption resource manifests for %s", class)
		}
		foundName, foundData, found = name, data, manifest
	}
	if foundName == "" {
		t.Fatalf("checked-in adoption resource manifest missing for %s", class)
	}
	return foundName, foundData, found
}

func TestCheckedInAdoptionResourceManifestsBindClassAndCases(t *testing.T) {
	for _, class := range []bootstrap.AdoptSourceClass{bootstrap.AdoptNetworkExisting, bootstrap.AdoptLegacyManaged} {
		name, data, manifest := embeddedAdoptionResourceManifest(t, class)
		digest := sha256.Sum256(data)
		if path.Base(name) != hex.EncodeToString(digest[:])+".json" {
			t.Fatalf("%s manifest is not content-addressed", class)
		}
		if manifest.SchemaVersion != 1 || manifest.ContractID != adoptionResourceContract || manifest.Revision == "" ||
			manifest.SourceClass != class || manifest.Candidate.DeclaredSHA256 != realCPA8ArchiveSHA256 ||
			manifest.Candidate.ArtifactID != "sha256:"+realCPA8BinarySHA256 ||
			len(manifest.Profiles) != 1 || manifest.Profiles[0].ID != adoptionResourceProfile ||
			len(manifest.Cases) != 3 {
			t.Fatalf("%s manifest identity drifted: %+v", class, manifest)
		}
		scopes := map[string]string{}
		for _, transitionCase := range manifest.Cases {
			if len(transitionCase.ProfileRefs) != 1 || transitionCase.ProfileRefs[0] != adoptionResourceProfile || transitionCase.ID == "" {
				t.Fatalf("%s manifest case drifted: %+v", class, transitionCase)
			}
			scopes[transitionCase.ProofScope] = transitionCase.ID
		}
		if scopes[adoptionResourceProofRead] == "" || scopes[adoptionResourceProofReplay] == "" || scopes[adoptionResourceProofVerify] == "" {
			t.Fatalf("%s manifest proof scopes drifted: %+v", class, scopes)
		}
		switch class {
		case bootstrap.AdoptNetworkExisting:
			if manifest.SourceProfile != adoptionResourceSourceCPAV8 || manifest.LegacySource != nil {
				t.Fatalf("network resource manifest source drifted: %+v", manifest)
			}
		case bootstrap.AdoptLegacyManaged:
			if manifest.SourceProfile != "" || manifest.LegacySource == nil ||
				manifest.LegacySource.Version != cpa.AcceptedLegacyManagedVersion ||
				manifest.LegacySource.ArchiveSHA256 != cpa.AcceptedLegacyManagedArchiveSHA256 ||
				manifest.LegacySource.BinarySHA256 != cpa.AcceptedLegacyManagedBinarySHA256 {
				t.Fatalf("legacy resource manifest source drifted: %+v", manifest)
			}
		}
	}
}

type syntheticAdoptionResource struct {
	head     string
	tree     string
	manifest adoptionResourceManifest
	report   evidenceReport
	read     adoptionResourceReadTrace
	replay   adoptionResourceReplayTrace
	verify   adoptionResourceVerifyTrace
	binding  adoptionResourceEvidence
}

func syntheticAdoptionResourceDefaults(record *productionRecord, class bootstrap.AdoptSourceClass) syntheticAdoptionResource {
	head, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
	short := "network"
	if class == bootstrap.AdoptLegacyManaged {
		short = "legacy"
	}
	readRef := "test-" + short + "-client-api-keys-read"
	replayRef := "test-" + short + "-client-api-keys-replay"
	verifyRef := "test-" + short + "-client-api-keys-verify"
	manifest := adoptionResourceManifest{
		SchemaVersion: 1,
		ContractID:    adoptionResourceContract,
		Revision:      "test-" + short + "-client-api-keys-r1",
		Candidate: evidenceCandidate{
			ID: "candidate-test", DeclaredSHA256: record.ArchiveSHA256, ArtifactID: string(record.ArtifactID),
		},
		Profiles:    []evidenceProfile{{ID: adoptionResourceProfile}},
		SourceClass: class,
		Cases: []evidenceCase{
			{ID: readRef, ProfileRefs: []string{adoptionResourceProfile}, ProofScope: adoptionResourceProofRead},
			{ID: replayRef, ProfileRefs: []string{adoptionResourceProfile}, ProofScope: adoptionResourceProofReplay},
			{ID: verifyRef, ProfileRefs: []string{adoptionResourceProfile}, ProofScope: adoptionResourceProofVerify},
		},
	}
	if class == bootstrap.AdoptNetworkExisting {
		manifest.SourceProfile = adoptionResourceSourceCPAV8
	} else {
		manifest.LegacySource = &evidenceSource{
			Version:       cpa.AcceptedLegacyManagedVersion,
			ArchiveSHA256: cpa.AcceptedLegacyManagedArchiveSHA256,
			BinarySHA256:  cpa.AcceptedLegacyManagedBinarySHA256,
		}
	}
	read := adoptionResourceReadTrace{
		SchemaVersion: 1, RunID: syntheticAdoptionResourceRunID, CaseRef: readRef,
		SourceClass: string(class), ConsumerImplementationSHA256: consumerImplementationSHA256(),
		ReadVerified: true, ClientKeyCount: 2,
	}
	if class == bootstrap.AdoptNetworkExisting {
		read.ManagementGeneration = "v8"
	} else {
		read.LegacyVersion = cpa.AcceptedLegacyManagedVersion
		read.LegacyArchiveSHA256 = cpa.AcceptedLegacyManagedArchiveSHA256
		read.LegacyBinarySHA256 = cpa.AcceptedLegacyManagedBinarySHA256
	}
	replay := adoptionResourceReplayTrace{
		SchemaVersion: 1, RunID: syntheticAdoptionResourceRunID, CaseRef: replayRef,
		SourceClass: string(class), ConsumerImplementationSHA256: consumerImplementationSHA256(),
		ReplayVerified: true, TargetArtifactID: string(record.ArtifactID),
		TargetArchiveSHA256: record.ArchiveSHA256, TargetBinarySHA256: record.BinarySHA256,
		SourcePreserved: true, GatewayPending: true,
	}
	verify := adoptionResourceVerifyTrace{
		SchemaVersion: 1, RunID: syntheticAdoptionResourceRunID, CaseRef: verifyRef,
		SourceClass: string(class), ConsumerImplementationSHA256: consumerImplementationSHA256(),
		ReadbackVerified: true, EffectiveAuthVerified: true, DestinationVerified: true,
		TargetArtifactID: string(record.ArtifactID), TargetArchiveSHA256: record.ArchiveSHA256,
		TargetBinarySHA256: record.BinarySHA256, GatewayPending: true,
	}
	report := evidenceReport{
		SchemaVersion: 1, ContractID: adoptionResourceContract, ManifestRevision: manifest.Revision,
		Run: evidenceRun{ID: syntheticAdoptionResourceRunID, RunnerCommit: head, TestedTree: tree},
		ArtifactObservation: evidenceArtifactObservation{
			CandidateRef: manifest.Candidate.ID, Provenance: "locally-verified",
			ObservedArchiveSHA256: record.ArchiveSHA256, ObservedBinarySHA256: record.BinarySHA256,
		},
		Results: []evidenceResult{
			{CaseRef: readRef, ExecutionState: "completed", AssertionOutcome: "pass", Capability: "supported", EvidenceRefs: []string{readRef}},
			{CaseRef: replayRef, ExecutionState: "completed", AssertionOutcome: "pass", Capability: "supported", EvidenceRefs: []string{replayRef}},
			{CaseRef: verifyRef, ExecutionState: "completed", AssertionOutcome: "pass", Capability: "supported", EvidenceRefs: []string{verifyRef}},
		},
		Evidence: []evidenceTraceRef{
			{ID: readRef, Kind: "sanitized-trace"},
			{ID: replayRef, Kind: "sanitized-trace"},
			{ID: verifyRef, Kind: "sanitized-trace"},
		},
	}
	return syntheticAdoptionResource{
		head: head, tree: tree, manifest: manifest, report: report, read: read, replay: replay, verify: verify,
		binding: adoptionResourceEvidence{
			Profile: adoptionResourceProfile, ImplementationHead: head,
			ReadCaseRef: readRef, ReplayCaseRef: replayRef, VerifyCaseRef: verifyRef,
		},
	}
}

// attachSyntheticAdoptionResource creates a class-specific resource chain in
// test memory. mutate rewrites content before content-addressing; corrupt
// deletes or alters already-addressed bytes. It never creates acceptance.
func attachSyntheticAdoptionResource(t *testing.T, files fstest.MapFS, record *productionRecord, index int, mutate func(*syntheticAdoptionResource), corrupt func(fstest.MapFS, syntheticAdoptionResource)) {
	attachSyntheticAdoptionResourceForClass(t, files, record, index, record.Consumers[index].SourceClass, mutate, corrupt)
}

func attachSyntheticAdoptionResourceForClass(t *testing.T, files fstest.MapFS, record *productionRecord, index int, class bootstrap.AdoptSourceClass, mutate func(*syntheticAdoptionResource), corrupt func(fstest.MapFS, syntheticAdoptionResource)) {
	t.Helper()
	s := syntheticAdoptionResourceDefaults(record, class)
	if mutate != nil {
		mutate(&s)
	}
	readData := encodeJSON(t, s.read)
	replayData := encodeJSON(t, s.replay)
	verifyData := encodeJSON(t, s.verify)
	shas := []string{sha256Hex(readData), sha256Hex(replayData), sha256Hex(verifyData)}
	for i := range s.report.Evidence {
		if s.report.Evidence[i].SHA256 == "" && i < len(shas) {
			s.report.Evidence[i].SHA256 = shas[i]
		}
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
	if s.report.ContractID == "" {
		s.report.ContractID = s.manifest.ContractID
	}
	reportData := encodeJSON(t, s.report)
	if s.binding.ReportSHA256 == "" {
		s.binding.ReportSHA256 = sha256Hex(reportData)
	}
	files["qualification/evidence/"+s.binding.ManifestSHA256+".json"] = &fstest.MapFile{Data: manifestData}
	files["qualification/evidence/"+s.binding.ReportSHA256+".json"] = &fstest.MapFile{Data: reportData}
	files["qualification/evidence/"+shas[0]+".json"] = &fstest.MapFile{Data: readData}
	files["qualification/evidence/"+shas[1]+".json"] = &fstest.MapFile{Data: replayData}
	files["qualification/evidence/"+shas[2]+".json"] = &fstest.MapFile{Data: verifyData}
	if corrupt != nil {
		corrupt(files, s)
	}
	record.Consumers[index].AdoptionEvidence = &s.binding
}

func resourceConsumerFixture(t *testing.T, index int) (productionRecord, fstest.MapFS) {
	t.Helper()
	record, files := acceptedConsumerFixture(t, index)
	attachSyntheticAdoptionResource(t, files, &record, index, nil, nil)
	writeSyntheticRecord(t, files, record)
	return record, files
}

func TestGeneratedAdoptionResourceEvidenceSatisfiesProductionLoader(t *testing.T) {
	for _, index := range []int{2, 3} {
		record, files := acceptedConsumerFixture(t, index)
		_, manifestData, manifest := embeddedAdoptionResourceManifest(t, record.Consumers[index].SourceClass)
		artifact := record.ArtifactID
		read := adoptionResourceReadTrace{ReadVerified: true, ClientKeyCount: 2}
		if manifest.SourceClass == bootstrap.AdoptNetworkExisting {
			read.ManagementGeneration = "v8"
		} else {
			read.LegacyVersion = cpa.AcceptedLegacyManagedVersion
			read.LegacyArchiveSHA256 = cpa.AcceptedLegacyManagedArchiveSHA256
			read.LegacyBinarySHA256 = cpa.AcceptedLegacyManagedBinarySHA256
		}
		replay := adoptionResourceReplayTrace{ReplayVerified: true, TargetArtifactID: string(artifact), TargetArchiveSHA256: record.ArchiveSHA256, TargetBinarySHA256: record.BinarySHA256, SourcePreserved: true, GatewayPending: true}
		verify := adoptionResourceVerifyTrace{ReadbackVerified: true, EffectiveAuthVerified: true, DestinationVerified: true, TargetArtifactID: string(artifact), TargetArchiveSHA256: record.ArchiveSHA256, TargetBinarySHA256: record.BinarySHA256, GatewayPending: true}
		head, tree := strings.Repeat("d", 40), strings.Repeat("e", 40)
		readData, replayData, verifyData, reportData, _ := adoptionResourceEvidencePayload(t, manifestData, manifest, manifest.SourceClass, artifact, read, replay, verify, head, tree)
		files["qualification/evidence/"+sha256Hex(manifestData)+".json"] = &fstest.MapFile{Data: manifestData}
		files["qualification/evidence/"+sha256Hex(reportData)+".json"] = &fstest.MapFile{Data: reportData}
		files["qualification/evidence/"+sha256Hex(readData)+".json"] = &fstest.MapFile{Data: readData}
		files["qualification/evidence/"+sha256Hex(replayData)+".json"] = &fstest.MapFile{Data: replayData}
		files["qualification/evidence/"+sha256Hex(verifyData)+".json"] = &fstest.MapFile{Data: verifyData}
		record.Consumers[index].AdoptionEvidence = &adoptionResourceEvidence{
			Profile: adoptionResourceProfile, ManifestSHA256: sha256Hex(manifestData), ReportSHA256: sha256Hex(reportData),
			ImplementationHead: head, ReadCaseRef: manifest.Cases[0].ID, ReplayCaseRef: manifest.Cases[1].ID, VerifyCaseRef: manifest.Cases[2].ID,
		}
		writeSyntheticRecord(t, files, record)
		consumers, err := loadProductionQualification(files)
		key := consumerKey{record.Consumers[index].Strategy, record.Consumers[index].SourceClass}
		if err != nil || len(consumers[key].AdoptionResources) != 1 || consumers[key].ArtifactID != artifact {
			t.Fatalf("%s generated resource evidence rejected by its loader: %v", manifest.SourceClass, err)
		}
	}
}

func TestAdoptionResourceEvidenceIsIndependentFromTransitionAndClass(t *testing.T) {
	// Accepted legacy adoption still requires its own transition chain; a valid
	// resource chain cannot replace it.
	record, files := resourceConsumerFixture(t, 3)
	record.Consumers[3].LegacyTransition = nil
	writeSyntheticRecord(t, files, record)
	if consumers, err := loadProductionQualification(files); err == nil || consumers != nil {
		t.Fatal("resource chain replaced the required legacy transition")
	}
	// A network resource chain grants resources and no transition.
	record, files = resourceConsumerFixture(t, 2)
	networkKey := consumerKey{bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting}
	consumers, err := loadProductionQualification(files)
	if err != nil || len(consumers[networkKey].AdoptionResources) != 1 ||
		consumers[networkKey].Qualification.StagingV8TransitionQualified {
		t.Fatalf("network resource chain scope drifted: %v", err)
	}
	// A valid transition chain grants the transition and no resources.
	legacyKey := consumerKey{bootstrap.AdoptExisting, bootstrap.AdoptLegacyManaged}
	record, files = acceptedConsumerFixture(t, 3)
	consumers, err = loadProductionQualification(files)
	if err != nil || len(consumers[legacyKey].AdoptionResources) != 0 || consumers[legacyKey].ArtifactID != "" ||
		!consumers[legacyKey].Qualification.StagingV8TransitionQualified {
		t.Fatalf("transition chain granted resource replay: %v", err)
	}
	// A network resource chain cannot stand in for a legacy consumer.
	record, files = acceptedConsumerFixture(t, 3)
	attachSyntheticAdoptionResourceForClass(t, files, &record, 3, bootstrap.AdoptNetworkExisting, nil, nil)
	writeSyntheticRecord(t, files, record)
	if consumers, err := loadProductionQualification(files); err == nil || consumers != nil {
		t.Fatal("network resource chain authorized legacy adoption")
	}
}

func TestPersistedAdoptionOperationRechecksResourceQualification(t *testing.T) {
	record, files := resourceConsumerFixture(t, 2)
	consumers, err := loadProductionQualification(files)
	if err != nil || len(consumers[consumerKey{bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting}].AdoptionResources) != 1 {
		t.Fatalf("accepted resources unavailable: %v", err)
	}
	var sourceCalls atomic.Int32
	source := newScopeTestServer(t, &sourceCalls)
	runtime, target := newScopeRuntime(t, record.ArtifactID)
	protector, _ := security.NewProtector([]byte("test-only-resource-recheck"))
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
	// Withdraw only the resource chain; the consumer remains accepted. The
	// persisted staged operation must not restore replay authority.
	withdrawnRecord, withdrawnFiles := acceptedConsumerFixture(t, 2)
	withdrawnRecord.Consumers[2].AdoptionEvidence = nil
	writeSyntheticRecord(t, withdrawnFiles, withdrawnRecord)
	withdrawnConsumers, err := loadProductionQualification(withdrawnFiles)
	if err != nil {
		t.Fatalf("withdrawn resource record: %v", err)
	}
	restarted := &Service{options: options, production: true, consumers: withdrawnConsumers}
	result, err := restarted.Execute(t.Context(), op, request)
	var blocker *Blocker
	if !errors.As(err, &blocker) || blocker.Code != "adoption_resource_qualification_required" || result.Checkpoint.Strategy != "" ||
		sourceCalls.Load() != 1 || len(runtime.stages) != 0 || len(runtime.starts) != 0 {
		t.Fatalf("persisted resource qualification not rechecked: result=%+v err=%v source=%d", result, err, sourceCalls.Load())
	}
}

func TestProductionAdoptionResourceChainMustBeComplete(t *testing.T) {
	networkKey := consumerKey{bootstrap.AdoptExisting, bootstrap.AdoptNetworkExisting}
	record, files := resourceConsumerFixture(t, 2)
	consumers, err := loadProductionQualification(files)
	if err != nil || len(consumers[networkKey].AdoptionResources) != 1 || consumers[networkKey].ArtifactID != record.ArtifactID {
		t.Fatalf("positive resource control: %v", err)
	}
	chain := func(mutate func(*syntheticAdoptionResource), corrupt func(fstest.MapFS, syntheticAdoptionResource)) func(*productionRecord, fstest.MapFS) {
		return func(record *productionRecord, files fstest.MapFS) {
			attachSyntheticAdoptionResource(t, files, record, 2, mutate, corrupt)
		}
	}
	corruptBytes := func(pick func(syntheticAdoptionResource) string) func(fstest.MapFS, syntheticAdoptionResource) {
		return func(files fstest.MapFS, s syntheticAdoptionResource) {
			files["qualification/evidence/"+pick(s)+".json"].Data = []byte("{}\n")
		}
	}
	deleteEntry := func(pick func(syntheticAdoptionResource) string) func(fstest.MapFS, syntheticAdoptionResource) {
		return func(files fstest.MapFS, s syntheticAdoptionResource) {
			delete(files, "qualification/evidence/"+pick(s)+".json")
		}
	}
	for name, mutate := range map[string]func(*productionRecord, fstest.MapFS){
		"binding profile":                   chain(func(s *syntheticAdoptionResource) { s.binding.Profile = "CPA8-STAGE-r1" }, nil),
		"binding manifest digest malformed": chain(func(s *syntheticAdoptionResource) { s.binding.ManifestSHA256 = "bad" }, nil),
		"binding report digest malformed":   chain(func(s *syntheticAdoptionResource) { s.binding.ReportSHA256 = "bad" }, nil),
		"binding head malformed":            chain(func(s *syntheticAdoptionResource) { s.binding.ImplementationHead = "bad" }, nil),
		"binding duplicate case":            chain(func(s *syntheticAdoptionResource) { s.binding.ReplayCaseRef = s.binding.ReadCaseRef }, nil),
		"binding empty verify case":         chain(func(s *syntheticAdoptionResource) { s.binding.VerifyCaseRef = "" }, nil),
		"manifest missing":                  chain(nil, deleteEntry(func(s syntheticAdoptionResource) string { return s.binding.ManifestSHA256 })),
		"manifest digest mismatch":          chain(nil, corruptBytes(func(s syntheticAdoptionResource) string { return s.binding.ManifestSHA256 })),
		"manifest schema":                   chain(func(s *syntheticAdoptionResource) { s.manifest.SchemaVersion = 2 }, nil),
		"manifest contract":                 chain(func(s *syntheticAdoptionResource) { s.manifest.ContractID = "other" }, nil),
		"manifest revision":                 chain(func(s *syntheticAdoptionResource) { s.manifest.Revision = "" }, nil),
		"manifest candidate archive":        chain(func(s *syntheticAdoptionResource) { s.manifest.Candidate.DeclaredSHA256 = strings.Repeat("a", 64) }, nil),
		"manifest candidate artifact": chain(func(s *syntheticAdoptionResource) {
			s.manifest.Candidate.ArtifactID = "sha256:" + strings.Repeat("a", 64)
		}, nil),
		"manifest source class":       chain(func(s *syntheticAdoptionResource) { s.manifest.SourceClass = bootstrap.AdoptLegacyManaged }, nil),
		"manifest source profile":     chain(func(s *syntheticAdoptionResource) { s.manifest.SourceProfile = "other" }, nil),
		"manifest profiles":           chain(func(s *syntheticAdoptionResource) { s.manifest.Profiles = nil }, nil),
		"manifest cases":              chain(func(s *syntheticAdoptionResource) { s.manifest.Cases = nil }, nil),
		"manifest proof scope":        chain(func(s *syntheticAdoptionResource) { s.manifest.Cases[0].ProofScope = adoptionResourceProofReplay }, nil),
		"report missing":              chain(nil, deleteEntry(func(s syntheticAdoptionResource) string { return s.binding.ReportSHA256 })),
		"report digest mismatch":      chain(nil, corruptBytes(func(s syntheticAdoptionResource) string { return s.binding.ReportSHA256 })),
		"report schema":               chain(func(s *syntheticAdoptionResource) { s.report.SchemaVersion = 2 }, nil),
		"report contract":             chain(func(s *syntheticAdoptionResource) { s.report.ContractID = "other" }, nil),
		"report manifest mismatch":    chain(func(s *syntheticAdoptionResource) { s.report.ManifestSHA256 = strings.Repeat("b", 64) }, nil),
		"report runner head mismatch": chain(func(s *syntheticAdoptionResource) { s.report.Run.RunnerCommit = strings.Repeat("b", 40) }, nil),
		"report run id missing":       chain(func(s *syntheticAdoptionResource) { s.report.Run.ID = "" }, nil),
		"report artifact archive": chain(func(s *syntheticAdoptionResource) {
			s.report.ArtifactObservation.ObservedArchiveSHA256 = strings.Repeat("a", 64)
		}, nil),
		"report artifact binary": chain(func(s *syntheticAdoptionResource) {
			s.report.ArtifactObservation.ObservedBinarySHA256 = strings.Repeat("a", 64)
		}, nil),
		"report result case":               chain(func(s *syntheticAdoptionResource) { s.report.Results[0].CaseRef = "other" }, nil),
		"report result execution":          chain(func(s *syntheticAdoptionResource) { s.report.Results[0].ExecutionState = "running" }, nil),
		"report result outcome":            chain(func(s *syntheticAdoptionResource) { s.report.Results[0].AssertionOutcome = "fail" }, nil),
		"report result capability":         chain(func(s *syntheticAdoptionResource) { s.report.Results[0].Capability = "limited" }, nil),
		"report result evidence ref":       chain(func(s *syntheticAdoptionResource) { s.report.Results[0].EvidenceRefs = nil }, nil),
		"report evidence missing":          chain(func(s *syntheticAdoptionResource) { s.report.Evidence = nil }, nil),
		"read trace missing":               chain(nil, deleteEntry(func(s syntheticAdoptionResource) string { return s.report.Evidence[0].SHA256 })),
		"read trace digest mismatch":       chain(nil, corruptBytes(func(s syntheticAdoptionResource) string { return s.report.Evidence[0].SHA256 })),
		"read not verified":                chain(func(s *syntheticAdoptionResource) { s.read.ReadVerified = false }, nil),
		"read client key count":            chain(func(s *syntheticAdoptionResource) { s.read.ClientKeyCount = 0 }, nil),
		"read management generation":       chain(func(s *syntheticAdoptionResource) { s.read.ManagementGeneration = "" }, nil),
		"read trace source class":          chain(func(s *syntheticAdoptionResource) { s.read.SourceClass = string(bootstrap.AdoptLegacyManaged) }, nil),
		"read trace implementation digest": chain(func(s *syntheticAdoptionResource) { s.read.ConsumerImplementationSHA256 = strings.Repeat("a", 64) }, nil),
		"read trace run id":                chain(func(s *syntheticAdoptionResource) { s.read.RunID = "other" }, nil),
		"read trace case ref":              chain(func(s *syntheticAdoptionResource) { s.read.CaseRef = "other" }, nil),
		"read trace schema":                chain(func(s *syntheticAdoptionResource) { s.read.SchemaVersion = 2 }, nil),
		"replay trace missing":             chain(nil, deleteEntry(func(s syntheticAdoptionResource) string { return s.report.Evidence[1].SHA256 })),
		"replay trace digest mismatch":     chain(nil, corruptBytes(func(s syntheticAdoptionResource) string { return s.report.Evidence[1].SHA256 })),
		"replay not verified":              chain(func(s *syntheticAdoptionResource) { s.replay.ReplayVerified = false }, nil),
		"replay target artifact":           chain(func(s *syntheticAdoptionResource) { s.replay.TargetArtifactID = "sha256:" + strings.Repeat("a", 64) }, nil),
		"replay target archive":            chain(func(s *syntheticAdoptionResource) { s.replay.TargetArchiveSHA256 = strings.Repeat("a", 64) }, nil),
		"replay target binary":             chain(func(s *syntheticAdoptionResource) { s.replay.TargetBinarySHA256 = strings.Repeat("a", 64) }, nil),
		"replay source not preserved":      chain(func(s *syntheticAdoptionResource) { s.replay.SourcePreserved = false }, nil),
		"replay gateway not pending":       chain(func(s *syntheticAdoptionResource) { s.replay.GatewayPending = false }, nil),
		"verify trace missing":             chain(nil, deleteEntry(func(s syntheticAdoptionResource) string { return s.report.Evidence[2].SHA256 })),
		"verify trace digest mismatch":     chain(nil, corruptBytes(func(s syntheticAdoptionResource) string { return s.report.Evidence[2].SHA256 })),
		"verify readback":                  chain(func(s *syntheticAdoptionResource) { s.verify.ReadbackVerified = false }, nil),
		"verify effective auth":            chain(func(s *syntheticAdoptionResource) { s.verify.EffectiveAuthVerified = false }, nil),
		"verify destination":               chain(func(s *syntheticAdoptionResource) { s.verify.DestinationVerified = false }, nil),
		"verify target artifact":           chain(func(s *syntheticAdoptionResource) { s.verify.TargetArtifactID = "sha256:" + strings.Repeat("a", 64) }, nil),
		"verify gateway not pending":       chain(func(s *syntheticAdoptionResource) { s.verify.GatewayPending = false }, nil),
		"consumer implementation drift": func(r *productionRecord, _ fstest.MapFS) {
			r.ImplementationSHA256 = strings.Repeat("a", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			record, files := acceptedConsumerFixture(t, 2)
			mutate(&record, files)
			writeSyntheticRecord(t, files, record)
			if consumers, err := loadProductionQualification(files); err == nil || consumers != nil {
				t.Fatal("incomplete or mismatched resource chain granted replay")
			}
		})
	}
}

func TestLegacyAdoptionResourceChainBindsPinnedSource(t *testing.T) {
	before := func(mutate func(*syntheticAdoptionResource), corrupt func(fstest.MapFS, syntheticAdoptionResource)) func(*productionRecord, fstest.MapFS) {
		return func(record *productionRecord, files fstest.MapFS) {
			attachSyntheticAdoptionResource(t, files, record, 3, mutate, corrupt)
		}
	}
	for name, mutate := range map[string]func(*productionRecord, fstest.MapFS){
		"manifest source missing": before(func(s *syntheticAdoptionResource) { s.manifest.LegacySource = nil }, nil),
		"manifest version":        before(func(s *syntheticAdoptionResource) { s.manifest.LegacySource.Version = "v7.0.13" }, nil),
		"manifest archive":        before(func(s *syntheticAdoptionResource) { s.manifest.LegacySource.ArchiveSHA256 = strings.Repeat("a", 64) }, nil),
		"manifest binary":         before(func(s *syntheticAdoptionResource) { s.manifest.LegacySource.BinarySHA256 = strings.Repeat("a", 64) }, nil),
		"read version":            before(func(s *syntheticAdoptionResource) { s.read.LegacyVersion = "v7.0.13" }, nil),
		"read archive":            before(func(s *syntheticAdoptionResource) { s.read.LegacyArchiveSHA256 = strings.Repeat("a", 64) }, nil),
		"read binary":             before(func(s *syntheticAdoptionResource) { s.read.LegacyBinarySHA256 = strings.Repeat("a", 64) }, nil),
		"network chain on legacy": func(record *productionRecord, files fstest.MapFS) {
			attachSyntheticAdoptionResourceForClass(t, files, record, 3, bootstrap.AdoptNetworkExisting, nil, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			record, files := acceptedConsumerFixture(t, 3)
			mutate(&record, files)
			writeSyntheticRecord(t, files, record)
			if consumers, err := loadProductionQualification(files); err == nil || consumers != nil {
				t.Fatal("invalid legacy resource chain granted replay")
			}
		})
	}
}
