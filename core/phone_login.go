package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Signing in to the panel with a code texted to a phone, beside the email and
// password (D193). The code is good for a person, and a person is good for
// every store they work in, so the limits live in the database, where every
// replica counts the same rows.
//
// Six digits, as a password reset's code, where a shopper's is four: an
// operator's account can refund orders and read every customer. A code lives
// ten minutes and takes five tries; a phone gets one code a minute and five a
// day, and one client address twenty an hour.

// EventLoginCode names the sign-in SMS, for operators and shoppers alike. One
// message, so an install's approved DLT template (SMS_OTP_TEMPLATE_ID) carries
// both and nobody registers a second with the regulator.
const EventLoginCode = "identity.login_code"

const (
	phoneCodeTTL      = 10 * time.Minute
	phoneCodeAttempts = 5
	phoneCodeLength   = 6
	phoneCodeSendWait = 30 * time.Second

	phoneCodesPerDay      = 5
	phoneCodesPerClientHr = 20
)

// reachProbe stands for "a number" where none has been typed yet. Indian
// because a provider that carries only some numbers (MSG91) carries those, and
// a provider for every number carries it too.
const reachProbe = "+919000000000"

// NormalizePhone is phone in E.164: a +, a country code and the digits. The
// country code is required, because without one the same digits are somebody
// else's number in another country. Spaces and the usual punctuation go;
// anything else is refused rather than dropped, so "+91 98765 x43210" does not
// become a number nobody typed.
func NormalizePhone(phone string) (string, error) {
	bad := Validationf("enter the phone number with its country code, such as +91 98765 43210")
	phone = strings.TrimSpace(phone)
	if !strings.HasPrefix(phone, "+") {
		return "", bad
	}
	d := make([]byte, 0, 15)
	for _, r := range phone[1:] {
		switch {
		case r >= '0' && r <= '9':
			d = append(d, byte(r))
		case unicode.IsSpace(r) || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", bad
		}
	}
	if len(d) < 8 || len(d) > 15 || d[0] == '0' {
		return "", bad
	}
	// India's mobile numbers have one shape. Without this, one handset is also
	// "+91 0 98765 43210": another account, and another phone's limits, if a
	// provider drops the zero.
	if d[0] == '9' && d[1] == '1' && (len(d) != 12 || d[2] < '6') {
		return "", bad
	}
	return "+" + string(d), nil
}

// MaskPhone is a number as a log or an audit row may keep it.
func MaskPhone(phone string) string { return maskPhone(phone) }

// MaskPhoneIn is text with phone masked wherever it appears, with or without
// its plus, for a provider's words on their way to a log.
func MaskPhoneIn(text, phone string) string {
	masked := MaskPhone(phone)
	text = strings.ReplaceAll(text, phone, masked)
	return strings.ReplaceAll(text, strings.TrimPrefix(phone, "+"), masked)
}

// ParseSMSCountries reads a comma-separated list of calling codes. Anything it
// cannot read gives India alone: a typo in a spending limit should neither stop
// the engine nor open the install to every country.
func ParseSMSCountries(list string) []string {
	india := []string{"91"}
	var out []string
	for _, code := range strings.Split(list, ",") {
		code = strings.TrimPrefix(strings.TrimSpace(code), "+")
		if len(code) < 1 || len(code) > 3 || code[0] == '0' || strings.Trim(code, "0123456789") != "" {
			return india
		}
		out = append(out, code)
	}
	return out
}

// checkSMSCountry refuses a number this install does not text codes to. About the
// number that was typed and not who holds it, so it says nothing about accounts.
func (s *Superusers) checkSMSCountry(phone string) error {
	for _, code := range s.app.cfg.SMSCountries {
		// Calling codes are prefix-free: no code begins another.
		if strings.HasPrefix(phone, "+"+code) {
			return nil
		}
	}
	return &APIError{Status: http.StatusConflict, Code: "sms_country",
		Message: "this install does not text sign-in codes to that country; sign in with your email"}
}

