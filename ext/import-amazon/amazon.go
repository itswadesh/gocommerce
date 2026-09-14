package amazon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// What a product page says, once the browser has rendered it. Everything here
// is read by a script run *inside* the page — see extractScript — because the
// variation widget and the hi-res image list are written by Amazon's own
// JavaScript and are not in the HTML a plain GET returns.

// Listing is one Amazon product with every variation the page named.
type Listing struct {
	ASIN        string   `json:"asin"`
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Brand       string   `json:"brand"`
	Bullets     []string `json:"bullets"`
	Description string   `json:"description"`
	Specs       []Spec   `json:"specs"`
	Breadcrumbs []string `json:"breadcrumbs"`
	// PriceMinor is the parent page's price in Currency — the currency the
	// page printed it in, which the browser session's delivery address
	// decides, with the marketplace's own as the fallback when the text
	// names none. See currencyFromPrice.
	PriceMinor int64    `json:"price_minor"`
	Currency   string   `json:"currency"`
	Images     []string `json:"images"`
	// Options are the variation axes — Color, Size — in the page's order, and
	// Variants each name one value per axis in that same order.
	Options  []ListingOption  `json:"options"`
	Variants []ListingVariant `json:"variants"`
	// Rating and ReviewCount are the listing's summary; Reviews are the ones
	// the page shows in full. They are the marketplace's customers' words
	// about the marketplace's listing, and are recorded as such.
	Rating      float64  `json:"rating"`
	ReviewCount int      `json:"review_count"`
	Reviews     []Review `json:"reviews"`
}

// Review is one customer review as the page showed it.
type Review struct {
	Title    string  `json:"title"`
	Rating   float64 `json:"rating"`
	Author   string  `json:"author"`
	Date     string  `json:"date"`
	Body     string  `json:"body"`
	Verified bool    `json:"verified"`
}

// Spec is one row of the details table, kept ordered because the page's
// order is a merchandiser's order.
type Spec struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ListingOption struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

type ListingVariant struct {
	ASIN       string   `json:"asin"`
	Options    []string `json:"options"`
	PriceMinor int64    `json:"price_minor"`
	Images     []string `json:"images"`
	Available  bool     `json:"available"`
	// Fetched says whether the variant's own page was visited. One that was
	// not carries the parent's price and images, which is a guess worth
	// labelling.
	Fetched bool `json:"fetched"`
}

// page is the raw shape the extraction script returns.
type page struct {
	URL     string `json:"url"`
	Blocked bool   `json:"blocked"`
	// DocTitle and Sample are what the document called itself and the first
	// of its text — read only so that "no product on the page" can say what
	// was on it instead.
	DocTitle string `json:"doc_title"`
	Sample   string `json:"sample"`
	// Debug is what the script saw where it looked for a price and a
	// variation widget — the keys and the candidates, not a verdict — so a
	// listing that came back without either can be diagnosed from the log.
	Debug       map[string]any `json:"debug"`
	ASIN        string         `json:"asin"`
	Title       string         `json:"title"`
	Brand       string         `json:"brand"`
	Bullets     []string       `json:"bullets"`
	Description string         `json:"description"`
	Specs       [][]string     `json:"specs"`
	Price       string         `json:"price"`
	Available   bool           `json:"available"`
	// Unshippable is the buy box saying the item cannot go to the session's
	// delivery location — the ordinary reason a listing has no price.
	Unshippable bool     `json:"unshippable"`
	Breadcrumbs []string `json:"breadcrumbs"`
	Images      []string `json:"images"`
	Twister     *twister `json:"twister"`
	Rating      float64  `json:"rating"`
	ReviewCount int      `json:"review_count"`
	Reviews     []Review `json:"reviews"`
}

// twister is Amazon's name for the variation widget, and the shape of the
// JSON it initialises from.
type twister struct {
	Dimensions []string            `json:"dimensions"`
	Labels     map[string]string   `json:"labels"`
	Values     map[string][]string `json:"values"` // asin → one value per dimension
	// Order is each dimension's values in the order the page listed them —
	// Amazon's variationValues — so "Small" precedes "Large" here because it
	// did there, and not because of the alphabet.
	Order   map[string][]string `json:"order"`
	Current string              `json:"current"`
	Parent  string              `json:"parent"`
}

// errBlocked is Amazon's robot check. It is its own error so the job can be
// marked "blocked" rather than "failed": the difference is that one is fixed
// by a person passing the check once in the headed browser, and the other by
// an engineer.
var errBlocked = errors.New("amazon showed a robot check instead of the product")

