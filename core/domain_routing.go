package gocommerce

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Routing a store's own domain without anyone touching the proxy (D113).
//
// A domain the engine has verified (store_domains.go) still reaches nothing
// until the reverse proxy in front of the server has a route and a
// certificate for it. Traefik's HTTP provider polls a URL for its dynamic
// configuration, so the engine serves one: a router per verified domain, each
// asking the configured ACME resolver for a certificate. A domain is routed a
// poll after it is verified and unrouted a poll after it is removed.
//
// Only verified domains are listed. Anybody can point DNS at the server, and
// a proxy that asked for a certificate for every name it was sent would spend
// the install's Let's Encrypt allowance on strangers' names.

// TraefikConfigPath is where the proxy reads its routes. It is outside /api/
// on purpose: those paths belong to a store, and this one answers for all.
const TraefikConfigPath = "/internal/traefik"

const (
	traefikServiceName  = "gocommerce-store-domains"
	traefikRedirectName = "gocommerce-store-domains-https"
	// Dokploy's, and Traefik's documented, names for the :80 and :443
	// entry points.
	traefikEntryPointHTTP  = "web"
	traefikEntryPointHTTPS = "websecure"
)

// validateDomainRouting checks the Config fields this file reads.
func (c *Config) validateDomainRouting() error {
	c.TraefikToken = strings.TrimSpace(c.TraefikToken)
	c.TraefikUpstream = strings.TrimSpace(c.TraefikUpstream)
	if c.TraefikCertResolver == "" {
		c.TraefikCertResolver = "letsencrypt"
	}
	if c.TraefikToken != "" {
		if c.TraefikUpstream == "" {
			return errors.New("gocommerce: Config.TraefikUpstream is required with Config.TraefikToken: " +
				"the address the proxy sends a store's domain to, such as http://gocommerce:8080")
		}
		if !strings.Contains(c.TraefikUpstream, "@") {
			u, err := url.Parse(c.TraefikUpstream)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return errors.New("gocommerce: Config.TraefikUpstream must be a URL such as http://gocommerce:8080, " +
					"or the name of a Traefik service such as storefront@docker")
			}
		}
	}
	if t := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(c.DomainTarget)), "."); t != "" {
		if net.ParseIP(t) == nil && (!strings.Contains(t, ".") || strings.ContainsAny(t, ":/ ")) {
			return errors.New("gocommerce: Config.DomainTarget must be the server's IP address, such as 203.0.113.10, " +
				"or a host name to CNAME to, such as stores.example.com")
		}
		c.DomainTarget = t
	}
	return nil
}

// pointAt fills in the record that sends d's visitors to this server, when
// the install has said where that is.
func (a *App) pointAt(d *Domain) {
	t := a.cfg.DomainTarget
	if t == "" {
		return
	}
	d.PointName, d.PointValue = d.Host, t
	switch ip := net.ParseIP(t); {
	case ip == nil:
		d.PointType = "CNAME"
	case ip.To4() != nil:
		d.PointType = "A"
	default:
		d.PointType = "AAAA"
	}
}

// traefikRouterName is unique per host: a host is letters, digits, hyphens
// and dots, and the underscore that stands in for the dots is none of them.
func traefikRouterName(host string) string {
	return "gocommerce-" + strings.ReplaceAll(host, ".", "_")
}

type traefikRouter struct {
	Rule        string            `json:"rule"`
	EntryPoints []string          `json:"entryPoints"`
	Service     string            `json:"service"`
	Middlewares []string          `json:"middlewares,omitempty"`
	TLS         *traefikRouterTLS `json:"tls,omitempty"`
}

type traefikRouterTLS struct {
	CertResolver string `json:"certResolver"`
}

