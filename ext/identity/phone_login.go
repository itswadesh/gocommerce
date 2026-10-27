package identity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net"
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

// The ways to sign in. LoginEither offers both, and an email account can
// link a phone it has proved, so either way opens it.
const (
	LoginEmailPassword = "email_password"
	LoginPhoneOTP      = "phone_otp"
	LoginEither        = "email_or_phone"
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
		Key: PluginKey, Title: "Customer sign-in", Icon: "ri-user-follow-line", Category: "storefront", DefaultEnabled: true,
		Home:        "/dash/settings",
		Description: "How shoppers sign in to their account on the storefront. Switched off, the shop has no customer accounts and everybody checks out as a guest.",
		Fields: []gocommerce.PluginField{{
			Key: "login_method", Label: "How customers sign in", Kind: "select",
			Options: []string{LoginEmailPassword, LoginPhoneOTP, LoginEither}, Default: LoginEmailPassword, Public: true,
			Help: "email_password: an email address and a password. phone_otp: a phone number and a 4-digit code sent by SMS, which needs an SMS provider under Notifications. email_or_phone: either, and a customer signed in by email can add a phone with a code.",
		}},
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelSMS, Event: EventLoginCode, Title: "Sign-in code",
		Description: "To a shopper signing in with their phone. The code works for ten minutes.",
		Variables:   []string{"code", "store_name", "expires_in_minutes", "domain"},
		// The wording of the DLT template MSG91 carries, so every SMS provider
		// sends the same text. The last line, when the shopper is on the
		// store's own site, is the one Chrome on Android reads to fill the
		// code in (the Web OTP API).
		Body: `<#> Hi, {{.code}} is your OTP to login to {{with .store_name}}{{.}}{{else}}your account{{end}}` +
			`{{with .domain}}` + "\n\n" + `@{{.}} #{{$.code}}{{end}}`,
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
	if s.LoginMethod != LoginPhoneOTP && s.LoginMethod != LoginEither {
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
	if method == want || method == LoginEither {
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

// smsCarries is whether a sign-in code texted to phone now would reach it.
// Asked of the code, not of SMS in general: MSG91 texts only what it has a
// DLT template for, so a store texting about orders can still have nobody for
// sign-in codes.
func (m *Module) smsCarries(ctx context.Context, phone string) bool {
	if m.cfg.Notifier != nil {
		return true
	}
	return m.app.WouldDeliver(ctx, gocommerce.Notification{Event: EventLoginCode, Channel: gocommerce.ChannelSMS, To: phone})
}

// RequestPhoneCode sends a sign-in code to phone. A phone with no account yet
// gets one too: signing in with a code is how an account is opened.
func (m *Module) RequestPhoneCode(ctx context.Context, phone string) error {
	return m.RequestPhoneCodeOn(ctx, phone, "")
}

// RequestPhoneCodeOn is RequestPhoneCode from a page on site, the storefront's
// host. When the site is this store's own, the text names it, so Chrome on
// Android can fill the code in by itself (the Web OTP API reads a last line
// "@site #code"). Any other site is left out: Chrome fills a code in only on
// the site the text names, and naming one the store does not own would hand
// a shopper's code to that site.
func (m *Module) RequestPhoneCodeOn(ctx context.Context, phone, site string) error {
	if err := m.requireMethod(ctx, LoginPhoneOTP); err != nil {
		return err
	}
	return m.sendPhoneCode(ctx, phone, m.ownSite(ctx, site))
}

// ownSite is host, lower-cased and without a port, when it is a verified
// domain of the store ctx is bound to or its subdomain of the base domain;
// otherwise empty.
func (m *Module) ownSite(ctx context.Context, host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return ""
	}
	id, ok := gocommerce.StoreFrom(ctx)
	if !ok {
		return ""
	}
	st, err := m.app.Stores().ByHost(ctx, host)
	if err != nil || st == nil || st.ID != id {
		return ""
	}
	return host
}

// sendPhoneCode texts a code to phone, within the limits, for signing in or
// for linking the phone to an account. The two share the phone's codes and
// limits, so linking is no way around them.
func (m *Module) sendPhoneCode(ctx context.Context, phone, site string) error {
	phone, err := normalizePhone(phone)
	if err != nil {
		return err
	}
	// Free for Indian numbers; any other country is part of the Pro plan
	// (D119). Said to the shopper as what the shop can do, not as a plan.
	if !strings.HasPrefix(phone, "+91") && m.app.StorePlan(ctx) != gocommerce.PlanPro {
		return &gocommerce.APIError{Status: http.StatusForbidden, Code: "plan_required",
			Message: "This shop can text sign-in codes to Indian (+91) numbers only. Use one, or sign in with your email."}
	}
	if !m.smsCarries(ctx, phone) {
		m.app.Log().Warn("phone sign-in refused: no SMS provider carries sign-in codes; set its Sign-in code template under Notifications › Setup SMS")
		return gocommerce.Conflictf("This shop cannot text sign-in codes yet, so nobody can sign in with a phone. Try again later.")
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
			"expires_in_minutes": fmt.Sprint(int(loginCodeTTL / time.Minute)), "domain": site},
		Secrets: []string{"code"},
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
	c, sess, _, err := m.VerifyPhoneCodeAsking(ctx, phone, code, false)
	return c, sess, err
}

// PhoneProof is the answer to a right code for a phone with no account, on a
// store that signs in either way (D123): the shopper says whether they
// already have an account, signing in to it with the proof to add the phone,
// or opens a new one with it (OpenPhoneAccount).
type PhoneProof struct {
	NewPhone   bool   `json:"new_phone"`
	PhoneToken string `json:"phone_token"`
}

// phoneProofTTL is how long the shopper has to answer.
const phoneProofTTL = 10 * time.Minute

// VerifyPhoneCodeAsking is VerifyPhoneCode, except that with ask, on a store
// that signs in either way, a phone with no account opens none: it answers a
// PhoneProof instead, as Myntra asks "already have an account?", so a
// customer who signed up by email is not handed a second, empty account.
func (m *Module) VerifyPhoneCodeAsking(ctx context.Context, phone, code string, ask bool) (*Customer, *Session, *PhoneProof, error) {
	if err := m.requireMethod(ctx, LoginPhoneOTP); err != nil {
		return nil, nil, nil, err
	}
	phone, err := m.spendPhoneCode(ctx, phone, code)
	if err != nil {
		return nil, nil, nil, err
	}
	if method, _ := m.signInMethod(ctx); ask && method == LoginEither {
		var known bool
		if err := m.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM identity_customers WHERE login_phone = $1)`, phone).Scan(&known); err != nil {
			return nil, nil, nil, gocommerce.Internalf(err, "look up phone")
		}
		if !known {
			token, err := newToken()
			if err != nil {
				return nil, nil, nil, gocommerce.Internalf(err, "prove phone")
			}
			// Kept beside the phone's codes, so asking for a new code
			// retires it as it retires them.
			if _, err := m.db.ExecContext(ctx,
				`INSERT INTO identity_login_codes (phone, code_hash, expires_at) VALUES ($1, $2, $3)`,
				phone, phoneProofHash(phone, token), time.Now().Add(phoneProofTTL)); err != nil {
				return nil, nil, nil, gocommerce.Internalf(err, "prove phone")
			}
			return nil, nil, &PhoneProof{NewPhone: true, PhoneToken: token}, nil
		}
	}
	c, sess, err := m.openPhoneAccount(ctx, phone)
	return c, sess, nil, err
}

// OpenPhoneAccount spends a phone's proof and signs in a new account on it:
// the shopper said they have no account already.
func (m *Module) OpenPhoneAccount(ctx context.Context, phone, token string) (*Customer, *Session, error) {
	if err := m.requireMethod(ctx, LoginPhoneOTP); err != nil {
		return nil, nil, err
	}
	phone, err := m.spendPhoneProof(ctx, m.db, phone, token)
	if err != nil {
		return nil, nil, err
	}
	return m.openPhoneAccount(ctx, phone)
}

// AuthenticateAddingPhone signs in with an email and a password and makes the
// proved phone a way into that account, as LinkPhone does. A wrong password
// leaves the proof unspent, to try again.
func (m *Module) AuthenticateAddingPhone(ctx context.Context, email, password, clientIP, phone, token string) (*Customer, *Session, error) {
	if err := m.requireMethod(ctx, LoginPhoneOTP); err != nil {
		return nil, nil, err
	}
	phone, err := normalizePhone(phone)
	if err != nil {
		return nil, nil, err
	}
	// The proof is checked before the password, so a stale one is said
	// plainly rather than after the shopper typed the password right.
	var open bool
	if err := m.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM identity_login_codes
		WHERE phone = $1 AND code_hash = $2 AND expires_at > now())`,
		phone, phoneProofHash(phone, token)).Scan(&open); err != nil {
		return nil, nil, gocommerce.Internalf(err, "check phone")
	}
	if !open {
		return nil, nil, phoneProofExpired
	}
	c, err := m.checkPassword(ctx, email, password, clientIP)
	if err != nil {
		return nil, nil, err
	}
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if _, err := m.spendPhoneProof(ctx, tx, phone, token); err != nil {
			return err
		}
		linked, err := scanCustomer(tx.QueryRowContext(ctx, `
			UPDATE identity_customers SET login_phone = $2, phone = $2, updated_at = now()
			WHERE id = $1 AND (login_phone IS NULL OR login_phone = $2) RETURNING `+customerColumns, c.ID, phone))
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.Conflictf("This account already signs in with another phone. Sign in with that phone, or with your email alone.")
		}
		if isUniqueViolation(err, "identity_customers_login_phone_key") {
			return gocommerce.Conflictf("%s already signs in to another account here.", phone)
		}
		if err != nil {
			return gocommerce.Internalf(err, "add phone")
		}
		c = linked
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sess, err := m.issue(ctx, c.ID)
	if err != nil {
		return nil, nil, err
	}
	return c, sess, nil
}

