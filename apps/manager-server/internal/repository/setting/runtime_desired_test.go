package setting

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestEmbeddedRuntimeDesiredStatePersistsAndRevisesOnlyOnChange(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "manager.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)

	if state, found, err := repository.LoadEmbeddedRuntimeDesiredState(t.Context()); err != nil || found {
		t.Fatalf("initial desired state = %#v found=%v err=%v", state, found, err)
	}
	first, err := repository.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredRunning)
	if err != nil {
		t.Fatalf("initialize desired state: %v", err)
	}
	if first.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning || first.Revision != 1 || first.UpdatedAtMS <= 0 {
		t.Fatalf("initial desired state = %#v", first)
	}
	same, err := repository.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredRunning)
	if err != nil {
		t.Fatalf("set same desired state: %v", err)
	}
	if same != first {
		t.Fatalf("same desired state changed metadata: got %#v want %#v", same, first)
	}
	changed, err := repository.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredStopped)
	if err != nil {
		t.Fatalf("change desired state: %v", err)
	}
	if changed.DesiredLifecycle != model.EmbeddedRuntimeDesiredStopped || changed.Revision != 2 ||
		changed.UpdatedAtMS <= first.UpdatedAtMS {
		t.Fatalf("changed desired state = %#v after %#v", changed, first)
	}
	loaded, found, err := repository.LoadEmbeddedRuntimeDesiredState(t.Context())
	if err != nil || !found || loaded != changed {
		t.Fatalf("loaded desired state = %#v found=%v err=%v", loaded, found, err)
	}
}

func TestEmbeddedRuntimeDesiredConcurrentSameChangeIncrementsOnce(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "manager.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	if _, err := repository.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredRunning); err != nil {
		t.Fatalf("initialize desired state: %v", err)
	}

	const writers = 12
	start := make(chan struct{})
	errorsByWriter := make(chan error, writers)
	var wait sync.WaitGroup
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			state, err := repository.SetEmbeddedRuntimeDesiredLifecycle(context.Background(), model.EmbeddedRuntimeDesiredStopped)
			if err == nil && state.Revision != 2 {
				err = &unexpectedRevisionError{revision: state.Revision}
			}
			errorsByWriter <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatalf("concurrent desired update: %v", err)
		}
	}
	loaded, found, err := repository.LoadEmbeddedRuntimeDesiredState(t.Context())
	if err != nil || !found || loaded.Revision != 2 || loaded.DesiredLifecycle != model.EmbeddedRuntimeDesiredStopped {
		t.Fatalf("final desired state = %#v found=%v err=%v", loaded, found, err)
	}
}

type unexpectedRevisionError struct {
	revision uint64
}

func (e *unexpectedRevisionError) Error() string {
	return "unexpected desired revision"
}
