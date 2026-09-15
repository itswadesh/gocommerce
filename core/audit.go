package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The operator audit trail: who did what, written in the same transaction as
// the change it records (D26).
//
// It is modelled on outbox.go and split the same way. The writer is an
// unexported package-level function, and the reader is an exported service, so
// a module can read the trail through App.Audit() but can neither forge a row
// nor delete one — AGENTS rule 3 holding by the Go package boundary rather than
// by documentation.
//
// It is a sibling of the outbox, not a projection of it. outbox_events is a
// delivery queue: it announces order state to subscribers, carries no actor,
// has one aggregate type, and its rows can be retried and dead-lettered. An
// event that may be dropped is not an audit. The two are correlated instead, by
// changes.event naming the outbox event the same transaction published, so an
// order timeline drawn from both renders each fact once.

// The three kinds of caller an admin route can have. SuperuserFrom returning
// nil used to be the whole answer and it was two answers at once: a script
// holding a static admin token, and the engine's own background work. An
// operator reading "cancelled by the system" needs to know which.
const (
	ActorOperator = "operator"
	ActorToken    = "token"
	ActorSystem   = "system"
)

// The entity vocabulary: one name per record an operator can actually point at
// and ask "what happened to this".
//
// AuditEntityRole is keyed by role name rather than by an id — see
// entity_id's comment in M20 for why the column is text. There is no stock
// entity: since M26 a stock movement is a row in stock_movements, which records
// the same act with more of it (see audit_http.go).
const (
	AuditEntityOrder      = "order"
	AuditEntityProduct    = "product"
	AuditEntityCategory   = "category"
	AuditEntityCollection = "collection"
	AuditEntityDiscount   = "discount"
	AuditEntityTaxRate    = "tax_rate"
	AuditEntityLocation   = "location"
	AuditEntitySuperuser  = "superuser"
	AuditEntityInvitation = "invitation"
	AuditEntityRole       = "role"
	// AuditEntityTaxonomyAttribute is keyed by handle rather than by an id,
	// like a role: taxonomy_attributes has no id of its own, because the
	// handle is what a category's metadata names.
	AuditEntityTaxonomyAttribute = "taxonomy_attribute"
	AuditEntityPlugin            = "plugin"
	// AuditEntityNotificationTemplate is the wording of one message, keyed
	// "channel/event".
	AuditEntityNotificationTemplate = "notification_template"
)

// AuditEntityTypes is the catalogue, in the panel's display order. The database
// carries no CHECK on entity_type, so this list and the tests over it are the
// only thing policing the vocabulary.
var AuditEntityTypes = []string{
	AuditEntityOrder,
	AuditEntityProduct,
	AuditEntityCategory,
	AuditEntityCollection,
	AuditEntityDiscount,
	AuditEntityTaxRate,
	AuditEntityLocation,
	AuditEntitySuperuser,
	AuditEntityInvitation,
	AuditEntityRole,
	AuditEntityTaxonomyAttribute,
	AuditEntityPlugin,
	AuditEntityNotificationTemplate,
}

