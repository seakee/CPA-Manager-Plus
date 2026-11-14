package lifecycle

import (
	"errors"
	"os"
	"testing"
)

func TestAdoptDestinationRefusesEmptyAuthenticationBeforeStaging(t *testing.T) {
	for _, keys := range [][]string{nil, {}} {
		f, request, root := destinationFixture(t)
		request.APIKeys = keys
		if _, err := f.starter.StageDestination(t.Context(), request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("empty adopted key set reached destination staging: %v", err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) || f.child.starts != 0 {
			t.Fatal("empty key refusal changed the destination or started an unauthenticated CPA")
		}
	}
}
