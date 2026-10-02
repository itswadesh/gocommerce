// Package identity adds shopper accounts to a gocommerce store.
//
// It is the module D22 anticipated: core carries no customer concept and never
// will, so accounts live here, in their own tables, reached through their own
// routes. Guest checkout is untouched by its presence — a shopper with an
// account still checks out with a cart token and an email; what an account
// adds is a place for that shopper's saved addresses and a way to find their
// orders again without keeping every access token.
//
//	app, err := gocommerce.New(cfg, identity.New(identity.Config{
//		ResetURL: "https://shop.example.com/auth/reset-password?token={token}",
//	}))
//
// Everything mounts under /x/identity/. A session is a bearer token, issued
// by register, login and password reset and sent as
// "Authorization: Bearer <token>" — the same shape as a superuser session,
// because a client that already speaks that has nothing new to learn.
//
// Order history is by claim, not by email. An order is linked to an account
// when the client presents the order's own access token, which only whoever
// placed the order holds. Matching on email alone would let anyone register an
// address they do not own and read that person's purchases.
//
// An account's address is unconfirmed until a link mailed to it comes back.
// Signing up sends nothing: the storefront asks, with
// POST /x/identity/me/email-verification, when it wants the address proved —
// straight after signup, or the first time the shopper needs it. The link
// confirms the address it was sent to and no other, so changing the address
// withdraws the confirmation, and a link still in the old inbox cannot confirm
// the new one. Completing a password reset confirms the address too, because
// the reset link reached it.
//
// The confirmation exists for prices (D66). A customer group is a list of
// email addresses, and a session proves a password, not a mailbox: anybody can
// sign up as a dealer's address before the dealer does. So
// POST /x/identity/me/carts, which says a basket is the signed-in shopper's and
// prices it as their groups, is refused until the address is confirmed.
package identity

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	// PBKDF2-HMAC-SHA256, the same construction superusers use and for the
	// same reason: it is in the standard library, and the engine's claim to
	// one production dependency has to hold for modules too.
	pbkdf2SaltBytes = 16
	pbkdf2KeyBytes  = 32
	pbkdf2Scheme    = "pbkdf2-sha256"

	tokenBytes = 32

	// MinPasswordLength is the shortest password a shopper may set.
	MinPasswordLength = 8

	// EventPasswordReset is the notification sent when a shopper asks to
	// reset their password. It reaches the store's email notifier with
	// customer_email, customer_name, reset_token, expires_in_minutes and,
	// when Config.ResetURL is set, reset_url.
	EventPasswordReset = "identity.password_reset"

	// EventEmailVerification is the notification sent when a shopper asks to
	// confirm their address. It reaches the store's email notifier with
	// customer_email, customer_name, verify_token, expires_in_minutes and,
	// when Config.VerifyURL is set, verify_url.
	EventEmailVerification = "identity.email_verification"

	defaultSessionTTL = 30 * 24 * time.Hour
	defaultResetTTL   = time.Hour
	// A day, not a reset's hour: nothing is at stake while the link waits, and
	// a confirmation email is the one a shopper leaves until tomorrow.
	defaultVerifyTTL = 24 * time.Hour

	// verifyCooldown is how soon an account may ask for another confirmation
	// email. The route sends mail to whatever address the account holds, so
	// without it a script could fill somebody's inbox.
	verifyCooldown = time.Minute
)

// pbkdf2Iterations is a var so tests can wind it down; the stored hash
// records the count it was made with, so lowering it never breaks a hash
// that already exists.
var pbkdf2Iterations = 600_000

// Config configures the module.
type Config struct {
	// SessionTTL is how long a sign-in lasts without a refresh. Defaults to
	// 30 days — a shop, not a bank.
	SessionTTL time.Duration
	// ResetTTL is how long a password-reset token stays valid. Defaults to an
	// hour.
	ResetTTL time.Duration
	// ResetURL is the storefront page a reset email links to, with "{token}"
	// where the token goes, e.g. "https://shop.example.com/auth/reset-password?token={token}".
	// It is configured here rather than taken from the request on purpose: a
	// URL a client can choose is a URL a phisher can choose, and the email
	// would carry the store's name over it. Leave it empty and the
	// notification carries reset_token alone for the template to place.
	ResetURL string
	// VerifyTTL is how long an email-confirmation token stays valid. Defaults
	// to 24 hours.
	VerifyTTL time.Duration
	// VerifyURL is the storefront page a confirmation email links to, with
	// "{token}" where the token goes, e.g.
	// "https://shop.example.com/auth/confirm-email?token={token}". Configured
	// here for ResetURL's reason: a link a client could choose is a link a
	// phisher could choose. Leave it empty and the notification carries
	// verify_token alone.
	VerifyURL string
	// Notifier overrides how the module's emails — password reset and address
	// confirmation — are delivered. Leave it nil and the module hands the
	// message to the engine's own notifiers — the same SendGrid the order
	// emails go through — falling back to the log when this build of the
	// engine has no App.Notify to offer.
	Notifier gocommerce.Notifier
}

// Customer is a shopper with an account. The password hash is never
// serialized: the struct is what handlers return, so the missing json tag on
// the hash is load-bearing.
type Customer struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	// EmailVerified says the shopper has proved they read mail at Email. It is
	// what a customer group's prices wait on (D66); signing up does not set it.
	EmailVerified bool      `json:"email_verified"`
	Name          string    `json:"name"`
	Phone         string    `json:"phone"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`

	passwordHash string
}

