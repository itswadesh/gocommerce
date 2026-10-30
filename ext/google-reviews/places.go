package googlereviews

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Place is the store's Google Business Profile as Google answered just now:
// developers.google.com/maps/documentation/places/web-service/reference/rest/v1/places
type Place struct {
	// Rating is Google's, 1.0 to 5.0; UserRatingCount the number of ratings
	// behind it. Both are Google's figures, never recomputed from Reviews.
	Rating          float64 `json:"rating"`
	UserRatingCount int     `json:"user_rating_count"`
	// GoogleMapsURI is where every review is. A storefront links to it: the
	// reviews below are at most five of them.
	GoogleMapsURI string `json:"google_maps_uri"`
	// Reviews are as Google sent them, in its order (most relevant first),
	// none left out.
	Reviews []Review `json:"reviews"`
}

// Review is one Google review with what its attribution needs: Google's
// policy is to credit the author with name, photo and profile link, and to
// let the shopper open the review on Google Maps
// (developers.google.com/maps/documentation/places/web-service/policies).
type Review struct {
	Author Author  `json:"author"`
	Rating float64 `json:"rating"`
	// Text is the reviewer's own words in their language, not Google's
	// translation, so nothing shown needs a "translated" notice. Empty for a
	// rating with no words.
	Text         string `json:"text,omitempty"`
	Language     string `json:"language,omitempty"`
	RelativeTime string `json:"relative_time,omitempty"`
	PublishTime  string `json:"publish_time,omitempty"`
	// VisitDate is YYYY-MM, when Google has one; the policy asks for it to be
	// shown for places in France.
	VisitDate     string `json:"visit_date,omitempty"`
	GoogleMapsURI string `json:"google_maps_uri,omitempty"`
	// FlagURI is where a shopper reports the review to Google.
	FlagURI string `json:"flag_uri,omitempty"`
}

// Author is a review's author. The links are https or empty.
type Author struct {
	Name     string `json:"name"`
	URI      string `json:"uri,omitempty"`
	PhotoURI string `json:"photo_uri,omitempty"`
}

// placeFields is the field mask: what is shown and nothing more, because
// Google bills by the fields asked for (reviews make it Place Details
// Enterprise + Atmosphere).
const placeFields = "rating,userRatingCount,googleMapsUri,reviews"

type localizedText struct {
	Text         string `json:"text"`
	LanguageCode string `json:"languageCode"`
}

type googlePlace struct {
	Rating          float64 `json:"rating"`
	UserRatingCount int     `json:"userRatingCount"`
	GoogleMapsURI   string  `json:"googleMapsUri"`
	Reviews         []struct {
		RelativePublishTimeDescription string         `json:"relativePublishTimeDescription"`
		Rating                         float64        `json:"rating"`
		Text                           *localizedText `json:"text"`
		OriginalText                   *localizedText `json:"originalText"`
		AuthorAttribution              struct {
			DisplayName string `json:"displayName"`
			URI         string `json:"uri"`
			PhotoURI    string `json:"photoUri"`
		} `json:"authorAttribution"`
		PublishTime    string `json:"publishTime"`
		FlagContentURI string `json:"flagContentUri"`
		GoogleMapsURI  string `json:"googleMapsUri"`
		VisitDate      *struct {
			Year, Month int
		} `json:"visitDate"`
	} `json:"reviews"`
}

type googleError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// fetchPlace asks Google for the place. The key goes in a header, never the
// URL, so no error that quotes the URL can carry it.
func (m *Module) fetchPlace(ctx context.Context, key, placeID string) (*Place, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.PlacesURL+"/v1/places/"+url.PathEscape(placeID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Goog-Api-Key", key)
	req.Header.Set("X-Goog-FieldMask", placeFields)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e googleError
		_ = json.Unmarshal(body, &e)
		return nil, fmt.Errorf("places api: %d %s: %s", resp.StatusCode, e.Error.Status, e.Error.Message)
	}
	var g googlePlace
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("places api: %w", err)
	}
	p := &Place{Rating: g.Rating, UserRatingCount: g.UserRatingCount, GoogleMapsURI: httpsOnly(g.GoogleMapsURI), Reviews: []Review{}}
	for _, r := range g.Reviews {
		rv := Review{
			Author: Author{Name: r.AuthorAttribution.DisplayName, URI: httpsOnly(r.AuthorAttribution.URI),
				PhotoURI: httpsOnly(r.AuthorAttribution.PhotoURI)},
			Rating: r.Rating, RelativeTime: r.RelativePublishTimeDescription, PublishTime: r.PublishTime,
			GoogleMapsURI: httpsOnly(r.GoogleMapsURI), FlagURI: httpsOnly(r.FlagContentURI),
		}
		if t := r.OriginalText; t != nil && t.Text != "" {
			rv.Text, rv.Language = t.Text, t.LanguageCode
		} else if t := r.Text; t != nil {
			rv.Text, rv.Language = t.Text, t.LanguageCode
		}
		if d := r.VisitDate; d != nil && d.Year > 0 && d.Month > 0 {
			rv.VisitDate = fmt.Sprintf("%04d-%02d", d.Year, d.Month)
		}
		p.Reviews = append(p.Reviews, rv)
	}
	return p, nil
}

// httpsOnly keeps a link a storefront will put in an href or a src only when
// it is https: the answer comes from outside, and javascript: is a link too.
func httpsOnly(u string) string {
	if strings.HasPrefix(u, "https://") {
		return u
	}
	return ""
}

func (m *Module) handlePlace(w http.ResponseWriter, r *http.Request) {
	s, err := m.settings(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if !s.showPlace || s.apiKey == "" || s.placeID == "" {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("Google reviews are not shown on this store"))
		return
	}
	// Nothing Google sends may be cached (Maps Platform Terms 3.2.3(b)), by
	// this server or by anything between it and the shopper.
	w.Header().Set("Cache-Control", "no-store")
	store, _ := gocommerce.StoreFrom(r.Context())
	if !m.takeLookup(store, s.dailyLookups, time.Now()) {
		gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusTooManyRequests,
			Code: "daily_limit", Message: "this store's Google lookups for today are used up"})
		return
	}
	p, err := m.fetchPlace(r.Context(), s.apiKey, s.placeID)
	if err != nil {
		m.app.Log().Warn("google-reviews: read the place", "store", store, "err", err)
		gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusBadGateway,
			Code: "google_unavailable", Message: "Google did not answer with the store's reviews"})
		return
	}
	gocommerce.Respond(w, http.StatusOK, p)
}

type dayCount struct {
	day string
	n   int
}

// takeLookup counts one Google call against the store's daily limit, or
// says the limit is reached. The route is public and every call is billed to
// the store, so a stranger in a loop must not be able to spend without end.
//
// ponytail: counted per process and reset on restart; an install running
// several engines allows the limit on each. Move it to the database when one
// does.
func (m *Module) takeLookup(store string, limit int, now time.Time) bool {
	if limit <= 0 {
		return false
	}
	day := now.UTC().Format("2006-01-02")
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.lookups[store]
	if c.day != day {
		c = dayCount{day: day}
	}
	if c.n >= limit {
		return false
	}
	c.n++
	m.lookups[store] = c
	return true
}
