package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// An API key is a credential with a role.
//
// Config.AdminTokens already exists and is deliberately roleless — it is the
// one scripts use, it has no person behind it, and requireRights waves it
// through everything. That is the right shape for a deploy script and the
// wrong shape for the thing a store actually wants: a key for a shipping
// partner that can read orders and not refund them. So a key carries a role,
// goes through the same rights the panel shows, and is refused by name when it
// reaches something its role does not carry.
func TestAnAPIKeyCarriesARole(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	created, secret, err := app.APIKeys().Create(ctx, APIKeyInput{
		Name: "  Warehouse robot  ", Role: RoleStaff,
	}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Name != "Warehouse robot" {
		t.Errorf("name = %q, want it trimmed", created.Name)
	}
	if created.Role != RoleStaff {
		t.Errorf("role = %q", created.Role)
	}
	// The secret is handed back exactly once and never stored, so the row the
	// panel lists afterwards cannot leak it.
	if !strings.HasPrefix(secret, apiKeyScheme) {
		t.Errorf("secret = %q, want it to announce what it is", secret)
	}
	if created.Prefix == "" || !strings.Contains(secret, created.Prefix) {
		t.Errorf("prefix %q is not in %q; the panel could not identify the key", created.Prefix, secret)
	}

	rows, err := app.APIKeys().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("keys = %d, want 1", len(rows))
	}
	// Nothing on a listed key can be turned back into the credential.
	if strings.Contains(strings.Join([]string{rows[0].Name, rows[0].Prefix}, " "), secret) {
		t.Error("the listing carries the secret")
	}

	// It authenticates, as somebody with staff's rights rather than as the
	// roleless static token.
	who, ok := app.APIKeys().Resolve(ctx, secret)
	if !ok {
		t.Fatal("the key it just made does not authenticate")
	}
	if who.Role != RoleStaff {
		t.Errorf("resolved role = %q, want staff", who.Role)
	}
	if !who.Has(RightCatalogRead) {
		t.Error("staff should read the catalogue")
	}
	if who.Has(RightRolesWrite) {
		t.Error("staff must not carry roles.write; the key would outrank the person who made it")
	}
}

// A revoked key stops working, and stays in the list so there is a record of
// what it was and when it stopped.
func TestARevokedKeyStopsWorkingAndIsStillListed(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	key, secret, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "Old integration", Role: RoleStaff}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := app.APIKeys().Resolve(ctx, secret); !ok {
		t.Fatal("a fresh key should work")
	}

	if err := app.APIKeys().Revoke(ctx, key.ID, nil); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok := app.APIKeys().Resolve(ctx, secret); ok {
		t.Error("a revoked key still authenticates")
	}

	rows, err := app.APIKeys().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RevokedAt == nil {
		t.Fatalf("listing = %+v, want the key still there and marked revoked", rows)
	}
}

// Nonsense does not authenticate, and neither does a well-formed key whose
// secret is wrong — the prefix alone is not a credential.
func TestAWrongKeyIsRefused(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	key, secret, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "Real", Role: RoleManager}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"", "nonsense", apiKeyScheme + "short",
		// The right prefix, the wrong secret: this is the attack the hash is
		// there to stop, because the prefix is stored in plain text.
		apiKeyScheme + key.Prefix + "_" + strings.Repeat("a", 32),
		// The right secret with a letter changed.
		secret[:len(secret)-1] + "X",
	} {
		if _, ok := app.APIKeys().Resolve(ctx, bad); ok {
			t.Errorf("%q authenticated", bad)
		}
	}
}

// The role has to be one the store has, or a key would be created carrying
// nothing and refused everywhere with no explanation.
func TestAKeyNeedsANameAndARealRole(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	if _, _, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "", Role: RoleStaff}, nil); err == nil {
		t.Error("a key with no name should be refused: the list would be unreadable")
	}
	if _, _, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "x", Role: "wizard"}, nil); err == nil {
		t.Error("a key with a role this store does not have should be refused")
	}
}

// Over HTTP: a key authenticates the admin API and is held to its role, which
// is the whole point of it existing beside the static token.
func TestAnAPIKeyIsHeldToItsRoleOverHTTP(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	_, secret, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "Partner", Role: RoleStaff}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Something staff carries.
	rec := do(t, app, http.MethodGet, "/api/admin/products", header("Authorization", "Bearer "+secret))
	if rec.Code != http.StatusOK {
		t.Fatalf("reading the catalogue with a staff key = %d: %s", rec.Code, rec.Body)
	}

	// Something staff does not, refused by name so whoever set the role knows
	// what to grant.
	rec = do(t, app, http.MethodGet, "/api/admin/roles", header("Authorization", "Bearer "+secret))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reading roles with a staff key = %d, want 403: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), string(RightRolesWrite)) {
		t.Errorf("the refusal does not name the right: %s", rec.Body)
	}
}

// The keys screen is itself behind a right, and it is not one staff has: a key
// that can make keys can make one that outranks it.
func TestMakingKeysIsOwnersWork(t *testing.T) {
	app := newTestApp(t)

	if !defaultRightsHas(RoleOwner, RightAPIKeysWrite) {
		t.Error("owner should be able to make API keys")
	}
	for _, role := range []string{RoleManager, RoleStaff} {
		if defaultRightsHas(role, RightAPIKeysWrite) {
			t.Errorf("%s carries apikeys.write by default; a key could then mint one with more rights than its maker", role)
		}
	}
	_ = app
}

func defaultRightsHas(role string, right Right) bool {
	for _, r := range roleRights[role] {
		if r == right {
			return true
		}
	}
	return false
}
