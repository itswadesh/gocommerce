package ads

import (
	"context"
	"strings"
	"testing"
)

func TestATouchNamesItsChannel(t *testing.T) {
	cases := []struct {
		name  string
		touch *Touch
		want  channel
	}{
		{"never recorded", nil, channel{notRecorded, "", ""}},
		{"tagged link", &Touch{UTMSource: " Google ", UTMMedium: "CPC", UTMCampaign: "Diwali Sale"}, channel{"google", "cpc", "Diwali Sale"}},
		{"tagged without a medium", &Touch{UTMSource: "newsletter"}, channel{"newsletter", "", ""}},
		{"auto-tagged Google ad", &Touch{GCLID: "abc", Referrer: "www.google.com"}, channel{"google", "cpc", ""}},
		{"Google app campaign", &Touch{GBRAID: "x"}, channel{"google", "cpc", ""}},
		{"Microsoft ad", &Touch{MSCLKID: "x"}, channel{"bing", "cpc", ""}},
		{"TikTok ad", &Touch{TTCLID: "x"}, channel{"tiktok", "cpc", ""}},
		// Meta adds fbclid to every outbound link, paid or not.
		{"a Facebook click", &Touch{FBCLID: "x", Referrer: "l.facebook.com"}, channel{"facebook", "social", ""}},
		{"search result", &Touch{Referrer: "www.google.co.in"}, channel{"google", "organic", ""}},
		{"Instagram link", &Touch{Referrer: "l.instagram.com"}, channel{"instagram", "social", ""}},
		{"another site", &Touch{Referrer: "www.blog.example"}, channel{"blog.example", "referral", ""}},
		{"typed address", &Touch{At: "2026-10-30T10:00:00Z"}, channel{direct, "", ""}},
	}
	for _, c := range cases {
		if got := c.touch.channel(); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestAggregateKeepsCurrenciesApartAndMatchesSpend(t *testing.T) {
	google := &Attribution{Last: &Touch{UTMSource: "google", UTMMedium: "cpc", UTMCampaign: "Diwali"}}
	orders := []orderFact{
		{ID: 1, Currency: "INR", Total: 100000, Cost: 40000, Attr: google},
		{ID: 2, Currency: "INR", Total: 50000, Cost: 20000, Uncosted: 1, Returning: true, Attr: google},
		{ID: 3, Currency: "USD", Total: 2000, Cost: 500, Attr: google},
	}
	spend := []spendFact{
		{channel{"google", "cpc", "diwali"}, "INR", 30000},
		{channel{"google", "cpc", "Diwali"}, "USD", 1000},
		{channel{"facebook", "paid", "Retarget"}, "INR", 4000},
	}
	got := aggregate(orders, spend, "last")
	if len(got) != 2 || got[0].Currency != "INR" || got[1].Currency != "USD" {
		t.Fatalf("blocks %+v, want INR then USD", got)
	}
	inr := got[0]
	if len(inr.Rows) != 2 {
		t.Fatalf("INR rows %+v, want Diwali and the unmatched Retarget", inr.Rows)
	}
	d := inr.Rows[0]
	if d.Campaign != "Diwali" || d.Orders != 2 || d.Revenue.AmountMinor != 150000 || d.GrossProfit.AmountMinor != 90000 {
		t.Errorf("Diwali %+v", d)
	}
	if d.Spend == nil || d.Spend.AmountMinor != 30000 || *d.ROAS != 5 || *d.POAS != 3 || d.CostPerOrder.AmountMinor != 15000 {
		t.Errorf("Diwali spend figures %+v roas %v poas %v", d.Spend, d.ROAS, d.POAS)
	}
	if d.UncostedUnits != 1 || d.NewCustomers != 1 || d.ReturningCustomers != 1 || d.AverageOrder.AmountMinor != 75000 {
		t.Errorf("Diwali counts %+v", d)
	}
	r := inr.Rows[1]
	if r.Campaign != "Retarget" || r.Orders != 0 || r.Spend.AmountMinor != 4000 || *r.ROAS != 0 || r.CostPerOrder != nil {
		t.Errorf("spend with no orders %+v", r)
	}
	if inr.Totals.Revenue.AmountMinor != 150000 || inr.Totals.Spend.AmountMinor != 34000 {
		t.Errorf("INR totals %+v", inr.Totals)
	}
	usd := got[1].Rows[0]
	if usd.Revenue.AmountMinor != 2000 || usd.Revenue.Currency != "USD" || usd.Spend.AmountMinor != 1000 || *usd.ROAS != 2 {
		t.Errorf("USD row %+v", usd)
	}
}

func TestProfitIsWhatTheShopKeeps(t *testing.T) {
	// ₹1,180 with ₹180 tax, half refunded: ₹590 kept, of which ₹90 is tax.
	o := orderFact{Total: 118000, Tax: 18000, Refunded: 59000, Cost: 30000}
	if got := o.profit(); got != 590_00-90_00-300_00 {
		t.Errorf("profit = %d", got)
	}
	// No cost recorded is no cost taken off, and the row says so.
	rows := aggregate([]orderFact{{Currency: "INR", Total: 1000, Uncosted: 2}}, nil, "last")
	if r := rows[0].Rows[0]; r.GrossProfit.AmountMinor != 1000 || r.UncostedUnits != 2 || r.Source != notRecorded {
		t.Errorf("uncosted row %+v", r)
	}
	// A fully refunded order keeps nothing and its goods came back.
	if got := (orderFact{Total: 1000, Tax: 100, Refunded: 1000}).profit(); got != 0 {
		t.Errorf("refunded profit = %d", got)
	}
}

func TestFirstTouchGroupsByTheFirstVisit(t *testing.T) {
	a := &Attribution{
		First: &Touch{UTMSource: "facebook", UTMMedium: "paid", UTMCampaign: "Launch"},
		Last:  &Touch{Referrer: "www.google.com"},
	}
	first := aggregate([]orderFact{{Currency: "INR", Total: 100, Attr: a}}, nil, "first")[0].Rows[0]
	last := aggregate([]orderFact{{Currency: "INR", Total: 100, Attr: a}}, nil, "last")[0].Rows[0]
	if first.Campaign != "Launch" || last.Source != "google" || last.Medium != "organic" {
		t.Errorf("first %+v last %+v", first, last)
	}
}

func TestSpendSplitsAcrossItsDaysExactly(t *testing.T) {
	got := splitEvenly(1000, 3)
	if len(got) != 3 || got[0]+got[1]+got[2] != 1000 || got[0] != 334 || got[2] != 333 {
		t.Errorf("split = %v", got)
	}
}

func TestGoogleAdsExportIsMappedByItsColumnNames(t *testing.T) {
	csv := "Campaign report\n" +
		"\"October 1, 2026 - October 2, 2026\"\n" +
		"Day,Campaign,Campaign status,Currency code,Impr.,Clicks,Cost\n" +
		"2026-10-01,Diwali Sale,Enabled,INR,1200,40,\"1,234.50\"\n" +
		"2026-10-02,Diwali Sale,Enabled,INR,900,22,--\n" +
		"\"Oct 2, 2026\",Brand,Enabled,INR,10,1,5.00\n" +
		"Total: Account,,,INR,2110,63,\"1,239.50\"\n" +
		"Total: Campaigns,,,INR,2110,63,\"1,239.50\"\n"
	rows, err := parseSpend("google", csv, "USD")
	if err != nil {
		t.Fatal(err)
	}
	// A day of nothing is kept, so it can replace one imported before.
	want := []dayRow{
		{"2026-10-01", "Diwali Sale", "INR", 123450},
		{"2026-10-02", "Diwali Sale", "INR", 0},
		{"2026-10-02", "Brand", "INR", 500},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v", rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}

	// The "Excel .csv" download is tab-separated, and has no currency column
	// when the shop left it out: the currency chosen on the form is used.
	tsv := bom + "Day\tCampaign\tCost\n2026-10-03\tBrand\t7.25\n"
	rows, err = parseSpend("google", tsv, "INR")
	if err != nil || len(rows) != 1 || rows[0] != (dayRow{"2026-10-03", "Brand", "INR", 725}) {
		t.Errorf("tsv = %+v, %v", rows, err)
	}

	_, err = parseSpend("google", "Campaign,Cost\nBrand,5\n", "INR")
	if err == nil || !strings.Contains(err.Error(), "Day") {
		t.Errorf("no Day column = %v, want a refusal asking for the Day segment", err)
	}
	_, err = parseSpend("google", "Day,Ad group,Clicks\n2026-10-01,x,1\n", "INR")
	if err == nil || !strings.Contains(err.Error(), "Campaign") {
		t.Errorf("no Campaign column = %v", err)
	}
}

func TestMetaAdsExportIsMappedByItsColumnNames(t *testing.T) {
	daily := "Campaign name,Day,Reach,Impressions,Amount spent (INR),Reporting starts,Reporting ends\n" +
		"Retarget,2026-10-01,500,900,400.5,2026-10-01,2026-10-01\n" +
		"Retarget,2026-10-02,500,900,0,2026-10-02,2026-10-02\n"
	rows, err := parseSpend("meta", daily, "USD")
	if err != nil || len(rows) != 2 || rows[0] != (dayRow{"2026-10-01", "Retarget", "INR", 40050}) || rows[1].Amount != 0 {
		t.Errorf("daily = %+v, %v", rows, err)
	}

	// Without the Day breakdown each row is its reporting period, spread over
	// its days so that a shorter report window counts its share.
	period := "Campaign name,Amount spent (USD),Reporting starts,Reporting ends\n" +
		"Launch,10.00,2026-10-01,2026-10-03\n"
	rows, err = parseSpend("meta", period, "INR")
	if err != nil || len(rows) != 3 {
		t.Fatalf("period = %+v, %v", rows, err)
	}
	var sum int64
	for _, r := range rows {
		sum += r.Amount
		if r.Currency != "USD" || r.Campaign != "Launch" {
			t.Errorf("period row %+v", r)
		}
	}
	if sum != 1000 || rows[0].Day != "2026-10-01" || rows[2].Day != "2026-10-03" {
		t.Errorf("period rows %+v", rows)
	}

	_, err = parseSpend("meta", "Campaign name,Reach\nLaunch,5\n", "INR")
	if err == nil || !strings.Contains(err.Error(), "Amount spent") {
		t.Errorf("no spend column = %v", err)
	}
	if _, err := parseSpend("tiktok", period, "INR"); err == nil {
		t.Error("an unknown format was read")
	}
}

func TestAmountsAreReadWithoutFloats(t *testing.T) {
	cases := map[string]int64{"1,234.50": 123450, "0.005": 1, "12": 1200, "₹ 99.99": 9999, "--": 0, "": 0}
	for in, want := range cases {
		got, err := parseAmount(in, 2)
		if err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", in, got, err, want)
		}
	}
	if got, _ := parseAmount("1500", 0); got != 1500 {
		t.Errorf("JPY = %d", got)
	}
	for _, bad := range []string{"-5", "abc", "1.2.3"} {
		if _, err := parseAmount(bad, 2); err == nil {
			t.Errorf("%q was read", bad)
		}
	}
}

func TestASummaryMayOnlyUseTheFiguresItWasGiven(t *testing.T) {
	in := summaryInput{From: "2026-10-01", To: "2026-10-30", Figures: []figure{{
		Row: 1, Campaign: "Diwali", Orders: 2, Revenue: "₹1,500.00", Spend: "₹4,000.00",
		GrossProfit: "-₹2,500.00", ROAS: "0.38", POAS: "-0.63",
	}}}
	ctx := withAllowed(context.Background(), in)
	ok := `{"headline":"Diwali lost money.","points":[{"row":1,"kind":"wasting",` +
		`"text":"Diwali: ₹4,000 spent, 2 orders, ₹1,500.00 in sales and a loss of ₹2,500.00 after product cost (ROAS 0.38)."}]}`
	if _, err := checkSummary(ctx, []byte(ok)); err != nil {
		t.Errorf("a summary of the given figures was refused: %v", err)
	}
	for name, bad := range map[string]string{
		"invented figure": `{"headline":"x","points":[{"row":1,"kind":"wasting","text":"Diwali returned 40% less."}]}`,
		"sum it made":     `{"headline":"₹5,500 went out in total.","points":[]}`,
		"row not given":   `{"headline":"x","points":[{"row":2,"kind":"working","text":"x"}]}`,
		"unknown kind":    `{"headline":"x","points":[{"row":1,"kind":"great","text":"x"}]}`,
	} {
		if _, err := checkSummary(ctx, []byte(bad)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestReviewedEdgesOfTheMoney(t *testing.T) {
	// Refunded in full with nothing back: the goods are still the shop's loss.
	if got := (orderFact{Total: 1000, Tax: 100, Refunded: 1000, Cost: 400}).profit(); got != -400 {
		t.Errorf("refunded, goods not back: profit = %d, want -400", got)
	}
	// A decimal comma, as European account languages write it.
	for in, want := range map[string]int64{"1.234,56": 123456, "1234,5": 123450, "12,34": 1234, "1,234": 123400} {
		if got, err := parseAmount(in, 2); err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", in, got, err, want)
		}
	}
}

func TestReviewedEdgesOfTheImports(t *testing.T) {
	// A campaign whose name begins "Total" is a campaign; a day spent nothing
	// is kept, so a re-download can lower a day to zero.
	csv := "Campaign,Day,Cost\nTotalcare,2026-10-01,5.00\nBrand,2026-10-01,--\nTotal: Account,,5.00\n"
	rows, err := parseSpend("google", csv, "INR")
	if err != nil || len(rows) != 2 || rows[0].Campaign != "Totalcare" || rows[1].Amount != 0 {
		t.Errorf("rows %+v, %v", rows, err)
	}
	// Tab-separated, with more commas than tabs in the title and amounts.
	tsv := "Campaign report\n\"October 1, 2026 - October 2, 2026\"\nDay\tCampaign\tCost\n\"Oct 1, 2026\"\tBrand\t1,234.50\n"
	rows, err = parseSpend("google", tsv, "INR")
	if err != nil || len(rows) != 1 || rows[0].Amount != 123450 {
		t.Errorf("narrow tsv = %+v, %v", rows, err)
	}
}

func TestASummaryMayNotMakeUpRatios(t *testing.T) {
	in := summaryInput{From: "2026-10-01", To: "2026-10-30", Figures: []figure{
		{Row: 1, Campaign: "A", Orders: 6, Spend: "₹100.00", ROAS: "1.56"},
		{Row: 2, Campaign: "B", Orders: 2, Spend: "₹50.00", ROAS: "none"},
		{Row: 3, Campaign: "C", Orders: 0, Spend: "₹10.00", ROAS: "0.00"},
	}}
	ctx := withAllowed(context.Background(), in)
	for _, bad := range []string{"A brought 3x the orders of B.", "A did 2% better."} {
		if _, err := checkSummary(ctx, []byte(`{"headline":"`+bad+`","points":[]}`)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := checkSummary(ctx, []byte(`{"headline":"A returned 1.56x its spend of ₹100.00 in 6 orders.","points":[]}`)); err != nil {
		t.Errorf("a ROAS as given was refused: %v", err)
	}
}
