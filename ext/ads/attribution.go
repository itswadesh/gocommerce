package ads

import "strings"

// Touch is one arrival at the storefront from somewhere: the link's UTM
// parameters, an ad platform's click ID, and the domain of the page the
// shopper came from. The storefront records it in the shopper's browser and
// the connector hands it to checkout as metadata.attribution, so the order
// keeps it like any other metadata, written once by the shopper's own
// checkout.
type Touch struct {
	UTMSource   string `json:"utm_source,omitempty"`
	UTMMedium   string `json:"utm_medium,omitempty"`
	UTMCampaign string `json:"utm_campaign,omitempty"`
	UTMContent  string `json:"utm_content,omitempty"`
	UTMTerm     string `json:"utm_term,omitempty"`
	GCLID       string `json:"gclid,omitempty"`
	GBRAID      string `json:"gbraid,omitempty"`
	WBRAID      string `json:"wbraid,omitempty"`
	FBCLID      string `json:"fbclid,omitempty"`
	TTCLID      string `json:"ttclid,omitempty"`
	MSCLKID     string `json:"msclkid,omitempty"`
	Referrer    string `json:"referrer,omitempty"`
	At          string `json:"at,omitempty"`
}

// Attribution is the first arrival with a source in the window the
// storefront keeps (30 days), and the latest one.
type Attribution struct {
	First *Touch `json:"first,omitempty"`
	Last  *Touch `json:"last,omitempty"`
}

func (a *Attribution) touch(which string) *Touch {
	if a == nil {
		return nil
	}
	if which == "first" {
		if a.First != nil {
			return a.First
		}
		return a.Last
	}
	if a.Last != nil {
		return a.Last
	}
	return a.First
}

// channel is a report row's key.
type channel struct {
	Source, Medium, Campaign string
}

func (c channel) key() channel {
	return channel{strings.ToLower(c.Source), strings.ToLower(c.Medium), strings.ToLower(c.Campaign)}
}

const (
	// notRecorded is an order placed with no attribution: before the
	// storefront recorded it, by an operator, or through another client.
	notRecorded = "(not recorded)"
	// direct is an arrival with nothing to say where from: a typed address,
	// a bookmark, an app that sends no referrer.
	direct = "(direct)"
	// The longest value kept: the metadata is the shopper's to write.
	maxValue = 100
)

// Referrers whose visits are a search engine's unpaid results, or a social
// site's links, named as GA4 names them.
// ponytail: a short list of the big ones; any other domain is a referral by its own name.
var (
	searchEngines = map[string]string{"google": "google", "bing": "bing", "duckduckgo": "duckduckgo",
		"yahoo": "yahoo", "yandex": "yandex", "baidu": "baidu", "ecosia": "ecosia"}
	socialSites = map[string]string{"facebook": "facebook", "fb": "facebook", "instagram": "instagram",
		"twitter": "twitter", "linkedin": "linkedin", "lnkd": "linkedin",
		"pinterest": "pinterest", "youtube": "youtube", "reddit": "reddit", "whatsapp": "whatsapp"}
)

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxValue {
		return string(r[:maxValue])
	}
	return s
}

// channel names where a touch came from. A tagged link says so itself; a
// click ID without tags is the ad platform that adds it; otherwise the
// referrer's domain.
func (t *Touch) channel() channel {
	if t == nil {
		return channel{Source: notRecorded}
	}
	if s := clip(t.UTMSource); s != "" {
		return channel{strings.ToLower(s), strings.ToLower(clip(t.UTMMedium)), clip(t.UTMCampaign)}
	}
	campaign := clip(t.UTMCampaign)
	switch {
	case t.GCLID != "" || t.GBRAID != "" || t.WBRAID != "":
		return channel{"google", "cpc", campaign}
	case t.MSCLKID != "":
		return channel{"bing", "cpc", campaign}
	case t.TTCLID != "":
		return channel{"tiktok", "cpc", campaign}
	case t.FBCLID != "":
		// Meta adds fbclid to every link that leaves it, paid or not, so it
		// says Facebook and not that an ad was clicked.
		return channel{"facebook", "social", campaign}
	}
	host := strings.TrimPrefix(strings.ToLower(clip(t.Referrer)), "www.")
	if host == "" {
		return channel{direct, "", campaign}
	}
	for _, label := range strings.Split(host, ".") {
		if name, ok := searchEngines[label]; ok {
			return channel{name, "organic", campaign}
		}
	}
	if host == "t.co" || host == "x.com" {
		return channel{"twitter", "social", campaign}
	}
	for _, label := range strings.Split(host, ".") {
		if name, ok := socialSites[label]; ok {
			return channel{name, "social", campaign}
		}
	}
	return channel{host, "referral", campaign}
}
