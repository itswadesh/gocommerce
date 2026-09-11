package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The taxonomy's HTTP door: the two imports an operator with no shell reaches
// for, and the dictionary those imports fill.
//
// What can go wrong here is quiet rather than loud. An import that ran against
// the wrong tree reports zeros and reads like a success; a dictionary entry
// stored under a handle no category will ever match looks perfectly fine in a
// listing; and a handle that could be renamed would detach every category using
// it without saying so.

func taxonomyApp(t *testing.T) *App {
	t.Helper()
	app := newTestApp(t)
	for _, r := range app.Routes() {
		if r.Path == "/api/admin/taxonomy-attributes" {
			return app
		}
	}
	app.mountTaxonomyRoutes()
	return app
}

const testTree = `gid://shopify/TaxonomyCategory/hb     : Health & Beauty
gid://shopify/TaxonomyCategory/hb-1   : Health & Beauty > Bath & Body
gid://shopify/TaxonomyCategory/hb-1-1 : Health & Beauty > Bath & Body > Bar Soap
`

const testFields = `scent = Scent : Citrus | Floral | Unscented

gid://shopify/TaxonomyCategory/hb-1-1 : scent
`

// ------------------------------------------------------------------ imports

// The embedded set over HTTP, twice: the second run is what proves an operator
// whose request timed out can simply click the button again.
func TestImportTaxonomyOverHTTP(t *testing.T) {
	app := taxonomyApp(t)

	rec := do(t, app, http.MethodPost, "/api/admin/import/taxonomy", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body)
	}
	var first TaxonomyImportResult
	decodeData(t, rec, &first)
	if first.Source != "embedded" {
		t.Errorf("source = %q, want embedded for an empty body", first.Source)
	}
	if first.Categories.Created < 14000 {
		t.Errorf("created = %d, want the whole published tree", first.Categories.Created)
	}
	// The tree brings the field definitions with it, because they match on the
	// taxonomy id this import writes.
	if first.Attributes == nil {
		t.Fatal("the embedded import did not bring the field definitions")
	}
	if first.Attributes.Attributes == 0 || first.Attributes.Categories == 0 {
		t.Errorf("attributes report = %+v, want both counts filled", first.Attributes)
	}

	rec = do(t, app, http.MethodPost, "/api/admin/import/taxonomy", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("second import = %d: %s", rec.Code, rec.Body)
	}
	var second TaxonomyImportResult
	decodeData(t, rec, &second)
	if second.Categories.Created != 0 {
		t.Errorf("second run created %d categories, want none", second.Categories.Created)
	}
	if second.Categories.Matched != first.Categories.Created {
		t.Errorf("second run matched %d of %d, want the whole tree",
			second.Categories.Matched, first.Categories.Created)
	}
}

// An uploaded tree imports alone, and `attributes` is absent rather than zeroed:
// the fields match on a taxonomy id only the embedded source writes, so a report
// of zeros would describe a failure nobody attempted.
func TestImportTaxonomyFromAnUploadedFileDoesNotChain(t *testing.T) {
	app := taxonomyApp(t)

	rec := doBody(t, app, http.MethodPost, "/api/admin/import/taxonomy", testTree, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body)
	}
	var got TaxonomyImportResult
	decodeData(t, rec, &got)
	if got.Source != "upload" {
		t.Errorf("source = %q, want upload", got.Source)
	}
	if got.Categories.Created != 3 {
		t.Errorf("created = %d, want the three lines sent", got.Categories.Created)
	}
	if got.Attributes != nil {
		t.Errorf("attributes = %+v, want the key absent entirely", got.Attributes)
	}
	if strings.Contains(rec.Body.String(), "attributes") {
		t.Errorf("the response carries an attributes key: %s", rec.Body)
	}
}

