// Package amazon imports a product from an Amazon listing.
//
// Registering it adds an "Import from Amazon" drawer to the panel's products
// page and three admin routes under /api/admin/x/import-amazon. An operator
// pastes a product URL; the module opens it in a real Chrome, reads the
// listing and every variation, rewrites the copy with Claude, adjusts the
// pictures, and creates the product as a draft for review.
//
//	app, err := gocommerce.New(cfg,
//	    amazon.New(amazon.Config{
//	        AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
//	        Headed:          true,
//	    }),
//	)
//
// # Why a real browser, and what it does not do
//
// Amazon writes a product page's variations and hi-res image list with
// JavaScript after load, and answers anything that is not a browser with a
// robot check. So this module drives Chrome — the one installed on the machine,
// over the DevTools Protocol, with no automation library and no changes to
// what the browser says it is. It reads the page a shopper would see.
//
// When Amazon shows the robot check anyway, the job is marked *blocked* and
// says so. The module does not solve CAPTCHAs and does not pretend to be a
// different browser: the way past a check is a person passing it once in the
// headed browser (Headed: true), after which the persistent profile keeps the
// session and later imports go through. A store importing at volume should be
// on Amazon's Product Advertising API, which is the sanctioned route and does
// not have this problem.
//
// # Three things to know before pointing this at a listing
//
//   - Amazon's terms of use forbid scraping. This module makes that easy to do
//     and does not make it allowed.
//   - The copy and the photographs are the seller's, or Amazon's. The rewrite
//     produces new copy from the listing's facts; the pictures are the same
//     pictures with the lighting and orientation changed, which is a change to
//     the pixels and not to the copyright. See images.go.
//   - The product is created as a draft. Prices come across in the
//     marketplace's currency; when that is not the store's, they are recorded
//     but not set, and the job says so.
//
// # Zero dependencies
//
// The WebSocket client, the DevTools client, the Messages API call and the
// image adjustments are all standard library, per rule 2. Chrome itself is the
// one thing the module needs installed, and it is found where Chrome usually
// lives or where CHROME_PATH says.
package amazon

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/misiki/gocommerce/core"
)

// Config configures the module. Nothing is required: without an API key the
// listing is used as scraped, and Chrome is found where it usually is.
type Config struct {
	// ChromePath is the browser executable. Empty means look in the usual
	// places, then CHROME_PATH.
	ChromePath string
	// Headed shows the browser window rather than running headless. Off on a
	// server with no display; on wherever an operator can pass Amazon's robot
	// check once and leave the profile signed in.
	Headed bool
	// ProfileDir is Chrome's user-data directory. It persists between imports
	// on purpose: it is where a passed robot check and a signed-in session
	// live. Defaults to a directory under the OS temp directory.
	ProfileDir string
	// The rewrite has four doors, and the first one configured is used:
	//
	//   - AnthropicAPIKey: Claude, through the Messages API.
	//   - GeminiAPIKey: Google's Gemini, through its OpenAI-compatible
	//     endpoint. Google AI Studio hands these out with a free tier.
	//   - OpenAIAPIKey: OpenAI, through chat completions.
	//   - LLMBaseURL alone: a local OpenAI-compatible server — Ollama at
	//     http://127.0.0.1:11434/v1, LM Studio at http://127.0.0.1:1234/v1 —
	//     with no key and no bill. Name the model in Model.
	//
	// LLMBaseURL also overrides the endpoint for the Gemini and OpenAI doors,
	// and carries the version segment, because the three differ in it.
	//
	// None of them configured means the listing is used as scraped, and each
	// job says so in its warnings. A Claude.ai or ChatGPT subscription is not
	// a door: those are products for people, with no endpoint a server calls.
	AnthropicAPIKey string
	GeminiAPIKey    string
	OpenAIAPIKey    string
	LLMBaseURL      string
	// Model names the model at whichever door is open. Defaults to
	// claude-sonnet-5, gemini-2.0-flash and gpt-4o-mini for the three
	// services; a local server has no sensible default, so name the one you
	// pulled.
	Model string
	// AnthropicBaseURL overrides the Anthropic endpoint, for tests.
	AnthropicBaseURL string
	// MaxVariants bounds how many variation pages one import visits. The rest
	// are created with the parent's price and pictures. Defaults to 30.
	MaxVariants int
	// MaxImages bounds how many pictures one import downloads across the
	// product and its variants. Defaults to 40.
	MaxImages int
	// PageSettle is how long to wait after a page's load event for its
	// scripts to finish writing the variation widget. Defaults to 1.5s.
	PageSettle time.Duration
	// JobTimeout bounds one import end to end. Defaults to 15 minutes.
	JobTimeout time.Duration
	// HumanWait is how long a headed import waits for a person to click
	// through Amazon's "are you a robot" page in the Chrome window before
	// giving up. Headless imports do not wait: nobody can see the window.
	// Defaults to 3 minutes.
	HumanWait time.Duration
	// ImageClient overrides the HTTP client pictures are fetched with.
	ImageClient *http.Client
}

