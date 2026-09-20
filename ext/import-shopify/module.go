// Package shopify imports a whole Shopify catalogue in one go.
//
// Registering it adds an "Import from Shopify" drawer to the panel's products
// page and three admin routes under /api/admin/x/import-shopify. An operator
// puts the shop's domain and an Admin API access token into the plugin's
// settings once, presses the button, and watches a progress bar walk the
// catalogue.
//
//	app, err := gocommerce.New(cfg, shopify.New())
//
// # Why this and not the CSV importer
//
// The engine already reads Shopify's CSV dialect (see D56 and
// core/transfer_shopify.go), and that remains the right tool for a file
// somebody was sent. This is for the store that still exists: no export, no
// download, no column mangling — the products come over the API with their
// variants, options, pictures and inventory attached, which a CSV flattens and
// partly loses.
//
// # Credentials
//
// A custom app in the Shopify admin, with read_products. That gives an Admin
// API access token, which goes in this plugin's settings as a secret and is
// never handed back out. No OAuth dance: a store importing its own catalogue
// once is not a public app, and asking an operator to register one would be
// ceremony in the way of the thing they came to do.
//
// # Running it twice
//
// Safe, and useful. Every imported product records where it came from in
// metadata.shopify.id, and a second run updates those rows rather than making a
// second copy beside them — so an import can be re-run after fixing something
// in Shopify, and a half-finished import can simply be started again.
package shopify

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

const pluginKey = "import-shopify"

// Module is the importer.
type Module struct {
	app *gocommerce.App

	// running guards against two imports of the same catalogue at once. Not a
	// database lock: the thing being protected is this process's worker, and a
	// second one would double every write and race on the "already imported"
	// lookup.
	running sync.Mutex
}

// New builds the module.
func New() *Module { return &Module{} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "import-shopify" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_jobs",
		SQL: `
			CREATE TABLE import_shopify_jobs (
			    id          bigserial   PRIMARY KEY,
			    shop        text        NOT NULL,
			    status      text        NOT NULL,
			    step        text        NOT NULL DEFAULT '',
			    message     text        NOT NULL DEFAULT '',
			    -- Counts for the progress bar. total is what the shop said it
			    -- had before the walk started, so the bar can say "40 of 900"
			    -- rather than counting upwards into the dark.
			    total       integer     NOT NULL DEFAULT 0,
			    done        integer     NOT NULL DEFAULT 0,
			    created     integer     NOT NULL DEFAULT 0,
			    updated     integer     NOT NULL DEFAULT 0,
			    failed      integer     NOT NULL DEFAULT 0,
			    warnings    jsonb       NOT NULL DEFAULT '[]',
			    started_at  timestamptz NOT NULL DEFAULT now(),
			    finished_at timestamptz
			);
			CREATE INDEX import_shopify_jobs_recent ON import_shopify_jobs (id DESC);
		`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Import from Shopify", Category: "catalog",
		Description: "Bring a Shopify catalogue over the API — products, variants, options, " +
			"pictures and stock. Re-running updates what it imported before rather than " +
			"duplicating it.",
		Docs: "https://shopify.dev/docs/api/admin-rest/latest/resources/product",
		Fields: []gocommerce.PluginField{
			{
				Key: "shop", Label: "Shop domain", Kind: "text", Required: true,
				Help: "acme.myshopify.com — the admin domain, not the storefront's own",
			},
			{
				Key: "access_token", Label: "Admin API access token", Kind: "secret", Required: true,
				Help: "From a custom app in Settings → Apps → Develop apps, with read_products",
			},
		},
	})

	app.HandleAdminFunc("POST /api/admin/x/import-shopify/jobs", m.handleStart, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("GET /api/admin/x/import-shopify/jobs", m.handleList, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("GET /api/admin/x/import-shopify/jobs/{id}", m.handleGet, gocommerce.RightCatalogWrite)
	// Checking the credentials without importing anything, so an operator can
	// find out the token is wrong before watching a bar fail at 0%.
	app.HandleAdminFunc("POST /api/admin/x/import-shopify/check", m.handleCheck, gocommerce.RightCatalogWrite)
	return nil
}

// ------------------------------------------------------------------- jobs

// Job is one import, as the panel reads it.
type Job struct {
	ID       int64    `json:"id"`
	Shop     string   `json:"shop"`
	Status   string   `json:"status"`
	Step     string   `json:"step,omitempty"`
	Message  string   `json:"message,omitempty"`
	Total    int      `json:"total"`
	Done     int      `json:"done"`
	Created  int      `json:"created"`
	Updated  int      `json:"updated"`
	Failed   int      `json:"failed"`
	Warnings []string `json:"warnings"`
	// Percent is computed rather than stored: a stored percentage is a second
	// copy of the same fact that can disagree with the counts beside it.
	Percent    int        `json:"percent"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Job statuses.
const (
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

func (j *Job) fill() {
	if j.Total > 0 {
		j.Percent = j.Done * 100 / j.Total
		if j.Percent > 100 {
			j.Percent = 100
		}
	} else if j.Status == StatusDone {
		// A shop with no products is finished, not stuck at zero.
		j.Percent = 100
	}
}

func (m *Module) settings(ctx context.Context) (*Client, error) {
	values, err := m.app.Plugins().Settings(ctx, pluginKey)
	if err != nil {
		return nil, err
	}
	str := func(key string) string {
		s, _ := values[key].(string)
		return strings.TrimSpace(s)
	}
	return NewClient(str("shop"), str("access_token"))
}

func (m *Module) handleCheck(w http.ResponseWriter, r *http.Request) {
	client, err := m.settings(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("%v", err))
		return
	}
	shop, err := client.Verify(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("%v", err))
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{
		"shop":     shop.Name,
		"domain":   client.Shop,
		"currency": shop.Currency,
		"products": shop.Products,
	})
}

func (m *Module) handleStart(w http.ResponseWriter, r *http.Request) {
	client, err := m.settings(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("%v", err))
		return
	}
	// Verified before the job row exists, so a wrong token is a 400 on the
	// button rather than a failed job somebody has to go and read.
	shop, err := client.Verify(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("%v", err))
		return
	}

	var id int64
	if err := m.app.DB().QueryRowContext(r.Context(), `
		INSERT INTO import_shopify_jobs (shop, status, step, total)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		client.Shop, StatusRunning, "reading the catalogue", shop.Products,
	).Scan(&id); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	// Detached from the request: a catalogue of ten thousand products outlives
	// any browser's patience, and the panel follows the job by polling it.
	go m.run(context.WithoutCancel(r.Context()), id, client)

	job, err := m.job(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, job)
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	rows, err := m.app.DB().QueryContext(r.Context(), `
		SELECT id, shop, status, step, message, total, done, created, updated, failed,
		       warnings, started_at, finished_at
		FROM import_shopify_jobs ORDER BY id DESC LIMIT 20`)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()

	out := []*Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		out = append(out, job)
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	var id int64
	if _, err := fmt.Sscan(r.PathValue("id"), &id); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("id must be a number"))
		return
	}
	job, err := m.job(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, job)
}