// pageFetcher is the seam the tests use: the real one drives Chrome, the stub
// answers with fixtures.
type pageFetcher interface {
	fetch(ctx context.Context, url string) (*page, error)
	close() error
}

// chromeFetcher fetches through one tab of one Chrome, sequentially.
type chromeFetcher struct {
	b      *browser
	tab    *tab
	settle time.Duration
	// headed and humanWait are the human-in-the-loop: when Amazon asks for a
	// person and there is a window a person can see, the fetch waits for
	// them rather than failing. onWait is how the job is told to say so.
	headed    bool
	humanWait time.Duration
	onWait    func(message string)
}

func newChromeFetcher(ctx context.Context, path, profileDir string, headless bool, settle, humanWait time.Duration, log interface {
	Info(string, ...any)
	Warn(string, ...any)
	Error(string, ...any)
}) (*chromeFetcher, error) {
	if path == "" {
		var err error
		if path, err = findChrome(); err != nil {
			return nil, err
		}
	}
	b, err := launch(ctx, path, profileDir, headless, nil)
	if err != nil {
		return nil, err
	}
	t, err := b.newTab(ctx)
	if err != nil {
		_ = b.close()
		return nil, err
	}
	log.Info("chrome started for Amazon imports", "path", path, "headless", headless, "profile", profileDir)
	return &chromeFetcher{b: b, tab: t, settle: settle, headed: !headless, humanWait: humanWait}, nil
}

func (f *chromeFetcher) fetch(ctx context.Context, url string) (*page, error) {
	if err := f.tab.navigate(ctx, url, f.settle); err != nil {
		return nil, err
	}
	p, err := f.extract(ctx)
	if err != nil {
		return nil, err
	}
	if !p.Blocked || !f.headed || f.humanWait <= 0 {
		return p, nil
	}

	// Amazon has asked for a person, and there is a window a person can see.
	// Wait for them: the module solves nothing itself, it only notices when
	// the page in front of the operator has become the product. The
	// persistent profile then remembers the session for the next import.
	if f.onWait != nil {
		f.onWait("Amazon is asking for a person. In the Chrome window that is open, click through (" +
			firstNonEmpty(p.DocTitle, "the page Amazon showed") + ") — this import carries on by itself once you have.")
	}
	deadline := time.Now().Add(f.humanWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		if p, err = f.extract(ctx); err != nil {
			// The operator's click navigated the tab mid-script; look again.
			continue
		}
		if !p.Blocked {
			// The gate has cleared — and Amazon lands the person on its home
			// page, not back on the listing. So ask for the listing again,
			// now that the session is accepted, and read that.
			if err := f.tab.navigate(ctx, url, f.settle); err != nil {
				return nil, err
			}
			return f.extract(ctx)
		}
	}
	return p, nil
}

func (f *chromeFetcher) extract(ctx context.Context) (*page, error) {
	var raw string
	if err := f.tab.evaluate(ctx, extractScript, &raw); err != nil {
		return nil, err
	}
	var p page
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("amazon: the extraction script returned something that is not JSON: %w", err)
	}
	return &p, nil
}

func (f *chromeFetcher) close() error { return f.b.close() }