// The action vocabulary, `<record>.<verb>`, declared here and nowhere else.
//
// These are deliberately NOT event names. EditLines and Update both emit
// order.edited, so a shared vocabulary would make "who changed the lines" and
// "who changed the shipping address" the same row; and order.refund and
// order.shipment_update are operator acts with no event behind them at all.
const (
	AuditOrderCreate    = "order.create"
	AuditOrderCancel    = "order.cancel"
	AuditOrderEditLines = "order.edit_lines"
	AuditOrderUpdate    = "order.update"
	// Writing the shop's own note on an order. Its own verb rather than
	// order.update, for the same reason edit_lines is not update: a note is not
	// a correction to what was recorded about the sale, and it is the one write
	// on an order that reaches nobody outside the store — it publishes no event
	// at all, so this row is the only record that it happened.
	AuditOrderNote       = "order.note"
	AuditOrderMarkPaid   = "order.mark_paid"
	AuditOrderMarkUnpaid = "order.mark_unpaid"
	AuditOrderMarkFailed = "order.mark_failed"
	AuditOrderRefund     = "order.refund"
	// Settling a refund the engine asked for and never heard back about. Its
	// own verb rather than a second order.refund, because a person deciding
	// what happened to money the gateway may or may not have moved is a
	// different act from asking for the refund in the first place.
	AuditOrderRefundSettle   = "order.refund_settle"
	AuditOrderDeliver        = "order.deliver"
	AuditOrderUndeliver      = "order.undeliver"
	AuditOrderShip           = "order.ship"
	AuditOrderShipmentUpdate = "order.shipment_update"
	AuditOrderShipmentDelete = "order.shipment_delete"
	// Goods coming back, and that record taken back. Their own verbs rather
	// than order.edit_lines, because a return changes nothing about what was
	// agreed — it says what happened to the goods afterwards.
	AuditOrderReturn         = "order.return"
	AuditOrderReturnWithdraw = "order.return_withdraw"
	// An operator reading the guest's access token back out. It changes
	// nothing, and it is recorded anyway — that is the whole point of the
	// route: the token is a bearer credential, so the only control over handing
	// one out is a row saying who asked for it and when. See
	// Orders.RevealAccessToken.
	AuditOrderTokenReveal = "order.token_reveal"

	AuditProductCreate         = "product.create"
	AuditProductUpdate         = "product.update"
	AuditProductDelete         = "product.delete"
	AuditProductOptionAdd      = "product.option_add"
	AuditProductOptionsSet     = "product.options_set"
	AuditProductMediaSet       = "product.media_set"
	AuditProductCollectionsSet = "product.collections_set"
	AuditProductImport         = "product.import"

	AuditVariantCreate   = "variant.create"
	AuditVariantUpdate   = "variant.update"
	AuditVariantDelete   = "variant.delete"
	AuditVariantMediaSet = "variant.media_set"

	AuditCategoryCreate = "category.create"
	AuditCategoryUpdate = "category.update"
	AuditCategoryDelete = "category.delete"

	AuditCollectionCreate = "collection.create"
	AuditCollectionUpdate = "collection.update"
	AuditCollectionDelete = "collection.delete"
	// Its own verb rather than product.collections_set: that one says a
	// product changed which collections it is in, this one says a collection
	// changed what is in it and in what order. They write different columns
	// and answer different questions (D40).
	AuditCollectionProductsSet = "collection.products_set"

	// The shared field dictionary. A category asking for a field it no longer
	// defines falls back to free text, so a delete here degrades screens
	// quietly — which is exactly why it is recorded.
	AuditTaxonomyAttributeCreate = "taxonomy_attribute.create"
	AuditTaxonomyAttributeUpdate = "taxonomy_attribute.update"
	AuditTaxonomyAttributeDelete = "taxonomy_attribute.delete"

	AuditDiscountCreate = "discount.create"
	AuditDiscountUpdate = "discount.update"
	AuditDiscountDelete = "discount.delete"

	// AuditPluginUpdate is a plugin switched, or its settings changed.
	AuditPluginUpdate = "plugin.update"
	// AuditNotificationTemplateUpdate is a message's wording edited, or its
	// default restored.
	AuditNotificationTemplateUpdate = "notification_template.update"

	AuditTaxRateCreate = "tax_rate.create"
	AuditTaxRateUpdate = "tax_rate.update"
	AuditTaxRateDelete = "tax_rate.delete"

	AuditLocationCreate     = "location.create"
	AuditLocationUpdate     = "location.update"
	AuditLocationDelete     = "location.delete"
	AuditLocationSetDefault = "location.set_default"

	AuditSuperuserCreate         = "superuser.create"
	AuditSuperuserUpdate         = "superuser.update"
	AuditSuperuserSelfUpdate     = "superuser.self_update"
	AuditSuperuserDelete         = "superuser.delete"
	AuditSuperuserSetRole        = "superuser.set_role"
	AuditSuperuserRevokeSessions = "superuser.revoke_sessions"

	AuditInvitationCreate = "invitation.create"
	AuditInvitationRevoke = "invitation.revoke"
	AuditInvitationAccept = "invitation.accept"

	AuditRoleSet   = "role.set"
	AuditRoleReset = "role.reset"

	AuditOrderImport = "order.import"
)

