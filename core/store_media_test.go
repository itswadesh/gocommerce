package gocommerce

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Each store's files live under stores/<store id>/, so a store's files can be
// found, and removed with it, without reading anybody else's.

func TestAStoresUploadsLiveUnderItsOwnFolder(t *testing.T) {
	app, dir := mediaApp(t)
	store := app.DefaultStore()
	item, err := app.MediaLibrary().Upload(WithStore(context.Background(), store), "photo.png", "image/png",
		strings.NewReader("pixels"), 6)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(item.URL, "/media/stores/"+store+"/") || !strings.HasSuffix(item.URL, ".png") {
		t.Fatalf("url = %q, want /media/stores/%s/<random>.png", item.URL, store)
	}
	if _, err := os.Stat(filepath.Join(dir, "stores", store, filepath.Base(item.URL))); err != nil {
		t.Fatalf("the file is not in the store's folder: %v", err)
	}
	rec := do(t, app, http.MethodGet, item.URL)
	if rec.Code != http.StatusOK || rec.Body.String() != "pixels" {
		t.Fatalf("GET %s = %d %q", item.URL, rec.Code, rec.Body)
	}
	// Nothing but a store id and a flat name is served from under stores/.
	for _, bad := range []string{"/media/stores/not-a-store/x.png", "/media/stores/" + store + "/..%2Fx.png"} {
		if rec := do(t, app, http.MethodGet, bad); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", bad, rec.Code)
		}
	}
}

func TestDeletingAStoreTakesItsFilesAndOnlyItsFiles(t *testing.T) {
	app, dir := mediaApp(t)
	keep, err := app.MediaLibrary().Upload(WithStore(context.Background(), app.DefaultStore()), "keep.png", "image/png",
		strings.NewReader("keep"), 4)
	if err != nil {
		t.Fatal(err)
	}
	doomed := addTestStore(t, app, "doomed")
	gone, err := app.MediaLibrary().Upload(WithStore(context.Background(), doomed), "gone.png", "image/png",
		strings.NewReader("gone"), 4)
	if err != nil {
		t.Fatal(err)
	}
	onDisk := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(gone.URL, "/media/")))
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("the upload is not where its URL says: %v", err)
	}
	if err := app.Stores().Delete(context.Background(), doomed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(onDisk); !os.IsNotExist(err) {
		t.Fatalf("the deleted store's file is still on disk (%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stores", doomed)); !os.IsNotExist(err) {
		t.Fatalf("the deleted store's folder is still on disk (%v)", err)
	}
	if rec := do(t, app, http.MethodGet, keep.URL); rec.Code != http.StatusOK {
		t.Fatalf("the other store's file went too: %d", rec.Code)
	}
}

// An install that holds no files (no media directory, no media store) may
// still have rows naming files from when it did; deleting the store must not
// trip over the store that is not there.
func TestDeletingAStoreWithoutAMediaStore(t *testing.T) {
	app := newTestApp(t)
	doomed := addTestStore(t, app, "doomed")
	if _, err := app.db.ExecContext(WithStore(context.Background(), doomed),
		`INSERT INTO media (kind, url, storage_key) VALUES ('image', '/media/old.png', 'old.png')`); err != nil {
		t.Fatal(err)
	}
	if err := app.Stores().Delete(context.Background(), doomed); err != nil {
		t.Fatal(err)
	}
}
