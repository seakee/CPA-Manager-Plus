package response

import (
	"errors"
	"strings"
	"testing"
)

func TestPublicErrorMessageRedactsManagedUpdatePaths(t *testing.T) {
	err := errors.New(`restrict private directory C:\Users\alice\CPA\backups: access denied`)
	code := UsageServiceErrorCode(err)
	if code != "update_private_storage_unavailable" {
		t.Fatalf("code = %q", code)
	}
	message := PublicErrorMessage(code, err)
	if strings.Contains(message, `C:\Users\alice`) {
		t.Fatalf("public message leaked a local path: %q", message)
	}
}

func TestPublicErrorMessageKeepsOrdinaryValidationErrors(t *testing.T) {
	err := errors.New("prices are required")
	if got := PublicErrorMessage(UsageServiceErrorCode(err), err); got != err.Error() {
		t.Fatalf("message = %q", got)
	}
}