func TestImportCategoryAttributesOverHTTP(t *testing.T) {
	app := taxonomyApp(t)
	ctx := context.Background()

	if rec := doBody(t, app, http.MethodPost, "/api/admin/import/taxonomy", testTree, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("import the tree = %d: %s", rec.Code, rec.Body)
	}
	rec := doBody(t, app, http.MethodPost, "/api/admin/import/category-attributes", testFields, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("import the fields = %d: %s", rec.Code, rec.Body)
	}
	var got TaxonomyAttributeImport
	decodeData(t, rec, &got)
	if got.Attributes != 1 || got.Categories != 1 {
		t.Errorf("report = %+v, want one definition attached to one category", got)
	}

	attr, err := app.Categories().GetAttribute(ctx, "scent")
	if err != nil {
		t.Fatalf("the dictionary row did not land: %v", err)
	}
	if !equalStrings(attr.Choices, []string{"Citrus", "Floral", "Unscented"}) {
		t.Errorf("choices = %v, want the published order", attr.Choices)
	}

	flat, err := app.Categories().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var soap *Category
	for _, c := range flat {
		if c.Title == "Bar Soap" {
			soap = c
		}
	}
	if soap == nil {
		t.Fatal("Bar Soap is missing from the tree")
	}
	chain, err := app.Categories().Ancestors(ctx, soap.ID)
	if err != nil {
		t.Fatalf("Ancestors: %v", err)
	}
	leaf := chain[len(chain)-1].Attributes
	if len(leaf) != 1 || leaf[0].Key != "scent" {
		t.Errorf("the category did not learn its field: %+v", leaf)
	}

	// A file with no definitions in it is a mistake worth naming, not an empty
	// success: it is what an operator sees after uploading the wrong file.
	rec = doBody(t, app, http.MethodPost, "/api/admin/import/category-attributes",
		"gid://shopify/TaxonomyCategory/hb-1-1 : scent\n", withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a file with no definitions = %d, want 400: %s", rec.Code, rec.Body)
	}
	if msg := decodeError(t, rec).Message; !strings.Contains(msg, "no attribute definitions") {
		t.Errorf("message = %q, want it to name what was missing", msg)
	}
}

// --------------------------------------------------------------- dictionary

