package gocommerce

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Customers, without customer records.
//
// D22 makes guest checkout a permanent guarantee: a shopper buys with a cart
// token and an email, and core carries no path that assumes a customer record
// exists. PLAN §10.4 says the same thing from the other side — an order holds
// its own snapshot of who bought it, and never depends on a mutable customer
// row for its legal or operational meaning.
//
// So this is a reading of the orders, not a table. "A customer" is every order
// that shares an email, and everything shown about them — their name, their
// phone, where it went — is what their most recent order says. Nothing here can
// be edited, because there is nothing to edit: correcting a customer means
// correcting an order.
//
// A future identity module may add the stored entity D22 anticipates. When it
// does, this stays true and stays useful: it is what the store knows about
// people who never made an account, which is most of them.

// Customer is a shopper as the orders remember them.
type Customer struct {
	Email string `json:"email"`
	// Name, Phone and Address come from the most recent order, because that is
	// the most recent thing the shopper told the store about themselves.
	Name    string  `json:"name,omitempty"`
	Phone   string  `json:"phone,omitempty"`
	Address Address `json:"address"`
	// Orders counts everything they placed that was not cancelled; Spent totals
	// only what was actually paid, less anything that has gone back. An order
	// awaiting cash on delivery is a real order and not yet money, and a
	// partial refund is money the customer no longer spent.
	Orders       int       `json:"orders"`
	Spent        Money     `json:"spent"`
	FirstOrderAt time.Time `json:"first_order_at"`
	LastOrderAt  time.Time `json:"last_order_at"`
}

// The aggregates, named once. They are consts rather than inline SQL because a
// listing and a sort spec have to agree about what "spent" means: two copies of
// the expression is how the column somebody reads and the column they sorted by
// come to be different numbers.
//
// customerSpent subtracts refunded_minor, and has to. A fully refunded order is
// already excluded by the payment_status filter; a partially refunded one stays
// `paid` (D36) and would otherwise be counted at full value, which is the one
// place keeping the four statuses could quietly make lifetime spend wrong. It
// is a clause inside the existing GROUP BY and not a correlated subquery — a
// subquery would ignore the outer WHERE and sum refunds from orders that are
// not in the group.
const (
	customerOrderCount = `count(*) FILTER (WHERE o.status <> 'cancelled')`
	customerSpent      = `coalesce(sum(o.total_minor - o.refunded_minor) FILTER (WHERE o.payment_status = 'paid'), 0)`
	customerFirstOrder = `min(o.created_at)`
	customerLastOrder  = `max(o.created_at)`
)

// CustomerQuery filters the reading. Search matches an email or a name.
type CustomerQuery struct {
	Search string
	// Sort is an operator-chosen ordering; zero keeps the listing's own, the
	// people who bought most recently first.
	Sort          Sort
	Limit, Offset int
}

// customerSorts orders a grouping, so every expression is either the group key
// or an aggregate over it — the same fragments the SELECT uses, so the number a
// row shows and the number it was ranked by cannot be different.
//
// There is no o.id left once the rows are collapsed, which is why the tiebreaker
// is the email: the one thing a group is guaranteed to have exactly one of.
var customerSorts = SortSpec{
	Tiebreak: "lower(o.email)",
	Columns: map[string]sortField{
		"email":          {"lower(o.email) ASC", "lower(o.email) DESC"},
		"orders":         {customerOrderCount + " ASC", customerOrderCount + " DESC"},
		"spent":          {customerSpent + " ASC", customerSpent + " DESC"},
		"first_order_at": {customerFirstOrder + " ASC", customerFirstOrder + " DESC"},
		"last_order_at":  {customerLastOrder + " ASC", customerLastOrder + " DESC"},
	},
}

// Customers groups the orders by who placed them.
//
// Grouped on the lower-cased email, which is the only handle a guest has. Two
// orders typed with different capitals are one person; two people sharing an
// address are not.
func (s *Orders) Customers(ctx context.Context, q CustomerQuery) ([]*Customer, int, error) {
	where, args := []string{"o.email <> ''"}, []any{}
	if needle := strings.ToLower(strings.TrimSpace(q.Search)); needle != "" {
		args = append(args, "%"+needle+"%")
		where = append(where,
			fmt.Sprintf("(lower(o.email) LIKE $%d OR lower(coalesce(o.name, '')) LIKE $%d)",
				len(args), len(args)))
	}
	clause := strings.Join(where, " AND ")

	// The fallback carries the tiebreaker too, and has to: max(o.created_at)
	// alone is not unique, so two shoppers whose newest orders share a timestamp
	// could already arrive on two pages or on none. Returned bare — every other
	// error in this function is wrapped in Internalf, and wrapping a Validationf
	// would serve a 400 as a 500.
	order, err := customerSorts.Clause(q.Sort, customerLastOrder+" DESC, lower(o.email) ASC")
	if err != nil {
		return nil, 0, err
	}

	var total int
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT count(DISTINCT lower(o.email)) FROM orders o WHERE `+clause, args...,
	).Scan(&total); err != nil {
		return nil, 0, Internalf(err, "count customers")
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	args = append(args, limit, q.Offset)

	// The name, phone and address are taken from the newest order in the group
	// rather than aggregated: array_agg ordered by id, first element. A shopper
	// who moved house should show the address the parcel is going to now.
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT lower(o.email),
		       `+customerOrderCount+`,
		       `+customerSpent+`,
		       `+customerFirstOrder+`, `+customerLastOrder+`,
		       (array_agg(coalesce(o.name, '')  ORDER BY o.id DESC))[1],
		       (array_agg(coalesce(o.phone, '') ORDER BY o.id DESC))[1],
		       (array_agg(o.address             ORDER BY o.id DESC))[1]
		FROM orders o
		WHERE `+clause+`
		GROUP BY lower(o.email)
		ORDER BY `+order+`
		LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, Internalf(err, "read customers")
	}
	defer rows.Close()

	var out []*Customer
	for rows.Next() {
		c := &Customer{}
		var spent int64
		var addr []byte
		if err := rows.Scan(&c.Email, &c.Orders, &spent,
			&c.FirstOrderAt, &c.LastOrderAt, &c.Name, &c.Phone, &addr); err != nil {
			return nil, 0, Internalf(err, "scan customer")
		}
		c.Spent = money(spent, s.app.cfg.Currency)
		if len(addr) > 0 {
			_ = json.Unmarshal(addr, &c.Address)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, Internalf(err, "read customers")
	}
	return out, total, nil
}
