package processlock

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPendingFreshReceiptSameInodeRetainedFence(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manager.sqlite")
	l, err := AcquireNew(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	before, err := os.Stat(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.BeginFreshAdmission()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.BeginFreshAdmission(); err == nil {
		t.Fatal("second admission minted identity")
	}
	if _, err = os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("receipt created database")
	}
	got, err := l.PendingFreshAdmission()
	if err != nil || got == nil || got.Identity != r.Identity {
		t.Fatal("pending", err)
	}
	wrong := r.Identity
	wrong.Generation = strings.Repeat("f", 32)
	if err = l.FinalizeFreshAdmission(wrong); err == nil {
		t.Fatal("foreign finalize")
	}
	if err = l.FinalizeFreshAdmission(r.Identity); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(l.Path())
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("replaced marker inode")
	}
	if _, err = Acquire(p); err == nil {
		t.Fatal("finalization released fence")
	}
	if p, err := l.PendingFreshAdmission(); err != nil || p != nil {
		t.Fatal("pending survived finalize", err)
	}
	if id, err := l.FreshIdentity(); err != nil || id == nil || *id != r.Identity {
		t.Fatal("retained identity", err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := AcquireExisting(p)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if id, err := reopened.FreshIdentity(); err != nil || id == nil || *id != r.Identity {
		t.Fatal("restart identity", err)
	}
}

func TestObserveFreshReceiptUnderHeldAndReleasedLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manager.sqlite")
	l, err := AcquireNew(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r, err := l.BeginFreshAdmission()
	if err != nil {
		t.Fatal(err)
	}

	gotHeld, errHeld := ObserveFreshReceipt(p)
	if runtime.GOOS == "windows" {
		if errHeld == nil || gotHeld != nil {
			t.Fatalf("expected Windows mandatory LockFileEx to refuse cross-handle receipt read while held, got receipt=%+v err=%v", gotHeld, errHeld)
		}
	} else {
		if errHeld != nil || gotHeld == nil || !gotHeld.IsPending() || gotHeld.Identity != r.Identity {
			t.Fatalf("expected held Unix advisory lock receipt observation, got receipt=%+v err=%v", gotHeld, errHeld)
		}
	}

	if err := l.FinalizeFreshAdmission(r.Identity); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	gotReleased, errReleased := ObserveFreshReceipt(p)
	if errReleased != nil || gotReleased == nil || gotReleased.IsPending() || gotReleased.Identity != r.Identity {
		t.Fatalf("expected released marker receipt to be readable on %s, got receipt=%+v err=%v", runtime.GOOS, gotReleased, errReleased)
	}
}
