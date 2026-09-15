package gocommerce

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

// The category file is the tree written down: one row per category, the
// trail from the root in one cell, parents above their children.

// pathsOf is the file's path column in the order the file wrote it.
func pathsOf(t *testing.T, header []string, rows [][]string) []string {
	t.Helper()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, cell(t, header, r, "path"))
	}
	return out
}

func indexOfPath(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

func TestCategoryFileWritesParentsBeforeChildren(t *testing.T) {
	app := categoriesApp(t)
	ctx := context.Background()
	_, _, shirts, _ := tree(t, app)

	// A product filed under Shirts, so the count column has something to say.
	price, stock := int64(1500), 3
	if _, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "A shirt", Status: ProductActive, SKU: "CAT-CSV-1",
		PriceMinor: &price, Stock: &stock, CategoryID: &shirts.ID,
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}

	var buf bytes.Buffer
	if err := app.Data().ExportCategories(ctx, &buf, ExportOptions{}); err != nil {
		t.Fatalf("export: %v", err)
	}
	header, rows := readCSV(t, buf.String())
	if strings.Join(header, ",") != strings.Join(categoryCSVHeader, ",") {
		t.Errorf("header = %v, want %v", header, categoryCSVHeader)
	}

	paths := pathsOf(t, header, rows)
	for _, pair := range [][2]string{
		{"Apparel", "Apparel / Clothing"},
		{"Apparel / Clothing", "Apparel / Clothing / Shirts"},
		{"Apparel", "Apparel / Footwear"},
	} {
		parent, child := indexOfPath(paths, pair[0]), indexOfPath(paths, pair[1])
		if parent < 0 || child < 0 {
			t.Fatalf("paths = %v, want %q and %q", paths, pair[0], pair[1])
		}
		if parent > child {
			// The importer resolves a parent from the rows above it, so a
			// child written first is a file that cannot be read back.
			t.Errorf("%q was written after %q", pair[0], pair[1])
		}
	}

	var shirtRow []string
	for _, r := range rows {
		if cell(t, header, r, "path") == "Apparel / Clothing / Shirts" {
			shirtRow = r
		}
	}
	if shirtRow == nil {
		t.Fatalf("no row for Shirts:\n%s", buf.String())
	}
	if got := cell(t, header, shirtRow, "slug"); got != shirts.Slug {
		t.Errorf("slug = %q, want %q", got, shirts.Slug)
	}
	if got := cell(t, header, shirtRow, "products"); got != "1" {
		t.Errorf("products = %q, want the one filed there", got)
	}
}

func TestCategoryFileImportsBackAndAddsWhatIsMissing(t *testing.T) {
	app := categoriesApp(t)
	ctx := context.Background()
	apparel, _, shirts, _ := tree(t, app)

	// Three rows: one that already exists and moves to a new position, one
	// whose whole trail is missing, and one that names its own slug.
	file := "path,slug,position\n" +
		"Apparel / Clothing / Shirts,,7\n" +
		"Homeware / Kitchen / Knives,,0\n" +
		"Apparel / Hats,sun-hats,1\n"
	result, err := app.Data().ImportCategories(ctx, strings.NewReader(file), ImportOptions{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("errors = %+v", result.Errors)
	}
	// Homeware and Kitchen are created on the way to Knives.
	if result.Created != 4 || result.Updated != 1 {
		t.Errorf("result = %+v, want four created and one updated", result)
	}

	got, err := app.Categories().Get(ctx, shirts.ID)
	if err != nil {
		t.Fatalf("get shirts: %v", err)
	}
	if got.Position != 7 {
		t.Errorf("shirts position = %d, want the file's 7", got.Position)
	}
	if got.Slug != shirts.Slug {
		t.Errorf("slug = %q, want it left alone by a blank cell", got.Slug)
	}

	list, err := app.Categories().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]*Category{}
	for _, c := range list {
		byPath[c.FullName] = c
	}
	knives, ok := byPath["Homeware / Kitchen / Knives"]
	if !ok {
		t.Fatalf("categories = %v, want the whole missing trail built", byPath)
	}
	if knives.Depth != 2 {
		t.Errorf("knives depth = %d, want 2", knives.Depth)
	}
	hats, ok := byPath["Apparel / Hats"]
	if !ok || hats.Slug != "sun-hats" {
		t.Fatalf("hats = %+v, want the slug the file chose", hats)
	}
	if hats.ParentID == nil || *hats.ParentID != apparel.ID {
		t.Errorf("hats parent = %v, want Apparel", hats.ParentID)
	}

	// A second run of the same file changes nothing: the path is the identity.
	again, err := app.Data().ImportCategories(ctx, strings.NewReader(file), ImportOptions{})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if again.Created != 0 {
		t.Errorf("second run created %d, want none", again.Created)
	}
}

