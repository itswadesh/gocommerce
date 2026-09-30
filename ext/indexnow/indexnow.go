// Package indexnow tells search engines a page changed, the moment it does.
//
// Registering it pings IndexNow whenever a product or collection changes:
//
//	app, err := gocommerce.New(cfg, indexnow.New())
//
// IndexNow is a ping and nothing more. One POST carrying the URLs that changed,
// and Bing, Yandex, Seznam and Naver fetch them rather than waiting to crawl.
// There is no account, no console and no verification round trip: a key file on
// the site is the verification, which is why this module can do the whole thing
// from a settings screen.
//
// Google does not participate. A store that wants Google to know sooner still
// needs Search Console and the sitemap this engine already serves; that is the
// half a person has to do in a browser, and this module does not pretend
// otherwise.
//
// # Where the key file goes, and why it is not here
//
// IndexNow proves ownership with a file, and it only trusts that file for URLs
// in its own folder and below. A key at /x/indexnow/abc.txt could therefore
// vouch for nothing but /x/indexnow/, which is not where any product page is —
// so it has to sit at the root of the site being submitted.
//
// That site is the storefront, and in a headless store the storefront is not
// this server. The URLs going to IndexNow are shop.example/products/…, so the
// key must answer at shop.example/abc.txt; serving it from this engine would
// put it on api.shop.example and prove ownership of the wrong host.
//
// So the engine keeps the key and the storefront serves it. Two ways, both one
// step: drop a file in the storefront's static directory, or add a route that
// fetches GET /x/indexnow/key from here — the second means rotating the key
// never needs a redeploy. Check() says whether it worked, so nobody has to
// diagnose a 403 from a search engine.
//
// # Why it batches
//
// An operator repricing a range edits thirty products in two minutes. Thirty
// pings is the behaviour IndexNow's own guidance asks you not to have, and it
// tells the search engines nothing that one ping would not. So URLs collect for
// a short window and go together.
package indexnow

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const pluginKey = "indexnow"

// Endpoint is the shared entry point, which forwards to every participating
// engine. Submitting to each of them separately is the thing it exists to
// stop, and doing it anyway is how a store gets rate limited by four services
// at once.
const Endpoint = "https://api.indexnow.org/indexnow"

// Config is what a host application may override. Everything here has a
// working default; a store that sets none of it still works.
type Config struct {
	// Endpoint overrides where submissions go. For tests.
	Endpoint string
	// Window is how long URLs collect before a submission goes. Zero takes the
	// default.
	Window time.Duration
	// HTTP overrides the client, for tests and for a store behind a proxy.
	HTTP *http.Client
}

// DefaultWindow is long enough to collect a bulk edit and short enough that a
// single change is still news. A shopper never waits on it: the submission is
// about a crawler's schedule, not a page load.
const DefaultWindow = 30 * time.Second

// Module is the pinger.
type Module struct {
	app *gocommerce.App
	cfg Config

	mu      sync.Mutex
	pending map[string]struct{}
	timer   *time.Timer
}

// New builds the module.
func New(cfg ...Config) *Module {
	m := &Module{pending: map[string]struct{}{}}
	if len(cfg) > 0 {
		m.cfg = cfg[0]
	}
	if m.cfg.Endpoint == "" {
		m.cfg.Endpoint = Endpoint
	}
	if m.cfg.Window <= 0 {
		m.cfg.Window = DefaultWindow
	}
	if m.cfg.HTTP == nil {
		m.cfg.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	return m
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "indexnow" }

// Migrations implements gocommerce.Module.
//
// None: the key lives in plugin settings and a submission is not worth a row.
// What was submitted and when is a question the search engine answers, and
// keeping a log here would be a second, always-staler copy of it.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "IndexNow", Category: "marketing",
		Description: "Tell Bing, Yandex, Seznam and Naver the moment a page changes, instead of " +
			"waiting for them to crawl. No account: the key file this serves is the whole of " +
			"the verification. Google does not take part — that one still needs Search Console.",
		Docs: "https://www.indexnow.org/documentation",
		Fields: []gocommerce.PluginField{
			{
				Key: "storefront_url", Label: "Storefront URL", Kind: "url", Required: true,
				Help: "Where the pages actually are. The key file is served from this engine, " +
					"so it has to be the same host.",
			},
			{
				Key: "key", Label: "Key", Kind: "text",
				Help: "Left empty, one is generated on the first submission. Anything from 8 " +
					"to 128 letters, digits and dashes.",
			},
			{Key: "product_path", Label: "Product page path", Kind: "text", Default: "/products/{slug}"},
			{Key: "collection_path", Label: "Collection page path", Kind: "text", Default: "/collections/{slug}"},
		},
	})

	// The key, for a storefront that would rather proxy it than keep a copy.
	// Public, because the key is public by design: it is on the storefront for
	// anyone to read, and that is what makes it proof of control rather than a
	// secret.
	app.HandleFunc("GET /x/indexnow/key", m.handleKey)

	// Setting up, from the panel: mint a key, and find out whether the
	// storefront is actually serving it.
	app.HandleAdminFunc("POST /api/admin/x/indexnow/key", m.handleMintKey, gocommerce.RightStoreWrite)
	app.HandleAdminFunc("GET /api/admin/x/indexnow/check", m.handleCheck, gocommerce.RightStoreWrite)

	app.Subscribe(gocommerce.EventProductCreated, m.onProduct)
	app.Subscribe(gocommerce.EventProductUpdated, m.onProduct)
	app.Subscribe(gocommerce.EventProductDeleted, m.onProduct)
	app.Subscribe(gocommerce.EventCollectionUpdated, m.onCollection)
	return nil
}

