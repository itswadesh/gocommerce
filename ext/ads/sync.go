package ads

// The spend connectors (D267): Google Ads and Meta read each day's spend per
// campaign from the platform's own API and write the same ads_spend rows a
// CSV import writes, so the report stays current without anyone downloading
// a file. They only read. Neither has been run against a real ad account:
// both are written to the platforms' documented APIs and tested against stub
// servers, and the panel says so.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

// The plugins that hold each platform's credentials, sealed (D110).
const (
	GooglePluginKey = "google-ads-spend"
	MetaPluginKey   = "meta-ads-spend"
)

// syncDays is how far back each run reads: platforms restate recent days as
// clicks are judged invalid and billing settles, so a week is read again.
const syncDays = 7

// syncEvery is how long after a good run the next one is due.
const syncEvery = 20 * time.Hour

type googleConfig struct {
	DeveloperToken  string `plugin:"developer_token"`
	ClientID        string `plugin:"client_id"`
	ClientSecret    string `plugin:"client_secret"`
	RefreshToken    string `plugin:"refresh_token"`
	CustomerID      string `plugin:"customer_id"`
	LoginCustomerID string `plugin:"login_customer_id"`
	Source          string `plugin:"utm_source"`
	Medium          string `plugin:"utm_medium"`
	APIVersion      string `plugin:"api_version"`
}

type metaConfig struct {
	AppID       string `plugin:"app_id"`
	AppSecret   string `plugin:"app_secret"`
	AccessToken string `plugin:"access_token"`
	AdAccountID string `plugin:"ad_account_id"`
	Source      string `plugin:"utm_source"`
	Medium      string `plugin:"utm_medium"`
	APIVersion  string `plugin:"api_version"`
}

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// missing names what a configuration still lacks, in the panel's words.
func (c googleConfig) missing() []string {
	var out []string
	for _, f := range []struct{ v, name string }{
		{c.DeveloperToken, "developer token"}, {c.ClientID, "OAuth client ID"}, {c.ClientSecret, "OAuth client secret"},
		{c.RefreshToken, "refresh token"}, {digits(c.CustomerID), "customer ID"},
	} {
		if strings.TrimSpace(f.v) == "" {
			out = append(out, f.name)
		}
	}
	return out
}

func (c metaConfig) missing() []string {
	var out []string
	if strings.TrimSpace(c.AccessToken) == "" {
		out = append(out, "access token")
	}
	if digits(c.AdAccountID) == "" {
		out = append(out, "ad account ID")
	}
	return out
}

