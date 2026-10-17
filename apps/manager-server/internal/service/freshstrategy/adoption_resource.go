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
	ManagementGeneration         string `json:"managementGeneration,omitempty"`
	LegacyVersion                string `json:"legacyVersion,omitempty"`
	LegacyArchiveSHA256          string `json:"legacyArchiveSha256,omitempty"`
	LegacyBinarySHA256           string `json:"legacyBinarySha256,omitempty"`
}

type adoptionResourceReplayTrace struct {
	SchemaVersion                int    `json:"schemaVersion"`
	RunID                        string `json:"runId"`
	CaseRef                      string `json:"caseRef"`
	SourceClass                  string `json:"sourceClass"`
	ConsumerImplementationSHA256 string `json:"consumerImplementationSha256"`
	ReplayVerified               bool   `json:"replayVerified"`
	TargetArtifactID             string `json:"targetArtifactId"`
	TargetArchiveSHA256          string `json:"targetArchiveSha256"`
	TargetBinarySHA256           string `json:"targetBinarySha256"`
	SourcePreserved              bool   `json:"sourcePreserved"`
	GatewayPending               bool   `json:"gatewayPending"`
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
	TargetArtifactID             string `json:"targetArtifactId"`
	TargetArchiveSHA256          string `json:"targetArchiveSha256"`
	TargetBinarySHA256           string `json:"targetBinarySha256"`
	GatewayPending               bool   `json:"gatewayPending"`
}

