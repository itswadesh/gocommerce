package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A shop's logo, its logo for dark backgrounds and its icon are pictures from
// its own media library, so one storefront serving many stores can show each
// its own.
func TestAStoreShowsItsOwnLogo(t *testing.T) {
	app, _ := mediaApp(t)
	ctx := context.Background()

	// A store that has chosen none says so, and the public answer carries no
	// address: a storefront then keeps its own.
	fresh, err := app.Profile().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.LogoMediaID != nil || fresh.LogoURL != "" || fresh.IconURL != "" {
		t.Errorf("a fresh store = %+v, want no pictures", fresh)
	}
	if got := publicStoreOf(t, app); got.LogoURL != "" || got.LogoDarkURL != "" || got.IconURL != "" {
		t.Errorf("public store with no logo = %+v", got)
	}

	logo, err := app.MediaLibrary().Upload(ctx, "logo.png", "image/png", strings.NewReader("png"), 3)
	if err != nil {
		t.Fatal(err)
	}
	icon, err := app.MediaLibrary().Upload(ctx, "icon.png", "image/png", strings.NewReader("png"), 3)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := app.Profile().Set(ctx, StoreProfile{Name: "Corner", LogoMediaID: &logo.ID, IconMediaID: &icon.ID}, nil)
	if err != nil {
		t.Fatalf("set the logo: %v", err)
	}
	if saved.LogoURL != logo.URL || saved.IconURL != icon.URL || saved.LogoDarkURL != "" {
		t.Errorf("saved = logo %q icon %q dark %q, want %q and %q", saved.LogoURL, saved.IconURL, saved.LogoDarkURL, logo.URL, icon.URL)
	}
	// The store's pictures are in use: not offered as unused files, and not
	// deleted out from under every storefront page.
	unused, _, err := app.MediaLibrary().List(ctx, MediaQuery{Usage: UsedNowhere})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range unused {
		if it.ID == logo.ID || it.ID == icon.ID {
			t.Errorf("the store's %s is listed as unused", it.Filename)
		}
	}
	if err := app.MediaLibrary().Delete(ctx, logo.ID); err == nil {
		t.Error("the store's logo was deleted")
	}
	pub := publicStoreOf(t, app)
	if pub.LogoURL != logo.URL || pub.IconURL != icon.URL {
		t.Errorf("public store = %+v, want the logo and icon", pub)
	}

	// A save that does not mention the pictures keeps them: the details form
	// and a script that predates them must not wipe a logo.
	kept, err := app.Profile().Set(ctx, StoreProfile{Name: "Corner Shop"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if kept.LogoURL != logo.URL {
		t.Errorf("a save without the logo dropped it: %q", kept.LogoURL)
	}

	// Zero removes one, and the trail says who changed the shop's face.
	zero := int64(0)
	removed, err := app.Profile().Set(ctx, StoreProfile{Name: "Corner Shop", LogoMediaID: &zero}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if removed.LogoMediaID != nil || removed.LogoURL != "" || removed.IconURL != icon.URL {
		t.Errorf("after removing the logo = %+v", removed)
	}
	rows, _, err := app.Audit().List(ctx, AuditQuery{Action: AuditStoreSetImages})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("audit rows = %d, want one per change (2)", len(rows))
	}
}

func TestAStoreLogoMustBeAnImageOfItsOwn(t *testing.T) {
	app, _ := mediaApp(t)
	ctx := context.Background()

	video, err := app.MediaLibrary().AddURL(ctx, "https://cdn.example/clip.mp4", MediaVideo, "")
	if err != nil {
		t.Fatal(err)
	}
	huge, err := app.MediaLibrary().Upload(ctx, "huge.png", "image/png", strings.NewReader("png"), MaxStoreImageBytes+1)
	if err != nil {
		t.Fatal(err)
	}
	missing := int64(987654)
	for name, in := range map[string]StoreProfile{
		"a video":                    {LogoMediaID: &video.ID},
		"a picture too big to serve": {IconMediaID: &huge.ID},
		"a file that is not there":   {LogoDarkMediaID: &missing},
	} {
		if _, err := app.Profile().Set(ctx, in, nil); err == nil {
			t.Errorf("%s was accepted as a store picture", name)
		}
	}

	// Through the route too, as a 400 a panel can show.
	rec := do(t, app, http.MethodPatch, "/api/admin/store", withAdmin,
		jsonBody(t, map[string]any{"logo_media_id": video.ID}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a video as the logo = %d, want 400: %s", rec.Code, rec.Body)
	}
}

func publicStoreOf(t *testing.T, app *App) publicStore {
	t.Helper()
	rec := do(t, app, http.MethodGet, "/api/store")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/store = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Data publicStore `json:"data"`
	}
	decodeJSONBody(t, rec.Body.Bytes(), &body)
	return body.Data
}
