package gocommerce

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"
)

// The /api/stores routes are about the person, not one store: they list the
// stores somebody works in, move them into one, and open a new one.

// joinTestStore makes an existing person staff of another store, the way an
// accepted invitation would, without the invitation.
func joinTestStore(t *testing.T, app *App, storeID, email, role string) {
	t.Helper()
	if _, err := app.db.ExecContext(context.Background(), `
		INSERT INTO store_members (store_id, superuser_id, role)
		SELECT $1, id, $3 FROM superusers WHERE email = $2`, storeID, email, role); err != nil {
		t.Fatal(err)
	}
}

func TestAPersonSeesTheStoresTheyWorkIn(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "two@example.com", RoleOwner)
	b := addTestStore(t, app, "second")
	addTestStore(t, app, "not-theirs")
	joinTestStore(t, app, b, "two@example.com", RoleStaff)

	rec := do(t, app, http.MethodGet, "/api/stores/my", bearer(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var mine []MyStore
	decodeData(t, rec, &mine)
	if len(mine) != 2 {
		t.Fatalf("stores = %+v, want their two", mine)
	}
	if mine[0].ID != app.DefaultStore() || mine[0].Role != RoleOwner ||
		mine[1].ID != b || mine[1].Slug != "second" || mine[1].Role != RoleStaff {
		t.Fatalf("stores = %+v, want first as owner then second as staff", mine)
	}
}

// A store the panel last remembered may since have gone. The list of stores
// is how the panel recovers from that, so it must not depend on the name.
func TestTheStoreListIgnoresTheStoreTheRequestNames(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "lost@example.com", RoleOwner)
	rec := do(t, app, http.MethodGet, "/api/stores/my", bearer(tok),
		header("x-litekart-store", "store_01ZZZZZZZZZZZZZZZZZZZZZZZZ"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}

func TestTheStoreListNeedsACredential(t *testing.T) {
	app := newTestApp(t)
	if rec := do(t, app, http.MethodGet, "/api/stores/my"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestSwitchingAnswersWithTheRoleThere(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "mover@example.com", RoleOwner)
	b := addTestStore(t, app, "second")
	joinTestStore(t, app, b, "mover@example.com", RoleStaff)

	rec := doBody(t, app, http.MethodPost, "/api/stores/switch", `{"store_id":"`+b+`"}`, bearer(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Store  Store     `json:"store"`
		Record Superuser `json:"record"`
	}
	decodeData(t, rec, &out)
	if out.Store.ID != b || out.Record.Role != RoleStaff || len(out.Record.Rights) == 0 {
		t.Fatalf("switch = %+v, want store B with the staff role and its rights", out)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "litekart_store_id" {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != b || !cookie.HttpOnly || cookie.Path != "/" {
		t.Fatalf("cookie = %+v, want litekart_store_id=%s on / and HttpOnly", cookie, b)
	}
}

func TestSwitchingTakesASlugToo(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "slug@example.com", RoleOwner)
	b := addTestStore(t, app, "second")
	joinTestStore(t, app, b, "slug@example.com", RoleStaff)
	rec := doBody(t, app, http.MethodPost, "/api/stores/switch", `{"store_id":"second"}`, bearer(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}

func TestNobodySwitchesIntoAStoreTheyAreNotIn(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "outsider@example.com", RoleOwner)
	b := addTestStore(t, app, "second")
	rec := doBody(t, app, http.MethodPost, "/api/stores/switch", `{"store_id":"`+b+`"}`, bearer(tok))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Header().Get("Set-Cookie"), "litekart_store_id") {
		t.Fatal("a refused switch still set the store cookie")
	}
	// An unknown store reads the same as somebody else's: the answer must not
	// tell an outsider which stores exist.
	other := doBody(t, app, http.MethodPost, "/api/stores/switch", `{"store_id":"no-such-store"}`, bearer(tok))
	if other.Code != http.StatusForbidden {
		t.Fatalf("unknown store status = %d, want 403", other.Code)
	}
}

func TestASuspendedStoreCannotBeSwitchedInto(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "paused@example.com", RoleOwner)
	b := addTestStore(t, app, "paused")
	joinTestStore(t, app, b, "paused@example.com", RoleOwner)
	if _, err := app.db.Exec(`UPDATE stores SET status = 'suspended' WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	rec := doBody(t, app, http.MethodPost, "/api/stores/switch", `{"store_id":"`+b+`"}`, bearer(tok))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}
}

func TestCreatingAStoreMakesTheCallerItsOwner(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "founder@example.com", RoleStaff)
	rec := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Corner Shop","slug":"corner"}`, bearer(tok))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Store  Store     `json:"store"`
		Record Superuser `json:"record"`
	}
	decodeData(t, rec, &out)
	if out.Store.Slug != "corner" || out.Store.Name != "Corner Shop" || out.Record.Role != RoleOwner {
		t.Fatalf("create = %+v, want corner with the caller as owner", out)
	}
	// And the new store is somewhere they can go.
	sw := doBody(t, app, http.MethodPost, "/api/stores/switch", `{"store_id":"corner"}`, bearer(tok))
	if sw.Code != http.StatusOK {
		t.Fatalf("switch into the new store = %d: %s", sw.Code, sw.Body)
	}
}

func TestATakenSlugIsAConflict(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "dup@example.com", RoleOwner)
	addTestStore(t, app, "taken")
	rec := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Taken","slug":"taken"}`, bearer(tok))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body)
	}
}

func TestAStaticTokenSeesEveryStoreAsOwner(t *testing.T) {
	app := newTestApp(t)
	addTestStore(t, app, "second")
	rec := do(t, app, http.MethodGet, "/api/stores/my", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var mine []MyStore
	decodeData(t, rec, &mine)
	if len(mine) != 2 || mine[0].Role != RoleOwner || mine[1].Role != RoleOwner {
		t.Fatalf("stores = %+v, want both as owner", mine)
	}
}

// The store cookie only says which store a new tab opens in. A cookie left
// naming a store that has since gone or been suspended must not stand
// between somebody and signing in: JavaScript cannot clear an HttpOnly
// cookie, so the panel would have no way out.
func TestAStaleStoreCookieDoesNotBlockSigningIn(t *testing.T) {
	app := newTestApp(t)
	signInAs(t, app, "stale@example.com", RoleOwner)
	paused := addTestStore(t, app, "paused")
	if _, err := app.db.Exec(`UPDATE stores SET status = 'suspended' WHERE id = $1`, paused); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"gone":      "store_01ZZZZZZZZZZZZZZZZZZZZZZZZ",
		"suspended": paused,
	} {
		rec := doBody(t, app, http.MethodPost, "/api/admin/auth-with-password",
			`{"identity":"stale@example.com","password":"a-long-enough-password"}`,
			func(r *http.Request) { r.AddCookie(&http.Cookie{Name: storeCookie, Value: value}) })
		if rec.Code != http.StatusOK {
			t.Fatalf("%s store in the cookie: sign-in = %d: %s", name, rec.Code, rec.Body)
		}
	}
}

func TestSigningOutClearsTheStoreCookie(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "leaving@example.com", RoleOwner)
	rec := do(t, app, http.MethodPost, "/api/admin/auth-logout", bearer(tok))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d: %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == storeCookie && c.MaxAge < 0 {
			return
		}
	}
	t.Fatal("signing out left the store cookie in place")
}

