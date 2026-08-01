package quotacooldown

import "testing"

func TestParseCooldownEvidenceDropsMismatchedRecoverySource(t *testing.T) {
	evidence := parseCooldownEvidence(
		`{"provider":"xai","code":"subscription:free-usage-exhausted","recover_at_ms":1900000000000,"recover_at_estimated":false}`,
		2_000_000_000_000,
	)
	if evidence == nil {
		t.Fatal("valid xAI evidence was rejected")
	}
	if evidence.RecoverAtMS != 0 || evidence.RecoverAtEstimated {
		t.Fatalf("mismatched recovery source was exposed: %#v", evidence)
	}
}

func TestParseCooldownEvidenceRejectsOtherProviderCodes(t *testing.T) {
	evidence := parseCooldownEvidence(
		`{"provider":"xai","code":"rate-limited","model":"sk-sensitive-token"}`,
		2_000_000_000_000,
	)
	if evidence != nil {
		t.Fatalf("unrelated provider evidence was exposed: %#v", evidence)
	}
}
