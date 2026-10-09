package cartrecovery

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// harness is one store with the module, a recorder for what it sends, and the
// clock moved where a test needs it. The clock is moved in SQL rather than by
// sleeping: "sat idle for twenty minutes" is a row whose updated_at is twenty
// minutes old, and waiting twenty minutes to make one is not a test.
type harness struct {
	t   *testing.T
	app *gocommerce.App
	mod *Module
	rec *gctest.RecordingNotifier
}

func newHarness(t *testing.T, cfg Config, appCfg gocommerce.Config) *harness {
	t.Helper()
	rec := &gctest.RecordingNotifier{}
	mod := New(cfg)
	app := gctest.NewWithConfig(t, appCfg, mod, notifierModule{rec})
	h := &harness{t: t, app: app, mod: mod, rec: rec}
	// Installed a day ago, so a basket idle for minutes is not history.
	h.exec(`UPDATE cart_recovery_settings SET installed_at = now() - interval '1 day'`)
	return h
}

func (h *harness) exec(q string, args ...any) {
	h.t.Helper()
	if _, err := h.app.DB().Exec(q, args...); err != nil {
		h.t.Fatalf("exec %q: %v", q, err)
	}
}

// basket fills a cart and, with an address, puts it on — which is what makes
// it a checkout rather than a basket.
func (h *harness) basket(sku, email string, qty int) (token string, cartID int64, variantID int64) {
	h.t.Helper()
	ctx := context.Background()
	p := gctest.CreateProduct(h.t, h.app, sku, 2500, 5)
	v := p.DefaultVariant()
	cart, err := h.app.Cart().Create(ctx, "")
	if err != nil {
		h.t.Fatalf("create cart: %v", err)
	}
	if _, err := h.app.Cart().AddLine(ctx, cart.Token, v.ID, qty); err != nil {
		h.t.Fatalf("add line: %v", err)
	}
	if email != "" {
		if _, err := h.app.Cart().SetEmail(ctx, cart.Token, email); err != nil {
			h.t.Fatalf("set email: %v", err)
		}
	}
	if err := h.app.DB().QueryRow(`SELECT id FROM carts WHERE token = $1`, cart.Token).Scan(&cartID); err != nil {
		h.t.Fatal(err)
	}
	return cart.Token, cartID, v.ID
}

// idle backdates a basket's last touch.
func (h *harness) idle(cartID int64, minutes int) {
	h.exec(`UPDATE carts SET updated_at = now() - make_interval(mins => $2) WHERE id = $1`, cartID, minutes)
}

// due brings a record's next step to now.
func (h *harness) due(cartID int64) {
	h.exec(`UPDATE cart_recovery_abandonments SET next_step_at = now() - interval '1 second' WHERE cart_id = $1`, cartID)
}

func (h *harness) pass() PassResult {
	h.t.Helper()
	res, err := h.mod.Pass(context.Background())
	if err != nil {
		h.t.Fatalf("pass: %v", err)
	}
	return res
}