func TestAnAPIKeyCannotListStores(t *testing.T) {
	app := newTestApp(t)
	_, secret, err := app.APIKeys().Create(context.Background(), APIKeyInput{Name: "scripts", Role: RoleOwner}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodGet, "/api/stores/my", bearer(secret)); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
}

// Somebody in several stores who has not chosen one yet (the store picker)
// can still sign out: the session is theirs, not one store's.
func TestSigningOutNeedsNoStore(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "bee")
	c := addTestStore(t, app, "sea")
	if _, err := app.Superusers().Create(WithStore(context.Background(), b), "two@example.com", "a-long-enough-password", RoleOwner); err != nil {
		t.Fatal(err)
	}
	joinTestStore(t, app, c, "two@example.com", RoleStaff)
	_, sess, err := app.Superusers().Authenticate(context.Background(), "two@example.com", "a-long-enough-password", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodPost, "/api/admin/auth-logout", bearer(sess.Token)); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d: %s", rec.Code, rec.Body)
	}
	if _, ok := app.Superusers().Resolve(context.Background(), sess.Token); ok {
		t.Fatal("the session outlived signing out")
	}
}

// A store has one name. Settings → Store details is where people change it,
// so that is the name the store list shows.
func TestRenamingTheStoreShowsInTheStoreList(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "renamer@example.com", RoleOwner)
	ctx := WithStore(context.Background(), app.DefaultStore())
	profile, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profile.Name = "Corner Shop"
	if _, err := app.Profile().Set(ctx, *profile, nil); err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodGet, "/api/stores/my", bearer(tok))
	var mine []MyStore
	decodeData(t, rec, &mine)
	if len(mine) == 0 || mine[0].Name != "Corner Shop" {
		t.Fatalf("stores = %+v, want the first called Corner Shop", mine)
	}
}

