//go:build windows

package desktopmonitor

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WindowPreferences is intentionally secret-free. Key ciphertext is a separate
// DPAPI current-user file bound to the normalized Manager base URL.
type WindowPreferences struct {
	BaseURL     string `json:"base_url"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
	HasPosition bool   `json:"has_position"`
	Compact     bool   `json:"compact"`
	Topmost     bool   `json:"topmost"`
	// AllowPlainHTTP is a deliberate opt-in for a remote Manager that only
	// serves plain HTTP; the admin key is then sent unencrypted.
	AllowPlainHTTP bool `json:"allow_plain_http,omitempty"`
}
type workRect struct{ Left, Top, Right, Bottom int }

func clampPreferences(p WindowPreferences, width, height int, r workRect) WindowPreferences {
	if !p.HasPosition {
		p.X = r.Right - width - 24
		p.Y = r.Top + 24
		p.HasPosition = true
	}
	p.X = max(r.Left, min(p.X, r.Right-width))
	p.Y = max(r.Top, min(p.Y, r.Bottom-height))
	return p
}

type preferencesStore struct{ dir string }

func defaultStore() (preferencesStore, error) {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return preferencesStore{}, errors.New("LOCALAPPDATA is unavailable")
	}
	return preferencesStore{dir: filepath.Join(root, "CPA-Manager-Plus", "monitor")}, nil
}
func (s preferencesStore) Load() (WindowPreferences, error) {
	defaults := WindowPreferences{Topmost: true}
	b, err := readSettings(filepath.Join(s.dir, "preferences.json"), 16384)
	if err != nil {
		return defaults, err
	}
	p := defaults
	if json.Unmarshal(b, &p) != nil {
		return defaults, errors.New("invalid window preferences")
	}
	if p.BaseURL != "" {
		base, err := normalizeBaseURLWith(p.BaseURL, p.AllowPlainHTTP)
		if err != nil {
			return defaults, errors.New("invalid saved Manager URL")
		}
		p.BaseURL = base
	}
	return p, nil
}
func (s preferencesStore) Save(p WindowPreferences) error {
	if p.BaseURL != "" {
		base, err := normalizeBaseURLWith(p.BaseURL, p.AllowPlainHTTP)
		if err != nil {
			return err
		}
		p.BaseURL = base
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.write("preferences.json", b)
}
func readSettings(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("settings file too large")
	}
	return b, nil
}

// privateACL protects against inherited Users/Everyone grants; Unix mode bits
// do not enforce a Windows DACL. SYSTEM is retained for normal OS backup.
func privateACL(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
func (s preferencesStore) write(name string, b []byte) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	if err := privateACL(s.dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, "monitor-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = privateACL(tmp); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(s.dir, name))
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

type savedKey struct {
	Base string `json:"base"`
	Key  string `json:"key"`
}

func (s preferencesStore) SaveKey(base, key string) error {
	normalized, err := normalizeBaseURLWith(base, true)
	if err != nil {
		return err
	}
	if key == "" {
		return s.ForgetKey()
	}
	b, err := json.Marshal(savedKey{normalized, key})
	if err != nil {
		return err
	}
	defer clear(b)
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	// CRYPTPROTECT_UI_FORBIDDEN only: deliberately NOT LOCAL_MACHINE.
	if err = windows.CryptProtectData(&in, nil, nil, 0, nil, 1, &out); err != nil {
		return errors.New("cannot protect administrator key")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return s.write("key.dpapi", unsafe.Slice(out.Data, int(out.Size)))
}
func (s preferencesStore) LoadKey(base string) (string, error) {
	if normalized, err := normalizeBaseURLWith(base, true); err == nil {
		base = normalized
	}
	b, err := readSettings(filepath.Join(s.dir, "key.dpapi"), 65536)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errors.New("empty encrypted key")
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	if err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, 1, &out); err != nil {
		return "", errors.New("cannot decrypt administrator key")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	plain := unsafe.Slice(out.Data, int(out.Size))
	defer clear(plain)
	var v savedKey
	if json.Unmarshal(plain, &v) != nil || v.Base != base {
		return "", errors.New("encrypted key belongs to another Manager URL")
	}
	return v.Key, nil
}
func (s preferencesStore) ForgetKey() error {
	err := os.Remove(filepath.Join(s.dir, "key.dpapi"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