// crawl reads the parent page and then each variation's own page, and folds
// them into one Listing.
//
// Each variant is visited because the parent page only names them: their
// prices and pictures live on their own pages. maxVariants bounds the walk so
// a listing with two hundred size-and-colour combinations does not turn one
// import into ten minutes of browsing; the ones past the cap are still created,
// carrying the parent's price and pictures and marked Fetched=false.
func crawl(ctx context.Context, f pageFetcher, rawURL string, maxVariants int) (*Listing, []string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return nil, nil, fmt.Errorf("amazon: %q is not a URL", rawURL)
	}
	if !strings.Contains(strings.ToLower(parsed.Host), "amazon.") {
		return nil, nil, fmt.Errorf("amazon: %s is not an Amazon host", parsed.Host)
	}

	parent, err := f.fetch(ctx, parsed.String())
	if err != nil {
		return nil, nil, err
	}
	if parent.Blocked {
		return nil, nil, errBlocked
	}
	if parent.ASIN == "" || parent.Title == "" {
		return nil, nil, fmt.Errorf("amazon: the page has no product on it (its title is %q, it begins %q) — check the URL is a /dp/ product page",
			parent.DocTitle, truncate(parent.Sample, 160))
	}

	currency := currencyFromPrice(parent.Price, currencyForHost(parsed.Host))
	var warnings []string
	if parent.Unshippable {
		warnings = append(warnings, "Amazon says this item cannot be shipped to the browser's delivery location, so it showed no price — set the delivery location once in the Chrome window (it is kept in the profile) and import again for prices")
	}
	listing := &Listing{
		ASIN: parent.ASIN, URL: parsed.String(), Title: parent.Title, Brand: cleanBrand(parent.Brand),
		Bullets: dedupe(parent.Bullets), Description: parent.Description,
		Breadcrumbs: parent.Breadcrumbs, Images: parent.Images, Currency: currency,
		Rating: parent.Rating, ReviewCount: parent.ReviewCount, Reviews: parent.Reviews,
	}
	for _, kv := range parent.Specs {
		if len(kv) == 2 {
			listing.Specs = append(listing.Specs, Spec{Key: kv[0], Value: kv[1]})
		}
	}
	if minor, ok := parsePrice(parent.Price, currency); ok {
		listing.PriceMinor = minor
	} else if parent.Price != "" {
		warnings = append(warnings, "could not read the price "+strconv.Quote(parent.Price))
	}

	tw := parent.Twister
	if tw == nil || len(tw.Dimensions) == 0 || len(tw.Values) <= 1 {
		// No variations: the product is its own only variant.
		listing.Variants = []ListingVariant{{
			ASIN: parent.ASIN, PriceMinor: listing.PriceMinor, Images: parent.Images,
			Available: parent.Available, Fetched: true,
		}}
		return listing, warnings, nil
	}

	for i, dim := range tw.Dimensions {
		name := dim
		if label, ok := tw.Labels[dim]; ok && label != "" {
			name = label
		}
		option := ListingOption{Name: name}
		seen := map[string]bool{}
		for _, asin := range sortedASINs(tw) {
			vals := tw.Values[asin]
			if i < len(vals) && !seen[vals[i]] {
				seen[vals[i]] = true
				option.Values = append(option.Values, vals[i])
			}
		}
		listing.Options = append(listing.Options, option)
	}

	all := sortedASINs(tw)
	visit, key := chooseVisits(tw, all, parent.ASIN, maxVariants)
	for _, asin := range all {
		vals := tw.Values[asin]
		if len(vals) != len(tw.Dimensions) {
			continue
		}
		v := ListingVariant{ASIN: asin, Options: vals, PriceMinor: listing.PriceMinor,
			Images: parent.Images, Available: true}
		switch {
		case asin == parent.ASIN:
			v.Available, v.Fetched = parent.Available, true
		case visit[asin]:
			child, err := f.fetch(ctx, variantURL(parsed, asin))
			if err != nil {
				return nil, nil, fmt.Errorf("variant %s: %w", asin, err)
			}
			if child.Blocked {
				return nil, nil, errBlocked
			}
			v.Fetched, v.Available = true, child.Available
			if len(child.Images) > 0 {
				v.Images = child.Images
			}
			if minor, ok := parsePrice(child.Price, currency); ok {
				v.PriceMinor = minor
			} else if child.Price != "" {
				warnings = append(warnings, "variant "+asin+": could not read the price "+strconv.Quote(child.Price))
			}
		}
		listing.Variants = append(listing.Variants, v)
	}

	// A variant past the cap borrows its pictures from a visited one of its
	// colour, because that is what its own page would have shown; the
	// parent's pictures — a mix of every colour on most listings — are the
	// fallback when no page of that colour was opened. The price stays the
	// parent's, which is as good a guess as any sibling's.
	shown := map[string]*ListingVariant{}
	for i := range listing.Variants {
		v := &listing.Variants[i]
		if v.Fetched {
			if _, ok := shown[key(v.ASIN)]; !ok || v.ASIN == parent.ASIN {
				shown[key(v.ASIN)] = v
			}
		}
	}
	for i := range listing.Variants {
		v := &listing.Variants[i]
		if v.Fetched {
			continue
		}
		carries := "the parent's price and pictures"
		if sibling, ok := shown[key(v.ASIN)]; ok && sibling.ASIN != parent.ASIN {
			v.Images = sibling.Images
			carries = "the parent's price and the pictures of " + sibling.ASIN +
				", the same " + strings.Join(pictureDimNames(tw), " and ")
		}
		warnings = append(warnings, "variant "+v.ASIN+" was not visited (over the limit of "+
			strconv.Itoa(maxVariants)+"); it carries "+carries)
	}
	return listing, warnings, nil
}

