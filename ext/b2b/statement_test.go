package b2b

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// placeOnAccount checks a basket of the fixture's widget out on account and
// returns the order.
func (f *fixture) placeOnAccount(t *testing.T, tok string, qty int, po string) gocommerce.Order {
	t.Helper()
	r := f.checkout(t, tok, f.cart(t, qty), map[string]any{"po_number": po})
	if r.code != http.StatusCreated {
		t.Fatalf("checkout = %d: %s", r.code, r.body)
	}
	var placed struct {
		Order gocommerce.Order `json:"order"`
	}
	decodeBody(t, r.body, &placed)
	return placed.Order
}

func (f *fixture) statement(t *testing.T, query string) *Statement {
	t.Helper()
	rec := gctest.AdminRequest(t, f.app, http.MethodGet, f.companyPath()+"/statement"+query, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("statement%s = %d: %s", query, rec.Code, rec.Body)
	}
	var st Statement
	gctest.DecodeData(t, rec, &st)
	return &st
}

// entriesOf picks a statement's lines for one order, in order.
func entriesOf(st *Statement, orderID int64) []StatementEntry {
	var out []StatementEntry
	for _, e := range st.Entries {
		if e.OrderID == orderID {
			out = append(out, e)
		}
	}
	return out
}

// Every change to a company's terms is recorded with who made it, and only
// what changed: the panel saves every field at once, and a history of the
// fields nobody touched would bury the one somebody did.
func TestTermsChangesAreRecordedWithWhoMadeThem(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var created []HistoryEntry
	rec := gctest.AdminRequest(t, f.app, http.MethodGet, f.companyPath()+"/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("history = %d: %s", rec.Code, rec.Body)
	}
	gctest.DecodeData(t, rec, &created)
	if len(created) != 7 {
		t.Fatalf("history after creating = %d entries, want one per term (7): %+v", len(created), created)
	}
	for _, e := range created {
		if e.Action != HistoryCreated || e.ActorKind != ActorToken || string(e.OldValue) != "null" {
			t.Errorf("creation entry = %+v; want created, by the admin token, with no earlier value", e)
		}
	}

	manager := gctest.OperatorToken(t, f.app, "manager@store.test", gocommerce.RoleManager)
	patch := gctest.SessionRequest(t, f.app, manager, http.MethodPatch, f.companyPath(), map[string]any{
		"name": "Acme Distribution", "status": "active", "credit_limit_minor": 75000,
		"net_days": 45, "approval_threshold_minor": 30000, "require_po": false, "group_id": *f.company.GroupID,
	})
	if patch.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", patch.Code, patch.Body)
	}
	if _, err := f.b2b.UpdateCompany(ctx, f.company.ID, CompanyInput{CreditLimitMinor: optionalInt64{Set: true}}); err != nil {
		t.Fatalf("clear the limit: %v", err)
	}

	list, total, err := f.b2b.History(ctx, f.company.ID, 10, 0)
	if err != nil || total != 10 {
		t.Fatalf("history = %d entries (%v), want 7 + 2 + 1", total, err)
	}
	cleared, raised, terms := list[0], list[1], list[2]
	if raised.Field != "net_days" {
		raised, terms = terms, raised
	}
	if cleared.Field != "credit_limit_minor" || string(cleared.OldValue) != "75000" ||
		string(cleared.NewValue) != "null" || cleared.ActorKind != ActorSystem || cleared.Currency != "USD" {
		t.Errorf("clearing the limit = %+v; want 75000 → null by the system, in USD", cleared)
	}
	if terms.Field != "credit_limit_minor" || string(terms.OldValue) != "50000" || string(terms.NewValue) != "75000" ||
		terms.Action != HistoryChanged || terms.ActorKind != ActorOperator || terms.ActorEmail != "manager@store.test" ||
		terms.ActorID == nil {
		t.Errorf("raising the limit = %+v; want 50000 → 75000 by manager@store.test", terms)
	}
	if raised.Field != "net_days" || string(raised.OldValue) != "30" || string(raised.NewValue) != "45" ||
		raised.Currency != "" {
		t.Errorf("net days = %+v; want 30 → 45, no currency", raised)
	}
}

