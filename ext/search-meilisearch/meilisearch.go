// Package meilisearch keeps a Meilisearch index of the catalogue and answers
// storefront searches from it.
//
// Meilisearch is a search engine a store runs beside its database: typo
// tolerant, fast on a laptop, and with a JSON API a product document fits
// without ceremony. This module is the sync and the door: a worker walks the
// catalogue and pushes what changed, an operator can ask for a full rebuild,
// and the storefront searches through the store rather than holding a key
// of its own — though the search-only key is published for one that wants
// to talk to Meilisearch directly.
//
// Over net/http, no SDK (rule 2): indexing is one POST of a JSON array,
// searching one POST of a query. Configured from the Plugins screen, or
// from Config for a store that prefers environment variables; the screen
// wins where both say something.
//
//	app, err := gocommerce.New(cfg, meilisearch.New(meilisearch.Config{
//	    Host:   os.Getenv("MEILI_HOST"),
//	    APIKey: os.Getenv("MEILI_MASTER_KEY"),
//	}))
package meilisearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey    = "meilisearch"
	defaultIndex = "products"
	defaultPoll  = 30 * time.Second
	pageSize     = 200
	// fullEvery is how many polls pass between two full reconciliations —
	// the ones that also remove documents the catalogue no longer has.
	fullEvery = 20
)

// Config configures the module. Every field can also be set from the
// Plugins screen, which wins.
type Config struct {
	// Host is the Meilisearch address: http://127.0.0.1:7700.
	Host string
	// APIKey is a key allowed to write the index — the master key on a
	// laptop, an admin key in production.
	APIKey string
	// SearchKey is a search-only key the storefront may hold. Optional; the
	// proxy route uses APIKey when there is none.
	SearchKey string
	// Index is the index name. Defaults to "products".
	Index string
	// Poll is how often the worker looks for changed products. Zero is the
	// default (30s); negative turns the worker off, for a store that calls
	// Sync itself.
	Poll time.Duration
	// Client overrides the HTTP client.
	Client *http.Client
}

// Status is what the admin route reports.
type Status struct {
	Enabled    bool       `json:"enabled"`
	Configured bool       `json:"configured"`
	Host       string     `json:"host,omitempty"`
	Index      string     `json:"index"`
	Syncing    bool       `json:"syncing"`
	LastSync   *time.Time `json:"last_sync,omitempty"`
	Indexed    int        `json:"indexed"`
	Removed    int        `json:"removed"`
	LastError  string     `json:"last_error,omitempty"`
}

// Module is the sync worker and the routes.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	log    *slog.Logger
	client *http.Client

	mu       sync.Mutex
	syncing  bool
	lastSync time.Time
	indexed  int
	removed  int
	lastErr  string
	polls    int
	stop     chan struct{}
	done     chan struct{}
}

// New constructs the module.
func New(cfg Config) *Module {
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Module{cfg: cfg, client: client}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "meilisearch" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	m.log = app.Log().With("module", "meilisearch")

	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Meilisearch", Category: "search",
		Description:    "Typo-tolerant product search from a Meilisearch index the store keeps up to date. The storefront searches through GET /x/meilisearch/search, or straight at Meilisearch with the search key.",
		DefaultEnabled: m.cfg.Host != "" && m.cfg.APIKey != "",
		Docs:           "https://www.meilisearch.com/docs",
		Fields: []gocommerce.PluginField{
			{Key: "host", Label: "Meilisearch URL", Kind: "url", Required: m.cfg.Host == "", Public: true, Help: "http://127.0.0.1:7700, or the cloud address."},
			{Key: "api_key", Label: "API key", Kind: "secret", Required: m.cfg.APIKey == "", Help: "A key that may write the index — the master key locally, an admin key in production."},
			{Key: "search_key", Label: "Search-only key", Kind: "text", Public: true, Help: "Safe to hand to the storefront for searching Meilisearch directly."},
			{Key: "index", Label: "Index name", Kind: "text", Default: defaultIndex, Public: true},
		},
	})

	app.HandleAdminFunc("POST /api/admin/x/meilisearch/reindex", m.handleReindex, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("GET /api/admin/x/meilisearch/status", m.handleStatus, gocommerce.RightCatalogRead)
	app.HandleFunc("GET /x/meilisearch/search", m.handleSearch)

	if m.cfg.Poll >= 0 {
		app.OnStart(m.start)
		app.OnStop(m.shutdown)
	}
	return nil
}

