package managerconfig

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/requestbody"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	managerconfigservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

type failIfReadBody struct {
	reads atomic.Int32
}

func (b *failIfReadBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	return 0, errors.New("unexpected body read before authentication")
}

func (b *failIfReadBody) Close() error { return nil }

func newControllerTestHandler(t *testing.T) http.Handler {
	t.Helper()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	colSvc := collectorservice.New(collectorpkg.NewManager(cfg, st))
	h := &Handler{
		App: &app.Context{
			Config:               cfg,
			Store:                st,
			AdminAuthService:     adminauth.New(cfg, st),
			ManagerConfigService: managerconfigservice.New(cfg, st, colSvc),
		},
	}
	return requestbody.Middleware(0, http.HandlerFunc(h.Handle))
}

func TestHandlerPutAuthenticatesBeforeBodyDecodeAndPatchClosesOversizedHTTP11(t *testing.T) {
	h := newControllerTestHandler(t)

	spy := &failIfReadBody{}
	req := httptest.NewRequest(http.MethodPut, "/usage-service/config", spy)
	req.ContentLength = 2 << 20
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated oversized PUT status = %d, want 401", rec.Code)
	}
	if got := spy.reads.Load(); got != 0 {
		t.Fatalf("unauthenticated oversized PUT read body %d times, want 0", got)
	}
	if got := rec.Header().Get("Connection"); got != "close" {
		t.Fatalf("Connection = %q, want close", got)
	}

	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	addr := ts.Listener.Addr().String()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial TCP: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	oversizedPatchPrefix := `{"collector":{"queue":"` + strings.Repeat("p", 5000)
	rawPatch := fmt.Sprintf(
		"PATCH /usage-service/config HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: 32768\r\n\r\n%s",
		addr,
		testutil.AdminKey,
		oversizedPatchPrefix,
	)
	started := time.Now()
	if _, err := conn.Write([]byte(rawPatch)); err != nil {
		t.Fatalf("write raw PATCH: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(conn)
	parseReq, _ := http.NewRequest(http.MethodPatch, "http://127.0.0.1/usage-service/config", nil)
	resp, err := http.ReadResponse(br, parseReq)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var extra [1]byte
	n, closeErr := br.Read(extra[:])
	if elapsed := time.Since(started); elapsed >= 200*time.Millisecond {
		t.Fatalf("oversized PATCH close took %v, want prompt close under 200ms", elapsed)
	}
	if resp.StatusCode != http.StatusBadRequest || !resp.Close {
		t.Fatalf("oversized PATCH = (%d, close=%v), want (400, true)", resp.StatusCode, resp.Close)
	}
	if n != 0 || closeErr == nil || errors.Is(closeErr, os.ErrDeadlineExceeded) {
		t.Fatalf("post-response TCP read = (%d, %v), want immediate socket close", n, closeErr)
	}
}
