package gocommerce

import (
	"maps"
	"net/http"
	"slices"
	"strings"
)

// Ordering a listing, without letting a request write SQL.
//
// The whole trust boundary of the feature is this file. A request supplies a
// key; the SQL that key stands for is a literal written here, and the direction
// is a Go bool rendered as one of two constant strings. Nothing a client sends
// is ever concatenated into a statement, which is why no escaping, quoting or
// identifier validation appears anywhere in this package — there is nothing to
// escape.

// Sort is an operator-chosen ordering for one listing.
//
// The zero value means "the listing's own order", which is what a request
// naming no sort asks for — and why an unsorted request produces byte-for-byte
// the SQL it produced before sorting existed.
type Sort struct {
	Field string
	Desc  bool
}

// sortField is one allow-listed ordering, with BOTH directions written out in
// full rather than composed from one expression plus a suffix.
//
// NULLS placement is not a property of the direction. PostgreSQL defaults to
// NULLS LAST for ASC and NULLS FIRST for DESC, so a composed clause moves the
// block of rows that have no value from the bottom of the listing to the top on
// the operator's second click — an order with no name, a discount with no code,
// a product whose variants are untracked. Writing both out lets a nullable
// expression pin NULLS LAST in both directions, which states "no answer" rather
// than guessing "furthest away".
//
// The converse is just as load-bearing: a NOT NULL expression carries no NULLS
// clause at all. An ascending (expr, id) btree is ASC NULLS LAST, and read
// backwards it yields DESC NULLS FIRST — which does not match an explicit
// "DESC NULLS LAST", so adding one as tidying would silently stop the index
// serving the descending direction, which is the panel's first click on dates,
// money and counts. That is the only reason M29's indexes serve both ways.
type sortField struct{ asc, desc string }

// SortSpec is one listing's allow-list: every ordering it will accept, and the
// key it ends every one of them with.
//
// Tiebreak must be unique within the result set — a primary key, or the
// grouping key of an aggregate listing. Without it PostgreSQL is free to order
// tied rows differently in two executions of the same statement, so a row
// arrives on page 1 and again on page 2 while another arrives on neither,
// silently, with a meta.total that says nothing is missing.
//
// Columns is a map and not a formatter on purpose: one file then holds every
// string that can become an ORDER BY, and a test compares that list with
// openapi.json's enums.
type SortSpec struct {
	Tiebreak string
	Columns  map[string]sortField
}

// Fields returns the accepted keys in sorted order, for error messages and for
// the test that compares them with the published contract.
func (s SortSpec) Fields() []string {
	return slices.Sorted(maps.Keys(s.Columns))
}

// Clause resolves a Sort into an ORDER BY body, or returns the listing's own
// fallback when nothing was asked for.
//
// This is the only thing that turns a key into SQL, so there is exactly one
// path from a wire value to an ORDER BY and it cannot be routed around: a
// caller that never went through ParseSort — ext/mcp, a store's own main(), a
// test — is checked here.
func (s SortSpec) Clause(sort Sort, fallback string) (string, error) {
	if sort.Field == "" {
		return fallback, nil
	}
	f, ok := s.Columns[sort.Field]
	if !ok {
		return "", Validationf("sort must be one of %s", strings.Join(s.Fields(), ", "))
	}
	expr, tie := f.asc, s.Tiebreak+" ASC"
	if sort.Desc {
		expr, tie = f.desc, s.Tiebreak+" DESC"
	}
	// The chosen key can BE the tiebreaker — ordering orders by `number` is
	// ordering by o.id, and a customer group's key is its email.
	if expr == tie {
		return expr, nil
	}
	return expr + ", " + tie, nil
}

// ParseSort reads ?sort= and ?order= off a request against one listing's
// allow-list. It is the sibling of Page(r), and exported for the same reason: a
// module that serves its own listing should allow-list it the same way.
//
// The check here is deliberately not the only one — Clause checks again,
// because the map lookup IS the resolution. Raising it here as well means a
// rejected sort costs no database work: the count query runs after this.
func ParseSort(r *http.Request, spec SortSpec) (Sort, error) {
	q := r.URL.Query()
	field := strings.TrimSpace(q.Get("sort"))
	order := strings.TrimSpace(q.Get("order"))

	if field == "" {
		// The default orderings are fixed clauses carrying their own direction
		// (`p.id DESC`, `pc.member_position, pc.product_id`), so a lone `order`
		// has nothing coherent to flip. Refused rather than ignored, for the
		// same reason queryInt64 refuses an unparseable filter: a sort that
		// quietly did nothing is indistinguishable from one that worked.
		if order != "" {
			return Sort{}, Validationf("order needs a sort field: pass sort=<field> with it")
		}
		return Sort{}, nil
	}
	if _, ok := spec.Columns[field]; !ok {
		return Sort{}, Validationf("sort must be one of %s", strings.Join(spec.Fields(), ", "))
	}

	// The wire string is compared and then discarded; what travels onward is a
	// bool, which cannot carry a payload.
	switch strings.ToLower(order) {
	case "", "asc":
		return Sort{Field: field}, nil
	case "desc":
		return Sort{Field: field, Desc: true}, nil
	default:
		return Sort{}, Validationf("order must be asc or desc")
	}
}
