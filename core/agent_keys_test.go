package gocommerce

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Agent keys (ADR 0003): an API key with kind = agent, a list of rights instead
// of a role, an expiry, a rate limit, and no readable copy of its secret.

// person makes an operator in a role and returns them as an agent key's
// issuer: a real row, because created_by points at superusers, with the
// rights this store gives the role.
func person(t *testing.T, app *App, email, role string) *Superuser {
	t.Helper()
	su, err := app.Superusers().Create(context.Background(), email, "a-long-enough-password", role)
	if err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	return su
}

// newAgentKey mints an agent key carrying rights, issued by an owner.
func newAgentKey(t *testing.T, app *App, by *Superuser, name string, rights ...Right) (*APIKey, string) {
	t.Helper()
	key, secret, err := app.APIKeys().Create(context.Background(), APIKeyInput{
		Name: name, Kind: KeyKindAgent, Rights: rights,
	}, by)
	if err != nil {
		t.Fatalf("create agent key %s: %v", name, err)
	}
	return key, secret
}

// wantRefusal asserts a validation error whose message names what was wrong.
func wantRefusal(t *testing.T, err error, mentions string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want a refusal naming %q, got none", mentions)
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("refusal = %v, want a validation error", err)
	}
	if !strings.Contains(err.Error(), mentions) {
		t.Errorf("refusal %q does not name %q", err, mentions)
	}
}

// The subset rule needs an issuer. A static token is nobody, and another key
// has no rights of its own to be a subset of — so neither can mint one.
func TestAnAgentKeyIsIssuedByAPerson(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	in := APIKeyInput{Name: "Order desk", Kind: KeyKindAgent, Rights: []Right{RightOrdersRead}}

	_, _, err := app.APIKeys().Create(ctx, in, nil)
	wantRefusal(t, err, "signed in")

	_, ownerKey, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "Owner script", Role: RoleOwner}, nil)
	if err != nil {
		t.Fatal(err)
	}
	asKey, ok := app.APIKeys().Resolve(ctx, ownerKey)
	if !ok {
		t.Fatal("the owner key does not resolve")
	}
	_, _, err = app.APIKeys().Create(ctx, in, asKey)
	wantRefusal(t, err, "signed in")

	// Over HTTP too, where the static token is the obvious way to try.
	rec := doBody(t, app, http.MethodPost, "/api/admin/api-keys",
		`{"name":"Order desk","kind":"agent","rights":["orders.read"]}`, withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("static token minting an agent key = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// What an agent key may carry: a non-empty list of rights this build knows,
// each one its issuer holds, each one a read, and none of the never-set.
func TestAnAgentKeysRightsAreReadsTheIssuerHolds(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)
	staff := person(t, app, "staff@example.com", RoleStaff)

	mint := func(by *Superuser, rights ...Right) error {
		_, _, err := app.APIKeys().Create(ctx, APIKeyInput{
			Name: "Desk", Kind: KeyKindAgent, Rights: rights,
		}, by)
		return err
	}

	wantRefusal(t, mint(owner), "at least one right")
	wantRefusal(t, mint(owner, "nonsense.read"), "nonsense.read")
	// Writes wait for the approval queue: nothing an agent key holds today can
	// change the store.
	for _, write := range []Right{RightOrdersWrite, RightOrdersRefund, RightOrdersFulfill,
		RightCatalogWrite, RightInventoryWrite} {
		wantRefusal(t, mint(owner, RightOrdersRead, write), string(write))
	}
	// The never-set, refused by name even to an owner who holds every one.
	for _, never := range []Right{RightAPIKeysRead, RightAPIKeysWrite, RightTeamRead,
		RightTeamWrite, RightRolesWrite, RightPluginsRead, RightPluginsWrite, RightStoreWrite,
		RightStoreOperate, RightDataExport, RightDataImport, RightReportsWrite, RightErrorsRead} {
		if _, listed := AgentKeyRefusedRights[never]; !listed {
			t.Errorf("%s is not in AgentKeyRefusedRights", never)
		}
		wantRefusal(t, mint(owner, never), string(never))
	}
	// Staff does not hold vendors.read, so cannot pass it on.
	wantRefusal(t, mint(staff, RightVendorsRead), string(RightVendorsRead))

	key, secret, err := app.APIKeys().Create(ctx, APIKeyInput{
		Name: "  Order desk  ", Kind: KeyKindAgent,
		Rights: []Right{RightOrdersRead, RightCustomersRead, RightOrdersRead},
	}, owner)
	if err != nil {
		t.Fatalf("a read-only agent key from an owner: %v", err)
	}
	if key.Kind != KeyKindAgent || key.Name != "Order desk" {
		t.Errorf("key = %+v, want an agent key named Order desk", key)
	}
	if !slices.Equal(key.Rights, []Right{RightCustomersRead, RightOrdersRead}) {
		t.Errorf("rights = %v, want the two reads once each, sorted", key.Rights)
	}
	if !strings.HasPrefix(secret, apiKeyScheme) {
		t.Errorf("secret = %q, want a gck_ key", secret)
	}
	if key.CreatedBy == nil || *key.CreatedBy != owner.ID {
		t.Errorf("created_by = %v, want the owner %d", key.CreatedBy, owner.ID)
	}
}

// An agent key expires (1 to 365 days, 90 by default) and is rationed (1 to
// 600 requests a minute, 60 by default).
func TestAnAgentKeysExpiryAndRateHaveBounds(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)
	mint := func(days, rate *int) (*APIKey, error) {
		key, _, err := app.APIKeys().Create(ctx, APIKeyInput{
			Name: "Desk", Kind: KeyKindAgent, Rights: []Right{RightOrdersRead},
			ExpiresInDays: days, RatePerMinute: rate,
		}, owner)
		return key, err
	}

	for _, days := range []int{0, -1, 366} {
		_, err := mint(intp(days), nil)
		wantRefusal(t, err, "365 days")
	}
	for _, rate := range []int{0, -3, 601} {
		_, err := mint(nil, intp(rate))
		wantRefusal(t, err, "600 requests")
	}

	key, err := mint(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key.ExpiresAt == nil {
		t.Fatal("an agent key with no expiry")
	}
	if left := time.Until(*key.ExpiresAt); left < 89*24*time.Hour || left > 91*24*time.Hour {
		t.Errorf("expires in %v, want 90 days by default", left)
	}
	if key.RatePerMinute == nil || *key.RatePerMinute != 60 {
		t.Errorf("rate = %v, want 60 a minute by default", key.RatePerMinute)
	}

	key, err = mint(intp(1), intp(600))
	if err != nil {
		t.Fatalf("the edges of both ranges: %v", err)
	}
	if left := time.Until(*key.ExpiresAt); left > 25*time.Hour {
		t.Errorf("a one-day key expires in %v", left)
	}
	if *key.RatePerMinute != 600 {
		t.Errorf("rate = %d, want 600", *key.RatePerMinute)
	}
}

// An integration key carries a role and none of an agent's terms; a kind the
// engine does not know is not quietly made into one it does.
func TestAnIntegrationKeyRefusesAgentTerms(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)

	_, _, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "x", Role: RoleStaff,
		Rights: []Right{RightOrdersRead}}, owner)
	wantRefusal(t, err, "agent")
	_, _, err = app.APIKeys().Create(ctx, APIKeyInput{Name: "x", Role: RoleStaff,
		ExpiresInDays: intp(30)}, owner)
	wantRefusal(t, err, "agent")
	_, _, err = app.APIKeys().Create(ctx, APIKeyInput{Name: "x", Role: RoleStaff, Kind: "robot"}, owner)
	wantRefusal(t, err, "robot")
	_, _, err = app.APIKeys().Create(ctx, APIKeyInput{Name: "x", Role: RoleStaff, Kind: KeyKindAgent,
		Rights: []Right{RightOrdersRead}}, owner)
	wantRefusal(t, err, "role")
}