func phoneUnavailable() *APIError {
	return &APIError{Status: http.StatusConflict, Code: "sms_unavailable",
		Message: "this install cannot text a sign-in code to that number; sign in with your email"}
}

func wrongPhoneCode() *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "invalid_code",
		Message: "that code is not right, or it has expired; check it, or ask for a new one"}
}

func tooManyPhoneCodes() *APIError {
	return &APIError{Status: http.StatusTooManyRequests, Code: "too_many_attempts",
		Message: "too many codes asked for; wait a minute and try again, or sign in with your email"}
}

func phoneCodeHash(phone, code string) string {
	return hashToken("operator-login:" + phone + ":" + code)
}

// ownCodeHash is phoneCodeHash for a code asked for from inside an account, to
// add an address to it. That person named the address, so what proves an address
// to them must not sign anybody in to it (D213).
func ownCodeHash(identity, code string) string {
	return hashToken("operator-link:" + identity + ":" + code)
}

// atInstall is ctx on the install's own channel (identityMailScope).
func atInstall(ctx context.Context) context.Context {
	return WithStore(context.WithoutCancel(ctx), installScope)
}

func loginCodeNote(to string) Notification {
	channel := ChannelSMS
	if isEmailAddress(to) {
		channel = ChannelEmail
	}
	return Notification{Event: EventLoginCode, Channel: channel, To: to}
}

func isEmailAddress(to string) bool { return strings.Contains(to, "@") }

// maskAddress is an email or a phone as a log may keep it.
func maskAddress(to string) string {
	if isEmailAddress(to) {
		return maskEmail(to)
	}
	return MaskPhone(to)
}

// phoneCanReach is whether a code for phone could leave on the one channel it
// may use (identityMailScope). Asked of the code rather than of SMS in general,
// because MSG91 texts only what it has a DLT template for. Nothing here depends
// on who the number belongs to.
func (s *Superusers) phoneCanReach(ctx context.Context, phone string) bool {
	return s.app.notifier.carries(s.identityMailScope(ctx), loginCodeNote(phone))
}

// phoneLoginOpen says whether the sign-in screen may offer a phone. Not on a
// demo, whose one account is the only way in; and not before there is an owner,
// because the installer takes an email.
func (a *App) phoneLoginOpen(ctx context.Context) bool {
	if a.cfg.Demo || !a.superusers.phoneCanReach(ctx, reachProbe) {
		return false
	}
	n, err := a.superusers.CountPeople(ctx)
	return err == nil && n > 0
}

// phoneSignupOpen is whether a new person can get a code: sign-up is open, and
// the install itself can text, since a new person's code never goes out on a
// store's channel.
func (a *App) phoneSignupOpen(ctx context.Context) bool {
	return a.signupOpen(ctx) && a.notifier.carries(atInstall(ctx), loginCodeNote(reachProbe))
}

