package meilisearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// fakeMeili is enough of Meilisearch's API to prove the sync and the search
// proxy: documents in, ids out, a search that matches on the title.
type fakeMeili struct {
	mu       sync.Mutex
	docs     map[string]map[string]any
	settings map[string]any
	keys     []string
	created  int
}

func (f *fakeMeili) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, r.Header.Get("Authorization"))
	body, _ := io.ReadAll(r.Body)
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/indexes":
		f.created++
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"taskUid":1,"status":"enqueued"}`))
	case r.Method == http.MethodPatch && path == "/indexes/products/settings":
		_ = json.Unmarshal(body, &f.settings)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"taskUid":2}`))
	case r.Method == http.MethodPost && path == "/indexes/products/documents":
		var docs []map[string]any
		_ = json.Unmarshal(body, &docs)
		for _, d := range docs {
			f.docs[jsonID(d["id"])] = d
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"taskUid":3}`))
	case r.Method == http.MethodPost && path == "/indexes/products/documents/delete-batch":
		var ids []string
		_ = json.Unmarshal(body, &ids)
		for _, id := range ids {
			delete(f.docs, id)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"taskUid":4}`))
	case r.Method == http.MethodGet && path == "/indexes/products/documents":
		var results []map[string]any
		for id, d := range f.docs {
			results = append(results, map[string]any{"id": json.Number(id), "store_id": d["store_id"]})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results, "total": len(results)})
	case r.Method == http.MethodPost && path == "/indexes/products/search":
		var q struct {
			Q      string `json:"q"`
			Filter any    `json:"filter"`
		}
		_ = json.Unmarshal(body, &q)
		// Only the one filter this module always sends is understood: the
		// store's own documents.
		filter, _ := json.Marshal(q.Filter)
		var hits []map[string]any
		for _, d := range f.docs {
			if store, _ := d["store_id"].(string); !strings.Contains(string(filter), `store_id = \"`+store+`\"`) {
				continue
			}
			if title, _ := d["title"].(string); strings.Contains(strings.ToLower(title), strings.ToLower(q.Q)) {
				hits = append(hits, d)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": hits, "estimatedTotalHits": len(hits), "processingTimeMs": 1})
	default:
		http.Error(w, `{"message":"no such route","code":"not_found"}`, http.StatusNotFound)
	}
}

// jsonID is a document id as a string, whatever the decoder made of it.
func jsonID(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case json.Number:
		return n.String()
	case string:
		return n
	}
	return ""
}

func newFake() *fakeMeili { return &fakeMeili{docs: map[string]map[string]any{}} }

func enable(t *testing.T, app *gocommerce.App, host string) {
	t.Helper()
	on := true
	if _, err := app.Plugins().Update(context.Background(), pluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"host": host, "api_key": "master-key", "search_key": "search-key"},
	}); err != nil {
		t.Fatalf("enable: %v", err)
	}
}

