package gocommerce

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/stdlib"
)

// The store a statement runs for is decided by its context, at the moment it
// runs, on whichever pooled connection it lands. Binding a connection to a
// request instead would pin it for the request's life, and any handler that
// queries while iterating rows would then fail with "conn busy"; binding per
// statement costs one extra round trip only when the store changes.
//
// What is bound is the session variable gocommerce.store_id, which every
// store table's row-security policy compares against (scope.go).

// unknownStore marks a connection whose setting is not known, so the next
// statement sets it whatever it wants. No real store id can equal it.
const unknownStore = "\x00unknown"

type storeConnector struct {
	inner    driver.Connector
	fallback atomic.Pointer[string]
}

// SetFallback names the store an unbound context is bound to. Empty means
// none: unbound work sees no store rows. Only embedders and tests set it.
func (c *storeConnector) SetFallback(storeID string) { c.fallback.Store(&storeID) }

func (c *storeConnector) want(ctx context.Context) string {
	if s, ok := StoreFrom(ctx); ok {
		return s
	}
	if f := c.fallback.Load(); f != nil {
		return *f
	}
	return ""
}

func (c *storeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &storeConn{inner: conn, c: c, current: unknownStore}, nil
}

func (c *storeConnector) Driver() driver.Driver { return c.inner.Driver() }

// connectors remembers which connector opened each pool, so the engine can
// set a pool's fallback after migrations have created the first store.
var connectors sync.Map // *sql.DB → *storeConnector

func storeConnectorOf(db *sql.DB) *storeConnector {
	v, _ := connectors.Load(db)
	c, _ := v.(*storeConnector)
	return c
}

func newStoreConnector(dsn string) (*storeConnector, error) {
	inner, err := stdlib.GetDefaultDriver().(driver.DriverContext).OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return &storeConnector{inner: inner}, nil
}

type storeConn struct {
	inner   driver.Conn
	c       *storeConnector
	current string // what gocommerce.store_id holds on the server, or unknownStore
}

func (s *storeConn) bind(ctx context.Context) error {
	want := s.c.want(ctx)
	if want == s.current {
		return nil
	}
	ex := s.inner.(driver.ExecerContext)
	if _, err := ex.ExecContext(ctx, `SELECT set_config('gocommerce.store_id', $1, false)`,
		[]driver.NamedValue{{Ordinal: 1, Value: want}}); err != nil {
		s.current = unknownStore
		return err
	}
	s.current = want
	return nil
}

func (s *storeConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := s.bind(ctx); err != nil {
		return nil, err
	}
	return s.inner.(driver.ExecerContext).ExecContext(ctx, q, args)
}

func (s *storeConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := s.bind(ctx); err != nil {
		return nil, err
	}
	return s.inner.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func (s *storeConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	st, err := s.inner.(driver.ConnPrepareContext).PrepareContext(ctx, q)
	if err != nil {
		return nil, err
	}
	return &storeStmt{inner: st, conn: s}, nil
}

func (s *storeConn) Prepare(q string) (driver.Stmt, error) {
	return s.PrepareContext(context.Background(), q)
}

func (s *storeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := s.bind(ctx); err != nil {
		return nil, err
	}
	tx, err := s.inner.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &storeTx{inner: tx, conn: s}, nil
}

func (s *storeConn) Begin() (driver.Tx, error) {
	return s.BeginTx(context.Background(), driver.TxOptions{})
}

func (s *storeConn) Close() error { return s.inner.Close() }

func (s *storeConn) Ping(ctx context.Context) error { return s.inner.(driver.Pinger).Ping(ctx) }

func (s *storeConn) ResetSession(ctx context.Context) error {
	if r, ok := s.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (s *storeConn) IsValid() bool {
	if v, ok := s.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// CheckNamedValue forwards pgx's own argument conversion; without it
// database/sql would apply the default converter and reject pgx types.
func (s *storeConn) CheckNamedValue(nv *driver.NamedValue) error {
	if c, ok := s.inner.(driver.NamedValueChecker); ok {
		return c.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

type storeTx struct {
	inner driver.Tx
	conn  *storeConn
}

func (t *storeTx) Commit() error { return t.inner.Commit() }

// Rollback undoes any set_config the transaction ran, so what the server
// holds is no longer known; the next statement sets it again.
func (t *storeTx) Rollback() error {
	t.conn.current = unknownStore
	return t.inner.Rollback()
}

type storeStmt struct {
	inner driver.Stmt
	conn  *storeConn
}

func (s *storeStmt) Close() error  { return s.inner.Close() }
func (s *storeStmt) NumInput() int { return s.inner.NumInput() }

var errNoContext = errors.New("gocommerce: statements must run with a context")

func (s *storeStmt) Exec(args []driver.Value) (driver.Result, error) { return nil, errNoContext }
func (s *storeStmt) Query(args []driver.Value) (driver.Rows, error)  { return nil, errNoContext }

func (s *storeStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.conn.bind(ctx); err != nil {
		return nil, err
	}
	return s.inner.(driver.StmtExecContext).ExecContext(ctx, args)
}

func (s *storeStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := s.conn.bind(ctx); err != nil {
		return nil, err
	}
	return s.inner.(driver.StmtQueryContext).QueryContext(ctx, args)
}

func (s *storeStmt) CheckNamedValue(nv *driver.NamedValue) error {
	if c, ok := s.inner.(driver.NamedValueChecker); ok {
		return c.CheckNamedValue(nv)
	}
	return s.conn.CheckNamedValue(nv)
}
