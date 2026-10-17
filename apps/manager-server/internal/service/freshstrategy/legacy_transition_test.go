package freshstrategy

import (
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

// embeddedLegacyTransitionManifest locates the checked-in, content-addressed
// case definition. It is review input, never production authority.
func embeddedLegacyTransitionManifest(t *testing.T) (string, []byte, legacyTransitionManifest) {
	t.Helper()
	names, err := fs.Glob(productionEvidence, "qualification/evidence/*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		data, err := fs.ReadFile(productionEvidence, name)
		if err != nil {
			t.Fatal(err)
		}
		var manifest legacyTransitionManifest
		if json.Unmarshal(data, &manifest) != nil {
			continue
		}
		for _, transitionCase := range manifest.Cases {
			if transitionCase.ID == acceptedLegacyTransitionCaseRef {
				return name, data, manifest
			}
		}
	}
	t.Fatal("checked-in legacy transition manifest is missing")
	return "", nil, legacyTransitionManifest{}
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
	_, manifestData, manifest := embeddedLegacyTransitionManifest(t)
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
