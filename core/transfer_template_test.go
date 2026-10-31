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
	// Instructions first, as notes the import skips, then the export's header.
	var header string
	for _, l := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(l, "#") {
			header = l
			break
		}
	}
	if !strings.HasPrefix(rec.Body.String(), "# ") || !strings.HasPrefix(header, strings.Join(shopifyProductHeader[:4], ",")) {
		t.Fatalf("the template does not start with notes and then Shopify's header: %q", header)
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
		t.Fatalf("the example landed as %s with %d variants, want a draft with two sizes", p.Status, len(p.Variants))
	}
	// 999.00 in the file is 99900 in minor units, as Shopify writes it.
	if got := p.Variants[0].Price.AmountMinor; got != 99900 {
		t.Errorf("the example's price is %d minor units, want 99900", got)
	}
}

// An error in a file with notes in it names the line it is on in the file,
// not its row number among the records.
func TestAnImportErrorNamesTheFilesOwnLine(t *testing.T) {
	app := newTestApp(t)
	file := "# a note\n# another\n" + strings.Join(productCSVHeader, ",") + "\n" +
		",Untitled" + strings.Repeat(",", len(productCSVHeader)-2) + "\n"
	res, err := app.Data().ImportProducts(context.Background(), strings.NewReader(file), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || res.Errors[0].Line != 4 {
		t.Fatalf("errors = %+v, want one on line 4", res.Errors)
	}
}
