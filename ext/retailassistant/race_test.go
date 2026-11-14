package retailassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	gocommerce "github.com/misiki-in/gocommerce/core"
	"github.com/misiki-in/gocommerce/gctest"
)

// Two presses at once are how a ledger goes wrong while every single request
// looks fine, so these run the requests together, through the real handlers.

// together sends every request at the same moment and returns the statuses.
func (h *harness) together(paths []string, bodies []map[string]any) []int {
	h.t.Helper()
	codes := make([]int, len(paths))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range paths {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, _ := json.Marshal(bodies[i])
			r := httptest.NewRequest(http.MethodPost, paths[i], bytes.NewReader(raw))
			r.Header.Set("Authorization", "Bearer "+gctest.AdminToken)
			r.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			<-start
			h.app.Handler().ServeHTTP(rec, r)
			codes[i] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()
	return codes
}

func created(codes []int) int {
	n := 0
	for _, c := range codes {
		if c == http.StatusCreated {
			n++
		}
	}
	return n
}

func TestOnePaymentCannotBeSpentTwiceAtOnce(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	var supplier int64
	invoices := make([]int64, 6)
	for i := range invoices {
		invoices[i], supplier = h.post(fmt.Sprintf("RACE-P-%d", i), 100)
	}
	for round := range 5 {
		payment := h.pay(supplier, 2000, true)
		paths := make([]string, len(invoices))
		bodies := make([]map[string]any, len(invoices))
		for i, inv := range invoices {
			paths[i] = path(fmt.Sprintf("/payments/%d/allocate", payment), "")
			bodies[i] = map[string]any{"invoice_id": inv, "amount_minor": 2000}
		}
		if n := created(h.together(paths, bodies)); n != 1 {
			t.Fatalf("round %d: a payment of 2000 was allocated in full %d times", round, n)
		}
	}
}

func TestOneBillCannotBePaidTwiceAtOnce(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	for round := range 5 {
		invoice, supplier := h.post(fmt.Sprintf("RACE-I-%d", round), 100)
		paths := make([]string, 6)
		bodies := make([]map[string]any, 6)
		for i := range paths {
			paths[i] = path(fmt.Sprintf("/payments/%d/allocate", h.pay(supplier, 10000, true)), "")
			bodies[i] = map[string]any{"invoice_id": invoice, "amount_minor": 10000}
		}
		if n := created(h.together(paths, bodies)); n != 1 {
			t.Fatalf("round %d: a bill of 10000 was paid in full %d times", round, n)
		}
	}
}

// A payment reversed while it is being allocated must not leave an allocation
// standing against money that was taken back.
func TestAReversedPaymentLeavesNothingAllocated(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	var supplier int64
	invoices := make([]int64, 8)
	for i := range invoices {
		invoices[i], supplier = h.post(fmt.Sprintf("RACE-R-%d", i), 100)
	}
	ctx := context.Background()
	for round, inv := range invoices {
		payment := h.pay(supplier, 1000, true)
		allocate := path(fmt.Sprintf("/payments/%d/allocate", payment), "")
		reverse := path(fmt.Sprintf("/payments/%d/reverse", payment), "")
		h.together(
			[]string{allocate, reverse},
			[]map[string]any{{"invoice_id": inv, "amount_minor": 1000}, {}},
		)
		var net int64
		if err := h.app.DB().QueryRowContext(gocommerce.WithStore(ctx, h.app.DefaultStore()), `
			SELECT COALESCE(sum(amount_minor), 0) FROM retailassistant_payment_allocations WHERE payment_id = $1`, payment).Scan(&net); err != nil {
			t.Fatal(err)
		}
		if net != 0 {
			t.Fatalf("round %d: reversed payment %d still has %d allocated", round, payment, net)
		}
	}
}
