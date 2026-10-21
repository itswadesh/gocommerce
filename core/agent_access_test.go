package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

// What an agent key does once it is in: who the audit trail says acted.

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