// chooseVisits picks which variation pages to open when there are more than
// the cap: one of each colour before a second size of any, because pictures
// follow the colour (or style, or pattern) — a size left unvisited can borrow
// its colour's pictures, while a colour left unvisited has nothing to borrow.
// Within that, page order. The parent's page is already open and not counted.
// The key it returns names a variant's colour, so the caller can match a
// variant past the cap with the sibling whose pictures it borrows.
func chooseVisits(tw *twister, all []string, parent string, max int) (map[string]bool, func(string) string) {
	dims := pictureDims(tw)
	key := func(asin string) string {
		vals := tw.Values[asin]
		parts := make([]string, 0, len(dims))
		for _, i := range dims {
			if i < len(vals) {
				parts = append(parts, vals[i])
			}
		}
		return strings.Join(parts, "\x00")
	}
	visit := map[string]bool{}
	seen := map[string]bool{key(parent): true}
	for pass := 0; pass < 2; pass++ {
		for _, asin := range all {
			if len(visit) >= max {
				return visit, key
			}
			if asin == parent || visit[asin] || (pass == 0 && seen[key(asin)]) {
				continue
			}
			seen[key(asin)] = true
			visit[asin] = true
		}
	}
	return visit, key
}

// pictureDims are the dimensions a listing's pictures follow: every one that
// is not a size. Amazon keys those size_name, fit_type, item_package_quantity
// and the like and labels them Size, Fit, Number of Items; the photographs
// change with the colour, style, pattern or material, and never with those.
// A listing whose every dimension is a size has no picture dimension, and
// then every variant shares the parent's pictures, which is right.
func pictureDims(tw *twister) []int {
	var out []int
	for i, dim := range tw.Dimensions {
		if !sizeLike(dim) && !sizeLike(tw.Labels[dim]) {
			out = append(out, i)
		}
	}
	return out
}

func pictureDimNames(tw *twister) []string {
	var out []string
	for _, i := range pictureDims(tw) {
		dim := tw.Dimensions[i]
		out = append(out, firstNonEmpty(tw.Labels[dim], dim))
	}
	return out
}

func sizeLike(name string) bool {
	name = strings.ToLower(name)
	for _, word := range []string{"size", "fit", "length", "width", "waist", "inseam", "quantity",
		"count", "pack", "number", "capacity", "wattage", "voltage", "weight", "volume", "unit"} {
		if strings.Contains(name, word) {
			return true
		}
	}
	return false
}

