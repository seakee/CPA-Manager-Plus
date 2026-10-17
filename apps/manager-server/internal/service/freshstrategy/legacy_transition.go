package freshstrategy

import (
	"encoding/json"
	"io/fs"
	"slices"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

const (
	acceptedLegacyTransitionProfile = "CPA8-STAGE-r1"
	acceptedLegacyTransitionCaseRef = "fresh-legacy-managed-stage-v8-r1"
)

// A legacy transition acceptance is a separate authority chain, not just a
// standalone observation: consumer binding -> manifest -> case -> report ->
// implementation head -> sanitized transition observation. It is deliberately
// independent of the retained original CPA8-STAGE-r1 report case refs.
type legacyTransitionEvidence struct {
	Profile string `json:"profile"`
	CaseRef string `json:"caseRef"`

	ManifestSHA256     string `json:"manifestSha256"`
	ReportSHA256       string `json:"reportSha256"`
	ImplementationHead string `json:"implementationHead"`
	EvidenceSHA256     string `json:"evidenceSha256"`

	LegacyVersion       string `json:"legacyVersion"`
	LegacyArchiveSHA256 string `json:"legacyArchiveSha256"`
	LegacyBinarySHA256  string `json:"legacyBinarySha256"`
}

// legacyTransitionManifest is the immutable case definition for the actual
// legacy-to-staging-v8 transition. It is evidence, never a second authority.
type legacyTransitionManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	ContractID    string            `json:"contractId"`
	Revision      string            `json:"revision"`
	Candidate     evidenceCandidate `json:"candidate"`
	Profiles      []evidenceProfile `json:"profiles"`
	Cases         []evidenceCase    `json:"cases"`
	LegacySource  evidenceSource    `json:"legacySource"`
}

type evidenceCandidate struct {
	ID             string `json:"id"`
	DeclaredSHA256 string `json:"declaredSha256"`
	ArtifactID     string `json:"artifactId"`
}

type evidenceProfile struct {
	ID string `json:"id"`
}

type evidenceCase struct {
	ID          string   `json:"id"`
	ProfileRefs []string `json:"profileRefs"`
	ProofScope  string   `json:"proofScope"`
}

type evidenceSource struct {
	Version       string `json:"version"`
	ArchiveSHA256 string `json:"archiveSha256"`
	BinarySHA256  string `json:"binarySha256"`
}

// evidenceReport is the sanitized per-run observation report. It binds
// the manifest, the executed implementation head and the observation bytes.
type evidenceReport struct {
	SchemaVersion       int                         `json:"schemaVersion"`
	ContractID          string                      `json:"contractId"`
	ManifestRevision    string                      `json:"manifestRevision"`
	ManifestSHA256      string                      `json:"manifestSha256"`
	Run                 evidenceRun                 `json:"run"`
	ArtifactObservation evidenceArtifactObservation `json:"artifactObservation"`
	Results             []evidenceResult            `json:"results"`
	Evidence            []evidenceTraceRef          `json:"evidence"`
}

type evidenceRun struct {
	ID           string `json:"id"`
	RunnerCommit string `json:"runnerCommit"`
	TestedTree   string `json:"testedTree,omitempty"`
}

type evidenceArtifactObservation struct {
	CandidateRef          string `json:"candidateRef"`
	Provenance            string `json:"provenance"`
	ObservedArchiveSHA256 string `json:"observedArchiveSha256"`
	ObservedBinarySHA256  string `json:"observedBinarySha256"`
}

type evidenceResult struct {
	CaseRef          string   `json:"caseRef"`
	ExecutionState   string   `json:"executionState"`
	AssertionOutcome string   `json:"assertionOutcome"`
	Capability       string   `json:"capability"`
	EvidenceRefs     []string `json:"evidenceRefs"`
}

type evidenceTraceRef struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

// legacyTransitionBinding is the sanitized observation pinned by evidenceSha256.
// Extra metadata is tolerated; these facts are mandatory and must match the
// accepted build, source profile and exact target.
type legacyTransitionBinding struct {
	SchemaVersion                int    `json:"schemaVersion"`
	CaseRef                      string `json:"caseRef"`
	SourceClass                  string `json:"sourceClass"`
	LegacyVersion                string `json:"legacyVersion"`
	LegacyArchiveSHA256          string `json:"legacyArchiveSha256"`
	LegacyBinarySHA256           string `json:"legacyBinarySha256"`
	TargetArtifactID             string `json:"targetArtifactId"`
	TargetArchiveSHA256          string `json:"targetArchiveSha256"`
	TargetBinarySHA256           string `json:"targetBinarySha256"`
	ConsumerImplementationSHA256 string `json:"consumerImplementationSha256"`
	SourcePreserved              bool   `json:"sourcePreserved"`
	LegacyReadVerified           bool   `json:"legacyReadVerified"`
	StagingV8Verified            bool   `json:"stagingV8Verified"`
	DestinationVerified          bool   `json:"destinationVerified"`
	GatewayPending               bool   `json:"gatewayPending"`
}