// Address is a saved delivery address. The fields mirror gocommerce.Address
// so one maps onto the other without loss at checkout; Label, Name and Phone
// are the extras an address book needs and a snapshot does not.
type Address struct {
	ID         int64     `json:"id"`
	Label      string    `json:"label"`
	Name       string    `json:"name"`
	Phone      string    `json:"phone"`
	Line1      string    `json:"line1"`
	Line2      string    `json:"line2"`
	City       string    `json:"city"`
	State      string    `json:"state"`
	PostalCode string    `json:"postal_code"`
	Country    string    `json:"country"`
	IsDefault  bool      `json:"is_default"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// AddressInput is what a client sends to create or, with every field
// optional, patch an address.
type AddressInput struct {
	Label      *string `json:"label"`
	Name       *string `json:"name"`
	Phone      *string `json:"phone"`
	Line1      *string `json:"line1"`
	Line2      *string `json:"line2"`
	City       *string `json:"city"`
	State      *string `json:"state"`
	PostalCode *string `json:"postal_code"`
	Country    *string `json:"country"`
	IsDefault  *bool   `json:"is_default"`
}

// Session is a signed-in shopper's credential.
type Session struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AuthResponse is what every sign-in shaped call returns: the credential and
// the account it belongs to.
type AuthResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Record    *Customer `json:"record"`
}

// Module is the identity module.
type Module struct {
	cfg      Config
	app      *gocommerce.App
	db       *sql.DB
	throttle *loginThrottle
}

// New constructs the module.
func New(cfg Config) *Module {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = defaultSessionTTL
	}
	if cfg.ResetTTL <= 0 {
		cfg.ResetTTL = defaultResetTTL
	}
	if cfg.VerifyTTL <= 0 {
		cfg.VerifyTTL = defaultVerifyTTL
	}
	return &Module{cfg: cfg, throttle: newLoginThrottle()}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "identity" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_accounts",
		SQL: `
			CREATE TABLE identity_customers (
			    id            bigserial   PRIMARY KEY,
			    -- Stored lower-cased, so the unique index is the case-insensitive one.
			    email         text        NOT NULL UNIQUE,
			    password_hash text        NOT NULL,
			    name          text        NOT NULL DEFAULT '',
			    phone         text        NOT NULL DEFAULT '',
			    created_at    timestamptz NOT NULL DEFAULT now(),
			    updated_at    timestamptz NOT NULL DEFAULT now()
			);

			CREATE TABLE identity_sessions (
			    token_hash  text        PRIMARY KEY,
			    customer_id bigint      NOT NULL REFERENCES identity_customers(id) ON DELETE CASCADE,
			    expires_at  timestamptz NOT NULL,
			    created_at  timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX identity_sessions_customer_idx ON identity_sessions (customer_id);

			CREATE TABLE identity_password_resets (
			    token_hash  text        PRIMARY KEY,
			    customer_id bigint      NOT NULL REFERENCES identity_customers(id) ON DELETE CASCADE,
			    expires_at  timestamptz NOT NULL,
			    created_at  timestamptz NOT NULL DEFAULT now()
			);

			CREATE TABLE identity_addresses (
			    id          bigserial   PRIMARY KEY,
			    customer_id bigint      NOT NULL REFERENCES identity_customers(id) ON DELETE CASCADE,
			    label       text        NOT NULL DEFAULT '',
			    name        text        NOT NULL DEFAULT '',
			    phone       text        NOT NULL DEFAULT '',
			    line1       text        NOT NULL,
			    line2       text        NOT NULL DEFAULT '',
			    city        text        NOT NULL,
			    state       text        NOT NULL DEFAULT '',
			    postal_code text        NOT NULL,
			    country     text        NOT NULL,
			    is_default  boolean     NOT NULL DEFAULT false,
			    created_at  timestamptz NOT NULL DEFAULT now(),
			    updated_at  timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX identity_addresses_customer_idx ON identity_addresses (customer_id, id);
			-- At most one default per account, enforced where it cannot drift.
			CREATE UNIQUE INDEX identity_addresses_default_idx ON identity_addresses (customer_id) WHERE is_default;

			-- Which orders belong to which account. No foreign key onto orders:
			-- a module's table must never be able to refuse a core delete, and
			-- an order that has gone is simply skipped when the history is read.
			CREATE TABLE identity_orders (
			    customer_id  bigint      NOT NULL REFERENCES identity_customers(id) ON DELETE CASCADE,
			    order_id     bigint      NOT NULL,
			    order_number text        NOT NULL,
			    created_at   timestamptz NOT NULL DEFAULT now(),
			    PRIMARY KEY (customer_id, order_id)
			);
			-- An order was placed by one person; the first account to prove it
			-- keeps it.
			CREATE UNIQUE INDEX identity_orders_order_idx ON identity_orders (order_id);`,
	}, {
		// Group prices are read from a proven address (D66), and until now an
		// account's address was whatever its owner typed. Every account that
		// exists starts unconfirmed: nothing ever proved those addresses either.
		ID: "0002_email_verification",
		SQL: `
			-- NULL is unconfirmed.
			ALTER TABLE identity_customers ADD COLUMN email_verified_at timestamptz;
			-- When the account last asked for a confirmation email, for the
			-- cooldown. Kept on the account rather than read off the tokens:
			-- changing the address deletes the tokens, and a cooldown that an
			-- address change resets is one a script can step around by
			-- flipping the address back and forth.
			ALTER TABLE identity_customers ADD COLUMN email_verification_sent_at timestamptz;

			-- The address a reset was mailed to, so completing it can confirm
			-- that address — and only while it is still the account's. NULL on a
			-- reset issued before this column, which confirms nothing.
			ALTER TABLE identity_password_resets ADD COLUMN email text;

			-- A confirmation token is bound to the address it was mailed to: a
			-- link still in the old inbox must not confirm a new address.
			CREATE TABLE identity_email_verifications (
			    token_hash  text        PRIMARY KEY,
			    customer_id bigint      NOT NULL REFERENCES identity_customers(id) ON DELETE CASCADE,
			    email       text        NOT NULL,
			    expires_at  timestamptz NOT NULL,
			    created_at  timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX identity_email_verifications_customer_idx ON identity_email_verifications (customer_id);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.registerRights(app)
	m.app = app
	m.db = app.DB()
	m.mountRoutes(app)
	// The reset email's default wording, so the operator can reword it from
	// the panel like the order emails. The absence of reset_url is
	// meaningful: without a ResetURL the code stands in for the link.
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: EventPasswordReset, Title: "Password reset",
		Description: "To a shopper who asked to reset their password.",
		Variables:   []string{"customer_name", "customer_email", "reset_url", "reset_token", "expires_in_minutes"},
		Subject:     "Reset your password",
		Body: `Hello {{.customer_name}},

Somebody asked to reset the password for {{.customer_email}}. If that was you,
{{if .reset_url}}open this link within {{.expires_in_minutes}} minutes:

{{.reset_url}}{{else}}use this code within {{.expires_in_minutes}} minutes:

{{.reset_token}}{{end}}

If it was not you, nothing has changed and you can ignore this message.`,
	})
	// The same arrangement for the confirmation email: without a VerifyURL the
	// code stands in for the link.
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: EventEmailVerification, Title: "Email confirmation",
		Description: "To a shopper who asked to confirm the address on their account.",
		Variables:   []string{"customer_name", "customer_email", "verify_url", "verify_token", "expires_in_minutes"},
		Subject:     "Confirm your email address",
		Body: `Hello {{.customer_name}},

Please confirm that {{.customer_email}} is your email address. {{if .verify_url}}Open this link within {{.expires_in_minutes}} minutes:

{{.verify_url}}{{else}}Use this code within {{.expires_in_minutes}} minutes:

{{.verify_token}}{{end}}

If it was not you, ignore this message and the address stays unconfirmed.`,
	})
	return nil
}

// ---------------------------------------------------------------- passwords