// storeLoginCode charges the limits and stores a new code as the phone's only
// open one, all under locks: a burst of requests counts one at a time, on
// every replica. It writes the row whether or not the phone has an account.
func (s *Superusers) storeLoginCode(ctx context.Context, phone, clientIP string, hash func(identity, code string) string) (string, error) {
	code, err := newResetCode()
	if err != nil {
		return "", Internalf(err, "generate sign-in code")
	}
	client := hashToken("operator-login-client:" + limitKey(clientIP))
	err = InTx(ctx, s.db, func(tx *sql.Tx) error {
		// In this order everywhere, and each statement after these sees what
		// the holder before it committed.
		for _, key := range []string{"install", "phone:" + phone, "client:" + client} {
			if _, err := tx.ExecContext(ctx,
				`SELECT pg_advisory_xact_lock(hashtext('superuser_login_codes'), hashtext($1))`, key); err != nil {
				return Internalf(err, "lock sign-in codes")
			}
		}
		// The install's whole day of codes, so many numbers from many addresses
		// cannot run up its SMS bill. People can still use a password. An email
		// costs nothing and must not use up what a text can.
		if !isEmailAddress(phone) {
			if full, err := s.smsDayIsFull(ctx, tx); err != nil {
				return err
			} else if full {
				return tooManyPhoneCodes()
			}
		}
		var recent, today, fromClient int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FILTER (WHERE identity = $1 AND created_at > now() - interval '1 minute'),
			       count(*) FILTER (WHERE identity = $1 AND created_at > now() - interval '1 day'),
			       count(*) FILTER (WHERE client_hash = $2 AND created_at > now() - interval '1 hour')
			FROM superuser_login_codes WHERE identity = $1 OR client_hash = $2`, phone, client).
			Scan(&recent, &today, &fromClient); err != nil {
			return Internalf(err, "count sign-in codes")
		}
		if recent > 0 || today >= phoneCodesPerDay || fromClient >= phoneCodesPerClientHr {
			return tooManyPhoneCodes()
		}
		// Only the latest code works.
		if _, err := tx.ExecContext(ctx, `UPDATE superuser_login_codes SET consumed_at = now()
			WHERE identity = $1 AND consumed_at IS NULL`, phone); err != nil {
			return Internalf(err, "retire sign-in codes")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO superuser_login_codes (identity, code_hash, client_hash, expires_at)
			VALUES ($1, $2, $3, $4)`, phone, hash(phone, code), client, time.Now().Add(phoneCodeTTL)); err != nil {
			return Internalf(err, "store sign-in code")
		}
		// Opportunistic, as Superusers.issue does for sessions. Kept this long
		// because the daily limit counts a row after its code has died.
		if _, err := tx.ExecContext(ctx, `DELETE FROM superuser_login_codes WHERE created_at < now() - interval '2 days'`); err != nil {
			return Internalf(err, "sweep sign-in codes")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// smsDayIsFull says the install has texted all the codes it may in 24 hours. The
// caller holds the install's lock, so a burst counts one at a time.
func (s *Superusers) smsDayIsFull(ctx context.Context, tx *sql.Tx) (bool, error) {
	var today int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM superuser_login_codes
		WHERE created_at > now() - interval '1 day' AND identity NOT LIKE '%@%'`).Scan(&today); err != nil {
		return false, Internalf(err, "count texted codes")
	}
	limit := s.app.cfg.SMSCodesPerDay
	if today < limit {
		return false, nil
	}
	s.app.log.Error("texted code refused: the install reached its daily limit", "limit", limit)
	return true, nil
}

// spendLoginCode spends one try at phone's open code, and says whether it was
// right. Charged and matched in one statement, so a burst of guesses queues on
// the row lock, and a right code is spent with its try.
func (s *Superusers) spendLoginCode(ctx context.Context, phone, code string, hash func(identity, code string) string) (bool, error) {
	code = strings.TrimSpace(code)
	if len(code) != phoneCodeLength {
		return false, nil
	}
	var right bool
	err := s.db.QueryRowContext(ctx, `
		WITH tried AS (
			UPDATE superuser_login_codes SET attempts = attempts + 1,
			       consumed_at = CASE WHEN code_hash = $2 THEN now() END
			WHERE identity = $1 AND consumed_at IS NULL AND expires_at > now() AND attempts < $3
			RETURNING code_hash)
		SELECT true FROM tried WHERE code_hash = $2`,
		phone, hash(phone, code), phoneCodeAttempts).Scan(&right)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, Internalf(err, "check sign-in code")
	}
	return right, nil
}

// RequestPhoneCode texts phone a code if it could sign in, or sign up, with
// one. Every outcome past the shape of the number and the provider answers the
// same whoever the number belongs to, and the looking up and the sending happen
// after that answer, so neither its words nor its timing can say.
func (s *Superusers) RequestPhoneCode(ctx context.Context, phone, clientIP string) error {
	phone, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	if err := s.checkSMSCountry(phone); err != nil {
		return err
	}
	if s.app.cfg.Demo || !s.phoneCanReach(ctx, phone) {
		return phoneUnavailable()
	}
	code, err := s.storeLoginCode(ctx, phone, clientIP, phoneCodeHash)
	if err != nil {
		return err
	}
	s.deliverLoginCode(ctx, phone, code, false)
	return nil
}

// RequestOwnPhoneCode texts a code to a number a signed-in person wants to
// sign in with. It shares the phone's limits, so it is no way around them, and
// the code only adds the number to an account (ownCodeHash).
func (s *Superusers) RequestOwnPhoneCode(ctx context.Context, phone, clientIP string) error {
	phone, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	if err := s.checkSMSCountry(phone); err != nil {
		return err
	}
	if !s.phoneCanReach(ctx, phone) {
		return phoneUnavailable()
	}
	code, err := s.storeLoginCode(ctx, phone, clientIP, ownCodeHash)
	if err != nil {
		return err
	}
	s.deliverLoginCode(ctx, phone, code, true)
	return nil
}

// deliverLoginCode sends the code off the request goroutine. An own code goes to
// the address its person named; any other to whoever holds the phone, or to a new
// person where sign-up is open, and to nobody otherwise. Either goes on the
// channel identityMailScope chooses, whoever the person is.
func (s *Superusers) deliverLoginCode(reqCtx context.Context, phone, code string, own bool) {
	brand := s.app.Brand(reqCtx)
	detached := context.WithoutCancel(reqCtx)
	s.deliveries.Add(1)
	go func() {
		defer s.deliveries.Done()
		scope := s.identityMailScope(detached)
		if !own {
			su, err := s.personOnPhone(detached, phone)
			switch {
			case err != nil:
				s.app.log.Error("sign-in code not sent: could not look the phone up", "error", err)
				return
			case su == nil && !s.app.signupOpen(detached) && !s.invitedPhone(detached, phone):
				return
			case su != nil && su.disabled:
				return
			}
		}
		note := loginCodeNote(phone)
		if !s.app.notifier.carries(scope, note) {
			s.app.log.Warn("sign-in code not sent: nothing on the install's channel carries it; set up SMS for the install",
				"to", maskAddress(phone))
			return
		}
		note.Language = s.app.cfg.DefaultLanguage
		note.From = s.app.identityFrom(note.Channel)
		note.Data = map[string]string{
			"code": code, "store_name": brand.Name,
			"expires_in_minutes": strconv.Itoa(int(phoneCodeTTL / time.Minute)),
		}
		note.Secrets = []string{"code"}
		ctx, cancel := context.WithTimeout(scope, phoneCodeSendWait)
		defer cancel()
		if err := s.app.notifier.send(ctx, note); err != nil {
			// A provider's refusal can quote the number it refused.
			reason := strings.ReplaceAll(err.Error(), phone, maskAddress(phone))
			if !isEmailAddress(phone) {
				reason = MaskPhoneIn(reason, phone)
			}
			s.app.log.Error("sign-in code could not be sent", "to", maskAddress(phone), "error", reason)
		}
	}()
}

// invitedPhone is whether a store is waiting on phone (D205), which lets a new
// person in where sign-up is closed: otherwise they could never answer.
func (s *Superusers) invitedPhone(ctx context.Context, phone string) bool {
	var yes bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM store_phone_invitations WHERE phone = $1 AND expires_at > now())`, phone).Scan(&yes)
	return err == nil && yes
}

// personOnPhone is the person who signs in with phone, or nil.
func (s *Superusers) personOnPhone(ctx context.Context, phone string) (*Superuser, error) {
	su, err := s.scan(ctx, s.db.QueryRowContext(ctx,
		`SELECT `+superuserColumns+` FROM superusers WHERE phone = $1`, phone))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, Internalf(err, "look up phone")
	}
	return su, nil
}

// VerifyPhoneCode spends one try at phone's code. Right, it signs in the person
// on that phone, or makes one where sign-up is open; the bool says it made one.
// A number nobody can sign in with is told what a wrong code is told.
func (s *Superusers) VerifyPhoneCode(ctx context.Context, phone, code string) (*Superuser, *Session, bool, error) {
	phone, err := NormalizePhone(phone)
	if err != nil {
		return nil, nil, false, err
	}
	if s.app.cfg.Demo {
		return nil, nil, false, phoneUnavailable()
	}
	right, err := s.spendLoginCode(ctx, phone, code, phoneCodeHash)
	if err != nil {
		return nil, nil, false, err
	}
	if !right {
		return nil, nil, false, wrongPhoneCode()
	}
	su, err := s.personOnPhone(ctx, phone)
	if err != nil {
		return nil, nil, false, err
	}
	created := false
	if su == nil {
		if !s.app.signupOpen(ctx) && !s.invitedPhone(ctx, phone) {
			return nil, nil, false, wrongPhoneCode()
		}
		if su, err = s.signUpPhone(ctx, phone); err != nil {
			return nil, nil, false, err
		}
		created = true
	}
	if !su.phoneProved && !su.disabled {
		if err := s.claimPhoneAccount(ctx, su); err != nil {
			return nil, nil, false, err
		}
	}
	sess, err := s.issue(ctx, su.ID)
	if errors.Is(err, errPersonDisabled) {
		return nil, nil, false, wrongPhoneCode()
	}
	if err != nil {
		return nil, nil, false, err
	}
	return su, sess, created, nil
}

// claimPhoneAccount is the first code to prove a number given at sign-up, where
// anybody may type anybody's number (D191). Whoever typed it may be a stranger
// who means to share the account with its real owner, so their password, their
// sessions and any email they put on it end here: a reset to that email would
// let them straight back in. The honest person who typed their own number sets a
// new password through the reset, which texts them, and adds the email again.
func (s *Superusers) claimPhoneAccount(ctx context.Context, su *Superuser) error {
	return InTx(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE superusers SET phone_proved_at = now(), password_hash = '', email = NULL, updated_at = now()
			WHERE id = $1 AND phone_proved_at IS NULL`, su.ID)
		if err != nil {
			return Internalf(err, "prove phone")
		}
		if n, err := res.RowsAffected(); err != nil {
			return Internalf(err, "prove phone")
		} else if n == 0 {
			return nil
		}
		for _, q := range []string{`DELETE FROM superuser_sessions WHERE superuser_id = $1`,
			`DELETE FROM superuser_password_resets WHERE superuser_id = $1`} {
			if _, err := tx.ExecContext(ctx, q, su.ID); err != nil {
				return Internalf(err, "end the account's other ways in")
			}
		}
		su.Email, su.passwordHash, su.phoneProved = "", "", true
		return nil
	})
}

// signUpPhone makes a person with no store, no email and no password: an empty
// hash, which verifyPassword refuses whatever is typed (D193). As in SignUp,
// they join no store, and are recorded as arriving through this brand.
func (s *Superusers) signUpPhone(ctx context.Context, phone string) (*Superuser, error) {
	su, err := scanSuperuser(s.db.QueryRowContext(ctx, `
		INSERT INTO superusers (phone, phone_proved_at, password_hash, brand_host) VALUES ($1, now(), '', $2)
		RETURNING `+superuserColumns, phone, s.app.arrivalBrandHost(ctx)))
	if err != nil {
		return nil, Internalf(err, "create superuser")
	}
	su.Rights = []Right{}
	return su, nil
}

// SetOwnPhone makes phone a way into person id's account, once code proves the
// number is theirs. The current password is asked for as UpdateSelf asks for
// it, and before the code is spent: an unlocked laptop must not be enough to
// add a way in. A number that opens another account is not taken from it.
func (s *Superusers) SetOwnPhone(ctx context.Context, id int64, currentPassword, phone, code, keepToken string) (*Superuser, error) {
	phone, err := NormalizePhone(phone)
	if err != nil {
		return nil, err
	}
	if err := s.checkOwnPassword(ctx, id, currentPassword); err != nil {
		return nil, err
	}
	right, err := s.spendLoginCode(ctx, phone, code, ownCodeHash)
	if err != nil {
		return nil, err
	}
	if !right {
		return nil, wrongPhoneCode()
	}
	return s.writeOwnPhone(ctx, id, phone, "Added a phone to sign in with", keepToken)
}

// RemoveOwnPhone is the answer to a lost SIM. It needs the password, and an
// email to go on signing in with.
func (s *Superusers) RemoveOwnPhone(ctx context.Context, id int64, currentPassword, keepToken string) (*Superuser, error) {
	if err := s.checkOwnPassword(ctx, id, currentPassword); err != nil {
		return nil, err
	}
	return s.writeOwnPhone(ctx, id, "", "Removed their phone", keepToken)
}

func (s *Superusers) checkOwnPassword(ctx context.Context, id int64, password string) error {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM superusers WHERE id = $1`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return NotFoundf("superuser %d not found", id)
	}
	if err != nil {
		return Internalf(err, "read superuser")
	}
	// Somebody who signed up with a phone has no password to be asked for, and
	// would otherwise have no way to change how they sign in (D195).
	//
	// ponytail: for them an unlocked laptop is enough to add a way in, since the
	// code proves the new address and nothing proves the person. A code to the
	// address already on the account would close that, but not for a lost SIM,
	// which is the case this is here for.
	if hash != "" && !verifyPassword(hash, password) {
		return Forbiddenf("your current password is not correct")
	}
	return nil
}

// RequestOwnEmailCode emails a code to an address a signed-in person wants on
// their account. It shares the codes and limits a phone's have, keyed by the
// address.
func (s *Superusers) RequestOwnEmailCode(ctx context.Context, email, clientIP string) error {
	email, err := checkedEmail(email)
	if err != nil {
		return err
	}
	if !s.app.notifier.delivers(s.identityMailScope(ctx), ChannelEmail) {
		return &APIError{Status: http.StatusConflict, Code: "email_unavailable", Message: "this install cannot send email yet"}
	}
	code, err := s.storeLoginCode(ctx, email, clientIP, ownCodeHash)
	if err != nil {
		return err
	}
	s.deliverLoginCode(ctx, email, code, true)
	return nil
}

// SetOwnEmail puts email on person id's account, once its code proves the
// address is theirs. They sign in with it by its password: somebody who has none
// chooses one through the reset, which is what brings them back after a lost SIM.
func (s *Superusers) SetOwnEmail(ctx context.Context, id int64, currentPassword, email, code string) (*Superuser, error) {
	email, err := checkedEmail(email)
	if err != nil {
		return nil, err
	}
	if err := s.checkOwnPassword(ctx, id, currentPassword); err != nil {
		return nil, err
	}
	right, err := s.spendLoginCode(ctx, email, code, ownCodeHash)
	if err != nil {
		return nil, err
	}
	if !right {
		return nil, wrongPhoneCode()
	}
	rights, err := s.roles.All(ctx)
	if err != nil {
		return nil, err
	}
	var su *Superuser
	err = InTx(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		su, err = scanSuperuser(tx.QueryRowContext(ctx, `UPDATE superusers SET email = $2, email_proved = $2, updated_at = now()
			WHERE id = $1 RETURNING `+superuserColumns, id, email))
		switch {
		case isUniqueViolation(err):
			return Conflictf("%s already signs in to another account here", email)
		case err != nil:
			return Internalf(err, "set email")
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditSuperuserSelfUpdate, Entity: AuditEntitySuperuser,
			ID: su.ID, Label: su.who(), Summary: "Changed the email on their account",
			After: map[string]any{"email_changed": true},
		})
	})
	if err != nil {
		return nil, err
	}
	su.Rights = rights[su.Role]
	return su, nil
}

