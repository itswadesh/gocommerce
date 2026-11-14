package retailassistant

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

// staffWithPhone is somebody in the default store who proved senderA with a code.
func staffWithPhone(t *testing.T, h *harness, role string) int64 {
	t.Helper()
	ctx := h.ctxOf("")
	su, err := h.app.Superusers().Create(ctx, "person@example.com", "a long enough password", role)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.Superusers().ChangePhone(ctx, "person@example.com", "+"+senderA); err != nil {
		t.Fatal(err)
	}
	return su.ID
}

func TestAnyMessageFromAnOwnersOwnPhoneLinksIt(t *testing.T) {
	h := setup(t)
	staffWithPhone(t, h, gocommerce.RoleOwner)

	h.deliver(text("hi-1", senderA, "hello"))
	if got := h.meta.waitFor(t, senderA).Body; !strings.Contains(got, "now linked") {
		t.Fatalf("reply = %q", got)
	}
	var links []PhoneLink
	h.admin(http.MethodGet, path("/phone-links", ""), nil, &links, http.StatusOK)
	if len(links) != 1 || links[0].Phone != senderA || links[0].VerifiedAt == nil {
		t.Fatalf("links = %+v", links)
	}
	// Meta delivers again sometimes: one link, one reply, and "hello" is not a document.
	before := len(h.meta.sent)
	h.deliver(text("hi-1", senderA, "hello"))
	if len(h.meta.sent) != before || h.count("", "retailassistant_documents") != 0 {
		t.Fatalf("a redelivery did something: %d messages, %d documents", len(h.meta.sent)-before, h.count("", "retailassistant_documents"))
	}
	if h.count("", "retailassistant_audit") == 0 {
		t.Fatal("linking wrote no audit line")
	}
}

func TestABillAsTheFirstMessageIsKept(t *testing.T) {
	h := setup(t)
	staffWithPhone(t, h, gocommerce.RoleOwner)
	bill := h.file("b-1", senderA, "m-b", "b.csv", csvInvoice("B-1", 100))
	h.deliver(bill)
	if h.count("", "retailassistant_documents") != 1 {
		t.Fatal("the first bill was dropped")
	}
	h.deliver(bill)
	if h.count("", "retailassistant_documents") != 1 {
		t.Fatal("a redelivered bill was kept twice")
	}
}

func TestAPendingCodeIsNotNeededForAnOwnersOwnPhone(t *testing.T) {
	h := setup(t)
	staffWithPhone(t, h, gocommerce.RoleOwner)
	h.admin(http.MethodPost, path("/phone-links", ""), map[string]string{"phone": "+" + senderA}, nil, http.StatusCreated)
	h.deliver(text("p-1", senderA, "Hi"))
	var links []PhoneLink
	h.admin(http.MethodGet, path("/phone-links", ""), nil, &links, http.StatusOK)
	if len(links) != 1 || links[0].VerifiedAt == nil {
		t.Fatalf("links = %+v", links)
	}
}

// The other store is neither joined nor told about: only where this person works.
func TestAPhoneLinksOnlyToTheStoreItsOwnerWorksIn(t *testing.T) {
	h := setup(t)
	staffWithPhone(t, h, gocommerce.RoleOwner)
	h.deliver(h.file("o-1", senderA, "m-o", "o.csv", csvInvoice("O-1", 100)))
	if h.count("", "retailassistant_documents") != 1 || h.count(h.second, "retailassistant_documents") != 0 ||
		h.count(h.second, "retailassistant_phone_links") != 0 {
		t.Fatal("the phone reached a store its owner does not work in")
	}
}

