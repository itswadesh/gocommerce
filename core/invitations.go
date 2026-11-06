package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Invitations are how somebody joins the team.
//
// The alternative — which is what this engine did until M18 — is for an owner
// to invent a password on the new person's behalf and then tell it to them.
// That password is known to two people from the moment it exists, it travels
// through whatever chat window was open, and it is almost never changed
// afterwards. An invitation moves the password to the only person who should
// ever know it, and gives the store a record of who was asked and who actually
// arrived.
//
// The token is generated once, returned once, and never stored: only its
// SHA-256 goes in the table, exactly as with a session. Losing the link means
// issuing a new invitation, which is the correct and cheap recovery.
type Invitations struct {
	app *App
}

// Team returns the invitation service.
func (a *App) Team() *Invitations { return a.invitations }

// How long an invitation link works for. Long enough to survive a weekend and
// a holiday; short enough that a link forgotten in an inbox is not a permanent
// way into the store.
// invitationTTL is how long a link works: two days, as Dokploy's do. Long
// enough to reach somebody over a weekend, short enough that a forwarded
// email is not a standing way in.
const invitationTTL = 48 * time.Hour

// Invitation is an outstanding or historical invitation to join the team.
type Invitation struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	// Phone is who a phone invitation is for, in place of Email. It has no link:
	// the person is asked when they sign in with this number (D205).
	Phone string `json:"phone,omitempty"`
	Role  string `json:"role"`
	// Rights is what the role carries *here*, resolved on the way out through
	// the store's matrix. The person deciding whether to send this needs to see
	// what they are handing over, and a role name alone does not say — least of
	// all in a store that has re-cut the role.
	Rights []Right `json:"rights"`
	// Token is the secret, and it is populated on exactly one response: the one
	// that created the invitation. Every later read leaves it empty, because by
	// then the store no longer knows it.
	Token string `json:"token,omitempty"`
	// AcceptURL is the link to send, filled in by the HTTP layer, which is the
	// only part that knows what this store is reached at. Populated alongside
	// Token and just as briefly.
	AcceptURL     string     `json:"accept_url,omitempty"`
	InvitedByID   *int64     `json:"invited_by_id,omitempty"`
	InvitedByName string     `json:"invited_by,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	AcceptedAt    *time.Time `json:"accepted_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	// Emailed and EmailError report, on the response that created the
	// invitation only, whether the link went out by email.
	Emailed    *bool  `json:"emailed,omitempty"`
	EmailError string `json:"email_error,omitempty"`
	// Status is the one field the panel actually renders, because "expired" is
	// a fact about now rather than a column.
	Status string `json:"status"`
}

// The three states an invitation can be in.
const (
	InvitationPending  = "pending"
	InvitationAccepted = "accepted"
	InvitationExpired  = "expired"
	InvitationRevoked  = "revoked"
)

const invitationColumns = `i.id, coalesce(i.email, ''), coalesce(i.phone, ''), i.role, i.invited_by,
	coalesce(s.email, s.phone, ''), i.created_at, i.expires_at, i.accepted_at, i.revoked_at`

// scanInvitation reads one invitation. rights is the store's resolved matrix,
// passed in rather than looked up here so that listing a page of invitations
// asks the question once — and so that what an invitee is told they are getting
// is the same set the store will actually give them.
func scanInvitation(row scanner, rights map[string][]Right) (*Invitation, error) {
	inv := &Invitation{}
	var invitedBy sql.NullInt64
	var accepted, revoked sql.NullTime
	if err := row.Scan(&inv.ID, &inv.Email, &inv.Phone, &inv.Role, &invitedBy,
		&inv.InvitedByName, &inv.CreatedAt, &inv.ExpiresAt, &accepted, &revoked); err != nil {
		return nil, err
	}
	if revoked.Valid {
		inv.RevokedAt = &revoked.Time
	}
	if invitedBy.Valid {
		inv.InvitedByID = &invitedBy.Int64
	}
	if accepted.Valid {
		inv.AcceptedAt = &accepted.Time
	}
	inv.Rights = rights[inv.Role]
	inv.Status = invitationStatus(inv.ExpiresAt, accepted, revoked)
	return inv, nil
}

