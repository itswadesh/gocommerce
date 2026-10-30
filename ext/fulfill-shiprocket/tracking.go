package shiprocket

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

// Tracking: Shiprocket's webhook, the poll that catches what it missed, and
// the failed deliveries (NDR) an operator answers.

// trackingHook is the last segment of the webhook address. Shiprocket refuses
// an address containing "shiprocket", "kartrocket", "sr" or "kr" (API docs,
// Webhooks), which rules out this module's own route namespace; the engine
// serves it at /api/tracking-updates/courier-aggregator instead.
const trackingHook = "courier-aggregator"

// TrackingHook implements gocommerce.TrackingReceiver.
func (m *Module) TrackingHook() string { return trackingHook }

// Tracking implements gocommerce.TrackingReceiver.
func (m *Module) Tracking() http.Handler { return http.HandlerFunc(m.handleWebhook) }

// ist is the zone Shiprocket writes its times in.
var ist = time.FixedZone("IST", 5*3600+30*60)

// scan is one line of a parcel's journey, as Shiprocket reports it.
type scan struct {
	Date     string `json:"date"`
	Status   string `json:"status,omitempty"`
	Activity string `json:"activity"`
	Location string `json:"location,omitempty"`
	Label    string `json:"sr-status-label,omitempty"`
}

// srUpdate is what one webhook or one poll said about a waybill.
type srUpdate struct {
	code  int
	label string
	at    time.Time
	scans []scan
	etd   string
}

// engineStatus is the engine's word for one of Shiprocket's documented
// shipment status codes (API docs, Tracking › Shipment Status Codes). An empty
// status with known true is a code that does not move the order — the parcel
// is booked and waiting for its courier. A code missing from the table is not
// known; the caller keeps the parcel in transit and logs it, and never takes
// it for a delivery.
func engineStatus(code int) (status string, known bool) {
	switch code {
	case 6, 18, 22, 38, 39, 42, 48, 49, 50, 51, 54, 55, 56, 57:
		// Shipped, In Transit, Delayed, Reached at Destination Hub, Misrouted,
		// Picked Up, and the international legs: on its way.
		return gocommerce.TrackingInTransit, true
	case 17:
		return gocommerce.TrackingOutForDelivery, true
	case 21, 77:
		// Undelivered; Issue related to the recipient: an NDR.
		return gocommerce.TrackingDeliveryFailed, true
	case 7:
		return gocommerce.TrackingDelivered, true
	case 9, 14, 40, 41, 46, 75, 78:
		// RTO Initiated, Acknowledged, NDR, Out for Delivery, In Transit,
		// Lock, Reached back at seller city: on its way back.
		return gocommerce.TrackingReturning, true
	case 10:
		return gocommerce.TrackingReturned, true
	case 12, 24, 25, 44, 76:
		// Lost, Destroyed, Damaged, Disposed Off, Untraceable.
		return gocommerce.TrackingLost, true
	case 8, 45:
		// Cancelled; Cancelled before dispatched.
		return gocommerce.TrackingCancelled, true
	case 1, 2, 3, 4, 5, 11, 13, 15, 16, 19, 20, 27, 52, 59, 60, 61, 62, 63, 67, 68, 71, 72:
		// From AWB Assigned to Out For Pickup, Pickup Error and Exception,
		// Cancellation Requested and the warehouse steps: not yet with the
		// courier, or not yet decided.
		return "", true
	}
	return "", false
}

// terminal are the codes after which nothing more will be heard.
var terminal = []int{7, 8, 10, 12, 24, 25, 44, 45}