func TestTheCatalogueIsSyncedAndSearched(t *testing.T) {
	fake := newFake()
	server := gctest.StubHTTP(t, fake.handler)
	m := New(Config{Poll: -1})
	app := gctest.New(t, m)
	ctx := context.Background()

	widget := gctest.CreateProduct(t, app, "MS-WIDGET", 1999, 5)
	gctest.CreateProduct(t, app, "MS-GADGET", 2999, 0)
	draft, err := app.Products().CreateProduct(ctx, gocommerce.ProductInput{Title: "Secret draft", SKU: "MS-DRAFT", PriceMinor: ptr(int64(100)), Status: "draft"})
	if err != nil {
		t.Fatalf("draft: %v", err)
	}

	// Off: nothing is sent, and the search door is shut.
	if _, _, err := m.Sync(ctx, true); err == nil {
		t.Errorf("a sync without configuration succeeded")
	}
	if rec := gctest.Request(t, app, http.MethodGet, "/x/meilisearch/search?q=widget", nil); rec.Code != http.StatusNotFound {
		t.Errorf("search while off = %d, want 404", rec.Code)
	}

	enable(t, app, server.URL)
	indexed, removed, err := m.Sync(ctx, true)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if indexed != 2 || removed != 1 || fake.created != 1 {
		t.Errorf("sync = %d indexed, %d removed, %d index creations; want the two active products, the draft dropped, one index", indexed, removed, fake.created)
	}
	doc := fake.docs[jsonID(float64(widget.ID))]
	if doc == nil || doc["title"] != widget.Title || doc["price_minor"] != float64(1999) || doc["in_stock"] != true {
		t.Errorf("widget document = %v", doc)
	}
	if _, there := fake.docs[jsonID(float64(draft.ID))]; there {
		t.Errorf("the draft reached the index")
	}
	if attrs, _ := fake.settings["filterableAttributes"].([]any); len(attrs) == 0 {
		t.Errorf("index settings were not set: %v", fake.settings)
	}
	// Writes used the API key; the storefront's search uses the search key.
	if !strings.Contains(strings.Join(fake.keys, " "), "Bearer master-key") {
		t.Errorf("keys sent = %v", fake.keys)
	}

	rec := gctest.Request(t, app, http.MethodGet, "/x/meilisearch/search?q=widget&limit=5", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body)
	}
	var result searchResult
	gctest.DecodeData(t, rec, &result)
	if result.Total != 1 || len(result.Hits) != 1 || result.Limit != 5 {
		t.Errorf("result = %+v", result)
	}
	if last := fake.keys[len(fake.keys)-1]; last != "Bearer search-key" {
		t.Errorf("the search used %q, want the search-only key", last)
	}

	// A product archived since is removed on the next incremental sync; an
	// untouched one is not sent again.
	archived := "archived"
	if _, err := app.Products().UpdateProduct(ctx, widget.ID, gocommerce.ProductPatch{Status: &archived}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	indexed, removed, err = m.Sync(ctx, false)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if indexed != 0 || removed != 1 {
		t.Errorf("incremental sync = %d indexed, %d removed; want only the archived product removed", indexed, removed)
	}
	if _, there := fake.docs[jsonID(float64(widget.ID))]; there {
		t.Errorf("the archived product is still in the index")
	}

	status := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/meilisearch/status", nil)
	var st Status
	gctest.DecodeData(t, status, &st)
	if !st.Enabled || !st.Configured || st.Indexed != 2 || st.Removed != 2 || st.LastSync == nil {
		t.Errorf("status = %+v", st)
	}
	reindex := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/meilisearch/reindex", nil)
	if reindex.Code != http.StatusOK {
		t.Errorf("reindex = %d: %s", reindex.Code, reindex.Body)
	}
}

func TestMeilisearchRoutesAreDocumentedAndGated(t *testing.T) {
	app := gctest.New(t, New(Config{Poll: -1}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "meilisearch")
	gctest.AssertSpecCoversModuleRoutes(t, app, "meilisearch")
	if rec := gctest.Request(t, app, http.MethodPost, "/api/admin/x/meilisearch/reindex", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("reindex without a token = %d", rec.Code)
	}
}

func ptr[T any](v T) *T { return &v }

// One engine serves every store and every store shares the one index, each
// document carrying its store: a search sees only the asking store's
// products, and one store's full sync removes only its own stale documents.
func TestStoresShareOneIndexEachSeeingItsOwn(t *testing.T) {
	fake := &fakeMeili{docs: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	mod := New(Config{Host: srv.URL, APIKey: "master", SearchKey: "search", Poll: -1})
	app := gctest.New(t, mod)
	b, err := app.Stores().Create(context.Background(), "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	ctxA, ctxB := context.Background(), gocommerce.WithStore(context.Background(), b.ID)
	price := int64(1000)
	for _, c := range []struct {
		ctx   context.Context
		title string
	}{{ctxA, "Alpha mug"}, {ctxB, "Beta mug"}} {
		on := true
		if _, err := app.Plugins().Update(c.ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on}); err != nil {
			t.Fatal(err)
		}
		if _, err := app.Products().CreateProduct(c.ctx, gocommerce.ProductInput{Title: c.title, Status: "active", PriceMinor: &price}); err != nil {
			t.Fatal(err)
		}
		conn, err := mod.connection(c.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if conn.index != defaultIndex {
			t.Fatalf("index = %q, want every store in %q", conn.index, defaultIndex)
		}
		if _, _, err := mod.Sync(c.ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.docs) != 2 {
		t.Fatalf("index holds %d documents after both stores synced fully, want 2: one store's sync removed the other's", len(fake.docs))
	}
	for _, c := range []struct {
		store, want, not string
	}{{"", "Alpha mug", "Beta mug"}, {b.ID, "Beta mug", "Alpha mug"}} {
		target := "/x/meilisearch/search?q=mug"
		if c.store != "" {
			target += "&store=" + c.store
		}
		rec := gctest.Request(t, app, http.MethodGet, target, nil)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, c.want) || strings.Contains(body, c.not) {
			t.Fatalf("search for store %q = %d %s; want %q and not %q", c.store, rec.Code, body, c.want, c.not)
		}
	}
	mod.stateFor(ctxA).lastSync = time.Time{}
	if mod.stateFor(ctxB).lastSync.IsZero() {
		t.Fatal("store B's sync time went with store A's")
	}
}
