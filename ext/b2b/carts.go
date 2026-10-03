package b2b

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/ext/identity"
)

// maxBulkLines bounds a pasted order. Every line is one AddLine — core's rules
// for a line, in core's transaction — so the cap is what bounds the work one
// request can ask for, and five hundred lines is a long purchase order.
const maxBulkLines = 500

// Why a line did not go into the basket. inactive and insufficient_stock are
// the words a refused checkout uses for the same two things.
const (
	RejectNotFound          = "not_found"
	RejectInactive          = gocommerce.ReasonInactive
	RejectInsufficientStock = gocommerce.ReasonInsufficientStock
	RejectInvalid           = "invalid"
)

// BulkLine is one line of a pasted order: a SKU or a variant id, and how
// many. A variant id, when given, is what names the variant; the SKU beside
// it is only echoed back.
type BulkLine struct {
	SKU       string `json:"sku"`
	VariantID int64  `json:"variant_id"`
	Quantity  int    `json:"quantity"`
}

// BulkRequest is a pasted order, into the basket named or a new one.
type BulkRequest struct {
	CartID string     `json:"cart_id"`
	Lines  []BulkLine `json:"lines"`
}

// RejectedLine is a line that did not go into the basket, and why. Message is
// the reason in words — "only 3 left in stock" — for a person to read.
type RejectedLine struct {
	SKU       string `json:"sku"`
	VariantID *int64 `json:"variant_id"`
	Quantity  int    `json:"quantity"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	// Row is the line's row in an uploaded file, counting the header as row
	// 1 the way a spreadsheet numbers it; absent for a line sent as JSON.
	Row int `json:"row,omitempty"`
}

// CartFill is a basket after lines were put into it, and the lines that would
// not go.
type CartFill struct {
	Cart     *gocommerce.Cart `json:"cart"`
	Rejected []RejectedLine   `json:"rejected"`
}

// wantLine is a line somebody asked for, before it is looked up. invalid is a
// file's row that could not be read as a line at all — a quantity of "two" —
// reported with the rest rather than failing the file.
type wantLine struct {
	sku       string
	variantID int64
	quantity  int
	row       int
	invalid   string
}

// AddLines puts a pasted order into a buyer's basket, priced as them. A line
// that cannot go in — a SKU nobody sells, something no longer for sale, more
// than is in stock — is reported and the rest still go in: a two-hundred-line
// paste must not fail at line thirty-seven and leave the buyer to work out
// which lines made it.
func (m *Module) AddLines(ctx context.Context, buyer *identity.Customer, req BulkRequest) (*CartFill, error) {
	want := make([]wantLine, len(req.Lines))
	for i, l := range req.Lines {
		want[i] = wantLine{sku: strings.TrimSpace(l.SKU), variantID: l.VariantID, quantity: l.Quantity}
	}
	return m.addLines(ctx, buyer, req.CartID, want)
}

// AddLinesCSV is AddLines from a spreadsheet: a header naming a sku or a
// variant_id column and a quantity column, in any case and in any order, and
// a row per line. Other columns are ignored, so a file exported with titles
// and prices beside the SKUs still reads. It goes through core's CSV reader,
// which drops a byte-order mark and the apostrophe an export put before a cell
// beginning with = + - or @ (D62). Each line rejected carries its row.
func (m *Module) AddLinesCSV(ctx context.Context, buyer *identity.Customer, cartID string, file io.Reader) (*CartFill, error) {
	want, err := readBulkCSV(file)
	if err != nil {
		return nil, err
	}
	if len(want) == 0 {
		return nil, gocommerce.Validationf("the file has no lines under its header")
	}
	return m.addLines(ctx, buyer, cartID, want)
}

func readBulkCSV(file io.Reader) ([]wantLine, error) {
	r, err := gocommerce.NewCSVReader(file)
	if err != nil {
		return nil, err
	}
	if !r.Has("quantity") || (!r.Has("sku") && !r.Has("variant_id")) {
		return nil, gocommerce.Validationf("the file's first row must name its columns: quantity, and sku or variant_id")
	}
	var want []wantLine
	for {
		row, err := r.Next()
		if errors.Is(err, io.EOF) {
			return want, nil
		}
		if err != nil {
			return nil, err
		}
		// A spreadsheet saves the empty rows under its last line; they are
		// not lines anybody asked for.
		if !row.Has("sku") && !row.Has("variant_id") && !row.Has("quantity") {
			continue
		}
		if len(want) == maxBulkLines {
			return nil, gocommerce.Validationf("at most %d lines at a time; send the rest in another file", maxBulkLines)
		}
		w := wantLine{sku: row.Get("sku"), row: row.Line()}
		if w.variantID, err = row.Int64("variant_id", 0); err != nil {
			w.invalid = "variant_id is not a number"
		}
		if w.quantity, err = row.Int("quantity", 0); err != nil {
			w.invalid = "quantity is not a whole number"
		}
		want = append(want, w)
	}
}

func (m *Module) addLines(ctx context.Context, buyer *identity.Customer, cartID string, want []wantLine) (*CartFill, error) {
	if _, _, err := m.orderingMember(ctx, buyer); err != nil {
		return nil, err
	}
	switch {
	case len(want) == 0:
		return nil, gocommerce.Validationf("lines is empty")
	case len(want) > maxBulkLines:
		return nil, gocommerce.Validationf("at most %d lines at a time; send the rest in another request", maxBulkLines)
	}
	token, err := m.buyerCart(ctx, buyer, cartID)
	if err != nil {
		return nil, err
	}
	return m.fill(ctx, token, want)
}

// Reorder puts a past order's lines into a new basket at today's prices. It
// never places anything: the order was agreed at the prices of its day — a
// quote's, perhaps, or a list since changed — and repeating it is a new order,
// checked out like any other, through the same limits and approvals.
//
// The order has to be in the company's ledger and the caller's to see, by the
// rule the order list applies: their own to a buyer, any of the company's to
// an admin or approver. Anything else is a 404, not a 403 — another company's
// orders are not something to confirm the existence of.
func (m *Module) Reorder(ctx context.Context, buyer *identity.Customer, orderID int64) (*CartFill, error) {
	mem, company, err := m.orderingMember(ctx, buyer)
	if err != nil {
		return nil, err
	}
	var placedBy sql.NullInt64
	err = m.db.QueryRowContext(ctx,
		`SELECT customer_id FROM b2b_orders WHERE order_id = $1 AND company_id = $2`,
		orderID, company.ID).Scan(&placedBy)
	if errors.Is(err, sql.ErrNoRows) ||
		(err == nil && mem.Role == RoleBuyer && (!placedBy.Valid || placedBy.Int64 != buyer.ID)) {
		return nil, gocommerce.NotFoundf("order %d does not exist", orderID)
	}
	if err != nil {
		return nil, err
	}
	o, err := m.app.Order().Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	// By variant where the order still knows it, by SKU where it does not: a
	// line whose variant was deleted keeps its SKU, and the store's own SKU
	// on a product re-created since names the same thing.
	want := make([]wantLine, 0, len(o.Lines))
	for _, l := range o.Lines {
		w := wantLine{sku: l.SKU, quantity: l.Quantity}
		if l.VariantID != nil {
			w.variantID = *l.VariantID
		}
		want = append(want, w)
	}
	token, err := m.buyerCart(ctx, buyer, "")
	if err != nil {
		return nil, err
	}
	return m.fill(ctx, token, want)
}

// orderingMember is the caller's membership and company, provided the company
// may still order. A closed company's buyers keep their group prices until
// they leave it, so filling a basket for one is refused here rather than
// left to fail at checkout.
func (m *Module) orderingMember(ctx context.Context, buyer *identity.Customer) (*Member, *Company, error) {
	mem, company, err := m.memberAndCompany(ctx, buyer)
	if err != nil {
		return nil, nil, err
	}
	if company.Status == StatusClosed {
		return nil, nil, gocommerce.Forbiddenf("%s's account is closed", company.Name)
	}
	return mem, company, nil
}

// buyerCart opens a basket, or takes the one named, and prices it as the
// buyer before anything goes in. AddLine prices a line for the address its
// basket is verified as, so every line is added at the buyer's price; and an
// account that cannot be priced as itself — an address not yet confirmed — is
// refused before anything has been added.
func (m *Module) buyerCart(ctx context.Context, buyer *identity.Customer, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		cart, err := m.app.Cart().Create(ctx, "")
		if err != nil {
			return "", err
		}
		token = cart.Token
	}
	if _, err := m.accounts.ClaimCart(ctx, buyer.ID, token); err != nil {
		return "", err
	}
	return token, nil
}

// fill adds each line through core's AddLine and collects the ones that would
// not go in. Only a failure that is not about the line — the database, say —
// stops it.
func (m *Module) fill(ctx context.Context, token string, lines []wantLine) (*CartFill, error) {
	rejected := []RejectedLine{}
	for _, l := range lines {
		r := RejectedLine{SKU: l.sku, Quantity: l.quantity, Row: l.row}
		if l.variantID > 0 {
			id := l.variantID
			r.VariantID = &id
		}
		reject := func(reason, message string) {
			r.Reason, r.Message = reason, message
			rejected = append(rejected, r)
		}
		if l.invalid != "" {
			reject(RejectInvalid, l.invalid)
			continue
		}
		if l.quantity < 1 {
			reject(RejectInvalid, "quantity must be at least 1")
			continue
		}
		v, err := m.variantFor(ctx, l)
		var apiErr *gocommerce.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			reject(RejectNotFound, apiErr.Message)
			continue
		}
		if err != nil {
			return nil, err
		}
		id := v.ID
		r.VariantID = &id
		if r.SKU == "" {
			r.SKU = v.SKU
		}
		if !v.Active {
			reject(RejectInactive, "that variant is not available")
			continue
		}
		if _, err := m.app.Cart().AddLine(ctx, token, v.ID, l.quantity); err != nil {
			if !errors.As(err, &apiErr) || apiErr.Status >= http.StatusInternalServerError {
				return nil, err
			}
			// Active was read a moment ago, so a conflict now is the stock
			// count — AddLine's other refusal. Message carries core's own
			// words either way.
			reason := RejectInvalid
			switch apiErr.Status {
			case http.StatusNotFound:
				reason = RejectNotFound
			case http.StatusConflict:
				reason = RejectInsufficientStock
			}
			reject(reason, apiErr.Message)
		}
	}
	cart, err := m.app.Cart().GetByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	return &CartFill{Cart: cart, Rejected: rejected}, nil
}

// variantFor looks a line up by its variant id, or failing that its SKU. A
// line naming neither is not found rather than malformed: a repeat order's
// line can lose both when its product is deleted, and nothing can be found
// from nothing either way.
func (m *Module) variantFor(ctx context.Context, l wantLine) (*gocommerce.Variant, error) {
	switch {
	case l.variantID > 0:
		return m.app.Products().GetVariant(ctx, l.variantID)
	case l.sku != "":
		return m.app.Products().GetVariantBySKU(ctx, l.sku)
	}
	return nil, gocommerce.NotFoundf("this line names no sku or variant_id")
}
