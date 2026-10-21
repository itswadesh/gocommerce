package gocommerce

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The copy of an API key the owner can read again (M46) is sealed like any
// other secret the engine reads back. The hash beside it, which is what
// authenticates, is untouched.

func storedAPIKeySecret(t *testing.T, app *App, id int64) string {
	t.Helper()
	var s string
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT secret FROM api_keys WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAnAPIKeysCopyIsStoredSealed(t *testing.T) {
	app := keyedApp(t, keyedDSN(t), newSecretsKey(t), "")
	ctx := context.Background()

	key, presented, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "Warehouse", Role: RoleStaff}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stored := storedAPIKeySecret(t, app, key.ID); !strings.HasPrefix(stored, "sealed:v1:") || strings.Contains(stored, presented) {
		t.Errorf("stored copy = %q, want it sealed", stored)
	}
	again, err := app.APIKeys().Secret(ctx, key.ID)
	if err != nil || again != presented {
		t.Errorf("read back = %q, %v; want the key that was issued", again, err)
	}
	if _, ok := app.APIKeys().Resolve(ctx, presented); !ok {
		t.Error("a key whose copy is sealed no longer authenticates")
	}
}

func TestWithoutAKeyAnAPIKeysCopyIsStoredAsBefore(t *testing.T) {
	app := newTestApp(t)
	key, presented, err := app.APIKeys().Create(context.Background(), APIKeyInput{Name: "Plain", Role: RoleStaff}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stored := storedAPIKeySecret(t, app, key.ID); stored != presented {
		t.Errorf("stored without a key = %q, want the key as issued", stored)
	}
}

// A copy sealed under a key the install no longer has cannot be shown, and
// says why. The key itself still works: the hash decides that.
func TestAnAPIKeysCopyUnderAnUnknownKeyCannotBeShown(t *testing.T) {
	dsn := keyedDSN(t)
	ctx := context.Background()
	first := keyedApp(t, dsn, newSecretsKey(t), "")
	key, presented, err := first.APIKeys().Create(ctx, APIKeyInput{Name: "Old", Role: RoleStaff}, nil)
	if err != nil {
		t.Fatal(err)
	}

	lost := keyedApp(t, dsn, newSecretsKey(t), "")
	got, err := lost.APIKeys().Secret(ctx, key.ID)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || !strings.Contains(err.Error(), "revoke it") {
		t.Errorf("read back = %q, %v; want a 409 that says what to do", got, err)
	}
	if got != "" {
		t.Errorf("an unreadable copy came back as %q", got)
	}
	if _, ok := lost.APIKeys().Resolve(ctx, presented); !ok {
		t.Error("the key stopped authenticating because its copy cannot be opened")
	}
}