func checkedEmail(email string) (string, error) {
	email = normalizeEmail(email)
	if _, err := mail.ParseAddress(email); err != nil {
		return "", Validationf("%q is not a valid email address", email)
	}
	return email, nil
}

// writeOwnPhone also ends everything else open on the account when a number goes
// or is replaced: that is how somebody answers a lost SIM, and a session the SIM
// opened must not outlive it. keepToken is the caller's own session.
func (s *Superusers) writeOwnPhone(ctx context.Context, id int64, phone, summary, keepToken string) (*Superuser, error) {
	rights, err := s.roles.All(ctx)
	if err != nil {
		return nil, err
	}
	var su *Superuser
	err = InTx(ctx, s.db, func(tx *sql.Tx) error {
		var old string
		err := tx.QueryRowContext(ctx, `SELECT coalesce(phone, '') FROM superusers WHERE id = $1 FOR UPDATE`, id).Scan(&old)
		if err != nil {
			return Internalf(err, "read superuser")
		}
		su, err = scanSuperuser(tx.QueryRowContext(ctx, `UPDATE superusers SET phone = NULLIF($2, ''), phone_proved_at = CASE WHEN $2 = '' THEN NULL ELSE now() END, updated_at = now()
			WHERE id = $1 RETURNING `+superuserColumns, id, phone))
		switch {
		case isUniqueViolation(err):
			return Conflictf("%s already signs in to another account here", phone)
		case isCheckViolation(err):
			return Conflictf("this account has no email to sign in with; add one before removing the phone")
		case err != nil:
			return Internalf(err, "set phone")
		}
		if old != "" && old != phone {
			if _, err := tx.ExecContext(ctx, `DELETE FROM superuser_sessions WHERE superuser_id = $1 AND token_hash <> $2`,
				id, hashToken(keepToken)); err != nil {
				return Internalf(err, "end other sessions")
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM superuser_password_resets WHERE superuser_id = $1`, id); err != nil {
				return Internalf(err, "revoke password reset links")
			}
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditSuperuserSelfUpdate, Entity: AuditEntitySuperuser,
			ID: su.ID, Label: su.who(), Summary: summary,
			After: map[string]any{"phone_changed": true},
		})
	})
	if err != nil {
		return nil, err
	}
	su.Rights = rights[su.Role]
	return su, nil
}

// ProvedPhoneOwner is who proved phone, in E.164, with a code, or 0. Somebody shut
// out of the install is nobody. It says nothing about any store: Get, bound to a
// store, says whether they belong there and what they may do.
func (s *Superusers) ProvedPhoneOwner(ctx context.Context, phone string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM superusers
		WHERE phone = $1 AND phone_proved_at IS NOT NULL AND disabled_at IS NULL`, phone).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, Internalf(err, "find a phone's owner")
	}
	return id, nil
}

// SetDisabled shuts a person out of the whole install, or lets them back. It
// names a person rather than a store's member, so it is the command line's and
// not any store's: one store must not lock somebody out of another.
func (s *Superusers) SetDisabled(ctx context.Context, emailOrPhone string, disabled bool) error {
	key := normalizeEmail(emailOrPhone)
	if p, err := NormalizePhone(emailOrPhone); err == nil {
		key = p
	}
	return InTx(ctx, s.db, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `
			UPDATE superusers SET updated_at = now(),
			       disabled_at = CASE WHEN $2 THEN coalesce(disabled_at, now()) END
			WHERE email = $1 OR phone = $1 RETURNING id`, key, disabled).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("nobody signs in with %q", emailOrPhone)
		}
		if err != nil {
			return Internalf(err, "set disabled")
		}
		if !disabled {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM superuser_sessions WHERE superuser_id = $1`, id); err != nil {
			return Internalf(err, "revoke sessions")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM superuser_password_resets WHERE superuser_id = $1`, id); err != nil {
			return Internalf(err, "revoke password reset links")
		}
		return nil
	})
}