func invitationStatus(expires time.Time, accepted, revoked sql.NullTime) string {
	switch {
	case accepted.Valid:
		return InvitationAccepted
	case revoked.Valid:
		return InvitationRevoked
	case time.Now().After(expires):
		return InvitationExpired
	default:
		return InvitationPending
	}
}

// Invite asks somebody to join the team in a role.
//
// It refuses an address that already belongs to an operator. Inviting somebody
// who is already here is either a mistake or an attempt to change their role,
// and the second one has its own endpoint that cannot be used to hand out a
// fresh password.
func (s *Invitations) Invite(ctx context.Context, email, role string, invitedBy *int64) (*Invitation, error) {
	email = normalizeEmail(email)
	if email == "" || !strings.Contains(email, "@") {
		return nil, Validationf("a valid email address is required")
	}
	if role == "" {
		role = RoleStaff
	}
	if err := s.app.roles.requireRole(ctx, role); err != nil {
		return nil, err
	}

	token, err := newSessionToken()
	if err != nil {
		return nil, Internalf(err, "generate invitation token")
	}

	var id int64
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT true FROM superusers WHERE email = $1 AND `+memberHere, email).Scan(&exists); err == nil {
			return Conflictf("%s is already on the team; change their role instead", email)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Internalf(err, "look up superuser")
		}

		// Re-inviting somebody replaces the outstanding invitation rather than
		// failing on the unique index: an owner who resends a link means the
		// old one to stop working, and two live links for one person is exactly
		// what the index exists to prevent.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM superuser_invitations WHERE email = $1 AND accepted_at IS NULL AND revoked_at IS NULL`,
			email); err != nil {
			return Internalf(err, "clear the previous invitation")
		}

		return roleGone(tx.QueryRowContext(ctx, `
			INSERT INTO superuser_invitations (email, role, token_hash, invited_by, expires_at)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			email, role, hashToken(token), invitedBy, time.Now().Add(invitationTTL),
		).Scan(&id), role)
	})
	if err != nil {
		return nil, err
	}

	inv, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// The only time this is ever populated. After this response the store
	// cannot produce it again, which is the point of storing the hash.
	inv.Token = token
	return inv, nil
}

// phoneInvitationTTL is longer than a link's: there is no link to forward, and
// it waits on somebody's next sign-in.
const phoneInvitationTTL = 30 * 24 * time.Hour

// InvitePhone asks whoever signs in with phone to join the team. Nothing is sent
// and there is no link to pass on: they are asked when they next sign in, and
// join only if they say yes (D205). Their first code makes the account if they
// have none, so the install must be able to text them one.
func (s *Invitations) InvitePhone(ctx context.Context, phone, role string, invitedBy *int64) (*Invitation, error) {
	phone, err := NormalizePhone(phone)
	if err != nil {
		return nil, err
	}
	if s.app.cfg.Demo || !s.app.notifier.carries(atInstall(ctx), loginCodeNote(phone)) {
		return nil, Validationf("this install cannot text a sign-in code to that number, so invite them by email")
	}
	if role == "" {
		role = RoleStaff
	}
	if err := s.app.roles.requireRole(ctx, role); err != nil {
		return nil, err
	}
	// Never handed out. The column is required, and an email invitation's route
	// must find nothing to accept here.
	token, err := newSessionToken()
	if err != nil {
		return nil, Internalf(err, "generate invitation token")
	}
	expires := time.Now().Add(phoneInvitationTTL)
	var id int64
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT true FROM superusers WHERE phone = $1 AND `+memberHere, phone).Scan(&exists); err == nil {
			return Conflictf("%s is already on the team; change their role instead", phone)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Internalf(err, "look up superuser")
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM superuser_invitations WHERE phone = $1 AND accepted_at IS NULL AND revoked_at IS NULL`,
			phone); err != nil {
			return Internalf(err, "clear the previous invitation")
		}
		if err := roleGone(tx.QueryRowContext(ctx, `
			INSERT INTO superuser_invitations (phone, role, token_hash, invited_by, expires_at)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			phone, role, hashToken(token), invitedBy, expires,
		).Scan(&id), role); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO store_phone_invitations (phone, store_id, expires_at)
			VALUES ($1, current_setting('gocommerce.store_id', true), $2)
			ON CONFLICT (phone, store_id) DO UPDATE SET expires_at = excluded.expires_at`,
			phone, expires); err != nil {
			return Internalf(err, "index the invitation")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// PhoneInvitation is a store asking the signed-in person to join.
type PhoneInvitation struct {
	ID        int64  `json:"id"`
	StoreID   string `json:"store_id"`
	StoreName string `json:"store_name"`
	Role      string `json:"role"`
	InvitedBy string `json:"invited_by,omitempty"`
}

// ForPerson lists the stores waiting on su's answer, in the brand the request
// came through, as their store list is (D114). Only a number a code has proved
// counts: anybody may type anybody's number at sign-up (D191).
func (s *Invitations) ForPerson(ctx context.Context, su *Superuser) ([]PhoneInvitation, error) {
	out := []PhoneInvitation{}
	if su.Phone == "" || !su.phoneProved {
		return out, nil
	}
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT st.id, st.name, coalesce(st.brand_host, '')
		FROM store_phone_invitations p JOIN stores st ON st.id = p.store_id
		WHERE p.phone = $1 AND p.expires_at > now() AND st.status = 'active'
		ORDER BY p.expires_at, st.id`, su.Phone)
	if err != nil {
		return nil, Internalf(err, "list invitations")
	}
	defer rows.Close()
	here := s.app.Brand(ctx)
	for rows.Next() {
		var inv PhoneInvitation
		var brandHost string
		if err := rows.Scan(&inv.StoreID, &inv.StoreName, &brandHost); err != nil {
			return nil, Internalf(err, "scan invitation")
		}
		if sameBrand(s.app.storeBrand(brandHost), here) {
			out = append(out, inv)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, Internalf(err, "list invitations")
	}
	rows.Close()
	open := []PhoneInvitation{}
	for _, inv := range out {
		err := s.app.db.QueryRowContext(WithStore(ctx, inv.StoreID), `
			SELECT i.id, i.role, coalesce(s.email, s.phone, '')
			FROM superuser_invitations i LEFT JOIN superusers s ON s.id = i.invited_by
			WHERE i.phone = $1 AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > now()`,
			su.Phone).Scan(&inv.ID, &inv.Role, &inv.InvitedBy)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, Internalf(err, "read invitation")
		}
		open = append(open, inv)
	}
	return open, nil
}

