package newsletter

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

func TestAnAddressSignsUpLeavesAndIsListed(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()

	rec := gctest.Request(t, app, http.MethodPost, "/x/newsletter/subscribe", SubscribeInput{Email: " Ann@Example.com ", Name: "Ann", Source: "footer"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"subscribed"`) || strings.Contains(rec.Body.String(), "token") {
		t.Fatalf("subscribe = %d: %s", rec.Code, rec.Body)
	}
	// Twice is once; a bot filling the honeypot is told yes and kept out.
	gctest.Request(t, app, http.MethodPost, "/x/newsletter/subscribe", SubscribeInput{Email: "ann@example.com"})
	if bot := gctest.Request(t, app, http.MethodPost, "/x/newsletter/subscribe", SubscribeInput{Email: "bot@example.com", Website: "http://spam"}); bot.Code != http.StatusCreated {
		t.Errorf("honeypot = %d", bot.Code)
	}
	if bad := gctest.Request(t, app, http.MethodPost, "/x/newsletter/subscribe", SubscribeInput{Email: "not an address"}); bad.Code != http.StatusBadRequest {
		t.Errorf("bad address = %d", bad.Code)
	}

	list := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/newsletter/subscriptions", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", list.Code, list.Body)
	}
	var subs []Subscription
	gctest.DecodeData(t, list, &subs)
	if len(subs) != 1 || subs[0].Email != "ann@example.com" || subs[0].Name != "Ann" || subs[0].Source != "footer" || subs[0].Status != StatusSubscribed {
		t.Fatalf("subscriptions = %+v, want Ann once, lower-cased, and no bot", subs)
	}

	// The unsubscribe link carries the token, which only the store knows.
	var tok string
	if err := app.DB().QueryRowContext(ctx, `SELECT token FROM newsletter_subscriptions WHERE email = 'ann@example.com'`).Scan(&tok); err != nil {
		t.Fatal(err)
	}
	if left := gctest.Request(t, app, http.MethodGet, "/x/newsletter/unsubscribe?token="+tok, nil); left.Code != http.StatusOK {
		t.Errorf("unsubscribe = %d: %s", left.Code, left.Body)
	}
	if unknown := gctest.Request(t, app, http.MethodGet, "/x/newsletter/unsubscribe?token=nope", nil); unknown.Code != http.StatusOK {
		t.Errorf("unknown token = %d, want the same quiet 200", unknown.Code)
	}
	// Fresh variables per decode: json.Unmarshal reuses a slice's elements,
	// and an omitted field would keep its stale value.
	gone := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/newsletter/subscriptions?status=unsubscribed", nil)
	var left []Subscription
	gctest.DecodeData(t, gone, &left)
	if len(left) != 1 || left[0].UnsubscribedAt == nil {
		t.Errorf("after unsubscribing = %+v", left)
	}
	// Signing up again brings the address back.
	gctest.Request(t, app, http.MethodPost, "/x/newsletter/subscribe", SubscribeInput{Email: "ann@example.com", Source: "checkout"})
	back := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/newsletter/subscriptions?status=subscribed", nil)
	var again []Subscription
	gctest.DecodeData(t, back, &again)
	if len(again) != 1 || again[0].Source != "checkout" || again[0].UnsubscribedAt != nil {
		t.Errorf("after re-subscribing = %+v", again)
	}

	// The export is what a mailing tool reads; the delete is for a request
	// to be forgotten.
	csv := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/newsletter/subscriptions.csv", nil)
	if csv.Code != http.StatusOK || !strings.HasPrefix(csv.Body.String(), "email,name,source,status,subscribed_at,unsubscribed_at\nann@example.com,Ann,checkout,subscribed,") {
		t.Errorf("export = %d:\n%s", csv.Code, csv.Body)
	}
	added := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/newsletter/subscriptions", SubscribeInput{Email: "bob@example.com"})
	var bob Subscription
	gctest.DecodeData(t, added, &bob)
	if added.Code != http.StatusCreated || bob.Source != "admin" {
		t.Errorf("add by hand = %d %+v", added.Code, bob)
	}
	if del := gctest.AdminRequest(t, app, http.MethodDelete, "/api/admin/x/newsletter/subscriptions/"+strconv.FormatInt(bob.ID, 10), nil); del.Code != http.StatusNoContent {
		t.Errorf("delete = %d", del.Code)
	}

	// Off: the box is shut, the list stays.
	off := false
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if shut := gctest.Request(t, app, http.MethodPost, "/x/newsletter/subscribe", SubscribeInput{Email: "late@example.com"}); shut.Code != http.StatusNotFound {
		t.Errorf("subscribe while off = %d", shut.Code)
	}
}

func TestNewsletterRoutesAreDocumentedAndGated(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "newsletter")
	gctest.AssertSpecCoversModuleRoutes(t, app, "newsletter")
}
