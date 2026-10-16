package alltest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	msg91 "github.com/itswadesh/gocommerce/ext/notify-msg91"
	twilio "github.com/itswadesh/gocommerce/ext/notify-twilio"
	"github.com/itswadesh/gocommerce/gctest"
)

// recipients collects who a stub provider was asked to text.
type recipients struct {
	mu sync.Mutex
	to []string
}

func (r *recipients) add(to string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.to = append(r.to, to)
}

func (r *recipients) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.to...)
}

// A store with both SMS providers texts each customer once: MSG91 for an
// Indian number, Twilio for the rest (the owner's rule).
func TestIndianNumbersGoToMSG91AndTheRestToTwilio(t *testing.T) {
	india, world := &recipients{}, &recipients{}
	msgServer := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Recipients []map[string]string `json:"recipients"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		for _, rc := range body.Recipients {
			india.add(rc["mobiles"])
		}
		_, _ = io.WriteString(w, `{"type":"success"}`)
	})
	twilioServer := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(b))
		world.add(form.Get("To"))
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"sid":"SM1","status":"queued"}`)
	})
	app := gctest.New(t,
		twilio.New(twilio.Config{AccountSID: "AC1", AuthToken: "t", From: "+15550000000", BaseURL: twilioServer.URL}),
		msg91.New(msg91.Config{AuthKey: "k", BaseURL: msgServer.URL,
			Templates: map[string]string{gocommerce.EventOrderShipped: "tpl-shipped"}}),
	)
	ctx := context.Background()
	for _, to := range []string{"+91 98765 43210", "+1 415 555 0100"} {
		if err := app.Notify(ctx, gocommerce.Notification{
			Event: gocommerce.EventOrderShipped, Channel: gocommerce.ChannelSMS, To: to,
			Data: map[string]string{"order_number": "GC-1"},
		}); err != nil {
			t.Fatalf("notify %s: %v", to, err)
		}
	}
	if got := india.all(); len(got) != 1 || !strings.HasSuffix(digitsOf(got[0]), "9876543210") {
		t.Fatalf("MSG91 texted %v, want only the Indian number", got)
	}
	if got := world.all(); len(got) != 1 || !strings.HasSuffix(digitsOf(got[0]), "4155550100") {
		t.Fatalf("Twilio texted %v, want only the US number", got)
	}
}

func digitsOf(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}
