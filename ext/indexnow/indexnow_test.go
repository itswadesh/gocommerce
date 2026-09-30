package indexnow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// What IndexNow gets wrong, and what this module must not.
//
// The protocol is four fields in a JSON body, so the interesting failures are
// not in the request. They are: a key file that does not match byte for byte
// (403, with nothing said about why), a URL that is not on the host the key
// vouches for (422, and the whole batch is refused), and a store that pings
// once per edit until it is rate limited.

// recorder is a stub IndexNow that keeps what it was sent.
type recorder struct {
	mu      sync.Mutex
	bodies  []map[string]any
	status  int
	calls   int
	release chan struct{}
}

func (r *recorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)

		r.mu.Lock()
		r.bodies = append(r.bodies, parsed)
		r.calls++
		status := r.status
		r.mu.Unlock()

		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if r.release != nil {
			select {
			case r.release <- struct{}{}:
			default:
			}
		}
	})
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *recorder) last() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		return nil
	}
	return r.bodies[len(r.bodies)-1]
}

// newApp boots an engine with the module, pointed at a stub endpoint and with a
// window short enough that a test does not wait thirty seconds for it.
func newApp(t *testing.T, rec *recorder, window time.Duration) (*gocommerce.App, *Module, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)

	m := New(Config{Endpoint: srv.URL, Window: window, HTTP: srv.Client()})
	app := gctest.New(t, m)

	on := true
	if _, err := app.Plugins().Update(context.Background(), pluginKey, gocommerce.PluginPatch{
		Enabled:  &on,
		Settings: map[string]any{"storefront_url": "https://shop.example"},
	}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return app, m, srv
}

func urlsOf(body map[string]any) []string {
	raw, _ := body["urlList"].([]any)
	out := make([]string, 0, len(raw))
	for _, u := range raw {
		s, _ := u.(string)
		out = append(out, s)
	}
	return out
}

