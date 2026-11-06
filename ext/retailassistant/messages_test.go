package retailassistant

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// phrases must not be able to lose a language, or take different arguments in
// different ones: fmt would print %!s(MISSING) to a shopkeeper.
func TestEveryPhraseHasBothLanguagesWithTheSameArguments(t *testing.T) {
	for key, entry := range phrases {
		en, hi := entry[langEnglish], entry[langHindi]
		if en == "" || hi == "" {
			t.Errorf("%s is missing a language", key)
		}
		if strings.Count(en, "%s") != strings.Count(hi, "%s") {
			t.Errorf("%s takes different arguments in Hindi and English", key)
		}
	}
}

func TestAnUnknownLanguageStillGetsAMessage(t *testing.T) {
	if got := phrase("ta", "started"); !strings.Contains(got, "confirmations") {
		t.Fatalf("got %q", got)
	}
}

func (h *harness) optIn() PhoneLink {
	h.t.Helper()
	var links []PhoneLink
	h.admin(http.MethodGet, path("/phone-links", ""), nil, &links, http.StatusOK)
	h.admin(http.MethodPatch, path(fmt.Sprintf("/phone-links/%d", links[0].ID), ""), map[string]bool{"notify": true}, nil, http.StatusOK)
	return links[0]
}

// leaveTheWindow makes this phone's last message older than Meta's window.
func (h *harness) leaveTheWindow() {
	h.t.Helper()
	if _, err := h.app.DB().ExecContext(h.ctxOf(""), `UPDATE retailassistant_messages SET received_at = now() - interval '2 days'`); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) text(id, from, body string) {
	h.t.Helper()
	h.deliver(map[string]any{"id": id, "from": from, "type": "text", "text": map[string]string{"body": body}})
}

func (f *fakeMeta) last(phone string) sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.sent) - 1; i >= 0; i-- {
		if f.sent[i].To == phone {
			return f.sent[i]
		}
	}
	return sentMessage{}
}

func (h *harness) reply(key string) (sent, dead bool, lastError string) {
	h.t.Helper()
	if err := h.app.DB().QueryRowContext(h.ctxOf(""), `
		SELECT sent_at IS NOT NULL, dead, last_error FROM retailassistant_replies WHERE dedupe_key LIKE $1 ORDER BY id DESC LIMIT 1`, key+"%").Scan(&sent, &dead, &lastError); err != nil {
		h.t.Fatal(err)
	}
	return
}

func TestInsideTheWindowIsFreeTextAndOutsideIsTheTemplate(t *testing.T) {
	h := setupWith(t, func(c *Config) { c.TemplateName = "retail_update" })
	h.link("", senderA)
	h.optIn()
	d := h.arrive("WIN-1")
	if got := h.meta.last(senderA); got.Template != "" || !strings.Contains(got.Body, "Got WIN-1.csv") {
		t.Fatalf("inside the window: %+v", got)
	}

	h.leaveTheWindow()
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/approve", d.ID), ""), map[string]any{"version": d.Version, "accept_mismatch": true}, nil, http.StatusOK)
	h.work("")
	got := h.meta.last(senderA)
	if got.Template != "retail_update" || got.Language != langEnglish || !strings.Contains(got.Body, "in your books") {
		t.Fatalf("outside the window: %+v", got)
	}
}

func TestOutsideTheWindowWithNoTemplateIsMarkedAndNotRetried(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	h.optIn()
	d := h.arrive("NOTPL-1")
	h.leaveTheWindow()
	before := len(h.meta.to(senderA))
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/approve", d.ID), ""), map[string]any{"version": d.Version, "accept_mismatch": true}, nil, http.StatusOK)
	h.work("")
	if got := len(h.meta.to(senderA)); got != before {
		t.Fatalf("%d messages were sent, and Meta would refuse them", got-before)
	}
	sent, dead, why := h.reply("posted:")
	if sent || !dead || !strings.Contains(why, "24-hour") {
		t.Fatalf("sent %v, dead %v, why %q", sent, dead, why)
	}
}