// The rule every file here follows, on the one importer that writes a whole
// tree: a column the file does not have says nothing about that field. A
// file of paths is a file of paths, not an instruction to blank everything
// else — and a category's metadata is where its taxonomy id and its attribute
// declarations live, so blanking it is not a cosmetic loss.
func TestCategoryFileLeavesColumnsItDoesNotNameAlone(t *testing.T) {
	app := categoriesApp(t)
	ctx := context.Background()

	apparel := newCategory(t, app, CategoryInput{Title: "Apparel"})
	position := 5
	shirts := newCategory(t, app, CategoryInput{
		Title: "Shirts", ParentID: &apparel.ID, Position: &position,
		Metadata: Metadata{"taxonomy_gid": "gid://shopify/TaxonomyCategory/aa-1"},
	})

	// Only paths. Nothing else is mentioned, so nothing else may move.
	result, err := app.Data().ImportCategories(ctx,
		strings.NewReader("path\nApparel / Shirts\n"), ImportOptions{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Updated != 1 || result.Created != 0 {
		t.Errorf("result = %+v, want the one row recognised", result)
	}

	got, err := app.Categories().Get(ctx, shirts.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Position != position {
		t.Errorf("position = %d, want the %d it had; a file with no position column moved it",
			got.Position, position)
	}
	if got.Metadata["taxonomy_gid"] != "gid://shopify/TaxonomyCategory/aa-1" {
		t.Errorf("metadata = %v; a file with no metadata column emptied it", got.Metadata)
	}
	if got.Slug != shirts.Slug {
		t.Errorf("slug = %q, want %q", got.Slug, shirts.Slug)
	}

	// A blank cell is the same as an absent column: the row says nothing
	// there, which is what lets one column be pasted back over an export.
	if _, err := app.Data().ImportCategories(ctx,
		strings.NewReader("path,position,metadata\nApparel / Shirts,,\n"), ImportOptions{}); err != nil {
		t.Fatalf("blank cells: %v", err)
	}
	got, err = app.Categories().Get(ctx, shirts.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Position != position || got.Metadata["taxonomy_gid"] == nil {
		t.Errorf("after blank cells: position = %d, metadata = %v", got.Position, got.Metadata)
	}
}

func TestCategoryFileRefusesRowsItCannotApply(t *testing.T) {
	app := categoriesApp(t)
	ctx := context.Background()
	apparel, _, _, _ := tree(t, app)

	file := "path,slug,position\n" +
		",,1\n" + // no path
		"Apparel / Socks,,x\n" + // position is not a number
		"Apparel / Gloves," + apparel.Slug + ",0\n" + // a slug another category holds
		"Apparel / Scarves,,2\n"
	result, err := app.Data().ImportCategories(ctx, strings.NewReader(file), ImportOptions{})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Errors) != 3 || result.Created != 1 {
		t.Errorf("result = %+v, want three refused rows and Scarves created", result)
	}
	for _, e := range result.Errors {
		if e.Line == 0 || e.Message == "" {
			t.Errorf("row error = %+v, want a line and a sentence", e)
		}
	}

	// A file with no path column is one mistake in the header, not a
	// thousand row errors.
	if _, err := app.Data().ImportCategories(ctx, strings.NewReader("slug,position\nx,1\n"), ImportOptions{}); err == nil {
		t.Error("a file with no path column was accepted")
	}
}

func TestCategoryFileDryRunWritesNothing(t *testing.T) {
	app := categoriesApp(t)
	ctx := context.Background()
	tree(t, app)

	before, err := app.Categories().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dry, err := app.Data().ImportCategories(ctx,
		strings.NewReader("path,position\nOutdoors / Tents,0\n"), ImportOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !dry.DryRun || dry.Created != 2 {
		t.Errorf("dry run = %+v, want it to report the two it would build", dry)
	}
	after, err := app.Categories().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("a rehearsal built %d categories", len(after)-len(before))
	}
}

func TestCategoryTransferRoutes(t *testing.T) {
	app := categoriesApp(t)
	tree(t, app)

	out := do(t, app, http.MethodGet, "/api/admin/export/admin-categories", withAdmin)
	if out.Code != http.StatusOK || !strings.HasPrefix(out.Body.String(), "path,slug,") {
		t.Fatalf("export = %d: %s", out.Code, out.Body)
	}
	if ct := out.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content type = %q", ct)
	}

	// The tree has one dialect. Asking for Shopify's is refused rather than
	// quietly answered with the store's own.
	if bad := do(t, app, http.MethodGet, "/api/admin/export/admin-categories?format=shopify", withAdmin); bad.Code != http.StatusBadRequest {
		t.Errorf("shopify export = %d, want 400", bad.Code)
	}

	rec := do(t, app, http.MethodPost, "/api/admin/import/categories", withAdmin,
		textBody("path,position\nApparel / Belts,3\n"))
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body)
	}
	list, err := app.Categories().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range list {
		if c.FullName == "Apparel / Belts" {
			found = true
		}
	}
	if !found {
		t.Error("the route did not build the category")
	}

	if denied := do(t, app, http.MethodPost, "/api/admin/import/categories", textBody("path\n")); denied.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", denied.Code)
	}
	if denied := do(t, app, http.MethodGet, "/api/admin/export/admin-categories"); denied.Code != http.StatusUnauthorized {
		t.Errorf("export with no token = %d, want 401", denied.Code)
	}
}