// AllAuditActions is the catalogue, in the panel's filter order. Like
// AuditEntityTypes it exists because the database will not police the
// vocabulary; a test walks it and fails on a constant nothing emits.
var AllAuditActions = []string{
	AuditOrderCreate, AuditOrderCancel, AuditOrderEditLines, AuditOrderUpdate,
	AuditOrderNote,
	AuditOrderMarkPaid, AuditOrderMarkUnpaid, AuditOrderMarkFailed,
	AuditOrderRefund, AuditOrderRefundSettle,
	AuditOrderDeliver, AuditOrderUndeliver,
	AuditOrderShip, AuditOrderShipmentUpdate, AuditOrderShipmentDelete,
	AuditOrderReturn, AuditOrderReturnWithdraw,
	AuditOrderTokenReveal,

	AuditProductCreate, AuditProductUpdate, AuditProductDelete,
	AuditProductOptionAdd, AuditProductOptionsSet, AuditProductMediaSet,
	AuditProductCollectionsSet, AuditProductImport,

	AuditVariantCreate, AuditVariantUpdate, AuditVariantDelete, AuditVariantMediaSet,

	AuditCategoryCreate, AuditCategoryUpdate, AuditCategoryDelete,

	AuditCollectionCreate, AuditCollectionUpdate, AuditCollectionDelete,
	AuditCollectionProductsSet,

	AuditTaxonomyAttributeCreate, AuditTaxonomyAttributeUpdate,
	AuditTaxonomyAttributeDelete,

	AuditDiscountCreate, AuditDiscountUpdate, AuditDiscountDelete,

	AuditTaxRateCreate, AuditTaxRateUpdate, AuditTaxRateDelete,

	AuditLocationCreate, AuditLocationUpdate, AuditLocationDelete, AuditLocationSetDefault,

	AuditSuperuserCreate, AuditSuperuserUpdate, AuditSuperuserSelfUpdate,
	AuditSuperuserDelete, AuditSuperuserSetRole, AuditSuperuserRevokeSessions,

	AuditInvitationCreate, AuditInvitationRevoke, AuditInvitationAccept,

	AuditRoleSet, AuditRoleReset,

	AuditOrderImport,

	AuditPluginUpdate, AuditNotificationTemplateUpdate,
}

// AuditChanges is what a row says actually moved.
//
// Two maps rather than a []{field, from, to}: `omitempty` on an `any` drops a
// genuine 0 or "", so a price going 0 -> 500 would silently lose its from.
// omitempty on the maps themselves is safe, because a nil map is genuinely
// absent — and an absent Before means the previous value was not recorded,
// never that it was empty. A reader must render that as "set X to Y".
type AuditChanges struct {
	Before map[string]any `json:"before,omitempty"`
	After  map[string]any `json:"after,omitempty"`
	// Event names the outbox event the same transaction published, or "" where
	// the act publishes none. It is the join a merged order timeline uses to
	// avoid rendering one fact twice.
	Event string `json:"event,omitempty"`
}

// AuditEntry is one recorded action.
type AuditEntry struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	ActorKind string    `json:"actor_kind"`
	// ActorID is null for a token and for the system, and it may point at a
	// superuser that no longer exists — see M20.
	ActorID     *int64       `json:"actor_id"`
	ActorEmail  string       `json:"actor_email,omitempty"`
	ActorRole   string       `json:"actor_role,omitempty"`
	ActorLabel  string       `json:"actor_label,omitempty"`
	Action      string       `json:"action"`
	EntityType  string       `json:"entity_type"`
	EntityID    string       `json:"entity_id"`
	EntityLabel string       `json:"entity_label,omitempty"`
	Summary     string       `json:"summary"`
	Changes     AuditChanges `json:"changes"`
}

// AuditActor is one line of the feed's Who filter: somebody who appears in the
// trail, not somebody who exists in the team table.
type AuditActor struct {
	Kind   string    `json:"kind"`
	ID     *int64    `json:"id,omitempty"`
	Email  string    `json:"email,omitempty"`
	Role   string    `json:"role,omitempty"`
	Label  string    `json:"label,omitempty"`
	Acts   int       `json:"acts"`
	LastAt time.Time `json:"last_at"`
}

// AuditQuery filters the cross-store feed. Every field is optional; a zero
// field is not a filter.
type AuditQuery struct {
	ActorID    *int64
	ActorKind  string
	Action     string
	EntityType string
	EntityID   string
	// From is inclusive and To is exclusive, as everywhere else in the engine.
	From, To      *time.Time
	Limit, Offset int
}

// Audits reads the trail. There is deliberately no Delete and no Update: the
// table is append-only because nothing in the package can do anything else to
// it, not because a handler refuses.
type Audits struct {
	app *App
}

// Audit returns the audit-trail reader.
func (a *App) Audit() *Audits { return a.audit }

// ------------------------------------------------------------------- writing

// auditRecord is what a service says about the change it just made.
type auditRecord struct {
	Action string
	Entity string
	// ID names a numbered record; Key names one that is not numbered (a role
	// is "manager") and wins over ID when it is set.
	ID      int64
	Key     string
	Label   string
	Summary string
	// Event names the outbox event written in this same transaction, if any.
	Event         string
	Before, After map[string]any
}

