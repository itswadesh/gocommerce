package gocommerce

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// API keys: a credential with a role.
//
// Config.AdminTokens already exists and stays. It is the credential a deploy
// script or a cron job uses — it lives in the process environment, there is
// nobody behind it, and requireRights deliberately waves it through
// everything, because narrowing what a script may do is a decision about which
// token it is given rather than about the route.
//
// That shape is wrong for the thing a store actually wants, which is a key it
// can hand to a shipping partner that reads orders and cannot refund them. So
// a key here carries a role, goes through the same rights the Roles screen
// shows, and is refused by name when it reaches something its role does not
// carry. Authorization has one implementation and the key is just another way
// to arrive at it.
//
// The secret is never stored. What is stored is the prefix in plain text —
// unique and indexed, so a presented key resolves in one lookup and the panel
// can say which key is which — and a SHA-256 of the whole key beside it, which
// is what decides whether the rest was right. SHA-256 rather than bcrypt on
// purpose: this is machine-generated randomness rather than a password
// somebody chose, so there is no dictionary to slow an attacker down, and the
// check runs on every request.
//
// Minting is owner-only, and that is not timidity. A role that can mint a key
// can mint one carrying more rights than itself, so granting apikeys.write to
// manager would quietly be granting manager everything.

const (
	// apiKeyScheme announces what a string is, so a key found in a log or a
	// config file is recognisable rather than being mistaken for a session
	// token — and so Resolve can decline without touching the database.
	apiKeyScheme = "gck_"
	// apiKeyPrefixLen is the visible half: enough to be unique across any
	// number of keys a store will hold, short enough to read in a table.
	apiKeyPrefixLen = 8
	apiKeySecretLen = 32
	// MaxAPIKeyName keeps the listing readable.
	MaxAPIKeyName = 80
)

