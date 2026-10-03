package b2b

import (
	"cmp"
	"context"
	"io"
	"slices"
	"strconv"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// What a statement's lines are.
const (
	EntryOrder = "order"
	// EntryPayment is an order on account recorded as paid: a credit.
	EntryPayment = "payment"
	// EntryPaymentReversed is a payment taken back as recorded in error
	// (core's mark-unpaid): the debt is owed again, so it is a debit.
	EntryPaymentReversed = "payment_reversed"
	// EntryCancellation is an order cancelled while it was still owed: a
	// credit. Cancelling one already paid moves nothing here — that money is
	// a refund, settled on the order.
	EntryCancellation = "cancellation"
)

// How an entry's date is known.
const (
	// DateRecorded is core's own record of the moment: the order's creation,
	// or the event written in the transaction that paid, unpaid or cancelled it.
	DateRecorded = "recorded"
	// DateNoticed is a transition no record of which survived — an order
	// imported already paid, an event deleted. It is dated when this module
	// first saw it, and happened at or before then.
	DateNoticed = "noticed"
)

// StatementCompany names whose statement it is.
type StatementCompany struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// StatementEntry is one line of a statement: an order placed on account, or
// something that changed what it owes. Date is the civil date in the store's
// time zone; At is the instant.
type StatementEntry struct {
	Date        string     `json:"date"`
	At          time.Time  `json:"at"`
	Kind        string     `json:"kind"`
	OrderID     int64      `json:"order_id"`
	OrderNumber string     `json:"order_number"`
	PONumber    string     `json:"po_number"`
	DueAt       *time.Time `json:"due_at"`
	DebitMinor  int64      `json:"debit_minor"`
	CreditMinor int64      `json:"credit_minor"`
	// BalanceMinor is what the company owed once this line was written.
	BalanceMinor int64  `json:"balance_minor"`
	DateSource   string `json:"date_source"`
}

// Aging is what is owed at the end of a statement, by how late it is.
// The five figures sum to the closing balance.
type Aging struct {
	// AsOf is the last day the statement covers.
	AsOf            string `json:"as_of"`
	CurrentMinor    int64  `json:"current_minor"`
	Days1To30Minor  int64  `json:"days_1_30_minor"`
	Days31To60Minor int64  `json:"days_31_60_minor"`
	Days61To90Minor int64  `json:"days_61_90_minor"`
	DaysOver90Minor int64  `json:"days_over_90_minor"`
}

// Statement is a company's account over a period.
//
// From is the first day it covers and To the day after its last — exclusive,
// the rule every date range in the engine follows — both civil dates in
// TimeZone, the store's.
type Statement struct {
	Company             StatementCompany `json:"company"`
	Currency            string           `json:"currency"`
	TimeZone            string           `json:"time_zone"`
	From                string           `json:"from"`
	To                  string           `json:"to"`
	OpeningBalanceMinor int64            `json:"opening_balance_minor"`
	ClosingBalanceMinor int64            `json:"closing_balance_minor"`
	Entries             []StatementEntry `json:"entries"`
	Aging               Aging            `json:"aging"`
}

const civilDate = "2006-01-02"

// StatementPeriod reads a statement's from and to, both YYYY-MM-DD in the
// store's time zone with to exclusive. Leaving both out is the month to date;
// leaving out from starts at the first of the month the period ends in.
func (m *Module) StatementPeriod(ctx context.Context, from, to string) (time.Time, time.Time, error) {
	loc := m.app.Profile().LocationOf(ctx)
	parse := func(name, s string) (time.Time, error) {
		d, err := time.ParseInLocation(civilDate, s, loc)
		if err != nil {
			return time.Time{}, gocommerce.Validationf("%s must be a date, YYYY-MM-DD", name)
		}
		return d, nil
	}
	var lo, hi time.Time
	var err error
	if to == "" {
		now := time.Now().In(loc)
		hi = time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc)
	} else if hi, err = parse("to", to); err != nil {
		return lo, hi, err
	}
	if from == "" {
		last := hi.AddDate(0, 0, -1)
		lo = time.Date(last.Year(), last.Month(), 1, 0, 0, 0, 0, loc)
	} else if lo, err = parse("from", from); err != nil {
		return lo, hi, err
	}
	if !lo.Before(hi) {
		return lo, hi, gocommerce.Validationf("from must be before to; to is the day after the last one the statement covers")
	}
	return lo, hi, nil
}