// connection is where to talk and with what, after the Plugins screen has
// had its say over Config.
type connection struct {
	host, apiKey, searchKey, index string
	enabled                        bool
}

func (c connection) configured() bool { return c.host != "" && c.apiKey != "" }

func (m *Module) connection(ctx context.Context) (connection, error) {
	enabled, err := m.app.Plugins().Enabled(ctx, pluginKey)
	if err != nil {
		return connection{}, err
	}
	settings, err := m.app.Plugins().Settings(ctx, pluginKey)
	if err != nil {
		return connection{}, err
	}
	str := func(key string) string {
		s, _ := settings[key].(string)
		return strings.TrimSpace(s)
	}
	c := connection{
		enabled:   enabled,
		host:      strings.TrimRight(first(str("host"), m.cfg.Host), "/"),
		apiKey:    first(str("api_key"), m.cfg.APIKey),
		searchKey: first(str("search_key"), m.cfg.SearchKey),
		index:     first(str("index"), m.cfg.Index, defaultIndex),
	}
	return c, nil
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// ------------------------------------------------------------------ worker

func (m *Module) start(ctx context.Context) error {
	m.stop = make(chan struct{})
	m.done = make(chan struct{})
	poll := m.cfg.Poll
	if poll == 0 {
		poll = defaultPoll
	}
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			m.tick()
			select {
			case <-m.stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (m *Module) shutdown(ctx context.Context) error {
	if m.stop == nil {
		return nil
	}
	close(m.stop)
	select {
	case <-m.done:
	case <-ctx.Done():
	}
	return nil
}

// tick is one poll: nothing when the plugin is off, a full reconciliation
// every fullEvery polls and on the first, the changed products otherwise.
func (m *Module) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	conn, err := m.connection(ctx)
	if err != nil || !conn.enabled || !conn.configured() {
		return
	}
	m.mu.Lock()
	full := m.polls%fullEvery == 0
	m.polls++
	m.mu.Unlock()
	if _, _, err := m.Sync(ctx, full); err != nil {
		m.log.Warn("meilisearch sync failed", "error", err)
	}
}

// Sync pushes the catalogue to the index: every active product when full,
// the ones changed since the last sync otherwise. A full sync also removes
// documents the catalogue no longer has. It returns how many it wrote and
// removed, and is safe to call from anywhere — two at once take turns.
func (m *Module) Sync(ctx context.Context, full bool) (indexed, removed int, err error) {
	m.mu.Lock()
	if m.syncing {
		m.mu.Unlock()
		return 0, 0, errors.New("a sync is already running")
	}
	m.syncing = true
	since := m.lastSync
	m.mu.Unlock()
	started := time.Now()
	defer func() {
		m.mu.Lock()
		m.syncing = false
		if err != nil {
			m.lastErr = err.Error()
		} else {
			m.lastErr = ""
			m.lastSync = started
			m.indexed += indexed
			m.removed += removed
		}
		m.mu.Unlock()
	}()

	conn, err := m.connection(ctx)
	if err != nil {
		return 0, 0, err
	}
	if !conn.configured() {
		return 0, 0, errors.New("meilisearch is not configured: it needs a URL and an API key")
	}
	if full {
		if err := m.ensureIndex(ctx, conn); err != nil {
			return 0, 0, err
		}
		since = time.Time{}
	}

	var batch []document
	var stale []string
	active := map[string]bool{}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := m.putDocuments(ctx, conn, batch); err != nil {
			return err
		}
		indexed += len(batch)
		batch = batch[:0]
		return nil
	}
	for offset := 0; ; offset += pageSize {
		products, _, err := m.app.Products().ListProducts(ctx, gocommerce.ProductQuery{Limit: pageSize, Offset: offset})
		if err != nil {
			return indexed, removed, err
		}
		if len(products) == 0 {
			break
		}
		for _, p := range products {
			id := strconv.FormatInt(p.ID, 10)
			if p.Status == gocommerce.ProductActive {
				active[id] = true
			}
			if !full && !p.UpdatedAt.After(since) {
				continue
			}
			if p.Status != gocommerce.ProductActive {
				stale = append(stale, id)
				continue
			}
			// The listing is a summary; the document wants every variant.
			if len(p.Variants) == 0 {
				if p, err = m.app.Products().GetProduct(ctx, p.ID); err != nil {
					return indexed, removed, err
				}
			}
			batch = append(batch, m.document(p))
			if len(batch) >= pageSize {
				if err := flush(); err != nil {
					return indexed, removed, err
				}
			}
		}
		if len(products) < pageSize {
			break
		}
	}
	if err := flush(); err != nil {
		return indexed, removed, err
	}

	if full {
		// What the index holds that the catalogue does not: deleted products,
		// and anything a previous life of this store put there.
		held, err := m.documentIDs(ctx, conn)
		if err != nil {
			return indexed, removed, err
		}
		for _, id := range held {
			if !active[id] {
				stale = append(stale, id)
			}
		}
	}
	if len(stale) > 0 {
		if err := m.deleteDocuments(ctx, conn, stale); err != nil {
			return indexed, removed, err
		}
		removed = len(stale)
	}
	return indexed, removed, nil
}

// --------------------------------------------------------------- document

// document is a product as the index holds it: what a shopper searches by,
// what a result card shows, and what a storefront filters and sorts on.
type document struct {
	ID          int64    `json:"id"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Vendor      string   `json:"vendor,omitempty"`
	ProductType string   `json:"product_type,omitempty"`
	Tags        []string `json:"tags"`
	Category    string   `json:"category,omitempty"`
	Image       string   `json:"image,omitempty"`
	Currency    string   `json:"currency"`
	PriceMinor  int64    `json:"price_minor"`
	// PriceMaxMinor is the dearest variant, for "from 19.99" cards.
	PriceMaxMinor int64    `json:"price_max_minor"`
	InStock       bool     `json:"in_stock"`
	SKUs          []string `json:"skus"`
	Options       []string `json:"options,omitempty"`
	UpdatedAt     int64    `json:"updated_at"`
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func plainText(html string) string {
	text := tagRE.ReplaceAllString(html, " ")
	text = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(text)
	return strings.Join(strings.Fields(text), " ")
}

func (m *Module) document(p *gocommerce.Product) document {
	d := document{
		ID: p.ID, Slug: p.Slug, Title: p.Title, Description: plainText(p.Description),
		Vendor: p.Vendor, ProductType: p.ProductType, Tags: p.Tags, Image: p.ImageURL,
		Currency: p.Currency, UpdatedAt: p.UpdatedAt.Unix(),
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	if p.Category != nil {
		d.Category = p.Category.FullName
	}
	for i, v := range p.Variants {
		if !v.Active {
			continue
		}
		price := v.Price.AmountMinor
		if i == 0 || price < d.PriceMinor {
			d.PriceMinor = price
		}
		if price > d.PriceMaxMinor {
			d.PriceMaxMinor = price
		}
		if v.InStock(1) {
			d.InStock = true
		}
		d.SKUs = append(d.SKUs, v.SKU)
		for _, o := range v.Options {
			if !contains(d.Options, o) {
				d.Options = append(d.Options, o)
			}
		}
	}
	if d.SKUs == nil {
		d.SKUs = []string{}
	}
	return d
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// --------------------------------------------------------------- the API

func (m *Module) call(ctx context.Context, conn connection, key, method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, conn.host+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("meilisearch: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("meilisearch: read the response: %w", err)
	}
	if resp.StatusCode >= 300 {
		var fail struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(payload, &fail) == nil && fail.Message != "" {
			return payload, resp.StatusCode, fmt.Errorf("meilisearch: %s (%s)", fail.Message, fail.Code)
		}
		return payload, resp.StatusCode, fmt.Errorf("meilisearch: %s", resp.Status)
	}
	return payload, resp.StatusCode, nil
}

// ensureIndex creates the index if it is missing and sets what is searched,
// filtered and sorted on. Both calls are idempotent on Meilisearch's side.
func (m *Module) ensureIndex(ctx context.Context, conn connection) error {
	_, status, err := m.call(ctx, conn, conn.apiKey, http.MethodPost, "/indexes",
		map[string]any{"uid": conn.index, "primaryKey": "id"})
	if err != nil && status != http.StatusConflict && !strings.Contains(err.Error(), "index_already_exists") {
		return err
	}
	_, _, err = m.call(ctx, conn, conn.apiKey, http.MethodPatch, "/indexes/"+conn.index+"/settings", map[string]any{
		"searchableAttributes": []string{"title", "skus", "vendor", "product_type", "tags", "category", "options", "description"},
		"filterableAttributes": []string{"vendor", "product_type", "tags", "category", "in_stock", "price_minor", "options"},
		"sortableAttributes":   []string{"price_minor", "updated_at", "title"},
	})
	return err
}

func (m *Module) putDocuments(ctx context.Context, conn connection, docs []document) error {
	_, _, err := m.call(ctx, conn, conn.apiKey, http.MethodPost, "/indexes/"+conn.index+"/documents?primaryKey=id", docs)
	return err
}

func (m *Module) deleteDocuments(ctx context.Context, conn connection, ids []string) error {
	_, _, err := m.call(ctx, conn, conn.apiKey, http.MethodPost, "/indexes/"+conn.index+"/documents/delete-batch", ids)
	return err
}

// documentIDs is every id the index holds.
func (m *Module) documentIDs(ctx context.Context, conn connection) ([]string, error) {
	var out []string
	for offset := 0; ; offset += 1000 {
		payload, _, err := m.call(ctx, conn, conn.apiKey, http.MethodGet,
			fmt.Sprintf("/indexes/%s/documents?fields=id&limit=1000&offset=%d", conn.index, offset), nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Results []struct {
				ID json.Number `json:"id"`
			} `json:"results"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(payload, &page); err != nil {
			return nil, fmt.Errorf("meilisearch: could not read the document list: %w", err)
		}
		for _, r := range page.Results {
			out = append(out, r.ID.String())
		}
		if len(page.Results) < 1000 || offset+1000 >= page.Total {
			break
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- routes

func (m *Module) handleReindex(w http.ResponseWriter, r *http.Request) {
	indexed, removed, err := m.Sync(r.Context(), true)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("%v", err))
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]int{"indexed": indexed, "removed": removed})
}

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	conn, err := m.connection(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.mu.Lock()
	st := Status{
		Enabled: conn.enabled, Configured: conn.configured(), Host: conn.host, Index: conn.index,
		Syncing: m.syncing, Indexed: m.indexed, Removed: m.removed, LastError: m.lastErr,
	}
	if !m.lastSync.IsZero() {
		at := m.lastSync
		st.LastSync = &at
	}
	m.mu.Unlock()
	gocommerce.Respond(w, http.StatusOK, st)
}

