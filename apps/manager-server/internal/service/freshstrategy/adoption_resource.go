package freshstrategy

import (
	"encoding/json"
	"io/fs"
	"slices"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// The adoption resource chain is a separate evidence family from the retained
// r4 background profile and from the legacy staging transition. It binds the
// exact read/replay/verify facts for the only migrated resource: client API
// keys. It is never a second qualification authority; consumer.json remains the
// only authority and must reference these bytes explicitly.
// adoptionResourceKindNames maps a migrated resource reference to the stable key
// its evidence counts use. The three traces must report the same key set, and
// that set must be exactly the migrated references: a kind is authorized by
// evidence for that kind, never by another kind's presence.
func adoptionResourceKindNames() map[bootstrap.ResourceRef]string {
	return map[bootstrap.ResourceRef]string{
		bootstrap.AdoptionRefClientAPIKeys:        "client-api-keys",
		bootstrap.AdoptionRefCPAAuthFiles:         "cpa-auth-files",
		bootstrap.AdoptionRefConfigRouting:        "cpa-config-routing",
		bootstrap.AdoptionRefProviderStateAPIKeys: "provider-state-api-keys",
	}
}

// adoptionResourceAuthorizedRefs returns the migrated references this chain
// authorizes, or nil when the three traces do not agree.
func adoptionResourceAuthorizedRefs(read adoptionResourceReadTrace, replay adoptionResourceReplayTrace, verify adoptionResourceVerifyTrace) []bootstrap.ResourceRef {
	names := adoptionResourceKindNames()
	authorized := make([]bootstrap.ResourceRef, 0, len(names))
	for ref, name := range names {
		count, ok := read.ResourceCounts[name]
		if !ok || count < 0 {
			continue
		}
		if _, ok := replay.CopiedResources[name]; !ok {
			return nil
		}
		if _, ok := verify.VerifiedResources[name]; !ok {
			return nil
		}
		authorized = append(authorized, ref)
	}
	slices.Sort(authorized)
	return authorized
}

const (
	adoptionResourceProfile     = "CPA8-ADOPT-RESOURCE-r1"
	adoptionResourceContract    = "cpamp-cpa-v8-adoption-resource"
	adoptionResourceSourceCPAV8 = "cpa_v8"

	adoptionResourceProofRead   = "source-read"
	adoptionResourceProofReplay = "destination-replay"
	adoptionResourceProofVerify = "destination-verify"
)

// adoptionResourceManifest is the immutable case definition for a class-specific
// client-key adoption resource proof. Cases are distinct: a source read cannot
// authorize replay, and replay cannot authorize destination verification.
type adoptionResourceManifest struct {
	SchemaVersion int                        `json:"schemaVersion"`
	ContractID    string                     `json:"contractId"`
	Revision      string                     `json:"revision"`
	Candidate     evidenceCandidate          `json:"candidate"`
	Profiles      []evidenceProfile          `json:"profiles"`
	Cases         []evidenceCase             `json:"cases"`
	SourceClass   bootstrap.AdoptSourceClass `json:"sourceClass"`
	SourceProfile string                     `json:"sourceProfile,omitempty"`
	LegacySource  *evidenceSource            `json:"legacySource,omitempty"`
}

// The three trace shapes are deliberately separate so one proof type cannot
// carry the fields of another.
type adoptionResourceReadTrace struct {
	SchemaVersion                int    `json:"schemaVersion"`
	RunID                        string `json:"runId"`
	CaseRef                      string `json:"caseRef"`
	SourceClass                  string `json:"sourceClass"`
	ConsumerImplementationSHA256 string `json:"consumerImplementationSha256"`
	ReadVerified                 bool   `json:"readVerified"`
	ClientKeyCount               int    `json:"clientKeyCount"`
	// ResourceCounts is the number of items the source read found for each
	// migrated resource kind the chain exercised. A kind absent from this map was
	// not read in this run, so it is not authorized by this chain.
	ResourceCounts       map[string]int `json:"resourceCounts"`
	ManagementGeneration string         `json:"managementGeneration,omitempty"`
	LegacyVersion        string         `json:"legacyVersion,omitempty"`
	LegacyArchiveSHA256  string         `json:"legacyArchiveSha256,omitempty"`
	LegacyBinarySHA256   string         `json:"legacyBinarySha256,omitempty"`
}

type adoptionResourceReplayTrace struct {
	SchemaVersion                int    `json:"schemaVersion"`
	RunID                        string `json:"runId"`
	CaseRef                      string `json:"caseRef"`
	SourceClass                  string `json:"sourceClass"`
	ConsumerImplementationSHA256 string `json:"consumerImplementationSha256"`
	ReplayVerified               bool   `json:"replayVerified"`
	// CopiedResources names the migrated resource kinds the executor copied to
	// the destination in this run, with the number of items it recorded as
	// migrated.
	CopiedResources     map[string]int `json:"copiedResources"`
	TargetArtifactID    string         `json:"targetArtifactId"`
	TargetArchiveSHA256 string         `json:"targetArchiveSha256"`
	TargetBinarySHA256  string         `json:"targetBinarySha256"`
	SourcePreserved     bool           `json:"sourcePreserved"`
	GatewayPending      bool           `json:"gatewayPending"`
}

type adoptionResourceVerifyTrace struct {
	SchemaVersion                int    `json:"schemaVersion"`
	RunID                        string `json:"runId"`
	CaseRef                      string `json:"caseRef"`
	SourceClass                  string `json:"sourceClass"`
	ConsumerImplementationSHA256 string `json:"consumerImplementationSha256"`
	ReadbackVerified             bool   `json:"readbackVerified"`
	EffectiveAuthVerified        bool   `json:"effectiveAuthVerified"`
	DestinationVerified          bool   `json:"destinationVerified"`
	// VerifiedResources is the number of items whose destination value this run
	// read back and compared with the source value, for each migrated kind.
	VerifiedResources   map[string]int `json:"verifiedResources"`
	TargetArtifactID    string         `json:"targetArtifactId"`
	TargetArchiveSHA256 string         `json:"targetArchiveSha256"`
	TargetBinarySHA256  string         `json:"targetBinarySha256"`
	GatewayPending      bool           `json:"gatewayPending"`
}

// verifyAdoptionResourceEvidence validates one class-specific resource chain.
// A missing, mismatched or cross-class chain grants no resource decisions and
// no destination ArtifactID. This function never grants source/profile or
// legacy-transition qualification.
func verifyAdoptionResourceEvidence(files fs.FS, record productionRecord, consumer consumerRecord, class bootstrap.AdoptSourceClass) ([]bootstrap.ResourceRef, error) {
	binding := consumer.AdoptionEvidence
	if binding == nil || binding.Profile != adoptionResourceProfile ||
		binding.ReadCaseRef == "" || binding.ReplayCaseRef == "" || binding.VerifyCaseRef == "" ||
		binding.ReadCaseRef == binding.ReplayCaseRef || binding.ReadCaseRef == binding.VerifyCaseRef || binding.ReplayCaseRef == binding.VerifyCaseRef ||
		!hexDigest(binding.ManifestSHA256, 64) || !hexDigest(binding.ReportSHA256, 64) || !hexDigest(binding.ImplementationHead, 40) {
		return nil, errProductionQualification
	}
	if class != bootstrap.AdoptNetworkExisting && class != bootstrap.AdoptLegacyManaged {
		return nil, errProductionQualification
	}
	manifestData, err := pinnedEvidence(files, binding.ManifestSHA256, ".json")
	if err != nil {
		return nil, err
	}
	var manifest adoptionResourceManifest
	if json.Unmarshal(manifestData, &manifest) != nil || manifest.SchemaVersion != 1 ||
		manifest.ContractID != adoptionResourceContract || manifest.Revision == "" ||
		manifest.SourceClass != class ||
		manifest.Candidate.DeclaredSHA256 != record.ArchiveSHA256 ||
		manifest.Candidate.ArtifactID != string(record.ArtifactID) ||
		len(manifest.Profiles) != 1 || manifest.Profiles[0].ID != adoptionResourceProfile ||
		len(manifest.Cases) != 3 {
		return nil, errProductionQualification
	}
	switch class {
	case bootstrap.AdoptNetworkExisting:
		if manifest.SourceProfile != adoptionResourceSourceCPAV8 || manifest.LegacySource != nil {
			return nil, errProductionQualification
		}
	case bootstrap.AdoptLegacyManaged:
		if manifest.SourceProfile != "" || manifest.LegacySource == nil ||
			manifest.LegacySource.Version != cpa.AcceptedLegacyManagedVersion ||
			manifest.LegacySource.ArchiveSHA256 != cpa.AcceptedLegacyManagedArchiveSHA256 ||
			manifest.LegacySource.BinarySHA256 != cpa.AcceptedLegacyManagedBinarySHA256 {
			return nil, errProductionQualification
		}
	}
	expectedScope := map[string]string{
		binding.ReadCaseRef:   adoptionResourceProofRead,
		binding.ReplayCaseRef: adoptionResourceProofReplay,
		binding.VerifyCaseRef: adoptionResourceProofVerify,
	}
	seenCases := map[string]bool{}
	for _, transitionCase := range manifest.Cases {
		scope, ok := expectedScope[transitionCase.ID]
		if !ok || seenCases[transitionCase.ID] || transitionCase.ProofScope != scope ||
			len(transitionCase.ProfileRefs) != 1 || transitionCase.ProfileRefs[0] != adoptionResourceProfile {
			return nil, errProductionQualification
		}
		seenCases[transitionCase.ID] = true
	}
	if len(seenCases) != 3 {
		return nil, errProductionQualification
	}
	reportData, err := pinnedEvidence(files, binding.ReportSHA256, ".json")
	if err != nil {
		return nil, err
	}
	var report evidenceReport
	if json.Unmarshal(reportData, &report) != nil || report.SchemaVersion != 1 ||
		report.ContractID != manifest.ContractID || report.ManifestRevision != manifest.Revision ||
		report.ManifestSHA256 != binding.ManifestSHA256 || report.Run.ID == "" ||
		report.Run.RunnerCommit != binding.ImplementationHead ||
		report.ArtifactObservation.CandidateRef != manifest.Candidate.ID ||
		report.ArtifactObservation.Provenance != "locally-verified" ||
		report.ArtifactObservation.ObservedArchiveSHA256 != record.ArchiveSHA256 ||
		report.ArtifactObservation.ObservedBinarySHA256 != record.BinarySHA256 ||
		len(report.Results) != 3 || len(report.Evidence) != 3 {
		return nil, errProductionQualification
	}
	seenResults := map[string]bool{}
	for _, result := range report.Results {
		if _, ok := expectedScope[result.CaseRef]; !ok || seenResults[result.CaseRef] ||
			result.ExecutionState != "completed" || result.AssertionOutcome != "pass" ||
			result.Capability != "supported" || !slices.Contains(result.EvidenceRefs, result.CaseRef) {
			return nil, errProductionQualification
		}
		seenResults[result.CaseRef] = true
	}
	if len(seenResults) != 3 {
		return nil, errProductionQualification
	}
	// The three traces must agree on the migrated kinds, and that set must be
	// exactly the rules' migrated set. A trace that silently dropped a kind would
	// otherwise authorize a resource this run never exercised.
	var readTrace adoptionResourceReadTrace
	var replayTrace adoptionResourceReplayTrace
	var verifyTrace adoptionResourceVerifyTrace
	for _, caseRef := range []string{binding.ReadCaseRef, binding.ReplayCaseRef, binding.VerifyCaseRef} {
		ref := slices.IndexFunc(report.Evidence, func(evidence evidenceTraceRef) bool {
			return evidence.ID == caseRef && evidence.Kind == "sanitized-trace" && hexDigest(evidence.SHA256, 64)
		})
		if ref < 0 {
			return nil, errProductionQualification
		}
		traceData, err := pinnedEvidence(files, report.Evidence[ref].SHA256, ".json")
		if err != nil {
			return nil, err
		}
		switch expectedScope[caseRef] {
		case adoptionResourceProofRead:
			if json.Unmarshal(traceData, &readTrace) != nil || !validAdoptionResourceTraceBase(readTrace.SchemaVersion, readTrace.RunID, readTrace.CaseRef, readTrace.SourceClass, readTrace.ConsumerImplementationSHA256, report.Run.ID, caseRef, class) ||
				!readTrace.ReadVerified || readTrace.ClientKeyCount < 1 || readTrace.ClientKeyCount > 128 {
				return nil, errProductionQualification
			}
			switch class {
			case bootstrap.AdoptNetworkExisting:
				if readTrace.ManagementGeneration != "v8" || readTrace.LegacyVersion != "" || readTrace.LegacyArchiveSHA256 != "" || readTrace.LegacyBinarySHA256 != "" {
					return nil, errProductionQualification
				}
			case bootstrap.AdoptLegacyManaged:
				if readTrace.ManagementGeneration != "" || readTrace.LegacyVersion != cpa.AcceptedLegacyManagedVersion ||
					readTrace.LegacyArchiveSHA256 != cpa.AcceptedLegacyManagedArchiveSHA256 ||
					readTrace.LegacyBinarySHA256 != cpa.AcceptedLegacyManagedBinarySHA256 {
					return nil, errProductionQualification
				}
			}
		case adoptionResourceProofReplay:
			if json.Unmarshal(traceData, &replayTrace) != nil || !validAdoptionResourceTraceBase(replayTrace.SchemaVersion, replayTrace.RunID, replayTrace.CaseRef, replayTrace.SourceClass, replayTrace.ConsumerImplementationSHA256, report.Run.ID, caseRef, class) ||
				!replayTrace.ReplayVerified || replayTrace.TargetArtifactID != string(record.ArtifactID) ||
				replayTrace.TargetArchiveSHA256 != record.ArchiveSHA256 || replayTrace.TargetBinarySHA256 != record.BinarySHA256 ||
				!replayTrace.SourcePreserved || !replayTrace.GatewayPending {
				return nil, errProductionQualification
			}
		case adoptionResourceProofVerify:
			if json.Unmarshal(traceData, &verifyTrace) != nil || !validAdoptionResourceTraceBase(verifyTrace.SchemaVersion, verifyTrace.RunID, verifyTrace.CaseRef, verifyTrace.SourceClass, verifyTrace.ConsumerImplementationSHA256, report.Run.ID, caseRef, class) ||
				!verifyTrace.ReadbackVerified || !verifyTrace.EffectiveAuthVerified || !verifyTrace.DestinationVerified ||
				verifyTrace.TargetArtifactID != string(record.ArtifactID) || verifyTrace.TargetArchiveSHA256 != record.ArchiveSHA256 ||
				verifyTrace.TargetBinarySHA256 != record.BinarySHA256 || !verifyTrace.GatewayPending {
				return nil, errProductionQualification
			}
		default:
			return nil, errProductionQualification
		}
	}
	// The chain authorizes exactly what it exercised, and the rules cap that. A
	// kind the three traces do not all report is not authorized; a kind the rules
	// do not mark migrate can never be authorized, whatever a trace claims. The
	// client keys are mandatory: a source with no adoptable key is refused
	// earlier, and the destination would otherwise accept anonymous callers.
	authorized := adoptionResourceAuthorizedRefs(readTrace, replayTrace, verifyTrace)
	if len(authorized) == 0 {
		return nil, errProductionQualification
	}
	migrated := bootstrap.MigratedAdoptionResourceRefs()
	for _, ref := range authorized {
		if !slices.Contains(migrated, ref) {
			return nil, errProductionQualification
		}
	}
	if !slices.Contains(authorized, bootstrap.AdoptionRefClientAPIKeys) {
		return nil, errProductionQualification
	}
	return authorized, nil
}

func validAdoptionResourceTraceBase(schemaVersion int, runID, caseRef, sourceClass, implementationSHA256, reportRunID, expectedCaseRef string, class bootstrap.AdoptSourceClass) bool {
	return schemaVersion == 1 && runID == reportRunID && caseRef == expectedCaseRef &&
		sourceClass == string(class) && implementationSHA256 == consumerImplementationSHA256()
}
