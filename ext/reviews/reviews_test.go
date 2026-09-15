package reviews

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

func TestAReviewIsModeratedVerifiedAndSummed(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()
	product := gctest.CreateProduct(t, app, "RV-MUG", 900, 10)
	// The test shopper (gctest@example.com) buys it, so a review from that
	// address is verified and one from another is not.
	gctest.Buy(t, app, gocommerce.CodeCOD, product.Variants[0].ID, 1)

	post := func(in ReviewInput) int {
		t.Helper()
		rec := gctest.Request(t, app, http.MethodPost, "/x/reviews", in)
		if rec.Code >= 300 {
			t.Logf("post %+v -> %d %s", in, rec.Code, rec.Body)
		}
		return rec.Code
	}
	if code := post(ReviewInput{ProductID: product.ID, Name: "GC", Email: "gctest@example.com", Rating: 5, Title: "Lovely", Body: "Keeps the tea hot."}); code != http.StatusCreated {
		t.Fatalf("buyer's review = %d", code)
	}
	if code := post(ReviewInput{ProductID: product.ID, Name: "Ann", Email: "ann@example.com", Rating: 3, Body: "Fine."}); code != http.StatusCreated {
		t.Fatalf("stranger's review = %d", code)
	}
	if code := post(ReviewInput{ProductID: product.ID, Name: "Six", Email: "six@example.com", Rating: 6, Body: "!"}); code != http.StatusBadRequest {
		t.Errorf("rating 6 = %d", code)
	}
	if code := post(ReviewInput{ProductID: 999999, Name: "Ghost", Email: "g@example.com", Rating: 4, Body: "?"}); code != http.StatusNotFound {
		t.Errorf("unknown product = %d", code)
	}

	// Nothing shows until moderated.
	public := gctest.Request(t, app, http.MethodGet, "/x/reviews?product_id="+strconv.FormatInt(product.ID, 10), nil)
	if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), `"count":0`) {
		t.Errorf("public before moderation = %d: %s", public.Code, public.Body)
	}

	list := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/reviews?status=pending", nil)
	var pending []Review
	gctest.DecodeData(t, list, &pending)
	if len(pending) != 2 || pending[0].Product != product.Title {
		t.Fatalf("pending = %+v", pending)
	}
	byName := map[string]Review{}
	for _, rv := range pending {
		byName[rv.Name] = rv
	}
	if !byName["GC"].Verified || byName["Ann"].Verified {
		t.Errorf("verified: GC %v, Ann %v; want the buyer only", byName["GC"].Verified, byName["Ann"].Verified)
	}

	approved := StatusApproved
	reply := "Thank you!"
	for _, name := range []string{"GC", "Ann"} {
		rec := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/x/reviews/"+strconv.FormatInt(byName[name].ID, 10), ReviewPatch{Status: &approved, Reply: &reply})
		if rec.Code != http.StatusOK {
			t.Fatalf("approve %s = %d: %s", name, rec.Code, rec.Body)
		}
	}
	public = gctest.Request(t, app, http.MethodGet, "/x/reviews?product_id="+strconv.FormatInt(product.ID, 10), nil)
	var shown struct {
		Reviews []publicReview `json:"reviews"`
		Summary Summary        `json:"summary"`
	}
	gctest.DecodeData(t, public, &shown)
	if len(shown.Reviews) != 2 || shown.Summary.Count != 2 || shown.Summary.Average != 4 || shown.Summary.Distribution[5] != 1 || shown.Summary.Distribution[3] != 1 {
		t.Errorf("public = %+v", shown)
	}
	if strings.Contains(public.Body.String(), "example.com") {
		t.Errorf("a reviewer's email reached the storefront")
	}
	if shown.Reviews[0].Reply != "Thank you!" {
		t.Errorf("reply = %q", shown.Reviews[0].Reply)
	}

	// The Plugins screen's two switches: buyers only, and approve on sight.
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Settings: map[string]any{"require_purchase": true, "auto_approve": true}}); err != nil {
		t.Fatal(err)
	}
	if code := post(ReviewInput{ProductID: product.ID, Name: "Bob", Email: "bob@example.com", Rating: 1, Body: "Never bought it."}); code != http.StatusBadRequest {
		t.Errorf("stranger with buyers-only = %d", code)
	}
	rec := gctest.Request(t, app, http.MethodPost, "/x/reviews", ReviewInput{ProductID: product.ID, Name: "GC again", Email: "gctest@example.com", Rating: 4, Body: "Second one."})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"approved"`) {
		t.Errorf("auto-approved = %d: %s", rec.Code, rec.Body)
	}

	if del := gctest.AdminRequest(t, app, http.MethodDelete, "/api/admin/x/reviews/"+strconv.FormatInt(byName["Ann"].ID, 10), nil); del.Code != http.StatusNoContent {
		t.Errorf("delete = %d", del.Code)
	}
}

func TestReviewRoutesAreDocumentedAndGated(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "reviews")
	gctest.AssertSpecCoversModuleRoutes(t, app, "reviews")
}
