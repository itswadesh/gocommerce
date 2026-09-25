package navigation

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

// rowsFor is one menu's rows, in the order the file wrote them.
func rowsFor(t *testing.T, head []string, rows [][]string, handle string) [][]string {
	t.Helper()
	var out [][]string
	for _, r := range rows {
		if cell(t, head, r, "menu") == handle {
			out = append(out, r)
		}
	}
	return out
}

// A menu is a tree, and the file is the tree flattened: the trail of titles
// in one cell, the order of the rows the order of the menu.
func TestMenuFileWritesTheTreeAndReadsItBack(t *testing.T) {
	mod := New(Config{})
	app := gctest.New(t, mod)
	ctx := context.Background()

	// The store ships with a header and a footer; this is the header laid
	// out the way an operator would.
	var menu Menu
	list := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/navigation/menus", nil)
	var menus []Menu
	gctest.DecodeData(t, list, &menus)
	for _, m := range menus {
		if m.Handle == "header" {
			menu = m
		}
	}
	if menu.ID == 0 {
		t.Fatalf("menus = %+v, want the seeded header", menus)
	}

	var buf bytes.Buffer
	if err := mod.Export(ctx, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	header, rows := readCSV(t, buf.String())
	if strings.Join(header, ",") != strings.Join(menuCSVHeader, ",") {
		t.Errorf("header = %v", header)
	}
	if len(rowsFor(t, header, rows, "header")) == 0 {
		t.Fatalf("no rows for the header menu:\n%s", buf.String())
	}

	// A file that rebuilds the header with a nested item, and builds a menu
	// the store has never had.
	file := "menu,menu_title,path,kind,target\n" +
		"header,Header,Home,home,\n" +
		"header,Header,Shop,url,/products\n" +
		"header,Header,Shop / Shirts,category,shirts\n" +
		"header,Header,Shop / Shirts / Linen,category,linen\n" +
		"sidebar,Sidebar,Help,page,help\n"
	result, err := mod.Import(ctx, strings.NewReader(file), false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Created != 1 || result.Updated != 1 || len(result.Errors) != 0 {
		t.Fatalf("result = %+v, want the sidebar created and the header replaced", result)
	}

	got := gctest.AdminRequest(t, app, http.MethodGet, "/x/navigation/menus/header", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("read the header = %d: %s", got.Code, got.Body)
	}
	var rebuilt Menu
	gctest.DecodeData(t, got, &rebuilt)
	if len(rebuilt.Items) != 2 {
		t.Fatalf("header items = %+v, want the file's two roots", rebuilt.Items)
	}
	if rebuilt.Items[0].Title != "Home" || rebuilt.Items[1].Title != "Shop" {
		t.Errorf("order = %q, %q; the file's row order is the menu's order",
			rebuilt.Items[0].Title, rebuilt.Items[1].Title)
	}
	if len(rebuilt.Items[1].Children) != 1 || rebuilt.Items[1].Children[0].Title != "Shirts" {
		t.Fatalf("children = %+v, want Shirts under Shop", rebuilt.Items[1].Children)
	}
	if len(rebuilt.Items[1].Children[0].Children) != 1 {
		t.Errorf("Linen did not land under Shirts: %+v", rebuilt.Items[1].Children[0])
	}

	// The footer was never named, so nothing happened to it.
	footer := gctest.AdminRequest(t, app, http.MethodGet, "/x/navigation/menus/footer", nil)
	var kept Menu
	gctest.DecodeData(t, footer, &kept)
	if kept.ItemCount == 0 {
		t.Error("a file that named only the header emptied the footer")
	}

	// And the export now says what the file said.
	buf.Reset()
	if err := mod.Export(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	header, rows = readCSV(t, buf.String())
	paths := []string{}
	for _, r := range rowsFor(t, header, rows, "header") {
		paths = append(paths, cell(t, header, r, "path"))
	}
	want := []string{"Home", "Shop", "Shop / Shirts", "Shop / Shirts / Linen"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Errorf("paths = %v, want %v", paths, want)
	}
}

func TestMenuFileRefusesRowsItCannotPlace(t *testing.T) {
	mod := New(Config{})
	app := gctest.New(t, mod)
	ctx := context.Background()
	_ = app

	file := "menu,path,kind,target\n" +
		"scratch,Alone / Orphan,url,/x\n" + // no row above creates Alone
		"scratch,,url,/y\n" + // no path
		"scratch,Bad,teapot,/z\n" + // not a kind
		"scratch,Linkless,url,\n" + // a url with no url
		// A handle the Menus screen would refuse. Letting the file in by a
		// side door would make a menu nobody can rename from the screen.
		"My Menu!,Home,home,\n" +
		"scratch,Fine,url,/ok\n"
	result, err := mod.Import(ctx, strings.NewReader(file), false)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(result.Errors) != 5 {
		t.Fatalf("errors = %+v, want five refused rows", result.Errors)
	}
	for _, e := range result.Errors {
		if e.Line == 0 || e.Message == "" {
			t.Errorf("row error = %+v, want a line and a sentence", e)
		}
	}
	got := gctest.AdminRequest(t, app, http.MethodGet, "/x/navigation/menus/scratch", nil)
	var built Menu
	gctest.DecodeData(t, got, &built)
	if len(built.Items) != 1 || built.Items[0].Title != "Fine" {
		t.Errorf("menu = %+v, want only the row that was good", built.Items)
	}

	// A file with no menu column is one mistake in the header.
	if _, err := mod.Import(ctx, strings.NewReader("path,kind\nHome,home\n"), false); err == nil {
		t.Error("a file with no menu column was accepted")
	}

	// Too deep is one row's mistake with a line number on it, not a file
	// that fails whole and leaves the operator hunting for the row.
	deep := "menu,path,kind,target\n" +
		"deep,A,url,/a\n" +
		"deep,A / B,url,/b\n" +
		"deep,A / B / C,url,/c\n" +
		"deep,A / B / C / D,url,/d\n" +
		"deep,A / B / C / D / E,url,/e\n"
	res, err := mod.Import(ctx, strings.NewReader(deep), false)
	if err != nil {
		t.Fatalf("a five-deep row failed the whole file: %v", err)
	}
	if len(res.Errors) != 1 || res.Errors[0].Line != 6 {
		t.Errorf("errors = %+v, want the fifth level refused on line 6", res.Errors)
	}
}

func TestMenuFileDryRunWritesNothing(t *testing.T) {
	mod := New(Config{})
	app := gctest.New(t, mod)
	ctx := context.Background()

	dry, err := mod.Import(ctx, strings.NewReader("menu,path,kind,target\nrehearsal,Home,home,\n"), true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !dry.DryRun || dry.Created != 1 {
		t.Errorf("dry run = %+v, want it to report the menu it would build", dry)
	}
	if got := gctest.AdminRequest(t, app, http.MethodGet, "/x/navigation/menus/rehearsal", nil); got.Code != http.StatusNotFound {
		t.Errorf("a rehearsal built the menu: %d", got.Code)
	}
}

func TestMenuTransferRoutes(t *testing.T) {
	app := gctest.New(t, New(Config{}))

	out := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/navigation/export", nil)
	if out.Code != http.StatusOK || !strings.HasPrefix(out.Body.String(), "menu,menu_title,") {
		t.Fatalf("export = %d: %s", out.Code, out.Body)
	}
	if ct := out.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content type = %q", ct)
	}

	rec := gctest.AdminUpload(t, app, http.MethodPost, "/api/admin/x/navigation/import", "text/csv",
		"menu,menu_title,path,kind,target\nlegal,Legal,Terms,page,terms\n")
	if rec.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", rec.Code, rec.Body)
	}
	var result gocommerce.ImportResult
	gctest.DecodeData(t, rec, &result)
	if result.Created != 1 {
		t.Errorf("result = %+v", result)
	}

	if denied := gctest.Request(t, app, http.MethodGet, "/api/admin/x/navigation/export", nil); denied.Code != http.StatusUnauthorized {
		t.Errorf("export with no token = %d, want 401", denied.Code)
	}
}