// ChangePhone is support moving a person to a new number after a lost SIM
// (D197), on the word of whoever checked who they are, so the number counts as
// proved. It is the command line's, like SetDisabled. Whoever holds the old SIM
// may be signed in, so their sessions and reset links end here.
func (s *Superusers) ChangePhone(ctx context.Context, emailOrPhone, phone string) error {
	phone, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	key := normalizeEmail(emailOrPhone)
	if p, err := NormalizePhone(emailOrPhone); err == nil {
		key = p
	}
	return InTx(ctx, s.db, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `
			UPDATE superusers SET phone = $2, phone_proved_at = now(), updated_at = now()
			WHERE email = $1 OR phone = $1 RETURNING id`, key, phone).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return NotFoundf("nobody signs in with %q", emailOrPhone)
		case isUniqueViolation(err):
			return Conflictf("%s already signs in to another account here", phone)
		case err != nil:
			return Internalf(err, "change phone")
		}
		for _, q := range []string{`DELETE FROM superuser_sessions WHERE superuser_id = $1`,
			`DELETE FROM superuser_password_resets WHERE superuser_id = $1`} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return Internalf(err, "end the old number's ways in")
			}
		}
		return nil
	})
}

// isCheckViolation reports a 23514: a CHECK constraint refused the row.
func isCheckViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23514")
}