// ------------------------------------------------------------------ settings

type settings struct {
	enabled    bool
	storefront string
	key        string
	product    string
	collection string
}

func (m *Module) settings(ctx context.Context) (settings, error) {
	enabled, err := m.app.Plugins().Enabled(ctx, pluginKey)
	if err != nil {
		return settings{}, err
	}
	values, err := m.app.Plugins().Settings(ctx, pluginKey)
	if err != nil {
		return settings{}, err
	}
	str := func(key, fallback string) string {
		if s, _ := values[key].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
		return fallback
	}
	return settings{
		enabled:    enabled,
		storefront: strings.TrimRight(str("storefront_url", ""), "/"),
		key:        str("key", ""),
		product:    str("product_path", "/products/{slug}"),
		collection: str("collection_path", "/collections/{slug}"),
	}, nil
}

// ensureKey returns the store's key, generating and storing one the first time.
//
// Generated rather than demanded, because a key is an arbitrary string whose
// only job is to be hard to guess, and asking an operator to invent one is
// asking them to type "mystore123". Stored on first use rather than at
// registration so that installing the module writes nothing.
func (m *Module) ensureKey(ctx context.Context, s settings) (string, error) {
	if s.key != "" {
		return s.key, nil
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	key := hex.EncodeToString(buf)
	if _, err := m.app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{
		Settings: map[string]any{"key": key},
	}); err != nil {
		return "", fmt.Errorf("store the IndexNow key: %w", err)
	}
	return key, nil
}

// handleKey hands the key to whoever is serving the file.
//
// The body is the key and nothing else — no trailing newline. The protocol
// compares the file to the key literally, and a newline is the commonest reason
// a submission comes back 403 with nothing said about why. A storefront
// proxying this route gets that right by copying the body through.
func (m *Module) handleKey(w http.ResponseWriter, r *http.Request) {
	s, err := m.settings(r.Context())
	if err != nil || !s.enabled || s.key == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, s.key)
}

// handleMintKey makes a key if the store has none, and returns what to do with
// it — the filename, the contents, and where it has to answer.
//
// Everything the operator needs in one response, because the failure this
// avoids is subtle: a key file that is served but wrong answers 403 from a
// search engine days later, with no clue attached.
func (m *Module) handleMintKey(w http.ResponseWriter, r *http.Request) {
	s, err := m.settings(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	key, err := m.ensureKey(r.Context(), s)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{
		"key":          key,
		"filename":     key + ".txt",
		"contents":     key,
		"expected_url": strings.TrimRight(s.storefront, "/") + "/" + key + ".txt",
		"proxy_from":   "/x/indexnow/key",
	})
}

// handleCheck fetches the key file from the storefront and says whether it is
// right, before anything is submitted.
func (m *Module) handleCheck(w http.ResponseWriter, r *http.Request) {
	result, err := m.Check(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("%v", err))
		return
	}
	gocommerce.Respond(w, http.StatusOK, result)
}

// CheckResult is what the storefront is currently answering.
type CheckResult struct {
	URL string `json:"url"`
	OK  bool   `json:"ok"`
	// Problem is empty when OK. It says what to change, not what failed.
	Problem string `json:"problem,omitempty"`
	Status  int    `json:"status"`
}

// Check fetches the key file from the storefront and compares it byte for byte.
//
// The comparison is exact on purpose. A file with a trailing newline looks
// perfect in a browser and is refused by IndexNow, and that is the single
// commonest way this setup goes wrong.
func (m *Module) Check(ctx context.Context) (*CheckResult, error) {
	s, err := m.settings(ctx)
	if err != nil {
		return nil, err
	}
	if s.storefront == "" {
		return nil, fmt.Errorf("no storefront URL is set, so there is nowhere to look for the key")
	}
	key, err := m.ensureKey(ctx, s)
	if err != nil {
		return nil, err
	}
	target := s.storefront + "/" + key + ".txt"
	out := &CheckResult{URL: target}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.cfg.HTTP.Do(req)
	if err != nil {
		out.Problem = "the storefront did not answer: " + err.Error()
		return out, nil
	}
	defer resp.Body.Close()
	out.Status = resp.StatusCode

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		out.Problem = "nothing is served at " + target + " yet — put a file called " +
			key + ".txt there containing the key, or proxy /x/indexnow/key to it"
	case resp.StatusCode >= 400:
		out.Problem = fmt.Sprintf("the storefront answered %d", resp.StatusCode)
	case string(body) == key:
		out.OK = true
	case strings.TrimSpace(string(body)) == key:
		// Named exactly, because it is invisible and it is the usual culprit.
		out.Problem = "the file has the right key but extra whitespace around it — most likely a " +
			"trailing newline, which IndexNow refuses. Write the key with no newline at the end"
	default:
		out.Problem = "the file is served but does not contain this store's key"
	}
	return out, nil
}

