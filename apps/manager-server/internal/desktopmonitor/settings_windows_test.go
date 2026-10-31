//go:build windows

package desktopmonitor

import (
	"bytes"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsUseProtectedCurrentUserACL(t *testing.T) {
	s := preferencesStore{dir: filepath.Join(t.TempDir(), "monitor")}
	if err := s.SaveKey("http://127.0.0.1:18317", "acl-fixture-key"); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{s.dir, filepath.Join(s.dir, "key.dpapi")} {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		dacl := sd.String()
		if !strings.HasPrefix(dacl, "D:P") || !strings.Contains(dacl, user.User.Sid.String()) || strings.Contains(dacl, ";;;BU)") || strings.Contains(dacl, ";;;WD)") || strings.Contains(dacl, ";;;AU)") {
			t.Fatalf("not user-private ACL: %s", dacl)
		}
	}
}

func TestPreferencesRoundTripCorruptAndClamp(t *testing.T) {
	s := preferencesStore{dir: t.TempDir()}
	p := WindowPreferences{BaseURL: "https://manager.example/prefix", X: -40000, Y: 90000, HasPosition: true, Compact: true, Topmost: false}
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil || got != p {
		t.Fatalf("roundtrip %+v %v", got, err)
	}
	got = clampPreferences(got, 420, 620, workRect{Left: -1920, Top: 0, Right: 0, Bottom: 1080})
	if got.X != -1920 || got.Y != 460 {
		t.Fatalf("offscreen %+v", got)
	}
	for _, bad := range []string{`{`, `{"base_url":"https://user:SECRET@example.com"}`, `{"x":1e100}`} {
		if err := os.WriteFile(filepath.Join(s.dir, "preferences.json"), []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		got, err = s.Load()
		if err == nil || !got.Topmost || got.HasPosition || got.BaseURL != "" {
			t.Fatalf("corrupt %+v %v", got, err)
		}
	}
}
func TestDPAPICurrentUserScopedAndOptionalPersistence(t *testing.T) {
	s := preferencesStore{dir: t.TempDir()}
	const base = "http://127.0.0.1:8080/prefix"
	const secret = "DPAPI-test-secret-never-plaintext"
	if err := s.SaveKey(base, secret); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(WindowPreferences{BaseURL: base, Topmost: true}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("plaintext on disk")
		}
	}
	key, err := s.LoadKey(base)
	if err != nil || key != secret {
		t.Fatalf("DPAPI %v", err)
	}
	if _, err = s.LoadKey("https://different.example"); err == nil {
		t.Fatal("key crossed scope")
	}
	if err = s.ForgetKey(); err != nil {
		t.Fatal(err)
	}
	if key, err = s.LoadKey(base); key != "" || !os.IsNotExist(err) {
		t.Fatalf("key not forgotten: %v", err)
	}
	if err = os.WriteFile(filepath.Join(s.dir, "key.dpapi"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LoadKey(base); err == nil {
		t.Fatal("accepted corrupt ciphertext")
	}
}