// Module is the Amazon importer.
type Module struct {
	cfg       Config
	app       *gocommerce.App
	log       *slog.Logger
	db        *sql.DB
	optimizer *optimizer
	imgClient *http.Client

	ctx    context.Context
	cancel context.CancelFunc
	// One import at a time: they share one browser tab, and a second Chrome
	// per job is not a thing a store wants.
	sem chan struct{}

	fmu     sync.Mutex
	fetcher pageFetcher
	// newFetcher builds the browser on first use. The tests swap it for a
	// fixture-backed one so a suite does not need Chrome, and so the parts
	// that are this module's — the walk, the rewrite, the pictures, the
	// product — are proven against known pages.
	newFetcher func(ctx context.Context) (pageFetcher, error)
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "import-amazon" }

// Migrations implements gocommerce.Module.
//
// One table: the jobs. An import takes a minute and the panel polls it, and a
// job that survives a restart is one an operator can still read the outcome
// of — or the reason it stopped.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_jobs",
		SQL: `
			CREATE TABLE import_amazon_jobs (
			    id          bigserial   PRIMARY KEY,
			    url         text        NOT NULL,
			    asin        text        NOT NULL DEFAULT '',
			    status      text        NOT NULL,
			    step        text        NOT NULL DEFAULT '',
			    message     text        NOT NULL DEFAULT '',
			    product_id  bigint,
			    warnings    jsonb       NOT NULL DEFAULT '[]',
			    options     jsonb       NOT NULL DEFAULT '{}',
			    created_at  timestamptz NOT NULL DEFAULT now(),
			    updated_at  timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX import_amazon_jobs_created_idx
			    ON import_amazon_jobs (created_at DESC);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	if m.cfg.ProfileDir == "" {
		m.cfg.ProfileDir = filepath.Join(os.TempDir(), "gocommerce-import-amazon-profile")
	}
	if m.cfg.MaxVariants <= 0 {
		m.cfg.MaxVariants = 30
	}
	if m.cfg.MaxImages <= 0 {
		m.cfg.MaxImages = 40
	}
	if m.cfg.PageSettle <= 0 {
		m.cfg.PageSettle = 1500 * time.Millisecond
	}
	if m.cfg.JobTimeout <= 0 {
		m.cfg.JobTimeout = 15 * time.Minute
	}
	if m.cfg.HumanWait <= 0 {
		m.cfg.HumanWait = 3 * time.Minute
	}
	m.imgClient = m.cfg.ImageClient
	if m.imgClient == nil {
		m.imgClient = &http.Client{Timeout: 60 * time.Second}
	}
	m.app = app
	m.log = app.Log()
	m.db = app.DB()
	m.optimizer = newOptimizer(m.cfg)
	m.sem = make(chan struct{}, 1)
	// Owned here rather than taken from OnStart, so a job posted the moment
	// the routes are up has a context — and so the tests, which never call
	// ListenAndServe, have one too.
	m.ctx, m.cancel = context.WithCancel(context.Background())
	if m.newFetcher == nil {
		m.newFetcher = func(ctx context.Context) (pageFetcher, error) {
			return newChromeFetcher(ctx, m.cfg.ChromePath, m.cfg.ProfileDir, !m.cfg.Headed, m.cfg.PageSettle, m.cfg.HumanWait, m.log)
		}
	}

	// catalog.write for all three: the list and the job are an audit of
	// products being written, and reading them is the same act as starting
	// one.
	app.HandleAdminFunc("POST /api/admin/x/import-amazon/jobs", m.handleCreate, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("GET /api/admin/x/import-amazon/jobs", m.handleList, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("GET /api/admin/x/import-amazon/jobs/{id}", m.handleGet, gocommerce.RightCatalogWrite)

	app.OnStop(func(context.Context) error {
		m.cancel()
		m.dropFetcher()
		return nil
	})
	return nil
}

// ------------------------------------------------------------------- jobs

// Job is one import, as the panel reads it.
type Job struct {
	ID        int64         `json:"id"`
	URL       string        `json:"url"`
	ASIN      string        `json:"asin,omitempty"`
	Status    string        `json:"status"`
	Step      string        `json:"step,omitempty"`
	Message   string        `json:"message,omitempty"`
	ProductID *int64        `json:"product_id,omitempty"`
	Warnings  []string      `json:"warnings"`
	Options   importOptions `json:"options"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// Job statuses.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
	// StatusBlocked is Amazon's robot check: fixed by a person, not a retry.
	StatusBlocked = "blocked"
)