func TestStopEndsConfirmationsAndOnlyThePhoneCanUndoIt(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	link := h.optIn()
	d := h.arrive("STOP-1")

	h.text("m-stop", senderA, "STOP")
	if got := h.meta.last(senderA); !strings.Contains(got.Body, "START") {
		t.Fatalf("reply to STOP: %+v", got)
	}
	// A message queued before STOP must not go after it.
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/approve", d.ID), ""), map[string]any{"version": d.Version, "accept_mismatch": true}, nil, http.StatusOK)
	h.work("")
	if strings.Contains(h.meta.last(senderA).Body, "in your books") {
		t.Fatal("a confirmation went out after STOP")
	}
	// An owner ticking the box cannot overrule the person who said no.
	h.admin(http.MethodPatch, path(fmt.Sprintf("/phone-links/%d", link.ID), ""), map[string]bool{"notify": true}, nil, http.StatusConflict)

	h.text("m-start", senderA, "start")
	var links []PhoneLink
	h.admin(http.MethodGet, path("/phone-links", ""), nil, &links, http.StatusOK)
	if !links[0].Notify || links[0].OptedOut {
		t.Fatalf("after START: %+v", links[0])
	}
	// Files keep working throughout: STOP is about what is sent to the phone.
	if n := h.count("", "retailassistant_documents"); n != 1 {
		t.Fatalf("%d documents", n)
	}
}

func TestStopCoversEveryStoreThePhoneIsLinkedTo(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	h.link(h.second, senderA)
	h.text("m-stop", senderA, "STOP")
	for _, store := range []string{"", h.second} {
		var links []PhoneLink
		h.admin(http.MethodGet, path("/phone-links", store), nil, &links, http.StatusOK)
		if !links[0].OptedOut {
			t.Fatalf("store %q still has the phone opted in", store)
		}
	}
}

func TestAPhoneChoosesHindiAndGetsEverythingInIt(t *testing.T) {
	h := setupWith(t, func(c *Config) { c.TemplateName = "retail_update" })
	h.link("", senderA)
	h.optIn()
	h.text("m-hi", senderA, "हिंदी")
	if got := h.meta.last(senderA).Body; !strings.Contains(got, "हिंदी में") {
		t.Fatalf("reply = %q", got)
	}
	d := h.arrive("HI-1")
	if got := h.meta.last(senderA).Body; !strings.Contains(got, "मिल गया") {
		t.Fatalf("acknowledgement = %q", got)
	}
	h.leaveTheWindow()
	h.admin(http.MethodPost, path(fmt.Sprintf("/documents/%d/approve", d.ID), ""), map[string]any{"version": d.Version, "accept_mismatch": true}, nil, http.StatusOK)
	h.work("")
	got := h.meta.last(senderA)
	if got.Language != langHindi || !strings.Contains(got.Body, "दर्ज") {
		t.Fatalf("template = %+v", got)
	}
}

func TestAnOwnerSetsAPhonesLanguageAndNothingElseChanges(t *testing.T) {
	h := setup(t)
	h.link("", senderA)
	link := h.optIn()
	h.admin(http.MethodPatch, path(fmt.Sprintf("/phone-links/%d", link.ID), ""), map[string]string{"language": langHindi}, nil, http.StatusOK)
	h.admin(http.MethodPatch, path(fmt.Sprintf("/phone-links/%d", link.ID), ""), map[string]string{"language": "klingon"}, nil, http.StatusBadRequest)
	var links []PhoneLink
	h.admin(http.MethodGet, path("/phone-links", ""), nil, &links, http.StatusOK)
	if links[0].Language != langHindi || !links[0].Notify {
		t.Fatalf("link = %+v", links[0])
	}
}

func TestAStrangerGetsBothLanguages(t *testing.T) {
	h := setup(t)
	h.text("m-1", stranger, "hello")
	got := h.meta.last(stranger).Body
	if !strings.Contains(got, "not linked") || !strings.Contains(got, "नहीं जुड़ा") {
		t.Fatalf("reply = %q", got)
	}
}

func TestATemplateVariableHoldsNoNewline(t *testing.T) {
	if got := templateText("Got bad\nname\t.pdf.   It waits."); got != "Got bad name .pdf. It waits." {
		t.Fatalf("got %q", got)
	}
}
