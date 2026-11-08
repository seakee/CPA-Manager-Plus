package security

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

func TestAdminCredentialVerifiesOnlyAdminKey(t *testing.T) {
	const adminKey = "cpamp_test_key_0123456789abcdef"

	credential, err := NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	if !VerifyAdminKey(credential, adminKey) {
		t.Fatal("admin key did not verify")
	}
	if VerifyAdminKey(credential, "management-key") {
		t.Fatal("cpa management key should not verify as admin key")
	}
	if strings.Contains(credential.KeyHash, adminKey) || strings.Contains(credential.Salt, adminKey) {
		t.Fatalf("credential contains admin key material: %#v", credential)
	}
}

func TestAdminCredentialStillAcceptsLegacyAdminKeyPrefix(t *testing.T) {
	const adminKey = "cmp_admin_test_key_0123456789abcdef"

	credential, err := NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	if !VerifyAdminKey(credential, adminKey) {
		t.Fatal("legacy admin key did not verify")
	}
}

func TestAdminCredentialStructureMatchesAuthentication(t *testing.T) {
	const key = "cpamp_structure_test"
	current, err := NewAdminCredential(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*model.AdminCredential)
		valid  bool
	}{
		{"current", func(*model.AdminCredential) {}, true},
		{"legacy zero iterations", func(c *model.AdminCredential) { c.Iterations = 0 }, true},
		{"legacy negative iterations", func(c *model.AdminCredential) { c.Iterations = -1 }, true},
		{"legacy PBKDF2", func(c *model.AdminCredential) {
			c.Iterations = 1000
			salt, _ := base64.RawStdEncoding.DecodeString(c.Salt)
			digest, err := hashAdminKey(key, salt, c.Iterations)
			if err != nil {
				t.Fatal(err)
			}
			c.KeyHash = base64.RawStdEncoding.EncodeToString(digest)
		}, true},
		{"unsupported version", func(c *model.AdminCredential) { c.Version = 2 }, false},
		{"missing version", func(c *model.AdminCredential) { c.Version = 0 }, false},
		{"empty salt", func(c *model.AdminCredential) { c.Salt = "" }, false},
		{"invalid salt", func(c *model.AdminCredential) { c.Salt = "secret!" }, false},
		{"whitespace salt", func(c *model.AdminCredential) { c.Salt = " " + c.Salt }, false},
		{"short digest", func(c *model.AdminCredential) { c.KeyHash = "a2V5" }, false},
		{"long digest", func(c *model.AdminCredential) { c.KeyHash = base64.RawStdEncoding.EncodeToString(make([]byte, 33)) }, false},
		{"invalid digest", func(c *model.AdminCredential) { c.KeyHash = "secret!" }, false},
		{"whitespace digest", func(c *model.AdminCredential) { c.KeyHash += " " }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			credential := current
			test.change(&credential)
			if IsValidAdminCredential(credential) != test.valid || VerifyAdminKey(credential, key) != test.valid {
				t.Fatalf("structural/real authentication disagreement; want valid=%v", test.valid)
			}
		})
	}
}

func TestGenerateAdminKeyUsesExpectedPrefixAndEntropyLength(t *testing.T) {
	adminKey, err := GenerateAdminKey()
	if err != nil {
		t.Fatalf("generate admin key: %v", err)
	}
	if !strings.HasPrefix(adminKey, "cpamp_") {
		t.Fatalf("admin key = %q", adminKey)
	}
	secret := strings.TrimPrefix(adminKey, "cpamp_")
	if got, want := len(secret), 32; got != want {
		t.Fatalf("random length = %d, want %d", got, want)
	}
	if !isAlnum(secret) {
		t.Fatalf("admin key contains non-alphanumeric characters: %q", adminKey)
	}
}

func TestRandomAlnumRejectsInvalidLength(t *testing.T) {
	if value, err := randomAlnum(0); err == nil || value != "" {
		t.Fatalf("randomAlnum(0) = %q, %v; want empty value and error", value, err)
	}
}

func TestProtectorEncryptsAndDecryptsString(t *testing.T) {
	protector, err := NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}

	encrypted, err := protector.ProtectString("management-key")
	if err != nil {
		t.Fatalf("protect string: %v", err)
	}
	if encrypted == "management-key" || !IsProtected(encrypted) {
		t.Fatalf("encrypted value = %q", encrypted)
	}

	plaintext, err := protector.UnprotectString(encrypted)
	if err != nil {
		t.Fatalf("unprotect string: %v", err)
	}
	if plaintext != "management-key" {
		t.Fatalf("plaintext = %q", plaintext)
	}

	otherProtector, err := NewProtector([]byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatalf("create other protector: %v", err)
	}
	if _, err := otherProtector.UnprotectString(encrypted); err == nil {
		t.Fatal("decrypt with wrong data key succeeded")
	}
}

