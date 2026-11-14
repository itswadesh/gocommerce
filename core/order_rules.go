package gocommerce

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"unicode/utf8"
)

// A small shop's own rules about taking an order (D271): collecting it from
// the counter, a smallest order worth packing, a pause while the shop is shut
// and the questions it asks every buyer. Each is a built-in plugin, off until
// an owner switches it on, and each is enforced here rather than by the
// storefront, because a storefront is whatever the store built and an order
// reaches checkout from more than one of them.
const (
	PickupPluginKey            = "store-pickup"
	MinimumOrderPluginKey      = "minimum-order"
	PauseOrdersPluginKey       = "pause-orders"
	CheckoutQuestionsPluginKey = "checkout-questions"

	// PickupMethod is the shipping method a pickup order is stored under.
	PickupMethod = "Pickup from the shop"
	// CheckoutAnswersKey is where an order keeps the buyer's answers to the
	// store's checkout questions: a list of {question, answer}, in the order
	// the store asks them. Written by the engine from CheckoutInput.Answers.
	CheckoutAnswersKey = "checkout_answers"

	defaultPausedMessage = "We are not taking orders right now. Please check back soon."
	maxAnswerRunes       = 500
)

// preparePickup lets a pickup checkout through without a delivery address: tax
// is charged where the goods change hands, so a buyer who typed no country is
// taxed at the shop's.
func (a *App) preparePickup(ctx context.Context, in *CheckoutInput) error {
	if !in.Pickup {
		return nil
	}
	on, err := a.plugins.Enabled(ctx, PickupPluginKey)
	if err != nil {
		return err
	}
	if !on {
		return Validationf("this store does not offer pickup; choose delivery")
	}
	if strings.TrimSpace(in.Address.Country) == "" {
		if prof, err := a.profile.Get(ctx); err == nil {
			in.Address.Country, in.Address.State = prof.Country, prof.State
		}
	}
	return nil
}

// checkoutQuestion is one line of the store's questions; a trailing * makes it
// required.
type checkoutQuestion struct {
	Text     string
	Required bool
}

func parseCheckoutQuestions(s string) []checkoutQuestion {
	var out []checkoutQuestion
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		required := strings.HasSuffix(line, "*")
		line = strings.TrimSpace(strings.TrimSuffix(line, "*"))
		if line != "" {
			out = append(out, checkoutQuestion{Text: line, Required: required})
		}
	}
	return out
}

// applyCheckoutAnswers moves the buyer's answers onto the order's metadata,
// only for questions the store asks and in its order, and refuses a shopper's
// checkout that leaves a required one empty. An operator placing an order by
// hand is not held to them: they are the shop's questions to its buyers.
func (a *App) applyCheckoutAnswers(ctx context.Context, in *CheckoutInput) error {
	meta := Metadata{}
	for k, v := range in.Metadata {
		meta[k] = v
	}
	// Only the engine writes it, from Answers, so it always names questions
	// the store asked.
	delete(meta, CheckoutAnswersKey)
	in.Metadata = meta
	on, err := a.plugins.Enabled(ctx, CheckoutQuestionsPluginKey)
	if err != nil || !on {
		return err
	}
	var answers []map[string]string
	for _, q := range parseCheckoutQuestions(a.plugins.String(ctx, CheckoutQuestionsPluginKey, "questions")) {
		answer := strings.TrimSpace(in.Answers[q.Text])
		if answer == "" {
			if q.Required && !byOperator(ctx) && !isCounterSale(ctx) {
				return Validationf("please answer %q", q.Text)
			}
			continue
		}
		if utf8.RuneCountInString(answer) > maxAnswerRunes {
			return Validationf("the answer to %q is too long: %d characters at most", q.Text, maxAnswerRunes)
		}
		answers = append(answers, map[string]string{"question": q.Text, "answer": answer})
	}
	if len(answers) > 0 {
		meta[CheckoutAnswersKey] = answers
	}
	return nil
}

// CheckoutAnswers reads the answers an order was placed with, in the order the
// store asked them.
func (o *Order) CheckoutAnswers() [][2]string {
	list, _ := o.Metadata[CheckoutAnswersKey].([]any)
	var out [][2]string
	for _, item := range list {
		m, _ := item.(map[string]any)
		q, _ := m["question"].(string)
		ans, _ := m["answer"].(string)
		if q != "" && ans != "" {
			out = append(out, [2]string{q, ans})
		}
	}
	return out
}

// orderRules refuses a shopper's checkout while orders are paused or below the
// store's minimum. It runs inside phase A beside the module guards, so it sees
// the final figures and a refusal leaves no order behind. An operator's order
// and a counter sale pass: the owner pausing the website is not the owner
// refusing to serve someone in front of them.
func (a *App) orderRules(ctx context.Context, tx *sql.Tx, at *CheckoutAttempt) error {
	if at.ByOperator || isCounterSale(ctx) {
		return nil
	}
	paused, err := a.plugins.EnabledTx(ctx, tx, PauseOrdersPluginKey)
	if err != nil {
		return err
	}
	if paused {
		msg := firstOf(a.plugins.String(ctx, PauseOrdersPluginKey, "message"), defaultPausedMessage)
		return &APIError{Status: http.StatusConflict, Code: "orders_paused", kind: "conflict", Message: msg}
	}
	minOn, err := a.plugins.EnabledTx(ctx, tx, MinimumOrderPluginKey)
	if err != nil || !minOn {
		return err
	}
	currency := at.Subtotal.Currency
	least, err := parseMoney(a.plugins.String(ctx, MinimumOrderPluginKey, "amount"), CurrencyExponent(currency))
	if err != nil || least <= 0 {
		// A minimum that does not read as money refuses nobody, as a bad cash
		// on delivery fee charges nothing.
		return nil
	}
	if short := least - at.Subtotal.AmountMinor; short > 0 {
		return (&APIError{Status: http.StatusBadRequest, Code: "below_minimum_order", kind: "validation_failed",
			Message: "The minimum order is " + messageMoney(money(least, currency)) +
				". Add " + messageMoney(money(short, currency)) + " more to place it."}).
			WithDetails(map[string]Money{"minimum": money(least, currency), "short": money(short, currency)})
	}
	return nil
}
