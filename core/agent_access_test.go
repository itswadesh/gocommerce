package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// What an agent key does once it is in: who the audit trail says acted, what
// it is shown, and where it may not go.

// --------------------------------------------------------------- the audit seam

// The trail names the key or agent that acted, by its own id, rather than the
// operator 0 every key used to share; an agent's row carries its run label.
// Operators, the static token and the system are unchanged (audit_test.go).
func TestTheAuditTrailNamesTheKeyOrAgentThatActed(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)

	till, tillSecret, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "Till", Role: RoleManager}, owner)
	if err != nil {
		t.Fatal(err)
	}
	order := paidOrder(t, app, "AGENT-AUD-1", 1500, 3)
	rec := doBody(t, app, http.MethodPost, "/api/admin/orders/"+strconv.FormatInt(order.ID, 10)+"/cancel",
		`{"reason":"by the till"}`, bearer(tillSecret))
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel with an integration key = %d: %s", rec.Code, rec.Body)
	}
	row := onlyRow(t, auditRows(t, app, "action = $1 AND entity_id = $2",
		AuditOrderCancel, strconv.FormatInt(order.ID, 10)))
	if row.ActorKind != ActorKey || row.ActorID == nil || *row.ActorID != till.ID {
		t.Errorf("integration key row = kind %q id %v, want key %d", row.ActorKind, row.ActorID, till.ID)
	}
	if row.ActorEmail != "api key: Till" || row.ActorRole != RoleManager {
		t.Errorf("integration key row = %q/%q, want api key: Till/manager", row.ActorEmail, row.ActorRole)
	}

	// An agent cannot cancel over HTTP in this release; the seam is the
	// context, which is what a module writing a staff-only record as the agent
	// will use.
	desk, deskSecret := newAgentKey(t, app, owner, "Order desk", RightOrdersRead)
	agent, ok := app.APIKeys().Resolve(ctx, deskSecret)
	if !ok {
		t.Fatal("agent key does not resolve")
	}
	second := paidOrder(t, app, "AGENT-AUD-2", 1500, 3)
	agentCtx := WithActorLabel(WithSuperuser(ctx, agent), "run 42")
	if _, err := app.Order().Cancel(agentCtx, second.ID, "by the agent"); err != nil {
		t.Fatal(err)
	}
	row = onlyRow(t, auditRows(t, app, "action = $1 AND entity_id = $2",
		AuditOrderCancel, strconv.FormatInt(second.ID, 10)))
	if row.ActorKind != ActorAgent || row.ActorID == nil || *row.ActorID != desk.ID {
		t.Errorf("agent row = kind %q id %v, want agent %d", row.ActorKind, row.ActorID, desk.ID)
	}
	if row.ActorEmail != "agent key: Order desk" || row.ActorLabel != "run 42" || row.ActorRole != KeyKindAgent {
		t.Errorf("agent row = %q/%q/%q, want the key's name, the run label and the role agent",
			row.ActorEmail, row.ActorLabel, row.ActorRole)
	}

	// The Who filter tells the two keys apart, and from the operators.
	token := signInAs(t, app, "auditor@example.com", RoleOwner)
	rec = do(t, app, http.MethodGet, "/api/admin/audit/actors", bearer(token))
	if rec.Code != http.StatusOK {
		t.Fatalf("actors = %d: %s", rec.Code, rec.Body)
	}
	var actors struct {
		Data []AuditActor `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &actors); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	for _, a := range actors.Data {
		if a.ID != nil {
			seen[a.Kind] = *a.ID
		}
	}
	if seen[ActorKey] != till.ID || seen[ActorAgent] != desk.ID {
		t.Errorf("actors = %+v, want the key %d and the agent %d by kind", seen, till.ID, desk.ID)
	}
	rec = do(t, app, http.MethodGet, "/api/admin/audit?actor_kind=agent&actor_id="+
		strconv.FormatInt(desk.ID, 10), bearer(token))
	var feed struct {
		Data []AuditEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Data) != 1 || feed.Data[0].ActorKind != ActorAgent {
		t.Errorf("the agent's feed = %+v, want its one row", feed.Data)
	}
}

// A key's name is not personal data, so the mask that hides an operator's
// address leaves "Key · Till" readable on a demo store.
func TestAMaskLeavesAKeysNameReadable(t *testing.T) {
	app := newTestApp(t)
	ctx := withStoreDemo(context.Background(), true)
	keyID, opID := int64(3), int64(4)
	entries := []*AuditEntry{
		{ActorKind: ActorKey, ActorID: &keyID, ActorEmail: "api key: Till"},
		{ActorKind: ActorAgent, ActorID: &keyID, ActorEmail: "agent key: Order desk"},
		{ActorKind: ActorOperator, ActorID: &opID, ActorEmail: "jane.doe@gmail.com"},
	}
	app.maskAuditEntries(ctx, entries)
	if entries[0].ActorEmail != "api key: Till" || entries[1].ActorEmail != "agent key: Order desk" {
		t.Errorf("key names masked: %q, %q", entries[0].ActorEmail, entries[1].ActorEmail)
	}
	if entries[2].ActorEmail == "jane.doe@gmail.com" {
		t.Error("an operator's address was not masked on a demo store")
	}
}

// ------------------------------------------------------------------- masking

// An agent sees personal data masked exactly as a demo store masks it, on
// every store, while an integration key with the same rights sees it in full.
func TestAnAgentSeesContactDetailsMasked(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)

	product := simpleProduct(t, app, "AGENT-MASK", 1200, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	in := checkoutInput(cart.Token)
	in.Email, in.Phone, in.Name = "jane.doe@gmail.com", "+31 20 555 1242", "Jane Doe"
	result, err := app.Order().Checkout(ctx, CodeCOD, in, "")
	if err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(result.Order.ID, 10)

	_, staffKey, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "Staff script", Role: RoleStaff}, owner)
	if err != nil {
		t.Fatal(err)
	}
	_, agentKey := newAgentKey(t, app, owner, "Order desk", RightOrdersRead, RightCustomersRead)

	for _, path := range []string{"/api/admin/orders/" + id, "/api/admin/orders", "/api/admin/customers"} {
		agentBody := do(t, app, http.MethodGet, path, bearer(agentKey)).Body.String()
		if strings.Contains(agentBody, "jane.doe") || strings.Contains(agentBody, "555 1242") {
			t.Errorf("%s: the agent read contact details in full: %s", path, agentBody)
		}
		if !strings.Contains(agentBody, "j•••@g•••.com") {
			t.Errorf("%s: the agent's answer has no masked address: %s", path, agentBody)
		}
		staffBody := do(t, app, http.MethodGet, path, bearer(staffKey)).Body.String()
		if !strings.Contains(staffBody, "jane.doe@gmail.com") {
			t.Errorf("%s: an integration key with the same rights was masked: %s", path, staffBody)
		}
	}

	// ?q= cannot read the address back a character at a time; the name still
	// finds the order, so the search box still works.
	total := func(path, key string) int {
		t.Helper()
		rec := do(t, app, http.MethodGet, path, bearer(key))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, rec.Code, rec.Body)
		}
		var out struct {
			Meta ListMeta `json:"meta"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Meta.Total
	}
	for _, path := range []string{"/api/admin/orders?q=jane.doe", "/api/admin/customers?q=jane.doe"} {
		if n := total(path, agentKey); n != 0 {
			t.Errorf("%s as an agent matched %d, want 0: the email is masked", path, n)
		}
		if n := total(path, staffKey); n != 1 {
			t.Errorf("%s as an integration key matched %d, want 1", path, n)
		}
	}
	if n := total("/api/admin/orders?q=Jane", agentKey); n != 1 {
		t.Errorf("an agent searching by name matched %d, want 1", n)
	}

	// The notification log masks the recipient and will not search it.
	if _, err := app.DB().ExecContext(ctx, `
		INSERT INTO notifications (event, channel, recipient, order_number, status)
		VALUES ('order.placed', 'email', 'jane.doe@gmail.com', $1, 'logged')`, result.Order.Number); err != nil {
		t.Fatal(err)
	}
	if n := total("/api/admin/notifications?q=jane.doe", agentKey); n != 0 {
		t.Errorf("an agent searching the notification log by address matched %d, want 0", n)
	}
	if n := total("/api/admin/notifications?q=jane.doe", staffKey); n != 1 {
		t.Errorf("an integration key searching the notification log matched %d, want 1", n)
	}
}