func (m *Module) job(ctx context.Context, id int64) (*Job, error) {
	row := m.app.DB().QueryRowContext(ctx, `
		SELECT id, shop, status, step, message, total, done, created, updated, failed,
		       warnings, started_at, finished_at
		FROM import_shopify_jobs WHERE id = $1`, id)
	return scanJob(row)
}

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var warnings []byte
	var message, step sql.NullString
	if err := row.Scan(&j.ID, &j.Shop, &j.Status, &step, &message, &j.Total, &j.Done,
		&j.Created, &j.Updated, &j.Failed, &warnings, &j.StartedAt, &j.FinishedAt); err != nil {
		return nil, err
	}
	j.Step, j.Message = step.String, message.String
	j.Warnings = []string{}
	_ = json.Unmarshal(warnings, &j.Warnings)
	j.fill()
	return &j, nil
}

// ------------------------------------------------------------------- the walk

// run imports the catalogue.
//
// One product at a time, committing as it goes. A single transaction over ten
// thousand products would hold locks for minutes and lose everything to one bad
// row; this way an import that dies halfway has half the catalogue in, and
// re-running finishes the job rather than starting again.
func (m *Module) run(ctx context.Context, id int64, client *Client) {
	m.running.Lock()
	defer m.running.Unlock()

	var created, updated, failed, done int
	warnings := []string{}

	addWarning := func(format string, args ...any) {
		// Capped: a catalogue where every product has a problem would otherwise
		// put a megabyte of near-identical lines in a jsonb column nobody reads
		// past the first screen of.
		if len(warnings) < 200 {
			warnings = append(warnings, fmt.Sprintf(format, args...))
		}
	}

	currency := m.app.Config().Currency
	exponent := currencyExponent(currency)

	err := client.EachProduct(ctx, func(p Product) error {
		done++
		mapped, err := MapProduct(p, currency, exponent)
		if err != nil {
			failed++
			addWarning("%s: %v", p.Handle, err)
			m.progress(ctx, id, "importing "+p.Title, done, created, updated, failed, warnings)
			return nil
		}
		warnings = append(warnings, mapped.Warnings...)

		switch wrote, err := m.upsert(ctx, p, mapped); {
		case err != nil:
			failed++
			addWarning("%s: %v", p.Handle, err)
		case wrote == wroteCreated:
			created++
		default:
			updated++
		}
		m.progress(ctx, id, "importing "+p.Title, done, created, updated, failed, warnings)
		return nil
	})

	status, message := StatusDone, ""
	if err != nil {
		status = StatusFailed
		message = err.Error()
	}
	_, _ = m.app.DB().ExecContext(ctx, `
		UPDATE import_shopify_jobs
		SET status = $2, step = '', message = $3, done = $4, created = $5, updated = $6,
		    failed = $7, warnings = $8, finished_at = now()
		WHERE id = $1`,
		id, status, message, done, created, updated, failed, jsonOf(warnings))
}