// traefikConfig is the dynamic configuration for hosts. A provider must not
// define what it does not use, so with no hosts it is empty, and the
// service is defined here only when the upstream is a URL rather than a
// service another provider owns.
func (a *App) traefikConfig(hosts []string) map[string]any {
	if len(hosts) == 0 {
		return map[string]any{}
	}
	service := a.cfg.TraefikUpstream
	services := map[string]any{}
	if !strings.Contains(service, "@") {
		services[traefikServiceName] = map[string]any{
			"loadBalancer": map[string]any{
				"servers":        []map[string]string{{"url": a.cfg.TraefikUpstream}},
				"passHostHeader": true,
			},
		}
		service = traefikServiceName
	}
	routers := map[string]traefikRouter{}
	for _, h := range hosts {
		rule := "Host(`" + h + "`)"
		name := traefikRouterName(h)
		routers[name] = traefikRouter{
			Rule: rule, EntryPoints: []string{traefikEntryPointHTTPS}, Service: service,
			TLS: &traefikRouterTLS{CertResolver: a.cfg.TraefikCertResolver},
		}
		routers[name+"-http"] = traefikRouter{
			Rule: rule, EntryPoints: []string{traefikEntryPointHTTP}, Service: service,
			Middlewares: []string{traefikRedirectName},
		}
	}
	httpCfg := map[string]any{
		"routers": routers,
		"middlewares": map[string]any{
			traefikRedirectName: map[string]any{"redirectScheme": map[string]any{"scheme": "https", "permanent": true}},
		},
	}
	if len(services) > 0 {
		httpCfg["services"] = services
	}
	return map[string]any{"http": httpCfg}
}

// verifiedHosts is every verified domain on the install. store_domains is a
// global table (store_domains.go), so this reads across stores on purpose.
// A host the request normaliser would refuse is skipped rather than written
// into a rule, where a backtick would end the quoted name.
func (a *App) verifiedHosts(ctx context.Context) ([]string, error) {
	rows, err := a.db.QueryContext(ctx,
		`SELECT host FROM store_domains WHERE verified_at IS NOT NULL ORDER BY host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		if n, err := a.normalizeHost(h); err == nil && n == h {
			out = append(out, h)
		}
	}
	return out, rows.Err()
}

// handleTraefikConfig answers the proxy. Unconfigured, the route reads as
// absent; configured, it wants the token in the Authorization header, the
// one place Traefik's provider can send it.
func (a *App) handleTraefikConfig(w http.ResponseWriter, r *http.Request) {
	want := a.cfg.TraefikToken
	if want == "" {
		RespondError(w, r, NotFoundf("domain routing is not configured on this install"))
		return
	}
	got, ok := bearerToken(r)
	if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="gocommerce proxy"`)
		RespondError(w, r, ErrUnauthorized)
		return
	}
	hosts, err := a.verifiedHosts(r.Context())
	if err != nil {
		// An error rather than an empty list: Traefik keeps the last good
		// configuration when a poll fails, and would drop every route on an
		// empty one.
		RespondError(w, r, Internalf(err, "list verified domains"))
		return
	}
	// Written bare, not in the API's {"data": ...} envelope: Traefik reads
	// the body as its configuration.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(a.traefikConfig(hosts))
}

// CaddyAskPath is Caddy's on-demand TLS "ask" endpoint, for the proxy the
// full compose stack runs (D143). Caddy gets a certificate the first time a
// visitor's browser asks for a name, but only after this answers 200 for it:
// a store's verified domain or <slug>.<base domain> of a store. Anything else
// is a 404, so somebody pointing their own DNS at the server cannot spend the
// install's certificate allowance, as with the Traefik routes above.
//
// No token: Caddy's ask sends none, and the answer for one name is no more
// than visiting that name shows. The compose Caddyfile still keeps /internal/
// off the public sites.
const CaddyAskPath = "/internal/caddy/ask"

func (a *App) handleCaddyAsk(w http.ResponseWriter, r *http.Request) {
	host, err := a.normalizeHost(r.URL.Query().Get("domain"))
	if err != nil {
		RespondError(w, r, NotFoundf("not a store's domain"))
		return
	}
	if _, err := a.stores.ByHost(r.Context(), host); err != nil {
		if errors.Is(err, ErrNotFound) {
			RespondError(w, r, NotFoundf("not a store's domain"))
		} else {
			RespondError(w, r, Internalf(err, "look up a store's domain"))
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}