// Only the hash of an agent key is kept: the readable copy M46 keeps is for
// integration keys, so an agent's is shown once and never again.
func TestAnAgentKeyIsShownOnce(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)
	key, secret := newAgentKey(t, app, owner, "Order desk", RightOrdersRead)

	var stored string
	if err := app.DB().QueryRowContext(ctx,
		`SELECT secret FROM api_keys WHERE id = $1`, key.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "" {
		t.Error("an agent key's secret was stored")
	}
	if _, err := app.APIKeys().Secret(ctx, key.ID); !errors.Is(err, ErrConflict) ||
		!strings.Contains(err.Error(), "shown once") {
		t.Errorf("reading an agent key back = %v, want a conflict saying it is shown once", err)
	}

	token := signInAs(t, app, "owner2@example.com", RoleOwner)
	rec := do(t, app, http.MethodGet, "/api/admin/api-keys/"+strconv.FormatInt(key.ID, 10)+"/secret", bearer(token))
	if rec.Code != http.StatusConflict {
		t.Fatalf("GET …/secret on an agent key = %d, want 409: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Error("the refusal carries the secret")
	}

	// The hash still authenticates.
	if _, ok := app.APIKeys().Resolve(ctx, secret); !ok {
		t.Error("the agent key it just made does not authenticate")
	}
}

// An agent key stops at its expiry and at its revocation.
func TestAnAgentKeyStopsAtExpiryAndRevocation(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)

	key, secret := newAgentKey(t, app, owner, "Short lived", RightOrdersRead)
	if _, ok := app.APIKeys().Resolve(ctx, secret); !ok {
		t.Fatal("a fresh agent key should work")
	}
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE api_keys SET expires_at = now() - interval '1 second' WHERE id = $1`, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.APIKeys().Resolve(ctx, secret); ok {
		t.Error("an expired agent key still authenticates")
	}

	key, secret = newAgentKey(t, app, owner, "Revoked", RightOrdersRead)
	if err := app.APIKeys().Revoke(ctx, key.ID, owner); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.APIKeys().Resolve(ctx, secret); ok {
		t.Error("a revoked agent key still authenticates")
	}
}

// An agent key acts with exactly the rights it was issued — not a role's, and
// never one of the refused set, even if a row is edited behind the engine's
// back.
func TestAnAgentKeyActsWithExactlyItsRights(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)
	key, secret := newAgentKey(t, app, owner, "Order desk", RightOrdersRead, RightCustomersRead)

	who, ok := app.APIKeys().Resolve(ctx, secret)
	if !ok {
		t.Fatal("the agent key does not resolve")
	}
	if who.Role != KeyKindAgent || who.KeyKind != KeyKindAgent || who.KeyID != key.ID || who.ID != 0 {
		t.Errorf("resolved = role %q kind %q key %d id %d, want an agent key %d with no person",
			who.Role, who.KeyKind, who.KeyID, who.ID, key.ID)
	}
	if !slices.Equal(who.Rights, []Right{RightCustomersRead, RightOrdersRead}) {
		t.Errorf("rights = %v, want exactly the two it was issued", who.Rights)
	}

	// The second line: a right written into the row by hand is stripped at
	// resolution if an agent key may never hold it, or may not hold it yet.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE api_keys SET rights = rights || '{team.read,orders.write}'::text[] WHERE id = $1`, key.ID); err != nil {
		t.Fatal(err)
	}
	who, ok = app.APIKeys().Resolve(ctx, secret)
	if !ok {
		t.Fatal("the edited agent key does not resolve")
	}
	if !slices.Equal(who.Rights, []Right{RightCustomersRead, RightOrdersRead}) {
		t.Errorf("rights = %v, want team.read and orders.write stripped", who.Rights)
	}

	// Held to the list over HTTP, and refused in words that say it is a key.
	rec := do(t, app, http.MethodGet, "/api/admin/orders", bearer(secret))
	if rec.Code != http.StatusOK {
		t.Fatalf("agent reading orders = %d: %s", rec.Code, rec.Body)
	}
	rec = doBody(t, app, http.MethodPost, "/api/admin/orders/1/cancel", `{}`, bearer(secret))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent cancelling = %d, want 403: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "agent key") || !strings.Contains(body, string(RightOrdersWrite)) {
		t.Errorf("refusal = %s, want it to say an agent key lacks orders.write", body)
	}
}