// hashPassword returns a self-describing hash, pbkdf2-sha256$<iter>$<salt>$<key>,
// so the cost can be raised later without touching a row that already exists.
func hashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyBytes)
	if err != nil {
		return "", fmt.Errorf("derive password key: %w", err)
	}
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", pbkdf2Scheme, pbkdf2Iterations,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// verifyPassword compares in constant time; a malformed hash is a mismatch,
// never a bypass.
func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != pbkdf2Scheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := b64.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyVerify burns what a real verification costs, so "no such account" and
// "wrong password" take the same time and the login form is not an
// enumeration oracle. The cost is copied from a hash that exists, because the
// stored count and pbkdf2Iterations diverge the moment the cost is raised.
func (m *Module) dummyVerify(ctx context.Context, password string) {
	var encoded string
	if err := m.db.QueryRowContext(ctx,
		`SELECT password_hash FROM identity_customers LIMIT 1`).Scan(&encoded); err == nil {
		verifyPassword(encoded, password)
		return
	}
	salt := make([]byte, pbkdf2SaltBytes)
	_, _ = pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyBytes)
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validateEmail(email string) error {
	if email == "" {
		return gocommerce.Validationf("email is required")
	}
	if _, err := mail.ParseAddress(email); err != nil {
		return gocommerce.Validationf("%q is not a valid email address", email)
	}
	return nil
}

func validatePassword(password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return gocommerce.Validationf("password must be at least %d characters", MinPasswordLength)
	}
	return nil
}

// invalidCredentials is the one answer every failed sign-in gets.
var invalidCredentials = &gocommerce.APIError{
	Status:  http.StatusBadRequest,
	Code:    "invalid_credentials",
	Message: "invalid login credentials",
}

// ----------------------------------------------------------------- accounts

// customerColumns is in scanCustomer's order. Resolve spells it out again with
// a table alias, so a column added here is added there too.
const customerColumns = `id, email, password_hash, name, phone, email_verified_at IS NOT NULL, created_at, updated_at`

