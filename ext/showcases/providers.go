package showcases

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The platforms a showcase can play from, and how a pasted address becomes
// that platform's own player.
//
// Only addresses the platform documents for sharing a video are accepted, and
// what is kept is a reference — YouTube's video id, Facebook's canonical video
// address — from which the player's address is built every time it is read. A
// pasted address is never put on a page, so nothing a merchant types can
// become markup, and a change of embed format reaches every showcase at once.

// Providers.
const (
	ProviderYouTube  = "youtube"
	ProviderFacebook = "facebook"
)

// video is a recognised video: its platform and the reference it is kept by.
type video struct {
	Provider string
	Ref      string
}

// Video is how a showcase's video is played and linked.
type Video struct {
	Provider string `json:"provider"`
	// URL is the address the store pasted.
	URL string `json:"url,omitempty"`
	// EmbedURL is the platform's own player, for an iframe.
	EmbedURL string `json:"embed_url"`
	// WatchURL opens the video on the platform, for when the player cannot.
	WatchURL string `json:"watch_url"`
	// ThumbnailURL is the platform's still for the video, where it has one
	// at an address that can be worked out; Facebook's needs its API.
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
}

var youTubeHosts = map[string]bool{
	"youtube.com": true, "www.youtube.com": true, "m.youtube.com": true,
	"youtube-nocookie.com": true, "www.youtube-nocookie.com": true,
}

var facebookHosts = map[string]bool{
	"facebook.com": true, "www.facebook.com": true, "m.facebook.com": true, "web.facebook.com": true,
}

var (
	youTubeID  = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	facebookID = regexp.MustCompile(`^[0-9]{5,25}$`)
	pageName   = regexp.MustCompile(`^[A-Za-z0-9.\-]{1,100}$`)
)

func unsupported() error {
	return gocommerce.Validationf("paste a link to a video on YouTube or Facebook")
}

// parseVideo recognises a pasted address, or says what to paste instead.
func parseVideo(raw string) (video, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return video{}, gocommerce.Validationf("paste the link to the video")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return video{}, unsupported()
	}
	host := strings.ToLower(u.Hostname())
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case host == "youtu.be":
		return youTube(parts[0])
	case youTubeHosts[host]:
		if parts[0] == "watch" {
			return youTube(u.Query().Get("v"))
		}
		if len(parts) == 2 && (parts[0] == "live" || parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "v") {
			return youTube(parts[1])
		}
		return video{}, gocommerce.Validationf(
			"that is a channel or page on YouTube; paste the video's own link (Share → Copy link on the video)")
	case host == "fb.watch" || (facebookHosts[host] && parts[0] == "share"):
		// A short share link only says where it goes by redirecting there,
		// and following it from the server would fetch an address a
		// merchant chose.
		return video{}, gocommerce.Validationf(
			"that is a short share link; open the video and paste its address from the browser's address bar")
	case facebookHosts[host]:
		return facebook(u, parts)
	}
	return video{}, unsupported()
}

func youTube(id string) (video, error) {
	if !youTubeID.MatchString(id) {
		return video{}, gocommerce.Validationf(
			"that link has no video in it; paste the video's own link (Share → Copy link on the video)")
	}
	return video{Provider: ProviderYouTube, Ref: id}, nil
}

func facebook(u *url.URL, parts []string) (video, error) {
	notVideo := gocommerce.Validationf(
		"that is a page on Facebook, not a video; open the video and paste the video's own link")
	switch {
	case parts[0] == "watch" || parts[0] == "video.php":
		if id := u.Query().Get("v"); facebookID.MatchString(id) {
			return video{Provider: ProviderFacebook, Ref: "https://www.facebook.com/watch/?v=" + id}, nil
		}
	case parts[0] == "reel" && len(parts) == 2 && facebookID.MatchString(parts[1]):
		return video{Provider: ProviderFacebook, Ref: "https://www.facebook.com/reel/" + parts[1]}, nil
	case len(parts) >= 3 && parts[1] == "videos" && pageName.MatchString(parts[0]):
		// /{page}/videos/{id} or /{page}/videos/{title}/{id}.
		if id := parts[len(parts)-1]; facebookID.MatchString(id) {
			return video{Provider: ProviderFacebook, Ref: "https://www.facebook.com/" + parts[0] + "/videos/" + id + "/"}, nil
		}
	}
	return video{}, notVideo
}

// describe is how a recognised video is played and linked.
func describe(v video) Video {
	switch v.Provider {
	case ProviderYouTube:
		return Video{
			Provider:     v.Provider,
			EmbedURL:     "https://www.youtube-nocookie.com/embed/" + v.Ref,
			WatchURL:     "https://www.youtube.com/watch?v=" + v.Ref,
			ThumbnailURL: "https://i.ytimg.com/vi/" + v.Ref + "/hqdefault.jpg",
		}
	case ProviderFacebook:
		return Video{
			Provider: v.Provider,
			EmbedURL: "https://www.facebook.com/plugins/video.php?href=" + url.QueryEscape(v.Ref) + "&show_text=false",
			WatchURL: v.Ref,
		}
	}
	return Video{Provider: v.Provider}
}

// Check is what the panel shows while a link is being pasted.
type Check struct {
	Video
	// Title is the video's own title, where the platform says it, to start
	// the showcase's title from.
	Title string `json:"title,omitempty"`
	// Problem is why the video will not play elsewhere, when the platform
	// says so; empty when it will, or when it could not be asked.
	Problem string `json:"problem,omitempty"`
}

// check asks YouTube, through its oEmbed endpoint, whether a video may be
// played on another site. A video whose owner turned that off shows an error
// in the player and nothing else, which a merchant should hear about before
// publishing rather than from a customer. Facebook's oEmbed needs an app
// token, so a Facebook video is checked by its preview alone.
func (m *Module) check(ctx context.Context, v video) Check {
	out := Check{Video: describe(v)}
	if v.Provider != ProviderYouTube {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	target := m.oembedURL() + "?format=json&url=" + url.QueryEscape(out.WatchURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return out
	}
	res, err := m.client().Do(req)
	if err != nil {
		// Not a refusal: YouTube being slow says nothing about the video.
		return out
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		var body struct {
			Title string `json:"title"`
		}
		if json.NewDecoder(res.Body).Decode(&body) == nil {
			out.Title = strings.TrimSpace(body.Title)
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		out.Problem = "The video's owner has turned off playing it on other sites. In YouTube Studio, open the video's details and allow embedding."
	case http.StatusNotFound, http.StatusBadRequest:
		out.Problem = "YouTube says this video is private or has been removed. Make it public or unlisted, or paste another."
	}
	return out
}

func (m *Module) oembedURL() string {
	if m.cfg.YouTubeOEmbedURL != "" {
		return strings.TrimRight(m.cfg.YouTubeOEmbedURL, "/")
	}
	return "https://www.youtube.com/oembed"
}

func (m *Module) client() *http.Client {
	if m.cfg.HTTPClient != nil {
		return m.cfg.HTTPClient
	}
	return http.DefaultClient
}