// sortedASINs keeps the widget's ASINs in a stable order, because a map's
// order would shuffle the option values between two imports of one page.
func sortedASINs(tw *twister) []string {
	out := make([]string, 0, len(tw.Values))
	for asin := range tw.Values {
		out = append(out, asin)
	}
	// Sort by the values themselves, dimension by dimension, in the order the
	// page listed them — and alphabetically where the page gave no order,
	// which is at least the same order twice. Iterating the map here would
	// shuffle the variants between two imports of one listing.
	order := map[string]int{}
	for i, dim := range tw.Dimensions {
		values := append([]string(nil), tw.Order[dim]...)
		if len(values) == 0 {
			seen := map[string]bool{}
			for _, vals := range tw.Values {
				if i < len(vals) && !seen[vals[i]] {
					seen[vals[i]] = true
					values = append(values, vals[i])
				}
			}
			sort.Strings(values)
		}
		for j, v := range values {
			order[dim+"\x00"+v] = j
		}
	}
	less := func(a, b string) bool {
		va, vb := tw.Values[a], tw.Values[b]
		for i, dim := range tw.Dimensions {
			var ka, kb int
			if i < len(va) {
				ka = order[dim+"\x00"+va[i]]
			}
			if i < len(vb) {
				kb = order[dim+"\x00"+vb[i]]
			}
			if ka != kb {
				return ka < kb
			}
		}
		return a < b
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && less(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// dedupe drops repeats, keeping the first of each and the order: the page
// carries "About this item" in a collapsed and an expanded copy, and both
// have the same bullets.
func dedupe(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, s := range items {
		key := strings.ToLower(strings.TrimSpace(s))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}

// cleanBrand strips the marketing Amazon wraps a brand name in — "Visit the
// Acme Store", "Brand: Acme" — leaving the name.
func cleanBrand(s string) string {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"Visit the ", "visit the ", "Brand: ", "brand: "} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, " Store"), " store")
	return strings.TrimSpace(s)
}

func variantURL(parent *url.URL, asin string) string {
	return parent.Scheme + "://" + parent.Host + "/dp/" + asin
}

// currencyForHost maps a marketplace to the currency it sells in. The page's
// own symbol is ambiguous — "$" is five currencies — and the host is not.
func currencyForHost(host string) string {
	h := strings.ToLower(host)
	switch {
	case strings.HasSuffix(h, ".co.uk"):
		return "GBP"
	case strings.HasSuffix(h, ".in"):
		return "INR"
	case strings.HasSuffix(h, ".ca"):
		return "CAD"
	case strings.HasSuffix(h, ".com.au"):
		return "AUD"
	case strings.HasSuffix(h, ".co.jp"), strings.HasSuffix(h, ".jp"):
		return "JPY"
	case strings.HasSuffix(h, ".com.mx"):
		return "MXN"
	case strings.HasSuffix(h, ".com.br"):
		return "BRL"
	case strings.HasSuffix(h, ".ae"):
		return "AED"
	case strings.HasSuffix(h, ".sa"):
		return "SAR"
	case strings.HasSuffix(h, ".sg"):
		return "SGD"
	case strings.HasSuffix(h, ".se"):
		return "SEK"
	case strings.HasSuffix(h, ".pl"):
		return "PLN"
	case strings.HasSuffix(h, ".com.tr"):
		return "TRY"
	case strings.HasSuffix(h, ".eg"):
		return "EGP"
	case strings.HasSuffix(h, ".de"), strings.HasSuffix(h, ".fr"), strings.HasSuffix(h, ".it"),
		strings.HasSuffix(h, ".es"), strings.HasSuffix(h, ".nl"), strings.HasSuffix(h, ".com.be"),
		strings.HasSuffix(h, ".ie"):
		return "EUR"
	}
	return "USD"
}

// currencyFromPrice reads the currency off a price as Amazon printed it —
// "EUR29.72", "£7.50", "₹1,299" — falling back to the marketplace's own only
// when the text says nothing. The host is not enough: a session whose
// delivery address is in Finland sees amazon.com in euros.
func currencyFromPrice(text, fallback string) string {
	t := strings.ToUpper(strings.TrimSpace(text))
	for _, code := range []string{"EUR", "GBP", "INR", "JPY", "CAD", "AUD", "MXN", "BRL", "AED", "SAR", "SGD", "SEK", "PLN", "TRY", "EGP", "USD"} {
		if strings.Contains(t, code) {
			return code
		}
	}
	switch {
	case strings.Contains(t, "€"):
		return "EUR"
	case strings.Contains(t, "£"):
		return "GBP"
	case strings.Contains(t, "₹"):
		return "INR"
	case strings.Contains(t, "¥"), strings.Contains(t, "￥"):
		return "JPY"
	case strings.Contains(t, "R$"):
		return "BRL"
	case strings.Contains(t, "C$"):
		return "CAD"
	case strings.Contains(t, "A$"):
		return "AUD"
	}
	// A bare "$" is whichever dollar the marketplace sells in.
	return fallback
}

// minorExponent is how many decimal places a currency's minor unit has. The
// engine keeps prices as integers of that unit, so a rupee is 100 paise and a
// yen is one yen.
func minorExponent(currency string) int {
	switch currency {
	case "JPY", "KRW", "VND", "CLP", "ISK":
		return 0
	case "BHD", "KWD", "OMR", "JOD", "TND", "IQD", "LYD":
		return 3
	}
	return 2
}

var priceDigits = regexp.MustCompile(`[0-9][0-9.,\s]*[0-9]|[0-9]`)

// parsePrice turns "$1,299.00", "1.299,00 €" or "₹1,29,900" into minor units.
//
// The separator convention is read from the number itself: whichever of "."
// and "," comes last, followed by exactly two digits (three for a
// three-decimal currency, none for a zero-decimal one), is the decimal point.
// Everything else is grouping.
func parsePrice(text, currency string) (int64, bool) {
	m := priceDigits.FindString(text)
	if m == "" {
		return 0, false
	}
	m = strings.ReplaceAll(m, " ", "")
	exp := minorExponent(currency)

	lastDot, lastComma := strings.LastIndex(m, "."), strings.LastIndex(m, ",")
	sep := max(lastDot, lastComma)
	whole, frac := m, ""
	if sep >= 0 {
		tail := m[sep+1:]
		if exp > 0 && len(tail) == exp && !strings.ContainsAny(tail, ".,") {
			whole, frac = m[:sep], tail
		}
	}
	whole = strings.NewReplacer(".", "", ",", "").Replace(whole)
	if whole == "" {
		whole = "0"
	}
	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	scale := int64(math.Pow10(exp))
	minor := units * scale
	if frac != "" {
		f, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, false
		}
		minor += f
	}
	return minor, true
}

