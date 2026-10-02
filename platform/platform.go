// Package platform runs many stores from one process and one PostgreSQL
// database: multi-tenant mode (D70).
//
//	p, err := platform.New(platform.Config{
//		DBURL:      "postgres://…/commerce",
//		BaseDomain: "shops.example.com",
//		Tokens:     []string{os.Getenv("GOCOMMERCE_PLATFORM_TOKEN")},
//		Store:      gocommerce.Config{Currency: "USD"},
//		Modules:    func(t platform.Tenant) []gocommerce.Module { return []gocommerce.Module{…} },
//	})
//	err = p.ListenAndServe()
//
// Every store is an ordinary engine — a *gocommerce.App with its own
// operators, admin panel, API, background work and modules — living in its
// own PostgreSQL schema. A request is routed to its store by the Host it was
// sent to: <slug>.<BaseDomain>, or any custom domain the platform has
// attached. Nothing in the engine knows it is one of many.
//
// Schema per store rather than a store_id on every table is the whole design,
// and the reason is what a forgotten filter costs. With a column, isolation is
// a WHERE clause in every one of thousands of statements, now and in every
// module anybody writes later, and the one that forgets it shows one shop's
// customers to another. With a schema, a connection can only see its own
// store's tables — the engine's test suite has always run every test in a
// schema of its own, through the same search_path this uses, which is the
// evidence that every statement in core and every module already works this
// way.
//
// Two kinds of administrator, never mixed. The platform's operators hold a
// platform token and manage stores — create, suspend, attach a domain — at
// /api/platform/ on the platform's own hosts. A store's operators are that
// store's superusers and its own admin token, and reach nothing outside it. A
// platform token is not a store token: it opens no store's admin API, and a
// store's token opens no other store's.
package platform

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The platform's own tables live in this schema, beside the stores' and
// never inside one; Config.Namespace prefixes it.
const platformSchemaName = "gocommerce_platform"

var namespaceRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,20}$`)

// migrationLockKey serialises two platform processes migrating at once.
// Distinct from core's key, which every store's migrations take.
const migrationLockKey = 7_302_515_221

// Tenant states.
const (
	StatusActive = "active"
	// StatusSuspended keeps the store's data and stops serving it: requests
	// answer 503, and its background work is stopped.
	StatusSuspended = "suspended"
)

var slugRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Config configures a platform.
type Config struct {
	// DBURL is the database every store and the platform share. Each store
	// gets a schema in it.
	DBURL string
	// Addr is the listen address. Defaults to ":8080".
	Addr string
	// BaseDomain gives every store a host of its own, <slug>.<BaseDomain>,
	// before any custom domain is attached. Empty means stores are reached
	// only by the domains attached to them.
	BaseDomain string
	// PlatformHosts are the hosts that serve the platform's own API. Defaults
	// to platform.<BaseDomain>. The platform API is never served on a store's
	// host, so a store's customers never see it.
	PlatformHosts []string
	// APIHosts are shared hosts — api.<BaseDomain>, say — where the store is
	// named by the X-Store header (its slug or one of its domains) instead of
	// by the host. A headless storefront talking to one API host for many
	// stores is what they are for.
	APIHosts []string
	// Tokens are the platform operators' bearer tokens. Required: a platform
	// whose API anybody can call creates stores for anybody.
	Tokens []string
	// Store is the template every store's engine is configured from. DBURL,
	// AdminTokens, MediaDir, Addr and MaxOpenConns are the platform's to set
	// and ignored here; the currency and languages are defaults a store may
	// override when it is created.
	Store gocommerce.Config
	// MediaRoot holds each store's uploads, in MediaRoot/<slug>. Empty
	// disables uploads for every store.
	MediaRoot string
	// Namespace prefixes every schema the platform creates — its own and
	// each store's — so two platforms can share one database: staging beside
	// production, or a test run beside a developer's store. Lower-case
	// letters, digits and underscores, starting with a letter. Empty is no
	// prefix.
	Namespace string
	// MaxOpenConnsPerStore caps each store's connection pool. Defaults to 4:
	// a hundred stores at the engine's single-store default of 25 would be
	// 2,500 connections, past any server's max_connections. Put PgBouncer in
	// front when the count of stores outgrows this.
	MaxOpenConnsPerStore int
	// Modules builds the modules one store runs. It is called once per store,
	// and must return new instances each time: a module holds the engine it
	// registered with, so one shared between stores would serve one store's
	// requests from another's tables.
	Modules func(t Tenant) []gocommerce.Module
	// Logger defaults to slog.Default(). Each store logs through it with its
	// slug attached.
	Logger *slog.Logger
}

// Tenant is one store on the platform.
type Tenant struct {
	ID        int64    `json:"id"`
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	Status    string   `json:"status"`
	Currency  string   `json:"currency"`
	Languages []string `json:"languages"`
	Domains   []string `json:"domains"`
	// Host is where the store is reached first: its first custom domain,
	// else <slug>.<BaseDomain>. A Modules function uses it to build the links
	// a store's emails carry.
	Host      string    `json:"host"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// The store's schema and its own admin token. The token is the static
	// credential the engine requires of a store outside dev mode; it is
	// returned once, when minted, and never listed.
	schema     string
	adminToken string
}

