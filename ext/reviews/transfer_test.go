package reviews

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// readCSV splits a file into its header and its rows.
func readCSV(t *testing.T, raw string) ([]string, [][]string) {
	t.Helper()
	records, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("parse the CSV: %v\n%s", err, raw)
	}
	if len(records) == 0 {
		t.Fatalf("the file is empty")
	}
	return records[0], records[1:]
}

func cell(t *testing.T, header, row []string, name string) string {
	t.Helper()
	for i, h := range header {
		if h == name && i < len(row) {
			return row[i]
		}
	}
	t.Fatalf("no column %q in %v", name, header)
	return ""
}

// The file is how a store moving in brings its reviews, and how a store
// already here moderates a thousand of them in one paste.
func TestReviewFileCarriesReviewsBothWays(t *testing.T) {
	mod := New(Config{})
	app := gctest.New(t, mod)
	ctx := context.Background()
	mug := gctest.CreateProduct(t, app, "RV-CSV-MUG", 900, 10)
	pot := gctest.CreateProduct(t, app, "RV-CSV-POT", 1900, 4)
	// The test shopper buys the mug, so a review from that address is
	// verified without the file having to say so.
	gctest.Buy(t, app, gocommerce.CodeCOD, mug.Variants[0].ID, 1)

	// A file from somewhere else: no ids, the product named by slug.
	incoming := "product_slug,name,email,rating,title,body,status\n" +
		mug.Slug + ",GC,gctest@example.com,5,Lovely,Keeps the tea hot.,approved\n" +
		pot.Slug + ",Ann,ann@example.com,3,,Fine.,\n" +
		"no-such-product,Ghost,g@example.com,4,,?,\n" +
		mug.Slug + ",,nobody@example.com,4,,No name here,\n" +
		mug.Slug + ",Six,six@example.com,6,,Too many stars,\n"

	result, err := mod.Import(ctx, strings.NewReader(incoming), false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 2 || len(result.Errors) != 3 {
		t.Fatalf("result = %+v, want two written and three refused", result)
	}

	// And the file back out.
	var buf bytes.Buffer
	if err := mod.Export(ctx, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	header, rows := readCSV(t, buf.String())
	if strings.Join(header, ",") != strings.Join(reviewCSVHeader, ",") {
		t.Errorf("header = %v", header)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the two that were written:\n%s", len(rows), buf.String())
	}
	byName := map[string][]string{}
	for _, r := range rows {
		byName[cell(t, header, r, "name")] = r
	}
	gc, ok := byName["GC"]
	if !ok {
		t.Fatalf("rows = %v, want the buyer's review", byName)
	}
	if got := cell(t, header, gc, "product_slug"); got != mug.Slug {
		t.Errorf("product_slug = %q, want %q", got, mug.Slug)
	}
	if got := cell(t, header, gc, "verified"); got != "true" {
		t.Errorf("verified = %q; the buyer's email should have been recognised", got)
	}
	if got := cell(t, header, gc, "status"); got != StatusApproved {
		t.Errorf("status = %q, want the file's own", got)
	}
	ann, ok := byName["Ann"]
	if !ok {
		t.Fatalf("rows = %v, want the stranger's review", byName)
	}
	if got := cell(t, header, ann, "verified"); got != "false" {
		t.Errorf("a stranger's review is verified = %q", got)
	}
	if got := cell(t, header, ann, "status"); got != StatusPending {
		t.Errorf("a blank status = %q, want pending", got)
	}

	// Moderating in bulk: the id column and one other, nothing else, and
	// every field the file does not mention is left alone.
	back := "id,status\n" + cell(t, header, ann, "id") + ",approved\n"
	result, err = mod.Import(ctx, strings.NewReader(back), false)
	if err != nil {
		t.Fatalf("moderation import: %v", err)
	}
	if result.Updated != 1 || result.Created != 0 || len(result.Errors) != 0 {
		t.Errorf("result = %+v, want the one row updated", result)
	}
	buf.Reset()
	if err := mod.Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	header, rows = readCSV(t, buf.String())
	for _, r := range rows {
		if cell(t, header, r, "name") != "Ann" {
			continue
		}
		if got := cell(t, header, r, "status"); got != StatusApproved {
			t.Errorf("status = %q after the paste", got)
		}
		if got := cell(t, header, r, "body"); got != "Fine." {
			t.Errorf("body = %q; a column the file never named was overwritten", got)
		}
	}

	// An id the store does not have is a row error, not a new review.
	result, err = mod.Import(ctx, strings.NewReader("id,status\n999999,approved\n"), false)
	if err != nil {
		t.Fatalf("unknown id: %v", err)
	}
	if len(result.Errors) != 1 || result.Updated != 0 {
		t.Errorf("result = %+v, want one refused row", result)
	}
}

func TestReviewFileDryRunWritesNothing(t *testing.T) {
	mod := New(Config{})
	app := gctest.New(t, mod)
	ctx := context.Background()
	mug := gctest.CreateProduct(t, app, "RV-DRY", 900, 10)

	dry, err := mod.Import(ctx,
		strings.NewReader("product_slug,name,email,rating,body\n"+mug.Slug+",Sam,sam@example.com,4,Good.\n"), true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !dry.DryRun || dry.Created != 1 {
		t.Errorf("dry run = %+v, want it to report the one it would write", dry)
	}
	var buf bytes.Buffer
	if err := mod.Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	if _, rows := readCSV(t, buf.String()); len(rows) != 0 {
		t.Errorf("a rehearsal wrote %d reviews", len(rows))
	}
}

func TestReviewTransferRoutes(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	mug := gctest.CreateProduct(t, app, "RV-ROUTE", 700, 3)

	out := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/reviews/export", nil)
	if out.Code != http.StatusOK || !strings.HasPrefix(out.Body.String(), "id,product_slug,") {
		t.Fatalf("export = %d: %s", out.Code, out.Body)
	}
	if ct := out.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content type = %q", ct)
	}

	rec := gctest.AdminUpload(t, app, http.MethodPost, "/api/admin/x/reviews/import", "text/csv",
		"product_slug,name,email,rating,body\n"+mug.Slug+",Pat,pat@example.com,5,Excellent.\n")
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body)
	}
	var result gocommerce.ImportResult
	gctest.DecodeData(t, rec, &result)
	if result.Created != 1 {
		t.Errorf("result = %+v", result)
	}

	// A file that says nothing about what it is reviewing is refused whole,
	// rather than becoming one error per line.
	bad := gctest.AdminUpload(t, app, http.MethodPost, "/api/admin/x/reviews/import", "text/csv", "name,body\nPat,Hello\n")
	if bad.Code != http.StatusBadRequest {
		t.Errorf("a file with no product column = %d, want 400", bad.Code)
	}

	if denied := gctest.Request(t, app, http.MethodGet, "/api/admin/x/reviews/export", nil); denied.Code != http.StatusUnauthorized {
		t.Errorf("export with no token = %d, want 401", denied.Code)
	}
}