// importOptions is what one job was asked to do, recorded with it.
type importOptions struct {
	Images      imageOptions `json:"images"`
	MaxVariants int          `json:"max_variants"`
	// Status the product is created with: draft (the default) or active.
	Status string `json:"status"`
	// PriceMinor, when set, is the price every variant gets in the store's
	// currency — the way to import from a marketplace that sells in another.
	PriceMinor *int64 `json:"price_minor,omitempty"`
}

type createRequest struct {
	URL         string        `json:"url"`
	Images      *imageOptions `json:"images"`
	MaxVariants int           `json:"max_variants"`
	Status      string        `json:"status"`
	PriceMinor  *int64        `json:"price_minor"`
}

// defaultImages is what an import does to pictures when not told otherwise,
// and it is the three things that can change without changing what the
// product looks like: the plain background replaced with a soft gradient and
// a shadow; the lighting lifted a touch; the orientation turned by three
// degrees, drawn a little smaller so the turn fits, and mirrored. Colour is
// deliberately left alone — warmth and saturation are available, and off,
// because they change the product and not the photograph. The mirror is on
// because it was asked for; it reverses any text in a picture, and the
// drawer says so beside the switch.
var defaultImages = imageOptions{
	Brightness: 0.03, Contrast: 1.04,
	Background: "gradient", BackgroundColor: "#F6F7F9", BackgroundTo: "#E4E7EC",
	Scale: 0.92, Tilt: 3, Shadow: true, Flip: "horizontal",
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in createRequest
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || u.Host == "" || !strings.Contains(strings.ToLower(u.Host), "amazon.") {
		gocommerce.RespondError(w, r, gocommerce.Validationf("url must be an Amazon product page"))
		return
	}
	opts := importOptions{Images: defaultImages, MaxVariants: m.cfg.MaxVariants, Status: "draft", PriceMinor: in.PriceMinor}
	if in.Images != nil {
		opts.Images = *in.Images
	}
	if err := opts.Images.validate(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("images: %v", err))
		return
	}
	if in.MaxVariants > 0 {
		opts.MaxVariants = min(in.MaxVariants, 200)
	}
	switch in.Status {
	case "", "draft":
	case "active":
		opts.Status = "active"
	default:
		gocommerce.RespondError(w, r, gocommerce.Validationf("status must be draft or active"))
		return
	}
	if in.PriceMinor != nil && *in.PriceMinor < 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("price_minor cannot be negative"))
		return
	}

	encoded, _ := json.Marshal(opts)
	var id int64
	if err := m.db.QueryRowContext(r.Context(), `
		INSERT INTO import_amazon_jobs (url, status, step, options)
		VALUES ($1, $2, 'queued', $3) RETURNING id`,
		u.String(), StatusQueued, encoded).Scan(&id); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record the import"))
		return
	}
	go m.run(id)

	job, err := m.load(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusAccepted, job)
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.QueryContext(r.Context(), `
		SELECT id, url, asin, status, step, message, product_id, warnings, options, created_at, updated_at
		FROM import_amazon_jobs ORDER BY id DESC LIMIT 25`)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list imports"))
		return
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read an import"))
			return
		}
		jobs = append(jobs, *job)
	}
	gocommerce.RespondList(w, jobs, gocommerce.ListMeta{Total: len(jobs), Limit: 25})
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no import %q", r.PathValue("id")))
		return
	}
	job, err := m.load(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, job)
}

