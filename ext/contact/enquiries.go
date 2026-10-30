package contact

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// maxQuantity bounds what an enquiry may ask a price for; past it the number
// is a typo or a probe rather than an order.
const maxQuantity = 1_000_000

// EnquiryInput is what a product page posts: one product, by id or by slug,
// and who is asking.
type EnquiryInput struct {
	ProductID   *int64 `json:"product_id"`
	ProductSlug string `json:"product_slug"`
	// VariantID narrows the question to one of the product's variants.
	VariantID *int64 `json:"variant_id"`
	// Quantity is how many the shopper has in mind, for a price on a number.
	Quantity *int   `json:"quantity"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	Body     string `json:"body"`
	// Website is a honeypot: a field no person sees.
	Website string `json:"website"`
}

// EnquiryProduct is what an enquiry was about, as the product was called when
// the shopper asked. A product renamed or deleted since leaves this as it was:
// it is the record of the question.
type EnquiryProduct struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Slug         string `json:"slug"`
	VariantID    *int64 `json:"variant_id,omitempty"`
	VariantTitle string `json:"variant_title,omitempty"`
	Quantity     *int   `json:"quantity,omitempty"`
}

func (m *Module) enquiriesOpen(ctx context.Context) bool {
	return m.app.Plugins().String(ctx, pluginKey, "enquiries") != "false"
}

func (m *Module) handleEnquiry(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !m.open(w, r) {
		return
	}
	if !m.enquiriesOpen(ctx) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("this store does not take product enquiries"))
		return
	}
	var in EnquiryInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if strings.TrimSpace(in.Website) != "" {
		gocommerce.Respond(w, http.StatusCreated, map[string]string{"status": StatusNew})
		return
	}
	s, err := checkSender(in.Name, in.Email, in.Phone, in.Body)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	p, err := m.enquiryProduct(ctx, in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var id int64
	if err := m.db.QueryRowContext(ctx, `
		INSERT INTO contact_messages (kind, name, email, phone, body,
			product_id, product_title, product_slug, variant_id, variant_title, quantity)
		VALUES ('enquiry', $1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		s.name, s.email, s.phone, s.body,
		p.ID, p.Title, p.Slug, p.VariantID, p.VariantTitle, p.Quantity).Scan(&id); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// Announced to the same address as the contact form's messages, so a
	// store has one inbox and one place it is told about it. Best effort, as
	// there: a mail backend being down must not fail the shopper's question.
	if to := m.notifyEmail(ctx); to != "" {
		quantity := ""
		if p.Quantity != nil {
			quantity = strconv.Itoa(*p.Quantity)
		}
		_ = m.app.Notify(ctx, gocommerce.Notification{
			Event: "contact.enquiry", Channel: gocommerce.ChannelEmail, To: to,
			Data: map[string]string{
				"message_id": strconv.FormatInt(id, 10), "from_name": s.name, "from_email": s.email, "phone": s.phone,
				"product_title": p.Title, "variant_title": p.VariantTitle, "quantity": quantity,
				"product_link": m.productLink(p.ID), "body": s.body,
			},
		})
	}
	gocommerce.Respond(w, http.StatusCreated, map[string]string{"status": StatusNew})
}

// enquiryProduct finds what the shopper is asking about through the
// catalogue's own service. Only a product on sale can be asked about: a draft
// or an archived one answers as if it did not exist, as the storefront's
// product route does, so the form cannot be used to find out what is coming.
func (m *Module) enquiryProduct(ctx context.Context, in EnquiryInput) (*EnquiryProduct, error) {
	slug := strings.TrimSpace(in.ProductSlug)
	var (
		p   *gocommerce.Product
		err error
	)
	switch {
	case in.ProductID != nil:
		p, err = m.app.Products().GetProduct(ctx, *in.ProductID)
	case slug != "":
		p, err = m.app.Products().GetProductBySlug(ctx, slug)
	default:
		return nil, gocommerce.Validationf("say which product the enquiry is about: product_id or product_slug")
	}
	if errors.Is(err, gocommerce.ErrNotFound) || (err == nil && p.Status != gocommerce.ProductActive) {
		return nil, gocommerce.Validationf("that product is not on sale here")
	}
	if err != nil {
		return nil, err
	}
	if in.ProductID != nil && slug != "" && slug != p.Slug {
		return nil, gocommerce.Validationf("product_id and product_slug name different products")
	}
	out := &EnquiryProduct{ID: p.ID, Title: p.Title, Slug: p.Slug}
	if in.VariantID != nil {
		for _, v := range p.Variants {
			if v.ID != *in.VariantID {
				continue
			}
			id := v.ID
			out.VariantID = &id
			out.VariantTitle = strings.Join(v.Options, " / ")
			if out.VariantTitle == "" {
				out.VariantTitle = v.SKU
			}
		}
		if out.VariantID == nil {
			return nil, gocommerce.Validationf("variant %d is not one of this product's", *in.VariantID)
		}
	}
	if in.Quantity != nil {
		if *in.Quantity < 1 || *in.Quantity > maxQuantity {
			return nil, gocommerce.Validationf("quantity is a whole number from 1 to %d", maxQuantity)
		}
		q := *in.Quantity
		out.Quantity = &q
	}
	return out, nil
}

// productLink is the product in the panel, for the announcement: the person
// reading it is the operator, and the panel is where they act on it. Empty
// without Config.PanelURL, which is the engine's own public address and the
// only one it trusts to put in a link.
func (m *Module) productLink(id int64) string {
	panel := m.app.Config().PanelURL
	if panel == "" {
		return ""
	}
	return panel + "/dash/products/" + strconv.FormatInt(id, 10)
}

// throttle is a fixed window per key, the way ext/b2b bounds its dealer form.
// The key is the store and the peer address. It is in memory, so per replica,
// and it is not a substitute for a captcha on a form that draws real abuse.
type throttle struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	seen   map[string]*window
	swept  time.Time
}

type window struct {
	start time.Time
	count int
}

// newThrottle allows limit attempts per window; a negative limit allows
// everything.
func newThrottle(limit int, per time.Duration) *throttle {
	return &throttle{limit: limit, window: per, seen: map[string]*window{}}
}

// allow counts one attempt and says whether it is within the limit — and if
// not, how long until it would be.
func (t *throttle) allow(key string, now time.Time) (time.Duration, bool) {
	if t.limit < 0 {
		return 0, true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Finished windows are forgotten once a window, so the map holds only the
	// addresses heard from lately however many there have been.
	if now.Sub(t.swept) >= t.window {
		for k, w := range t.seen {
			if now.Sub(w.start) >= t.window {
				delete(t.seen, k)
			}
		}
		t.swept = now
	}
	w := t.seen[key]
	if w == nil || now.Sub(w.start) >= t.window {
		t.seen[key] = &window{start: now, count: 1}
		return 0, true
	}
	if w.count >= t.limit {
		return w.start.Add(t.window).Sub(now), false
	}
	w.count++
	return 0, true
}

// peerAddr is the connection's address without its port, which the engine
// has already made the client's when the request came through one of its
// trusted proxies. Never X-Forwarded-For here: from anyone but a trusted
// proxy that header is whatever the client wrote, and trusting it would let
// anyone reset their own count at will.
func peerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