func (h *harness) record(cartID int64) *Abandonment {
	h.t.Helper()
	var id int64
	if err := h.app.DB().QueryRow(`SELECT id FROM cart_recovery_abandonments WHERE cart_id = $1`, cartID).Scan(&id); err != nil {
		h.t.Fatalf("no record for cart %d: %v", cartID, err)
	}
	a, err := h.mod.get(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

func (h *harness) detail(id int64) *Detail {
	h.t.Helper()
	rec := gctest.AdminRequest(h.t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/abandonments/"+itoa(id), nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("detail: %d %s", rec.Code, rec.Body)
	}
	var d Detail
	gctest.DecodeData(h.t, rec, &d)
	return &d
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func testAppConfig() gocommerce.Config { return gocommerce.Config{} }

// The whole point: a checkout left for longer than the threshold is recorded,
// the first reminder goes out when it is due, and it carries a link back.
func TestAnIdleCheckoutIsRecordedAndRemindedWithALinkBack(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	token, cartID, _ := h.basket("R-1", "shopper@example.com", 2)
	h.idle(cartID, 20)

	if res := h.pass(); res.Recorded != 1 {
		t.Fatalf("recorded = %d, want 1", res.Recorded)
	}
	a := h.record(cartID)
	if a.Kind != kindCheckout || a.Status != statusScheduled || a.NextStepAt == nil {
		t.Fatalf("record = kind %q status %q next %v, want checkout scheduled with a time", a.Kind, a.Status, a.NextStepAt)
	}
	if want := time.Now().Add(55 * time.Minute); a.NextStepAt.Before(want) {
		t.Errorf("first step at %v, want about an hour after abandonment", a.NextStepAt)
	}
	if h.rec.Count(templateFirst) != 0 {
		t.Fatal("a reminder went out before it was due")
	}

	h.due(cartID)
	if res := h.pass(); res.Sent != 1 {
		t.Fatalf("sent = %d, want 1 (%+v)", res.Sent, res)
	}
	sent := h.rec.All()
	if len(sent) != 1 || sent[0].Event != templateFirst || sent[0].To != "shopper@example.com" {
		t.Fatalf("sent = %+v, want one cart.recovery.1 to the shopper", sent)
	}
	if want := "https://shop.example.com/cart/" + token; sent[0].Data["recovery_url"] != want {
		t.Errorf("recovery_url = %q, want %q", sent[0].Data["recovery_url"], want)
	}
	if sent[0].Data["item_count"] != "2" || sent[0].Data["subtotal_minor"] != "5000" {
		t.Errorf("data = %v, want two items worth 5000", sent[0].Data)
	}

	a = h.record(cartID)
	if a.Status != statusContacted || a.StepsSent != 1 || a.MessagesSent != 1 || a.Indicator != "email_sent" {
		t.Errorf("after send: status %q steps %d messages %d indicator %q", a.Status, a.StepsSent, a.MessagesSent, a.Indicator)
	}
	if a.NextStepAt == nil || a.NextStepAt.Before(time.Now().Add(19*time.Hour)) {
		t.Errorf("second step at %v, want twenty hours after the first", a.NextStepAt)
	}

	// The same pass again sends nothing: nothing is due.
	if res := h.pass(); res.Sent != 0 {
		t.Errorf("a second pass sent %d, want 0", res.Sent)
	}
}

// A basket that never reached checkout has no address and nobody to write to,
// but it is still counted — the screen reports what was left behind, not only
// what can be chased.
func TestABasketWithNoAddressIsCountedAndNotChased(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	_, cartID, _ := h.basket("R-2", "", 1)
	h.idle(cartID, 40)
	h.pass()

	a := h.record(cartID)
	if a.Kind != kindCart || a.Status != statusAbandoned || a.HoldReason != holdNoContact || a.Indicator != holdNoContact {
		t.Fatalf("record = %+v, want an abandoned cart held for no_contact", a)
	}
	h.exec(`UPDATE cart_recovery_abandonments SET next_step_at = now() WHERE cart_id = $1`, cartID)
	h.pass()
	if n := len(h.rec.All()); n != 0 {
		t.Fatalf("sent %d messages to a basket with no address", n)
	}
}

// A basket shorter than its threshold is not abandoned yet, and a cart idle
// past the cart threshold but short of nothing is: the two kinds keep their
// own clocks.
func TestThresholdsArePerKind(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	_, checkoutID, _ := h.basket("R-3a", "a@example.com", 1)
	_, cartID, _ := h.basket("R-3b", "", 1)
	h.idle(checkoutID, 5) // checkout threshold is 10
	h.idle(cartID, 15)    // cart threshold is 30
	if res := h.pass(); res.Recorded != 0 {
		t.Fatalf("recorded %d baskets short of their thresholds", res.Recorded)
	}
	h.idle(checkoutID, 11)
	h.idle(cartID, 31)
	if res := h.pass(); res.Recorded != 2 {
		t.Fatalf("recorded %d, want both once past their thresholds", res.Recorded)
	}
}

// Installing the module must not mail everyone whose basket went stale before
// it existed. The engine's note on subscribeNotifications names that as the
// reason core does not chase baskets itself, so the module that does has to
// answer it.
func TestBasketsIdleBeforeInstallAreShownButNeverChased(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	h.exec(`UPDATE cart_recovery_settings SET installed_at = now()`)
	_, cartID, _ := h.basket("R-4", "old@example.com", 1)
	h.idle(cartID, 60*24*3)
	h.pass()

	a := h.record(cartID)
	if !a.History || a.HoldReason != holdBeforeInstall || a.NextStepAt != nil {
		t.Fatalf("record = history %v hold %q next %v, want history held before_install", a.History, a.HoldReason, a.NextStepAt)
	}
}

// The order a basket becomes is credited to it, and nothing more is sent.
func TestAnOrderFromTheBasketRecoversIt(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	token, cartID, _ := h.basket("R-5", "buyer@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	h.due(cartID)
	h.pass()

	result, err := h.app.Order().Checkout(context.Background(), "cod", gocommerce.CheckoutInput{
		CartID: token, Email: "buyer@example.com", Name: "Buyer",
		Address: gocommerce.Address{Line1: "1 Test Street", City: "Testville", State: "CA",
			PostalCode: "94117", Country: "US", Phone: "+15555550123"},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	gctest.DrainOutbox(t, h.app)

	a := h.record(cartID)
	if a.Status != statusRecovered || a.RecoveredOrder == nil || a.RecoveredOrder.Number != result.Order.Number {
		t.Fatalf("record = %q %+v, want recovered by %s", a.Status, a.RecoveredOrder, result.Order.Number)
	}
	if a.RecoveredAfterMessage == nil || !*a.RecoveredAfterMessage {
		t.Error("recovered_after_message = false, want true: a reminder went out first")
	}
	if a.NextStepAt != nil || a.Indicator != "purchased" {
		t.Errorf("next %v indicator %q, want nothing scheduled and purchased", a.NextStepAt, a.Indicator)
	}

	// At least once means twice sometimes: the second delivery changes nothing.
	gctest.DrainOutbox(t, h.app)
	var events int
	h.app.DB().QueryRow(`SELECT count(*) FROM cart_recovery_events e JOIN cart_recovery_abandonments a
		ON a.id = e.abandonment_id WHERE a.cart_id = $1 AND e.kind = 'recovered'`, cartID).Scan(&events)
	if events != 1 {
		t.Errorf("recovered events = %d, want 1", events)
	}

	// And the screen refuses to chase it, in words it can show.
	rec := gctest.AdminRequest(t, h.app, http.MethodPost,
		"/api/admin/x/cart-recovery/abandonments/"+itoa(a.ID)+"/send", map[string]any{"template": templateFirst})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already_recovered") ||
		!strings.Contains(rec.Body.String(), result.Order.Number) {
		t.Fatalf("send on a recovered basket = %d %s, want 409 already_recovered naming the order", rec.Code, rec.Body)
	}
}

// A shopper who came back on their own is recovered all the same, and the
// sequence does not take the credit.
func TestARecoveryWithoutAMessageIsNotCreditedToTheSequence(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	token, cartID, _ := h.basket("R-6", "self@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	if _, err := h.app.Order().Checkout(context.Background(), "cod", gocommerce.CheckoutInput{
		CartID: token, Email: "self@example.com", Name: "Self",
		Address: gocommerce.Address{Line1: "1 Test Street", City: "Testville", State: "CA",
			PostalCode: "94117", Country: "US", Phone: "+15555550123"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	gctest.DrainOutbox(t, h.app)

	a := h.record(cartID)
	if a.Status != statusRecovered || a.RecoveredAfterMessage == nil || *a.RecoveredAfterMessage {
		t.Fatalf("record = %q after_message %v, want recovered without a message", a.Status, a.RecoveredAfterMessage)
	}
	rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/summary", nil)
	var s Summary
	gctest.DecodeData(t, rec, &s)
	if s.Recovered != 1 || s.RecoveredAfterMessage != 0 || s.RecoveryRateBP != 10000 {
		t.Errorf("summary = %+v, want one recovered, none after a message, rate 100%%", s)
	}
}

// Suppression stops the sequence, stays visible, and is answered in words when
// somebody tries to send anyway.
func TestSuppressionStopsTheSequenceAndStaysVisible(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	_, cartID, _ := h.basket("R-7", "stop@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	a := h.record(cartID)

	rec := gctest.AdminRequest(t, h.app, http.MethodPost,
		"/api/admin/x/cart-recovery/abandonments/"+itoa(a.ID)+"/suppress",
		map[string]any{"reason": "requested_no_contact", "note": "asked on the phone"})
	if rec.Code != http.StatusOK {
		t.Fatalf("suppress = %d %s", rec.Code, rec.Body)
	}
	a = h.record(cartID)
	if a.Status != statusSuppressed || a.NextStepAt != nil || a.Suppression == nil ||
		a.Suppression.Reason != "requested_no_contact" || a.Suppression.Note != "asked on the phone" {
		t.Fatalf("record = %+v, want suppressed with its reason and nothing scheduled", a)
	}

	h.due(cartID)
	h.pass()
	if len(h.rec.All()) != 0 {
		t.Fatal("a suppressed basket was reminded")
	}
	rec = gctest.AdminRequest(t, h.app, http.MethodPost,
		"/api/admin/x/cart-recovery/abandonments/"+itoa(a.ID)+"/send", map[string]any{})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "suppressed") {
		t.Fatalf("send on a suppressed basket = %d %s, want 409 suppressed", rec.Code, rec.Body)
	}

	list := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/abandonments?status=suppressed", nil)
	var rows []Abandonment
	gctest.DecodeData(t, list, &rows)
	if len(rows) != 1 {
		t.Fatalf("suppressed list = %d rows, want 1", len(rows))
	}

	// "other" needs a note, and an unknown reason is refused.
	_, otherID, _ := h.basket("R-7b", "x@example.com", 1)
	h.idle(otherID, 20)
	h.pass()
	b := h.record(otherID)
	for _, body := range []map[string]any{{"reason": "other"}, {"reason": "bored"}} {
		rec := gctest.AdminRequest(t, h.app, http.MethodPost,
			"/api/admin/x/cart-recovery/abandonments/"+itoa(b.ID)+"/suppress", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("suppress %v = %d, want 400", body, rec.Code)
		}
	}
}

// An operator's send goes out now, is recorded with who sent it, and leaves
// the automated sequence where it was.
func TestAManualSendIsRecordedAndLeavesTheSequenceAlone(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	_, cartID, _ := h.basket("R-8", "manual@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	a := h.record(cartID)

	rec := gctest.AdminRequest(t, h.app, http.MethodPost,
		"/api/admin/x/cart-recovery/abandonments/"+itoa(a.ID)+"/send",
		map[string]any{"channel": "email", "template": templateSecond})
	if rec.Code != http.StatusOK {
		t.Fatalf("send = %d %s", rec.Code, rec.Body)
	}
	if h.rec.Count(templateSecond) != 1 {
		t.Fatalf("cart.recovery.2 sent %d times, want 1", h.rec.Count(templateSecond))
	}
	a = h.record(cartID)
	if a.MessagesSent != 1 || a.StepsSent != 0 || a.Status != statusContacted || a.NextStepAt == nil {
		t.Errorf("record = messages %d steps %d status %q next %v, want a manual send that left step 1 pending",
			a.MessagesSent, a.StepsSent, a.Status, a.NextStepAt)
	}
	d := h.detail(a.ID)
	var manual *TimelineEvent
	for i, e := range d.Timeline {
		if e.Kind == "sent" {
			manual = &d.Timeline[i]
		}
	}
	if manual == nil || manual.Actor != "token" || !strings.Contains(manual.Label, "by hand") {
		t.Errorf("timeline = %+v, want a manual send attributed to the caller", d.Timeline)
	}
}

// The link an operator copies is the tracked one when the engine knows its own
// address, and following it counts the click and lands on the basket.
func TestTheRecoveryLinkCountsItsClickAndLandsOnTheBasket(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"},
		gocommerce.Config{PanelURL: "https://admin.example.com"})
	token, cartID, _ := h.basket("R-9", "click@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	a := h.record(cartID)

	rec := gctest.AdminRequest(t, h.app, http.MethodPost,
		"/api/admin/x/cart-recovery/abandonments/"+itoa(a.ID)+"/link", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("link = %d %s", rec.Code, rec.Body)
	}
	var link struct {
		URL     string `json:"url"`
		Tracked bool   `json:"tracked"`
	}
	gctest.DecodeData(t, rec, &link)
	if !link.Tracked || !strings.HasPrefix(link.URL, "https://admin.example.com/x/cart-recovery/r/") {
		t.Fatalf("link = %+v, want the tracked redirect", link)
	}

	path := strings.TrimPrefix(link.URL, "https://admin.example.com")
	click := gctest.Request(t, h.app, http.MethodGet, path, nil)
	if click.Code != http.StatusFound {
		t.Fatalf("click = %d %s, want 302", click.Code, click.Body)
	}
	if loc := click.Header().Get("Location"); loc != "https://shop.example.com/cart/"+token {
		t.Errorf("Location = %q, want the basket", loc)
	}
	a = h.record(cartID)
	if a.Clicks != 1 || a.FirstClickedAt == nil || a.Indicator != "clicked" {
		t.Errorf("record = clicks %d first %v indicator %q, want one click", a.Clicks, a.FirstClickedAt, a.Indicator)
	}

	if bad := gctest.Request(t, h.app, http.MethodGet, "/x/cart-recovery/r/nope", nil); bad.Code != http.StatusNotFound {
		t.Errorf("unknown token = %d, want 404", bad.Code)
	}
}

// Without a storefront address there is nowhere for a link to land, and the
// screen is told so rather than handed half a URL.
func TestWithoutAStorefrontTheLinkIsRefusedAndTheTokenTravelsAlone(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	token, cartID, _ := h.basket("R-10", "nolink@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	a := h.record(cartID)

	rec := gctest.AdminRequest(t, h.app, http.MethodPost,
		"/api/admin/x/cart-recovery/abandonments/"+itoa(a.ID)+"/link", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no_storefront") {
		t.Fatalf("link = %d %s, want 409 no_storefront", rec.Code, rec.Body)
	}
	h.due(cartID)
	h.pass()
	sent := h.rec.All()
	if len(sent) != 1 {
		t.Fatalf("sent %d, want 1", len(sent))
	}
	if _, ok := sent[0].Data["recovery_url"]; ok || sent[0].Data["cart_token"] != token {
		t.Errorf("data = %v, want the token and no recovery_url", sent[0].Data)
	}
}

// A shopper in the basket right now is not written to: the step waits for the
// visit to go quiet, and the record says the shopper came back.
func TestAShopperWhoCameBackIsNotRemindedMidVisit(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	token, cartID, variantID := h.basket("R-11", "back@example.com", 1)
	h.idle(cartID, 20)
	h.pass()

	if _, err := h.app.Cart().AddLine(context.Background(), token, variantID, 1); err != nil {
		t.Fatal(err)
	}
	h.due(cartID)
	res := h.pass()
	if res.Resumed != 1 || res.Sent != 0 {
		t.Fatalf("pass = %+v, want the visit noticed and nothing sent", res)
	}
	a := h.record(cartID)
	if a.ItemCount != 2 || a.NextStepAt == nil || a.NextStepAt.Before(time.Now().Add(5*time.Minute)) {
		t.Errorf("record = items %d next %v, want the new basket and a step after the visit", a.ItemCount, a.NextStepAt)
	}
}

// Nothing buyable, nothing sent — and the screen says why.
func TestABasketWithNothingBuyableIsHeld(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	_, cartID, variantID := h.basket("R-12", "sold@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	if _, err := h.app.Stock().Adjust(context.Background(), variantID, 0, -5, "sold out"); err != nil {
		t.Fatal(err)
	}
	h.idle(cartID, 20)
	h.due(cartID)
	h.pass()
	if len(h.rec.All()) != 0 {
		t.Fatal("reminded about a basket nothing in which can be bought")
	}
	a := h.record(cartID)
	if a.HoldReason != holdNothingPurchasable || a.Indicator != "inventory_unavailable" {
		t.Fatalf("record = hold %q indicator %q, want nothing_purchasable", a.HoldReason, a.Indicator)
	}
	d := h.detail(a.ID)
	if len(d.Lines) != 1 || d.Lines[0].Stock != "out_of_stock" {
		t.Errorf("lines = %+v, want the line shown out of stock", d.Lines)
	}
}

// A basket deleted by core's retention purge is expired, not left pending.
func TestADeletedBasketExpires(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	_, cartID, _ := h.basket("R-13", "gone@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	h.exec(`DELETE FROM carts WHERE id = $1`, cartID)
	if res := h.pass(); res.Expired != 1 {
		t.Fatalf("expired = %d, want 1", res.Expired)
	}
	a := h.record(cartID)
	if a.Status != statusExpired || a.NextStepAt != nil || a.ExpiredAt == nil {
		t.Fatalf("record = %+v, want expired", a)
	}
}

// Switching a sequence off stops its reminders the moment the save returns,
// and switching it back on plans them again.
func TestSavingTheAutomationReplansOpenRecords(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	_, cartID, _ := h.basket("R-14", "plan@example.com", 1)
	h.idle(cartID, 20)
	h.pass()

	s := DefaultSettings()
	s.Checkout.Enabled = false
	rec := gctest.AdminRequest(t, h.app, http.MethodPut, "/api/admin/x/cart-recovery/settings", s)
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	a := h.record(cartID)
	if a.Status != statusAbandoned || a.NextStepAt != nil || a.HoldReason != holdAutomationOff {
		t.Fatalf("after switching off: %q next %v hold %q", a.Status, a.NextStepAt, a.HoldReason)
	}

	s.Checkout.Enabled = true
	s.Checkout.Steps = []Step{{WaitMinutes: 5, Action: actionEmail, Template: templateLast}}
	rec = gctest.AdminRequest(t, h.app, http.MethodPut, "/api/admin/x/cart-recovery/settings", s)
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	a = h.record(cartID)
	if a.Status != statusScheduled || a.NextStepAt == nil {
		t.Fatalf("after switching on: %q next %v, want scheduled again", a.Status, a.NextStepAt)
	}
	// Five minutes after abandonment, which was moments ago.
	if a.NextStepAt.After(time.Now().Add(6 * time.Minute)) {
		t.Errorf("next step %v, want the new five-minute wait", a.NextStepAt)
	}

	var view SettingsView
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/settings", nil), &view)
	if !view.Customized || len(view.Settings.Checkout.Steps) != 1 || len(view.Templates) != 3 || len(view.Guards) == 0 {
		t.Errorf("view = %+v, want the saved sequence, three templates and the guards", view)
	}
}

func TestTheAutomationRefusesWhatItCannotRun(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	bad := []func(*Settings){
		func(s *Settings) { s.Checkout.AbandonAfterMinutes = 0 },
		func(s *Settings) { s.Cart.Steps[0].WaitMinutes = 0 },
		func(s *Settings) { s.Cart.Steps[0].Action = "send_sms" },
		func(s *Settings) { s.Cart.Steps[0].Template = "order.created" },
		func(s *Settings) {
			for len(s.Checkout.Steps) <= maxSteps {
				s.Checkout.Steps = append(s.Checkout.Steps, s.Checkout.Steps[0])
			}
		},
		func(s *Settings) { s.StorefrontURL = "shop.example.com" },
	}
	for i, mutate := range bad {
		s := DefaultSettings()
		mutate(&s)
		rec := gctest.AdminRequest(t, h.app, http.MethodPut, "/api/admin/x/cart-recovery/settings", s)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("case %d: %d %s, want 400", i, rec.Code, rec.Body)
		}
	}
}

// The cards above the list count the list: the same filter reaches both.
func TestTheSummaryAndTheListAgree(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	for i, email := range []string{"a@example.com", "b@example.com", ""} {
		_, cartID, _ := h.basket("R-15-"+itoa(int64(i)), email, 1)
		h.idle(cartID, 45)
	}
	h.pass()

	var all, checkouts Summary
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/summary", nil), &all)
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/summary?kind=checkout", nil), &checkouts)
	if all.Abandoned != 3 || all.Reachable != 2 || all.Potential.AmountMinor != 7500 {
		t.Errorf("all = %+v, want 3 abandoned, 2 reachable, 7500 potential", all)
	}
	if checkouts.Abandoned != 2 {
		t.Errorf("checkouts = %+v, want 2", checkouts)
	}
	list := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/abandonments?kind=checkout", nil)
	var rows []Abandonment
	gctest.DecodeData(t, list, &rows)
	if len(rows) != checkouts.Abandoned {
		t.Errorf("list = %d rows, summary says %d", len(rows), checkouts.Abandoned)
	}

	var an Analytics
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, http.MethodGet,
		"/api/admin/x/cart-recovery/analytics?group_by=week&tz=Asia/Kolkata", nil), &an)
	if len(an.Funnel) != 5 || an.Funnel[0].Count != 3 || an.Funnel[1].Count != 2 || len(an.Trend) == 0 || len(an.Breakdown) != 2 {
		t.Errorf("analytics = %+v", an)
	}
	for _, q := range []string{"?status=lost", "?kind=box", "?from=yesterday", "?sort=email"} {
		if rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/abandonments"+q, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("list%s = %d, want 400", q, rec.Code)
		}
	}
	if rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/analytics?tz=Mars/Base", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("analytics with a bad zone = %d, want 400", rec.Code)
	}
}

func TestEveryRouteDeclaresARightAndIsInTheSpec(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	gctest.AssertAdminRoutesDeclareRights(t, h.app, "cart-recovery")
	gctest.AssertSpecCoversModuleRoutes(t, h.app, "cart-recovery")
}

// A role without the right is refused, and staff carry the reading and the
// contacting but not the automation.
func TestStaffMayChaseButNotRewriteTheAutomation(t *testing.T) {
	h := newHarness(t, Config{}, testAppConfig())
	tok := gctest.OperatorToken(t, h.app, "staff@example.com", gocommerce.RoleStaff)
	if rec := gctest.SessionRequest(t, h.app, tok, http.MethodGet, "/api/admin/x/cart-recovery/abandonments", nil); rec.Code != http.StatusOK {
		t.Errorf("staff list = %d, want 200", rec.Code)
	}
	if rec := gctest.SessionRequest(t, h.app, tok, http.MethodPut, "/api/admin/x/cart-recovery/settings", DefaultSettings()); rec.Code != http.StatusForbidden {
		t.Errorf("staff save = %d, want 403", rec.Code)
	}
}

// A storefront URL that is not a URL is a configuration mistake worth refusing
// at startup rather than discovering in somebody's inbox.
func TestABadStorefrontURLIsRefusedAtStartup(t *testing.T) {
	if err := New(Config{StorefrontURL: "shop.example.com"}).Register(nil); err == nil {
		t.Fatal("a StorefrontURL with no scheme was accepted, want an error")
	}
}

// notifierModule installs the recorder through the public module surface, the
// same way a real store installs ext/notify-sendgrid.
type notifierModule struct{ rec *gctest.RecordingNotifier }

func (notifierModule) Name() string                       { return "recorder" }
func (notifierModule) Migrations() []gocommerce.Migration { return nil }
func (m notifierModule) Register(app *gocommerce.App) error {
	app.RegisterNotifier(gocommerce.ChannelEmail, m.rec)
	return nil
}