func (m *Module) load(ctx context.Context, id int64) (*Job, error) {
	row := m.db.QueryRowContext(ctx, `
		SELECT id, url, asin, status, step, message, product_id, warnings, options, created_at, updated_at
		FROM import_amazon_jobs WHERE id = $1`, id)
	job, err := scanJob(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no import %d", id)
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the import")
	}
	return job, nil
}

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var warnings, options []byte
	if err := row.Scan(&j.ID, &j.URL, &j.ASIN, &j.Status, &j.Step, &j.Message, &j.ProductID,
		&warnings, &options, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(warnings, &j.Warnings)
	_ = json.Unmarshal(options, &j.Options)
	if j.Warnings == nil {
		j.Warnings = []string{}
	}
	return &j, nil
}

// progress records where a job has got to. The panel reads step and message
// while it waits, so a stall shows *where* it stalled.
func (m *Module) progress(ctx context.Context, id int64, status, step, message string) {
	if _, err := m.db.ExecContext(ctx, `
		UPDATE import_amazon_jobs SET status = $2, step = $3, message = $4, updated_at = now()
		WHERE id = $1`, id, status, step, message); err != nil {
		m.log.Warn("could not record import progress", "job", id, "error", err)
	}
}

func (m *Module) finish(ctx context.Context, id int64, status string, productID *int64, message string, asin string, warnings []string) {
	if warnings == nil {
		warnings = []string{}
	}
	encoded, _ := json.Marshal(warnings)
	if _, err := m.db.ExecContext(ctx, `
		UPDATE import_amazon_jobs
		SET status = $2, step = '', message = $3, product_id = $4, asin = $5, warnings = $6, updated_at = now()
		WHERE id = $1`, id, status, message, productID, asin, encoded); err != nil {
		m.log.Error("could not record the import's outcome", "job", id, "error", err)
	}
}

// ------------------------------------------------------------------ running