// The exported predicate modules ask, beside the one that still means what its
// name says.
func TestMasksIsADemoOrAnAgent(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if app.Masks(ctx) || app.Demo(ctx) {
		t.Fatal("a live store with nobody signed in masks")
	}
	agent := WithSuperuser(ctx, &Superuser{KeyID: 7, KeyKind: KeyKindAgent})
	if !app.Masks(agent) {
		t.Error("an agent is not masked")
	}
	if app.Demo(agent) {
		t.Error("an agent made the store a demo")
	}
	integration := WithSuperuser(ctx, &Superuser{KeyID: 8, KeyKind: KeyKindIntegration})
	if app.Masks(integration) {
		t.Error("an integration key is masked on a live store")
	}
	if !app.Masks(withStoreDemo(ctx, true)) {
		t.Error("a demo store does not mask")
	}
}

// The routes a demo closes for every method — a credential, or a read with no
// shape for a mask — are closed to an agent on every store.
func TestAnAgentCannotReachAroundTheMask(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)
	_, agent := newAgentKey(t, app, owner, "Analyst", RightReportsRead)
	_, staff, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "Staff script", Role: RoleStaff}, owner)
	if err != nil {
		t.Fatal(err)
	}

	rec := doBody(t, app, http.MethodPost, "/api/admin/reports/custom/1/run", `{}`, bearer(agent))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "agent_key_refused") {
		t.Fatalf("an agent running a saved report = %d, want 403 agent_key_refused: %s", rec.Code, rec.Body)
	}
	// The same right on an integration key reaches the route (and finds no
	// such report).
	rec = doBody(t, app, http.MethodPost, "/api/admin/reports/custom/1/run", `{}`, bearer(staff))
	if rec.Code == http.StatusForbidden {
		t.Fatalf("an integration key with reports.read was refused: %s", rec.Body)
	}
	// Reports the engine shapes itself are still the agent's to read.
	if rec := do(t, app, http.MethodGet, "/api/admin/reports/sales", bearer(agent)); rec.Code != http.StatusOK {
		t.Fatalf("an agent reading the sales report = %d: %s", rec.Code, rec.Body)
	}
}
