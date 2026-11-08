package gocommerce

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSendGA(t *testing.T) {
	var got []map[string]any
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Errorf("body is not JSON: %s", body)
		}
		got = append(got, m)
		query = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	ctx := context.Background()

	for _, cfg := range []Config{
		{GAEndpoint: srv.URL},
		{GAEndpoint: srv.URL, GAMeasurementID: "G-TEST"},
		{GAEndpoint: srv.URL, GAAPISecret: "s"},
	} {
		if sendGA(ctx, cfg, "su:1", "sign_up", nil) {
			t.Errorf("sent with %+v", cfg)
		}
	}
	if len(got) != 0 {
		t.Fatalf("an unset config reached the server %d times", len(got))
	}

	cfg := Config{GAEndpoint: srv.URL, GAMeasurementID: "G-TEST", GAAPISecret: "s"}
	if !sendGA(ctx, cfg, "su:1", "sign_up", map[string]any{"method": "email"}) {
		t.Fatal("not sent")
	}
	if query != "api_secret=s&measurement_id=G-TEST" {
		t.Errorf("query = %q", query)
	}
	want := map[string]any{
		"client_id": gaClientID("su:1"),
		"events": []any{map[string]any{
			"name":   "sign_up",
			"params": map[string]any{"method": "email", "engagement_time_msec": float64(1)},
		}},
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Errorf("body = %v, want %v", got, want)
	}
}