func (m *Module) registerConnectors(app *gocommerce.App) {
	const untested = " Written to the platform's documented API and never yet run against a real account: check the first days it brings in against the platform's own figures."
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: GooglePluginKey, Title: "Google Ads spend", Icon: "ri-google-line", Category: "marketing",
		Description: "Reads each day's cost per campaign from the Google Ads API into the ads report, once a day, and proposes changes from it. It changes nothing in the account until somebody approves a change on the ads changes screen." + untested,
		Docs:        "https://developers.google.com/google-ads/api/docs/get-started/introduction",
		Fields: []gocommerce.PluginField{
			{Key: "developer_token", Label: "Developer token", Kind: "secret", Required: true, Help: "Google Ads › Tools › API Center, in a manager account. Basic access is needed to read a live account."},
			{Key: "client_id", Label: "OAuth client ID", Kind: "text", Required: true, Help: "Google Cloud console › APIs & Services › Credentials, a Desktop or Web OAuth client, with the Google Ads API enabled."},
			{Key: "client_secret", Label: "OAuth client secret", Kind: "secret", Required: true},
			{Key: "refresh_token", Label: "Refresh token", Kind: "secret", Required: true, Help: "From signing in once with the scope https://www.googleapis.com/auth/adwords, for example with the OAuth Playground using your own client. To make approved changes, sign in as a user who can edit the account."},
			{Key: "customer_id", Label: "Customer ID", Kind: "text", Required: true, Help: "The ad account's ID, shown top right in Google Ads, such as 123-456-7890."},
			{Key: "login_customer_id", Label: "Manager account ID", Kind: "text", Help: "Only when you sign in through a manager account: its ID."},
			{Key: "utm_source", Label: "utm_source", Kind: "text", Default: "google", Help: "What these ads put in utm_source, so their spend meets their orders."},
			{Key: "utm_medium", Label: "utm_medium", Kind: "text", Default: "cpc"},
			{Key: "api_version", Label: "API version", Kind: "text", Default: "v22", Help: "Google retires each version after about a year; raise it when Google says so."},
		},
	})
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: MetaPluginKey, Title: "Meta ads spend", Icon: "ri-meta-line", Category: "marketing",
		Description: "Reads each day's spend per campaign from the Meta Marketing API into the ads report, once a day, and proposes changes from it. It changes nothing in the account until somebody approves a change on the ads changes screen." + untested,
		Docs:        "https://developers.facebook.com/docs/marketing-api/insights",
		Fields: []gocommerce.PluginField{
			{Key: "app_id", Label: "App ID", Kind: "text", Help: "developers.facebook.com › My Apps, a Business app with the Marketing API added."},
			{Key: "app_secret", Label: "App secret", Kind: "secret", Help: "The app's Settings › Basic. When given, each call is signed with it (appsecret_proof)."},
			{Key: "access_token", Label: "Access token", Kind: "secret", Required: true, Help: "A system user's token from Business Settings › Users › System users, with ads_read on the ad account, and ads_management too for approved changes to be made."},
			{Key: "ad_account_id", Label: "Ad account ID", Kind: "text", Required: true, Help: "Business Settings › Accounts › Ad accounts; the number, with or without act_."},
			{Key: "utm_source", Label: "utm_source", Kind: "text", Default: "facebook", Help: "What these ads put in utm_source, so their spend meets their orders."},
			{Key: "utm_medium", Label: "utm_medium", Kind: "text", Default: "paid"},
			{Key: "api_version", Label: "API version", Kind: "text", Default: "v23.0", Help: "Meta retires each version about two years on; raise it when Meta says so."},
		},
	})
}

// Connector is one platform's state as the ads screen shows it.
type Connector struct {
	Platform string `json:"platform"`
	Plugin   string `json:"plugin"`
	Title    string `json:"title"`
	Enabled  bool   `json:"enabled"`
	// Missing names the settings still to fill; empty when it can run.
	Missing []string `json:"missing"`
	// Last is the latest run, or null before the first.
	Last *SyncRun `json:"last"`
}

// SyncRun is what one run did.
type SyncRun struct {
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
	Message   string    `json:"message"`
	Days      int       `json:"days"`
	Campaigns int       `json:"campaigns"`
}

type connector struct {
	platform, plugin, title string
	// read loads the configuration: whether its plugin is on, what is
	// missing, the utm_source and utm_medium its spend is kept under, and
	// fetch, which reads the days from..to.
	read func(ctx context.Context) (on bool, missing []string, source, medium string, fetch func(ctx context.Context, from, to string) ([]dayRow, error))
}

// googleSettings is the plugin's state: whether it is on, and its settings
// with their defaults.
func (m *Module) googleSettings(ctx context.Context) (bool, googleConfig) {
	c := googleConfig{Source: "google", Medium: "cpc", APIVersion: "v22"}
	on, _ := m.app.Plugins().Enabled(ctx, GooglePluginKey)
	if on {
		_ = m.app.Plugins().Fill(ctx, GooglePluginKey, &c)
	}
	return on, c
}

func (m *Module) metaSettings(ctx context.Context) (bool, metaConfig) {
	c := metaConfig{Source: "facebook", Medium: "paid", APIVersion: "v23.0"}
	on, _ := m.app.Plugins().Enabled(ctx, MetaPluginKey)
	if on {
		_ = m.app.Plugins().Fill(ctx, MetaPluginKey, &c)
	}
	return on, c
}

// googleConfig is the settings when the plugin is on and filled in.
func (m *Module) googleConfig(ctx context.Context) (googleConfig, bool) {
	on, c := m.googleSettings(ctx)
	return c, on && len(c.missing()) == 0
}

func (m *Module) metaConfig(ctx context.Context) (metaConfig, bool) {
	on, c := m.metaSettings(ctx)
	return c, on && len(c.missing()) == 0
}

