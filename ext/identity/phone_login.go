package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// A store chooses one way for its customers to sign in (D116): an email and a
// password, or a phone and a 4-digit code sent to it by SMS. The choice is a
// setting on the module's plugin, public so the storefront draws only that
// form, and the engine refuses the other way rather than trusting the
// storefront to hide it.
//
// Four digits are ten thousand guesses, so the limits carry the design, as
// they do for an order's code: a code lives ten minutes and takes five tries,
// and a phone gets one code a minute and five a day.

// PluginKey is the module's plugin, which holds the sign-in choice.
const PluginKey = "identity"

// The two ways to sign in.
const (
	LoginEmailPassword = "email_password"
	LoginPhoneOTP      = "phone_otp"
)

// EventLoginCode names the sign-in SMS.
const EventLoginCode = "identity.login_code"

const (
	loginCodeTTL      = 10 * time.Minute
	loginCodeAttempts = 5
	loginCodeGap      = time.Minute
	loginCodeDaily    = 5
	loginCodeSendWait = 30 * time.Second
)

type signInSettings struct {
	LoginMethod string `plugin:"login_method"`
}

func (m *Module) registerSignIn(app *gocommerce.App) {
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "Customer sign-in", Category: "storefront", DefaultEnabled: true,
		Description: "How shoppers sign in to their account on the storefront. Switched off, the shop has no customer accounts and everybody checks out as a guest.",
		Fields: []gocommerce.PluginField{{
			Key: "login_method", Label: "How customers sign in", Kind: "select",
			Options: []string{LoginEmailPassword, LoginPhoneOTP}, Default: LoginEmailPassword, Public: true,
			Help: "email_password: an email address and a password. phone_otp: a phone number and a 4-digit code sent by SMS, which needs an SMS provider under Notifications.",
		}},
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelSMS, Event: EventLoginCode, Title: "Sign-in code",
		Description: "To a shopper signing in with their phone. The code works for ten minutes.",
		Variables:   []string{"code", "store_name", "expires_in_minutes"},
		Body:        `{{.code}} is your {{with .store_name}}{{.}} {{end}}sign-in code. It works for {{.expires_in_minutes}} minutes.`,
	})
}

// signInMethod is the way the store ctx is bound to signs customers in, and
// whether it has customer accounts at all.
func (m *Module) signInMethod(ctx context.Context) (method string, on bool) {
	on, err := m.app.Plugins().Enabled(ctx, PluginKey)
	if err != nil {
		// An unreadable setting is not a reason to lock every shopper out.
		on = true
	}
	var s signInSettings
	_ = m.app.Plugins().Fill(ctx, PluginKey, &s)
	if s.LoginMethod != LoginPhoneOTP {
		s.LoginMethod = LoginEmailPassword
	}
	return s.LoginMethod, on
}

// requireMethod refuses a route that belongs to the way the store did not
// choose.
func (m *Module) requireMethod(ctx context.Context, want string) error {
	method, on := m.signInMethod(ctx)
	if !on {
		return &gocommerce.APIError{Status: http.StatusConflict, Code: "sign_in_method",
			Message: "This shop has no customer accounts. Check out as a guest."}
	}
	if method == want {
		return nil
	}
	msg := "This shop signs customers in with an email address and a password."
	if method == LoginPhoneOTP {
		msg = "This shop signs customers in with a code sent to their phone."
	}
	return &gocommerce.APIError{Status: http.StatusConflict, Code: "sign_in_method", Message: msg}
}

// normalizePhone keeps a leading + and the digits. A country code is required:
// without one the same digits are somebody else's number in another country.
func normalizePhone(phone string) (string, error) {
	phone = strings.TrimSpace(phone)
	var d []byte
	for i := 0; i < len(phone); i++ {
		if phone[i] >= '0' && phone[i] <= '9' {
			d = append(d, phone[i])
		}
	}
	if !strings.HasPrefix(phone, "+") || len(d) < 8 || len(d) > 15 {
		return "", gocommerce.Validationf("enter the phone number with its country code, such as +91 98765 43210")
	}
	return "+" + string(d), nil
}

// smsDelivers is whether a code sent now would reach anybody.
func (m *Module) smsDelivers(ctx context.Context) bool {
	if m.cfg.Notifier != nil {
		return true
	}
	for _, c := range m.app.NotifierChannelsFor(ctx) {
		if c.Channel == gocommerce.ChannelSMS {
			return c.Delivers
		}
	}
	return false
}

