package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type failingOrPanickingConnector struct {
	base      driver.Connector
	err       error
	shouldPan bool
}

func (c *failingOrPanickingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.shouldPan {
		panic("simulated base.Connect panic")
	}
	if c.err != nil {
		return nil, c.err
	}
	return c.base.Connect(ctx)
}

func (c *failingOrPanickingConnector) Driver() driver.Driver {
	return c.base.Driver()
}

func TestWrapConnectorWithCloseWaiterReleasesReservationOnConnectErrorAndPanic(t *testing.T) {
	base, err := NewConnector(filepath.Join(t.TempDir(), "tracked.sqlite"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("connect_error_decrements_reservation", func(t *testing.T) {
		wantErr := errors.New("connect refused")
		wrapped, waitClosed := WrapConnectorWithCloseWaiter(&failingOrPanickingConnector{
			base: base,
			err:  wantErr,
		})
		if _, cErr := wrapped.Connect(context.Background()); !errors.Is(cErr, wantErr) {
			t.Fatalf("Connect error = %v, want %v", cErr, wantErr)
		}
		done := make(chan struct{})
		go func() {
			waitClosed()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("waitClosed hung after base.Connect error")
		}
		// Post-seal Connect fails closed without touching base.
		if _, cErr := wrapped.Connect(context.Background()); !errors.Is(cErr, driver.ErrBadConn) {
			t.Fatalf("post-seal Connect = %v, want driver.ErrBadConn", cErr)
		}
	})

	t.Run("connect_panic_decrements_reservation", func(t *testing.T) {
		wrapped, waitClosed := WrapConnectorWithCloseWaiter(&failingOrPanickingConnector{
			base:      base,
			shouldPan: true,
		})
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic from base.Connect")
				}
			}()
			_, _ = wrapped.Connect(context.Background())
		}()
		done := make(chan struct{})
		go func() {
			waitClosed()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("waitClosed hung after base.Connect panic")
		}
	})
}