// Platform hosts the stores.
type Platform struct {
	cfg Config
	db  *sql.DB
	log *slog.Logger
	api http.Handler
	// schema is the platform's own schema, the namespace applied.
	schema string

	mu      sync.RWMutex
	tenants map[string]*Tenant         // by slug, every store
	apps    map[string]*gocommerce.App // by slug, the running ones
	// handlers holds each running store's middleware chain, built once at
	// boot rather than on every request.
	handlers map[string]http.Handler
	hosts    map[string]string // host -> slug

	// runCtx is what started stores run under; nil until Start.
	runCtx context.Context
	stop   context.CancelFunc
}

// New opens the platform's database, brings its own schema up to date and
// boots every active store. The stores' background work does not run until
// Start.
func New(cfg Config) (*Platform, error) {
	if cfg.DBURL == "" {
		return nil, errors.New("platform: Config.DBURL is required")
	}
	if len(cfg.Tokens) == 0 {
		return nil, errors.New("platform: at least one Config.Tokens entry is required")
	}
	for _, t := range cfg.Tokens {
		if len(t) < 16 {
			return nil, errors.New("platform: a platform token must be at least 16 characters")
		}
	}
	if cfg.Modules == nil {
		cfg.Modules = func(Tenant) []gocommerce.Module { return nil }
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.MaxOpenConnsPerStore <= 0 {
		cfg.MaxOpenConnsPerStore = 4
	}
	cfg.BaseDomain = normalizeHost(cfg.BaseDomain)
	if len(cfg.PlatformHosts) == 0 && cfg.BaseDomain != "" {
		cfg.PlatformHosts = []string{"platform." + cfg.BaseDomain}
	}
	for i, h := range cfg.PlatformHosts {
		cfg.PlatformHosts[i] = normalizeHost(h)
	}
	for i, h := range cfg.APIHosts {
		cfg.APIHosts[i] = normalizeHost(h)
	}
	if len(cfg.PlatformHosts) == 0 {
		return nil, errors.New("platform: set Config.BaseDomain or Config.PlatformHosts, or the platform API has nowhere to be served")
	}

	if cfg.Namespace != "" && !namespaceRE.MatchString(cfg.Namespace) {
		return nil, errors.New("platform: Config.Namespace is lower-case letters, digits and underscores, starting with a letter")
	}
	schema := platformSchemaName
	if cfg.Namespace != "" {
		schema = cfg.Namespace + "_" + platformSchemaName
	}
	db, err := gocommerce.OpenDB(context.Background(), withSearchPath(cfg.DBURL, schema))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(cfg.MaxOpenConnsPerStore)
	p := &Platform{
		cfg: cfg, db: db, log: cfg.Logger, schema: schema,
		tenants: map[string]*Tenant{}, apps: map[string]*gocommerce.App{}, hosts: map[string]string{},
		handlers: map[string]http.Handler{},
	}
	if err := p.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	p.api = p.routes()
	if err := p.load(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return p, nil
}

// load reads every store and boots the active ones.
func (p *Platform) load(ctx context.Context) error {
	tenants, err := p.readTenants(ctx, "")
	if err != nil {
		return err
	}
	for _, t := range tenants {
		p.remember(t)
		if t.Status != StatusActive {
			continue
		}
		if err := p.boot(ctx, t); err != nil {
			// One broken store must not keep the rest offline. It is logged
			// loudly and answers 503 until it is fixed and resumed.
			p.log.Error("platform: a store failed to boot", "store", t.Slug, "error", err)
		}
	}
	return nil
}

// boot builds a store's engine — which brings its schema up to date — and
// starts its background work if the platform is already running.
func (p *Platform) boot(ctx context.Context, t *Tenant) error {
	cfg := p.cfg.Store
	cfg.DBURL = withSearchPath(p.cfg.DBURL, t.schema)
	// A store's static token is its own and nobody else's: the template's
	// tokens are deliberately dropped, because one token opening every store
	// would be a platform credential wearing a store's name.
	cfg.AdminTokens = []string{t.adminToken}
	cfg.MaxOpenConns = p.cfg.MaxOpenConnsPerStore
	cfg.Logger = p.log.With("store", t.Slug)
	cfg.MediaDir = ""
	if p.cfg.MediaRoot != "" {
		dir := filepath.Join(p.cfg.MediaRoot, t.Slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("platform: create %s's media directory: %w", t.Slug, err)
		}
		cfg.MediaDir = dir
	}
	if t.Currency != "" {
		cfg.Currency = t.Currency
	}
	if len(t.Languages) > 0 {
		cfg.Languages = t.Languages
		cfg.DefaultLanguage = t.Languages[0]
	}
	if host := p.primaryHost(t); host != "" && cfg.PanelURL == "" {
		cfg.PanelURL = "https://" + host
	}
	app, err := gocommerce.New(cfg, p.cfg.Modules(*t)...)
	if err != nil {
		return err
	}
	handler := app.Handler()
	p.mu.Lock()
	running := p.runCtx
	p.apps[t.Slug] = app
	p.handlers[t.Slug] = handler
	p.mu.Unlock()
	if running != nil {
		if err := app.Start(running); err != nil {
			p.retire(t.Slug)
			return err
		}
	}
	return nil
}

// retire stops a store's engine and forgets it, leaving its data alone.
func (p *Platform) retire(slug string) {
	p.mu.Lock()
	app := p.apps[slug]
	delete(p.apps, slug)
	delete(p.handlers, slug)
	p.mu.Unlock()
	if app != nil {
		if err := app.Close(); err != nil {
			p.log.Warn("platform: closing a store", "store", slug, "error", err)
		}
	}
}

// remember files a store's hosts.
func (p *Platform) remember(t *Tenant) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old := p.tenants[t.Slug]; old != nil {
		for host, slug := range p.hosts {
			if slug == t.Slug {
				delete(p.hosts, host)
			}
		}
	}
	p.tenants[t.Slug] = t
	for _, h := range p.hostsOf(t) {
		p.hosts[h] = t.Slug
	}
}

func (p *Platform) forget(slug string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.tenants, slug)
	for host, s := range p.hosts {
		if s == slug {
			delete(p.hosts, host)
		}
	}
}

