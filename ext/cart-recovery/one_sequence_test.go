package cartrecovery

import "testing"

// One shopper, one sequence. A shopper who left three baskets — a phone, a
// laptop, a visit after signing in — is written to about the newest only, so
// each reminder arrives once rather than three times in the same minute. The
// older baskets stay on the screen, held, with the reason.
func TestAShopperWithSeveralBasketsIsRemindedOnce(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	_, oldest, _ := h.basket("ONE-1", "asha@example.com", 1)
	_, older, _ := h.basket("ONE-2", "Asha@Example.com", 1)
	_, newest, _ := h.basket("ONE-3", "asha@example.com", 1)
	_, someoneElse, _ := h.basket("ONE-4", "ravi@example.com", 1)
	h.idle(oldest, 50)
	h.idle(older, 40)
	h.idle(newest, 20)
	h.idle(someoneElse, 45)

	if res := h.pass(); res.Recorded != 4 {
		t.Fatalf("recorded = %d, want 4", res.Recorded)
	}
	for _, c := range []int64{oldest, older, newest, someoneElse} {
		h.due(c)
	}
	h.pass()

	sent := h.rec.All()
	if len(sent) != 2 {
		t.Fatalf("sent %d messages, want one to each shopper: %+v", len(sent), sent)
	}
	for _, c := range []int64{oldest, older} {
		if a := h.record(c); a.StepsSent != 0 || a.NextStepAt != nil || a.HoldReason != holdNewerBasket {
			t.Errorf("an older basket: steps %d next %v hold %q, want held for the newer basket", a.StepsSent, a.NextStepAt, a.HoldReason)
		}
	}
	if a := h.record(newest); a.StepsSent != 1 {
		t.Errorf("the newest basket sent %d steps, want 1", a.StepsSent)
	}
	if a := h.record(someoneElse); a.StepsSent != 1 {
		t.Errorf("another shopper's basket sent %d steps, want 1", a.StepsSent)
	}

	// The next step goes once too.
	h.due(newest)
	h.pass()
	if n := h.rec.Count(templateSecond); n != 1 {
		t.Errorf("second reminders = %d, want 1", n)
	}
}