// extractScript runs inside the product page and returns a JSON string.
//
// It reads the DOM for the parts that are plain HTML and the page's own
// <script> tags for the two that are not: the hi-res image list ('colorImages')
// and the variation widget's initial data ('dataToReturn'). Both are JSON
// embedded in JavaScript, found by name and cut out with a brace counter that
// respects strings, because a regular expression cannot balance braces.
//
// It returns a string rather than an object so the DevTools Protocol has
// nothing to serialise but text; a cyclic or huge object would otherwise be
// its problem.
const extractScript = `(() => {
  const q = (s, r) => (r || document).querySelector(s);
  const qa = (s, r) => Array.from((r || document).querySelectorAll(s));
  // Text of an element with its <script> and <style> children left out:
  // Amazon's overview table carries an inline script in the cell, and
  // "Material: (function(f){…" is not a material.
  const text = el => {
    if (!el) return '';
    let s = '';
    const walk = n => {
      if (n.nodeType === 3) { s += n.nodeValue; return; }
      if (n.nodeType !== 1) return;
      const tag = n.tagName;
      if (tag === 'SCRIPT' || tag === 'STYLE' || tag === 'NOSCRIPT' || tag === 'TEMPLATE') return;
      for (const c of n.childNodes) walk(c);
    };
    walk(el);
    return s.replace(/[‎‏]/g, '').replace(/\s+/g, ' ').trim();
  };
  const out = { url: location.href, blocked: false };
  const title = document.title || '';
  out.doc_title = title;
  out.sample = text(document.body).slice(0, 400);

  // The robot check proper, and its quieter cousin: a page with nothing on it
  // but a "Continue shopping" button, which Amazon serves a browser it has
  // not seen before. Both are Amazon asking for a person; neither is a
  // product, and the fix for both is the same headed visit.
  const continueButton = qa('button, input[type=submit]').some(b => /continue shopping/i.test(text(b) || b.value || ''));
  if (/robot check|captcha/i.test(title) || q('#captchacharacters') || q('form[action*="validateCaptcha"]')
      || (continueButton && !q('#productTitle'))) {
    out.blocked = true;
    return JSON.stringify(out);
  }

  const asinInput = q('#ASIN');
  const pathAsin = (location.pathname.match(/\/(?:dp|gp\/product)\/([A-Z0-9]{10})/) || [])[1] || '';
  out.asin = (asinInput && asinInput.value) || pathAsin;
  out.title = text(q('#productTitle'));
  out.brand = text(q('#bylineInfo')).replace(/^(visit the|brand:)\s*/i, '').replace(/\s*store$/i, '');
  out.bullets = qa('#feature-bullets li:not(.aok-hidden) span.a-list-item').map(text).filter(Boolean);
  if (!out.bullets.length) out.bullets = qa('#feature-bullets ul li').map(text).filter(Boolean);
  if (!out.bullets.length) out.bullets = qa('#productFactsDesktopExpander ul li span.a-list-item, #productFactsDesktop_feature_div ul li').map(text).filter(Boolean);
  out.description = text(q('#productDescription')) || text(q('#aplus_feature_div'));

  const specs = [];
  const push = (k, v) => { k = (k || '').replace(/:$/, '').trim(); v = (v || '').trim(); if (k && v && k !== v) specs.push([k, v]); };
  qa('#productDetails_techSpec_section_1 tr, #productDetails_detailBullets_sections1 tr, #prodDetails table tr').forEach(tr => push(text(q('th', tr)), text(q('td', tr))));
  qa('#poExpander tr, #productOverview_feature_div tr').forEach(tr => {
    const cells = qa('td', tr);
    if (cells.length < 2) return;
    // The value cell holds the text twice — a truncated span and a full one
    // — plus a "See more" control. The full span alone is the value.
    const last = cells[cells.length - 1];
    const full = q('.a-truncate-full', last);
    push(text(cells[0]), text(full || last).replace(/\s*See more$/i, ''));
  });
  qa('#detailBullets_feature_div li').forEach(li => { const parts = text(li).split(/\s*:\s*/); if (parts.length >= 2) push(parts[0], parts.slice(1).join(': ')); });
  // The newer "product facts" layout — Fabric type, Care instructions, Origin —
  // is a grid of left/right columns rather than a table, and lives above the
  // "About this item" bullets on apparel listings.
  qa('#productFactsDesktopExpander .a-fixed-left-grid, #productFactsDesktop_feature_div .a-fixed-left-grid').forEach(row => {
    push(text(q('.a-col-left', row)), text(q('.a-col-right', row)));
  });
  qa('#productFactsDesktopExpander .product-facts-detail, #productFactsDesktop_feature_div .product-facts-detail').forEach(row => {
    const cols = qa('.a-col-left, .a-col-right', row);
    if (cols.length >= 2) push(text(cols[0]), text(cols[1]));
  });
  out.specs = specs;

  // A price is read from the buy box and nowhere else: the page also carries
  // "similar items" and "frequently bought together", each with an .a-price,
  // and the first of those is not this product's price. Amazon prints a
  // price twice — an .a-offscreen span with the whole thing, and visible
  // whole/fraction spans — and the offscreen one is taken when present,
  // because the visible pair lose their decimal point when read as text.
  const priceOf = box => {
    if (!box) return '';
    const off = q('.a-price .a-offscreen', box);
    if (off) return text(off);
    const whole = q('.a-price-whole', box), fraction = q('.a-price-fraction', box);
    if (whole) {
      const symbol = text(q('.a-price-symbol', box));
      return symbol + text(whole).replace(/[.,]$/, '') + (fraction ? '.' + text(fraction) : '');
    }
    return text(q('#price_inside_buybox, #priceblock_ourprice, #priceblock_dealprice', box));
  };
  out.price = '';
  for (const sel of ['#corePriceDisplay_desktop_feature_div', '#corePrice_feature_div', '#apex_desktop', '#buybox', '#desktop_buybox', '#price', '#tp_price_block_total_price_ww']) {
    out.price = priceOf(q(sel));
    if (out.price) break;
  }
  const buyboxText = text(q('#buybox') || q('#desktop_buybox') || q('#availability'));
  out.unshippable = /cannot be shipped to your selected delivery location|does not ship to/i.test(buyboxText);
  out.available = !/currently unavailable/i.test(text(q('#availability')));
  out.breadcrumbs = qa('#wayfinding-breadcrumbs_feature_div a').map(text).filter(Boolean);

  // Cut a balanced {...} or [...] out of JavaScript source, starting at 'from'.
  const balanced = (src, from) => {
    const open = src[from], close = open === '{' ? '}' : ']';
    let depth = 0, inStr = null;
    for (let i = from; i < src.length; i++) {
      const ch = src[i];
      if (inStr) { if (ch === '\\') i++; else if (ch === inStr) inStr = null; continue; }
      if (ch === '"' || ch === "'") { inStr = ch; continue; }
      if (ch === open) depth++;
      else if (ch === close) { depth--; if (depth === 0) return src.slice(from, i + 1); }
    }
    return null;
  };
  const scripts = qa('script').map(s => s.textContent || '');

  let images = [];
  for (const src of scripts) {
    const at = src.indexOf("'colorImages'");
    if (at < 0) continue;
    const init = src.indexOf("'initial'", at);
    const start = src.indexOf('[', init);
    const chunk = init > 0 && start > 0 ? balanced(src, start) : null;
    if (!chunk) continue;
    try {
      for (const it of JSON.parse(chunk)) {
        let u = it.hiRes || it.large;
        if (!u && it.main) {
          const keys = Object.keys(it.main).sort((a, b) => (it.main[b][0] || 0) - (it.main[a][0] || 0));
          u = keys[0];
        }
        if (u) images.push(u);
      }
    } catch (e) {}
    if (images.length) break;
  }
  if (!images.length) {
    images = qa('#altImages img').map(i => (i.getAttribute('src') || '').replace(/\._[^./]*_\./, '.'))
      .filter(u => u && !/sprite|play-icon|360_icon|\.gif/i.test(u));
  }
  if (!images.length) {
    const li = q('#landingImage');
    if (li) {
      const dyn = li.getAttribute('data-a-dynamic-image');
      if (dyn) { try { images = Object.keys(JSON.parse(dyn)); } catch (e) { images = [li.src]; } } else images = [li.src];
    }
  }
  out.images = Array.from(new Set(images.filter(Boolean)));

  // Ratings and reviews, for the record: the average and the count from the
  // summary, and every review the page shows in full.
  const stars = s => { const m = (s || '').match(/([0-9][.,][0-9])/); return m ? parseFloat(m[1].replace(',', '.')) : 0; };
  out.rating = stars(text(q('#acrPopover .a-icon-alt') || q('#averageCustomerReviews .a-icon-alt') || q('[data-hook="rating-out-of-text"]')));
  const countMatch = text(q('#acrCustomerReviewText') || q('[data-hook="total-review-count"]')).replace(/[,.]/g, '').match(/(\d+)/);
  out.review_count = countMatch ? parseInt(countMatch[1], 10) : 0;
  out.reviews = qa('[data-hook="review"], div[id^="customer_review-"], li[data-hook="review"]').slice(0, 20).map(r => ({
    title: text(q('[data-hook="review-title"]', r)).replace(/^[0-9][.,][0-9] out of 5 stars\s*/i, ''),
    rating: stars(text(q('[data-hook="review-star-rating"] .a-icon-alt', r) || q('[data-hook="cmps-review-star-rating"] .a-icon-alt', r))),
    author: text(q('.a-profile-name', r)),
    date: text(q('[data-hook="review-date"]', r)),
    body: text(q('[data-hook="review-body"]', r)).slice(0, 4000),
    verified: !!q('[data-hook="avp-badge"]', r)
  })).filter(rv => rv.body);

  out.debug = {
    prices: qa('.a-price .a-offscreen, #price_inside_buybox, #priceblock_ourprice, .priceToPay').slice(0, 6).map(text),
    apex: text(q('#apex_desktop')).slice(0, 160),
    buybox: text(q('#buybox') || q('#desktop_buybox')).slice(0, 160),
    widgets: ['#twister', '#twister_feature_div', '#inline-twister-expander-content', '#variation_color_name', '#variation_size_name']
      .filter(sel => q(sel)),
    twister_keys: []
  };

  // The widget's data is a JavaScript object literal, not JSON — it carries
  // values JSON.parse refuses — so the members this needs are cut out by
  // name, each of which is plain JSON on its own.
  const member = (src, key) => {
    const at = src.indexOf('"' + key + '"');
    if (at < 0) return null;
    const colon = src.indexOf(':', at + key.length + 2);
    if (colon < 0) return null;
    let i = colon + 1;
    while (i < src.length && /\s/.test(src[i])) i++;
    const ch = src[i];
    let raw = null;
    if (ch === '{' || ch === '[') raw = balanced(src, i);
    else if (ch === '"') { const m = src.slice(i).match(/^"(?:[^"\\]|\\.)*"/); raw = m ? m[0] : null; }
    if (raw === null) return null;
    try { return JSON.parse(raw); } catch (e) { out.debug.twister_error = key + ': ' + String(e).slice(0, 100); return null; }
  };
  out.twister = null;
  for (const src of scripts) {
    const at = src.indexOf('dimensionValuesDisplayData');
    if (at < 0) continue;
    const values = member(src, 'dimensionValuesDisplayData');
    // The axis names come from three places depending on the page's vintage:
    // "dimensions" (the keys), "variationValues" (the keys again, with the
    // values), and "variationDisplayLabels" (key → label). Older pages also
    // carried "dimensionsDisplay", a plain list of labels; current ones do
    // not, which is why the labels object is the one that is relied on.
    const labelsObj = member(src, 'variationDisplayLabels') || {};
    const order = member(src, 'variationValues') || {};
    let keys = member(src, 'dimensions') || Object.keys(order);
    if (!keys.length) keys = Object.keys(labelsObj);
    const display = member(src, 'dimensionsDisplay') || keys.map(k => labelsObj[k] || k);
    if (!values || !keys.length) {
      // Which half was missing, and what the source looked like there — the
      // markup has changed if this ever fires, and this is the evidence.
      out.debug.twister_missing = (values ? '' : 'dimensionValuesDisplayData ') + (keys.length ? '' : 'dimensions');
      out.debug.twister_window = src.slice(Math.max(0, at - 80), at + 220);
      continue;
    }
    const labels = {};
    keys.forEach((k, i) => { labels[k] = labelsObj[k] || display[i] || k; });
    out.debug.twister_keys = keys;
    out.twister = {
      dimensions: keys,
      labels: labels,
      values: values,
      order: member(src, 'variationValues') || {},
      current: member(src, 'currentAsin') || out.asin,
      parent: member(src, 'parentAsin') || ''
    };
    break;
  }
  return JSON.stringify(out);
})()`
