// Package freshkeyapply owns bounded private S1 install and S2 admission.
// The host launcher owns isolation evidence; neither Admin JSON nor an environment
// boolean is proof. No general configuration writer exists.
package freshkeyapply

import (
	"encoding/json"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

const PermitPath = "/run/cpamp-s1/permit.json"

// Permit is the short-lived, per-request authority derived from a committed
// Fresh NewManaged destination. Private profile fields remain populated only
// by the attested launcher.
type Permit struct {
	RunID         string                  `json:"runId"`
	Generation    string                  `json:"generation"`
	ManagementKey string                  `json:"managementKey"`
	IssuedAtMS    int64                   `json:"issuedAtMs"`
	ExpiresAtMS   int64                   `json:"expiresAtMs"`
	S2B           cpa.PrivateProviderSeed `json:"s2b,omitempty"`
	Profile       string                  `json:"-"`
	Identity      string                  `json:"-"`
	ArtifactID    string                  `json:"-"`
	Strategy      string                  `json:"-"`
}

func loadPermit() (Permit, error) {
	var p Permit
	if readHostPermit(PermitPath, &p) != nil || !p.valid(time.Now()) {
		return Permit{}, ErrUnavailable
	}
	return p, nil
}

func readHostPermit(path string, destination any) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return ErrUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return ErrUnavailable
	}
	if !rootOwned(info) {
		return ErrUnavailable
	}
	// The launcher uses a dedicated volume; reject symlink substitution too.
	link, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, link) || link.Mode()&os.ModeSymlink != 0 {
		return ErrUnavailable
	}
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if d.Decode(destination) != nil || d.Decode(&struct{}{}) != io.EOF {
		return ErrUnavailable
	}
	return nil
}

func (p Permit) valid(now time.Time) bool {
	n := now.UnixMilli()
	s2b := os.Getenv("CPAMP_PRIVATE_S2B_CHAT") == "1"
	if s2b {
		if os.Getenv("CPAMP_PRIVATE_S2A_CHAT") == "1" || !p.S2B.Valid() {
			return false
		}
	} else if p.S2B != (cpa.PrivateProviderSeed{}) {
		return false
	}
	return len(p.RunID) >= 16 && p.Generation != "" && len(p.ManagementKey) == 64 && p.IssuedAtMS > 0 && p.IssuedAtMS <= n && p.ExpiresAtMS > n && p.ExpiresAtMS-p.IssuedAtMS <= 600000
}