var phoneProofExpired = &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "invalid_code",
	Message: "That phone check has expired. Ask for a new code."}

// rowQuerier is a database or a transaction.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// spendPhoneProof spends phone's open proof, returning the normalized phone.
func (m *Module) spendPhoneProof(ctx context.Context, q rowQuerier, phone, token string) (string, error) {
	phone, err := normalizePhone(phone)
	if err != nil {
		return "", err
	}
	var id int64
	err = q.QueryRowContext(ctx, `
		UPDATE identity_login_codes SET expires_at = now()
		WHERE phone = $1 AND code_hash = $2 AND expires_at > now() RETURNING id`,
		phone, phoneProofHash(phone, token)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", phoneProofExpired
	}
	if err != nil {
		return "", gocommerce.Internalf(err, "spend phone check")
	}
	return phone, nil
}

func phoneProofHash(phone, token string) string {
	return hashToken("proof:" + phone + ":" + token)
}

// openPhoneAccount signs in the account on phone, opening it the first time.
func (m *Module) openPhoneAccount(ctx context.Context, phone string) (*Customer, *Session, error) {
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

// spendPhoneCode spends one try and returns the normalized phone when code is
// its open code. A wrong, spent or expired code is one answer.
func (m *Module) spendPhoneCode(ctx context.Context, phone, code string) (string, error) {
	wrong := &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "invalid_code",
		Message: "That code is not right, or it has expired. Check it, or ask for a new one."}
	phone, err := normalizePhone(phone)
	if err != nil {
		return "", err
	}
	code = strings.TrimSpace(code)
	if len(code) != 4 {
		return "", wrong
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
		return "", wrong
	}
	if err != nil {
		return "", gocommerce.Internalf(err, "check sign-in code")
	}
	return phone, nil
}