// run is one import, start to finish, on its own goroutine.
func (m *Module) run(id int64) {
	select {
	case m.sem <- struct{}{}:
	case <-m.ctx.Done():
		return
	}
	defer func() { <-m.sem }()

	ctx, cancel := context.WithTimeout(m.ctx, m.cfg.JobTimeout)
	defer cancel()

	job, err := m.load(ctx, id)
	if err != nil {
		m.log.Error("import job vanished", "job", id, "error", err)
		return
	}
	// The outcome is written with a context that outlives the job's: a job
	// that timed out still needs its failure recorded.
	record := context.Background()

	m.progress(ctx, id, StatusRunning, "fetching", "Opening the listing in Chrome")
	fetcher, err := m.getFetcher(ctx)
	if err != nil {
		m.finish(record, id, StatusFailed, nil, "Could not start Chrome: "+err.Error(), "", nil)
		return
	}

	if cf, ok := fetcher.(*chromeFetcher); ok {
		// While a person is being waited for, the job says so — the panel is
		// polling, and "fetching" for three minutes reads as a hang.
		cf.onWait = func(message string) { m.progress(ctx, id, StatusRunning, "waiting", message) }
	}
	listing, warnings, err := crawl(ctx, fetcher, job.URL, job.Options.MaxVariants)
	if err != nil {
		if errors.Is(err, errBlocked) {
			m.finish(record, id, StatusBlocked, nil,
				"Amazon showed a robot check instead of the product. Run the store with Headed: true and retry: the Chrome window will wait for you to click through once, and the profile keeps the session for later imports.",
				"", nil)
			return
		}
		if strings.Contains(err.Error(), "chrome:") {
			// The browser is in a state this module cannot reason about;
			// the next job gets a fresh one.
			m.dropFetcher()
		}
		m.finish(record, id, StatusFailed, nil, "Could not read the listing: "+err.Error(), "", nil)
		return
	}
	m.progress(ctx, id, StatusRunning, "fetched",
		fmt.Sprintf("Read %s with %d variants and %d pictures", listing.ASIN, len(listing.Variants), len(listing.Images)))

	// The rewrite.
	var rw *rewrite
	m.progress(ctx, id, StatusRunning, "optimizing", "Rewriting the copy")
	if m.optimizer == nil {
		warnings = append(warnings, "no model is configured for the rewrite (an Anthropic, Gemini or OpenAI key, or a local server's URL), so the listing's own copy was used")
	} else if rw, err = m.optimizer.optimize(ctx, listing); err != nil {
		warnings = append(warnings, "the rewrite failed, so the listing's own copy was used: "+err.Error())
		rw = nil
	}

	// The pictures.
	m.progress(ctx, id, StatusRunning, "images", "Downloading and adjusting the pictures")
	media, mediaWarnings := m.importImages(ctx, listing, job.Options.Images)
	warnings = append(warnings, mediaWarnings...)

	// The product.
	m.progress(ctx, id, StatusRunning, "creating", "Creating the product")
	product, createWarnings, err := m.createProduct(ctx, listing, rw, job.Options, media)
	warnings = append(warnings, createWarnings...)
	if err != nil {
		m.finish(record, id, StatusFailed, nil, "Could not create the product: "+err.Error(), listing.ASIN, warnings)
		return
	}

	m.finish(record, id, StatusDone, &product.ID,
		fmt.Sprintf("Created %q as a %s with %d variants", product.Title, product.Status, len(product.Variants)),
		listing.ASIN, warnings)
	m.log.Info("imported a product from Amazon", "job", id, "asin", listing.ASIN, "product_id", product.ID)
}

func (m *Module) getFetcher(ctx context.Context) (pageFetcher, error) {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	if m.fetcher != nil {
		return m.fetcher, nil
	}
	f, err := m.newFetcher(ctx)
	if err != nil {
		return nil, err
	}
	m.fetcher = f
	return f, nil
}

func (m *Module) dropFetcher() {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	if m.fetcher != nil {
		_ = m.fetcher.close()
		m.fetcher = nil
	}
}

// imported is what the picture step produced: media ids in display order, and
// which variant nominates which.
type imported struct {
	order     []int64
	byVariant map[string][]int64 // variant ASIN → its pictures, in page order
}

// importImages fetches, adjusts and stores every distinct picture, parent
// first, then each variant's, up to the cap.
func (m *Module) importImages(ctx context.Context, l *Listing, o imageOptions) (imported, []string) {
	out := imported{byVariant: map[string][]int64{}}
	var warnings []string
	seen := map[string]int64{}
	linkedOnly := false
	total := 0
	var kept int // pictures whose product could not be separated from its background

	store := func(rawURL, alt string) (int64, bool) {
		if id, ok := seen[rawURL]; ok {
			return id, true
		}
		if total >= m.cfg.MaxImages {
			return 0, false
		}
		total++

		data, err := fetchImage(ctx, m.imgClient, rawURL)
		if err != nil {
			warnings = append(warnings, "could not download "+rawURL+": "+err.Error())
			return 0, false
		}
		filename := l.ASIN + "-" + strconv.Itoa(total) + ".jpg"
		if !o.noop() {
			if processed, _, _, how, err := processImage(data, o); err != nil {
				warnings = append(warnings, "could not adjust "+rawURL+": "+err.Error()+"; stored as downloaded")
			} else {
				data = processed
				if how == treatedAround && (o.replacesBackground() || o.changesGeometry()) {
					kept++
				}
			}
		}

		item, err := m.app.MediaLibrary().Upload(ctx, filename, "image/jpeg", bytes.NewReader(data), int64(len(data)))
		if err != nil {
			var apiErr *gocommerce.APIError
			if errors.As(err, &apiErr) && apiErr.Code == "media_store_unconfigured" {
				// No disk to put pictures on: link the marketplace's own,
				// unadjusted, and say so once. A product with pictures that
				// are somebody else's URLs is worse than one with its own and
				// better than one with none.
				if !linkedOnly {
					linkedOnly = true
					warnings = append(warnings, "this store has no media directory (Config.MediaDir), so pictures were linked from Amazon rather than adjusted and stored")
				}
				item, err = m.app.MediaLibrary().AddURL(ctx, rawURL, gocommerce.MediaImage, alt)
			}
			if err != nil {
				warnings = append(warnings, "could not store "+rawURL+": "+err.Error())
				return 0, false
			}
		}
		if alt != "" {
			_, _ = m.app.MediaLibrary().SetAlt(ctx, item.ID, alt)
		}
		seen[rawURL] = item.ID
		out.order = append(out.order, item.ID)
		return item.ID, true
	}

	for _, u := range l.Images {
		store(u, l.Title)
	}
	for _, v := range l.Variants {
		alt := strings.TrimSpace(l.Title + " " + strings.Join(v.Options, " "))
		for _, u := range v.Images {
			if id, ok := store(u, alt); ok {
				out.byVariant[v.ASIN] = append(out.byVariant[v.ASIN], id)
			}
		}
	}
	if total >= m.cfg.MaxImages {
		warnings = append(warnings, fmt.Sprintf("stopped after %d pictures (Config.MaxImages)", m.cfg.MaxImages))
	}
	if kept > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d picture(s) show a product the same colour as its background, so the product was left exactly as photographed and not turned; only the background around the edges was blended toward the new one", kept))
	}
	return out, warnings
}