func (m *Module) connectors() []connector {
	return []connector{
		{"google", GooglePluginKey, "Google Ads", func(ctx context.Context) (bool, []string, string, string, func(context.Context, string, string) ([]dayRow, error)) {
			on, c := m.googleSettings(ctx)
			return on, c.missing(), c.Source, c.Medium, func(ctx context.Context, from, to string) ([]dayRow, error) { return m.googleSpend(ctx, c, from, to) }
		}},
		{"meta", MetaPluginKey, "Meta", func(ctx context.Context) (bool, []string, string, string, func(context.Context, string, string) ([]dayRow, error)) {
			on, c := m.metaSettings(ctx)
			return on, c.missing(), c.Source, c.Medium, func(ctx context.Context, from, to string) ([]dayRow, error) { return m.metaSpend(ctx, c, from, to) }
		}},
	}
}

func (m *Module) client() *http.Client {
	if m.cfg.Client != nil {
		return m.cfg.Client
	}
	return m.app.OutboundClient(30 * time.Second)
}

// getJSON sends req and decodes a 2xx answer into out; anything else is an
// error carrying the platform's own message, which the panel shows.
func (m *Module) getJSON(req *http.Request, out any) error {
	body, err := m.call(req)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// call sends req and returns a 2xx answer's body; anything else is an error
// carrying the platform's own message.
func (m *Module) call(req *http.Request) ([]byte, error) {
	res, err := m.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		var e struct {
			Error json.RawMessage `json:"error"`
		}
		msg := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &e) == nil && len(e.Error) > 0 {
			var detail struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(e.Error, &detail) == nil && detail.Message != "" {
				msg = detail.Message
			} else {
				msg = strings.Trim(string(e.Error), `"`)
			}
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return body, fmt.Errorf("%s answered %d: %s", req.URL.Host, res.StatusCode, msg)
	}
	return body, nil
}

// micros turns Google's cost in millionths of a unit into minor units, half up.
func micros(v int64, exp int) int64 {
	div := int64(1)
	for range 6 - exp {
		div *= 10
	}
	return (v + div/2) / div
}

