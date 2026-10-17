package security

import "testing"

func TestFreshAdminKeyValidation(t *testing.T) {
	for _, key := range []string{"", "short", "123456789012345678901234567890", "aaaaaaaaaaaaaaaaaaaaaaaaaaaa", "    cpamp_ValidAuthority123456789!", "cpamp_AuthorityWith\nControl123456789!"} {
		if ValidateFreshAdminKey(key) == nil {
			t.Fatal("weak key accepted")
		}
	}
	for range 20 {
		key, err := GenerateAdminKey()
		if err != nil {
			t.Fatal(err)
		}
		if err = ValidateFreshAdminKey(key); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateFreshAdminKey("cpamp_ManualAuthority0123456789!"); err != nil {
		t.Fatal(err)
	}
}