func (m *Module) progress(ctx context.Context, id int64, step string, done, created, updated, failed int, warnings []string) {
	_, _ = m.app.DB().ExecContext(ctx, `
		UPDATE import_shopify_jobs
		SET step = $2, done = $3, created = $4, updated = $5, failed = $6, warnings = $7
		WHERE id = $1`, id, step, done, created, updated, failed, jsonOf(warnings))
}

type wrote int

const (
	wroteCreated wrote = iota
	wroteUpdated
)

// upsert creates the product, or updates the one a previous import made.
//
// Matched on the Shopify id recorded in metadata rather than on the slug.
//
// A merchant who renames a product in Shopify changes its handle, and a handle
// match then fails to recognise the row it imported last time. Tested by
// changing the rule and watching it happen: the rename never lands, the old
// title stays, and the second import does not quietly create a duplicate either
// — it cannot, because SKUs are unique across the whole catalogue, so the
// create is refused and the product is counted as failed. The visible symptom
// is therefore an import that reports failures and changes nothing, which is a
// confusing way to find out about a rename.
//
// The slug is a fallback for rows that predate this module — a store that came
// in through the CSV importer and is now being kept in step over the API.
func (m *Module) upsert(ctx context.Context, p Product, mapped *Mapped) (wrote, error) {
	var existing *int64
	err := m.app.DB().QueryRowContext(ctx, `
		SELECT id FROM products
		WHERE metadata -> 'shopify' ->> 'id' = $1
		   OR (slug = $2 AND metadata -> 'shopify' ->> 'id' IS NULL)
		ORDER BY (metadata -> 'shopify' ->> 'id' = $1) DESC
		LIMIT 1`, fmt.Sprint(p.ID), mapped.Input.Slug).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return wroteCreated, err
	}

	if existing == nil {
		product, err := m.app.Products().CreateProduct(ctx, mapped.Input)
		if err != nil {
			return wroteCreated, err
		}
		m.attachImages(ctx, product.ID, mapped)
		return wroteCreated, nil
	}

	// An update touches what a merchant edits in Shopify and leaves what they
	// edit here. Options and variants are not reconciled: a second import
	// re-pricing a variant somebody adjusted in this panel would undo their
	// work silently, and that is a reconcile with its own screen rather than a
	// side effect of pressing Import.
	title := mapped.Input.Title
	description := mapped.Input.Description
	status := mapped.Input.Status
	vendor := mapped.Input.Vendor
	productType := mapped.Input.ProductType
	if _, err := m.app.Products().UpdateProduct(ctx, *existing, gocommerce.ProductPatch{
		Title:       &title,
		Description: &description,
		Status:      &status,
		Vendor:      &vendor,
		ProductType: &productType,
		Tags:        &mapped.Input.Tags,
		Metadata:    &mapped.Input.Metadata,
	}); err != nil {
		return wroteUpdated, err
	}
	return wroteUpdated, nil
}

// attachImages links Shopify's pictures by URL.
//
// Linked, never downloaded — the same rule D56 set for the CSV importer, and
// for the same reason: fetching somebody else's images means a client with a
// size cap, a timeout and a retry policy, which is a module's business and not
// core's. A picture the library already holds by that URL is reused.
func (m *Module) attachImages(ctx context.Context, productID int64, mapped *Mapped) {
	if len(mapped.ImageURLs) == 0 {
		return
	}
	ids := make([]int64, 0, len(mapped.ImageURLs))
	for i, src := range mapped.ImageURLs {
		alt := ""
		if i < len(mapped.ImageAlts) {
			alt = mapped.ImageAlts[i]
		}
		item, err := m.app.MediaLibrary().AddURL(ctx, src, "image", alt)
		if err != nil {
			continue
		}
		ids = append(ids, item.ID)
	}
	if len(ids) > 0 {
		_ = m.app.MediaLibrary().SetProductMedia(ctx, productID, ids)
	}
}

func jsonOf(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		return []byte("[]")
	}
	return out
}

// currencyExponent mirrors core's: two decimals for most currencies, none for
// the yen, three for the dinar. Duplicated rather than exported from core
// because it is four lines and an ext package importing a helper for it would
// be a wider surface than the thing is worth.
func currencyExponent(code string) int {
	switch strings.ToUpper(code) {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF",
		"UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	}
	return 2
}
