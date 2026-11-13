package ads

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// platforms is a stub of Google's token and Ads endpoints and Meta's Graph
// API, answering as their documentation says they answer.
type platforms struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	fail     bool
}

func (p *platforms) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.requests = append(p.requests, r)
	p.bodies = append(p.bodies, string(body))
	fail := p.fail
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Host == "oauth2.googleapis.com":
		_, _ = io.WriteString(w, `{"access_token":"ya29.stub","expires_in":3599,"token_type":"Bearer"}`)
	case r.Host == "googleads.googleapis.com" && fail:
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":403,"message":"The developer token is only approved for use with test accounts.","status":"PERMISSION_DENIED"}}`)
	case r.Host == "googleads.googleapis.com" && !strings.Contains(string(body), `"pageToken":"p2"`):
		day := time.Now().UTC().Format(time.DateOnly)
		fmt.Fprintf(w, `{"results":[
		  {"customer":{"resourceName":"customers/1234567890","currencyCode":"INR"},"campaign":{"resourceName":"customers/1234567890/campaigns/1","name":"Diwali"},"metrics":{"costMicros":"1234565000"},"segments":{"date":%q}}
		],"nextPageToken":"p2","fieldMask":"segments.date,campaign.name,customer.currencyCode,metrics.costMicros"}`, day)
	case r.Host == "googleads.googleapis.com":
		day := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
		fmt.Fprintf(w, `{"results":[
		  {"customer":{"currencyCode":"INR"},"campaign":{"name":"Brand"},"metrics":{"costMicros":"500000000"},"segments":{"date":%q}}
		]}`, day)
	case r.Host == "graph.facebook.com" && r.URL.Query().Get("after") == "":
		day := time.Now().UTC().Format(time.DateOnly)
		fmt.Fprintf(w, `{"data":[{"campaign_name":"Retarget","spend":"400.5","account_currency":"INR","date_start":%q,"date_stop":%q}],
		  "paging":{"cursors":{"after":"c2"},"next":"https://graph.facebook.com/v23.0/act_42/insights?after=c2&access_token=EAAB"}}`, day, day)
	case r.Host == "graph.facebook.com":
		_, _ = io.WriteString(w, `{"data":[],"paging":{}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newSyncHarness(t *testing.T) (*harness, *platforms) {
	t.Helper()
	p := &platforms{}
	return newStubHarness(t, p.serve), p
}

// newStubHarness is a store whose calls to the ad platforms reach serve,
// with the Host still the platform's so serve knows who was called.
func newStubHarness(t *testing.T, serve http.HandlerFunc) *harness {
	t.Helper()
	srv := httptest.NewServer(serve)
	t.Cleanup(srv.Close)
	to, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		out := r.Clone(r.Context())
		out.URL.Scheme, out.URL.Host = to.Scheme, to.Host
		// Host stays the platform's, so the stub knows who was called.
		return http.DefaultTransport.RoundTrip(out)
	})}
	mod := New(Config{Client: client})
	app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "INR"}, mod)
	return &harness{t: t, app: app, mod: mod}
}

func (h *harness) plugin(key string, settings map[string]any) {
	h.t.Helper()
	rec := gctest.AdminRequest(h.t, h.app, http.MethodPatch, "/api/admin/plugins/"+key, map[string]any{"enabled": true, "settings": settings})
	if rec.Code != http.StatusOK {
		h.t.Fatalf("switch on %s = %d %s", key, rec.Code, rec.Body)
	}
}

func (h *harness) connectors() []Connector {
	h.t.Helper()
	var out []Connector
	gctest.DecodeData(h.t, gctest.AdminRequest(h.t, h.app, http.MethodGet, "/api/admin/x/ads/connectors", nil), &out)
	return out
}

func TestConnectorsDoNothingUntilFilledIn(t *testing.T) {
	h, p := newSyncHarness(t)
	cs := h.connectors()
	if len(cs) != 2 || cs[0].Enabled || len(cs[0].Missing) != 5 || cs[0].Last != nil || len(cs[1].Missing) != 2 {
		t.Fatalf("connectors %+v", cs)
	}
	if err := h.mod.syncAll(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/connectors/google/sync", nil); rec.Code != http.StatusConflict {
		t.Errorf("sync with nothing set = %d %s", rec.Code, rec.Body)
	}
	if len(p.requests) != 0 {
		t.Errorf("%d calls with no credentials", len(p.requests))
	}
}

func TestGoogleSpendIsReadIntoTheSpendRows(t *testing.T) {
	h, p := newSyncHarness(t)
	h.plugin(GooglePluginKey, map[string]any{"developer_token": "dev-tok", "client_id": "cid", "client_secret": "csecret",
		"refresh_token": "rtok", "customer_id": "123-456-7890", "login_customer_id": "111-222-3333"})
	rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/connectors/google/sync", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync = %d %s", rec.Code, rec.Body)
	}
	var run SyncRun
	gctest.DecodeData(t, rec, &run)
	if !run.OK || run.Days != 2 || run.Campaigns != 2 {
		t.Errorf("run %+v", run)
	}
	// The token request, then two pages of the search.
	if len(p.requests) != 3 || !strings.Contains(p.bodies[0], "refresh_token=rtok") || !strings.Contains(p.bodies[0], "grant_type=refresh_token") {
		t.Fatalf("token call %d requests, body %q", len(p.requests), p.bodies)
	}
	s := p.requests[1]
	if s.URL.Path != "/v22/customers/1234567890/googleAds:search" || s.Header.Get("developer-token") != "dev-tok" ||
		s.Header.Get("Authorization") != "Bearer ya29.stub" || s.Header.Get("login-customer-id") != "1112223333" {
		t.Errorf("search %s %v", s.URL.Path, s.Header)
	}
	var q map[string]string
	_ = json.Unmarshal([]byte(p.bodies[1]), &q)
	if !strings.Contains(q["query"], "metrics.cost_micros") || !strings.Contains(q["query"], "segments.date BETWEEN") {
		t.Errorf("query %q", q["query"])
	}
	// The same rows a CSV import writes: origin google, under the ads' utm values.
	var r Report
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/ads/report?tz=UTC&to="+today()+
		"&from="+time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly), nil), &r)
	if d := row(t, r, "INR", "google", "Diwali"); d.Spend == nil || d.Spend.AmountMinor != 123457 {
		t.Errorf("Diwali spend %+v, want ₹1,234.57 rounded from micros", d.Spend)
	}
	if b := row(t, r, "INR", "google", "Brand"); b.Spend == nil || b.Spend.AmountMinor != 50000 || b.Medium != "cpc" {
		t.Errorf("Brand %+v", b)
	}
	var origins []Spend
	gctest.DecodeData(t, gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/ads/spend?from="+time.Now().UTC().AddDate(0, 0, -7).Format(time.DateOnly)+"&to="+today(), nil), &origins)
	if len(origins) != 2 {
		t.Errorf("%d spend rows, want the two read", len(origins))
	}
	for _, s := range origins {
		if s.Origin != "google" {
			t.Errorf("origin %q", s.Origin)
		}
	}

	// A refusal is kept and shown, and the rows already read stay.
	p.fail = true
	gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/connectors/google/sync", nil)
	cs := h.connectors()
	if cs[0].Last == nil || cs[0].Last.OK || !strings.Contains(cs[0].Last.Message, "only approved for use with test accounts") {
		t.Errorf("failed run %+v", cs[0].Last)
	}

	// The daily job does not run again within the day after a good run.
	p.fail = false
	gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/connectors/google/sync", nil)
	n := len(p.requests)
	if err := h.mod.syncAll(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != n {
		t.Errorf("the job ran again %d calls after a good run an instant ago", len(p.requests)-n)
	}
}

func TestMetaSpendIsReadIntoTheSpendRows(t *testing.T) {
	h, p := newSyncHarness(t)
	h.plugin(MetaPluginKey, map[string]any{"access_token": "EAAB", "app_secret": "shh", "ad_account_id": "act_42"})
	rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/ads/connectors/meta/sync", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync = %d %s", rec.Code, rec.Body)
	}
	first := p.requests[0]
	q := first.URL.Query()
	if first.URL.Path != "/v23.0/act_42/insights" || q.Get("level") != "campaign" || q.Get("time_increment") != "1" ||
		q.Get("access_token") != "EAAB" || q.Get("appsecret_proof") == "" || !strings.Contains(q.Get("time_range"), `"since"`) {
		t.Errorf("insights call %s", first.URL)
	}
	if len(p.requests) != 2 {
		t.Errorf("%d calls, want the second page followed", len(p.requests))
	}
	r := h.report("")
	if d := row(t, r, "INR", "facebook", "Retarget"); d.Spend == nil || d.Spend.AmountMinor != 40050 || d.Medium != "paid" {
		t.Errorf("Retarget %+v", d)
	}
}

func TestMicros(t *testing.T) {
	for _, c := range []struct {
		v    int64
		exp  int
		want int64
	}{{1234565000, 2, 123457}, {1234564999, 2, 123456}, {1500000, 0, 2}, {1000, 3, 1}} {
		if got := micros(c.v, c.exp); got != c.want {
			t.Errorf("micros(%d, %d) = %d, want %d", c.v, c.exp, got, c.want)
		}
	}
}
