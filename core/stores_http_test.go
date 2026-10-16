package gocommerce

import (
	"context"
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
