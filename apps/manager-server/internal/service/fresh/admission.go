package fresh

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

type OpenOptions struct {
	Initialize bool
	AdminKey   string
	DataKey    string
	// Hook is an isolated acceptance-test seam, never a configurable runtime state.
	Hook func(string) error
}
type Opened struct {
	Admission *legacypreflight.Admission
	DB        *sql.DB
	Protector *security.Protector
	Record    freshstore.Record
}

func (o *Opened) Close() error {
	if o == nil {
		return nil
	}
	var err error
	if o.DB != nil {
		err = o.DB.Close()
	}
	return errors.Join(err, o.Admission.Close())
}
func checkpoint(opts OpenOptions, name string) error {
	if opts.Hook != nil {
		return opts.Hook(name)
	}
	return nil
}

// Open enters the actual Manager store while retaining the original fence.
// No DB, data key or checkpoint can be created before the pending receipt sync.
func Open(ctx context.Context, paths sqlite.InspectionPaths, opts OpenOptions) (opened *Opened, err error) {
	fileBackedKey := strings.TrimSpace(opts.DataKey) == ""
	admission, err := legacypreflight.AdmitStartup(ctx, paths, legacypreflight.Options{Initialize: opts.Initialize, FreshAdmission: true})
	if err != nil {
		return nil, err
	}
	o := &Opened{Admission: admission}
	defer func() {
		if err != nil {
			_ = o.Close()
		}
	}()
	paths.Database = admission.DatabasePath()
	pending := admission.PendingFresh
	markerIdentity, err := admission.FreshIdentity()
	if err != nil {
		return nil, err
	}
	var prior freshstore.Record
	// Existing DB state is inspected before writable Open/Migrate. A retained
	// ordinary marker alone never admits an unknown native-v2 installation.
	if info, e := os.Stat(paths.Database); e == nil {
		if info.Size() == 0 {
			return nil, freshstore.ErrNotFresh
		}
		prior, err = inspectCheckpoint(ctx, paths.Database, pendingIdentity(pending))
		if err != nil {
			return nil, err
		}
		if prior.Operation.Identity.Valid() && (markerIdentity == nil || *markerIdentity != prior.Operation.Identity) {
			return nil, freshstore.ErrConflict
		}
		if prior.Operation.Identity.Valid() && opts.AdminKey != "" && (prior.Admin == nil || !security.VerifyAdminKey(*prior.Admin, opts.AdminKey)) {
			return nil, freshstore.ErrConflict
		}
		if fileBackedKey {
			if _, e = os.Stat(paths.DataKey); e != nil {
				return nil, errors.New("Fresh data key unavailable")
			}
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	} else if pending == nil {
		return nil, freshstore.ErrNotFresh
	}
	if pending != nil {
		if err = checkpoint(opts, "pending-receipt"); err != nil {
			return nil, err
		}
	}
	if pending == nil && !prior.Operation.Identity.Valid() {
		return nil, freshstore.ErrNotFresh
	}
	key, _, err := security.LoadOrCreateDataKey(opts.DataKey, paths.DataKey)
	if err != nil {
		return nil, err
	}
	if err = checkpoint(opts, "data-key-loaded"); err != nil {
		return nil, err
	}
	if fileBackedKey {
		f, e := os.OpenFile(paths.DataKey, os.O_RDWR, 0)
		if e != nil {
			return nil, e
		}
		err = f.Sync()
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		// Windows does not support fsync on an opened directory. The key
		// file itself is already synced above; retain directory-entry fsync
		// on platforms that support it.
		if runtime.GOOS != "windows" {
			dir, e := os.Open(filepath.Dir(paths.DataKey))
			if e != nil {
				return nil, e
			}
			e = dir.Sync()
			_ = dir.Close()
			if e != nil {
				return nil, e
			}
		}
	}
	if err = checkpoint(opts, "data-key"); err != nil {
		return nil, err
	}
	o.Protector, err = security.NewProtector(key)
	if err != nil {
		return nil, err
	}
	o.DB, err = sqlite.Open(paths.Database)
	if err != nil {
		return nil, err
	}
	admission.RetainMarker()
	if err = checkpoint(opts, "schema"); err != nil {
		return nil, err
	}
	st := freshstore.New(o.DB)
	if prior.Operation.Identity.Valid() {
		o.Record, err = st.Load(ctx)
		if err != nil {
			return nil, err
		}
		if opts.AdminKey != "" && (o.Record.Admin == nil || !security.VerifyAdminKey(*o.Record.Admin, opts.AdminKey)) {
			return nil, freshstore.ErrConflict
		}
	} else {
		if pending == nil {
			return nil, freshstore.ErrNotFresh
		}
		var admin *model.AdminCredential
		if opts.AdminKey != "" {
			if err = security.ValidateFreshAdminKey(opts.AdminKey); err != nil {
				return nil, err
			}
			value, e := security.NewAdminCredential(opts.AdminKey, "host")
			if e != nil {
				return nil, e
			}
			admin = &value
		}
		o.Record, err = st.Create(ctx, pending.Identity, admin)
		if err != nil {
			return nil, err
		}
	}
	if o.Record.Connection != nil {
		if _, err = o.Protector.UnprotectString(o.Record.Connection.ManagementKey); err != nil {
			return nil, errors.New("Fresh connection secret unavailable")
		}
	}
	if o.Record.AdoptionCiphertext != "" {
		if _, err = o.Protector.UnprotectString(o.Record.AdoptionCiphertext); err != nil {
			return nil, errors.New("Fresh adoption snapshot unavailable")
		}
	}
	if err = checkpoint(opts, "checkpoint"); err != nil {
		return nil, err
	}
	if pending != nil {
		if o.Record.Operation.Identity != pending.Identity {
			return nil, freshstore.ErrConflict
		}
		if err = admission.FinalizeFreshAdmission(o.Record.Operation.Identity); err != nil {
			return nil, err
		}
	}
	return o, nil
}

func pendingIdentity(r *processlock.FreshReceipt) *bootstrap.FreshIdentity {
	if r != nil {
		return &r.Identity
	}
	return nil
}

func inspectCheckpoint(ctx context.Context, path string, pending *bootstrap.FreshIdentity) (freshstore.Record, error) {
	db, err := sql.Open("sqlite", checkpointDataSourceName(path))
	if err != nil {
		return freshstore.Record{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var exists int
	if err = db.QueryRowContext(ctx, "select count(*) from sqlite_master where type='table' and name='settings'").Scan(&exists); err != nil {
		return freshstore.Record{}, err
	}
	if exists == 0 {
		return freshstore.Record{}, freshstore.ErrNotFresh
	}
	v, err := freshstore.New(db).Load(ctx)
	if err == nil {
		if v.Operation.Identity.DatabasePath != path || pending != nil && v.Operation.Identity != *pending {
			return freshstore.Record{}, freshstore.ErrConflict
		}
		return v, nil
	}
	if pending == nil {
		return freshstore.Record{}, freshstore.ErrNotFresh
	}
	// A checkpoint-less schema is resumable only with the original pending
	// receipt and no existing product/admin/runtime/history state whatsoever.
	if err = freshstore.ValidatePristine(ctx, db); err != nil {
		return freshstore.Record{}, err
	}
	return freshstore.Record{}, nil
}

func checkpointDataSourceName(path string) string {
	// Keep a Windows drive in the local file path, matching sqlite's URI handling.
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(2000)")
	u.RawQuery = q.Encode()
	return u.String()
}
