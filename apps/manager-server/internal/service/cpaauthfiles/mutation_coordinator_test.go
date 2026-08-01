package cpaauthfiles

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMutationCoordinatorSerializesSamePhysicalFileAndAllowsDifferentFiles(t *testing.T) {
	coordinator := NewMutationCoordinator()
	releaseFirst, err := coordinator.Acquire(context.Background(), " Shared.JSON ")
	if err != nil {
		t.Fatalf("acquire first file mutation: %v", err)
	}

	sameAcquired := make(chan func(), 1)
	sameErr := make(chan error, 1)
	go func() {
		release, acquireErr := coordinator.Acquire(context.Background(), "shared.json")
		if acquireErr != nil {
			sameErr <- acquireErr
			return
		}
		sameAcquired <- release
	}()

	select {
	case release := <-sameAcquired:
		release()
		t.Fatal("same physical file mutation acquired before the first mutation released")
	case err := <-sameErr:
		t.Fatalf("same physical file mutation failed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	differentRelease, err := coordinator.Acquire(context.Background(), "other.json")
	if err != nil {
		t.Fatalf("acquire different file mutation: %v", err)
	}
	differentRelease()
	differentRelease()

	releaseFirst()
	select {
	case release := <-sameAcquired:
		release()
	case err := <-sameErr:
		t.Fatalf("same physical file mutation failed after release: %v", err)
	case <-time.After(time.Second):
		t.Fatal("same physical file mutation did not acquire after release")
	}
}

func TestMutationCoordinatorAcquireAllBlocksExistingAndNewFileMutations(t *testing.T) {
	coordinator := NewMutationCoordinator()
	releaseExisting, err := coordinator.Acquire(context.Background(), "existing.json")
	if err != nil {
		t.Fatalf("acquire existing file mutation: %v", err)
	}

	allAcquired := make(chan func(), 1)
	allErr := make(chan error, 1)
	go func() {
		release, acquireErr := coordinator.AcquireAll(context.Background())
		if acquireErr != nil {
			allErr <- acquireErr
			return
		}
		allAcquired <- release
	}()
	waitForMutationCoordinatorAllWaiter(t, coordinator)

	newFileAcquired := make(chan func(), 1)
	newFileErr := make(chan error, 1)
	go func() {
		release, acquireErr := coordinator.Acquire(context.Background(), "new.json")
		if acquireErr != nil {
			newFileErr <- acquireErr
			return
		}
		newFileAcquired <- release
	}()

	select {
	case release := <-newFileAcquired:
		release()
		t.Fatal("new file mutation bypassed a waiting AcquireAll")
	case err := <-newFileErr:
		t.Fatalf("new file mutation failed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	releaseExisting()
	var releaseAll func()
	select {
	case releaseAll = <-allAcquired:
	case err := <-allErr:
		t.Fatalf("AcquireAll failed after existing mutation released: %v", err)
	case <-time.After(time.Second):
		t.Fatal("AcquireAll did not acquire after existing mutation released")
	}

	select {
	case release := <-newFileAcquired:
		release()
		t.Fatal("new file mutation acquired while AcquireAll was active")
	case err := <-newFileErr:
		t.Fatalf("new file mutation failed while waiting: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	releaseAll()
	select {
	case release := <-newFileAcquired:
		release()
	case err := <-newFileErr:
		t.Fatalf("new file mutation failed after AcquireAll release: %v", err)
	case <-time.After(time.Second):
		t.Fatal("new file mutation did not acquire after AcquireAll release")
	}
}

func TestMutationCoordinatorCanceledAcquireAllUnblocksFileMutations(t *testing.T) {
	coordinator := NewMutationCoordinator()
	releaseExisting, err := coordinator.Acquire(context.Background(), "existing.json")
	if err != nil {
		t.Fatalf("acquire existing file mutation: %v", err)
	}
	defer releaseExisting()

	allCtx, cancelAll := context.WithCancel(context.Background())
	allErr := make(chan error, 1)
	go func() {
		_, acquireErr := coordinator.AcquireAll(allCtx)
		allErr <- acquireErr
	}()
	waitForMutationCoordinatorAllWaiter(t, coordinator)
	cancelAll()

	select {
	case err := <-allErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("AcquireAll cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled AcquireAll did not return")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	releaseOther, err := coordinator.Acquire(ctx, "other.json")
	if err != nil {
		t.Fatalf("different file remained blocked after canceled AcquireAll: %v", err)
	}
	releaseOther()
}

func TestMutationCoordinatorZeroValueIsReadyForUse(t *testing.T) {
	var coordinator MutationCoordinator
	releaseFile, err := coordinator.Acquire(context.Background(), "zero-value.json")
	if err != nil {
		t.Fatalf("acquire file mutation from zero value: %v", err)
	}

	allCtx, cancelAll := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancelAll()
	if _, err := coordinator.AcquireAll(allCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireAll while zero-value file mutation is active = %v, want deadline exceeded", err)
	}

	releaseFile()
	releaseAll, err := coordinator.AcquireAll(context.Background())
	if err != nil {
		t.Fatalf("AcquireAll from initialized zero value: %v", err)
	}
	releaseAll()
}

func waitForMutationCoordinatorAllWaiter(t *testing.T, coordinator *MutationCoordinator) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		coordinator.mu.Lock()
		waiting := coordinator.waitingAll
		coordinator.mu.Unlock()
		if waiting > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("AcquireAll did not register as waiting")
}
