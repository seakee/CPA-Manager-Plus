package freshstrategy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

func canonicalLegacyTransitionManifest(t *testing.T) ([]byte, legacyTransitionManifest) {
	t.Helper()
	manifest := legacyTransitionManifest{
		SchemaVersion: 1,
		ContractID:    "cpamp-cpa-v8-qualification",
		Revision:      acceptedLegacyTransitionCaseRef,
		Candidate: evidenceCandidate{
			ID:             "CPA8-8.0.55-linux-amd64-plugin",
			DeclaredSHA256: realCPA8ArchiveSHA256,
			ArtifactID:     "sha256:" + realCPA8BinarySHA256,
		},
		Profiles: []evidenceProfile{{ID: acceptedLegacyTransitionProfile}},
		Cases: []evidenceCase{{
			ID:          acceptedLegacyTransitionCaseRef,
			ProfileRefs: []string{acceptedLegacyTransitionProfile},
			ProofScope:  "fresh-legacy-staging-transition",
		}},
		LegacySource: evidenceSource{
			Version:       cpa.AcceptedLegacyManagedVersion,
			ArchiveSHA256: cpa.AcceptedLegacyManagedArchiveSHA256,
			BinarySHA256:  cpa.AcceptedLegacyManagedBinarySHA256,
		},
	}
	return encodeJSON(t, manifest), manifest
}

// producerLegacyTransitionManifest returns the canonical manifest for the
// current accepted legacy_managed v8 profile, verifying exact byte identity
// whenever the content-addressed manifest is already checked into evidence/.
func producerLegacyTransitionManifest(t *testing.T) (string, []byte, legacyTransitionManifest) {
	t.Helper()
	canonicalData, canonical := canonicalLegacyTransitionManifest(t)
	name := "qualification/evidence/" + sha256Hex(canonicalData) + ".json"
	if checkedIn, err := fs.ReadFile(productionEvidence, name); err == nil && !bytes.Equal(checkedIn, canonicalData) {
		t.Fatalf("checked-in transition manifest %s drifted from canonical bytes", name)
	}
	return name, canonicalData, canonical
}

// embeddedLegacyTransitionManifest locates the checked-in, content-addressed
// case definition for the current accepted legacy_managed source profile.
// Historical manifests for superseded source profiles remain preserved in
// evidence/ and are ignored here rather than deleted.
func embeddedLegacyTransitionManifest(t *testing.T) (string, []byte, legacyTransitionManifest) {
	t.Helper()
	names, err := fs.Glob(productionEvidence, "qualification/evidence/*.json")
	if err != nil {
		t.Fatal(err)
	}
	wantSource := evidenceSource{
		Version:       cpa.AcceptedLegacyManagedVersion,
		ArchiveSHA256: cpa.AcceptedLegacyManagedArchiveSHA256,
		BinarySHA256:  cpa.AcceptedLegacyManagedBinarySHA256,
	}
	var foundName string
	var foundData []byte
	var found legacyTransitionManifest
	for _, name := range names {
		data, err := fs.ReadFile(productionEvidence, name)
		if err != nil {
			t.Fatal(err)
		}
		var manifest legacyTransitionManifest
		if json.Unmarshal(data, &manifest) != nil || manifest.Candidate.DeclaredSHA256 != realCPA8ArchiveSHA256 ||
			manifest.LegacySource != wantSource {
			continue
		}
		for _, transitionCase := range manifest.Cases {
			if transitionCase.ID == acceptedLegacyTransitionCaseRef {
				if foundName != "" {
					t.Fatal("multiple legacy transition manifests")
				}
				foundName, foundData, found = name, data, manifest
			}
		}
	}
	if foundName == "" {
		t.Fatal("checked-in legacy transition manifest is missing")
	}
	return foundName, foundData, found
}

func TestCheckedInLegacyTransitionManifestBindsAcceptedSourceProfile(t *testing.T) {
	name, data, manifest := embeddedLegacyTransitionManifest(t)
	digest := sha256.Sum256(data)
	if path.Base(name) != hex.EncodeToString(digest[:])+".json" {
		t.Fatal("legacy transition manifest is not content-addressed")
	}
	if manifest.SchemaVersion != 1 || manifest.ContractID != "cpamp-cpa-v8-qualification" || manifest.Revision == "" {
		t.Fatalf("invalid transition manifest identity: %+v", manifest)
	}
	if manifest.Candidate.ID == "" || manifest.Candidate.DeclaredSHA256 != realCPA8ArchiveSHA256 ||
		manifest.Candidate.ArtifactID != "sha256:"+realCPA8BinarySHA256 {
		t.Fatalf("transition manifest target drifted: %+v", manifest.Candidate)
	}
	if manifest.LegacySource.Version != cpa.AcceptedLegacyManagedVersion ||
		manifest.LegacySource.ArchiveSHA256 != cpa.AcceptedLegacyManagedArchiveSHA256 ||
		manifest.LegacySource.BinarySHA256 != cpa.AcceptedLegacyManagedBinarySHA256 {
		t.Fatalf("transition manifest source profile drifted: %+v", manifest.LegacySource)
	}
	if len(manifest.Profiles) != 1 || manifest.Profiles[0].ID != acceptedLegacyTransitionProfile {
		t.Fatalf("transition manifest profile drifted: %+v", manifest.Profiles)
	}
	if len(manifest.Cases) != 1 || manifest.Cases[0].ID != acceptedLegacyTransitionCaseRef ||
		len(manifest.Cases[0].ProfileRefs) != 1 || manifest.Cases[0].ProfileRefs[0] != acceptedLegacyTransitionProfile ||
		manifest.Cases[0].ProofScope == "" {
		t.Fatalf("transition manifest case drifted: %+v", manifest.Cases)
	}
}

func TestGeneratedLegacyTransitionEvidenceSatisfiesProductionLoader(t *testing.T) {
	record, files := acceptedConsumerFixture(t, 3)
	_, manifestData, manifest := producerLegacyTransitionManifest(t)
	artifact := model.RuntimeArtifactID(manifest.Candidate.ArtifactID)
	head, tree := strings.Repeat("b", 40), strings.Repeat("c", 40)
	observation, report, _ := legacyTransitionEvidencePayload(t, manifestData, manifest, artifact, true, true, head, tree, head)
	files["qualification/evidence/"+sha256Hex(manifestData)+".json"] = &fstest.MapFile{Data: manifestData}
	files["qualification/evidence/"+sha256Hex(report)+".json"] = &fstest.MapFile{Data: report}
	files["qualification/evidence/"+sha256Hex(observation)+".json"] = &fstest.MapFile{Data: observation}
	record.Consumers[3].LegacyTransition = &legacyTransitionEvidence{
		Profile: acceptedLegacyTransitionProfile, CaseRef: acceptedLegacyTransitionCaseRef,
		ManifestSHA256: sha256Hex(manifestData), ReportSHA256: sha256Hex(report),
		ImplementationHead: head, EvidenceSHA256: sha256Hex(observation),
		LegacyVersion: cpa.AcceptedLegacyManagedVersion, LegacyArchiveSHA256: cpa.AcceptedLegacyManagedArchiveSHA256,
		LegacyBinarySHA256: cpa.AcceptedLegacyManagedBinarySHA256,
	}
	writeSyntheticRecord(t, files, record)
	consumers, err := loadProductionQualification(files)
	if err != nil || !consumers[consumerKey{bootstrap.AdoptExisting, bootstrap.AdoptLegacyManaged}].Qualification.StagingV8TransitionQualified {
		t.Fatalf("generated transition evidence rejected by its own loader: %v", err)
	}
}