// createProduct turns the listing into a draft, attaches its pictures, and
// points each variant at its own.
func (m *Module) createProduct(ctx context.Context, l *Listing, rw *rewrite, opts importOptions, media imported) (*gocommerce.Product, []string, error) {
	var warnings []string
	storeCurrency := strings.ToUpper(m.app.Config().Currency)
	status := opts.Status

	// Prices only cross when they are already in the store's currency; a
	// converted figure would be this module inventing a price. Otherwise the
	// operator's price applies to every variant, or nothing is set and the
	// product stays a draft with the marketplace prices on record.
	priceFor := func(minor int64) int64 {
		switch {
		case opts.PriceMinor != nil:
			return *opts.PriceMinor
		case l.Currency == storeCurrency:
			return minor
		default:
			return 0
		}
	}
	if opts.PriceMinor == nil && l.Currency != storeCurrency {
		warnings = append(warnings, fmt.Sprintf(
			"prices are in %s and this store sells in %s, so none were set — they are recorded in the product's metadata; set them before publishing", l.Currency, storeCurrency))
		status = "draft"
	}

	title, description := l.Title, plainDescription(l)
	in := gocommerce.ProductInput{
		Status: status, Vendor: l.Brand, Tags: []string{},
		Metadata: gocommerce.Metadata{"amazon": map[string]any{
			"asin": l.ASIN, "url": l.URL, "brand": l.Brand,
			"price_minor": l.PriceMinor, "currency": l.Currency,
			"specs": l.Specs, "breadcrumbs": l.Breadcrumbs, "bullets": l.Bullets,
			"rating": l.Rating, "review_count": l.ReviewCount, "reviews": l.Reviews,
			"imported_at": time.Now().UTC().Format(time.RFC3339),
			"rewritten":   rw != nil,
		}},
	}
	if rw != nil {
		title = firstNonEmpty(rw.Title, title)
		description = firstNonEmpty(rw.Description, description)
		in.ProductType = rw.ProductType
		in.Vendor = firstNonEmpty(rw.Vendor, l.Brand)
		in.Tags = rw.Tags
		in.SEOTitle = rw.SEOTitle
		in.SEODescription = rw.SEODescription
	}
	in.Title, in.Description = title, description
	if in.Tags == nil {
		in.Tags = []string{}
	}

	zero := 0
	if len(l.Options) == 0 {
		v := l.Variants[0]
		price := priceFor(v.PriceMinor)
		in.SKU, in.PriceMinor, in.Stock = l.ASIN, &price, &zero
	} else {
		for _, o := range l.Options {
			in.Options = append(in.Options, gocommerce.OptionInput{Name: o.Name, Values: o.Values})
		}
		for _, v := range l.Variants {
			in.Variants = append(in.Variants, gocommerce.VariantInput{
				SKU: v.ASIN, PriceMinor: priceFor(v.PriceMinor), Options: v.Options, StockOnHand: &zero,
				Metadata: gocommerce.Metadata{"amazon": map[string]any{
					"asin": v.ASIN, "price_minor": v.PriceMinor, "currency": l.Currency,
					"available": v.Available, "fetched": v.Fetched,
				}},
			})
		}
	}

	// The same listing imported twice collides on the slug (the title) and on
	// the SKUs (the ASINs). Each is retried with a suffix rather than refused:
	// a second import is a legitimate thing to do, and a store that wants one
	// product per ASIN can see the duplicate in the metadata and delete it.
	var product *gocommerce.Product
	var err error
	for attempt := 1; ; attempt++ {
		product, err = m.app.Products().CreateProduct(ctx, in)
		if err == nil {
			break
		}
		if attempt >= 6 {
			return nil, warnings, err
		}
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "slug"):
			in.Slug = slugify(title) + "-" + strings.ToLower(l.ASIN)
			if attempt > 1 {
				in.Slug += "-" + strconv.Itoa(attempt)
			}
		case strings.Contains(msg, "sku"):
			suffix := "-" + strconv.Itoa(attempt+1)
			if in.SKU != "" {
				in.SKU = skuBase(in.SKU) + suffix
			}
			for i := range in.Variants {
				in.Variants[i].SKU = skuBase(in.Variants[i].SKU) + suffix
			}
		default:
			return nil, warnings, err
		}
	}

	if len(media.order) > 0 {
		if err := m.app.MediaLibrary().SetProductMedia(ctx, product.ID, media.order); err != nil {
			warnings = append(warnings, "could not attach the pictures: "+err.Error())
		} else {
			for _, v := range product.Variants {
				if ids, ok := media.byVariant[skuBase(v.SKU)]; ok {
					if err := m.app.MediaLibrary().SetVariantMedia(ctx, v.ID, ids); err != nil {
						warnings = append(warnings, "could not set the pictures for variant "+v.SKU+": "+err.Error())
					}
				}
			}
		}
	}
	return product, warnings, nil
}