// TestProtectorAlwaysEncryptsPlaintextLookingLikeCiphertext proves that a real
// CPA Management Key whose plaintext happens to start with the encrypted-value
// prefix is still encrypted at rest. The legacy implementation short-circuited
// ProtectString whenever IsProtected(value) was true, so such a key would be
// persisted verbatim and then misread as ciphertext on the next load.
func TestProtectorAlwaysEncryptsPlaintextLookingLikeCiphertext(t *testing.T) {
	protector, err := NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}

	const plaintextKey = "enc:v1:real-cpa-management-key"
	encrypted, err := protector.ProtectString(plaintextKey)
	if err != nil {
		t.Fatalf("protect plaintext that resembles ciphertext: %v", err)
	}
	if encrypted == plaintextKey {
		t.Fatalf("ProtectString returned the plaintext verbatim: %q", encrypted)
	}
	if !IsProtected(encrypted) {
		t.Fatalf("encrypted value is not a protected envelope: %q", encrypted)
	}

	roundtrip, err := protector.UnprotectString(encrypted)
	if err != nil {
		t.Fatalf("unprotect roundtrip: %v", err)
	}
	if roundtrip != plaintextKey {
		t.Fatalf("roundtrip = %q, want %q", roundtrip, plaintextKey)
	}
}

// TestProtectorUnprotectsValidLegacyCiphertext ensures values produced by the
// current envelope format still decrypt after the prefix-collision fix.
func TestProtectorUnprotectsValidLegacyCiphertext(t *testing.T) {
	protector, err := NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	encrypted, err := protector.ProtectString("legacy-secret")
	if err != nil {
		t.Fatalf("protect legacy secret: %v", err)
	}
	plaintext, err := protector.UnprotectString(encrypted)
	if err != nil {
		t.Fatalf("unprotect legacy ciphertext: %v", err)
	}
	if plaintext != "legacy-secret" {
		t.Fatalf("legacy plaintext = %q", plaintext)
	}
}

// TestProtectorRejectsMalformedEnvelopePlaintext verifies that a plaintext
// value such as "enc:v1:not-ciphertext" (which is not a structurally valid
// envelope) is treated as legacy plaintext by the migration path rather than
// being silently accepted or causing a hard decrypt failure.
func TestIsValidProtectedEnvelope(t *testing.T) {
	protector, err := NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	valid, _ := protector.ProtectString("payload")
	if !IsValidProtectedEnvelope(valid) {
		t.Fatalf("valid ciphertext not recognized as envelope: %q", valid)
	}
	if IsValidProtectedEnvelope("enc:v1:not-ciphertext") {
		t.Fatal("malformed enc:v1 plaintext incorrectly recognized as envelope")
	}
	if IsValidProtectedEnvelope("enc:v1:real-key") {
		t.Fatal("prefix-only plaintext incorrectly recognized as envelope")
	}
	if IsValidProtectedEnvelope("") {
		t.Fatal("empty value recognized as envelope")
	}
	if IsValidProtectedEnvelope("management-key") {
		t.Fatal("ordinary plaintext recognized as envelope")
	}
	shortNonce := base64.RawStdEncoding.EncodeToString(make([]byte, encryptedNonceSize-1))
	validTagOnly := base64.RawStdEncoding.EncodeToString(make([]byte, encryptedTagSize))
	if IsValidProtectedEnvelope(encryptedPrefix + shortNonce + ":" + validTagOnly) {
		t.Fatal("envelope with a short nonce recognized as valid")
	}
	validNonce := base64.RawStdEncoding.EncodeToString(make([]byte, encryptedNonceSize))
	shortCiphertext := base64.RawStdEncoding.EncodeToString(make([]byte, encryptedTagSize-1))
	if IsValidProtectedEnvelope(encryptedPrefix + validNonce + ":" + shortCiphertext) {
		t.Fatal("envelope shorter than the AES-GCM tag recognized as valid")
	}
}

// TestUnprotectStringFailClosed proves that a value which is a structurally
// valid envelope but fails authenticated decryption (wrong key or corrupted
// ciphertext) produces an error instead of being returned as plaintext.
func TestUnprotectStringFailClosed(t *testing.T) {
	protector, err := NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}
	encrypted, err := protector.ProtectString("secret-value")
	if err != nil {
		t.Fatalf("protect secret: %v", err)
	}

	other, err := NewProtector([]byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatalf("create other protector: %v", err)
	}
	if _, err := other.UnprotectString(encrypted); err == nil {
		t.Fatal("wrong data key decryption succeeded; must fail closed")
	}

	corrupted := encrypted[:len(encrypted)-4] + "AAAA"
	if _, err := protector.UnprotectString(corrupted); err == nil {
		t.Fatal("corrupted ciphertext decryption succeeded; must fail closed")
	}
}