// A statement is the account as the store recorded it: each order placed on
// account a debit, each payment a credit at the moment core recorded it, and
// a closing balance that is exactly what the credit check counts as owed.
func TestAStatementDatesEachPaymentWhenTheStoreRecordedIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)

	paid := f.placeOnAccount(t, tok, 2, "PO-1")
	open := f.placeOnAccount(t, tok, 1, "=SUM(A1:A9)")
	if _, err := f.app.Pay().MarkPaid(ctx, paid.ID, "bank transfer"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	// The event is delivered some time after it was written: the statement
	// must carry when the payment was recorded, not when it was heard. Moving
	// the record back to just after the order makes the two unmistakable.
	recordedAt := paid.CreatedAt.Add(time.Millisecond).Truncate(time.Microsecond)
	if _, err := f.app.DB().ExecContext(ctx, `
		UPDATE outbox_events SET created_at = $2
		WHERE aggregate_id = $1 AND event_name = 'order.paid'`, paid.ID, recordedAt); err != nil {
		t.Fatalf("age the event: %v", err)
	}
	gctest.DrainOutbox(t, f.app)

	st := f.statement(t, "")
	lines := entriesOf(st, paid.ID)
	if len(lines) != 2 || lines[0].Kind != EntryOrder || lines[0].DebitMinor != paid.Total.AmountMinor ||
		lines[1].Kind != EntryPayment || lines[1].CreditMinor != paid.Total.AmountMinor ||
		!lines[1].At.Equal(recordedAt) || lines[1].DateSource != DateRecorded {
		t.Fatalf("paid order's lines = %+v; want its debit, then a credit dated %v, recorded", lines, recordedAt)
	}
	if o := entriesOf(st, open.ID); len(o) != 1 || o[0].PONumber != "=SUM(A1:A9)" || o[0].DueAt == nil {
		t.Errorf("open order's lines = %+v; want one debit with its PO and due date", o)
	}
	credit, err := f.b2b.Credit(ctx, f.company.ID)
	if err != nil {
		t.Fatalf("credit: %v", err)
	}
	if st.OpeningBalanceMinor != 0 || st.ClosingBalanceMinor != credit.Outstanding.AmountMinor ||
		st.ClosingBalanceMinor != open.Total.AmountMinor {
		t.Errorf("balances = %d → %d; want 0 → %d, the outstanding figure", st.OpeningBalanceMinor,
			st.ClosingBalanceMinor, credit.Outstanding.AmountMinor)
	}
	if st.Aging.CurrentMinor != open.Total.AmountMinor || st.Company.Code != f.company.Code || st.Currency != "USD" {
		t.Errorf("statement = %+v; want the open order current, for this company, in USD", st)
	}

	// A period that starts after everything carries it all as the opening
	// balance and has no lines; and thirty-day terms read as 61–90 days late
	// a hundred days on.
	day := func(d int) string { return time.Now().UTC().AddDate(0, 0, d).Format("2006-01-02") }
	later := f.statement(t, "?from="+day(2)+"&to="+day(100))
	if len(later.Entries) != 0 || later.OpeningBalanceMinor != open.Total.AmountMinor ||
		later.Aging.Days61To90Minor != open.Total.AmountMinor || later.Aging.CurrentMinor != 0 {
		t.Errorf("a later period = %+v; want no lines, the open order as its opening and 61–90 days late", later)
	}

	if rec := gctest.AdminRequest(t, f.app, http.MethodGet,
		f.companyPath()+"/statement?from="+day(3)+"&to="+day(3), nil); rec.Code != http.StatusBadRequest {
		t.Errorf("an empty period = %d, want 400", rec.Code)
	}
}