func scanCustomer(row interface{ Scan(...any) error }) (*Customer, error) {
	var c Customer
	if err := row.Scan(&c.ID, &c.Email, &c.passwordHash, &c.Name, &c.Phone, &c.EmailVerified,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func (m *Module) customerByID(ctx context.Context, id int64) (*Customer, error) {
	c, err := scanCustomer(m.db.QueryRowContext(ctx,
		`SELECT `+customerColumns+` FROM identity_customers WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("account %d does not exist", id)
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "look up account")
	}
	return c, nil
}

func (m *Module) customerByEmail(ctx context.Context, email string) (*Customer, error) {
	return scanCustomer(m.db.QueryRowContext(ctx,
		`SELECT `+customerColumns+` FROM identity_customers WHERE email = $1`, normalizeEmail(email)))
}

// CustomerByID returns one account, for another module that holds its id.
func (m *Module) CustomerByID(ctx context.Context, id int64) (*Customer, error) {
	return m.customerByID(ctx, id)
}

// CustomerByEmail returns the account registered at an address, matched
// exactly after case folding — List's search is a substring match, which is
// the wrong tool for "is this person a shopper here".
//
// Finding an account says only that somebody signed up with the address, not
// that they read its mail: check EmailVerified before treating the account as
// the person the address belongs to.
func (m *Module) CustomerByEmail(ctx context.Context, email string) (*Customer, error) {
	c, err := m.customerByEmail(ctx, email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no account is registered for %s", normalizeEmail(email))
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "look up account")
	}
	return c, nil
}

func isUniqueViolation(err error, index string) bool {
	return err != nil && strings.Contains(err.Error(), index)
}

// Signup creates an account and signs it in. The address starts unconfirmed
// and no email goes out: when to ask is the storefront's call, through
// RequestVerification, not something every signup should trigger.
func (m *Module) Signup(ctx context.Context, email, password, name, phone string) (*Customer, *Session, error) {
	email = normalizeEmail(email)
	if err := validateEmail(email); err != nil {
		return nil, nil, err
	}
	if err := validatePassword(password); err != nil {
		return nil, nil, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, nil, gocommerce.Internalf(err, "hash password")
	}
	row := m.db.QueryRowContext(ctx, `
		INSERT INTO identity_customers (email, password_hash, name, phone)
		VALUES ($1, $2, $3, $4)
		RETURNING `+customerColumns,
		email, hash, strings.TrimSpace(name), strings.TrimSpace(phone))
	c, err := scanCustomer(row)
	if isUniqueViolation(err, "identity_customers_email_key") {
		return nil, nil, gocommerce.Conflictf("an account already exists for %s", email)
	}
	if err != nil {
		return nil, nil, gocommerce.Internalf(err, "create account")
	}
	sess, err := m.issue(ctx, c.ID)
	if err != nil {
		return nil, nil, err
	}
	return c, sess, nil
}

// Authenticate verifies an email and password and issues a session. Every
// failure is the same error, and the timing says no more than the message.
func (m *Module) Authenticate(ctx context.Context, email, password, clientIP string) (*Customer, *Session, error) {
	email = normalizeEmail(email)
	if retryAfter, ok := m.throttle.blocked(email, clientIP); ok {
		return nil, nil, &gocommerce.APIError{
			Status:  http.StatusTooManyRequests,
			Code:    "too_many_attempts",
			Message: fmt.Sprintf("too many failed attempts; try again in %s", retryAfter.Round(time.Second)),
		}
	}
	c, err := m.customerByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			m.dummyVerify(ctx, password)
			m.throttle.fail(email, clientIP)
			return nil, nil, invalidCredentials
		}
		return nil, nil, gocommerce.Internalf(err, "look up account")
	}
	if !verifyPassword(c.passwordHash, password) {
		m.throttle.fail(email, clientIP)
		return nil, nil, invalidCredentials
	}
	m.throttle.succeed(email, clientIP)
	sess, err := m.issue(ctx, c.ID)
	if err != nil {
		return nil, nil, err
	}
	return c, sess, nil
}

// Update changes the account's own details. An email change keeps every
// session — the credential is the token, not the address — but not the
// confirmation: the new address has proved nothing, and the confirmation links
// already mailed went to the old one.
func (m *Module) Update(ctx context.Context, id int64, email, name, phone *string) (*Customer, error) {
	var newEmail string
	if email != nil {
		newEmail = normalizeEmail(*email)
		if err := validateEmail(newEmail); err != nil {
			return nil, err
		}
	}
	var out *Customer
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		// Locked, so the comparison below is with the address the UPDATE
		// replaces and not one a concurrent request has already moved past.
		current, err := scanCustomer(tx.QueryRowContext(ctx,
			`SELECT `+customerColumns+` FROM identity_customers WHERE id = $1 FOR UPDATE`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.NotFoundf("account %d does not exist", id)
		}
		if err != nil {
			return gocommerce.Internalf(err, "look up account")
		}

		sets, args := []string{}, []any{}
		add := func(col string, v any) {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
		}
		// Retyping the same address in another case is not a move, and must
		// not cost the shopper a confirmation they already made.
		moved := email != nil && newEmail != current.Email
		if moved {
			add("email", newEmail)
			sets = append(sets, "email_verified_at = NULL")
		}
		if name != nil {
			add("name", strings.TrimSpace(*name))
		}
		if phone != nil {
			add("phone", strings.TrimSpace(*phone))
		}
		if len(sets) == 0 {
			out = current
			return nil
		}
		sets = append(sets, "updated_at = now()")
		args = append(args, id)
		c, err := scanCustomer(tx.QueryRowContext(ctx,
			`UPDATE identity_customers SET `+strings.Join(sets, ", ")+
				fmt.Sprintf(` WHERE id = $%d RETURNING `, len(args))+customerColumns, args...))
		if isUniqueViolation(err, "identity_customers_email_key") {
			return gocommerce.Conflictf("an account already exists for %s", newEmail)
		}
		if err != nil {
			return gocommerce.Internalf(err, "update account")
		}
		if moved {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM identity_email_verifications WHERE customer_id = $1`, id); err != nil {
				return gocommerce.Internalf(err, "withdraw confirmation links")
			}
		}
		out = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ChangePassword sets a new password after checking the current one, then
// revokes every session and issues a fresh one to the caller. The property is
// that no session predating the change survives — not that the shopper has to
// sign in again to finish what they started.
func (m *Module) ChangePassword(ctx context.Context, id int64, current, password string) (*Customer, *Session, error) {
	c, err := m.customerByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if !verifyPassword(c.passwordHash, current) {
		return nil, nil, invalidCredentials
	}
	if err := m.setPassword(ctx, id, password, ""); err != nil {
		return nil, nil, err
	}
	sess, err := m.issue(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return c, sess, nil
}

// setPassword stores a new hash and, in the same transaction, ends every
// session and every outstanding reset for the account.
//
// provenEmail is an address the change itself proved the shopper reads — the
// one a reset link was mailed to — or empty. It is confirmed in the same
// transaction if it is still the account's address, so a reset that completes
// never leaves the password changed and the confirmation lost.
func (m *Module) setPassword(ctx context.Context, id int64, password, provenEmail string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return gocommerce.Internalf(err, "hash password")
	}
	return gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE identity_customers SET password_hash = $1, updated_at = now() WHERE id = $2`, hash, id)
		if err != nil {
			return gocommerce.Internalf(err, "set password")
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return gocommerce.NotFoundf("account %d does not exist", id)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM identity_sessions WHERE customer_id = $1`, id); err != nil {
			return gocommerce.Internalf(err, "revoke sessions")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM identity_password_resets WHERE customer_id = $1`, id); err != nil {
			return gocommerce.Internalf(err, "clear password resets")
		}
		if provenEmail != "" {
			// No row is the account having moved to another address since the
			// link went out: the reset still counts, the confirmation does not.
			if _, err := markVerified(ctx, tx, id, provenEmail); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return gocommerce.Internalf(err, "confirm address")
			}
		}
		return nil
	})
}

// Delete removes an account and, through the cascades, its sessions,
// addresses and order links. The orders themselves are core's and stay.
func (m *Module) Delete(ctx context.Context, id int64) error {
	res, err := m.db.ExecContext(ctx, `DELETE FROM identity_customers WHERE id = $1`, id)
	if err != nil {
		return gocommerce.Internalf(err, "delete account")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return gocommerce.NotFoundf("account %d does not exist", id)
	}
	return nil
}

// CustomerQuery filters the admin listing.
type CustomerQuery struct {
	Search        string
	Limit, Offset int
}

// List returns a page of accounts for the admin panel.
func (m *Module) List(ctx context.Context, q CustomerQuery) ([]*Customer, int, error) {
	where, args := []string{"1 = 1"}, []any{}
	if needle := strings.ToLower(strings.TrimSpace(q.Search)); needle != "" {
		args = append(args, "%"+needle+"%")
		where = append(where, fmt.Sprintf("(email LIKE $%d OR lower(name) LIKE $%d)", len(args), len(args)))
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM identity_customers WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, gocommerce.Internalf(err, "count accounts")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = gocommerce.DefaultLimit
	}
	args = append(args, limit, q.Offset)
	rows, err := m.db.QueryContext(ctx,
		`SELECT `+customerColumns+` FROM identity_customers WHERE `+clause+
			fmt.Sprintf(" ORDER BY id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, gocommerce.Internalf(err, "list accounts")
	}
	defer rows.Close()
	out := []*Customer{}
	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return nil, 0, gocommerce.Internalf(err, "scan account")
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, gocommerce.Internalf(err, "list accounts")
	}
	return out, total, nil
}

// ----------------------------------------------------------------- sessions

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

func newToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (m *Module) issue(ctx context.Context, customerID int64) (*Session, error) {
	token, err := newToken()
	if err != nil {
		return nil, gocommerce.Internalf(err, "issue session")
	}
	expires := time.Now().Add(m.cfg.SessionTTL)
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO identity_sessions (token_hash, customer_id, expires_at)
		VALUES ($1, $2, $3)`, hashToken(token), customerID, expires); err != nil {
		return nil, gocommerce.Internalf(err, "store session")
	}
	// Opportunistic sweep; a janitor for this table would be more machinery
	// than the problem deserves.
	_, _ = m.db.ExecContext(ctx, `DELETE FROM identity_sessions WHERE expires_at < now()`)
	_, _ = m.db.ExecContext(ctx, `DELETE FROM identity_password_resets WHERE expires_at < now()`)
	_, _ = m.db.ExecContext(ctx, `DELETE FROM identity_email_verifications WHERE expires_at < now()`)
	return &Session{Token: token, ExpiresAt: expires}, nil
}

// Resolve returns the account a session token belongs to, or false when the
// token is unknown or expired.
func (m *Module) Resolve(ctx context.Context, token string) (*Customer, bool) {
	if token == "" {
		return nil, false
	}
	c, err := scanCustomer(m.db.QueryRowContext(ctx, `
		SELECT c.id, c.email, c.password_hash, c.name, c.phone, c.email_verified_at IS NOT NULL,
		       c.created_at, c.updated_at
		FROM identity_sessions s
		JOIN identity_customers c ON c.id = s.customer_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, hashToken(token)))
	if err != nil {
		return nil, false
	}
	return c, true
}

// Touch extends a session and says whose it is. The token is not rotated:
// rotating on every refresh invalidates the credential every other open tab
// holds, and a server-side session gets its safety from the expiry, not from
// churning the secret.
func (m *Module) Touch(ctx context.Context, token string) (*Customer, *Session, bool) {
	c, ok := m.Resolve(ctx, token)
	if !ok {
		return nil, nil, false
	}
	expires := time.Now().Add(m.cfg.SessionTTL)
	if _, err := m.db.ExecContext(ctx,
		`UPDATE identity_sessions SET expires_at = $1 WHERE token_hash = $2`, expires, hashToken(token)); err != nil {
		return nil, nil, false
	}
	return c, &Session{Token: token, ExpiresAt: expires}, true
}

// Revoke ends one session.
func (m *Module) Revoke(ctx context.Context, token string) error {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM identity_sessions WHERE token_hash = $1`, hashToken(token)); err != nil {
		return gocommerce.Internalf(err, "revoke session")
	}
	return nil
}

// ------------------------------------------------------------ password reset

// RequestReset issues a reset token and hands it to the store's email
// notifier. It returns nil whether or not the address has an account: the
// response must not say which, and the email is the only channel that may.
func (m *Module) RequestReset(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	if err := validateEmail(email); err != nil {
		return err
	}
	c, err := m.customerByEmail(ctx, email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return gocommerce.Internalf(err, "look up account")
	}
	token, err := newToken()
	if err != nil {
		return gocommerce.Internalf(err, "issue reset token")
	}
	expires := time.Now().Add(m.cfg.ResetTTL)
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO identity_password_resets (token_hash, customer_id, email, expires_at)
		VALUES ($1, $2, $3, $4)`, hashToken(token), c.ID, c.Email, expires); err != nil {
		return gocommerce.Internalf(err, "store reset token")
	}

	data := map[string]string{
		"customer_email":     c.Email,
		"customer_name":      c.Name,
		"reset_token":        token,
		"expires_in_minutes": strconv.Itoa(int(m.cfg.ResetTTL / time.Minute)),
	}
	if m.cfg.ResetURL != "" {
		data["reset_url"] = strings.ReplaceAll(m.cfg.ResetURL, "{token}", token)
	}
	if err := m.notify(ctx, gocommerce.Notification{
		Event: EventPasswordReset, Channel: gocommerce.ChannelEmail, To: c.Email, Language: m.language(ctx), Data: data,
	}); err != nil {
		// The token is stored and the shopper can ask again; what must not
		// happen is a 500 that tells them the address exists.
		m.app.Log().Error("password reset email failed", "error", err)
	}
	return nil
}

// notify picks the delivery path: an explicitly configured notifier, else the
// engine's own (App.Notify, checked for rather than assumed so this module
// builds against an engine that predates it), else the log — which is what
// the engine itself does for an order email with no vendor installed.
func (m *Module) notify(ctx context.Context, n gocommerce.Notification) error {
	if m.cfg.Notifier != nil {
		return m.cfg.Notifier.Notify(ctx, n)
	}
	if engine, ok := any(m.app).(gocommerce.Notifier); ok {
		return engine.Notify(ctx, n)
	}
	m.app.Log().Info("notification (no delivery path)", "event", n.Event, "channel", n.Channel, "to", n.To)
	return nil
}

// language is the one the shopper's request asked for, else the store's.
func (m *Module) language(ctx context.Context) string {
	if lang := gocommerce.Language(ctx); lang != "" {
		return lang
	}
	return m.app.Config().DefaultLanguage
}

var invalidResetToken = &gocommerce.APIError{
	Status:  http.StatusBadRequest,
	Code:    "invalid_token",
	Message: "this password reset link is invalid or has expired",
}

// ConfirmReset spends a reset token: sets the password, ends every existing
// session, and signs the shopper in. The link reached the address it was
// mailed to, so that address is confirmed as well while it is still the
// account's — which is also how the owner of an address takes back an account
// somebody else signed up with it.
func (m *Module) ConfirmReset(ctx context.Context, token, password string) (*Customer, *Session, error) {
	if token == "" {
		return nil, nil, invalidResetToken
	}
	if err := validatePassword(password); err != nil {
		return nil, nil, err
	}
	var customerID int64
	var sentTo sql.NullString
	err := m.db.QueryRowContext(ctx, `
		DELETE FROM identity_password_resets
		WHERE token_hash = $1 AND expires_at > now()
		RETURNING customer_id, email`, hashToken(token)).Scan(&customerID, &sentTo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, invalidResetToken
	}
	if err != nil {
		return nil, nil, gocommerce.Internalf(err, "spend reset token")
	}
	if err := m.setPassword(ctx, customerID, password, sentTo.String); err != nil {
		return nil, nil, err
	}
	c, err := m.customerByID(ctx, customerID)
	if err != nil {
		return nil, nil, err
	}
	sess, err := m.issue(ctx, customerID)
	if err != nil {
		return nil, nil, err
	}
	return c, sess, nil
}

// ------------------------------------------------------- email confirmation

// RequestVerification mails the account a link that confirms its current
// address, replacing any link sent before. It refuses an address that is
// already confirmed, and a second request inside verifyCooldown.
func (m *Module) RequestVerification(ctx context.Context, customerID int64) error {
	token, err := newToken()
	if err != nil {
		return gocommerce.Internalf(err, "issue confirmation token")
	}
	var c *Customer
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		// The row lock is what makes the cooldown hold: two requests racing
		// each other would otherwise both find nothing recent and both send.
		got, err := scanCustomer(tx.QueryRowContext(ctx,
			`SELECT `+customerColumns+` FROM identity_customers WHERE id = $1 FOR UPDATE`, customerID))
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.NotFoundf("account %d does not exist", customerID)
		}
		if err != nil {
			return gocommerce.Internalf(err, "look up account")
		}
		if got.EmailVerified {
			return gocommerce.Conflictf("%s is already confirmed", got.Email)
		}
		// Both instants from the database's clock, so a replica whose clock
		// drifts cannot shorten the wait.
		var sentAt sql.NullTime
		var now time.Time
		if err := tx.QueryRowContext(ctx,
			`SELECT email_verification_sent_at, now() FROM identity_customers WHERE id = $1`,
			customerID).Scan(&sentAt, &now); err != nil {
			return gocommerce.Internalf(err, "read confirmation cooldown")
		}
		if sentAt.Valid {
			if wait := sentAt.Time.Add(verifyCooldown).Sub(now); wait > 0 {
				return &gocommerce.APIError{
					Status: http.StatusTooManyRequests,
					Code:   "too_many_attempts",
					Message: fmt.Sprintf("a confirmation email was sent a moment ago; try again in %s",
						(wait + time.Second - 1).Truncate(time.Second)),
				}
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE identity_customers SET email_verification_sent_at = now() WHERE id = $1`, customerID); err != nil {
			return gocommerce.Internalf(err, "start confirmation cooldown")
		}
		// One live link per account: the newest email is the one the shopper
		// will open, and an older one still working is only something to leak.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM identity_email_verifications WHERE customer_id = $1`, customerID); err != nil {
			return gocommerce.Internalf(err, "replace confirmation link")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO identity_email_verifications (token_hash, customer_id, email, expires_at)
			VALUES ($1, $2, $3, $4)`,
			hashToken(token), customerID, got.Email, time.Now().Add(m.cfg.VerifyTTL)); err != nil {
			return gocommerce.Internalf(err, "store confirmation token")
		}
		c = got
		return nil
	})
	if err != nil {
		return err
	}

	data := map[string]string{
		"customer_email":     c.Email,
		"customer_name":      c.Name,
		"verify_token":       token,
		"expires_in_minutes": strconv.Itoa(int(m.cfg.VerifyTTL / time.Minute)),
	}
	if m.cfg.VerifyURL != "" {
		data["verify_url"] = strings.ReplaceAll(m.cfg.VerifyURL, "{token}", token)
	}
	// Sent after the commit, never inside it: a mail vendor that takes four
	// seconds must not hold the account's row for four seconds.
	if err := m.notify(ctx, gocommerce.Notification{
		Event: EventEmailVerification, Channel: gocommerce.ChannelEmail, To: c.Email,
		Language: m.language(ctx), Data: data,
	}); err != nil {
		// Unlike a reset there is nothing to hide — the shopper is signed in
		// and the address is their own — so the failure is reported rather
		// than answered with an "accepted" that would send them to an empty
		// inbox. The token stays: another backend may have delivered it.
		return gocommerce.Internalf(err, "send confirmation email")
	}
	return nil
}

var invalidVerificationToken = &gocommerce.APIError{
	Status:  http.StatusBadRequest,
	Code:    "invalid_token",
	Message: "this confirmation link is invalid or has expired",
}

// ConfirmVerification spends a confirmation token and marks the address it
// was mailed to as confirmed — provided that is still the account's address.
//
// It signs nobody in. The link is often opened in a mail app on another device
// from the one that asked, and a session created there is a credential the
// shopper never wanted.
func (m *Module) ConfirmVerification(ctx context.Context, token string) (*Customer, error) {
	if token == "" {
		return nil, invalidVerificationToken
	}
	var out *Customer
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var customerID int64
		var sentTo string
		err := tx.QueryRowContext(ctx, `
			DELETE FROM identity_email_verifications
			WHERE token_hash = $1 AND expires_at > now()
			RETURNING customer_id, email`, hashToken(token)).Scan(&customerID, &sentTo)
		if errors.Is(err, sql.ErrNoRows) {
			return invalidVerificationToken
		}
		if err != nil {
			return gocommerce.Internalf(err, "spend confirmation token")
		}
		c, err := markVerified(ctx, tx, customerID, sentTo)
		if errors.Is(err, sql.ErrNoRows) {
			// The account has moved to another address since the link went
			// out. The link proves the old mailbox, which is no longer theirs.
			return invalidVerificationToken
		}
		if err != nil {
			return gocommerce.Internalf(err, "confirm address")
		}
		out = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MarkEmailVerified confirms an account's address on another module's proof
// that the shopper reads mail there — an invitation mailed to the address and
// accepted while signed in to this account, say.
//
// The caller names the address its proof is about, and it is checked here
// against the account's own rather than trusted to line up: a proof for one
// address must not confirm whatever the account has since changed to. A
// mismatch is a Conflict; confirming a confirmed address is not an error.
func (m *Module) MarkEmailVerified(ctx context.Context, customerID int64, email string) (*Customer, error) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, gocommerce.Validationf("email is required")
	}
	var out *Customer
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		c, err := markVerified(ctx, tx, customerID, email)
		if errors.Is(err, sql.ErrNoRows) {
			var exists bool
			if err := tx.QueryRowContext(ctx,
				`SELECT EXISTS (SELECT 1 FROM identity_customers WHERE id = $1)`, customerID).Scan(&exists); err != nil {
				return gocommerce.Internalf(err, "look up account")
			}
			if !exists {
				return gocommerce.NotFoundf("account %d does not exist", customerID)
			}
			return gocommerce.Conflictf("%s is not the address on account %d", email, customerID)
		}
		if err != nil {
			return gocommerce.Internalf(err, "confirm address")
		}
		out = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// markVerified confirms an account's address when email is still that address,
// and retires the account's outstanding confirmation links, which have nothing
// left to prove. sql.ErrNoRows means it is not the address — or not an account.
func markVerified(ctx context.Context, tx *sql.Tx, customerID int64, email string) (*Customer, error) {
	c, err := scanCustomer(tx.QueryRowContext(ctx, `
		UPDATE identity_customers
		SET email_verified_at = coalesce(email_verified_at, now()),
		    updated_at = CASE WHEN email_verified_at IS NULL THEN now() ELSE updated_at END
		WHERE id = $1 AND email = $2
		RETURNING `+customerColumns, customerID, normalizeEmail(email)))
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM identity_email_verifications WHERE customer_id = $1`, customerID); err != nil {
		return nil, err
	}
	return c, nil
}

// ---------------------------------------------------------------- addresses

const addressColumns = `id, label, name, phone, line1, line2, city, state, postal_code, country, is_default, created_at, updated_at`

func scanAddress(row interface{ Scan(...any) error }) (*Address, error) {
	var a Address
	if err := row.Scan(&a.ID, &a.Label, &a.Name, &a.Phone, &a.Line1, &a.Line2, &a.City, &a.State,
		&a.PostalCode, &a.Country, &a.IsDefault, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// Shipping returns the address in the shape checkout takes.
func (a *Address) Shipping() gocommerce.Address {
	return gocommerce.Address{
		Name: a.Name, Phone: a.Phone, Line1: a.Line1, Line2: a.Line2,
		City: a.City, State: a.State, PostalCode: a.PostalCode, Country: a.Country,
	}
}

func addressNotFound(id int64) error {
	return gocommerce.NotFoundf("address %d is not in this account's address book", id)
}

// Addresses lists an account's address book, default first.
func (m *Module) Addresses(ctx context.Context, customerID int64, limit, offset int) ([]*Address, int, error) {
	var total int
	if err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM identity_addresses WHERE customer_id = $1`, customerID).Scan(&total); err != nil {
		return nil, 0, gocommerce.Internalf(err, "count addresses")
	}
	if limit <= 0 {
		limit = gocommerce.DefaultLimit
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT `+addressColumns+` FROM identity_addresses WHERE customer_id = $1
		 ORDER BY is_default DESC, id DESC LIMIT $2 OFFSET $3`, customerID, limit, offset)
	if err != nil {
		return nil, 0, gocommerce.Internalf(err, "list addresses")
	}
	defer rows.Close()
	out := []*Address{}
	for rows.Next() {
		a, err := scanAddress(rows)
		if err != nil {
			return nil, 0, gocommerce.Internalf(err, "scan address")
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, gocommerce.Internalf(err, "list addresses")
	}
	return out, total, nil
}

// Address returns one saved address, scoped to the account so an id from
// somebody else's book is simply not found.
func (m *Module) Address(ctx context.Context, customerID, id int64) (*Address, error) {
	a, err := scanAddress(m.db.QueryRowContext(ctx,
		`SELECT `+addressColumns+` FROM identity_addresses WHERE id = $1 AND customer_id = $2`, id, customerID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, addressNotFound(id)
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "look up address")
	}
	return a, nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// AddAddress saves an address. The first one an account saves becomes its
// default, because a book with one entry and no default is a form that asks a
// question with one answer.
func (m *Module) AddAddress(ctx context.Context, customerID int64, in AddressInput) (*Address, error) {
	a := Address{
		Label: str(in.Label), Name: str(in.Name), Phone: str(in.Phone),
		Line1: str(in.Line1), Line2: str(in.Line2), City: str(in.City), State: str(in.State),
		PostalCode: str(in.PostalCode), Country: str(in.Country),
	}
	if err := a.Shipping().Validate(); err != nil {
		return nil, err
	}
	var out *Address
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var existing int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM identity_addresses WHERE customer_id = $1`, customerID).Scan(&existing); err != nil {
			return gocommerce.Internalf(err, "count addresses")
		}
		isDefault := existing == 0 || (in.IsDefault != nil && *in.IsDefault)
		if isDefault && existing > 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE identity_addresses SET is_default = false, updated_at = now()
				 WHERE customer_id = $1 AND is_default`, customerID); err != nil {
				return gocommerce.Internalf(err, "clear default address")
			}
		}
		row := tx.QueryRowContext(ctx, `
			INSERT INTO identity_addresses
			    (customer_id, label, name, phone, line1, line2, city, state, postal_code, country, is_default)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING `+addressColumns,
			customerID, a.Label, a.Name, a.Phone, a.Line1, a.Line2, a.City, a.State, a.PostalCode, a.Country, isDefault)
		saved, err := scanAddress(row)
		if err != nil {
			return gocommerce.Internalf(err, "save address")
		}
		out = saved
		return nil
	})
	return out, err
}

