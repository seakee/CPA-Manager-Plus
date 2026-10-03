package reconnect

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	reconnectsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/reconnect"
)

func TestLimiterWindow(t *testing.T) {
	l := newLimiter(2, time.Minute)
	now := time.Now()
	if !l.allow("ip", now) || !l.allow("ip", now) {
		t.Fatal("first two requests must pass")
	}
	if l.allow("ip", now) {
		t.Fatal("third request in the window must be refused")
	}
	if !l.allow("other", now) {
		t.Fatal("limits are per client")
	}
	if !l.allow("ip", now.Add(time.Minute)) {
		t.Fatal("a new window resets the count")
	}
}

func TestWritePublicHidesInternalErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	writePublic(rec, nil, errors.New("dial tcp 10.0.0.5:8317: connection refused"))
	if rec.Code != http.StatusBadGateway || contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("internal error leaked: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	writePublic(rec, nil, reconnectsvc.ErrNotFound)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