// A transition the event handler never saw is read back from the order's own
// history at the instant it was recorded; one with no record left is marked
// as noticed rather than given a date nobody wrote down.
func TestAMissedPaymentIsDatedFromTheRecordOrMarkedNoticed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)

	fromHistory := f.placeOnAccount(t, tok, 1, "")
	unrecorded := f.placeOnAccount(t, tok, 1, "")
	reversed := f.placeOnAccount(t, tok, 1, "")
	cancelled := f.placeOnAccount(t, tok, 1, "")
	for _, id := range []int64{fromHistory.ID, unrecorded.ID, reversed.ID} {
		if _, err := f.app.Pay().MarkPaid(ctx, id, ""); err != nil {
			t.Fatalf("mark paid %d: %v", id, err)
		}
	}
	if _, err := f.app.Pay().MarkUnpaid(ctx, reversed.ID); err != nil {
		t.Fatalf("mark unpaid: %v", err)
	}
	if _, err := f.app.Order().Cancel(ctx, cancelled.ID, "ordered twice"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// Nobody has delivered a single event. One order loses every record of
	// its payment, as an order imported already paid never had one.
	if _, err := f.app.DB().ExecContext(ctx, `DELETE FROM outbox_events
		WHERE aggregate_id = $1 AND event_name = 'order.paid'`, unrecorded.ID); err != nil {
		t.Fatalf("lose the event: %v", err)
	}
	if _, err := f.app.DB().ExecContext(ctx, `DELETE FROM admin_audit
		WHERE entity_type = 'order' AND entity_id = $1`, strconv.FormatInt(unrecorded.ID, 10)); err != nil {
		t.Fatalf("lose the audit row: %v", err)
	}
	var recordedAt time.Time
	if err := f.app.DB().QueryRowContext(ctx, `SELECT created_at FROM outbox_events
		WHERE aggregate_id = $1 AND event_name = 'order.paid'`, fromHistory.ID).Scan(&recordedAt); err != nil {
		t.Fatalf("read the event: %v", err)
	}

	before := time.Now()
	st := f.statement(t, "")
	if l := entriesOf(st, fromHistory.ID); len(l) != 2 || l[1].Kind != EntryPayment ||
		!l[1].At.Equal(recordedAt) || l[1].DateSource != DateRecorded {
		t.Errorf("payment read from history = %+v; want it at %v, recorded", l, recordedAt)
	}
	if l := entriesOf(st, unrecorded.ID); len(l) != 2 || l[1].Kind != EntryPayment ||
		l[1].DateSource != DateNoticed || l[1].At.Before(before.Add(-time.Second)) {
		t.Errorf("payment with no record = %+v; want it noticed, now", l)
	}
	// Paid and taken back, it owes what it owed: nothing disagrees with core
	// yet, so the pair waits for its events.
	if l := entriesOf(st, reversed.ID); len(l) != 1 || l[0].Kind != EntryOrder {
		t.Errorf("reversed payment before its events = %+v; want only the order", l)
	}
	if l := entriesOf(st, cancelled.ID); len(l) != 2 || l[1].Kind != EntryCancellation ||
		l[1].CreditMinor != cancelled.Total.AmountMinor || l[1].DateSource != DateRecorded {
		t.Errorf("cancelled order = %+v; want a recorded credit for what it no longer owes", l)
	}
	if st.ClosingBalanceMinor != reversed.Total.AmountMinor {
		t.Errorf("closing = %d, want only the reversed order still owed (%d)", st.ClosingBalanceMinor, reversed.Total.AmountMinor)
	}

	// The events arriving late add the pair, file nothing twice, and leave
	// the balance where it was.
	gctest.DrainOutbox(t, f.app)
	again := f.statement(t, "")
	if l := entriesOf(again, reversed.ID); len(l) != 3 || l[1].Kind != EntryPayment || l[2].Kind != EntryPaymentReversed ||
		l[1].CreditMinor != reversed.Total.AmountMinor || l[2].DebitMinor != reversed.Total.AmountMinor ||
		l[2].BalanceMinor != l[1].BalanceMinor+reversed.Total.AmountMinor {
		t.Errorf("reversed payment = %+v; want a credit, then the same amount owed again", l)
	}
	if len(again.Entries) != len(st.Entries)+2 || again.ClosingBalanceMinor != st.ClosingBalanceMinor {
		t.Errorf("after the events arrived = %d lines closing %d; want %d closing %d",
			len(again.Entries), again.ClosingBalanceMinor, len(st.Entries)+2, st.ClosingBalanceMinor)
	}
}