// statementOrder is an order on account, as the statement reads it.
type statementOrder struct {
	id       int64
	number   string
	po       string
	total    int64
	placed   time.Time
	due      time.Time
	paid     bool
	canceled bool
}

type statementEvent struct {
	at     time.Time
	kind   string
	source string
	order  *statementOrder
	seq    int64
}

// kindRank orders two entries at the same instant: an order before anything
// that happened to it.
var kindRank = map[string]int{EntryOrder: 0, EntryPayment: 1, EntryPaymentReversed: 2, EntryCancellation: 3}

// CompanyStatement is a company's account between lo and hi: what it owed at
// lo, every order placed on account and every payment, reversal and
// cancellation since, and what is owed at hi by how late it is.
//
// What is owed is worked out exactly as the credit check works it out — an
// order on account, neither paid nor cancelled — so a statement ending now
// closes on the outstanding figure the guard enforces the limit against.
// Missed transitions are filed first, so the two can never disagree.
func (m *Module) CompanyStatement(ctx context.Context, companyID int64, lo, hi time.Time) (*Statement, error) {
	c, err := m.Company(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if err := m.reconcileAccounts(ctx, companyID); err != nil {
		return nil, err
	}
	loc := lo.Location()
	st := &Statement{
		Company:  StatementCompany{ID: c.ID, Code: c.Code, Name: c.Name},
		Currency: m.app.Config().Currency,
		TimeZone: loc.String(),
		From:     lo.Format(civilDate),
		To:       hi.Format(civilDate),
		Entries:  []StatementEntry{},
	}

	rows, err := m.db.QueryContext(ctx, `
		SELECT o.id, o.number, coalesce(bo.po_number, o.metadata -> 'b2b' ->> 'po_number', ''),
		       o.total_minor, o.created_at,
		       coalesce(bo.due_at, o.created_at + make_interval(days =>
		                coalesce((o.metadata -> 'b2b' ->> 'net_days')::int, 30)))
		FROM orders o
		LEFT JOIN b2b_orders bo ON bo.order_id = o.id
		WHERE o.payment_provider = $2
		  AND o.metadata -> 'b2b' ->> 'company_id' = $1
		  AND o.created_at < $3
		ORDER BY o.created_at, o.id`,
		strconv.FormatInt(companyID, 10), CodeOnAccount, hi)
	if err != nil {
		return nil, err
	}
	orders := map[int64]*statementOrder{}
	var events []statementEvent
	for rows.Next() {
		o := &statementOrder{}
		if err := rows.Scan(&o.id, &o.number, &o.po, &o.total, &o.placed, &o.due); err != nil {
			rows.Close()
			return nil, err
		}
		orders[o.id] = o
		events = append(events, statementEvent{at: o.placed, kind: EntryOrder, source: DateRecorded, order: o})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(orders))
	for id := range orders {
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		erows, err := m.db.QueryContext(ctx, `
			SELECT id, order_id, kind, at, date_source FROM b2b_account_entries
			WHERE order_id = ANY($1) AND at < $2`, ids, hi)
		if err != nil {
			return nil, err
		}
		for erows.Next() {
			var e statementEvent
			var orderID int64
			if err := erows.Scan(&e.seq, &orderID, &e.kind, &e.at, &e.source); err != nil {
				erows.Close()
				return nil, err
			}
			e.order = orders[orderID]
			events = append(events, e)
		}
		erows.Close()
		if err := erows.Err(); err != nil {
			return nil, err
		}
	}
	slices.SortStableFunc(events, func(a, b statementEvent) int {
		return cmp.Or(a.at.Compare(b.at), cmp.Compare(a.order.id, b.order.id),
			cmp.Compare(kindRank[a.kind], kindRank[b.kind]), cmp.Compare(a.seq, b.seq))
	})

	// A line's money is how much it moved what the company owes, which is
	// why a second payment of a paid order, or a cancellation of one, moves
	// nothing: the debt was settled once.
	var balance int64
	for _, e := range events {
		o := e.order
		owed := !o.paid && !o.canceled
		var debit, credit int64
		switch e.kind {
		case EntryOrder:
			debit = o.total
		case EntryPayment:
			if owed {
				credit = o.total
			}
			o.paid = true
		case EntryPaymentReversed:
			if o.paid && !o.canceled {
				debit = o.total
			}
			o.paid = false
		case EntryCancellation:
			if owed {
				credit = o.total
			}
			o.canceled = true
		}
		balance += debit - credit
		if e.at.Before(lo) {
			st.OpeningBalanceMinor = balance
			continue
		}
		due := o.due
		st.Entries = append(st.Entries, StatementEntry{
			Date: e.at.In(loc).Format(civilDate), At: e.at, Kind: e.kind,
			OrderID: o.id, OrderNumber: o.number, PONumber: o.po, DueAt: &due,
			DebitMinor: debit, CreditMinor: credit, BalanceMinor: balance, DateSource: e.source,
		})
	}
	st.ClosingBalanceMinor = balance

	// Days late are counted in whole days of the store's calendar, the way a
	// statement is read: due on the 30th is current on the 30th and a day late
	// on the 31st, whatever hour the order was placed at.
	last := hi.AddDate(0, 0, -1)
	st.Aging.AsOf = last.Format(civilDate)
	asOf := civilDay(last, loc)
	for _, o := range orders {
		if o.paid || o.canceled {
			continue
		}
		switch late := asOf - civilDay(o.due, loc); {
		case late <= 0:
			st.Aging.CurrentMinor += o.total
		case late <= 30:
			st.Aging.Days1To30Minor += o.total
		case late <= 60:
			st.Aging.Days31To60Minor += o.total
		case late <= 90:
			st.Aging.Days61To90Minor += o.total
		default:
			st.Aging.DaysOver90Minor += o.total
		}
	}
	return st, nil
}

// civilDay numbers a calendar day in a zone, so two of them subtract to a
// count of days with no daylight-saving hour in the way.
func civilDay(t time.Time, loc *time.Location) int64 {
	y, mo, d := t.In(loc).Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.UTC).Unix() / 86400
}

