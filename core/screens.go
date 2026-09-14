package gocommerce

import (
	"net/http"
	"sort"
	"strings"
)

// Screens are how a module gets a place in the admin panel.
//
// # Why a descriptor rather than a component
//
// The panel is a SvelteKit app compiled once and embedded in the binary, so a
// module cannot ship code into it: whatever a module contributes has to be
// something the already-compiled panel can read at runtime. The three ways out
// of that are to let a module serve its own page in an iframe, to compile
// modules' components into the panel at build time, or to have a module
// describe its screen as data and let the panel render it.
//
// This is the third. It costs the panel nothing at runtime — no third-party
// JavaScript, no relaxation of the content security policy, no second design
// system inside a frame — and it keeps every screen looking like the panel
// because the panel is what draws them.
//
// # What it cannot do, stated plainly
//
// A descriptor covers a list, and optionally a form behind it. That is what
// nearly every module screen in this repository actually is — webhooks'
// endpoints, the CMS's pages, identity's accounts — but it is a real ceiling,
// not a temporary one. A module wanting a chart, a drag-and-drop ordering or a
// bespoke editor cannot express it here and would still need the panel to grow
// the screen itself. When something real needs that, the iframe escape hatch is
// the decision to take then; the shape below is deliberately additive so it can
// sit beside one.
//
// # Rights
//
// A screen names the right it needs and the listing filters on it, so a module
// screen is invisible to somebody who could not use it. That is the panel's own
// rule for every built-in screen, and a module does not get to opt out of it.
type ScreenContributor interface {
	// Screens returns the screens this module contributes. Called once, at
	// registration, so the descriptors are static — they describe the shape of
	// a screen, never its contents.
	Screens() []Screen
}

// Screen is one module-contributed admin screen.
type Screen struct {
	// Slug is the URL segment the panel serves it at, under /x/. Unique across
	// modules; the engine refuses a collision at boot rather than letting one
	// module's screen shadow another's.
	Slug string `json:"slug"`
	// Module is filled in by the engine, so a screen cannot claim to come from
	// somewhere it does not.
	Module string `json:"module"`
	Title  string `json:"title"`
	// Icon is a Remix Icon class, the set the panel already ships.
	Icon string `json:"icon,omitempty"`
	// Group is the heading it appears under in the settings sidebar. Empty puts
	// it under the module's own name.
	Group string `json:"group,omitempty"`
	// Right gates both the nav entry and the screen. Required: a screen with no
	// right would be visible to everyone with a session.
	Right Right `json:"right"`
	// Help is one sentence under the title, for what the list does not say.
	Help string     `json:"help,omitempty"`
	List ScreenList `json:"list"`
	// Form is optional. Without it the screen is a reading.
	Form *ScreenForm `json:"form,omitempty"`
}

// ScreenList describes the table.
type ScreenList struct {
	// Endpoint is a GET returning {"data": [...]}, the shape every listing in
	// this engine already answers with.
	Endpoint string         `json:"endpoint"`
	Columns  []ScreenColumn `json:"columns"`
	// Empty is what the table says when there are no rows. A sentence about
	// this screen rather than "No data": an empty table is the first thing an
	// operator sees and the last chance to explain what would fill it.
	Empty string `json:"empty,omitempty"`
	// Paged says the endpoint honours limit/offset and answers a total.
	Paged bool `json:"paged,omitempty"`
}

// ScreenColumn is one column of the table.
type ScreenColumn struct {
	// Key is a field of the row object. Dots reach into nested objects.
	Key   string `json:"key"`
	Label string `json:"label"`
	// Kind decides how the cell is drawn: text, code, number, date, relative,
	// badge or bool. Unknown kinds fall back to text rather than failing, so a
	// module built against a newer engine degrades instead of breaking.
	Kind string `json:"kind,omitempty"`
	// Narrow keeps the column to its content's width.
	Narrow bool `json:"narrow,omitempty"`
}