// UpdateAddress patches an address. Making one the default un-defaults the
// rest in the same transaction, so the partial unique index never has to say
// no.
func (m *Module) UpdateAddress(ctx context.Context, customerID, id int64, in AddressInput) (*Address, error) {
	current, err := m.Address(ctx, customerID, id)
	if err != nil {
		return nil, err
	}
	apply := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	next := *current
	apply(&next.Label, in.Label)
	apply(&next.Name, in.Name)
	apply(&next.Phone, in.Phone)
	apply(&next.Line1, in.Line1)
	apply(&next.Line2, in.Line2)
	apply(&next.City, in.City)
	apply(&next.State, in.State)
	apply(&next.PostalCode, in.PostalCode)
	apply(&next.Country, in.Country)
	if in.IsDefault != nil {
		next.IsDefault = *in.IsDefault
	}
	if err := next.Shipping().Validate(); err != nil {
		return nil, err
	}
	if !next.IsDefault && current.IsDefault {
		// The default cannot be switched off, only moved: an account with
		// addresses and no default is the state the first save exists to avoid.
		next.IsDefault = true
	}

	var out *Address
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if next.IsDefault && !current.IsDefault {
			if _, err := tx.ExecContext(ctx,
				`UPDATE identity_addresses SET is_default = false, updated_at = now()
				 WHERE customer_id = $1 AND is_default`, customerID); err != nil {
				return gocommerce.Internalf(err, "clear default address")
			}
		}
		row := tx.QueryRowContext(ctx, `
			UPDATE identity_addresses
			SET label = $3, name = $4, phone = $5, line1 = $6, line2 = $7, city = $8, state = $9,
			    postal_code = $10, country = $11, is_default = $12, updated_at = now()
			WHERE id = $1 AND customer_id = $2
			RETURNING `+addressColumns,
			id, customerID, next.Label, next.Name, next.Phone, next.Line1, next.Line2, next.City,
			next.State, next.PostalCode, next.Country, next.IsDefault)
		saved, err := scanAddress(row)
		if errors.Is(err, sql.ErrNoRows) {
			return addressNotFound(id)
		}
		if err != nil {
			return gocommerce.Internalf(err, "update address")
		}
		out = saved
		return nil
	})
	return out, err
}

