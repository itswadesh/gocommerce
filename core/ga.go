package gocommerce

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The hosted product's funnel, told to Google Analytics by the server through
// the Measurement Protocol. Server-side because the panel is locked to its own
// origin by its CSP, and a tag in it would read every operator's screens.
//
// Nothing personal is sent: the client id is a hash of an internal id, and the
// only parameter is how somebody signed up. Fire-and-forget, so a slow or
// failing Google never touches the request that caused the event.

const gaEndpoint = "https://www.google-analytics.com/mp/collect"

// gaClientID is the opaque id GA counts a person or store by. Hashed so the
// raw row id is not what GA holds.
func gaClientID(key string) string {
	sum := sha256.Sum256([]byte("gocommerce-ga:" + key))
	return hex.EncodeToString(sum[:16])
}

// sendGA posts one event and reports whether it was sent. Either setting empty
// sends nothing.
func sendGA(ctx context.Context, cfg Config, clientKey, name string, params map[string]any) bool {
	if cfg.GAMeasurementID == "" || cfg.GAAPISecret == "" {
		return false
	}
	if params == nil {
		params = map[string]any{}
	}
	// Without engagement_time_msec GA keeps the event out of its realtime and
	// engagement reports.
	params["engagement_time_msec"] = 1
	body, err := json.Marshal(map[string]any{
		"client_id": gaClientID(clientKey),
		"events":    []map[string]any{{"name": name, "params": params}},
	})
	if err != nil {
		return false
	}
	endpoint := cfg.GAEndpoint
	if endpoint == "" {
		endpoint = gaEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		endpoint+"?"+url.Values{"measurement_id": {cfg.GAMeasurementID}, "api_secret": {cfg.GAAPISecret}}.Encode(), bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	// An address the install's owner set, not a store, so an ordinary client
	// (AGENTS.md rule 16).
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 300
}

// trackGA sends an event off the request path. A failure is dropped: an
// analytics miss is not worth a log line or a retry.
func (a *App) trackGA(clientKey, name string, params map[string]any) {
	if a.cfg.GAMeasurementID == "" || a.cfg.GAAPISecret == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		sendGA(ctx, a.cfg, clientKey, name, params)
	}()
}

// trackSignUp is GA4's recommended sign_up, keyed by the new person.
func (a *App) trackSignUp(su *Superuser, method string) {
	a.trackGA("su:"+strconv.FormatInt(su.ID, 10), "sign_up", map[string]any{"method": method})
}

// trackFirstProduct sends first_product_added when the product just made is the
// store's first that is not a sample. ctx is the request's, bound to the store.
// ponytail: two products created in the same instant can both count two and
// send nothing; a column recording the event would close that, if it matters.
func (a *App) trackFirstProduct(ctx context.Context) {
	if a.cfg.GAMeasurementID == "" || a.cfg.GAAPISecret == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var n int
		if err := a.db.QueryRowContext(ctx, `SELECT count(*) FROM products WHERE NOT sample`).Scan(&n); err != nil || n != 1 {
			return
		}
		store, _ := StoreFrom(ctx)
		key := "store:" + store
		if su := SuperuserFrom(ctx); su != nil && su.ID != 0 {
			key = "su:" + strconv.FormatInt(su.ID, 10)
		}
		sendGA(ctx, a.cfg, key, "first_product_added", nil)
	}()
}
