//go:build windows

package desktopmonitor

import "testing"

func TestApprovedMockupDimensions(t *testing.T) {
	w := &nativeWindow{}
	if w.width() != 354 || w.height() != 628 {
		t.Fatalf("expanded %dx%d, want approved 354x628 CSS px", w.width(), w.height())
	}
	w.prefs.Compact = true
	if w.width() != 354 || w.height() != 96 {
		t.Fatalf("compact %dx%d, want approved 354x96 CSS px", w.width(), w.height())
	}
}

func TestRememberedRemoteHTTPKeyKeepsURLBinding(t *testing.T) {
	s := preferencesStore{dir: t.TempDir()}
	const remote = "http://185.222.43.103:18317"
	if err := s.SaveKey(remote, "synthetic-key"); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadKey(remote)
	if err != nil || got != "synthetic-key" {
		t.Fatalf("remembered key: %q %v", got, err)
	}
	if _, err = s.LoadKey("http://another.example:18317"); err == nil {
		t.Fatal("key crossed Manager origins")
	}
}