// apply records what Shiprocket said on the module's row and, where it moves
// the order, through the engine. An update older than the last one applied is
// dropped: Shiprocket's events can arrive out of order, and a late "in
// transit" must not undo "out for delivery".
func (m *Module) apply(ctx context.Context, row *shipment, u srUpdate) error {
	if row.trackedAt != nil && u.at.Before(*row.trackedAt) {
		return nil
	}
	scans, err := json.Marshal(u.scans)
	if err != nil {
		return err
	}
	if err := m.save(ctx, row.ID, map[string]any{
		"status_code": u.code, "status": u.label, "scans": string(scans), "etd": u.etd, "tracked_at": u.at,
	}); err != nil {
		return err
	}
	status, known := engineStatus(u.code)
	if !known {
		m.log.Warn("shiprocket: a shipment status this module does not know; keeping the parcel in transit",
			"status_id", u.code, "status", u.label, "awb", row.AWB)
		status = gocommerce.TrackingInTransit
	}
	if status == "" {
		return nil
	}
	if status == gocommerce.TrackingCancelled && row.CancelledAt == nil {
		// Shiprocket called it off: the booking is over here too, so the card
		// stops offering to cancel it and the next booking takes a new id.
		now := time.Now()
		row.CancelledAt = &now
		if err := m.save(ctx, row.ID, map[string]any{"cancelled_at": now}); err != nil {
			return err
		}
	}
	order, err := m.app.Order().Get(ctx, row.OrderID)
	if err != nil {
		return err
	}
	fid := recordedAs(order, row)
	if fid == 0 {
		// Booked but never recorded on the order, or a return's waybill:
		// the row is the whole record.
		return nil
	}
	detail := u.label
	if len(u.scans) > 0 && u.scans[0].Activity != "" {
		detail = u.scans[0].Activity
	}
	_, err = m.app.Ship().Track(ctx, fid, gocommerce.TrackingUpdate{Status: status, Detail: detail, At: u.at})
	return err
}