// Integration keys are what they were: a role, its rights, no expiry, no
// rate. The caller now says it is a key, which is what the audit trail reads.
func TestAnIntegrationKeyStillActsInItsRole(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)

	key, secret, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "Warehouse robot", Role: RoleStaff}, owner)
	if err != nil {
		t.Fatal(err)
	}
	if key.Kind != KeyKindIntegration {
		t.Errorf("kind = %q, want integration by default", key.Kind)
	}
	who, ok := app.APIKeys().Resolve(ctx, secret)
	if !ok {
		t.Fatal("does not resolve")
	}
	if who.KeyKind != KeyKindIntegration || who.KeyID != key.ID || who.Role != RoleStaff {
		t.Errorf("resolved = kind %q key %d role %q", who.KeyKind, who.KeyID, who.Role)
	}
	if !slices.Equal(who.Rights, DefaultRightsOf(RoleStaff)) {
		t.Errorf("rights = %v, want staff's", who.Rights)
	}

	agent, _ := newAgentKey(t, app, owner, "Order desk", RightOrdersRead)
	rows, err := app.APIKeys().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]APIKey{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if got := byID[key.ID]; got.Kind != KeyKindIntegration || got.Rights != nil ||
		got.ExpiresAt != nil || got.RatePerMinute != nil {
		t.Errorf("integration key listed as %+v, want a role and nothing of an agent's", got)
	}
	if got := byID[agent.ID]; got.Kind != KeyKindAgent || !slices.Equal(got.Rights, []Right{RightOrdersRead}) ||
		got.ExpiresAt == nil || got.RatePerMinute == nil || *got.RatePerMinute != 60 {
		t.Errorf("agent key listed as %+v, want kind, rights, expiry and rate", got)
	}

	// The integration key's secret is still readable (M46).
	if again, err := app.APIKeys().Secret(ctx, key.ID); err != nil || again != secret {
		t.Errorf("integration key read back = %q, %v", again, err)
	}
}

// The panel's checklist comes from the engine: the reads the caller holds that
// an agent key may carry.
func TestTheAgentRightsAnOwnerCanGive(t *testing.T) {
	app := newTestApp(t)
	token := signInAs(t, app, "owner@example.com", RoleOwner)

	rec := do(t, app, http.MethodGet, "/api/admin/api-keys/agent-rights", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("agent rights = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Data []RightCatalogue `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := map[Right]bool{}
	for _, r := range out.Data {
		got[r.Right] = true
	}
	for _, want := range []Right{RightOrdersRead, RightCustomersRead, RightCatalogRead, RightReportsRead} {
		if !got[want] {
			t.Errorf("%s missing from what an owner can give an agent", want)
		}
	}
	for _, never := range []Right{RightOrdersWrite, RightTeamRead, RightAPIKeysRead, RightPluginsRead, RightErrorsRead} {
		if got[never] {
			t.Errorf("%s offered for an agent key", never)
		}
	}
}