// AnswerPhone is su's yes or no to a store's invitation. Yes joins the store in
// the invited role; no is kept as a withdrawn invitation, which the store's
// team screen can show. Either way the store stops asking.
func (s *Invitations) AnswerPhone(ctx context.Context, su *Superuser, storeID string, id int64, accept bool) error {
	if su.Phone == "" || !su.phoneProved {
		return NotFoundf("this invitation is no longer open")
	}
	ctx = WithStore(ctx, storeID)
	rights, err := s.app.roles.All(ctx)
	if err != nil {
		return err
	}
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		inv, err := scanInvitation(tx.QueryRowContext(ctx, `
			SELECT `+invitationColumns+`
			FROM superuser_invitations i
			LEFT JOIN superusers s ON s.id = i.invited_by
			WHERE i.id = $1 AND i.phone = $2 FOR UPDATE OF i`, id, su.Phone), rights)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("this invitation is no longer open")
		}
		if err != nil {
			return Internalf(err, "read invitation")
		}
		if err := usableInvitation(inv); err != nil {
			return err
		}
		column := "revoked_at"
		if accept {
			column = "accepted_at"
			var here bool
			err := tx.QueryRowContext(ctx, `SELECT true FROM superusers WHERE id = $1 AND `+memberHere, su.ID).Scan(&here)
			if errors.Is(err, sql.ErrNoRows) {
				if err := joinStore(ctx, tx, su.ID, inv.Role, nil); err != nil {
					return Internalf(err, "add to store")
				}
			} else if err != nil {
				return Internalf(err, "look up membership")
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE superuser_invitations SET `+column+` = now() WHERE id = $1`, id); err != nil {
			return Internalf(err, "answer invitation")
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM store_phone_invitations WHERE phone = $1 AND store_id = $2`, su.Phone, storeID); err != nil {
			return Internalf(err, "answer invitation")
		}
		return nil
	})
}