// handleWebhook takes Shiprocket's tracking webhook (API docs, Webhooks): a
// POST of {awb, current_status, shipment_status, shipment_status_id,
// current_timestamp, etd, scans[], is_return, …}, with the security token the
// store set in Shiprocket in the x-api-key header.
//
// The token is the store's id, a dot and a secret, generated in the panel: the
// address names no store, because a store id can itself hold a word Shiprocket
// refuses in an address, so the token says which store's secret to check. A
// call without a matching one changes nothing. Every call is answered 200,
// because that is the only answer Shiprocket accepts from a webhook; what
// went wrong is in the log, and the poll catches up on what was missed.
func (m *Module) handleWebhook(w http.ResponseWriter, r *http.Request) {
	accepted := false
	defer func() { gocommerce.Respond(w, http.StatusOK, map[string]bool{"accepted": accepted}) }()

	key := r.Header.Get("x-api-key")
	store, _, _ := strings.Cut(key, ".")
	if store == "" {
		m.log.Warn("shiprocket: a tracking call without a token")
		return
	}
	ctx := gocommerce.WithStore(r.Context(), store)
	if !m.refresh(ctx) {
		m.log.Warn("shiprocket: a tracking call for a store that has not set Shiprocket up", "store", store)
		return
	}
	want := m.conf(ctx).WebhookToken
	if want == "" || subtle.ConstantTimeCompare([]byte(key), []byte(want)) != 1 {
		m.log.Warn("shiprocket: a tracking call whose token does not match", "store", store)
		return
	}
	var ev struct {
		AWB              string `json:"awb"`
		ShipmentStatus   string `json:"shipment_status"`
		ShipmentStatusID number `json:"shipment_status_id"`
		CurrentTimestamp string `json:"current_timestamp"`
		ETD              string `json:"etd"`
		Scans            []scan `json:"scans"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&ev); err != nil || ev.AWB == "" {
		m.log.Warn("shiprocket: a tracking call this module could not read", "store", store)
		return
	}
	row, err := m.shipmentByAWB(ctx, ev.AWB)
	if err != nil {
		m.log.Info("shiprocket: tracking for a waybill this store did not book here", "store", store, "awb", ev.AWB)
		return
	}
	at, err := time.ParseInLocation("02 01 2006 15:04:05", ev.CurrentTimestamp, ist)
	if err != nil {
		at = time.Now()
	}
	if err := m.apply(ctx, row, srUpdate{code: int(ev.ShipmentStatusID), label: ev.ShipmentStatus,
		at: at, scans: newestFirst(ev.Scans), etd: ev.ETD}); err != nil {
		m.log.Error("shiprocket: could not record a tracking update", "store", store, "awb", ev.AWB, "error", err)
		return
	}
	accepted = true
}

// newestFirst orders scans as the panel shows them. Shiprocket's webhook
// lists them oldest first and its tracking API newest first.
func newestFirst(scans []scan) []scan {
	if len(scans) > 1 && scans[0].Date < scans[len(scans)-1].Date {
		out := make([]scan, len(scans))
		for i, s := range scans {
			out[len(scans)-1-i] = s
		}
		return out
	}
	return scans
}

// handleWebhookToken makes a new token for this store's webhook and saves it
// sealed with the plugin's other secrets. It is shown once, here: whoever
// pastes it into Shiprocket copies it now, and a new one replaces it.
func (m *Module) handleWebhookToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	store, ok := gocommerce.StoreFrom(ctx)
	if !ok || store == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("name the store this token is for"))
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	token := store + "." + base64.RawURLEncoding.EncodeToString(secret)
	if _, err := m.app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{
		Settings: map[string]any{"webhook_token": token}}); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]string{
		"path": "/api/tracking-updates/" + trackingHook, "token": token,
	})
}

// --------------------------------------------------------------- polling

// Poll asks Shiprocket about every parcel of this store still on its way, so
// a webhook that never arrived — Shiprocket not set up to send it, or a call
// lost — is caught up on. "Tracking › Get Tracking Data for Multiple AWBS":
// POST /v1/external/courier/track/awbs {awbs: [...]}, at most 50 at a time →
// {awb: {tracking_data: {track_status, shipment_status,
// shipment_track_activities[], etd}}}.
func (m *Module) Poll(ctx context.Context) (int, error) {
	if !m.refresh(ctx) {
		return 0, nil
	}
	rows, err := m.pending(ctx)
	if err != nil {
		return 0, err
	}
	return m.track(ctx, rows)
}

// track asks Shiprocket about these rows and applies what it says.
func (m *Module) track(ctx context.Context, rows []*shipment) (int, error) {
	byAWB := map[string]*shipment{}
	awbs := make([]string, 0, len(rows))
	for _, r := range rows {
		byAWB[r.AWB] = r
		awbs = append(awbs, r.AWB)
	}
	applied := 0
	for start := 0; start < len(awbs); start += 50 {
		batch := awbs[start:min(start+50, len(awbs))]
		var out map[string]struct {
			Data struct {
				TrackStatus    number `json:"track_status"`
				ShipmentStatus number `json:"shipment_status"`
				Activities     []scan `json:"shipment_track_activities"`
				ETD            string `json:"etd"`
			} `json:"tracking_data"`
		}
		if err := m.call(ctx, http.MethodPost, "/v1/external/courier/track/awbs", nil,
			map[string]any{"awbs": batch}, &out); err != nil {
			return applied, err
		}
		for awb, t := range out {
			row, ok := byAWB[awb]
			if !ok || t.Data.TrackStatus != 1 || t.Data.ShipmentStatus == 0 {
				continue
			}
			scans := newestFirst(t.Data.Activities)
			at := time.Now()
			if len(scans) > 0 {
				if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", scans[0].Date, ist); err == nil {
					at = parsed
				}
			}
			label := ""
			if len(scans) > 0 {
				label = firstNonEmpty(scans[0].Label, scans[0].Activity)
			}
			if err := m.apply(ctx, row, srUpdate{code: int(t.Data.ShipmentStatus), label: label,
				at: at, scans: scans, etd: t.Data.ETD}); err != nil {
				m.log.Error("shiprocket: could not record a polled update", "awb", awb, "error", err)
				continue
			}
			applied++
		}
	}
	return applied, nil
}

// pending is what is worth asking about: booked, not cancelled, not finished,
// and booked in the last sixty days, the longest any parcel spends in transit
// here; the ones heard from least recently first.
func (m *Module) pending(ctx context.Context) ([]*shipment, error) {
	rows, err := m.app.DB().QueryContext(ctx, `
		SELECT `+shipmentColumns+` FROM shiprocket_shipments
		WHERE awb <> '' AND cancelled_at IS NULL
		  AND NOT (coalesce(status_code, 0) = ANY($1::int[]))
		  AND created_at > now() - interval '60 days'
		ORDER BY coalesce(tracked_at, created_at)
		LIMIT 500`, intArray(terminal))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*shipment
	for rows.Next() {
		s, err := scanShipment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func intArray(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = fmt.Sprint(n)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// startPolling polls every store on a ticker until shutdown.
func (m *Module) startPolling(ctx context.Context) error {
	go func() {
		t := time.NewTicker(m.cfg.PollEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			err := m.app.ForEachStore(ctx, func(ctx context.Context) error {
				_, err := m.Poll(ctx)
				return err
			})
			if err != nil && ctx.Err() == nil {
				m.log.Error("shiprocket: tracking poll failed", "error", err)
			}
		}
	}()
	return nil
}

// ------------------------------------------------------------------- NDR

// NDR is a failed delivery as Shiprocket records it.
type NDR struct {
	Reason   string       `json:"reason"`
	Attempts int          `json:"attempts"`
	RaisedAt string       `json:"raised_at,omitempty"`
	History  []NDRAttempt `json:"history"`
}

// NDRAttempt is one failed attempt.
type NDRAttempt struct {
	Reason   string `json:"reason"`
	Attempt  int    `json:"attempt"`
	RaisedAt string `json:"raised_at,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

// ndr reads one. "NDR › Get Specific NDR Shipment Details": GET
// /v1/external/ndr/{awb} → data[] {reason, attempts, ndr_raised_at,
// history[] {ndr_reason, ndr_attempt, ndr_raised_at, comment}}.
func (m *Module) ndr(ctx context.Context, awb string) (*NDR, error) {
	var out struct {
		Data []struct {
			Reason   string `json:"reason"`
			Attempts number `json:"attempts"`
			RaisedAt string `json:"ndr_raised_at"`
			History  []struct {
				Reason   string `json:"ndr_reason"`
				Attempt  number `json:"ndr_attempt"`
				RaisedAt string `json:"ndr_raised_at"`
				Comment  string `json:"comment"`
			} `json:"history"`
		} `json:"data"`
	}
	if err := m.call(ctx, http.MethodGet, "/v1/external/ndr/"+awb, nil, nil, &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, gocommerce.NotFoundf("Shiprocket has no failed delivery on waybill %s", awb)
	}
	d := out.Data[0]
	res := &NDR{Reason: d.Reason, Attempts: int(d.Attempts), RaisedAt: d.RaisedAt, History: []NDRAttempt{}}
	for _, h := range d.History {
		res.History = append(res.History, NDRAttempt{Reason: h.Reason, Attempt: int(h.Attempt), RaisedAt: h.RaisedAt, Comment: h.Comment})
	}
	return res, nil
}

// NDRAction is the operator's answer to a failed delivery. "NDR › Action
// NDR": POST /v1/external/ndr/{awb}/action {action, comments, phone?,
// address1?, address2?, deferred_date?}. Shiprocket documents a third action,
// fake-attempt, which disputes the courier's attempt and needs proof — an
// audio or image URL — that the panel has no way to collect, so it is not
// offered.
type NDRAction struct {
	Action       string `json:"action"`
	Comments     string `json:"comments"`
	Phone        string `json:"phone,omitempty"`
	Address1     string `json:"address1,omitempty"`
	Address2     string `json:"address2,omitempty"`
	DeferredDate string `json:"deferred_date,omitempty"`
}

func (a NDRAction) validate() error {
	if a.Action != "re-attempt" && a.Action != "return" {
		return gocommerce.Validationf("action is re-attempt or return")
	}
	if strings.TrimSpace(a.Comments) == "" {
		return gocommerce.Validationf("Shiprocket needs a comment with every answer to a failed delivery")
	}
	if a.DeferredDate != "" {
		if _, err := time.Parse("2006-01-02", a.DeferredDate); err != nil {
			return gocommerce.Validationf("deferred_date is a date, YYYY-MM-DD")
		}
	}
	return nil
}
