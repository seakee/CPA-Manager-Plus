package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"sync"
)

var (
	sqliteDriverOnce sync.Once
	sqliteDriverInst driver.Driver
	sqliteDriverErr  error
)

func registeredSQLiteDriver() (driver.Driver, error) {
	sqliteDriverOnce.Do(func() {
		db, err := sql.Open("sqlite", "")
		if err != nil {
			sqliteDriverErr = err
			return
		}
		sqliteDriverInst = db.Driver()
		_ = db.Close()
	})
	return sqliteDriverInst, sqliteDriverErr
}

type dsnConnector struct {
	drv driver.Driver
	dsn string
}

func (c *dsnConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.drv.Open(c.dsn)
}

func (c *dsnConnector) Driver() driver.Driver {
	return c.drv
}

// NewConnector returns a driver.Connector configured for path with the standard
// Manager SQLite PRAGMAs and transaction lock mode.
func NewConnector(path string) (driver.Connector, error) {
	dbPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	drv, err := registeredSQLiteDriver()
	if err != nil {
		return nil, err
	}
	return &dsnConnector{drv: drv, dsn: dataSourceName(dbPath)}, nil
}

type connCloseTracker struct {
	mu   sync.Mutex
	cond *sync.Cond
	open int
}

func newConnCloseTracker() *connCloseTracker {
	t := &connCloseTracker{}
	t.cond = sync.NewCond(&t.mu)
	return t
}

func (t *connCloseTracker) onOpen() {
	t.mu.Lock()
	t.open++
	t.mu.Unlock()
}

func (t *connCloseTracker) onClose() {
	t.mu.Lock()
	t.open--
	if t.open <= 0 {
		t.open = 0
		t.cond.Broadcast()
	}
	t.mu.Unlock()
}

func (t *connCloseTracker) waitZero() {
	t.mu.Lock()
	for t.open > 0 {
		t.cond.Wait()
	}
	t.mu.Unlock()
}

type trackedConnector struct {
	base    driver.Connector
	tracker *connCloseTracker
}

func (c *trackedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	c.tracker.onOpen()
	return &trackedDriverConn{
		inner:   conn,
		onClose: c.tracker.onClose,
	}, nil
}

func (c *trackedConnector) Driver() driver.Driver {
	return c.base.Driver()
}

// WrapConnectorWithCloseWaiter wraps base so waitClosed blocks (via sync.Cond,
// without polling or timeouts) until every opened driver.Conn has completed
// Close().
func WrapConnectorWithCloseWaiter(base driver.Connector) (driver.Connector, func()) {
	tracker := newConnCloseTracker()
	return &trackedConnector{
		base:    base,
		tracker: tracker,
	}, tracker.waitZero
}

type trackedDriverConn struct {
	inner     driver.Conn
	onClose   func()
	closeOnce sync.Once
}

func (c *trackedDriverConn) Prepare(query string) (driver.Stmt, error) {
	return c.inner.Prepare(query)
}

func (c *trackedDriverConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if p, ok := c.inner.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, query)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.inner.Prepare(query)
}

func (c *trackedDriverConn) Close() error {
	err := c.inner.Close()
	c.closeOnce.Do(c.onClose)
	return err
}

func (c *trackedDriverConn) Begin() (driver.Tx, error) {
	//nolint:staticcheck // required by driver.Conn interface
	return c.inner.Begin()
}

func (c *trackedDriverConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.inner.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	//nolint:staticcheck // fallback when inner only implements driver.Conn
	return c.inner.Begin()
}

func (c *trackedDriverConn) Ping(ctx context.Context) error {
	if p, ok := c.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *trackedDriverConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := c.inner.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *trackedDriverConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if q, ok := c.inner.(driver.QueryerContext); ok {
		return q.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *trackedDriverConn) ResetSession(ctx context.Context) error {
	if r, ok := c.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *trackedDriverConn) IsValid() bool {
	if v, ok := c.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *trackedDriverConn) CheckNamedValue(nv *driver.NamedValue) error {
	if chk, ok := c.inner.(driver.NamedValueChecker); ok {
		return chk.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}
