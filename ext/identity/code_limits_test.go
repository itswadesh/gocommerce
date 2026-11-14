package identity

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
	"github.com/misiki-in/gocommerce/gctest"
)

// A right code sent many times at once opens one session. now() is when a
// transaction began, so spending a code by setting expires_at = now() left it
// open to any request that began earlier and was queued on the row.
func TestARightCodeSentManyTimesAtOnceWorksOnce(t *testing.T) {
	app, mod, sms := phoneApp(t)
	for round := range 15 {
		phone := fmt.Sprintf("+9198765432%02d", round)
		code := phoneCode(t, app, mod, sms, phone)
		var mu sync.Mutex
		wins := 0
		var wg sync.WaitGroup
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp/verify",
					map[string]any{"phone": phone, "code": code})
				if rec.Code == http.StatusOK {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if wins != 1 {
			t.Fatalf("round %d: one code signed in %d times", round, wins)
		}
	}
}

// Many numbers cannot run up a store's SMS bill: past the day's limit nobody
// more is texted.
func TestAStoreStopsTextingCodesAtItsDailyLimit(t *testing.T) {
	sms := &capture{}
	mod := New(Config{Notifier: sms})
	app := gctest.NewWithConfig(t, gocommerce.Config{SMSCodesPerDay: 3}, mod)
	signInBy(t, app, LoginPhoneOTP)
	for i := range 3 {
		phoneCode(t, app, mod, sms, fmt.Sprintf("+9198765432%02d", i))
	}
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp", map[string]any{"phone": "+919876543299"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("past the limit = %d %s, want 429", rec.Code, rec.Body)
	}
	mod.waitForSends(5 * time.Second)
	if got := sms.count(); got != 3 {
		t.Fatalf("%d codes texted, want 3", got)
	}
}
