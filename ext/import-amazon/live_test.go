package amazon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveAmazonPage drives the real Chrome at a real listing. It is the only
// test here that proves the extraction script against today's Amazon rather
// than a fixture, and it runs only when asked:
//
//	AMAZON_LIVE_URL=https://www.amazon.com/dp/B0... go test ./ext/import-amazon -run Live -v
//
// It needs Chrome installed and a network. It writes the listing it read to
// stdout under -v, which is how a change to Amazon's markup gets diagnosed.
// A robot check is reported as a skip, not a failure — that is Amazon's
// choice, not a defect in the extractor.
func TestLiveAmazonPage(t *testing.T) {
	rawURL := os.Getenv("AMAZON_LIVE_URL")
	if rawURL == "" {
		t.Skip("set AMAZON_LIVE_URL to a product page to run the live test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	headed := os.Getenv("AMAZON_LIVE_HEADED") != ""
	// The profile persists under the temp directory across runs on purpose:
	// a check passed once in the headed window should not have to be passed
	// again to run this test twice.
	profile := filepath.Join(os.TempDir(), "gocommerce-import-amazon-live-test")
	f, err := newChromeFetcher(ctx, "", profile, !headed, 2*time.Second, 2*time.Minute, slog.Default())
	if err != nil {
		t.Fatalf("start chrome: %v", err)
	}
	defer f.close()
	f.onWait = func(message string) { t.Logf("waiting: %s", message) }

	first, err := f.fetch(ctx, rawURL)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	t.Logf("document title %q; page begins %q", first.DocTitle, first.Sample)
	debug, _ := json.MarshalIndent(first.Debug, "", "  ")
	t.Logf("what the script saw:\n%s", debug)

	listing, warnings, err := crawl(ctx, f, rawURL, 3)
	if err == errBlocked {
		t.Skip("Amazon showed a robot check; run again with AMAZON_LIVE_HEADED=1 and pass it")
	}
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	pretty, _ := json.MarshalIndent(listing, "", "  ")
	t.Logf("listing:\n%s\nwarnings: %v", pretty, warnings)

	if listing.Title == "" || listing.ASIN == "" {
		t.Error("no title or ASIN read")
	}
	if len(listing.Images) == 0 {
		t.Error("no images read — the colorImages script may have changed shape")
	}
	if listing.PriceMinor == 0 {
		t.Log("no price read (fine for an unavailable listing; otherwise the price selector has moved)")
	}
}
