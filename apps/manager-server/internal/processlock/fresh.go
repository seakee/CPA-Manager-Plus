package processlock

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

const pendingFresh = "pending_fresh_admission"

// FreshReceipt is host recovery evidence only. It never grants browser access.
type FreshReceipt struct {
	Version  int                     `json:"version"`
	State    string                  `json:"state"`
	Identity bootstrap.FreshIdentity `json:"identity"`
}

func randomID() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}

func (l *Lock) BeginFreshAdmission() (FreshReceipt, error) {
	if !l.createdNew {
		return FreshReceipt{}, errors.New("Fresh admission requires exclusive marker creation")
	}
	if err := l.Validate(); err != nil {
		return FreshReceipt{}, err
	}
	if info, err := l.file.Stat(); err != nil || info.Size() != 0 {
		return FreshReceipt{}, errors.New("Fresh admission already established")
	}
	ids := make([]string, 4)
	for i := range ids {
		var err error
		ids[i], err = randomID()
		if err != nil {
			return FreshReceipt{}, err
		}
	}
	r := FreshReceipt{Version: 1, State: pendingFresh, Identity: bootstrap.FreshIdentity{AdmissionID: ids[0], InstallationID: ids[1], Generation: ids[2], DatabasePath: l.DatabasePath(), OperationID: bootstrap.OperationID("fresh-" + ids[3])}}
	if err := l.writeReceipt(r); err != nil {
		return FreshReceipt{}, err
	}
	// Persist the directory entry as well as the contents before source creation
	// where the platform supports directory fsync. Windows rejects Sync on an
	// opened directory; writeReceipt already fsyncs the marker file itself.
	if runtime.GOOS != "windows" {
		dir, err := os.Open(filepath.Dir(l.Path()))
		if err != nil {
			return FreshReceipt{}, err
		}
		defer dir.Close()
		if err = dir.Sync(); err != nil {
			return FreshReceipt{}, err
		}
	}
	return r, nil
}

func (l *Lock) readFreshReceipt() (*FreshReceipt, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	data := make([]byte, 4097)
	n, err := l.file.ReadAt(data, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	data = data[:n]
	if len(data) == 0 {
		return nil, nil
	}
	if len(data) > 4096 {
		return nil, errors.New("invalid Manager marker")
	}
	var r FreshReceipt
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&r); err != nil {
		return nil, errors.New("invalid Manager marker")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid Manager marker")
	}
	if r.Version != 1 || (r.State != pendingFresh && r.State != "manager") || !r.Identity.Valid() || r.Identity.DatabasePath != l.DatabasePath() {
		return nil, errors.New("invalid Fresh admission receipt")
	}
	for _, id := range []string{r.Identity.AdmissionID, r.Identity.InstallationID, r.Identity.Generation} {
		b, e := hex.DecodeString(id)
		if e != nil || len(b) != 16 {
			return nil, errors.New("invalid Fresh admission identity")
		}
	}
	operation := string(r.Identity.OperationID)
	b, e := hex.DecodeString(strings.TrimPrefix(operation, "fresh-"))
	if !strings.HasPrefix(operation, "fresh-") || e != nil || len(b) != 16 {
		return nil, errors.New("invalid Fresh operation identity")
	}
	return &r, nil
}

func (l *Lock) PendingFreshAdmission() (*FreshReceipt, error) {
	r, err := l.readFreshReceipt()
	if err != nil || r == nil {
		return nil, err
	}
	if r.State == "manager" {
		return nil, nil
	}
	return r, nil
}

// FreshIdentity returns non-secret marker provenance, never authorization.
func (l *Lock) FreshIdentity() (*bootstrap.FreshIdentity, error) {
	r, err := l.readFreshReceipt()
	if err != nil || r == nil {
		return nil, err
	}
	return &r.Identity, nil
}

func (l *Lock) FinalizeFreshAdmission(identity bootstrap.FreshIdentity) error {
	r, err := l.PendingFreshAdmission()
	if err != nil {
		return err
	}
	if r == nil || r.Identity != identity {
		return errors.New("Fresh admission receipt mismatch")
	}
	r.State = "manager"
	return l.writeReceipt(*r)
}

func (l *Lock) writeReceipt(r FreshReceipt) error {
	if err := l.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err = l.file.WriteAt(append(data, '\n'), 0); err != nil {
		return err
	}
	if err = l.file.Truncate(int64(len(data) + 1)); err != nil {
		return err
	}
	return l.file.Sync()
}
