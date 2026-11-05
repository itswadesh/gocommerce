// Package metalpricing prices jewellery from the day's metal rate (D188).
//
// A jeweller's price is not a number somebody types. It is the weight of the
// piece at today's rate, plus wastage, a making charge and any stones, and it
// moves whenever the rate does. So a variant here has a recipe, a store has
// rates, and entering a rate reprices every variant made from that metal.
//
// The result is an ordinary variant price, written through Catalog.UpdateVariant
// (AGENTS.md rule 3), so a cart, a checkout and an order behave as they do for
// any other product, and an order keeps the price it was placed at.
//
// What it does not do is fetch a rate. Which rate a jeweller sells at is their
// decision, usually their association's board rate, so a person enters it.
package metalpricing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey = "metal-pricing"

	rightRead  gocommerce.Right = "metal-pricing.read"
	rightWrite gocommerce.Right = "metal-pricing.write"

	// These bounds keep every product in price() inside an int64.
	maxWeightMg    = 100_000_000   // 100 kg
	maxRateMinor   = 1_000_000_000 // per gram
	maxBasisPoints = 10_000        // 100%
	maxFlatMinor   = 100_000_000_000_000
	maxImportRows  = 20_000
)

// How a making charge is worked out.
const (
	MakingFlat    = "flat"     // MakingValue is minor units for the piece
	MakingPerGram = "per_gram" // MakingValue is minor units per gram
	MakingPercent = "percent"  // MakingValue is basis points of the metal's value
)

var rateCode = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Config configures the module. It has no settings yet.
type Config struct{}

// Module is the metal pricing module.
type Module struct {
	app     *gocommerce.App
	db      *sql.DB
	catalog *gocommerce.Catalog
}