// RequestPhoneCode sends a sign-in code to phone. A phone with no account yet
// gets one too: signing in with a code is how an account is opened.
func (m *Module) RequestPhoneCode(ctx context.Context, phone string) error {
	if err := m.requireMethod(ctx, LoginPhoneOTP); err != nil {
		return err
	}
	phone, err := normalizePhone(phone)
	if err != nil {
		return err
	}
	if !m.smsDelivers(ctx) {
		return gocommerce.Conflictf("This shop cannot send text messages yet, so nobody can sign in. Try again later.")
	}
	code, err := newLoginCode()
	if err != nil {
		return gocommerce.Internalf(err, "generate sign-in code")
	}
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		// One phone's requests count one at a time.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "identity-login:"+phone); err != nil {
			return err
		}
		var recent, today int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FILTER (WHERE created_at > now() - $2::interval),
			       count(*) FILTER (WHERE created_at > now() - interval '1 day')
			FROM identity_login_codes WHERE phone = $1`,
			phone, fmt.Sprintf("%d seconds", int(loginCodeGap.Seconds()))).Scan(&recent, &today); err != nil {
			return err
		}
		if recent > 0 || today >= loginCodeDaily {
			when := "a minute"
			if today >= loginCodeDaily {
				when = "tomorrow"
			}
			return &gocommerce.APIError{Status: http.StatusTooManyRequests, Code: "too_many_requests",
				Message: "A code was sent a moment ago. Check your messages, or ask again in " + when + "."}
		}
		// Only the latest code works.
		if _, err := tx.ExecContext(ctx,
			`UPDATE identity_login_codes SET expires_at = now() WHERE phone = $1 AND expires_at > now()`, phone); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO identity_login_codes (phone, code_hash, expires_at) VALUES ($1, $2, $3)`,
			phone, loginCodeHash(phone, code), time.Now().Add(loginCodeTTL))
		return err
	})
	var apiErr *gocommerce.APIError
	if errors.As(err, &apiErr) {
		return err
	}
	if err != nil {
		return gocommerce.Internalf(err, "store sign-in code")
	}

	n := gocommerce.Notification{
		Event: EventLoginCode, Channel: gocommerce.ChannelSMS, To: phone, Language: m.language(ctx),
		Data: map[string]string{"code": code, "store_name": m.storeName(ctx),
			"expires_in_minutes": fmt.Sprint(int(loginCodeTTL / time.Minute))},
	}
	sendCtx := context.WithoutCancel(ctx)
	m.sends.Add(1)
	go func() {
		defer m.sends.Done()
		ctx, cancel := context.WithTimeout(sendCtx, loginCodeSendWait)
		defer cancel()
		if err := m.notify(ctx, n); err != nil {
			// The code is never logged: it is a sign-in.
			m.app.Log().Error("sign-in code could not be sent", "error", err)
		}
	}()
	return nil
}

// VerifyPhoneCode spends one try and, when code is the phone's open code,
// signs in the account on that phone, opening one if it has none.
func (m *Module) VerifyPhoneCode(ctx context.Context, phone, code string) (*Customer, *Session, error) {
	if err := m.requireMethod(ctx, LoginPhoneOTP); err != nil {
		return nil, nil, err
	}
	wrong := &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "invalid_code",
		Message: "That code is not right, or it has expired. Check it, or ask for a new one."}
	phone, err := normalizePhone(phone)
	if err != nil {
		return nil, nil, err
	}
	code = strings.TrimSpace(code)
	if len(code) != 4 {
		return nil, nil, wrong
	}
	// Charged and matched in one statement, as an order's code is: a burst of
	// guesses queues on the row lock, and a right code is spent with it.
	var opened bool
	err = m.db.QueryRowContext(ctx, `
		WITH tried AS (
			UPDATE identity_login_codes SET attempts = attempts + 1,
			       expires_at = CASE WHEN code_hash = $2 THEN now() ELSE expires_at END
			WHERE phone = $1 AND expires_at > now() AND attempts < $3
			RETURNING code_hash)
		SELECT true FROM tried WHERE code_hash = $2`,
		phone, loginCodeHash(phone, code), loginCodeAttempts).Scan(&opened)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, wrong
	}
	if err != nil {
		return nil, nil, gocommerce.Internalf(err, "check sign-in code")
	}

	// The account on this phone, or a new one. ON CONFLICT makes two first
	// sign-ins at once open one account between them.
	c, err := scanCustomer(m.db.QueryRowContext(ctx, `
		INSERT INTO identity_customers (login_phone, phone) VALUES ($1, $1)
		ON CONFLICT (store_id, login_phone) WHERE login_phone IS NOT NULL
		DO UPDATE SET updated_at = identity_customers.updated_at
		RETURNING `+customerColumns, phone))
	if err != nil {
		return nil, nil, gocommerce.Internalf(err, "open account")
	}
	sess, err := m.issue(ctx, c.ID)
	if err != nil {
		return nil, nil, err
	}
	return c, sess, nil
}

func (m *Module) storeName(ctx context.Context) string {
	if p, err := m.app.Profile().Get(ctx); err == nil {
		return p.Name
	}
	return ""
}

func newLoginCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(10_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%04d", n.Int64()), nil
}

// loginCodeHash keys a code to its phone, because two phones can hold the same
// four digits.
func loginCodeHash(phone, code string) string {
	return hashToken("login:" + phone + ":" + code)
}

// waitForSends waits for the codes in flight, up to max.
func (m *Module) waitForSends(max time.Duration) {
	done := make(chan struct{})
	go func() {
		m.sends.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(max):
	}
}

func (m *Module) handleRequestPhoneCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := m.RequestPhoneCode(r.Context(), in.Phone); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusAccepted, map[string]int{"expires_in_minutes": int(loginCodeTTL / time.Minute)})
}

func (m *Module) handleVerifyPhoneCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	c, sess, err := m.VerifyPhoneCode(r.Context(), in.Phone, in.Code)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	respondAuth(w, http.StatusOK, c, sess)
}