// verifyAdoptionResourceEvidence validates one class-specific resource chain.
// A missing, mismatched or cross-class chain grants no resource decisions and
// no destination ArtifactID. This function never grants source/profile or
// legacy-transition qualification.
func verifyAdoptionResourceEvidence(files fs.FS, record productionRecord, consumer consumerRecord, class bootstrap.AdoptSourceClass) error {
	binding := consumer.AdoptionEvidence
	if binding == nil || binding.Profile != adoptionResourceProfile ||
		binding.ReadCaseRef == "" || binding.ReplayCaseRef == "" || binding.VerifyCaseRef == "" ||
		binding.ReadCaseRef == binding.ReplayCaseRef || binding.ReadCaseRef == binding.VerifyCaseRef || binding.ReplayCaseRef == binding.VerifyCaseRef ||
		!hexDigest(binding.ManifestSHA256, 64) || !hexDigest(binding.ReportSHA256, 64) || !hexDigest(binding.ImplementationHead, 40) {
		return errProductionQualification
	}
	if class != bootstrap.AdoptNetworkExisting && class != bootstrap.AdoptLegacyManaged {
		return errProductionQualification
	}
	manifestData, err := pinnedEvidence(files, binding.ManifestSHA256, ".json")
	if err != nil {
		return err
	}
	var manifest adoptionResourceManifest
	if json.Unmarshal(manifestData, &manifest) != nil || manifest.SchemaVersion != 1 ||
		manifest.ContractID != adoptionResourceContract || manifest.Revision == "" ||
		manifest.SourceClass != class ||
		manifest.Candidate.DeclaredSHA256 != record.ArchiveSHA256 ||
		manifest.Candidate.ArtifactID != string(record.ArtifactID) ||
		len(manifest.Profiles) != 1 || manifest.Profiles[0].ID != adoptionResourceProfile ||
		len(manifest.Cases) != 3 {
		return errProductionQualification
	}
	switch class {
	case bootstrap.AdoptNetworkExisting:
		if manifest.SourceProfile != adoptionResourceSourceCPAV8 || manifest.LegacySource != nil {
			return errProductionQualification
		}
	case bootstrap.AdoptLegacyManaged:
		if manifest.SourceProfile != "" || manifest.LegacySource == nil ||
			manifest.LegacySource.Version != cpa.AcceptedLegacyManagedVersion ||
			manifest.LegacySource.ArchiveSHA256 != cpa.AcceptedLegacyManagedArchiveSHA256 ||
			manifest.LegacySource.BinarySHA256 != cpa.AcceptedLegacyManagedBinarySHA256 {
			return errProductionQualification
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
			return errProductionQualification
		}
		seenCases[transitionCase.ID] = true
	}
	if len(seenCases) != 3 {
		return errProductionQualification
	}
	reportData, err := pinnedEvidence(files, binding.ReportSHA256, ".json")
	if err != nil {
		return err
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
		return errProductionQualification
	}
	seenResults := map[string]bool{}
	for _, result := range report.Results {
		if _, ok := expectedScope[result.CaseRef]; !ok || seenResults[result.CaseRef] ||
			result.ExecutionState != "completed" || result.AssertionOutcome != "pass" ||
			result.Capability != "supported" || !slices.Contains(result.EvidenceRefs, result.CaseRef) {
			return errProductionQualification
		}
		seenResults[result.CaseRef] = true
	}
	if len(seenResults) != 3 {
		return errProductionQualification
	}
	for _, caseRef := range []string{binding.ReadCaseRef, binding.ReplayCaseRef, binding.VerifyCaseRef} {
		ref := slices.IndexFunc(report.Evidence, func(evidence evidenceTraceRef) bool {
			return evidence.ID == caseRef && evidence.Kind == "sanitized-trace" && hexDigest(evidence.SHA256, 64)
		})
		if ref < 0 {
			return errProductionQualification
		}
		traceData, err := pinnedEvidence(files, report.Evidence[ref].SHA256, ".json")
		if err != nil {
			return err
		}
		switch expectedScope[caseRef] {
		case adoptionResourceProofRead:
			var trace adoptionResourceReadTrace
			if json.Unmarshal(traceData, &trace) != nil || !validAdoptionResourceTraceBase(trace.SchemaVersion, trace.RunID, trace.CaseRef, trace.SourceClass, trace.ConsumerImplementationSHA256, report.Run.ID, caseRef, class) ||
				!trace.ReadVerified || trace.ClientKeyCount < 1 || trace.ClientKeyCount > 128 {
				return errProductionQualification
			}
			switch class {
			case bootstrap.AdoptNetworkExisting:
				if trace.ManagementGeneration != "v8" || trace.LegacyVersion != "" || trace.LegacyArchiveSHA256 != "" || trace.LegacyBinarySHA256 != "" {
					return errProductionQualification
				}
			case bootstrap.AdoptLegacyManaged:
				if trace.ManagementGeneration != "" || trace.LegacyVersion != cpa.AcceptedLegacyManagedVersion ||
					trace.LegacyArchiveSHA256 != cpa.AcceptedLegacyManagedArchiveSHA256 ||
					trace.LegacyBinarySHA256 != cpa.AcceptedLegacyManagedBinarySHA256 {
					return errProductionQualification
				}
			}
		case adoptionResourceProofReplay:
			var trace adoptionResourceReplayTrace
			if json.Unmarshal(traceData, &trace) != nil || !validAdoptionResourceTraceBase(trace.SchemaVersion, trace.RunID, trace.CaseRef, trace.SourceClass, trace.ConsumerImplementationSHA256, report.Run.ID, caseRef, class) ||
				!trace.ReplayVerified || trace.TargetArtifactID != string(record.ArtifactID) ||
				trace.TargetArchiveSHA256 != record.ArchiveSHA256 || trace.TargetBinarySHA256 != record.BinarySHA256 ||
				!trace.SourcePreserved || !trace.GatewayPending {
				return errProductionQualification
			}
		case adoptionResourceProofVerify:
			var trace adoptionResourceVerifyTrace
			if json.Unmarshal(traceData, &trace) != nil || !validAdoptionResourceTraceBase(trace.SchemaVersion, trace.RunID, trace.CaseRef, trace.SourceClass, trace.ConsumerImplementationSHA256, report.Run.ID, caseRef, class) ||
				!trace.ReadbackVerified || !trace.EffectiveAuthVerified || !trace.DestinationVerified ||
				trace.TargetArtifactID != string(record.ArtifactID) || trace.TargetArchiveSHA256 != record.ArchiveSHA256 ||
				trace.TargetBinarySHA256 != record.BinarySHA256 || !trace.GatewayPending {
				return errProductionQualification
			}
		default:
			return errProductionQualification
		}
	}
	return nil
}

func validAdoptionResourceTraceBase(schemaVersion int, runID, caseRef, sourceClass, implementationSHA256, reportRunID, expectedCaseRef string, class bootstrap.AdoptSourceClass) bool {
	return schemaVersion == 1 && runID == reportRunID && caseRef == expectedCaseRef &&
		sourceClass == string(class) && implementationSHA256 == consumerImplementationSHA256()
}