func TestTaxonomyAttributeCRUD(t *testing.T) {
	app := taxonomyApp(t)

	rec := doBody(t, app, http.MethodPost, "/api/admin/taxonomy-attributes",
		`{"handle":"sleeve-length","label":"Sleeve length","choices":["Short","Long"]}`, withAdmin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	var created TaxonomyAttribute
	decodeData(t, rec, &created)
	if created.Handle != "sleeve-length" || created.Label != "Sleeve length" {
		t.Errorf("created = %+v", created)
	}
	if !equalStrings(created.Choices, []string{"Short", "Long"}) {
		t.Errorf("choices = %v, want the order sent", created.Choices)
	}

	rec = do(t, app, http.MethodGet, "/api/admin/taxonomy-attributes/sleeve-length", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}

	// A label patch leaves the choices exactly as they were.
	rec = doBody(t, app, http.MethodPatch, "/api/admin/taxonomy-attributes/sleeve-length",
		`{"label":"Sleeve"}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH label = %d: %s", rec.Code, rec.Body)
	}
	var patched TaxonomyAttribute
	decodeData(t, rec, &patched)
	if patched.Label != "Sleeve" || !equalStrings(patched.Choices, []string{"Short", "Long"}) {
		t.Errorf("patched = %+v, want only the label changed", patched)
	}

	// A sent choices array replaces the whole list rather than adding to it.
	rec = doBody(t, app, http.MethodPatch, "/api/admin/taxonomy-attributes/sleeve-length",
		`{"choices":["Sleeveless"]}`, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH choices = %d: %s", rec.Code, rec.Body)
	}
	decodeData(t, rec, &patched)
	if !equalStrings(patched.Choices, []string{"Sleeveless"}) {
		t.Errorf("choices = %v, want the list replaced whole", patched.Choices)
	}

	rec = doBody(t, app, http.MethodPost, "/api/admin/taxonomy-attributes",
		`{"handle":"sleeve-length","label":"Again"}`, withAdmin)
	if rec.Code != http.StatusConflict {
		t.Errorf("a repeated handle = %d, want 409", rec.Code)
	}

	if rec := do(t, app, http.MethodDelete, "/api/admin/taxonomy-attributes/sleeve-length", withAdmin); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/taxonomy-attributes/sleeve-length", withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("GET after DELETE = %d, want 404", rec.Code)
	}
}

// The handle is the key every category's metadata names, so renaming it here
// would detach them all. It is not a patch field, and sending it is a 400 rather
// than a silent no-op — the mistake a client would otherwise never notice.
func TestTaxonomyAttributeHandleIsNotPatchable(t *testing.T) {
	app := taxonomyApp(t)

	if rec := doBody(t, app, http.MethodPost, "/api/admin/taxonomy-attributes",
		`{"handle":"colour","label":"Colour"}`, withAdmin); rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	rec := doBody(t, app, http.MethodPatch, "/api/admin/taxonomy-attributes/colour",
		`{"handle":"color"}`, withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH handle = %d, want 400: %s", rec.Code, rec.Body)
	}
	if code := decodeError(t, rec).Code; code != "validation_failed" {
		t.Errorf("error code = %q, want validation_failed", code)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/taxonomy-attributes/colour", withAdmin); rec.Code != http.StatusOK {
		t.Errorf("the original handle = %d after the refused rename, want 200", rec.Code)
	}
}

// The primary key's idea of sameness has to match the UI's, or a store types
// "Color", gets a row, and no category ever matches it — while the case-folded
// list search shows the two as the same thing.
func TestTaxonomyAttributeHandlesAreFolded(t *testing.T) {
	app := taxonomyApp(t)

	rec := doBody(t, app, http.MethodPost, "/api/admin/taxonomy-attributes",
		`{"handle":"Sleeve-Length","label":"Sleeve length"}`, withAdmin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	var created TaxonomyAttribute
	decodeData(t, rec, &created)
	if created.Handle != "sleeve-length" {
		t.Errorf("handle = %q, want it folded", created.Handle)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/taxonomy-attributes/Sleeve-Length", withAdmin); rec.Code != http.StatusOK {
		t.Errorf("GET by the unfolded path = %d, want 200", rec.Code)
	}
	rec = doBody(t, app, http.MethodPost, "/api/admin/taxonomy-attributes",
		`{"handle":"SLEEVE-LENGTH","label":"Again"}`, withAdmin)
	if rec.Code != http.StatusConflict {
		t.Errorf("a differently-cased handle = %d, want 409 rather than a second row", rec.Code)
	}
	// A handle that could be stored and then never addressed is refused.
	for _, body := range []string{
		`{"handle":"sleeve length","label":"Spaces"}`,
		`{"handle":"sleeve/length","label":"Slash"}`,
		`{"handle":"  ","label":"Blank"}`,
		`{"handle":"fine","label":"  "}`,
	} {
		if rec := doBody(t, app, http.MethodPost, "/api/admin/taxonomy-attributes", body, withAdmin); rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", body, rec.Code)
		}
	}
}

// Unlike tags, choices are neither sorted nor case-folded: "XS, S, M, L, XL" is
// the publisher's order and the only useful one.
func TestTaxonomyAttributeChoicesSurviveTheRoundTrip(t *testing.T) {
	app := taxonomyApp(t)
	ctx := context.Background()

	want := []string{"XS", "S", "M", "L", "XL"}
	if _, err := app.Categories().CreateAttribute(ctx, TaxonomyAttributeInput{
		Handle: "size", Label: "Size", Choices: []string{"XS", "S", " M ", "L", "XL", "S", ""},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := app.Categories().GetAttribute(ctx, "size")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !equalStrings(got.Choices, want) {
		t.Errorf("choices = %v, want %v — trimmed and de-duplicated, never sorted", got.Choices, want)
	}
}

// Deleting an entry degrades a field to free text; it does not break it. That is
// what makes the delete safe to offer without a usage count behind it.
func TestDeletingAnAttributeLeavesTheCategoryAsFreeText(t *testing.T) {
	app := taxonomyApp(t)
	ctx := context.Background()

	if _, err := app.Categories().ImportTaxonomy(ctx, strings.NewReader(testTree)); err != nil {
		t.Fatalf("import the tree: %v", err)
	}
	if _, err := app.Categories().ImportCategoryAttributes(ctx, strings.NewReader(testFields)); err != nil {
		t.Fatalf("import the fields: %v", err)
	}
	flat, err := app.Categories().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var soap *Category
	for _, c := range flat {
		if c.Title == "Bar Soap" {
			soap = c
		}
	}
	if soap == nil {
		t.Fatal("Bar Soap is missing from the tree")
	}

	if err := app.Categories().DeleteAttribute(ctx, "scent"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	chain, err := app.Categories().Ancestors(ctx, soap.ID)
	if err != nil {
		t.Fatalf("Ancestors: %v", err)
	}
	leaf := chain[len(chain)-1].Attributes
	if len(leaf) != 1 || leaf[0].Key != "scent" {
		t.Fatalf("the field vanished with its dictionary entry: %+v", leaf)
	}
	if leaf[0].Choices == nil || len(leaf[0].Choices) != 0 {
		t.Errorf("choices = %+v, want an empty list — a free-text field, not a broken one", leaf[0].Choices)
	}
}

func TestTaxonomyAttributeListSearchAndPaging(t *testing.T) {
	app := taxonomyApp(t)
	ctx := context.Background()

	for _, in := range []TaxonomyAttributeInput{
		{Handle: "colour", Label: "Colour"},
		{Handle: "age-group", Label: "Age group"},
		{Handle: "sleeve-length", Label: "Sleeve length"},
	} {
		if _, err := app.Categories().CreateAttribute(ctx, in); err != nil {
			t.Fatalf("create %s: %v", in.Handle, err)
		}
	}

	list := func(query string) []*TaxonomyAttribute {
		t.Helper()
		rec := do(t, app, http.MethodGet, "/api/admin/taxonomy-attributes"+query, withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", query, rec.Code, rec.Body)
		}
		var out []*TaxonomyAttribute
		decodeData(t, rec, &out)
		return out
	}

	// Alphabetical by handle, because a dictionary's order is its spelling.
	all := list("")
	if len(all) != 3 || all[0].Handle != "age-group" || all[2].Handle != "sleeve-length" {
		t.Errorf("listing = %+v, want it ordered by handle", all)
	}
	if got := list("?q=SLEEVE"); len(got) != 1 || got[0].Handle != "sleeve-length" {
		t.Errorf("search by handle = %+v", got)
	}
	if got := list("?q=age+g"); len(got) != 1 || got[0].Handle != "age-group" {
		t.Errorf("search by label = %+v", got)
	}
	if got := list("?limit=2"); len(got) != 2 || got[0].Handle != "age-group" {
		t.Errorf("first page = %+v", got)
	}
	if got := list("?limit=2&page=2"); len(got) != 1 || got[0].Handle != "sleeve-length" {
		t.Errorf("second page = %+v", got)
	}
}

// Every one of the seven routes is an admin route, and the two imports are the
// data door rather than the catalog one.
func TestTaxonomyRoutesAreGated(t *testing.T) {
	app := taxonomyApp(t)
	staff := signInAs(t, app, "staff@example.com", RoleStaff)

	for _, r := range []struct{ method, path string }{
		{http.MethodPost, "/api/admin/import/taxonomy"},
		{http.MethodPost, "/api/admin/import/category-attributes"},
		{http.MethodGet, "/api/admin/taxonomy-attributes"},
		{http.MethodPost, "/api/admin/taxonomy-attributes"},
		{http.MethodGet, "/api/admin/taxonomy-attributes/colour"},
		{http.MethodPatch, "/api/admin/taxonomy-attributes/colour"},
		{http.MethodDelete, "/api/admin/taxonomy-attributes/colour"},
	} {
		if rec := do(t, app, r.method, r.path); rec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s = %d, want 401", r.method, r.path, rec.Code)
		}
	}

	// Staff read the catalog and import nothing.
	for _, path := range []string{"/api/admin/import/taxonomy", "/api/admin/import/category-attributes"} {
		rec := do(t, app, http.MethodPost, path, bearer(staff))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("staff POST %s = %d, want 403: %s", path, rec.Code, rec.Body)
		}
		if msg := decodeError(t, rec).Message; !strings.Contains(msg, string(RightDataImport)) {
			t.Errorf("message = %q, want it to name %s", msg, RightDataImport)
		}
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/taxonomy-attributes", bearer(staff)); rec.Code != http.StatusOK {
		t.Errorf("staff GET the dictionary = %d, want 200", rec.Code)
	}
	if rec := doBody(t, app, http.MethodPost, "/api/admin/taxonomy-attributes",
		`{"handle":"colour","label":"Colour"}`, bearer(staff)); rec.Code != http.StatusForbidden {
		t.Errorf("staff POST the dictionary = %d, want 403", rec.Code)
	}
}
