package showcases

import (
	"strings"
	"testing"
)

// Every way YouTube shares a video's address is the same video; the player is
// always YouTube's own privacy-enhanced embed, never what was pasted.
func TestAYouTubeLinkBecomesItsOwnEmbed(t *testing.T) {
	for _, in := range []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com/watch?v=dQw4w9WgXcQ&t=42s&list=PL123",
		"http://m.youtube.com/watch?feature=share&v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?si=abc",
		"https://www.youtube.com/live/dQw4w9WgXcQ?feature=shared",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ",
		"https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ",
		"  www.youtube.com/watch?v=dQw4w9WgXcQ  ",
	} {
		v, err := parseVideo(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if v.Provider != ProviderYouTube || v.Ref != "dQw4w9WgXcQ" {
			t.Errorf("%q = %s %q", in, v.Provider, v.Ref)
		}
	}
	v, _ := parseVideo("https://youtu.be/dQw4w9WgXcQ")
	got := describe(v)
	if got.EmbedURL != "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ" ||
		got.WatchURL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" ||
		got.ThumbnailURL != "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg" {
		t.Errorf("describe = %+v", got)
	}
}

// A Facebook video is played through Facebook's own video plugin, pointed at
// the video's canonical address.
func TestAFacebookLinkBecomesItsVideoPlugin(t *testing.T) {
	for in, canonical := range map[string]string{
		"https://www.facebook.com/varnijewels/videos/1234567890123456/":              "https://www.facebook.com/varnijewels/videos/1234567890123456/",
		"https://facebook.com/varnijewels/videos/diwali-collection/1234567890123456": "https://www.facebook.com/varnijewels/videos/1234567890123456/",
		"https://m.facebook.com/watch/?v=1234567890123456":                           "https://www.facebook.com/watch/?v=1234567890123456",
		"https://www.facebook.com/watch?v=1234567890123456&ref=sharing":              "https://www.facebook.com/watch/?v=1234567890123456",
		"https://web.facebook.com/video.php?v=1234567890123456":                      "https://www.facebook.com/watch/?v=1234567890123456",
		"https://www.facebook.com/reel/1234567890123456":                             "https://www.facebook.com/reel/1234567890123456",
	} {
		v, err := parseVideo(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if v.Provider != ProviderFacebook || v.Ref != canonical {
			t.Errorf("%q = %s %q, want %q", in, v.Provider, v.Ref, canonical)
		}
	}
	v, _ := parseVideo("https://www.facebook.com/varnijewels/videos/1234567890123456/")
	got := describe(v)
	want := "https://www.facebook.com/plugins/video.php?href=https%3A%2F%2Fwww.facebook.com%2Fvarnijewels%2Fvideos%2F1234567890123456%2F&show_text=false"
	if got.EmbedURL != want || got.WatchURL != v.Ref || got.ThumbnailURL != "" {
		t.Errorf("describe = %+v", got)
	}
}

// What is not a video on a supported platform is refused with what to paste
// instead — never stored, and never turned into a player.
func TestWhatIsNotASupportedVideoIsRefused(t *testing.T) {
	for in, says := range map[string]string{
		"":                    "paste",
		"javascript:alert(1)": "YouTube or Facebook",
		"https://evil.example/watch?v=dQw4w9WgXcQ":               "YouTube or Facebook",
		"https://youtube.com.evil.example/watch?v=dQw4w9WgXcQ":   "YouTube or Facebook",
		"https://user@www.youtube.com/watch?v=dQw4w9WgXcQ":       "YouTube or Facebook",
		"ftp://www.youtube.com/watch?v=dQw4w9WgXcQ":              "YouTube or Facebook",
		"https://www.youtube.com/@varnijewels/live":              "the video's own link",
		"https://www.youtube.com/channel/UC123":                  "the video's own link",
		"https://www.youtube.com/watch?v=short":                  "the video's own link",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ\"><script>": "the video's own link",
		"https://fb.watch/abcDEF/":                               "address bar",
		"https://www.facebook.com/share/v/abc123/":               "address bar",
		"https://www.facebook.com/varnijewels":                   "the video's own link",
		"https://www.instagram.com/reel/abc/":                    "YouTube or Facebook",
		"https://www.tiktok.com/@shop/video/123":                 "YouTube or Facebook",
	} {
		_, err := parseVideo(in)
		if err == nil {
			t.Errorf("%q was accepted", in)
			continue
		}
		if !strings.Contains(err.Error(), says) {
			t.Errorf("%q: %v, want it to say %q", in, err, says)
		}
	}
}