func (p *Platform) hostsOf(t *Tenant) []string {
	hosts := append([]string{}, t.Domains...)
	if p.cfg.BaseDomain != "" {
		hosts = append(hosts, t.Slug+"."+p.cfg.BaseDomain)
	}
	return hosts
}

func (p *Platform) primaryHost(t *Tenant) string {
	if len(t.Domains) > 0 {
		return t.Domains[0]
	}
	if p.cfg.BaseDomain != "" {
		return t.Slug + "." + p.cfg.BaseDomain
	}
	return ""
}

// App is a running store's engine, for a program that embeds the platform
// and needs to reach a store's services directly. Nil when the store is
// unknown or not running.
func (p *Platform) App(slug string) *gocommerce.App {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.apps[slug]
}

// Start runs every store's background work and every store booted later.
func (p *Platform) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.runCtx != nil {
		p.mu.Unlock()
		return nil
	}
	p.runCtx, p.stop = context.WithCancel(ctx)
	apps := make(map[string]*gocommerce.App, len(p.apps))
	for slug, app := range p.apps {
		apps[slug] = app
	}
	running := p.runCtx
	p.mu.Unlock()
	for slug, app := range apps {
		if err := app.Start(running); err != nil {
			p.log.Error("platform: a store's background work failed to start", "store", slug, "error", err)
		}
	}
	return nil
}

// Close stops every store and closes the platform's database.
func (p *Platform) Close() error {
	p.mu.Lock()
	if p.stop != nil {
		p.stop()
	}
	slugs := make([]string, 0, len(p.apps))
	for slug := range p.apps {
		slugs = append(slugs, slug)
	}
	p.mu.Unlock()
	for _, slug := range slugs {
		p.retire(slug)
	}
	return p.db.Close()
}

// ListenAndServe starts the stores and serves every one of them, and the
// platform API, until SIGINT or SIGTERM.
func (p *Platform) ListenAndServe() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := p.Start(ctx); err != nil {
		return err
	}
	srv := &http.Server{
		Addr: p.cfg.Addr, Handler: p.Handler(),
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		p.mu.RLock()
		n := len(p.apps)
		p.mu.RUnlock()
		p.log.Info("platform listening", "addr", p.cfg.Addr, "stores", n,
			"platform_hosts", p.cfg.PlatformHosts, "base_domain", p.cfg.BaseDomain)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()
	select {
	case err := <-errc:
		p.Close()
		return err
	case <-ctx.Done():
		p.log.Info("shutdown signal received")
	}
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := srv.Shutdown(sctx)
	if cerr := p.Close(); err == nil {
		err = cerr
	}
	return err
}