// ------------------------------------------------------------------ the ping

type productPayload struct {
	Slug         string `json:"slug"`
	PreviousSlug string `json:"previous_slug"`
}

func (m *Module) onProduct(ctx context.Context, e gocommerce.Event) error {
	var p productPayload
	if err := e.Decode(&p); err != nil {
		return nil
	}
	s, err := m.settings(ctx)
	if err != nil || !s.enabled || s.storefront == "" {
		return nil
	}
	m.queue(pageURL(s.storefront, s.product, p.Slug))
	// A rename leaves an address behind. Submitting it too is what asks a
	// search engine to look at the 404 and drop it, rather than keeping a dead
	// URL in the index until it happens to recrawl.
	if p.PreviousSlug != "" && p.PreviousSlug != p.Slug {
		m.queue(pageURL(s.storefront, s.product, p.PreviousSlug))
	}
	return nil
}

func (m *Module) onCollection(ctx context.Context, e gocommerce.Event) error {
	var p productPayload
	if err := e.Decode(&p); err != nil {
		return nil
	}
	s, err := m.settings(ctx)
	if err != nil || !s.enabled || s.storefront == "" {
		return nil
	}
	m.queue(pageURL(s.storefront, s.collection, p.Slug))
	return nil
}

func pageURL(storefront, path, slug string) string {
	if slug == "" {
		return ""
	}
	return storefront + strings.ReplaceAll(path, "{slug}", url.PathEscape(slug))
}

// queue collects a URL and starts the window if it is not already running.
//
// A set, so an operator saving the same product four times submits it once.
func (m *Module) queue(target string) {
	if target == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[target] = struct{}{}
	if m.timer == nil {
		m.timer = time.AfterFunc(m.cfg.Window, m.flush)
	}
}

// flush submits everything collected.
func (m *Module) flush() {
	m.mu.Lock()
	urls := make([]string, 0, len(m.pending))
	for u := range m.pending {
		urls = append(urls, u)
	}
	m.pending = map[string]struct{}{}
	m.timer = nil
	m.mu.Unlock()

	if len(urls) == 0 {
		return
	}
	// Its own context: the request that caused the change finished long ago,
	// and hanging this off it would cancel every submission.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Submit(ctx, urls); err != nil {
		// Logged and dropped. A failed ping means a page is indexed on the
		// crawler's own schedule instead of sooner, which is where every store
		// without this module already is — not worth a retry queue, and
		// certainly not worth failing anything a shopper is waiting on.
		m.app.Log().Warn("indexnow submission failed", slog.Any("error", err), slog.Int("urls", len(urls)))
	}
}

// Submit sends URLs to IndexNow.
//
// Exported because it is the whole of what this module does, and a host that
// wants to submit something this module does not know about — a CMS page, a
// hand-built landing page — should not have to reimplement the protocol.
func (m *Module) Submit(ctx context.Context, urls []string) error {
	s, err := m.settings(ctx)
	if err != nil {
		return err
	}
	if s.storefront == "" {
		return fmt.Errorf("indexnow: no storefront URL configured, so there is nothing to submit for")
	}
	key, err := m.ensureKey(ctx, s)
	if err != nil {
		return err
	}
	host, err := url.Parse(s.storefront)
	if err != nil || host.Host == "" {
		return fmt.Errorf("indexnow: %q is not a URL", s.storefront)
	}

	// IndexNow takes at most 10,000 in one request, and every URL must be on
	// the host the key belongs to — a mixed batch is refused whole, so the
	// ones that do not belong are dropped here rather than losing the rest.
	mine := make([]string, 0, len(urls))
	for _, u := range urls {
		if strings.HasPrefix(u, s.storefront+"/") || u == s.storefront {
			mine = append(mine, u)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	if len(mine) > 10000 {
		mine = mine[:10000]
	}

	body, err := json.Marshal(map[string]any{
		"host":        host.Host,
		"key":         key,
		"keyLocation": s.storefront + "/" + key + ".txt",
		"urlList":     mine,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := m.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		// 202 is "received, key validation pending" and is the ordinary answer
		// to a first submission. Both are success.
		return nil
	case http.StatusForbidden:
		return fmt.Errorf("indexnow: the key was refused — check %s/%s.txt is reachable and "+
			"contains exactly the key with no trailing newline", s.storefront, key)
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("indexnow: the URLs do not belong to %s, or they do not match the key",
			host.Host)
	case http.StatusTooManyRequests:
		return fmt.Errorf("indexnow: rate limited — submitting too often, or too many URLs")
	default:
		return fmt.Errorf("indexnow: %s answered %d", m.cfg.Endpoint, resp.StatusCode)
	}
}