// plainDescription is the listing's own copy as HTML, for a store with no
// rewrite: the description as a paragraph, the "About this item" bullets as
// a list, and every detail row — fabric, care, dimensions, model number — as
// a second list. On the page, because the product page is where an operator
// looks for them; a metadata field is where they went missing.
func plainDescription(l *Listing) string {
	var b strings.Builder
	if d := strings.TrimSpace(l.Description); d != "" {
		b.WriteString("<p>" + html.EscapeString(d) + "</p>")
	}
	if len(l.Bullets) > 0 {
		b.WriteString("<h3>About this item</h3><ul>")
		for _, bullet := range l.Bullets {
			b.WriteString("<li>" + html.EscapeString(bullet) + "</li>")
		}
		b.WriteString("</ul>")
	}
	if specs := usefulSpecs(l.Specs); len(specs) > 0 {
		b.WriteString("<h3>Details</h3><ul>")
		for _, s := range specs {
			b.WriteString("<li><strong>" + html.EscapeString(s.Key) + ":</strong> " + html.EscapeString(s.Value) + "</li>")
		}
		b.WriteString("</ul>")
	}
	return b.String()
}

// usefulSpecs is the detail rows worth putting in front of a shopper: each
// key once, and without the two rows that only describe the marketplace —
// its own review summary and its sales rank.
func usefulSpecs(specs []Spec) []Spec {
	seen := map[string]bool{}
	out := make([]Spec, 0, len(specs))
	for _, s := range specs {
		key := strings.ToLower(strings.TrimSpace(s.Key))
		if key == "" || seen[key] || strings.Contains(key, "customer reviews") || strings.Contains(key, "best sellers rank") {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

// skuBase is the ASIN a SKU began as, before any "-2" a retry added.
func skuBase(sku string) string {
	base, _, _ := strings.Cut(sku, "-")
	return base
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 80 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