// Handler routes every request: the platform's hosts to the platform API, a
// shared API host to the store its X-Store header names, and any other host
// to the store it belongs to.
func (p *Platform) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := normalizeHost(r.Host)
		if contains(p.cfg.PlatformHosts, host) {
			p.api.ServeHTTP(w, r)
			return
		}
		name := host
		if contains(p.cfg.APIHosts, host) {
			name = normalizeHost(r.Header.Get("X-Store"))
			if name == "" {
				gocommerce.RespondError(w, r, gocommerce.Validationf("name the store in the X-Store header"))
				return
			}
		}
		p.mu.RLock()
		slug, ok := p.hosts[name]
		if !ok && contains(p.cfg.APIHosts, host) {
			// On a shared host the header may carry the slug itself.
			if _, known := p.tenants[name]; known {
				slug, ok = name, true
			}
		}
		handler := p.handlers[slug]
		t := p.tenants[slug]
		p.mu.RUnlock()
		switch {
		case !ok || t == nil:
			gocommerce.RespondError(w, r, gocommerce.NotFoundf("there is no store at this address"))
		case handler == nil:
			storeUnavailable(w)
		default:
			handler.ServeHTTP(w, r)
		}
	})
}

// ------------------------------------------------------------------ schema

// The platform's migrations, append-only like core's: a shipped one is frozen.
var migrations = []struct{ id, sql string }{
	{"0001_tenants", `
		CREATE TABLE tenants (
		    id          bigserial   PRIMARY KEY,
		    slug        text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND length(slug) <= 40),
		    name        text        NOT NULL CHECK (name <> ''),
		    schema_name text        NOT NULL UNIQUE,
		    status      text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
		    currency    text        NOT NULL DEFAULT '',
		    languages   text[]      NOT NULL DEFAULT '{}',
		    -- The store's static admin token. Stored as itself, not hashed,
		    -- because the engine compares the token it is configured with;
		    -- this schema is the platform's, and no store can read it.
		    admin_token text        NOT NULL,
		    created_at  timestamptz NOT NULL DEFAULT now(),
		    updated_at  timestamptz NOT NULL DEFAULT now()
		);

		-- A domain reaches one store. position orders a store's domains, and
		-- the first is the one its panel and its emails name.
		CREATE TABLE domains (
		    domain     text        PRIMARY KEY CHECK (domain = lower(domain) AND domain <> ''),
		    tenant_id  bigint      NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
		    position   integer     NOT NULL DEFAULT 0,
		    created_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE INDEX domains_tenant_idx ON domains (tenant_id, position);`},
}

func (p *Platform) migrate(ctx context.Context) error {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("platform: migration lock: %w", err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey)

	if _, err := conn.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+p.schema+`;
		CREATE TABLE IF NOT EXISTS `+p.schema+`.migrations (
		    id text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("platform: create the platform schema: %w", err)
	}
	for _, m := range migrations {
		var done bool
		if err := conn.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM `+p.schema+`.migrations WHERE id = $1)`, m.id).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `SET LOCAL search_path TO `+p.schema+`;`+m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("platform: migration %s: %w", m.id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+p.schema+`.migrations (id) VALUES ($1)`, m.id); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ----------------------------------------------------------------- helpers

// storeUnavailable answers for a suspended store, or one that failed to boot.
// Written here rather than through RespondError, which treats every 5xx as a
// failure — logging it as one and replacing its message with "internal
// error" — and a store a platform operator suspended is neither.
func storeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Retry-After", "300")
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte(`{"error":{"code":"store_unavailable","message":"this store is not open right now"}}`))
}

// withSearchPath points a DSN at one schema for every pooled connection.
// libpq's options parameter is applied per connection, where a bare SET would
// reach only the connection that ran it — the same mechanism the engine's own
// tests isolate themselves with.
func withSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "options=" + url.QueryEscape("-csearch_path="+schema)
}

// schemaFor names a store's schema. The slug and the namespace are already
// restricted to plain identifier characters, so there is nothing to escape.
func (p *Platform) schemaFor(slug string) string {
	s := "store_" + strings.ReplaceAll(slug, "-", "_")
	if p.cfg.Namespace != "" {
		s = p.cfg.Namespace + "_" + s
	}
	return s
}

func normalizeHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(h, ".")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func newSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (p *Platform) authorized(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if len(auth) <= 7 || !strings.EqualFold(auth[:7], "bearer ") {
		return false
	}
	presented := []byte(strings.TrimSpace(auth[7:]))
	ok := false
	for _, t := range p.cfg.Tokens {
		if subtle.ConstantTimeCompare(presented, []byte(t)) == 1 {
			ok = true
		}
	}
	return ok
}