// WriteStatementCSV writes a statement through core's CSV writer, whose
// escaping is the one rule every export here shares (D62): a PO number is a
// buyer's own text, and one beginning with = is a formula to a spreadsheet.
//
// One table rather than three: the opening balance is its first row, the
// closing balance and the aging buckets its last, each named in kind with the
// amount in balance_minor — so the file still sorts and sums in a spreadsheet.
func WriteStatementCSV(w io.Writer, st *Statement) error {
	cw, err := gocommerce.NewCSVWriter(w, []string{"date", "kind", "order_number", "po_number",
		"due_date", "debit_minor", "credit_minor", "balance_minor", "currency", "date_source"})
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(st.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	n := strconv.FormatInt
	summary := func(date, kind string, amount int64) error {
		return cw.Write([]string{date, kind, "", "", "", "", "", n(amount, 10), st.Currency, ""})
	}
	if err := summary(st.From, "opening_balance", st.OpeningBalanceMinor); err != nil {
		return err
	}
	for _, e := range st.Entries {
		due := ""
		if e.DueAt != nil {
			due = e.DueAt.In(loc).Format(civilDate)
		}
		if err := cw.Write([]string{e.Date, e.Kind, e.OrderNumber, e.PONumber, due,
			n(e.DebitMinor, 10), n(e.CreditMinor, 10), n(e.BalanceMinor, 10), st.Currency, e.DateSource}); err != nil {
			return err
		}
	}
	for _, row := range []struct {
		kind   string
		amount int64
	}{
		{"closing_balance", st.ClosingBalanceMinor},
		{"aging_current", st.Aging.CurrentMinor},
		{"aging_1_30", st.Aging.Days1To30Minor},
		{"aging_31_60", st.Aging.Days31To60Minor},
		{"aging_61_90", st.Aging.Days61To90Minor},
		{"aging_over_90", st.Aging.DaysOver90Minor},
	} {
		if err := summary(st.Aging.AsOf, row.kind, row.amount); err != nil {
			return err
		}
	}
	return cw.Flush()
}

// ------------------------------------------------------------- payment dates

// accountKinds maps the events this module files to the entry each becomes.
var accountKinds = map[string]string{
	gocommerce.EventOrderPaid:      EntryPayment,
	gocommerce.EventOrderUnpaid:    EntryPaymentReversed,
	gocommerce.EventOrderCancelled: EntryCancellation,
}

// onAccountEvent files a payment, a reversal or a cancellation of an order on
// account at the moment core wrote it: e.At is the outbox row's created_at,
// the now() of the transaction that changed the order, however late the
// event is delivered. Redelivery files nothing new — the entry's key is the
// order, the kind and that instant.
func (m *Module) onAccountEvent(ctx context.Context, e gocommerce.Event) error {
	kind, ok := accountKinds[e.Name]
	if !ok {
		return nil
	}
	var ev gocommerce.OrderEvent
	if err := e.Decode(&ev); err != nil {
		return err
	}
	if ev.Provider != CodeOnAccount {
		return nil
	}
	return m.fileEntry(ctx, ev.OrderID, kind, e.At, DateRecorded, e.ID)
}

func (m *Module) fileEntry(ctx context.Context, orderID int64, kind string, at time.Time, source, eventID string) error {
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO b2b_account_entries (order_id, kind, at, date_source, event_id)
		VALUES ($1, $2, $3, $4, nullif($5, ''))
		ON CONFLICT (order_id, kind, at) DO NOTHING`, orderID, kind, at, source, eventID)
	return err
}

// reconcileAccounts files what the event handler missed: a payment recorded
// before this module subscribed, an event that died in the outbox, an order
// imported already paid. An order whose entries disagree with what core says
// of it now is read back from its history — core's audit rows and events
// carry the instant of every transition — and only a transition with no
// record left is filed as noticed, now. Nothing is ever dated by guesswork.
//
// companyID narrows it to one company's orders, for a statement about to be
// read; zero is every company's, for the hourly pass.
func (m *Module) reconcileAccounts(ctx context.Context, companyID int64) error {
	args := []any{CodeOnAccount}
	where := `o.payment_provider = $1 AND o.metadata ? 'b2b'`
	if companyID > 0 {
		args = append(args, strconv.FormatInt(companyID, 10))
		where += ` AND o.metadata -> 'b2b' ->> 'company_id' = $2`
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT o.id, o.payment_status NOT IN ('pending', 'failed'), o.status = 'cancelled',
		       coalesce(e.paid, 0) > 0, coalesce(e.cancelled, false)
		FROM orders o
		LEFT JOIN (
		    SELECT order_id,
		           count(*) FILTER (WHERE kind = 'payment') - count(*) FILTER (WHERE kind = 'payment_reversed') AS paid,
		           bool_or(kind = 'cancellation') AS cancelled
		    FROM b2b_account_entries GROUP BY order_id
		) e ON e.order_id = o.id
		WHERE `+where, args...)
	if err != nil {
		return err
	}
	type drift struct {
		id                        int64
		paid, cancelled           bool
		filedPaid, filedCancelled bool
	}
	var stale []drift
	for rows.Next() {
		var d drift
		if err := rows.Scan(&d.id, &d.paid, &d.cancelled, &d.filedPaid, &d.filedCancelled); err != nil {
			rows.Close()
			return err
		}
		if d.paid != d.filedPaid || (d.cancelled && !d.filedCancelled) {
			stale = append(stale, d)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range stale {
		if err := m.refileOrder(ctx, d.id); err != nil {
			return err
		}
		paid, cancelled, err := m.filedState(ctx, d.id)
		if err != nil {
			return err
		}
		now := time.Now()
		switch {
		case d.paid && !paid:
			err = m.fileEntry(ctx, d.id, EntryPayment, now, DateNoticed, "")
		case !d.paid && paid:
			err = m.fileEntry(ctx, d.id, EntryPaymentReversed, now, DateNoticed, "")
		}
		if err == nil && d.cancelled && !cancelled {
			err = m.fileEntry(ctx, d.id, EntryCancellation, now, DateNoticed, "")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// refileOrder reads an order's history and files every payment, reversal and
// cancellation in it at the instant it was recorded.
func (m *Module) refileOrder(ctx context.Context, orderID int64) error {
	const pageSize = 200
	for offset := 0; ; offset += pageSize {
		entries, total, err := m.app.Order().Timeline(ctx, orderID, pageSize, offset)
		if err != nil {
			return err
		}
		for _, e := range entries {
			kind, ok := accountKinds[e.Name]
			if !ok {
				continue
			}
			if err := m.fileEntry(ctx, orderID, kind, e.At, DateRecorded, e.EventID); err != nil {
				return err
			}
		}
		if offset+pageSize >= total {
			return nil
		}
	}
}

func (m *Module) filedState(ctx context.Context, orderID int64) (paid, cancelled bool, err error) {
	err = m.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE kind = 'payment') - count(*) FILTER (WHERE kind = 'payment_reversed') > 0,
		       coalesce(bool_or(kind = 'cancellation'), false)
		FROM b2b_account_entries WHERE order_id = $1`, orderID).Scan(&paid, &cancelled)
	return
}