// searchResult is what the storefront gets: Meilisearch's hits and the
// counts, without the storefront needing a key.
type searchResult struct {
	Hits       []json.RawMessage `json:"hits"`
	Query      string            `json:"query"`
	Total      int               `json:"total"`
	Limit      int               `json:"limit"`
	Offset     int               `json:"offset"`
	TimeMS     int               `json:"processing_ms"`
	Configured bool              `json:"configured"`
}

func (m *Module) handleSearch(w http.ResponseWriter, r *http.Request) {
	conn, err := m.connection(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if !conn.enabled || !conn.configured() {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("search is not enabled on this store"))
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	body := map[string]any{"q": q.Get("q"), "limit": limit, "offset": offset}
	if f := q.Get("filter"); f != "" {
		body["filter"] = f
	}
	if s := q.Get("sort"); s != "" {
		body["sort"] = strings.Split(s, ",")
	}
	payload, _, err := m.call(r.Context(), conn, first(conn.searchKey, conn.apiKey), http.MethodPost,
		"/indexes/"+url.PathEscape(conn.index)+"/search", body)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "search"))
		return
	}
	var raw struct {
		Hits               []json.RawMessage `json:"hits"`
		EstimatedTotalHits int               `json:"estimatedTotalHits"`
		TotalHits          int               `json:"totalHits"`
		ProcessingTimeMs   int               `json:"processingTimeMs"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read the search result"))
		return
	}
	total := raw.EstimatedTotalHits
	if raw.TotalHits > total {
		total = raw.TotalHits
	}
	hits := raw.Hits
	if hits == nil {
		hits = []json.RawMessage{}
	}
	gocommerce.Respond(w, http.StatusOK, searchResult{
		Hits: hits, Query: q.Get("q"), Total: total, Limit: limit, Offset: offset,
		TimeMS: raw.ProcessingTimeMs, Configured: true,
	})
}
