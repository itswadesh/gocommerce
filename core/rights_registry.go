package gocommerce

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Rights a module brings with it.
//
// rights.go says nothing outside it may invent a right, and that rule was
// written when every screen was core's. It stopped being true the moment a
// module shipped a screen: reviews, pages, menus, the FAQ, the contact inbox,
// the newsletter list, wishlists, invoices and webhooks each added a surface an
// operator can be kept out of, and each borrowed the nearest right that already
// existed — reviews under catalog.write, the newsletter under store.operate.
//
// The cost is that a permission cannot be given without the thing it was
// borrowed from. "Let this person moderate reviews" meant handing them the
// whole catalogue; "let them read the newsletter list" meant handing them the
// outbox. And none of those screens appeared on the Roles grid at all, because
// the grid draws rights and they had none of their own — a surface nobody can
// see on that screen is a surface nobody can grant or take away.
//
// So a module declares its rights, the same way it declares a plugin or a
// notification template. The rule rights.go states survives in the form that
// mattered: a right is declared in exactly one place, by the code that owns the
// routes it gates, and nothing invents one at the point of use.
//
// What a module may NOT do is widen an existing role. A declaration says which
// roles carry the right before the store has said otherwise, and that list is
// how "growing the list changes nobody's access" keeps holding: a right lifted
// off catalog.write is declared for the roles that had catalog.write, so the
// same people reach the same screens after the upgrade as before.

// RightSpec is one right a module brings.
type RightSpec struct {
	// Right is the dotted name, `resource.verb`. The panel splits on the dot
	// to draw its grid, so a name without one becomes a row of its own.
	Right Right
	// Label is the imperative gloss — "Moderate reviews" — and Scope is what
	// the right covers. Both are shown on the Roles screen; without them it
	// renders the dotted name, which asks the person granting it to already
	// know what it means.
	Label string
	Scope string
	// Default names the roles that carry this right in a store that has never
	// said otherwise. Owner always carries every right and does not need
	// naming; listing it is harmless.
	//
	// The safe value for a right lifted off an existing one is exactly the
	// roles that held the old one. Anything wider hands every store on upgrade
	// a permission nobody granted.
	Default []string
}

var rightNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*\.[a-z0-9]+(-[a-z0-9]+)*$`)

// RegisterRight declares a right this module's routes are gated on.
//
// Called from Register, like the other declarations. A right registered twice,
// or one that collides with a core right, is a registration error rather than
// a silent last-one-wins: two modules meaning different things by one name is
// the kind of thing that is only noticed when somebody is refused a screen.
func (a *App) RegisterRight(spec RightSpec) {
	owner := a.ownerName()
	if !rightNameRE.MatchString(string(spec.Right)) {
		a.regErrf("module %q: right %q: want resource.verb in lowercase", owner, spec.Right)
		return
	}
	if slices.Contains(AllRights, spec.Right) {
		a.regErrf("module %q: right %q is already one of the engine's", owner, spec.Right)
		return
	}
	for _, existing := range a.moduleRights {
		if existing.Right == spec.Right {
			a.regErrf("module %q: right %q is already declared by module %q",
				owner, spec.Right, existing.module)
			return
		}
	}
	for _, role := range spec.Default {
		if !ValidRole(role) {
			a.regErrf("module %q: right %q names role %q, which does not exist",
				owner, spec.Right, role)
			return
		}
	}
	if strings.TrimSpace(spec.Label) == "" {
		a.regErrf("module %q: right %q has no label", owner, spec.Right)
		return
	}
	a.moduleRights = append(a.moduleRights, moduleRight{RightSpec: spec, module: owner})
}

// moduleRight is a declaration plus who made it, for the error above and for
// the Roles screen, which says which module a right came from.
type moduleRight struct {
	RightSpec
	module string
}

// Rights is every right this build has: the engine's, then each module's in
// registration order. Appended rather than sorted, for the reason AllRights
// gives: the panel draws the grid in this order and re-sorting moves a control
// an operator has learned the position of.
func (a *App) Rights() []Right {
	out := make([]Right, 0, len(AllRights)+len(a.moduleRights))
	out = append(out, AllRights...)
	for _, r := range a.moduleRights {
		out = append(out, r.Right)
	}
	return out
}

// RightCatalogue is every right with its words, for the screen that grants
// them. Core's labels live in the panel; a module's travel with the right,
// because the panel cannot know what a module it was not built with calls
// its own permissions.
type RightCatalogue struct {
	Right  Right  `json:"right"`
	Label  string `json:"label,omitempty"`
	Scope  string `json:"scope,omitempty"`
	Module string `json:"module,omitempty"`
}

// RightsCatalogue is the module-declared half, which is the half the panel
// cannot have a table for.
func (a *App) RightsCatalogue() []RightCatalogue {
	out := make([]RightCatalogue, 0, len(a.moduleRights))
	for _, r := range a.moduleRights {
		out = append(out, RightCatalogue{
			Right: r.Right, Label: r.Label, Scope: r.Scope, Module: r.module,
		})
	}
	return out
}

// defaultRightsOf is DefaultRightsOf widened by what the modules in this build
// declared. Owner carries everything, which now means everything this binary
// has rather than everything core knows about — a module right owner did not
// carry would be a screen the owner is refused from their own store.
func (a *App) defaultRightsOf(role string) []Right {
	var out []Right
	if role == RoleOwner {
		out = a.Rights()
	} else {
		out = DefaultRightsOf(role)
		for _, r := range a.moduleRights {
			if slices.Contains(r.Default, role) {
				out = append(out, r.Right)
			}
		}
	}
	// Sorted, for the reason DefaultRightsOf gives: every other role's set
	// comes back sorted, and a matrix that compares owner's against it saw two
	// orderings of the same rights and called them different.
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// hasRight reports whether this build has the right at all, which is what the
// roles service validates a stored set against. A right from a module this
// binary was not built with is refused rather than stored: the row would gate
// nothing and would reappear as a checkbox for a screen that does not exist.
func (a *App) hasRight(right Right) bool {
	if slices.Contains(AllRights, right) {
		return true
	}
	for _, r := range a.moduleRights {
		if r.Right == right {
			return true
		}
	}
	return false
}