func TestASubmissionCarriesWhatTheProtocolWants(t *testing.T) {
	rec := &recorder{}
	app, m, _ := newApp(t, rec, time.Hour)
	ctx := context.Background()

	if err := m.Submit(ctx, []string{"https://shop.example/products/linen-jumper"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	body := rec.last()
	if body == nil {
		t.Fatal("nothing was submitted")
	}
	if body["host"] != "shop.example" {
		t.Errorf("host = %v", body["host"])
	}
	key, _ := body["key"].(string)
	if len(key) < 8 {
		t.Errorf("key = %q, want one generated for the store", key)
	}
	// The key file has to be findable, and the protocol is told where.
	if want := "https://shop.example/" + key + ".txt"; body["keyLocation"] != want {
		t.Errorf("keyLocation = %v, want %s", body["keyLocation"], want)
	}
	if got := urlsOf(body); len(got) != 1 || got[0] != "https://shop.example/products/linen-jumper" {
		t.Errorf("urlList = %v", got)
	}

	// The key was kept, so the next submission uses the same one — a key that
	// changed every time would never validate.
	if err := m.Submit(ctx, []string{"https://shop.example/products/other"}); err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if again, _ := rec.last()["key"].(string); again != key {
		t.Errorf("the key changed between submissions: %q then %q", key, again)
	}
	_ = app
}

// The engine hands the key out byte-exact, so a storefront proxying this route
// serves something IndexNow will accept.
func TestTheKeyIsHandedOutExactly(t *testing.T) {
	rec := &recorder{}
	app, m, _ := newApp(t, rec, time.Hour)
	ctx := context.Background()

	if err := m.Submit(ctx, []string{"https://shop.example/products/x"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	key, _ := rec.last()["key"].(string)

	resp := gctest.Request(t, app, http.MethodGet, "/x/indexnow/key", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /x/indexnow/key = %d", resp.Code)
	}
	// Byte for byte: a trailing newline is the commonest reason a real
	// submission comes back 403, and it is invisible in a browser.
	if got := resp.Body.String(); got != key {
		t.Errorf("the key route answers %q, want exactly %q with nothing around it", got, key)
	}
}

// A URL that is not on the key's host is refused by IndexNow for the whole
// batch, so it is dropped here rather than losing everything else with it.
func TestForeignURLsAreDroppedRatherThanPoisoningTheBatch(t *testing.T) {
	rec := &recorder{}
	_, m, _ := newApp(t, rec, time.Hour)

	if err := m.Submit(context.Background(), []string{
		"https://shop.example/products/mine",
		"https://someone-else.example/products/theirs",
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	got := urlsOf(rec.last())
	if len(got) != 1 || !strings.HasPrefix(got[0], "https://shop.example/") {
		t.Errorf("urlList = %v, want only the URLs on this store's host", got)
	}
}

// The submission that would have contained nothing is not sent at all.
func TestNothingToSubmitSendsNothing(t *testing.T) {
	rec := &recorder{}
	_, m, _ := newApp(t, rec, time.Hour)

	if err := m.Submit(context.Background(), []string{"https://elsewhere.example/x"}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if rec.count() != 0 {
		t.Errorf("%d requests were made with nothing to say", rec.count())
	}
}

// 202 is the ordinary answer to a first submission — received, key validation
// pending — and treating it as a failure would log an error on every new store.
func TestAcceptedIsSuccess(t *testing.T) {
	rec := &recorder{status: http.StatusAccepted}
	_, m, _ := newApp(t, rec, time.Hour)

	if err := m.Submit(context.Background(), []string{"https://shop.example/products/x"}); err != nil {
		t.Errorf("202 was treated as a failure: %v", err)
	}
}

// And the refusals say what to do about them.
func TestRefusalsExplainThemselves(t *testing.T) {
	for _, c := range []struct {
		status int
		expect string
	}{
		{http.StatusForbidden, "trailing newline"},
		{http.StatusUnprocessableEntity, "do not belong"},
		{http.StatusTooManyRequests, "rate limited"},
	} {
		rec := &recorder{status: c.status}
		_, m, _ := newApp(t, rec, time.Hour)
		err := m.Submit(context.Background(), []string{"https://shop.example/products/x"})
		if err == nil {
			t.Errorf("%d was treated as success", c.status)
			continue
		}
		if !strings.Contains(err.Error(), c.expect) {
			t.Errorf("%d said %q, want it to mention %q", c.status, err, c.expect)
		}
	}
}

// The batching. An operator repricing a range must not send one ping per edit.
func TestABulkEditIsOnePing(t *testing.T) {
	rec := &recorder{release: make(chan struct{}, 4)}
	app, _, _ := newApp(t, rec, 150*time.Millisecond)
	ctx := context.Background()
	price := int64(2500)

	for _, title := range []string{"One", "Two", "Three"} {
		if _, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{
			Title: title, Status: "active", PriceMinor: &price,
		}); err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
	}
	// All three delivered before the window closes, which is the case being
	// tested: three edits inside one batch are one ping.
	gctest.DrainOutbox(t, app)

	select {
	case <-rec.release:
	case <-time.After(10 * time.Second):
		t.Fatal("nothing was submitted; the events did not reach the module")
	}
	// Let any second submission arrive, so this fails if the batching does not
	// hold rather than passing because it was quick.
	time.Sleep(400 * time.Millisecond)

	if got := rec.count(); got != 1 {
		t.Errorf("%d submissions for three edits, want 1", got)
	}
	if got := urlsOf(rec.last()); len(got) != 3 {
		t.Errorf("the one submission carried %v, want all three URLs", got)
	}
}

// A rename leaves an address behind, and the old one is submitted too so a
// search engine looks at the 404 and drops it.
func TestARenameSubmitsTheAddressItLeftBehind(t *testing.T) {
	rec := &recorder{release: make(chan struct{}, 4)}
	app, _, _ := newApp(t, rec, 120*time.Millisecond)
	ctx := context.Background()
	price := int64(2500)

	p, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{
		Title: "Linen jumper", Status: "active", PriceMinor: &price,
	})
	if err != nil {
		t.Fatal(err)
	}
	gctest.DrainOutbox(t, app)
	select {
	case <-rec.release:
	case <-time.After(10 * time.Second):
		t.Fatal("the create was not submitted")
	}

	slug := "linen-jumper-2026"
	if _, err := app.Products().UpdateProduct(ctx, p.ID, gocommerce.ProductPatch{Slug: &slug}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	gctest.DrainOutbox(t, app)
	select {
	case <-rec.release:
	case <-time.After(10 * time.Second):
		t.Fatal("the rename was not submitted")
	}

	got := urlsOf(rec.last())
	var hasNew, hasOld bool
	for _, u := range got {
		if strings.HasSuffix(u, "/products/linen-jumper-2026") {
			hasNew = true
		}
		if strings.HasSuffix(u, "/products/linen-jumper") {
			hasOld = true
		}
	}
	if !hasNew {
		t.Errorf("the new address was not submitted: %v", got)
	}
	if !hasOld {
		t.Errorf("the old address was not submitted, so the 404 stays indexed: %v", got)
	}
}

// Switched off, it does nothing at all — including serving a key file that
// would let somebody claim the site.
func TestDisabledIsSilent(t *testing.T) {
	rec := &recorder{}
	app, m, _ := newApp(t, rec, 100*time.Millisecond)
	ctx := context.Background()

	if err := m.Submit(ctx, []string{"https://shop.example/products/x"}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	off := false
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &off}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := gctest.Request(t, app, http.MethodGet, "/x/indexnow/key", nil).Code; got != http.StatusNotFound {
		t.Errorf("the key is still handed out while disabled: %d", got)
	}

	before := rec.count()
	price := int64(2500)
	if _, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{
		Title: "Quiet", Status: "active", PriceMinor: &price,
	}); err != nil {
		t.Fatal(err)
	}
	gctest.DrainOutbox(t, app)
	time.Sleep(400 * time.Millisecond)
	if rec.count() != before {
		t.Errorf("a disabled module submitted %d times", rec.count()-before)
	}
}
