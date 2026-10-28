package cms

import (
	"context"
	"os"
	"regexp"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// A new store starts with the six standard pages as drafts, which no shopper
// sees until the merchant publishes them; seeding again adds nothing, and a
// slug the store already wrote is left as it was.
func TestANewStoreStartsWithTheStandardPagesAsDrafts(t *testing.T) {
	mod := New(Config{})
	app := gctest.New(t, mod)
	st, err := app.Stores().Create(context.Background(), "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	ctx := gocommerce.WithStore(context.Background(), st.ID)

	count := func() (drafts, all int) {
		t.Helper()
		if err := app.DB().QueryRowContext(ctx,
			`SELECT count(*) FILTER (WHERE status = 'draft'), count(*) FROM cms_pages`).Scan(&drafts, &all); err != nil {
			t.Fatal(err)
		}
		return
	}
	if drafts, all := count(); drafts != len(StandardPages) || all != len(StandardPages) {
		t.Fatalf("new store has %d drafts of %d pages, want %d drafts", drafts, all, len(StandardPages))
	}
	var title string
	if err := app.DB().QueryRowContext(ctx, `SELECT title FROM cms_pages WHERE slug = 'refund-policy'`).Scan(&title); err != nil || title != "Refund and returns policy" {
		t.Errorf("refund-policy title = %q (%v)", title, err)
	}

	// Seeding again — and over a page the merchant changed — writes nothing.
	if _, err := app.DB().ExecContext(ctx, `UPDATE cms_pages SET title = 'Our story', status = 'published', published_at = now() WHERE slug = 'about-us'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().ExecContext(ctx, `DELETE FROM cms_pages WHERE slug = 'shipping-policy'`); err != nil {
		t.Fatal(err)
	}
	if err := mod.SeedStore(ctx); err != nil {
		t.Fatal(err)
	}
	if drafts, all := count(); drafts != len(StandardPages)-1 || all != len(StandardPages) {
		t.Errorf("after a second seed: %d drafts of %d pages", drafts, all)
	}
	if err := app.DB().QueryRowContext(ctx, `SELECT title FROM cms_pages WHERE slug = 'about-us'`).Scan(&title); err != nil || title != "Our story" {
		t.Errorf("about-us title after a second seed = %q (%v), want the merchant's", title, err)
	}

	// The first store's pages are not touched by opening a second one.
	var first int
	if err := app.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM cms_pages`).Scan(&first); err != nil || first != 0 {
		t.Errorf("first store has %d pages (%v), want none", first, err)
	}
}

// The Pages screen offers the same six pages to a store that lacks them.
func TestStandardPagesMatchThePanel(t *testing.T) {
	src, err := os.ReadFile("../../admin/src/lib/standard-pages.js")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`slug: "([^"]*)",\s*title: "([^"]*)",\s*body: "([^"]*)"`)
	found := re.FindAllStringSubmatch(string(src), -1)
	if len(found) != len(StandardPages) {
		t.Fatalf("the panel lists %d standard pages, the engine %d", len(found), len(StandardPages))
	}
	for i, p := range StandardPages {
		if got := found[i]; got[1] != p.Slug || got[2] != p.Title || got[3] != p.Body {
			t.Errorf("page %d: panel %q %q %q, engine %q %q %q", i, got[1], got[2], got[3], p.Slug, p.Title, p.Body)
		}
	}
}