// ScreenForm describes creating, editing and deleting a row.
type ScreenForm struct {
	// Create is a POST endpoint; empty means rows cannot be created here.
	Create string `json:"create,omitempty"`
	// Update is a PATCH endpoint with {id} where the row's id goes.
	Update string `json:"update,omitempty"`
	// Delete is a DELETE endpoint with {id}.
	Delete string `json:"delete,omitempty"`
	// IDKey is the field holding the row's id. Defaults to "id".
	IDKey  string        `json:"id_key,omitempty"`
	Fields []ScreenField `json:"fields,omitempty"`
	// DeleteWarning is the sentence in the confirm dialog. A module knows what
	// deleting one of its rows costs and the panel does not.
	DeleteWarning string `json:"delete_warning,omitempty"`
}

// ScreenField is one control on the form.
type ScreenField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Kind: text, textarea, number, toggle, select. Unknown falls back to text.
	Kind     string         `json:"kind,omitempty"`
	Help     string         `json:"help,omitempty"`
	Required bool           `json:"required,omitempty"`
	Options  []ScreenOption `json:"options,omitempty"`
}

// ScreenOption is one choice in a select field.
type ScreenOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// registerScreens collects one module's screens, refusing the mistakes that
// would otherwise show up as a blank page much later.
func (a *App) registerScreens(module string, mod Module) error {
	contributor, ok := mod.(ScreenContributor)
	if !ok {
		return nil
	}
	for _, s := range contributor.Screens() {
		s.Module = module
		s.Slug = strings.TrimSpace(strings.ToLower(s.Slug))
		switch {
		case s.Slug == "":
			return Internalf(nil, "module %q contributed a screen with no slug", module)
		case strings.Trim(s.Slug, "abcdefghijklmnopqrstuvwxyz0123456789-") != "":
			return Internalf(nil, "module %q contributed the screen slug %q: use lowercase letters, digits and dashes", module, s.Slug)
		case strings.TrimSpace(s.Title) == "":
			return Internalf(nil, "module %q contributed a screen with no title", module)
		case s.Right == "":
			// Not a default: a screen the engine quietly showed to everyone
			// with a session would be a permissions hole that looks like a
			// feature.
			return Internalf(nil, "module %q screen %q names no right", module, s.Slug)
		case s.List.Endpoint == "":
			return Internalf(nil, "module %q screen %q has no list endpoint", module, s.Slug)
		}
		for _, existing := range a.screens {
			if existing.Slug == s.Slug {
				return Internalf(nil, "module %q screen %q collides with module %q",
					module, s.Slug, existing.Module)
			}
		}
		a.screens = append(a.screens, s)
	}
	return nil
}

// mountScreenRoutes serves the descriptors to the panel.
func (a *App) mountScreenRoutes() {
	// Behind nothing but a session, and filtered per caller instead: the list
	// is what the nav is built from, so refusing it outright would leave an
	// operator with a panel missing entries rather than a panel without the
	// ones they cannot use.
	a.HandleAdminFunc("GET /api/admin/screens", a.handleListScreens, RightCatalogRead)
}

func (a *App) handleListScreens(w http.ResponseWriter, r *http.Request) {
	out := make([]Screen, 0, len(a.screens))
	for _, s := range a.screens {
		if !a.callerHas(r, s.Right) {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Title < out[j].Title
	})
	Respond(w, http.StatusOK, out)
}

// Screens returns every registered screen, for a caller assembling its own
// navigation. Rights are not applied here — handleListScreens is where the
// per-request filtering happens, because only a request has a caller.
func (a *App) Screens() []Screen {
	out := make([]Screen, len(a.screens))
	copy(out, a.screens)
	return out
}

// callerHas reports whether this request's operator carries a right.
//
// A static admin token carries every right — it is the machine credential a
// script runs under and rights.go already treats it that way — so a token
// sees every screen. A signed-in operator sees the ones their role allows.
func (a *App) callerHas(r *http.Request, right Right) bool {
	su := SuperuserFrom(r.Context())
	if su == nil {
		return true
	}
	return su.Has(right)
}