// DeleteAddress removes an address. If it was the default, the newest of the
// rest inherits, for the same reason the first save sets one.
func (m *Module) DeleteAddress(ctx context.Context, customerID, id int64) error {
	return gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var wasDefault bool
		err := tx.QueryRowContext(ctx, `
			DELETE FROM identity_addresses WHERE id = $1 AND customer_id = $2
			RETURNING is_default`, id, customerID).Scan(&wasDefault)
		if errors.Is(err, sql.ErrNoRows) {
			return addressNotFound(id)
		}
		if err != nil {
			return gocommerce.Internalf(err, "delete address")
		}
		if wasDefault {
			if _, err := tx.ExecContext(ctx, `
				UPDATE identity_addresses SET is_default = true, updated_at = now()
				WHERE id = (SELECT id FROM identity_addresses WHERE customer_id = $1 ORDER BY id DESC LIMIT 1)`,
				customerID); err != nil {
				return gocommerce.Internalf(err, "promote default address")
			}
		}
		return nil
	})
}

// ------------------------------------------------------------------- orders

// ClaimOrder links an order to an account. The order's own access token is
// the proof: it was returned once, at checkout, to whoever placed the order.
// The bool reports whether the link is new; presenting the same proof twice
// is not an error.
func (m *Module) ClaimOrder(ctx context.Context, customerID int64, number, accessToken string) (*gocommerce.Order, bool, error) {
	o, err := m.app.Order().GetForGuest(ctx, strings.TrimSpace(number), strings.TrimSpace(accessToken))
	if err != nil {
		return nil, false, err
	}
	res, err := m.db.ExecContext(ctx, `
		INSERT INTO identity_orders (customer_id, order_id, order_number)
		VALUES ($1, $2, $3)
		ON CONFLICT (customer_id, order_id) DO NOTHING`, customerID, o.ID, o.Number)
	if isUniqueViolation(err, "identity_orders_order_idx") {
		return nil, false, gocommerce.Conflictf("order %s already belongs to another account", o.Number)
	}
	if err != nil {
		return nil, false, gocommerce.Internalf(err, "link order")
	}
	n, _ := res.RowsAffected()
	// GetForGuest has already redacted; said again here so all three of this
	// module's customer-facing reads say the same thing.
	o.Redact()
	return o, n > 0, nil
}

