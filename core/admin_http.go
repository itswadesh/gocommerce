package gocommerce

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/itswadesh/gocommerce/admin"
)

// AdminPanelPath is where the admin panel is served.
//
// The root: the whole API lives under /api (plus /health, /doc, and a module's
// /x/), so nothing else wants this URL. Typing the store's address gets you the
// dashboard, which is what a person expects.
//
// The consequence, stated plainly: this binary cannot also host a storefront at
// the root. A headless store's storefront is a separate application anyway —
// put it on its own origin, or in front of this one behind a proxy.
const AdminPanelPath = "/"

// apiPrefixes are the paths the API owns. A request under one of these that
// matched no route is a missing endpoint, and must get the JSON error every
// other endpoint gives — not the panel's HTML.
//
// Without this, the root catch-all below would answer GET /api/typo with an
// index.html, and a client decoding JSON would report a syntax error instead of
// "no route for GET /api/typo".
var apiPrefixes = []string{"/api", "/health", "/doc", "/docs", "/x", "/internal"}

func isAPIPath(p string) bool {
	for _, prefix := range apiPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// mountAdminPanel serves the embedded admin panel, if this binary has one.
//
// A build with -tags no_admin has no panel and mounts nothing, so an API-only
// deployment does not carry a megabyte of JavaScript it will never serve — and
// the root stays free.
func (a *App) mountAdminPanel() {
	if admin.DistFS == nil {
		a.log.Debug("built without the admin panel; the root is not served")
		return
	}

	// The panel used to live at /_/, matching PocketBase. Keep those URLs
	// working rather than breaking a bookmark.
	redirect := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, AdminPanelPath, http.StatusMovedPermanently)
	}
	a.handleUI("GET /_", redirect)
	a.handleUI("GET /_/{path...}", redirect)

	// A catch-all. Go's ServeMux prefers the most specific pattern, so every
	// real API route still wins over this one.
	a.handleUI("GET /{path...}", a.handleAdminPanel)

	a.log.Info("admin panel available", "path", AdminPanelPath)
}

// handleUI mounts a route that serves the panel's own files, marking it so the
// OpenAPI coverage check knows it is not an API endpoint.
func (a *App) handleUI(pattern string, h http.HandlerFunc) {
	a.HandleFunc(pattern, h)
	if n := len(a.routes); n > 0 {
		a.routes[n-1].UI = true
	}
}

func (a *App) handleAdminPanel(w http.ResponseWriter, r *http.Request) {
	// Only a document obeys a policy, and only the shell is one: a script or
	// a font is served without the store lookup.
	var frames []string
	if path.Ext(r.URL.Path) == "" {
		frames = a.panelFrameOrigins(r)
	}
	serveAdminPanel(w, r, frames)
}