// readableValues rewrites the []byte columns a call site naturally has to hand
// — an encoded address, a metadata blob, a tags array — as the JSON they
// already are.
//
// Without this, encoding/json renders a []byte as base64, and the one field an
// operator most wants to read ("what did the address change to") arrives as a
// wall of letters. Anything that is not valid JSON is left alone rather than
// guessed at: base64 of something unexpected is at least honest.
func readableValues(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if b, ok := v.([]byte); ok && json.Valid(b) {
			out[k] = json.RawMessage(b)
			continue
		}
		out[k] = v
	}
	return out
}

// withField adds one key to a changes map, allocating when the caller had none.
// It exists so a call site can fill Before and After lazily without opening
// every one of them with a nil check.
func withField(m map[string]any, key string, v any) map[string]any {
	if m == nil {
		m = map[string]any{}
	}
	m[key] = v
	return m
}

func (r auditRecord) entityID() string {
	if r.Key != "" {
		return r.Key
	}
	return strconv.FormatInt(r.ID, 10)
}

// writeAudit appends one row inside the caller's transaction. It is the only
// path into admin_audit.
//
// It takes a *sql.Tx and no service handle for outbox.write's reason: there is
// no correct way to call it without a transaction, and a signature that cannot
// express the wrong call is better than a comment asking for the right one. It
// is a free function rather than a method so that Superusers — which holds a
// bare *sql.DB and no *App — can call it too.
//
// A returned error rolls the caller's transaction back, deliberately. The table
// has no CHECK, no foreign key and no unique index beyond its primary key, so
// nothing but a genuine database failure can refuse this row, and a database
// that refuses it is one the business write was about to fail against anyway.
// The alternative — a SAVEPOINT so the audit can be discarded — costs two round
// trips on every audited write and hides a real defect behind a log line.
func writeAudit(ctx context.Context, tx *sql.Tx, rec auditRecord) error {
	if rec.Action == "" || rec.Entity == "" {
		// A caller's mistake, and it surfaces the first time the path is
		// exercised rather than as an unreadable row somebody finds in a year.
		return fmt.Errorf("audit: a record with no action or entity")
	}

	kind, id, email, role, label := auditActor(ctx)

	data, err := json.Marshal(AuditChanges{
		Before: readableValues(rec.Before),
		After:  readableValues(rec.After),
		Event:  rec.Event,
	})
	if err != nil {
		// An unencodable payload is our bug in the record, and a defect in the
		// record must not cost a sale. There is no logger reachable from here;
		// the calling service logs it if it wants to.
		data = []byte(`{"error":"payload could not be encoded"}`)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO admin_audit (actor_kind, actor_id, actor_email, actor_role, actor_label,
		                         action, entity_type, entity_id, entity_label, summary, changes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		kind, id, email, role, label,
		rec.Action, rec.Entity, rec.entityID(), rec.Label, rec.Summary, data)
	if err != nil {
		return fmt.Errorf("write audit record %s: %w", rec.Action, err)
	}
	return nil
}

// ------------------------------------------------------------------- the actor

// WithActorLabel names a caller that is not a person: a sweeper, a cron job, a
// module naming itself.
//
// It never changes the actor kind. How a request authenticated is the
// middleware's answer and not the caller's, so a label on an operator's context
// decorates the row rather than disguising it. It is what stops two hundred
// cancellations by SweepUnpaid at 3am reading as somebody's night's work.
func WithActorLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, ctxKeyActorLabel, label)
}

func actorLabelFrom(ctx context.Context) string {
	label, _ := ctx.Value(ctxKeyActorLabel).(string)
	return label
}

// auditActor reads who is acting off the request context.
//
// It is the one actor seam in the engine: everything that wants to attribute a
// write — the audit trail, and anything later that records an operator — calls
// this rather than reading SuperuserFrom itself, so there is one place where
// "nobody signed in" is turned into an answer.
//
// The context is the seam on purpose. No service signature grows an actor
// parameter, which is what keeps attribution out of forty call sites and out of
// every module that calls a core service.
func auditActor(ctx context.Context) (kind string, id *int64, email, role, label string) {
	label = actorLabelFrom(ctx)
	if su := SuperuserFrom(ctx); su != nil {
		actorID := su.ID
		return ActorOperator, &actorID, su.Email, su.Role, label
	}
	if adminTokenFrom(ctx) {
		return ActorToken, nil, "", "", label
	}
	return ActorSystem, nil, "", "", label
}