// TestMigrateLegacyPlaintextEnvelope exercises the repository-side migration
// helper that distinguishes legacy plaintext from real ciphertext at the
// storage boundary. A malformed enc:v1 plaintext must be re-encrypted; a valid
// ciphertext must decrypt; a structurally valid but undecryptable value must
// error fail-closed.
func TestMigrateLegacyPlaintextEnvelope(t *testing.T) {
	protector, err := NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("create protector: %v", err)
	}

	// Malformed enc:v1 plaintext is legacy plaintext and should be re-encrypted.
	migrated, err := MigrateLegacyValue(protector, "enc:v1:legacy-real-key")
	if err != nil {
		t.Fatalf("migrate malformed legacy plaintext: %v", err)
	}
	if migrated == "enc:v1:legacy-real-key" {
		t.Fatal("legacy plaintext was not re-encrypted")
	}
	if plaintext, err := protector.UnprotectString(migrated); err != nil || plaintext != "enc:v1:legacy-real-key" {
		t.Fatalf("migrated legacy plaintext roundtrip = %q, %v", plaintext, err)
	}

	// A valid ciphertext is returned as its decrypted plaintext.
	encrypted, err := protector.ProtectString("already-encrypted")
	if err != nil {
		t.Fatalf("protect already encrypted: %v", err)
	}
	decrypted, err := MigrateLegacyValue(protector, encrypted)
	if err != nil {
		t.Fatalf("migrate valid ciphertext: %v", err)
	}
	if decrypted != "already-encrypted" {
		t.Fatalf("migrated ciphertext = %q", decrypted)
	}

	// Structurally valid envelope but wrong key must fail closed.
	other, err := NewProtector([]byte("abcdef0123456789abcdef0123456789"))
	if err != nil {
		t.Fatalf("create other protector: %v", err)
	}
	otherEncrypted, err := other.ProtectString("other-key-secret")
	if err != nil {
		t.Fatalf("protect with other key: %v", err)
	}
	if _, err := MigrateLegacyValue(protector, otherEncrypted); err == nil {
		t.Fatal("migrating undecryptable envelope succeeded; must fail closed")
	}

	// Empty value stays empty.
	if migrated, err := MigrateLegacyValue(protector, ""); err != nil || migrated != "" {
		t.Fatalf("migrate empty = %q, %v", migrated, err)
	}
}

func TestLoadOrCreateDataKeyCreatesStableRestrictedFile(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "data.key")

	first, created, err := LoadOrCreateDataKey("", keyPath)
	if err != nil {
		t.Fatalf("create data key: %v", err)
	}
	if !created || len(first) != 32 {
		t.Fatalf("created=%v len=%d", created, len(first))
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat data key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("data key permissions = %o, want 600", info.Mode().Perm())
	}

	second, created, err := LoadOrCreateDataKey("", keyPath)
	if err != nil {
		t.Fatalf("load data key: %v", err)
	}
	if created || string(second) != string(first) {
		t.Fatalf("second created=%v key stable=%v", created, string(second) == string(first))
	}
}

func TestLoadOrCreateDataKeyTightensExistingPermissiveFile(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "data.key")
	rawKey := []byte("0123456789abcdef0123456789abcdef")
	encoded := base64.RawStdEncoding.EncodeToString(rawKey) + "\n"
	if err := os.WriteFile(keyPath, []byte(encoded), 0o644); err != nil {
		t.Fatalf("seed permissive data.key: %v", err)
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod 0644 data.key: %v", err)
	}

	// Read-only LoadExistingDataKey must not mutate permissions.
	if _, err := LoadExistingDataKey("", keyPath); err != nil {
		t.Fatalf("LoadExistingDataKey: %v", err)
	}
	infoBefore, err := os.Stat(keyPath)
	if err != nil || infoBefore.Mode().Perm() != 0o644 {
		t.Fatalf("mode after LoadExistingDataKey = %#04o, want 0644 (err=%v)", infoBefore.Mode().Perm(), err)
	}

	loaded, created, err := LoadOrCreateDataKey("", keyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateDataKey on existing 0644 file: %v", err)
	}
	if created || string(loaded) != string(rawKey) {
		t.Fatalf("created=%v loaded=%x, want false / %x", created, loaded, rawKey)
	}
	infoAfter, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat tightened data.key: %v", err)
	}
	if got := infoAfter.Mode().Perm(); got != 0o600 {
		t.Fatalf("tightened data.key mode = %#04o, want 0600", got)
	}
}

func isAlnum(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= 'a' && r <= 'z' {
			continue
		}
		return false
	}
	return value != ""
}