// Orders returns a page of the account's order history, newest claim first.
func (m *Module) Orders(ctx context.Context, customerID int64, limit, offset int) ([]*gocommerce.Order, int, error) {
	var total int
	if err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM identity_orders WHERE customer_id = $1`, customerID).Scan(&total); err != nil {
		return nil, 0, gocommerce.Internalf(err, "count orders")
	}
	if limit <= 0 {
		limit = gocommerce.DefaultLimit
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT order_id FROM identity_orders WHERE customer_id = $1
		ORDER BY created_at DESC, order_id DESC LIMIT $2 OFFSET $3`, customerID, limit, offset)
	if err != nil {
		return nil, 0, gocommerce.Internalf(err, "list orders")
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, gocommerce.Internalf(err, "scan order link")
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, gocommerce.Internalf(err, "list orders")
	}

	out := []*gocommerce.Order{}
	for _, id := range ids {
		o, err := m.app.Order().Get(ctx, id)
		if errors.Is(err, gocommerce.ErrNotFound) {
			// The order was deleted from under its link. The history shows
			// what still exists; the count may be off by one until the
			// link is swept, which is the honest state.
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		// A signed-in shopper reads their own order, not the store's copy of
		// it: Redact is the same stripping the guest token route performs.
		o.Redact()
		out = append(out, o)
	}
	return out, total, nil
}