// panelFrameOrigins are the storefront addresses the panel may frame (D126):
// those of the store the shell is opened for, and the origins the server's
// owner listed in CORSOrigins, which name the storefronts it trusts.
//
// The shell is fetched by a browser navigation, which sends no X-Store, so
// the store is the one the address names (?store=), else the store cookie,
// else the host, else the install's only store — resolveStore's order. A
// document's policy is fixed when it loads, so the theme editor reloads
// itself with ?store= when this guess was another store than its tab's.
// Naming a store here widens framing to that store's public addresses and
// nothing else: no data, no right.
func (a *App) panelFrameOrigins(r *http.Request) []string {
	var out []string
	for _, o := range a.cfg.CORSOrigins {
		if frameOrigin(o) {
			out = append(out, strings.TrimSuffix(o, "/"))
		}
	}
	st, err := a.resolveStore(r)
	if err != nil || st == nil {
		return out
	}
	for _, o := range a.storefrontOrigins(WithStore(r.Context(), st.ID)) {
		if frameOrigin(o) && !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out
}

// frameOriginRE is a bare origin: scheme, host, optional port. Anything else
// — a wildcard, a path, a stray semicolon — could change the policy it is
// written into, so it is left out rather than escaped.
var frameOriginRE = regexp.MustCompile(`^https?://[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?/?$`)

func frameOrigin(o string) bool { return frameOriginRE.MatchString(o) }

// ServeAdminPanel serves the embedded panel's files — index.html for a
// client-side route — with the cache rules and Content-Security-Policy a
// store's own host sends. It is exported so a program that mounts the engine
// under its own mux serves the panel with the same policy.
// A binary built without the panel answers a JSON 404.
func ServeAdminPanel(w http.ResponseWriter, r *http.Request) { serveAdminPanel(w, r, nil) }

func serveAdminPanel(w http.ResponseWriter, r *http.Request, frames []string) {
	if admin.DistFS == nil {
		RespondError(w, r, NotFoundf("this binary was built without the admin panel"))
		return
	}
	requested := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))

	// An unmatched API path is a missing endpoint, not a client-side route.
	if isAPIPath(requested) {
		RespondError(w, r, NotFoundf(
			"no route for %s %s — see /docs for what this store serves", r.Method, r.URL.Path))
		return
	}

	name := strings.TrimPrefix(requested, "/")
	if name == "" || name == "." {
		name = "index.html"
	}

	file, err := admin.DistFS.Open(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			RespondError(w, r, Internalf(err, "could not read the admin panel"))
			return
		}
		// Single-page app: an unknown path is a client-side route, so hand
		// back index.html and let the panel route it. A missing *asset* is a
		// build problem, and answering those with HTML would hide it — so only
		// extension-less paths fall through.
		if path.Ext(name) != "" {
			RespondError(w, r, NotFoundf("no such file in the admin panel: %s", name))
			return
		}
		name = "index.html"
		file, err = admin.DistFS.Open(name)
		if err != nil {
			RespondError(w, r, Internalf(err, "the admin panel is missing its index.html"))
			return
		}
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		file.Close()
		name = "index.html"
		file, err = admin.DistFS.Open(name)
		if err != nil {
			RespondError(w, r, NotFoundf("not found"))
			return
		}
		defer file.Close()
		info, err = file.Stat()
		if err != nil {
			RespondError(w, r, Internalf(err, "could not read the admin panel"))
			return
		}
	}

	setAdminCacheHeaders(w, name)

	// Lock the panel to its own origin so a compromised dependency could not
	// exfiltrate an admin token.
	//
	// script-src permits inline here because SvelteKit emits one inline
	// bootstrap script that cannot be removed. That is not the end of the
	// story: the page also carries a <meta> CSP listing the SHA-256 of that
	// exact script (see admin/svelte.config.js). Both policies are enforced
	// and a script must satisfy both, so the hash is what actually decides —
	// an injected inline script is refused by the meta policy even though this
	// header would have allowed it.
	//
	// frame-ancestors lives here rather than in the meta, because meta CSP
	// ignores it.
	//
	// Images are the one thing let off the origin, because the catalogue is not
	// all on it: the library links pictures it does not hold (POST
	// /api/admin/media/link, and every URL a product import names), and a
	// panel that refuses them shows an imported catalogue as broken
	// thumbnails. What that costs is one more way out for a compromised
	// dependency, since an image URL can carry a token — but a top-level
	// navigation or a form post could already, so this policy never stopped
	// sending. What it does stop, script from elsewhere and fetches that read
	// a reply, is unchanged.
	//
	// Frames are let in from exactly two players, YouTube's privacy-enhanced
	// one and Facebook's video plugin, so the showcase editor (D88) can play
	// the video it is attaching products to. A frame cannot read the panel or
	// its token; the addresses are built on the server from a video's id,
	// never pasted.
	//
	// And from the storefront of the store being worked on (frames), so the
	// theme editor can show the real home page as it is edited (D126). A frame
	// is another origin: it cannot read the panel or its token, and it talks
	// to the panel only through messages the panel checks the origin of.
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'unsafe-inline'; "+
			"style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https: http:; "+
			"font-src 'self'; connect-src 'self'; base-uri 'self'; "+
			"frame-src https://www.youtube-nocookie.com https://www.facebook.com"+frameList(frames)+"; "+
			"object-src 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")

	readSeeker, ok := file.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	})
	if !ok {
		http.Error(w, "unreadable asset", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), readSeeker)
}

// setAdminCacheHeaders caches fingerprinted assets hard and the shell not at
// all. SvelteKit hashes everything under _app/immutable, so those filenames
// change whenever their contents do; index.html must never be cached or a
// deploy would not reach anyone.
func setAdminCacheHeaders(w http.ResponseWriter, name string) {
	switch {
	case strings.HasPrefix(name, "_app/immutable/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case strings.HasPrefix(name, "fonts/"):
		w.Header().Set("Cache-Control", "public, max-age=1209600")
	case name == "index.html":
		w.Header().Set("Cache-Control", "no-cache")
	default:
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
}

// HasAdminPanel reports whether this binary was built with the panel.
func HasAdminPanel() bool { return admin.DistFS != nil }

func frameList(origins []string) string {
	if len(origins) == 0 {
		return ""
	}
	return " " + strings.Join(origins, " ")
}
