package b2b

import (
	"context"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The wording of what this module sends is the store's (D58): each message has
// a default template the operator can reword from the panel, and the engine
// sends flat data rather than rendered copy.
func (m *Module) registerTemplates(app *gocommerce.App) {
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: EventInvitation, Title: "Company invitation",
		Description: "To an address invited to buy for a company.",
		Variables:   []string{"company_name", "role", "invited_by", "invite_url", "invite_token", "expires_in_days"},
		Subject:     "You are invited to buy for {{.company_name}}",
		Body: `Hello,

{{if .invited_by}}{{.invited_by}} has{{else}}You have been{{end}} invited you to buy for {{.company_name}} as {{.role}}.

{{if .invite_url}}Accept within {{.expires_in_days}} days:

{{.invite_url}}{{else}}Sign in with this address and enter this code within {{.expires_in_days}} days:

{{.invite_token}}{{end}}

If you were not expecting this, you can ignore it.`,
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: EventApprovalRequest, Title: "Order awaiting approval",
		Description: "To a company's approvers when a buyer's order is over its approval limit.",
		Variables:   []string{"company_name", "requested_by", "approval_id", "total_minor", "currency", "po_number"},
		Subject:     "An order for {{.company_name}} is waiting for your approval",
		Body: `{{.requested_by}} has asked to place an order for {{.company_name}}{{if .po_number}} under PO {{.po_number}}{{end}}.

Total: {{.total_minor}} ({{.currency}}, minor units)

It is waiting for an approver: request {{.approval_id}}.`,
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: EventApprovalDecision, Title: "Approval decided",
		Description: "To the buyer who asked, when their order is approved or rejected.",
		Variables:   []string{"company_name", "decision", "reason", "order_number", "approval_id"},
		Subject:     "Your order for {{.company_name}} was {{.decision}}",
		Body: `Your request {{.approval_id}} was {{.decision}}.{{if .order_number}}

Order {{.order_number}} has been placed.{{end}}{{if .reason}}

{{.reason}}{{end}}`,
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: EventQuoteReady, Title: "Quote ready",
		Description: "To the buyer who asked for a quote, when the store has priced it.",
		Variables:   []string{"company_name", "quote_number", "total_minor", "currency", "expires_at", "reply"},
		Subject:     "Your quote {{.quote_number}} is ready",
		Body: `Your quote {{.quote_number}} for {{.company_name}} has been priced.

Total: {{.total_minor}} ({{.currency}}, minor units), open until {{.expires_at}}.{{if .reply}}

{{.reply}}{{end}}`,
	})
}

// send hands one message to the store's notifiers. A failure is logged and
// not returned: the invitation, the request or the quote already exists, and
// the store can resend; failing the call would leave the caller unsure
// whether it did.
func (m *Module) send(ctx context.Context, event, to string, data map[string]string) {
	if strings.TrimSpace(to) == "" {
		return
	}
	if err := m.app.Notify(ctx, gocommerce.Notification{
		Event: event, Channel: gocommerce.ChannelEmail, To: to, Data: data,
	}); err != nil {
		m.app.Log().Warn("b2b: notification not sent", "event", event, "error", err)
	}
}

// addressOf is where to reach an account now: its current address, which may
// differ from the one its company's group holds (empty while a new address
// awaits confirmation) or from the one it asked with. fallback is used when
// the account is gone.
func (m *Module) addressOf(ctx context.Context, customerID int64, fallback string) string {
	if acct, err := m.accounts.CustomerByID(ctx, customerID); err == nil {
		return acct.Email
	}
	return fallback
}

func (m *Module) notifyInvitation(ctx context.Context, c *Company, email, role, by, token string) {
	data := map[string]string{
		"company_name":    c.Name,
		"role":            role,
		"invited_by":      by,
		"invite_token":    token,
		"expires_in_days": strconv.Itoa(int(m.cfg.InviteTTL / (24 * time.Hour))),
	}
	if m.cfg.InviteURL != "" {
		data["invite_url"] = strings.ReplaceAll(m.cfg.InviteURL, "{token}", token)
	}
	m.send(ctx, EventInvitation, email, data)
}

// notifyApprovers tells every approver and admin of the company, the
// requester excepted.
func (m *Module) notifyApprovers(ctx context.Context, c *Company, a *Approval) {
	members, err := m.Members(ctx, c.ID)
	if err != nil {
		m.app.Log().Warn("b2b: could not list approvers", "company", c.ID, "error", err)
		return
	}
	data := map[string]string{
		"company_name": c.Name,
		"requested_by": a.RequesterMail,
		"approval_id":  strconv.FormatInt(a.ID, 10),
		"total_minor":  strconv.FormatInt(a.Total.AmountMinor, 10),
		"currency":     a.Total.Currency,
		"po_number":    a.PONumber,
	}
	for _, mem := range members {
		if mem.CustomerID == a.RequestedBy || (mem.Role != RoleAdmin && mem.Role != RoleApprover) {
			continue
		}
		m.send(ctx, EventApprovalRequest, m.addressOf(ctx, mem.CustomerID, mem.Email), data)
	}
}

func (m *Module) notifyDecision(ctx context.Context, c *Company, a *Approval) {
	m.send(ctx, EventApprovalDecision, m.addressOf(ctx, a.RequestedBy, a.RequesterMail), map[string]string{
		"company_name": c.Name,
		"decision":     a.Status,
		"reason":       a.Reason,
		"order_number": a.OrderNumber,
		"approval_id":  strconv.FormatInt(a.ID, 10),
	})
}

func (m *Module) notifyQuoteReady(ctx context.Context, q *Quote) {
	data := map[string]string{
		"company_name": q.CompanyName,
		"quote_number": q.Number,
		"reply":        q.Reply,
	}
	if q.Total != nil {
		data["total_minor"] = strconv.FormatInt(q.Total.AmountMinor, 10)
		data["currency"] = q.Total.Currency
	}
	if q.ExpiresAt != nil {
		data["expires_at"] = q.ExpiresAt.Format(time.RFC3339)
	}
	m.send(ctx, EventQuoteReady, m.addressOf(ctx, q.RequestedBy, q.RequesterMail), data)
}