// New constructs the module.
func New(Config) *Module { return &Module{} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return pluginKey }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{
		{
			ID: "0001_tables",
			SQL: `
				CREATE TABLE metal_pricing_rates (
				    code       text        NOT NULL,
				    name       text        NOT NULL,
				    rate_minor bigint      NOT NULL CHECK (rate_minor > 0),
				    updated_at timestamptz NOT NULL DEFAULT now()
				);
				CREATE TABLE metal_pricing_rate_history (
				    id         bigserial   PRIMARY KEY,
				    code       text        NOT NULL,
				    rate_minor bigint      NOT NULL,
				    changed_by text        NOT NULL DEFAULT '',
				    changed_at timestamptz NOT NULL DEFAULT now()
				);
				CREATE TABLE metal_pricing_recipes (
				    variant_id   bigint      NOT NULL,
				    rate_code    text        NOT NULL,
				    weight_mg    bigint      NOT NULL CHECK (weight_mg > 0),
				    wastage_bp   integer     NOT NULL DEFAULT 0 CHECK (wastage_bp >= 0),
				    making_kind  text        NOT NULL CHECK (making_kind IN ('flat', 'per_gram', 'percent')),
				    making_value bigint      NOT NULL DEFAULT 0 CHECK (making_value >= 0),
				    stones_minor bigint      NOT NULL DEFAULT 0 CHECK (stones_minor >= 0),
				    updated_at   timestamptz NOT NULL DEFAULT now()
				);`,
		},
		gocommerce.ScopeMigration("0002_store_scope", "metal_pricing_rates", "metal_pricing_rate_history", "metal_pricing_recipes"),
		{
			ID: "0003_keys",
			SQL: `
				ALTER TABLE metal_pricing_rates ADD PRIMARY KEY (store_id, code);
				ALTER TABLE metal_pricing_recipes ADD PRIMARY KEY (store_id, variant_id);
				ALTER TABLE metal_pricing_recipes ADD FOREIGN KEY (store_id, rate_code)
				    REFERENCES metal_pricing_rates (store_id, code);
				CREATE INDEX metal_pricing_recipes_rate ON metal_pricing_recipes (store_id, rate_code);`,
		},
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app, m.db, m.catalog = app, app.DB(), app.Products()
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightRead, Label: "See metal rates",
		Scope:   "The day's metal rates and how each design's price is worked out",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightWrite, Label: "Change metal rates",
		Scope:   "Entering a rate reprices every design made from that metal",
		Default: []string{gocommerce.RoleManager},
	})
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Metal rate pricing", Icon: "ri-scales-3-line", Category: "catalog", DefaultEnabled: true,
		Home:        "/dash/metal-pricing",
		Description: "Price jewellery from the day's metal rate, weight, wastage, making charge and stones. Enter a rate and every design made from that metal is repriced.",
	})
	// Changing a rate changes prices, so it needs the catalogue's right as well.
	app.HandleAdminFunc("GET /api/admin/x/metal-pricing/rates", m.handleRates, rightRead)
	app.HandleAdminFunc("PUT /api/admin/x/metal-pricing/rates/{code}", m.handleSetRate, rightWrite, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/metal-pricing/rates/{code}", m.handleDeleteRate, rightWrite)
	app.HandleAdminFunc("POST /api/admin/x/metal-pricing/reprice", m.handleReprice, rightWrite, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("GET /api/admin/x/metal-pricing/recipes", m.handleRecipes, rightRead)
	app.HandleAdminFunc("POST /api/admin/x/metal-pricing/recipes", m.handleSetRecipe, rightWrite, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("POST /api/admin/x/metal-pricing/recipes/import", m.handleImport, rightWrite, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/metal-pricing/recipes/{variant_id}", m.handleDeleteRecipe, rightWrite)
	return nil
}

// Recipe is how one variant's price is worked out.
type Recipe struct {
	VariantID   int64  `json:"variant_id"`
	SKU         string `json:"sku"`
	RateCode    string `json:"rate_code"`
	WeightMg    int64  `json:"weight_mg"`
	WastageBP   int64  `json:"wastage_bp"`
	MakingKind  string `json:"making_kind"`
	MakingValue int64  `json:"making_value"`
	StonesMinor int64  `json:"stones_minor"`
}

// Breakdown is a price in its parts, which is what a jeweller checks by hand.
type Breakdown struct {
	MetalMinor   int64 `json:"metal_minor"`
	WastageMinor int64 `json:"wastage_minor"`
	MakingMinor  int64 `json:"making_minor"`
	StonesMinor  int64 `json:"stones_minor"`
	TotalMinor   int64 `json:"total_minor"`
}

// divRound is a/b, half up. Inputs are never negative.
func divRound(a, b int64) int64 { return (a + b/2) / b }

// price works out a recipe at a rate. The total is rounded to a whole unit of
// the currency, because that is how a jeweller quotes: nobody prices a necklace
// at ₹72,431.37. wholeUnit is that unit in minor units, 100 for rupees.
func price(r Recipe, rateMinor, wholeUnit int64) Breakdown {
	metal := divRound(r.WeightMg*rateMinor, 1000)
	b := Breakdown{MetalMinor: metal, WastageMinor: divRound(metal*r.WastageBP, 10_000), StonesMinor: r.StonesMinor}
	switch r.MakingKind {
	case MakingFlat:
		b.MakingMinor = r.MakingValue
	case MakingPerGram:
		b.MakingMinor = divRound(r.WeightMg*r.MakingValue, 1000)
	case MakingPercent:
		b.MakingMinor = divRound(metal*r.MakingValue, 10_000)
	}
	b.TotalMinor = divRound(b.MetalMinor+b.WastageMinor+b.MakingMinor+b.StonesMinor, wholeUnit) * wholeUnit
	return b
}

func (r *Recipe) check() error {
	switch {
	case !rateCode.MatchString(r.RateCode):
		return gocommerce.Validationf("rate_code names a rate, such as gold-22k")
	case r.WeightMg <= 0 || r.WeightMg > maxWeightMg:
		return gocommerce.Validationf("weight_mg must be between 1 and %d", maxWeightMg)
	case r.WastageBP < 0 || r.WastageBP > maxBasisPoints:
		return gocommerce.Validationf("wastage_bp must be between 0 and %d", maxBasisPoints)
	case r.StonesMinor < 0 || r.StonesMinor > maxFlatMinor:
		return gocommerce.Validationf("stones_minor is out of range")
	case r.MakingValue < 0:
		return gocommerce.Validationf("making_value must not be negative")
	}
	switch r.MakingKind {
	case MakingFlat:
		if r.MakingValue > maxFlatMinor {
			return gocommerce.Validationf("making_value is out of range")
		}
	case MakingPerGram:
		if r.MakingValue > maxRateMinor {
			return gocommerce.Validationf("making_value is out of range")
		}
	case MakingPercent:
		if r.MakingValue > maxBasisPoints {
			return gocommerce.Validationf("a percent making_value is in basis points, at most %d", maxBasisPoints)
		}
	default:
		return gocommerce.Validationf("making_kind is flat, per_gram or percent")
	}
	return nil
}

func (m *Module) wholeUnit(ctx context.Context) int64 {
	unit := int64(1)
	for range gocommerce.CurrencyExponent(m.app.Currency(ctx)) {
		unit *= 10
	}
	return unit
}

// Rate is one metal and purity, at a price per gram.
type Rate struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	RateMinor int64  `json:"rate_minor"`
	UpdatedAt string `json:"updated_at"`
	Designs   int    `json:"designs"`
}

func (m *Module) rates(ctx context.Context) ([]Rate, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT r.code, r.name, r.rate_minor, to_char(r.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       (SELECT count(*) FROM metal_pricing_recipes x WHERE x.rate_code = r.code)
		FROM metal_pricing_rates r ORDER BY r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rate{}
	for rows.Next() {
		var x Rate
		if err := rows.Scan(&x.Code, &x.Name, &x.RateMinor, &x.UpdatedAt, &x.Designs); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (m *Module) rateOf(ctx context.Context, code string) (int64, error) {
	var rate int64
	err := m.db.QueryRowContext(ctx, `SELECT rate_minor FROM metal_pricing_rates WHERE code = $1`, code).Scan(&rate)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, gocommerce.NotFoundf("no rate called %s", code)
	}
	return rate, err
}

// Outcome is what a repricing did. A failure leaves that variant at its old
// price and says so, because a design quietly left behind sells at the wrong rate.
type Outcome struct {
	Repriced  int             `json:"repriced"`
	Unchanged int             `json:"unchanged"`
	Failed    []FailedVariant `json:"failed"`
}

// FailedVariant is a variant a repricing could not change.
type FailedVariant struct {
	VariantID int64  `json:"variant_id"`
	Error     string `json:"error"`
}

// reprice brings every recipe on one rate, or on all rates, up to date. Each
// variant is its own update with no transaction held across the loop.
//
// ponytail: runs inside the request, fine for thousands of variants; move it to
// a background job if a store outgrows the request timeout. Two rates saved in
// the same moment for one metal could leave the older one on some variants;
// POST /reprice repairs that, and one person enters a store's rates.
func (m *Module) reprice(ctx context.Context, code string) (Outcome, error) {
	out := Outcome{Failed: []FailedVariant{}}
	rows, err := m.db.QueryContext(ctx, `
		SELECT r.variant_id, r.rate_code, r.weight_mg, r.wastage_bp, r.making_kind, r.making_value, r.stones_minor, x.rate_minor
		FROM metal_pricing_recipes r JOIN metal_pricing_rates x ON x.code = r.rate_code
		WHERE $1 = '' OR r.rate_code = $1 ORDER BY r.variant_id`, code)
	if err != nil {
		return out, err
	}
	type due struct {
		recipe Recipe
		rate   int64
	}
	var all []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.recipe.VariantID, &d.recipe.RateCode, &d.recipe.WeightMg, &d.recipe.WastageBP,
			&d.recipe.MakingKind, &d.recipe.MakingValue, &d.recipe.StonesMinor, &d.rate); err != nil {
			rows.Close()
			return out, err
		}
		all = append(all, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	unit := m.wholeUnit(ctx)
	for _, d := range all {
		changed, err := m.apply(ctx, d.recipe.VariantID, price(d.recipe, d.rate, unit).TotalMinor)
		switch {
		case errors.Is(err, gocommerce.ErrNotFound):
			// The variant was deleted; its recipe has nothing left to price.
			_, _ = m.db.ExecContext(ctx, `DELETE FROM metal_pricing_recipes WHERE variant_id = $1`, d.recipe.VariantID)
		case err != nil:
			out.Failed = append(out.Failed, FailedVariant{d.recipe.VariantID, err.Error()})
		case changed:
			out.Repriced++
		default:
			out.Unchanged++
		}
	}
	return out, nil
}

// apply sets a variant's price. A struck-through price at or below the new one
// is taken off: as gold rises it would otherwise show a "discount" that costs more.
func (m *Module) apply(ctx context.Context, variantID, totalMinor int64) (bool, error) {
	v, err := m.catalog.GetVariant(ctx, variantID)
	if err != nil {
		return false, err
	}
	if v.Price.AmountMinor == totalMinor {
		return false, nil
	}
	patch := gocommerce.VariantPatch{PriceMinor: &totalMinor}
	if v.CompareAtPrice != nil && v.CompareAtPrice.AmountMinor <= totalMinor {
		patch.CompareAtPriceMinor = gocommerce.NullableAmount{Present: true}
	}
	_, err = m.catalog.UpdateVariant(ctx, variantID, patch)
	return err == nil, err
}

// setRecipe saves a recipe and prices the variant by it at once. An unknown rate
// or variant is refused before anything is written.
func (m *Module) setRecipe(ctx context.Context, r Recipe) (Breakdown, error) {
	if err := r.check(); err != nil {
		return Breakdown{}, err
	}
	rate, err := m.rateOf(ctx, r.RateCode)
	if err != nil {
		return Breakdown{}, err
	}
	if r.VariantID == 0 {
		v, err := m.catalog.GetVariantBySKU(ctx, r.SKU)
		if err != nil {
			return Breakdown{}, err
		}
		r.VariantID = v.ID
	} else if _, err := m.catalog.GetVariant(ctx, r.VariantID); err != nil {
		return Breakdown{}, err
	}
	b := price(r, rate, m.wholeUnit(ctx))
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO metal_pricing_recipes (variant_id, rate_code, weight_mg, wastage_bp, making_kind, making_value, stones_minor)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (store_id, variant_id) DO UPDATE SET rate_code = EXCLUDED.rate_code, weight_mg = EXCLUDED.weight_mg,
		    wastage_bp = EXCLUDED.wastage_bp, making_kind = EXCLUDED.making_kind, making_value = EXCLUDED.making_value,
		    stones_minor = EXCLUDED.stones_minor, updated_at = now()`,
		r.VariantID, r.RateCode, r.WeightMg, r.WastageBP, r.MakingKind, r.MakingValue, r.StonesMinor); err != nil {
		return b, err
	}
	_, err = m.apply(ctx, r.VariantID, b.TotalMinor)
	return b, err
}

func (m *Module) handleRates(w http.ResponseWriter, r *http.Request) {
	list, err := m.rates(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, list)
}

func (m *Module) handleSetRate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := r.PathValue("code")
	var in struct {
		Name      string `json:"name"`
		RateMinor int64  `json:"rate_minor"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case !rateCode.MatchString(code):
		gocommerce.RespondError(w, r, gocommerce.Validationf("a rate's code is lower-case letters, numbers and dashes, such as gold-22k"))
		return
	case in.Name == "":
		gocommerce.RespondError(w, r, gocommerce.Validationf("name a rate, such as Gold 22K"))
		return
	case in.RateMinor <= 0 || in.RateMinor > maxRateMinor:
		gocommerce.RespondError(w, r, gocommerce.Validationf("rate_minor is the price of one gram in minor units, between 1 and %d", maxRateMinor))
		return
	}
	by := ""
	if su := gocommerce.SuperuserFrom(ctx); su != nil {
		by = su.Email
	}
	// One statement, so a rate is never changed without its line in the history.
	if _, err := m.db.ExecContext(ctx, `
		WITH saved AS (
		    INSERT INTO metal_pricing_rates (code, name, rate_minor) VALUES ($1, $2, $3)
		    ON CONFLICT (store_id, code) DO UPDATE SET name = EXCLUDED.name, rate_minor = EXCLUDED.rate_minor, updated_at = now()
		    RETURNING code, rate_minor
		)
		INSERT INTO metal_pricing_rate_history (code, rate_minor, changed_by) SELECT code, rate_minor, $4 FROM saved`,
		code, in.Name, in.RateMinor, by); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out, err := m.reprice(ctx, code)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handleDeleteRate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := r.PathValue("code")
	var used int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM metal_pricing_recipes WHERE rate_code = $1`, code).Scan(&used); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if used > 0 {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("%d designs are priced from %s; move or remove them first", used, code))
		return
	}
	res, err := m.db.ExecContext(ctx, `DELETE FROM metal_pricing_rates WHERE code = $1`, code)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no rate called %s", code))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) handleReprice(w http.ResponseWriter, r *http.Request) {
	out, err := m.reprice(r.Context(), "")
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handleSetRecipe(w http.ResponseWriter, r *http.Request) {
	var in Recipe
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	in.SKU = strings.TrimSpace(in.SKU)
	if in.VariantID == 0 && in.SKU == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("name the design by sku or variant_id"))
		return
	}
	b, err := m.setRecipe(r.Context(), in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, b)
}

// RecipeRow is a recipe with what it comes to now and what the variant is priced at.
type RecipeRow struct {
	Recipe
	Breakdown Breakdown         `json:"breakdown"`
	Price     *gocommerce.Money `json:"price,omitempty"`
}

func (m *Module) handleRecipes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	const limit = 50
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM metal_pricing_recipes`).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT r.variant_id, r.rate_code, r.weight_mg, r.wastage_bp, r.making_kind, r.making_value, r.stones_minor, x.rate_minor
		FROM metal_pricing_recipes r JOIN metal_pricing_rates x ON x.code = r.rate_code
		ORDER BY r.updated_at DESC, r.variant_id LIMIT $1 OFFSET $2`, limit, (page-1)*limit)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	type row struct {
		RecipeRow
		rate int64
	}
	var read []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.VariantID, &x.RateCode, &x.WeightMg, &x.WastageBP, &x.MakingKind, &x.MakingValue, &x.StonesMinor, &x.rate); err != nil {
			rows.Close()
			gocommerce.RespondError(w, r, err)
			return
		}
		read = append(read, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	unit := m.wholeUnit(ctx)
	out := make([]RecipeRow, 0, len(read))
	for _, x := range read {
		x.Breakdown = price(x.Recipe, x.rate, unit)
		if v, err := m.catalog.GetVariant(ctx, x.VariantID); err == nil {
			x.SKU, x.Price = v.SKU, &v.Price
		}
		out = append(out, x.RecipeRow)
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: (page - 1) * limit, Page: page, TotalPages: (total + limit - 1) / limit})
}

func (m *Module) handleDeleteRecipe(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("variant_id"), 10, 64)
	if err != nil || id <= 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no such recipe"))
		return
	}
	res, err := m.db.ExecContext(r.Context(), `DELETE FROM metal_pricing_recipes WHERE variant_id = $1`, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no such recipe"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decimal reads text such as "12.345" as an integer scaled by 10^places, with no
// floats. More decimals than places is refused rather than silently rounded,
// because a weight quietly changed is a price quietly changed.
func decimal(text string, places int) (int64, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, ",", ""))
	if text == "" {
		return 0, nil
	}
	whole, frac, _ := strings.Cut(text, ".")
	if len(frac) > places {
		return 0, fmt.Errorf("%q has more than %d decimals", text, places)
	}
	if whole == "" {
		whole = "0"
	}
	n, err := strconv.ParseInt(whole+frac+strings.Repeat("0", places-len(frac)), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a number", text)
	}
	return n, nil
}

// RejectedRow is a row an import left out, by its number in the spreadsheet.
type RejectedRow struct {
	Row   int    `json:"row"`
	SKU   string `json:"sku"`
	Error string `json:"error"`
}

// recipeFromRow reads one spreadsheet row, in a jeweller's own units.
func recipeFromRow(get func(string) string, moneyPlaces int) (Recipe, error) {
	r := Recipe{SKU: strings.TrimSpace(get("sku")), RateCode: strings.ToLower(strings.TrimSpace(get("rate_code"))),
		MakingKind: strings.ToLower(strings.TrimSpace(get("making_kind")))}
	if r.MakingKind == "" {
		r.MakingKind = MakingFlat
	}
	var err error
	if r.WeightMg, err = decimal(get("weight_g"), 3); err != nil {
		return r, fmt.Errorf("weight_g: %w", err)
	}
	if r.WastageBP, err = decimal(get("wastage_percent"), 2); err != nil {
		return r, fmt.Errorf("wastage_percent: %w", err)
	}
	places := moneyPlaces
	if r.MakingKind == MakingPercent {
		places = 2
	}
	if r.MakingValue, err = decimal(get("making_value"), places); err != nil {
		return r, fmt.Errorf("making_value: %w", err)
	}
	if r.StonesMinor, err = decimal(get("stones_value"), moneyPlaces); err != nil {
		return r, fmt.Errorf("stones_value: %w", err)
	}
	return r, nil
}

// handleImport reads recipes from a spreadsheet, because a manufacturer's range
// is thousands of designs: sku, rate_code, weight_g, wastage_percent, making_kind,
// making_value, stones_value. A bad row is reported and the rest still go in.
func (m *Module) handleImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reader, err := gocommerce.NewCSVReader(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if !reader.Has("sku") || !reader.Has("rate_code") || !reader.Has("weight_g") {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the file's first row must name its columns: sku, rate_code, weight_g, and optionally wastage_percent, making_kind, making_value, stones_value"))
		return
	}
	places := gocommerce.CurrencyExponent(m.app.Currency(ctx))
	saved, rejected, seen := 0, []RejectedRow{}, 0
	for {
		row, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		if !row.Has("sku") && !row.Has("weight_g") {
			continue
		}
		if seen++; seen > maxImportRows {
			gocommerce.RespondError(w, r, gocommerce.Validationf("at most %d rows at a time; send the rest in another file", maxImportRows))
			return
		}
		recipe, err := recipeFromRow(row.Get, places)
		if err == nil {
			if recipe.SKU == "" {
				err = errors.New("sku is empty")
			} else {
				_, err = m.setRecipe(ctx, recipe)
			}
		}
		if err != nil {
			rejected = append(rejected, RejectedRow{row.Line(), recipe.SKU, err.Error()})
			continue
		}
		saved++
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"saved": saved, "rejected": rejected})
}