// googleToken trades the refresh token for an access token, per run: it
// lasts an hour.
func (m *Module) googleToken(ctx context.Context, c googleConfig) (string, error) {
	form := url.Values{"client_id": {c.ClientID}, "client_secret": {c.ClientSecret},
		"refresh_token": {c.RefreshToken}, "grant_type": {"refresh_token"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := m.getJSON(req, &tok); err != nil {
		return "", fmt.Errorf("sign in to Google: %w", err)
	}
	return tok.AccessToken, nil
}

// googleRequest is a POST to the account's endpoint ("googleAds:search",
// "googleAds:mutate") with the headers every Google Ads call carries.
func googleRequest(ctx context.Context, c googleConfig, token, method string, body []byte) (*http.Request, error) {
	endpoint := fmt.Sprintf("https://googleads.googleapis.com/%s/customers/%s/%s", c.APIVersion, digits(c.CustomerID), method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("developer-token", c.DeveloperToken)
	if id := digits(c.LoginCustomerID); id != "" {
		req.Header.Set("login-customer-id", id)
	}
	return req, nil
}

// googleSearch runs a GAQL query and hands each result row to each, page by page.
func (m *Module) googleSearch(ctx context.Context, c googleConfig, token, query string, each func(json.RawMessage) error) error {
	page := ""
	for range 100 {
		body, _ := json.Marshal(map[string]string{"query": query, "pageToken": page})
		req, err := googleRequest(ctx, c, token, "googleAds:search", body)
		if err != nil {
			return err
		}
		var out struct {
			Results       []json.RawMessage `json:"results"`
			NextPageToken string            `json:"nextPageToken"`
		}
		if err := m.getJSON(req, &out); err != nil {
			return fmt.Errorf("read Google Ads: %w", err)
		}
		for _, r := range out.Results {
			if err := each(r); err != nil {
				return err
			}
		}
		if page = out.NextPageToken; page == "" {
			return nil
		}
	}
	return errors.New("read Google Ads: more than 100 pages")
}

func (m *Module) googleSpend(ctx context.Context, c googleConfig, from, to string) ([]dayRow, error) {
	tok, err := m.googleToken(ctx, c)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("SELECT segments.date, campaign.name, customer.currency_code, metrics.cost_micros FROM campaign "+
		"WHERE segments.date BETWEEN '%s' AND '%s' AND metrics.cost_micros > 0", from, to)
	var rows []dayRow
	err = m.googleSearch(ctx, c, tok, query, func(raw json.RawMessage) error {
		var r struct {
			Segments struct {
				Date string `json:"date"`
			} `json:"segments"`
			Campaign struct {
				Name string `json:"name"`
			} `json:"campaign"`
			Customer struct {
				CurrencyCode string `json:"currencyCode"`
			} `json:"customer"`
			Metrics struct {
				// int64 fields are strings in the API's JSON.
				CostMicros string `json:"costMicros"`
			} `json:"metrics"`
		}
		_ = json.Unmarshal(raw, &r)
		v, err := strconv.ParseInt(r.Metrics.CostMicros, 10, 64)
		cur := strings.ToUpper(r.Customer.CurrencyCode)
		if err != nil || !currencyRE.MatchString(cur) || clip(r.Campaign.Name) == "" {
			return fmt.Errorf("read Google Ads: a row it sent does not read: %s", raw)
		}
		rows = append(rows, dayRow{Day: r.Segments.Date, Campaign: clip(r.Campaign.Name), Currency: cur,
			Amount: micros(v, gocommerce.CurrencyExponent(cur))})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return merge(rows), nil
}

// metaAuth is the token, and its proof when the app secret is given, that
// every Graph call carries.
func metaAuth(c metaConfig, q url.Values) url.Values {
	q.Set("access_token", c.AccessToken)
	if c.AppSecret != "" {
		mac := hmac.New(sha256.New, []byte(c.AppSecret))
		mac.Write([]byte(c.AccessToken))
		q.Set("appsecret_proof", hex.EncodeToString(mac.Sum(nil)))
	}
	return q
}

func (m *Module) metaSpend(ctx context.Context, c metaConfig, from, to string) ([]dayRow, error) {
	q := url.Values{
		"level":          {"campaign"},
		"fields":         {"campaign_name,spend,account_currency"},
		"time_increment": {"1"},
		"time_range":     {fmt.Sprintf(`{"since":"%s","until":"%s"}`, from, to)},
		"limit":          {"500"},
	}
	next := fmt.Sprintf("https://graph.facebook.com/%s/act_%s/insights?%s", c.APIVersion, digits(c.AdAccountID), metaAuth(c, q).Encode())
	var rows []dayRow
	for range 100 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		var out struct {
			Data []struct {
				CampaignName    string `json:"campaign_name"`
				Spend           string `json:"spend"`
				AccountCurrency string `json:"account_currency"`
				DateStart       string `json:"date_start"`
			} `json:"data"`
			Paging struct {
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := m.getJSON(req, &out); err != nil {
			return nil, fmt.Errorf("read Meta: %w", err)
		}
		for _, r := range out.Data {
			cur := strings.ToUpper(r.AccountCurrency)
			if !currencyRE.MatchString(cur) || clip(r.CampaignName) == "" {
				return nil, fmt.Errorf("read Meta: a row it sent does not read: %+v", r)
			}
			v, err := parseAmount(r.Spend, gocommerce.CurrencyExponent(cur))
			if err != nil {
				return nil, fmt.Errorf("read Meta: spend %q: %w", r.Spend, err)
			}
			rows = append(rows, dayRow{Day: r.DateStart, Campaign: clip(r.CampaignName), Currency: cur, Amount: v})
		}
		// The next page's address is Meta's, and carries the token itself.
		if next = out.Paging.Next; next == "" || !strings.HasPrefix(next, "https://graph.facebook.com/") {
			return merge(rows), nil
		}
	}
	return nil, errors.New("read Meta: more than 100 pages")
}

// syncOne runs one connector for the store on ctx, if it is on and filled
// in, and keeps what happened. force runs it however recently it last ran.
func (m *Module) syncOne(ctx context.Context, c connector, now time.Time, force bool) (*SyncRun, error) {
	on, missing, source, medium, fetch := c.read(ctx)
	if !on || len(missing) > 0 {
		return nil, nil
	}
	if !force {
		var last time.Time
		err := m.app.DB().QueryRowContext(ctx, `SELECT ran_at FROM ads_sync WHERE platform = $1 AND ok`, c.platform).Scan(&last)
		if err == nil && now.Sub(last) < syncEvery {
			return nil, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, gocommerce.Internalf(err, "read the last sync")
		}
	}
	source, medium, err := channelOf(source, medium)
	run := &SyncRun{At: now}
	var rows []dayRow
	if err == nil {
		to := now.UTC().Format(time.DateOnly)
		from := now.UTC().AddDate(0, 0, -syncDays).Format(time.DateOnly)
		rows, err = fetch(ctx, from, to)
	}
	if err == nil {
		err = gocommerce.InTx(ctx, m.app.DB(), func(tx *sql.Tx) error { return m.upsert(ctx, tx, source, medium, c.platform, rows) })
	}
	if err != nil {
		run.Message = err.Error()
	} else {
		run.OK = true
		days, campaigns := map[string]bool{}, map[string]bool{}
		for _, r := range rows {
			days[r.Day], campaigns[strings.ToLower(r.Campaign)] = true, true
		}
		run.Days, run.Campaigns = len(days), len(campaigns)
		run.Message = fmt.Sprintf("Read %d days of spend for %d campaigns.", run.Days, run.Campaigns)
	}
	// Not on ctx alone: a shutdown mid-run still records that it ran.
	_, dbErr := m.app.DB().ExecContext(context.WithoutCancel(ctx), `
		INSERT INTO ads_sync (platform, ran_at, ok, message, days, campaigns) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (store_id, platform) DO UPDATE SET ran_at = EXCLUDED.ran_at, ok = EXCLUDED.ok,
		    message = EXCLUDED.message, days = EXCLUDED.days, campaigns = EXCLUDED.campaigns`,
		c.platform, run.At, run.OK, run.Message, run.Days, run.Campaigns)
	if dbErr != nil {
		return nil, gocommerce.Internalf(dbErr, "keep the sync result")
	}
	return run, nil
}

// syncAll is the daily job's step for one store. A failed run is kept and
// shown, and tried again in an hour: unlike the weekly summary, it costs
// nothing to ask again.
func (m *Module) syncAll(ctx context.Context, now time.Time) error {
	for _, c := range m.connectors() {
		if _, err := m.syncOne(ctx, c, now, false); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) listConnectors(ctx context.Context) ([]Connector, error) {
	out := []Connector{}
	for _, c := range m.connectors() {
		on, missing, _, _, _ := c.read(ctx)
		k := Connector{Platform: c.platform, Plugin: c.plugin, Title: c.title, Enabled: on, Missing: missing}
		if k.Missing == nil {
			k.Missing = []string{}
		}
		var r SyncRun
		err := m.app.DB().QueryRowContext(ctx, `SELECT ran_at, ok, message, days, campaigns FROM ads_sync WHERE platform = $1`, c.platform).
			Scan(&r.At, &r.OK, &r.Message, &r.Days, &r.Campaigns)
		switch {
		case err == nil:
			k.Last = &r
		case !errors.Is(err, sql.ErrNoRows):
			return nil, gocommerce.Internalf(err, "read the last sync")
		}
		out = append(out, k)
	}
	return out, nil
}

func (m *Module) handleConnectors(w http.ResponseWriter, r *http.Request) {
	out, err := m.listConnectors(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

// handleSync runs one connector now, whenever it last ran.
func (m *Module) handleSync(w http.ResponseWriter, r *http.Request) {
	for _, c := range m.connectors() {
		if c.platform != r.PathValue("platform") {
			continue
		}
		run, err := m.syncOne(r.Context(), c, time.Now(), true)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		if run == nil {
			gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusConflict, Code: "not_connected",
				Message: c.title + " is not connected: switch on its plugin and fill in its settings"})
			return
		}
		gocommerce.Respond(w, http.StatusOK, run)
		return
	}
	gocommerce.RespondError(w, r, gocommerce.NotFoundf("no connector is called %q", r.PathValue("platform")))
}