func TestANewStoresProfileCarriesItsName(t *testing.T) {
	app := newTestApp(t)
	st, err := app.Stores().Create(context.Background(), "Night Market", "night")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := app.Profile().Get(WithStore(context.Background(), st.ID))
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "Night Market" {
		t.Fatalf("profile name = %q, want the store's", profile.Name)
	}
}

// A name typed into the profile before the two were joined is the store's
// name after the upgrade.
func TestTheUpgradeTakesTheStoreNameFromTheProfile(t *testing.T) {
	app := newTestApp(t)
	ctx := WithStore(context.Background(), app.DefaultStore())
	if _, err := app.db.ExecContext(ctx, `UPDATE store_profile SET name = 'Old Name Shop'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`UPDATE stores SET name = '' WHERE id = $1`, app.DefaultStore()); err != nil {
		t.Fatal(err)
	}
	if err := InTx(context.Background(), app.db, func(tx *sql.Tx) error {
		return migrate0057StoreNames(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
	st, err := app.Stores().Get(context.Background(), app.DefaultStore())
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != "Old Name Shop" {
		t.Fatalf("store name = %q, want the profile's", st.Name)
	}
}

// The store list says where each store lives, for the account menu's
// "Visit store": its own domain first, else its address under the
// install's base domain, else nothing.
func TestTheStoreListSaysWhereEachStoreLives(t *testing.T) {
	app := newTestApp(t)
	app.cfg.BaseDomain = "shops.test"
	tok := signInAs(t, app, "where@example.com", RoleOwner)
	b := addTestStore(t, app, "bee")
	joinTestStore(t, app, b, "where@example.com", RoleStaff)
	if _, err := app.db.Exec(`INSERT INTO store_domains (host, store_id, verified_at) VALUES ('bee.example', $1, now())`, b); err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodGet, "/api/stores/my", bearer(tok))
	var mine []MyStore
	decodeData(t, rec, &mine)
	if len(mine) != 2 || mine[0].Domain != mine[0].Slug+".shops.test" || mine[1].Domain != "bee.example" {
		t.Fatalf("stores = %+v, want slug.shops.test then bee.example", mine)
	}
	// A store with a domain of its own still answers at its subdomain, and
	// the Domains screen says so.
	if mine[0].Subdomain != mine[0].Slug+".shops.test" || mine[1].Subdomain != "bee.shops.test" {
		t.Fatalf("subdomains = %q, %q, want slug.shops.test and bee.shops.test", mine[0].Subdomain, mine[1].Subdomain)
	}

	app.cfg.BaseDomain = ""
	rec = do(t, app, http.MethodGet, "/api/stores/my", bearer(tok))
	decodeData(t, rec, &mine)
	if mine[0].Domain != "" || mine[0].Subdomain != "" {
		t.Fatalf("with no base domain, the first store lives at %q / %q, want nowhere", mine[0].Domain, mine[0].Subdomain)
	}
}

// Whether the store cookie is Secure is the operator's to say — a TLS
// connection, or a panel configured on https — not a header any client can
// send.
func TestTheStoreCookieIgnoresAForwardedProtoAnybodyCanSend(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "proto@example.com", RoleOwner)
	secure := func() bool {
		rec := doBody(t, app, http.MethodPost, "/api/stores/switch", `{"store_id":"`+app.DefaultStore()+`"}`,
			bearer(tok), header("X-Forwarded-Proto", "https"))
		for _, c := range rec.Result().Cookies() {
			if c.Name == storeCookie {
				return c.Secure
			}
		}
		t.Fatalf("no store cookie: %d %s", rec.Code, rec.Body)
		return false
	}
	if secure() {
		t.Fatal("a client's X-Forwarded-Proto made the cookie Secure")
	}
	app.cfg.PanelURL = "https://admin.example.com"
	if !secure() {
		t.Fatal("a panel configured on https did not make the cookie Secure")
	}
}

// A store and its owner are written together: an owner that cannot be
// recorded leaves no store behind that nobody can enter.
func TestAStoreAndItsOwnerAreWrittenTogether(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.Stores().CreateFor(context.Background(), "Orphan", "orphan", 987654321); err == nil {
		t.Fatal("a store was created for an owner who does not exist")
	}
	if _, err := app.Stores().BySlug(context.Background(), "orphan"); err == nil {
		t.Fatal("the store row outlived its failed owner")
	}
}

func TestANewStoreStartsWithSampleProductsOnlyWhenAsked(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "founder@example.com", RoleStaff)

	rec := doBody(t, app, http.MethodPost, "/api/stores/create",
		`{"name":"Sampled","slug":"sampled","sample_products":true}`, bearer(tok))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with samples = %d: %s", rec.Code, rec.Body)
	}
	var with struct {
		Store          Store `json:"store"`
		SampleProducts *struct {
			Added int    `json:"added"`
			Error string `json:"error"`
		} `json:"sample_products"`
	}
	decodeData(t, rec, &with)
	if with.SampleProducts == nil || with.SampleProducts.Added != 8 || with.SampleProducts.Error != "" {
		t.Fatalf("sample_products = %+v, want 8 added", with.SampleProducts)
	}
	if got := sampleCount(t, app, WithStore(context.Background(), with.Store.ID)); got != 8 {
		t.Fatalf("the new store holds %d samples, want 8", got)
	}

	rec = doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Plain","slug":"plain"}`, bearer(tok))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create without samples = %d: %s", rec.Code, rec.Body)
	}
	var without struct {
		Store          Store `json:"store"`
		SampleProducts any   `json:"sample_products"`
	}
	decodeData(t, rec, &without)
	if without.SampleProducts != nil {
		t.Fatalf("sample_products = %v on a store that did not ask, want it absent", without.SampleProducts)
	}
	if got := sampleCount(t, app, WithStore(context.Background(), without.Store.ID)); got != 0 {
		t.Fatalf("a store opened without samples holds %d", got)
	}
}