// Get returns one invitation, without its token.
func (s *Invitations) Get(ctx context.Context, id int64) (*Invitation, error) {
	rights, err := s.app.roles.All(ctx)
	if err != nil {
		return nil, err
	}
	row := s.app.db.QueryRowContext(ctx, `
		SELECT `+invitationColumns+`
		FROM superuser_invitations i
		LEFT JOIN superusers s ON s.id = i.invited_by
		WHERE i.id = $1`, id)
	inv, err := scanInvitation(row, rights)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("invitation %d does not exist", id)
	}
	if err != nil {
		return nil, Internalf(err, "read invitation")
	}
	return inv, nil
}

// List returns every invitation, outstanding ones first, then most recent.
//
// Accepted ones are kept and shown because "who let this person in" is a
// question a team screen should be able to answer without a database client.
func (s *Invitations) List(ctx context.Context) ([]*Invitation, error) {
	rights, err := s.app.roles.All(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT `+invitationColumns+`
		FROM superuser_invitations i
		LEFT JOIN superusers s ON s.id = i.invited_by
		ORDER BY i.accepted_at IS NOT NULL, i.created_at DESC`)
	if err != nil {
		return nil, Internalf(err, "list invitations")
	}
	defer rows.Close()
	out := []*Invitation{}
	for rows.Next() {
		inv, err := scanInvitation(rows, rights)
		if err != nil {
			return nil, Internalf(err, "scan invitation")
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// Revoke closes an outstanding invitation. An accepted one is history and
// cannot be revoked: the way to undo that is to remove the operator.
func (s *Invitations) Revoke(ctx context.Context, id int64) error {
	// Marked rather than deleted: the list keeps what was sent and what became
	// of it, and the link stops working either way.
	res, err := s.app.db.ExecContext(ctx,
		`UPDATE superuser_invitations SET revoked_at = now()
		 WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL`, id)
	if err != nil {
		return Internalf(err, "revoke invitation")
	}
	if _, err := s.app.db.ExecContext(ctx, `
		DELETE FROM store_phone_invitations
		WHERE store_id = current_setting('gocommerce.store_id', true)
		  AND phone = (SELECT phone FROM superuser_invitations WHERE id = $1)`, id); err != nil {
		return Internalf(err, "revoke invitation")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Distinguish the two, because they need different actions: one is a
		// stale screen, the other is "remove the person instead".
		var accepted, revoked bool
		err := s.app.db.QueryRowContext(ctx,
			`SELECT accepted_at IS NOT NULL, revoked_at IS NOT NULL FROM superuser_invitations WHERE id = $1`,
			id).Scan(&accepted, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("invitation %d does not exist", id)
		}
		if err != nil {
			return Internalf(err, "read invitation")
		}
		if revoked {
			return nil // already off, which is what the caller wanted
		}
		return Conflictf("this invitation was already accepted; remove the operator instead")
	}
	return nil
}

// Lookup resolves a token so the accept page can say who the invitation is for
// and what it grants, before asking for a password.
//
// It is deliberately reachable without authentication — the invitee has no
// account yet, which is the whole point — and so it returns only what somebody
// holding the link already knows or is about to be told. The token itself is
// the secret; nothing here is enumerable without it.
func (s *Invitations) Lookup(ctx context.Context, token string) (*Invitation, error) {
	rights, err := s.app.roles.All(ctx)
	if err != nil {
		return nil, err
	}
	row := s.app.db.QueryRowContext(ctx, `
		SELECT `+invitationColumns+`
		FROM superuser_invitations i
		LEFT JOIN superusers s ON s.id = i.invited_by
		WHERE i.token_hash = $1`, hashToken(token))
	inv, err := scanInvitation(row, rights)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("this invitation link is not valid")
	}
	if err != nil {
		return nil, Internalf(err, "read invitation")
	}
	if err := usableInvitation(inv); err != nil {
		return nil, err
	}
	// Deliberately silent on whether the address already has an account:
	// inviters can mint links for any address. The page asks for a password
	// either way, and Accept checks it against the account if there is one.
	return inv, nil
}

// usableInvitation is the one place that decides whether a link still works, so
// that Lookup and Accept can never disagree about it.
func usableInvitation(inv *Invitation) error {
	switch inv.Status {
	case InvitationAccepted:
		return Conflictf("this invitation has already been used")
	case InvitationRevoked:
		return Conflictf("this invitation was withdrawn")
	case InvitationExpired:
		return Conflictf("this invitation expired on %s; ask for a new one",
			inv.ExpiresAt.Format("2 January 2006"))
	}
	return nil
}

// Accept turns an invitation into an operator and signs them in.
//
// Signing them in is not a convenience: an invitee who has just chosen a
// password and is then shown a login form will type it again, and the one thing
// worse than that is being told the credentials are wrong because they fumbled
// it the first time and there is nothing to compare against.
//
// The whole thing is one transaction, and the invitation is claimed with a
// conditional UPDATE, so two people opening the same link at once cannot both
// become operators.
func (s *Invitations) Accept(ctx context.Context, token, password string) (*Superuser, *Session, error) {
	return s.AcceptFrom(ctx, token, password, "")
}

// AcceptFrom is Accept with the caller's address, which the sign-in throttle
// counts by. Accepting as an existing account checks that account's password,
// so it is a sign-in and is limited like one: inviters can mint tokens for any
// address, and an unthrottled check would let them guess at its password.
func (s *Invitations) AcceptFrom(ctx context.Context, token, password, clientIP string) (*Superuser, *Session, error) {
	rights, err := s.app.roles.All(ctx)
	if err != nil {
		return nil, nil, err
	}
	var su *Superuser
	// Who joins through this invitation arrived through its store's brand,
	// which their emails go out in when nothing else says which (D114). Read
	// before the transaction, which must not wait on a second connection.
	arrival := s.app.storeBrandHost(ctx)
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			SELECT `+invitationColumns+`
			FROM superuser_invitations i
			LEFT JOIN superusers s ON s.id = i.invited_by
			WHERE i.token_hash = $1 FOR UPDATE OF i`, hashToken(token))
		inv, err := scanInvitation(row, rights)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("this invitation link is not valid")
		}
		if err != nil {
			return Internalf(err, "read invitation")
		}
		if err := usableInvitation(inv); err != nil {
			return err
		}
		// The address is validated here rather than at Invite: the password is
		// the invitee's, and it is the first time anybody has typed one.
		if err := validateCredentials(inv.Email, password); err != nil {
			return err
		}
		hash, err := hashPassword(password)
		if err != nil {
			return Internalf(err, "hash password")
		}

		// Somebody who already signs in to another store accepts with the
		// password they have; anything else would let whoever holds the link
		// set a new password on an existing account.
		var id int64
		var existingHash string
		err = tx.QueryRowContext(ctx, `SELECT id, password_hash FROM superusers WHERE email = $1 FOR UPDATE`,
			inv.Email).Scan(&id, &existingHash)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO superusers (email, password_hash, brand_host) VALUES ($1, $2, $3) RETURNING id`,
				inv.Email, hash, arrival).Scan(&id); err != nil {
				return Internalf(err, "create superuser")
			}
		case err != nil:
			return Internalf(err, "look up superuser")
		default:
			throttle := s.app.superusers.throttle
			if retryAfter, blocked := throttle.blocked(inv.Email, clientIP); blocked {
				return &APIError{
					Status:  http.StatusTooManyRequests,
					Code:    "too_many_attempts",
					Message: fmt.Sprintf("too many failed attempts; try again in %s", retryAfter.Round(time.Second)),
				}
			}
			if !verifyPassword(existingHash, password) {
				throttle.fail(inv.Email, clientIP)
				return Forbiddenf("%s already has an account; accept with its current password", inv.Email)
			}
			throttle.succeed(inv.Email, clientIP)
		}
		if err := joinStore(ctx, tx, id, inv.Role, nil); err != nil {
			if isUniqueViolation(err) {
				return Conflictf("%s is already on the team; sign in instead", inv.Email)
			}
			return Internalf(err, "add to store")
		}
		// scanSuperuser rather than the resolving scan: this is inside a
		// transaction, and that one would take a second pool connection.
		su, err = scanSuperuser(tx.QueryRowContext(ctx,
			`SELECT `+superuserColumns+` FROM superusers WHERE id = $1`, id))
		if err != nil {
			return Internalf(err, "read superuser")
		}

		res, err := tx.ExecContext(ctx, `
			UPDATE superuser_invitations SET accepted_at = now()
			WHERE id = $1 AND accepted_at IS NULL`, inv.ID)
		if err != nil {
			return Internalf(err, "claim invitation")
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Somebody else got here first. Rolling back is what undoes the
			// operator this transaction just created.
			return Conflictf("this invitation has already been used")
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	su.Rights = rights[su.Role]

	sess, err := s.app.superusers.issue(ctx, su.ID)
	if err != nil {
		return nil, nil, err
	}
	return su, sess, nil
}

// invitationEmailTimeout bounds the send the inviter is waiting on. It is
// shorter than a reset's because a person is watching the dialog.
const invitationEmailTimeout = 15 * time.Second

// email sends inv's link to the invitee through the store's email provider
// — Resend, on a store that installed it; there is no provider to pick. It
// reports whether the email went, and if not, why, in words for the dialog:
// the link is still there to copy either way.
func (s *Invitations) email(ctx context.Context, inv *Invitation) (bool, string) {
	if !s.app.notifier.delivers(ctx, ChannelEmail) {
		return false, "No email provider is set up for this store, so nothing was sent. Copy the link and send it yourself, or add Resend under Notifications."
	}
	store := ""
	if st, err := s.app.stores.Get(ctx, s.app.storeOf(ctx)); err == nil {
		store = st.Name
	}
	ctx, cancel := context.WithTimeout(ctx, invitationEmailTimeout)
	defer cancel()
	err := s.app.notifier.send(ctx, Notification{
		Event:    EventSuperuserInvitation,
		Channel:  ChannelEmail,
		To:       inv.Email,
		Language: s.app.mainLanguage(ctx),
		Data: map[string]string{
			"invite_url":       inv.AcceptURL,
			"brand_name":       s.app.StoreBrand(ctx).Name,
			"store_name":       store,
			"role":             inv.Role,
			"invited_by":       inv.InvitedByName,
			"expires_in_hours": strconv.Itoa(int(invitationTTL / time.Hour)),
		},
		Secrets: []string{"invite_url"},
	})
	if err != nil {
		// The link is never logged: it is a credential until it is used.
		s.app.log.Error("invitation email failed", "invitation", inv.ID, "error", err)
		return false, "The email could not be sent. Copy the link and send it yourself."
	}
	return true, ""
}