// RequestPhoneLink texts a code to phone so a signed-in customer can prove it
// is theirs. Only a store that signs in by phone has a use for one.
func (m *Module) RequestPhoneLink(ctx context.Context, phone string) error {
	if err := m.requireMethod(ctx, LoginPhoneOTP); err != nil {
		return err
	}
	return m.sendPhoneCode(ctx, phone, "")
}

// LinkPhone makes phone a way into customerID's account once code proves it,
// so signing in with the phone opens this account. The phone on the profile
// is set to it as well. A phone that already opens another account is not
// taken from that one: merging two accounts is not this.
func (m *Module) LinkPhone(ctx context.Context, customerID int64, phone, code string) (*Customer, error) {
	if err := m.requireMethod(ctx, LoginPhoneOTP); err != nil {
		return nil, err
	}
	phone, err := m.spendPhoneCode(ctx, phone, code)
	if err != nil {
		return nil, err
	}
	c, err := scanCustomer(m.db.QueryRowContext(ctx, `
		UPDATE identity_customers SET login_phone = $2, phone = $2, updated_at = now()
		WHERE id = $1 RETURNING `+customerColumns, customerID, phone))
	if isUniqueViolation(err, "identity_customers_login_phone_key") {
		return nil, gocommerce.Conflictf("%s already signs in to another account here.", phone)
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "link phone")
	}
	return c, nil
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
		// The storefront's host, named in the text when it is this store's
		// own, for Chrome to fill the code in (RequestPhoneCodeOn).
		Domain string `json:"domain"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := m.RequestPhoneCodeOn(r.Context(), in.Phone, in.Domain); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusAccepted, map[string]int{"expires_in_minutes": int(loginCodeTTL / time.Minute)})
}

func (m *Module) handleVerifyPhoneCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone    string `json:"phone"`
		Code     string `json:"code"`
		AskIfNew bool   `json:"ask_if_new"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	c, sess, proof, err := m.VerifyPhoneCodeAsking(r.Context(), in.Phone, in.Code, in.AskIfNew)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if proof != nil {
		gocommerce.Respond(w, http.StatusOK, proof)
		return
	}
	respondAuth(w, http.StatusOK, c, sess)
}

func (m *Module) handleOpenPhoneAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone      string `json:"phone"`
		PhoneToken string `json:"phone_token"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	c, sess, err := m.OpenPhoneAccount(r.Context(), in.Phone, in.PhoneToken)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	respondAuth(w, http.StatusOK, c, sess)
}

func (m *Module) handleRequestPhoneLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := m.RequestPhoneLink(r.Context(), in.Phone); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusAccepted, map[string]int{"expires_in_minutes": int(loginCodeTTL / time.Minute)})
}

func (m *Module) handleLinkPhone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	c, err := m.LinkPhone(r.Context(), CustomerFrom(r.Context()).ID, in.Phone, in.Code)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, c)
}