// APIKey is one key, as everything except its holder sees it. There is no
// field here that can be turned back into the credential.
type APIKey struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Prefix identifies the key in the listing and in a log line.
	Prefix    string     `json:"prefix"`
	Role      string     `json:"role"`
	CreatedAt time.Time  `json:"created_at"`
	CreatedBy *int64     `json:"created_by,omitempty"`
	LastUsed  *time.Time `json:"last_used_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	RevokedBy *int64     `json:"revoked_by,omitempty"`
}

// Active is whether this key would authenticate.
func (k *APIKey) Active() bool { return k != nil && k.RevokedAt == nil }

// APIKeyInput is what an operator provides.
type APIKeyInput struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// APIKeys is the service.
type APIKeys struct{ app *App }

// APIKeys returns the service.
func (a *App) APIKeys() *APIKeys { return a.apiKeys }

// Create mints a key and returns it with its secret.
//
// The secret is returned exactly once, from here, and is not recoverable
// afterwards — not by the panel, not by the owner, not from the table. A store
// that loses one revokes it and mints another, which is the only honest thing
// a system that does not keep the secret can offer.
func (k *APIKeys) Create(ctx context.Context, in APIKeyInput, by *Superuser) (*APIKey, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, "", Validationf("a key needs a name, so the list says what each one is for")
	}
	if len([]rune(name)) > MaxAPIKeyName {
		return nil, "", Validationf("the name is at most %d characters", MaxAPIKeyName)
	}
	role := strings.TrimSpace(in.Role)
	if !isRole(role) {
		return nil, "", Validationf("%q is not a role this store has", in.Role)
	}

	prefix, err := randomToken(apiKeyPrefixLen)
	if err != nil {
		return nil, "", Internalf(err, "generate a key")
	}
	secret, err := randomToken(apiKeySecretLen)
	if err != nil {
		return nil, "", Internalf(err, "generate a key")
	}
	presented := apiKeyScheme + prefix + "_" + secret

	var createdBy *int64
	if by != nil {
		createdBy = &by.ID
	}
	var out APIKey
	err = k.app.db.QueryRowContext(ctx, `
		INSERT INTO api_keys (name, prefix, token_hash, secret, role, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, prefix, role, created_at, created_by`,
		name, prefix, hashAPIKey(presented), presented, role, createdBy,
	).Scan(&out.ID, &out.Name, &out.Prefix, &out.Role, &out.CreatedAt, &out.CreatedBy)
	if err != nil {
		return nil, "", Internalf(err, "save the key")
	}
	k.app.log.Info("api key created", "id", out.ID, "prefix", out.Prefix, "role", out.Role)
	return &out, presented, nil
}

// List returns every key, revoked ones included.
//
// A revoked key stays in the list because the question asked after an incident
// is "what had access and when did it stop", and a deleted row answers
// neither.
func (k *APIKeys) List(ctx context.Context) ([]APIKey, error) {
	rows, err := k.app.db.QueryContext(ctx, `
		SELECT id, name, prefix, role, created_at, created_by,
		       last_used_at, revoked_at, revoked_by
		FROM api_keys
		ORDER BY revoked_at IS NOT NULL, id DESC`)
	if err != nil {
		return nil, Internalf(err, "list the keys")
	}
	defer rows.Close()

	out := []APIKey{}
	for rows.Next() {
		var a APIKey
		if err := rows.Scan(&a.ID, &a.Name, &a.Prefix, &a.Role, &a.CreatedAt, &a.CreatedBy,
			&a.LastUsed, &a.RevokedAt, &a.RevokedBy); err != nil {
			return nil, Internalf(err, "read a key")
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Revoke stops a key working. Revoking one already revoked is not an error:
// the caller wanted it off, and it is off.
func (k *APIKeys) Revoke(ctx context.Context, id int64, by *Superuser) error {
	var revokedBy *int64
	if by != nil {
		revokedBy = &by.ID
	}
	res, err := k.app.db.ExecContext(ctx, `
		UPDATE api_keys SET revoked_at = now(), revoked_by = $2
		WHERE id = $1 AND revoked_at IS NULL`, id, revokedBy)
	if err != nil {
		return Internalf(err, "revoke the key")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either it does not exist or it was already off. Tell the two apart,
		// because "there is no such key" is a different thing to learn.
		var exists bool
		if err := k.app.db.QueryRowContext(ctx,
			`SELECT true FROM api_keys WHERE id = $1`, id).Scan(&exists); err == sql.ErrNoRows {
			return NotFoundf("no such API key")
		}
		return nil
	}
	k.app.log.Info("api key revoked", "id", id)
	return nil
}

// Secret returns the key itself, for an operator who needs it again.
//
// A separate call rather than a field on the listing, and behind
// apikeys.write rather than apikeys.read: opening the screen should not put a
// credential on it, and knowing a key exists is a smaller thing than being
// able to use it. Every read is logged, because "who took a copy of this" is a
// question somebody asks after an incident.
//
// A key issued before M46 has nothing stored and says so, rather than
// returning an empty string the screen would render as a blank box.
func (k *APIKeys) Secret(ctx context.Context, id int64) (string, error) {
	var secret, name string
	err := k.app.db.QueryRowContext(ctx,
		`SELECT secret, name FROM api_keys WHERE id = $1`, id).Scan(&secret, &name)
	if err == sql.ErrNoRows {
		return "", NotFoundf("no such API key")
	}
	if err != nil {
		return "", Internalf(err, "read the key")
	}
	if secret == "" {
		return "", Conflictf("this key was issued before the engine kept a copy, so it cannot be shown again; revoke it and make another")
	}
	k.app.log.Info("api key secret read", "id", id, "name", name)
	return secret, nil
}

// Resolve authenticates a presented key.
//
// It returns a Superuser rather than a key, because everything downstream —
// requireRights, the audit trail, the panel's own `can()` — is written against
// one kind of actor, and a second kind would be a second authorization path to
// keep in step with the first. The ID is zero and the email names the key, so
// a trail says "the Warehouse robot key did this" rather than inventing a
// person.
func (k *APIKeys) Resolve(ctx context.Context, presented string) (*Superuser, bool) {
	prefix, ok := apiKeyPrefixOf(presented)
	if !ok {
		return nil, false
	}
	var (
		id       int64
		name     string
		role     string
		hash     string
		revoked  *time.Time
		lastUsed *time.Time
	)
	err := k.app.db.QueryRowContext(ctx, `
		SELECT id, name, role, token_hash, revoked_at, last_used_at
		FROM api_keys WHERE prefix = $1`, prefix,
	).Scan(&id, &name, &role, &hash, &revoked, &lastUsed)
	if err != nil {
		return nil, false
	}
	if revoked != nil {
		return nil, false
	}
	// Constant time, so a near-miss cannot be walked towards a hit by timing.
	if subtle.ConstantTimeCompare([]byte(hash), []byte(hashAPIKey(presented))) != 1 {
		return nil, false
	}

	// Rights as this store cut them, not the role's shipped defaults — the
	// same resolution a person goes through, so a key and an operator holding
	// the same role can never disagree about what they may do.
	rights, err := k.app.roles.Of(ctx, role)
	if err != nil {
		return nil, false
	}

	k.touch(ctx, id, lastUsed)
	return &Superuser{
		// No ID: there is no person here. The email says what did it, in the
		// one field every trail already prints.
		Email:  "api key: " + name,
		Role:   role,
		Rights: rights,
	}, true
}

// touch records that the key was used, at most once a minute.
//
// "When was this last used" is the question that decides whether a key can be
// revoked, and it is worth a write — but not a write on every request. A
// minute's resolution answers it and keeps a busy integration from turning
// authentication into an UPDATE per call.
func (k *APIKeys) touch(ctx context.Context, id int64, lastUsed *time.Time) {
	if lastUsed != nil && time.Since(*lastUsed) < time.Minute {
		return
	}
	// Detached from the request: the answer is already decided, and a slow
	// write here would make authentication slow for no benefit.
	go func() {
		bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = k.app.db.ExecContext(bg, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, id)
	}()
}

func hashAPIKey(presented string) string {
	sum := sha256.Sum256([]byte(presented))
	return hex.EncodeToString(sum[:])
}

// apiKeyPrefixOf pulls the lookup half out of a presented key, and says no to
// anything that is not shaped like one — so a session token never becomes a
// database query.
func apiKeyPrefixOf(presented string) (string, bool) {
	if !strings.HasPrefix(presented, apiKeyScheme) {
		return "", false
	}
	rest := presented[len(apiKeyScheme):]
	prefix, secret, found := strings.Cut(rest, "_")
	if !found || len(prefix) != apiKeyPrefixLen || len(secret) != apiKeySecretLen {
		return "", false
	}
	return prefix, true
}

// randomToken returns n characters from an alphabet with no look-alikes in it:
// a key gets read off a screen and typed into somebody else's config, and
// telling O from 0 is not a thing to ask of them.
func randomToken(n int) (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

func isRole(role string) bool {
	for _, known := range Roles {
		if role == known {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------- routes

func (a *App) mountAPIKeyRoutes() {
	a.HandleAdminFunc("GET /api/admin/api-keys", a.handleListAPIKeys, RightAPIKeysRead)
	a.HandleAdminFunc("POST /api/admin/api-keys", a.handleCreateAPIKey, RightAPIKeysWrite)
	a.HandleAdminFunc("DELETE /api/admin/api-keys/{id}", a.handleRevokeAPIKey, RightAPIKeysWrite)
	// apikeys.write, not apikeys.read: seeing the credential is a different act
	// from knowing the key exists.
	a.HandleAdminFunc("GET /api/admin/api-keys/{id}/secret", a.handleReadAPIKeySecret, RightAPIKeysWrite)
}

func (a *App) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.apiKeys.List(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, keys)
}

// createdAPIKey is the one response that carries a secret, and it says so in
// the field name rather than hiding it among the rest of the row.
type createdAPIKey struct {
	*APIKey
	// Secret is shown once and never again.
	Secret string `json:"secret"`
}

func (a *App) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var in APIKeyInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	key, secret, err := a.apiKeys.Create(r.Context(), in, SuperuserFrom(r.Context()))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, createdAPIKey{APIKey: key, Secret: secret})
}

func (a *App) handleReadAPIKeySecret(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	secret, err := a.apiKeys.Secret(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, struct {
		Secret string `json:"secret"`
	}{Secret: secret})
}

func (a *App) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.apiKeys.Revoke(r.Context(), id, SuperuserFrom(r.Context())); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
