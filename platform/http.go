package platform

import (
	_ "embed"
	"net/http"
	"path"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

//go:embed openapi.json
var openapiDoc []byte

// route is one platform API endpoint. open marks the two a machine without a
// platform token must reach: the contract, and the TLS ask a proxy makes with
// nothing but a host name.
type route struct {
	pattern string
	handler http.HandlerFunc
	open    bool
}

// routeTable is the platform API. The mux and the contract test both read it,
// so a route cannot be served without being checked against the document.
func (p *Platform) routeTable() []route {
	return []route{
		{"GET /api/platform/doc", p.handleDoc, true},
		{"GET /api/platform/tls/allowed", p.handleTLSAllowed, true},
		{"GET /api/platform/health", p.handleHealth, false},
		{"GET /api/platform/tenants", p.handleTenants, false},
		{"POST /api/platform/tenants", p.handleProvision, false},
		{"GET /api/platform/tenants/{slug}", p.handleTenant, false},
		{"PATCH /api/platform/tenants/{slug}", p.handleUpdateTenant, false},
		{"DELETE /api/platform/tenants/{slug}", p.handleDeleteTenant, false},
		{"POST /api/platform/tenants/{slug}/domains", p.handleAddDomain, false},
		{"DELETE /api/platform/tenants/{slug}/domains/{domain}", p.handleRemoveDomain, false},
		{"POST /api/platform/tenants/{slug}/owners", p.handleAddOwner, false},
		{"POST /api/platform/tenants/{slug}/token", p.handleRotateToken, false},
	}
}

// routes is the platform API, served only on the platform's hosts.
func (p *Platform) routes() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range p.routeTable() {
		h := rt.handler
		if !rt.open {
			inner := h
			h = func(w http.ResponseWriter, r *http.Request) {
				if !p.authorized(r) {
					gocommerce.RespondError(w, r, gocommerce.ErrUnauthorized)
					return
				}
				inner(w, r)
			}
		}
		mux.HandleFunc(rt.pattern, h)
	}
	// Everything else on a platform host is the console or a JSON 404, never
	// a store.
	mux.HandleFunc("/", handleConsole)
	return mux
}

// consolePath is where the admin panel keeps the platform's own screens.
const consolePath = "/platform"

// handleConsole serves the platform console (D73): the panel's /platform
// screens and the asset files they load, through the same function a store's
// host serves its panel with. Only those — the rest of the panel is a store's,
// and its login form would ask a platform operator for a credential no store
// issued them. `/` goes to the console because the platform's address is what
// an operator types. Without a panel in the binary, all of it is a JSON 404.
func handleConsole(w http.ResponseWriter, r *http.Request) {
	clean := path.Clean("/" + r.URL.Path)
	readOnly := r.Method == http.MethodGet || r.Method == http.MethodHead
	switch {
	case !readOnly || !gocommerce.HasAdminPanel() || clean == "/api" || strings.HasPrefix(clean, "/api/"):
		// A write, an API path nobody serves, or no panel: the 404 below.
	case clean == "/":
		http.Redirect(w, r, consolePath, http.StatusFound)
		return
	case clean == consolePath || strings.HasPrefix(clean, consolePath+"/") || path.Ext(clean) != "":
		gocommerce.ServeAdminPanel(w, r)
		return
	}
	gocommerce.RespondError(w, r, gocommerce.NotFoundf("no platform route for %s %s", r.Method, r.URL.Path))
}

func (p *Platform) handleDoc(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(openapiDoc)
}

func respond(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, status, v)
}

func (p *Platform) handleTLSAllowed(w http.ResponseWriter, r *http.Request) {
	if p.TLSAllowed(r.URL.Query().Get("domain")) {
		gocommerce.Respond(w, http.StatusOK, map[string]bool{"allowed": true})
		return
	}
	gocommerce.RespondError(w, r, gocommerce.NotFoundf("this platform does not serve that host"))
}

type health struct {
	Stores    int      `json:"stores"`
	Running   int      `json:"running"`
	Unbooted  []string `json:"unbooted"`
	BaseHosts []string `json:"platform_hosts"`
	// BaseDomain is what every store's own address ends in, so the console
	// can name it — <slug>.<base_domain> — before and after a store exists.
	// Empty when stores are reached only by attached domains.
	BaseDomain string `json:"base_domain"`
}

// handleHealth says how many stores there are and which active ones are not
// running — a store that failed to boot answers 503 and is otherwise silent.
func (p *Platform) handleHealth(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	h := health{Stores: len(p.tenants), Running: len(p.apps), Unbooted: []string{},
		BaseHosts: p.cfg.PlatformHosts, BaseDomain: p.cfg.BaseDomain}
	for slug, t := range p.tenants {
		if t.Status == StatusActive && p.apps[slug] == nil {
			h.Unbooted = append(h.Unbooted, slug)
		}
	}
	gocommerce.Respond(w, http.StatusOK, h)
}

func (p *Platform) handleTenants(w http.ResponseWriter, r *http.Request) {
	list, err := p.Tenants(r.Context())
	respond(w, r, http.StatusOK, list, err)
}

func (p *Platform) handleProvision(w http.ResponseWriter, r *http.Request) {
	var in ProvisionInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out, err := p.Provision(r.Context(), in)
	respond(w, r, http.StatusCreated, out, err)
}

func (p *Platform) handleTenant(w http.ResponseWriter, r *http.Request) {
	t, err := p.Tenant(r.Context(), r.PathValue("slug"))
	respond(w, r, http.StatusOK, t, err)
}

func (p *Platform) handleUpdateTenant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   *string `json:"name"`
		Status *string `json:"status"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	slug := r.PathValue("slug")
	t, err := p.Tenant(r.Context(), slug)
	if err == nil && in.Name != nil {
		t, err = p.Rename(r.Context(), slug, *in.Name)
	}
	if err == nil && in.Status != nil {
		t, err = p.SetStatus(r.Context(), slug, *in.Status)
	}
	respond(w, r, http.StatusOK, t, err)
}

// handleDeleteTenant needs the slug repeated as ?confirm=, on top of the
// store being suspended: a DELETE nobody meant, replayed from a log or a
// shell history, must not be able to drop a store.
func (p *Platform) handleDeleteTenant(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if r.URL.Query().Get("confirm") != slug {
		gocommerce.RespondError(w, r, gocommerce.Validationf("repeat the store's slug as ?confirm=%s to delete it and everything in it", slug))
		return
	}
	if err := p.Delete(r.Context(), slug); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Platform) handleAddDomain(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Domain string `json:"domain"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	t, err := p.AddDomain(r.Context(), r.PathValue("slug"), in.Domain)
	respond(w, r, http.StatusCreated, t, err)
}

func (p *Platform) handleRemoveDomain(w http.ResponseWriter, r *http.Request) {
	t, err := p.RemoveDomain(r.Context(), r.PathValue("slug"), r.PathValue("domain"))
	respond(w, r, http.StatusOK, t, err)
}

func (p *Platform) handleAddOwner(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	pw, err := p.AddOwner(r.Context(), r.PathValue("slug"), in.Email, in.Password)
	out := map[string]string{"email": in.Email}
	if pw != "" {
		out["password"] = pw
	}
	respond(w, r, http.StatusCreated, out, err)
}

func (p *Platform) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	tok, err := p.RotateToken(r.Context(), r.PathValue("slug"))
	respond(w, r, http.StatusOK, map[string]string{"admin_token": tok}, err)
}
