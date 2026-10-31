package gocommerce

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

// The template is a file the import takes as it is, so an owner who fills it
// in from the example never meets a refusal the template itself caused; and
// the example lands as a draft, never on sale.
func TestTheProductTemplateImportsAsWritten(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	rec := do(t, app, http.MethodGet, "/api/admin/import/products/template", withAdmin)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("GET the template = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if first, _, _ := strings.Cut(rec.Body.String(), "\n"); !strings.HasPrefix(first, strings.Join(productCSVHeader[:4], ",")) {
		t.Fatalf("the template's header is not the export's: %q", first)
	}

	res, err := app.Data().ImportProducts(ctx, bytes.NewReader(rec.Body.Bytes()), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 || res.Created == 0 {
		t.Fatalf("the template imported with %d created and errors %+v", res.Created, res.Errors)
	}
	p, err := app.Products().GetProductBySlug(ctx, "cotton-kurta")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != ProductDraft || len(p.Variants) != 2 {
		t.Errorf("the example landed as %s with %d variants, want a draft with two sizes", p.Status, len(p.Variants))
	}
}