func (a *App) handleRequestPhoneCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.superusers.RequestPhoneCode(r.Context(), in.Phone, clientIP(r)); err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusAccepted, map[string]int{"expires_in_minutes": int(phoneCodeTTL / time.Minute)})
}

func (a *App) handleAuthWithPhone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	su, sess, created, err := a.superusers.VerifyPhoneCode(r.Context(), in.Phone, in.Code)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		a.log.Info("superuser created", "id", su.ID, "via", "phone")
		a.trackSignUp(su, "phone")
		status = http.StatusCreated
	}
	a.respondSignedIn(w, r, status, su, sess)
}

// personCaller is the person a /me route acts for. A token or an API key has
// no account to change.
func personCaller(w http.ResponseWriter, r *http.Request) *Superuser {
	su := SuperuserFrom(r.Context())
	if su == nil || su.ID == 0 {
		RespondError(w, r, Forbiddenf("this route is for a signed-in operator; a token or a key has no account"))
		return nil
	}
	return su
}

func (a *App) handleRequestMyPhoneCode(w http.ResponseWriter, r *http.Request) {
	su := personCaller(w, r)
	if su == nil {
		return
	}
	var in struct {
		Phone string `json:"phone"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.superusers.RequestOwnPhoneCode(r.Context(), in.Phone, clientIP(r)); err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusAccepted, map[string]int{"expires_in_minutes": int(phoneCodeTTL / time.Minute)})
}

func (a *App) handleSetMyPhone(w http.ResponseWriter, r *http.Request) {
	su := personCaller(w, r)
	if su == nil {
		return
	}
	var in struct {
		CurrentPassword string `json:"current_password"`
		Phone           string `json:"phone"`
		Code            string `json:"code"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	keep, _ := bearerToken(r)
	updated, err := a.superusers.SetOwnPhone(r.Context(), su.ID, in.CurrentPassword, in.Phone, in.Code, keep)
	respondOr(w, r, updated, err)
}

func (a *App) handleRequestMyEmailCode(w http.ResponseWriter, r *http.Request) {
	su := personCaller(w, r)
	if su == nil {
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.superusers.RequestOwnEmailCode(r.Context(), in.Email, clientIP(r)); err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusAccepted, map[string]int{"expires_in_minutes": int(phoneCodeTTL / time.Minute)})
}

func (a *App) handleSetMyEmail(w http.ResponseWriter, r *http.Request) {
	su := personCaller(w, r)
	if su == nil {
		return
	}
	var in struct {
		CurrentPassword string `json:"current_password"`
		Email           string `json:"email"`
		Code            string `json:"code"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	updated, err := a.superusers.SetOwnEmail(r.Context(), su.ID, in.CurrentPassword, in.Email, in.Code)
	respondOr(w, r, updated, err)
}

func (a *App) handleRemoveMyPhone(w http.ResponseWriter, r *http.Request) {
	su := personCaller(w, r)
	if su == nil {
		return
	}
	var in struct {
		CurrentPassword string `json:"current_password"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	keep, _ := bearerToken(r)
	updated, err := a.superusers.RemoveOwnPhone(r.Context(), su.ID, in.CurrentPassword, keep)
	respondOr(w, r, updated, err)
}