// ------------------------------------------------------------------- reading

// List is the cross-store feed, newest first.
//
// Ordered by id rather than by created_at: created_at is now(), which is
// transaction_timestamp(), so two rows can share an instant and the id is the
// tie-break. It is also the primary key, so the unfiltered read walks an index
// it already has.
func (s *Audits) List(ctx context.Context, q AuditQuery) ([]*AuditEntry, int, error) {
	where := []string{}
	args := []any{}
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if q.ActorID != nil {
		add("actor_id = $%d", *q.ActorID)
	}
	if q.ActorKind != "" {
		add("actor_kind = $%d", q.ActorKind)
	}
	if q.Action != "" {
		add("action = $%d", q.Action)
	}
	if q.EntityType != "" {
		add("entity_type = $%d", q.EntityType)
	}
	if q.EntityID != "" {
		add("entity_id = $%d", q.EntityID)
	}
	if q.From != nil {
		add("created_at >= $%d", *q.From)
	}
	if q.To != nil {
		add("created_at < $%d", *q.To)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT count(*) FROM admin_audit`+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit, offset := q.Limit, q.Offset
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := s.app.db.QueryContext(ctx, auditSelect+clause+
		fmt.Sprintf(" ORDER BY id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2),
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	entries, err := scanAuditRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// OfEntity is one record's own history: the admin_audit_entity_idx lookup, and
// the only query on this table that runs on a screen an operator opens all day.
//
// A record with no rows is an empty list and not an error. The log knowing
// nothing about a record is not the record being absent, and every record that
// existed before this migration ran has no history.
func (s *Audits) OfEntity(ctx context.Context, entityType, entityID string, limit, offset int) ([]*AuditEntry, int, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	var total int
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT count(*) FROM admin_audit WHERE entity_type = $1 AND entity_id = $2`,
		entityType, entityID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.app.db.QueryContext(ctx, auditSelect+
		` WHERE entity_type = $1 AND entity_id = $2 ORDER BY id DESC LIMIT $3 OFFSET $4`,
		entityType, entityID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	entries, err := scanAuditRows(rows)
	if err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// Actors lists everybody who appears in the trail, most recently active first.
//
// It is built from admin_audit and never from the team table, so an operator
// who holds store.operate but not team.read fills the feed's Who filter without
// being handed the store's full roster.
func (s *Audits) Actors(ctx context.Context) ([]AuditActor, error) {
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT DISTINCT ON (actor_kind, actor_id)
		       actor_kind, actor_id, actor_email, actor_role, actor_label,
		       count(*) OVER (PARTITION BY actor_kind, actor_id), created_at
		FROM admin_audit
		ORDER BY actor_kind, actor_id, id DESC
		LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actors []AuditActor
	for rows.Next() {
		var a AuditActor
		var email, role, label sql.NullString
		if err := rows.Scan(&a.Kind, &a.ID, &email, &role, &label, &a.Acts, &a.LastAt); err != nil {
			return nil, err
		}
		a.Email, a.Role, a.Label = email.String, role.String, label.String
		actors = append(actors, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// DISTINCT ON dictates its own ORDER BY, so the useful order — who acted
	// most recently — is applied here rather than fought for in SQL.
	for i := 1; i < len(actors); i++ {
		for j := i; j > 0 && actors[j].LastAt.After(actors[j-1].LastAt); j-- {
			actors[j], actors[j-1] = actors[j-1], actors[j]
		}
	}
	return actors, nil
}

const auditSelect = `
	SELECT id, created_at, actor_kind, actor_id, actor_email, actor_role, actor_label,
	       action, entity_type, entity_id, entity_label, summary, changes
	FROM admin_audit`

func scanAuditRows(rows *sql.Rows) ([]*AuditEntry, error) {
	defer rows.Close()
	entries := []*AuditEntry{}
	for rows.Next() {
		e := &AuditEntry{}
		var raw []byte
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.ActorKind, &e.ActorID, &e.ActorEmail,
			&e.ActorRole, &e.ActorLabel, &e.Action, &e.EntityType, &e.EntityID,
			&e.EntityLabel, &e.Summary, &raw); err != nil {
			return nil, err
		}
		changes, err := scanAuditChanges(raw)
		if err != nil {
			return nil, err
		}
		e.Changes = changes
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func scanAuditChanges(raw []byte) (AuditChanges, error) {
	var c AuditChanges
	if len(raw) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	return c, nil
}