func TestAnOwnerOfTwoStoresGetsThePrimaryUntilChoosingAnother(t *testing.T) {
	h := setup(t)
	id := staffWithPhone(t, h, gocommerce.RoleOwner)
	st, err := h.app.Stores().BySlug(h.ctxOf(""), h.second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.DB().ExecContext(h.ctxOf(h.second),
		`INSERT INTO store_members (store_id, superuser_id, role) VALUES ($1, $2, $3)`, st.ID, id, gocommerce.RoleOwner); err != nil {
		t.Fatal(err)
	}
	h.deliver(h.file("t-1", senderA, "m-t", "t.csv", csvInvoice("T-1", 100)))
	if h.count("", "retailassistant_documents") != 1 || h.count(h.second, "retailassistant_documents") != 0 {
		t.Fatal("the bill did not go to the older store")
	}
	if got := h.meta.last(senderA).Body; !strings.Contains(got, h.second) || !strings.Contains(got, "STORE") {
		t.Fatalf("it was not told how to use the other store: %q", got)
	}
	// Told once: a second bill goes the same way with nothing more said about stores.
	h.deliver(h.file("t-1b", senderA, "m-t2", "t2.csv", csvInvoice("T-2", 100)))
	if h.count("", "retailassistant_documents") != 2 {
		t.Fatal("the second bill did not follow the first")
	}
	h.meta.mu.Lock()
	told := 0
	for _, s := range h.meta.sent {
		if strings.Contains(s.Body, "several stores") {
			told++
		}
	}
	h.meta.mu.Unlock()
	if told != 1 {
		t.Fatalf("told about the primary store %d times", told)
	}
	h.deliver(text("t-2", senderA, "STORE "+h.second))
	h.deliver(h.file("t-3", senderA, "m-t", "t.csv", csvInvoice("T-1", 100)))
	if h.count(h.second, "retailassistant_documents") != 1 {
		t.Fatal("the bill did not go to the chosen store")
	}
}

func TestAPhoneStaysOutWithoutTheRightProof(t *testing.T) {
	refused := func(t *testing.T, h *harness) {
		t.Helper()
		h.deliver(text("no-1", senderA, "Hi"))
		h.deliver(h.file("no-2", senderA, "m-n", "n.csv", csvInvoice("N-1", 100)))
		if n := h.count("", "retailassistant_documents"); n != 0 {
			t.Fatalf("%d documents were kept", n)
		}
		var verified int
		if err := h.app.DB().QueryRowContext(h.ctxOf(""),
			`SELECT count(*) FROM retailassistant_phone_links WHERE verified_at IS NOT NULL AND revoked_at IS NULL`).Scan(&verified); err != nil {
			t.Fatal(err)
		}
		if verified != 0 {
			t.Fatal("the phone was linked")
		}
	}
	t.Run("a stranger", func(t *testing.T) {
		refused(t, setup(t))
	})
	t.Run("a phone nobody proved", func(t *testing.T) {
		h := setup(t)
		staffWithPhone(t, h, gocommerce.RoleOwner)
		if _, err := h.app.DB().ExecContext(h.ctxOf(""), `UPDATE superusers SET phone_proved_at = NULL`); err != nil {
			t.Fatal(err)
		}
		refused(t, h)
	})
	t.Run("an account shut out", func(t *testing.T) {
		h := setup(t)
		staffWithPhone(t, h, gocommerce.RoleOwner)
		if err := h.app.Superusers().SetDisabled(h.ctxOf(""), "person@example.com", true); err != nil {
			t.Fatal(err)
		}
		refused(t, h)
	})
	t.Run("a role that cannot link numbers", func(t *testing.T) {
		h := setup(t)
		staffWithPhone(t, h, gocommerce.RoleManager)
		refused(t, h)
	})
	t.Run("a phone the store removed", func(t *testing.T) {
		h := setup(t)
		staffWithPhone(t, h, gocommerce.RoleOwner)
		h.deliver(text("r-1", senderA, "Hi"))
		var links []PhoneLink
		h.admin(http.MethodGet, path("/phone-links", ""), nil, &links, http.StatusOK)
		h.admin(http.MethodDelete, path(fmt.Sprintf("/phone-links/%d", links[0].ID), ""), nil, nil, http.StatusOK)
		refused(t, h)
	})
}

func TestStopFromAnUnlinkedPhoneLinksNothing(t *testing.T) {
	h := setup(t)
	staffWithPhone(t, h, gocommerce.RoleOwner)
	h.deliver(text("s-1", senderA, "STOP"))
	if n := h.count("", "retailassistant_phone_links"); n != 0 {
		t.Fatalf("STOP made %d links", n)
	}
}