// verifyLegacyTransition requires the separately pinned manifest/report/
// observation chain. Accepted source/profile evidence cannot substitute for it,
// and the pinned case refs remain independent of the original profile report.
func verifyLegacyTransition(files fs.FS, record productionRecord, consumer consumerRecord) error {
	binding := consumer.LegacyTransition
	if binding == nil || binding.Profile != acceptedLegacyTransitionProfile || binding.CaseRef != acceptedLegacyTransitionCaseRef ||
		!hexDigest(binding.ManifestSHA256, 64) || !hexDigest(binding.ReportSHA256, 64) ||
		!hexDigest(binding.ImplementationHead, 40) || !hexDigest(binding.EvidenceSHA256, 64) {
		return errProductionQualification
	}
	if !slices.ContainsFunc(consumer.Evidence, func(evidence profileEvidence) bool { return evidence.Profile == binding.Profile }) {
		return errProductionQualification
	}
	// The consumer binding must name exactly the legacy source profile this
	// build accepts. This is an exact profile, not a version range.
	if binding.LegacyVersion != cpa.AcceptedLegacyManagedVersion ||
		binding.LegacyArchiveSHA256 != cpa.AcceptedLegacyManagedArchiveSHA256 ||
		binding.LegacyBinarySHA256 != cpa.AcceptedLegacyManagedBinarySHA256 {
		return errProductionQualification
	}
	manifestData, err := pinnedEvidence(files, binding.ManifestSHA256, ".json")
	if err != nil {
		return err
	}
	var manifest legacyTransitionManifest
	if json.Unmarshal(manifestData, &manifest) != nil || manifest.SchemaVersion != 1 ||
		manifest.ContractID != "cpamp-cpa-v8-qualification" || manifest.Revision == "" ||
		manifest.Candidate.DeclaredSHA256 != record.ArchiveSHA256 ||
		manifest.Candidate.ArtifactID != string(record.ArtifactID) ||
		manifest.LegacySource.Version != binding.LegacyVersion ||
		manifest.LegacySource.ArchiveSHA256 != binding.LegacyArchiveSHA256 ||
		manifest.LegacySource.BinarySHA256 != binding.LegacyBinarySHA256 ||
		len(manifest.Profiles) == 0 {
		return errProductionQualification
	}
	profileFound := false
	for _, profile := range manifest.Profiles {
		if profile.ID == binding.Profile {
			profileFound = true
		}
	}
	if !profileFound {
		return errProductionQualification
	}
	caseMatches := 0
	for _, transitionCase := range manifest.Cases {
		if transitionCase.ID != binding.CaseRef {
			continue
		}
		caseMatches++
		if !slices.Contains(transitionCase.ProfileRefs, binding.Profile) || transitionCase.ProofScope == "" {
			return errProductionQualification
		}
	}
	if caseMatches != 1 {
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
		report.ArtifactObservation.ObservedBinarySHA256 != record.BinarySHA256 {
		return errProductionQualification
	}
	resultMatches := 0
	for _, result := range report.Results {
		if result.CaseRef != binding.CaseRef {
			continue
		}
		resultMatches++
		if result.ExecutionState != "completed" || result.AssertionOutcome != "pass" ||
			(result.Capability != "supported" && result.Capability != "limited") ||
			!slices.Contains(result.EvidenceRefs, binding.CaseRef) {
			return errProductionQualification
		}
	}
	if resultMatches != 1 {
		return errProductionQualification
	}
	evidenceBound := false
	for _, evidence := range report.Evidence {
		if evidence.ID == binding.CaseRef && evidence.Kind == "sanitized-trace" && evidence.SHA256 == binding.EvidenceSHA256 {
			evidenceBound = true
		}
	}
	if !evidenceBound {
		return errProductionQualification
	}
	observationData, err := pinnedEvidence(files, binding.EvidenceSHA256, ".json")
	if err != nil {
		return err
	}
	var observation legacyTransitionBinding
	if json.Unmarshal(observationData, &observation) != nil || observation.SchemaVersion != 1 ||
		observation.CaseRef != binding.CaseRef ||
		observation.SourceClass != string(bootstrap.AdoptLegacyManaged) ||
		observation.LegacyVersion != binding.LegacyVersion ||
		observation.LegacyArchiveSHA256 != binding.LegacyArchiveSHA256 ||
		observation.LegacyBinarySHA256 != binding.LegacyBinarySHA256 ||
		observation.TargetArtifactID != string(record.ArtifactID) ||
		observation.TargetArchiveSHA256 != record.ArchiveSHA256 ||
		observation.TargetBinarySHA256 != record.BinarySHA256 ||
		observation.ConsumerImplementationSHA256 != consumerImplementationSHA256() ||
		!observation.SourcePreserved || !observation.LegacyReadVerified || !observation.StagingV8Verified ||
		!observation.DestinationVerified || !observation.GatewayPending {
		return errProductionQualification
	}
	return nil
}
