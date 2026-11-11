package model

import (
	"strings"
	"testing"
)

// The install-generated Client Key is the v1.x shape: "sk-" and 64
// [A-Za-z0-9]. Every other shape is a different credential and must never be
// staged as a default destination's only key.
func TestInitialClientKeyShapeIsExact(t *testing.T) {
	valid := "sk-" + strings.Repeat("aB3", 21) + "Z"
	if len(valid) != InitialClientKeyLength || !ValidInitialClientKey(valid) {
		t.Fatalf("valid install key rejected: %q", valid)
	}
	for name, value := range map[string]string{
		"empty":          "",
		"prefix only":    "sk-",
		"short":          valid[:len(valid)-1],
		"long":           valid + "A",
		"no prefix":      "cp" + valid[2:],
		"workspace key":  "cpamp_sk_" + strings.Repeat("a", 64),
		"hex uppercase":  "sk-" + strings.Repeat("A", 63) + "-",
		"space":          "sk-" + strings.Repeat("a", 63) + " ",
		"newline":        valid[:len(valid)-1] + "\n",
		"unicode digit":  "sk-" + strings.Repeat("a", 63) + "\u00b2",
		"separator char": "sk-" + strings.Repeat("a", 63) + "_",
	} {
		if ValidInitialClientKey(value) {
			t.Fatalf("%s accepted as an install key", name)
		}
	}
}

func TestWorkspaceClientKeyShapeIsExact(t *testing.T) {
	valid := "cpamp_sk_" + strings.Repeat("ab", 32)
	if !ValidWorkspaceClientKey(valid) || !ValidCanonicalClientKey(valid) {
		t.Fatalf("valid workspace key rejected: %q", valid)
	}
	for _, value := range []string{
		"cpamp_sk_" + strings.Repeat("ab", 31),
		"cpamp_sk_" + strings.Repeat("A", 64),
		"sk-" + strings.Repeat("a", 64),
		"",
	} {
		if ValidWorkspaceClientKey(value) {
			t.Fatalf("workspace shape accepted %q", value)
		}
	}
	if !ValidCanonicalClientKey("sk-"+strings.Repeat("a", 64)) || ValidCanonicalClientKey("cpamp_sk_"+strings.Repeat("z", 64)) {
		t.Fatal("canonical shape union is wrong")
	}
}

// Only the shape-owning prefix and the last four characters may be rendered.
func TestMaskedClientKeyKeepsOnlyPrefixAndLastFour(t *testing.T) {
	install := "sk-" + strings.Repeat("aB3", 21) + "Z"
	if got := MaskedClientKey(install); got != "sk-…"+install[len(install)-4:] {
		t.Fatalf("install mask = %q", got)
	}
	workspace := "cpamp_sk_" + strings.Repeat("ab", 32)
	if got := MaskedClientKey(workspace); got != "cpamp_sk_…"+workspace[len(workspace)-4:] {
		t.Fatalf("workspace mask = %q", got)
	}
	if MaskedClientKey("not a client key") != "" {
		t.Fatal("invalid secret produced a preview")
	}
}

// A key this Manager did not mint has the source CPA's own shape, so it is
// rendered as the ellipsis and the last four characters it shares with the
// canonical policy — never more than a quarter of the secret.
func TestMaskedImportedClientKeyKeepsTheLastFourOrNothing(t *testing.T) {
	for secret, want := range map[string]string{
		"sk-adopt-source-key-01":     "…y-01",
		"source-client-two":          "…-two",
		"short":                      "…",
		"123456789012345":            "…",
		"1234567890123456":           "…3456",
		"0123456789abcdef0123456789": "…6789",
	} {
		if got := MaskedImportedClientKey(secret); got != want {
			t.Fatalf("imported mask of %q = %q, want %q", secret, got, want)
		}
	}
	// Runes, not bytes: the last four characters of a non-ASCII secret are never
	// a split code point.
	if got := MaskedImportedClientKey("ključ-初-a1ü2-üöä-asdf"); got != "…asdf" {
		t.Fatalf("rune mask = %q", got)
	}
}