// A buyer who runs the account reads their company's statement, as JSON or
// as a spreadsheet; a plain buyer does not. The file goes through core's CSV
// writer, so a PO number that looks like a formula arrives as text.
func TestABuyerReadsTheCompanysStatementAsAFile(t *testing.T) {
	f := newFixture(t)
	_, bossTok := f.member(t, "boss@acme.test", RoleAdmin)
	_, juniorTok := f.member(t, "junior@acme.test", RoleBuyer)
	f.placeOnAccount(t, bossTok, 1, "=HYPERLINK(\"x\")")

	if rec := gctest.SessionRequest(t, f.app, juniorTok, http.MethodGet, "/x/b2b/statement", nil); rec.Code != http.StatusForbidden {
		t.Errorf("a plain buyer's statement = %d, want 403", rec.Code)
	}
	rec := gctest.SessionRequest(t, f.app, bossTok, http.MethodGet, "/x/b2b/statement", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("statement = %d: %s", rec.Code, rec.Body)
	}
	var st Statement
	gctest.DecodeData(t, rec, &st)
	if len(st.Entries) != 1 || st.ClosingBalanceMinor != 8000 {
		t.Errorf("buyer's statement = %+v; want one order of 8000", st)
	}

	file := gctest.SessionRequest(t, f.app, bossTok, http.MethodGet, "/x/b2b/statement?format=csv", nil)
	if file.Code != http.StatusOK || !strings.HasPrefix(file.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(file.Header().Get("Content-Disposition"), "statement-"+f.company.Code) {
		t.Fatalf("csv = %d %v", file.Code, file.Header())
	}
	body := file.Body.String()
	for _, want := range []string{
		"date,kind,order_number,po_number,due_date,debit_minor,credit_minor,balance_minor,currency,date_source\n",
		",opening_balance,,,,,,0,USD,\n",
		`"'=HYPERLINK(""x"")"`,
		",8000,0,8000,USD,recorded\n",
		",closing_balance,,,,,,8000,USD,\n",
		",aging_current,,,,,,8000,USD,\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("csv lacks %q:\n%s", want, body)
		}
	}
	if bad := gctest.SessionRequest(t, f.app, bossTok, http.MethodGet, "/x/b2b/statement?format=pdf", nil); bad.Code != http.StatusBadRequest {
		t.Errorf("format=pdf = %d, want 400", bad.Code)
	}
}

// A store whose clock is not UTC closes its statement's days on its own
// midnight, as its reports and invoices do.
func TestAStatementPeriodIsTheStoresCalendar(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.app.Profile().Set(ctx, gocommerce.StoreProfile{Timezone: "Asia/Kolkata"}, nil); err != nil {
		t.Fatalf("set the zone: %v", err)
	}
	lo, hi, err := f.b2b.StatementPeriod(ctx, "2026-09-01", "2026-10-01")
	if err != nil {
		t.Fatalf("period: %v", err)
	}
	if want := time.Date(2026, 8, 31, 18, 30, 0, 0, time.UTC); !lo.Equal(want) {
		t.Errorf("from = %v, want %v: midnight in Kolkata", lo.UTC(), want)
	}
	if want := time.Date(2026, 9, 30, 18, 30, 0, 0, time.UTC); !hi.Equal(want) {
		t.Errorf("to = %v, want %v", hi.UTC(), want)
	}
	st, err := f.b2b.CompanyStatement(ctx, f.company.ID, lo, hi)
	if err != nil {
		t.Fatalf("statement: %v", err)
	}
	b, _ := json.Marshal(st)
	if st.TimeZone != "Asia/Kolkata" || st.From != "2026-09-01" || st.To != "2026-10-01" || st.Aging.AsOf != "2026-09-30" {
		t.Errorf("statement = %s; want September in Kolkata, aged as of the 30th", b)
	}
	if _, _, err := f.b2b.StatementPeriod(ctx, "2026-13-01", ""); err == nil {
		t.Error("a month 13 was accepted")
	}
}