// Order returns one order from the account's history.
func (m *Module) Order(ctx context.Context, customerID int64, number string) (*gocommerce.Order, error) {
	var orderID int64
	err := m.db.QueryRowContext(ctx,
		`SELECT order_id FROM identity_orders WHERE customer_id = $1 AND order_number = $2`,
		customerID, strings.TrimSpace(number)).Scan(&orderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("order not found")
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "look up order link")
	}
	o, err := m.app.Order().Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	o.Redact()
	return o, nil
}

// -------------------------------------------------------------------- carts

var errEmailUnverified = &gocommerce.APIError{
	Status:  http.StatusForbidden,
	Code:    "email_unverified",
	Message: "confirm your email address first",
}

// ClaimCart says a basket is the signed-in shopper's, so it is priced as them:
// the customer groups their address is in reach its lines, re-priced on the
// spot (D66). The cart token proves the basket is theirs, as an order's access
// token does for ClaimOrder; the confirmed address proves they are who a group
// lists. An unconfirmed address is refused rather than quietly ignored, so the
// storefront knows to ask for the confirmation instead of showing a trade
// customer the retail price with no explanation.
func (m *Module) ClaimCart(ctx context.Context, customerID int64, cartToken string) (*gocommerce.Cart, error) {
	cartToken = strings.TrimSpace(cartToken)
	if cartToken == "" {
		return nil, gocommerce.Validationf("cart_id is required")
	}
	c, err := m.customerByID(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if !c.EmailVerified {
		return nil, errEmailUnverified
	}
	return m.app.Cart().VerifyEmail(ctx, cartToken, c.Email)
}

// --------------------------------------------------------------- throttling

// loginThrottle slows password guessing. In-process, so per replica — the
// same trade the superuser throttle makes: it raises the cost of online
// guessing without a shared store, and it is not a substitute for a decent
// password.
type loginThrottle struct {
	mu       sync.Mutex
	attempts map[string]*attemptRecord
}

type attemptRecord struct {
	failures int
	until    time.Time
	seen     time.Time
}

const (
	throttleFreeAttempts = 5
	throttleBaseDelay    = 30 * time.Second
	throttleMaxDelay     = 15 * time.Minute
	throttleForget       = time.Hour
)

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{attempts: map[string]*attemptRecord{}}
}

func (t *loginThrottle) keys(identity, ip string) []string {
	return []string{"id:" + identity, "ip:" + ip}
}

func (t *loginThrottle) blocked(identity, ip string) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for _, k := range t.keys(identity, ip) {
		if rec := t.attempts[k]; rec != nil && rec.until.After(now) {
			return rec.until.Sub(now), true
		}
	}
	return 0, false
}

func (t *loginThrottle) fail(identity, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.sweep(now)
	for _, k := range t.keys(identity, ip) {
		rec := t.attempts[k]
		if rec == nil {
			rec = &attemptRecord{}
			t.attempts[k] = rec
		}
		rec.failures++
		rec.seen = now
		if rec.failures >= throttleFreeAttempts {
			delay := throttleBaseDelay << uint(rec.failures-throttleFreeAttempts)
			if delay > throttleMaxDelay || delay <= 0 {
				delay = throttleMaxDelay
			}
			rec.until = now.Add(delay)
		}
	}
}

func (t *loginThrottle) succeed(identity, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, k := range t.keys(identity, ip) {
		delete(t.attempts, k)
	}
}

// sweep forgets records nobody has touched in an hour, so the map is bounded
// by recent activity rather than by every address that ever mistyped.
func (t *loginThrottle) sweep(now time.Time) {
	for k, rec := range t.attempts {
		if now.Sub(rec.seen) > throttleForget && !rec.until.After(now) {
			delete(t.attempts, k)
		}
	}
}

// Screens implements gocommerce.ScreenContributor.
//
// A store running this module has shopper accounts and, until the panel learned
// to render a module's screen, no way to see them: the routes existed and the
// interface did not. This is that gap closed from the module's own side, which
// is the point of the descriptor — the panel draws it, so it looks like every
// other screen and needs no code from this package.
//
// Read-only but for the delete. Accounts are the shopper's to edit, not the
// operator's: an operator who could rewrite an address book would be a support
// call away from changing where somebody's parcels go. Deleting is the
// exception because somebody has to be able to honour an erasure request, and
// it pairs accounts.read with accounts.erase for exactly that reason.
func (m *Module) Screens() []gocommerce.Screen {
	return []gocommerce.Screen{{
		Slug:  "identity-accounts",
		Title: "Shopper accounts",
		Icon:  "ri-account-circle-line",
		Group: "Customers",
		Right: gocommerce.RightCustomersRead,
		Help: "People who signed up rather than checking out as guests. " +
			"Guest checkout never stops working, so this is a subset of the customers list, not a replacement for it.",
		List: gocommerce.ScreenList{
			Endpoint: "/api/admin/x/identity/customers",
			Paged:    true,
			Empty:    "Nobody has created an account yet. Orders still arrive through guest checkout.",
			Columns: []gocommerce.ScreenColumn{
				{Key: "email", Label: "Email"},
				{Key: "name", Label: "Name"},
				{Key: "phone", Label: "Phone", Narrow: true},
				{Key: "created_at", Label: "Joined", Kind: "relative", Narrow: true},
			},
		},
		Form: &gocommerce.ScreenForm{
			// No create and no update: see the note above. Delete alone, so an
			// erasure request can be honoured from the panel.
			Delete: "/api/admin/x/identity/customers/{id}",
			DeleteWarning: "The account, its saved addresses and its sign-in go for good. " +
				"Orders already placed keep their own snapshot of the address they shipped to, so history stays readable.",
		},
	}}
}

// The rights this module's screens are gated on.
//
// Reading an account is the personal data customers.read already governs, so
// accounts.read goes to the roles that held it and installing this module
// widens nobody. Erasing one is not a read: it takes the account, its
// sessions, its address book and its order links, so it is its own right and
// defaults to nobody — rights are all-of, so whoever may erase can still see
// what they are erasing.
const (
	rightAccountsRead  gocommerce.Right = "accounts.read"  // was customers.read
	rightAccountsErase gocommerce.Right = "accounts.erase" // was store.operate
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightAccountsRead,
		Label:   "See shopper accounts",
		Scope:   "Who has signed up — names, emails, addresses",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightAccountsErase,
		Label: "Erase a shopper account",
		Scope: "The account, its sessions, its address book and its order links",
	})
}