// The sample table is written in major units, so a tee is 25 in every
// currency: 2500 minor units in dollars, 25 in yen, which has no decimals, and
// 25000 in dinars, which have three.
func TestSamplePricesFollowTheNewStoresCurrency(t *testing.T) {
	app := newTestApp(t)
	tok := signInAs(t, app, "yen@example.com", RoleStaff)
	for _, tc := range []struct {
		currency string
		want     int64
	}{{"JPY", 25}, {"KWD", 25000}} {
		rec := doBody(t, app, http.MethodPost, "/api/stores/create",
			`{"name":"Shop `+tc.currency+`","currency":"`+tc.currency+`","sample_products":true}`, bearer(tok))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s store = %d: %s", tc.currency, rec.Code, rec.Body)
		}
		var out struct {
			Store Store `json:"store"`
		}
		decodeData(t, rec, &out)
		tee, err := app.Products().GetProductBySlug(WithStore(context.Background(), out.Store.ID), "cotton-crew-tee")
		if err != nil {
			t.Fatal(err)
		}
		if got := tee.Variants[0].Price; got != (Money{AmountMinor: tc.want, Currency: tc.currency}) {
			t.Errorf("%s tee = %+v, want %d %s", tc.currency, got, tc.want, tc.currency)
		}
	}
}
