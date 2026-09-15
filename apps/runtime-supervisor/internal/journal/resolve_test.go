package journal

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
)

func TestResolveSubmissionLeavesUnseenIntentUnwritten(t *testing.T) {
	store, _ := openTestStore(t, Options{})
	authority := testAuthority(101)
	intent := testIntent("op-unseen", 101, "start-v1")

	operation, found, err := store.ResolveSubmission(context.Background(), authority, intent)
	if err != nil || found || operation != (Operation{}) {
		t.Fatalf("ResolveSubmission() = %+v, %v, %v", operation, found, err)
	}
	if _, err := store.Get(context.Background(), authority.RuntimeIdentity, intent.OperationID); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("unseen resolve wrote durable state: %v", err)
	}
}

func TestResolveSubmissionReturnsEquivalentExistingOperation(t *testing.T) {
	store, _ := openTestStore(t, Options{})
	authority := testAuthority(103)
	intent := testIntent("op-existing", 103, "start-v1")
	created, _, err := store.Begin(context.Background(), authority, intent)
	if err != nil {
		t.Fatal(err)
	}

	resolved, found, err := store.ResolveSubmission(context.Background(), authority, intent)
	if err != nil || !found {
		t.Fatalf("ResolveSubmission() = %+v, %v, %v", resolved, found, err)
	}
	if resolved.OperationID != created.OperationID || resolved.State != created.State || resolved.RequestFingerprint != created.RequestFingerprint {
		t.Fatalf("resolved = %+v, want %+v", resolved, created)
	}
}

func TestResolveSubmissionFencesBeforeLookupAndDetectsConflict(t *testing.T) {
	store, _ := openTestStore(t, Options{})
	authority := testAuthority(107)
	intent := testIntent("op-fenced", 107, "start-v1")
	if _, _, err := store.Begin(context.Background(), authority, intent); err != nil {
		t.Fatal(err)
	}

	wrongIdentity := intent
	wrongIdentity.ExpectedRuntimeIdentity = "other-runtime"
	if _, _, err := store.ResolveSubmission(context.Background(), authority, wrongIdentity); !errors.Is(err, ErrRuntimeIdentityMismatch) {
		t.Fatalf("identity mismatch = %v", err)
	}

	stale := intent
	stale.ExpectedRuntimeGeneration = 106
	if _, _, err := store.ResolveSubmission(context.Background(), authority, stale); !errors.Is(err, ErrStaleRuntimeGeneration) {
		t.Fatalf("generation mismatch = %v", err)
	}

	conflict := intent
	conflict.RequestFingerprint = RequestFingerprint(sha256.Sum256([]byte("different-start-request")))
	if _, _, err := store.ResolveSubmission(context.Background(), authority, conflict); !errors.Is(err, ErrOperationIDConflict) {
		t.Fatalf("request conflict = %v", err)
	}
}
