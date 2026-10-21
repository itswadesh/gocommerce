package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Agent keys (ADR 0003, D105): the credential an AI agent holds.
//
// An agent key is an API key with kind = 'agent', and it differs from the
// integration keys beside it in four ways, each because an agent runs
// unattended and a person did not read what it is about to do:
//
//   - it carries a list of rights, chosen from its issuer's, instead of a role.
//     A role is a bundle a store re-cuts; the list is what the owner looked at
//     when they made the key, and nothing widens it afterwards;
//   - it expires, 90 days unless the issuer says otherwise, so a key pasted
//     into an agent's configuration and forgotten stops on its own;
//   - only its hash is kept. M46 keeps a readable copy of an integration key so
//     an owner can copy it again; an agent key is shown once, because the
//     place a forgotten agent credential gets read back from is the panel;
//   - it is rationed per minute, in Postgres, so a loop in somebody's agent is
//     a 429 rather than a store's worth of load, on every replica at once.
//
// And it reads only, in this release. An agent's writes arrive with the
// approval queue (ADR 0003 §5), where the change runs as the person who
// approved it; until then nothing an agent key can hold changes the store.
//
// Personal data is masked for an agent on every store, exactly as a demo store
// masks it for everybody (App.Masks in demo.go), and no right unmasks it yet.

// The two kinds of key, as api_keys.kind stores them and Superuser.KeyKind
// carries them.
const (
	KeyKindIntegration = "integration"
	KeyKindAgent       = "agent"
)

const (
	agentKeyDefaultDays = 90
	agentKeyMaxDays     = 365
	agentKeyDefaultRate = 60
	agentKeyMaxRate     = 600
)

// AgentKeyRefusedRights are the rights an agent key may never hold, whatever
// its issuer holds and whatever a later release lets agents write. Each value
// is why, and it is the sentence a refusal quotes.
//
// The check runs twice: when a key is made, and again every time one is
// presented, so a right written into api_keys.rights by hand is dropped rather
// than honoured. A module's right is named here by its string, because the
// list is about what an agent may hold rather than about declaring the right —
// ext/alltest fails if a name here stops being a right somebody declares.
var AgentKeyRefusedRights = map[Right]string{
	RightAPIKeysRead:  "an agent has no business with the store's credentials, even the list of them",
	RightAPIKeysWrite: "a key that can mint keys can mint one wider than itself",
	RightTeamRead: "the team list is the staff's addresses and who holds which role; " +
		"masking hides the addresses but not the roster",
	RightTeamWrite:  "it hands out roles, owner included",
	RightRolesWrite: "it rewrites what every role in the store may do",
	RightPluginsRead: "the plugin screens are where a gateway's and a carrier's credentials live, " +
		"masked or not",
	RightPluginsWrite: "it can point the store's money somewhere else",
	RightStoreWrite:   "it changes what the shop calls itself on every invoice, slip and receipt",
	RightStoreOperate: "it reads the outbox, whose payloads carry buyers' addresses in full, " +
		"and every colleague's actions by name",
	RightDataExport: "it takes the whole catalogue or every order out of the building as a file",
	RightDataImport: "one file changes every price and stock level at once",
	RightReportsWrite: "a custom report is owner-written SQL, and it reads around any mask the " +
		"engine applies",
	RightErrorsRead: "the error page is the database's own words and the unmasked address of " +
		"whoever hit the error",
	// Module rights.
	"webhooks.read":  "a webhook endpoint is how data leaves the store, and its signing secret is on the screen",
	"webhooks.write": "a webhook endpoint is how data leaves the store",
	"abandonment.read": "the abandoned-checkout screens do not mask contact details yet, " +
		"so an agent would read them in full",
	"abandonment.automate": "it decides what the store sends every shopper who leaves a basket",
}

// agentReadLikeRights are the rights that do not end in ".read" and were
// reviewed and found to read only when an agent key holds them.
var agentReadLikeRights = map[Right]string{
	// The route into ext/mcp. Every tool still checks its own rights, an agent
	// key holds no write, and ext/mcp refuses an agent key any tool marked as
	// mutating (including a module's that names no right), so what it reaches
	// is the read tools.
	"agent.dispatch": "the read tools of ext/mcp",
}

// AgentMayHold reports whether an agent key may carry right in this release:
// a read, or a right reviewed as read-like, and never one of the refused set.
func AgentMayHold(right Right) bool {
	if _, refused := AgentKeyRefusedRights[right]; refused {
		return false
	}
	if _, ok := agentReadLikeRights[right]; ok {
		return true
	}
	return strings.HasSuffix(string(right), ".read")
}

// refuseAgentTerms keeps an integration key from arriving with an agent's
// terms, which it would otherwise silently ignore — a list of rights the
// caller believes is limiting the key while the role decides everything.
func refuseAgentTerms(in APIKeyInput) error {
	if len(in.Rights) > 0 || in.ExpiresInDays != nil || in.RatePerMinute != nil {
		return Validationf("rights, an expiry and a rate are an agent key's terms; " +
			"an integration key acts in a role (send kind \"agent\" for the other)")
	}
	return nil
}

// createAgent mints an agent key. It is Create's other half, kept apart so
// the integration path reads as it did.
func (k *APIKeys) createAgent(ctx context.Context, in APIKeyInput, by *Superuser) (*APIKey, string, error) {
	// The subset rule needs a set to be a subset of. A static token is nobody,
	// and a key's rights are themselves borrowed from whoever made it.
	if by == nil || by.ID == 0 || by.KeyID != 0 {
		return nil, "", Validationf("an agent key is issued by a person signed in to the panel: " +
			"it carries a share of its issuer's rights, and a static token or another key has none to share")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, "", Validationf("a key needs a name, so the list says what each one is for")
	}
	if len([]rune(name)) > MaxAPIKeyName {
		return nil, "", Validationf("the name is at most %d characters", MaxAPIKeyName)
	}
	if strings.TrimSpace(in.Role) != "" {
		return nil, "", Validationf("an agent key carries a list of rights, not a role; leave role out")
	}
	rights, err := k.agentRights(in.Rights, by)
	if err != nil {
		return nil, "", err
	}
	days := agentKeyDefaultDays
	if in.ExpiresInDays != nil {
		days = *in.ExpiresInDays
	}
	if days < 1 || days > agentKeyMaxDays {
		return nil, "", Validationf("an agent key expires in 1 to %d days; %d is outside that", agentKeyMaxDays, days)
	}
	rate := agentKeyDefaultRate
	if in.RatePerMinute != nil {
		rate = *in.RatePerMinute
	}
	if rate < 1 || rate > agentKeyMaxRate {
		return nil, "", Validationf("an agent key is allowed 1 to %d requests a minute; %d is outside that", agentKeyMaxRate, rate)
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
	names := make([]string, len(rights))
	for i, r := range rights {
		names[i] = string(r)
	}

	// The secret column is left at its default, empty: only the hash is kept.
	// The index row goes in with the key, as Create's does.
	out := APIKey{}
	var raw []byte
	err = k.app.db.QueryRowContext(ctx, `
		WITH k AS (
			INSERT INTO api_keys (name, prefix, token_hash, role, created_by,
			                      kind, rights, expires_at, rate_per_minute)
			VALUES ($1, $2, $3, '', $4, 'agent', $5::text[], now() + make_interval(days => $6), $7)
			RETURNING id, name, prefix, role, created_at, created_by, store_id,
			          kind, rights, expires_at, rate_per_minute
		), routed AS (
			INSERT INTO api_key_index (prefix, store_id) SELECT prefix, store_id FROM k
		)
		SELECT id, name, prefix, role, created_at, created_by,
		       kind, to_jsonb(rights), expires_at, rate_per_minute FROM k`,
		name, prefix, hashAPIKey(presented), by.ID, stringArray(names), days, rate,
	).Scan(&out.ID, &out.Name, &out.Prefix, &out.Role, &out.CreatedAt, &out.CreatedBy,
		&out.Kind, &raw, &out.ExpiresAt, &out.RatePerMinute)
	if err != nil {
		return nil, "", Internalf(err, "save the key")
	}
	if out.Rights, err = decodeKeyRights(raw); err != nil {
		return nil, "", Internalf(err, "read the key's rights")
	}
	k.app.log.Info("agent key created", "id", out.ID, "prefix", out.Prefix,
		"rights", len(out.Rights), "expires_at", out.ExpiresAt, "by", by.ID)
	return &out, presented, nil
}

// agentRights validates what an agent key is asked to carry and returns it
// deduplicated and sorted, so the stored list is stable to read and compare.
//
// Every refusal names the right, because the person reading it is choosing
// from a checklist and needs to know which box to clear.
func (k *APIKeys) agentRights(asked []Right, by *Superuser) ([]Right, error) {
	if len(asked) == 0 {
		return nil, Validationf("an agent key needs at least one right: it carries a list of rights instead of a role")
	}
	out := []Right{}
	for _, r := range asked {
		r = Right(strings.TrimSpace(string(r)))
		if !k.app.hasRight(r) {
			return nil, Validationf("%q is not a right this store has", r)
		}
		if why, refused := AgentKeyRefusedRights[r]; refused {
			return nil, Validationf("%s can never be given to an agent key: %s", r, why)
		}
		if !AgentMayHold(r) {
			return nil, Validationf("%s changes the store, and agent keys only read until "+
				"an agent's changes go through an approval queue", r)
		}
		if !by.Has(r) {
			return nil, Validationf("you do not hold %s, so you cannot give it to an agent key", r)
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return out, nil
}

// agentCaller is the Superuser an agent key resolves to.
//
// Its rights are the stored list filtered again by AgentMayHold: the second
// line behind Create, so a row edited by hand — or a right this build no
// longer has — grants nothing. Role reads "agent", because every refusal and
// every audit row prints the role, and "staff" would be a lie.
func (k *APIKeys) agentCaller(id int64, name string, raw []byte, rate *int) (*Superuser, bool) {
	stored, err := decodeKeyRights(raw)
	if err != nil {
		return nil, false
	}
	rights := []Right{}
	for _, r := range stored {
		if AgentMayHold(r) && k.app.hasRight(r) && !slices.Contains(rights, r) {
			rights = append(rights, r)
		}
	}
	slices.Sort(rights)
	perMinute := agentKeyDefaultRate
	if rate != nil {
		perMinute = *rate
	}
	return &Superuser{
		Email:   "agent key: " + name,
		Role:    KeyKindAgent,
		Rights:  rights,
		KeyID:   id,
		KeyKind: KeyKindAgent,
		keyRate: perMinute,
	}, true
}

// decodeKeyRights reads to_jsonb(rights). A NULL column — every integration
// key — is a nil slice, which the listing omits.
func decodeKeyRights(raw []byte) ([]Right, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var out []Right
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// refuseAgentSecret answers Secret for an agent key. A lookup of its own, ahead
// of Secret's, so the copy M46 keeps for integration keys is read the way it
// always was.
func (k *APIKeys) refuseAgentSecret(ctx context.Context, id int64) error {
	var kind string
	err := k.app.db.QueryRowContext(ctx, `SELECT kind FROM api_keys WHERE id = $1`, id).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // Secret says there is no such key
	}
	if err != nil {
		return Internalf(err, "read the key")
	}
	if kind == KeyKindAgent {
		return Conflictf("an agent key is shown once, when it is made, and only its hash is kept; " +
			"revoke it and make another")
	}
	return nil
}

// ------------------------------------------------------------- per request

// admitAgent is what an agent key goes through on every request after it has
// authenticated: its rate, then the routes no agent may reach. It writes the
// refusal itself and reports whether the request may go on.
func (a *App) admitAgent(w http.ResponseWriter, r *http.Request, su *Superuser) bool {
	wait, ok, err := a.apiKeys.allowAgent(r.Context(), su.KeyID, su.keyRate)
	if err != nil {
		RespondError(w, r, Internalf(err, "count the agent key's requests"))
		return false
	}
	if !ok {
		seconds := int(math.Ceil(wait.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		RespondError(w, r, &APIError{
			Status: http.StatusTooManyRequests,
			Code:   "rate_limited",
			Message: fmt.Sprintf("this agent key is allowed %d requests a minute; try again in %d seconds",
				su.keyRate, seconds),
			Details: map[string]any{
				"limit": "agent_key_per_minute", "per_minute": su.keyRate, "retry_after_seconds": seconds,
			},
		})
		return false
	}
	// The routes a demo freezes for every method are the ones that hand out a
	// credential or read with no shape for a mask to act on (demoFrozenAny).
	// An agent is masked on every store, so they are closed to it on every
	// store; the demo's other freezes — who can sign in — stay a demo's.
	for _, p := range demoFrozenAny {
		if demoPathMatch(p, r.URL.Path) {
			RespondError(w, r, &APIError{
				Status: http.StatusForbidden,
				Code:   "agent_key_refused",
				Message: "an agent key cannot reach this route: it hands out a credential, " +
					"or reads around the mask an agent is shown",
			})
			return false
		}
	}
	return true
}

// allowAgent counts one request against the key's minute and reports whether
// it is within the key's rate, and if not how long until the minute ends.
//
// One statement per request, in Postgres, so the count holds across every
// replica serving the store. The window opens at the key's first request and
// resets the first time a request arrives a minute or more after it opened —
// a fixed window, which lets a burst straddle two of them; a key allowed 60 a
// minute can make 120 in two seconds across a boundary, which is a bound, and
// a sliding log would cost a row per request to tighten it.
func (k *APIKeys) allowAgent(ctx context.Context, keyID int64, perMinute int) (time.Duration, bool, error) {
	var count int
	var left float64
	err := k.app.db.QueryRowContext(ctx, `
		INSERT INTO api_key_rates AS r (key_id, window_start, count)
		VALUES ($1, now(), 1)
		ON CONFLICT (store_id, key_id) DO UPDATE SET
		    window_start = CASE WHEN r.window_start <= now() - interval '1 minute'
		                        THEN now() ELSE r.window_start END,
		    count        = CASE WHEN r.window_start <= now() - interval '1 minute'
		                        THEN 1 ELSE r.count + 1 END
		RETURNING count, extract(epoch FROM r.window_start + interval '1 minute' - now())::float8`,
		keyID).Scan(&count, &left)
	if err != nil {
		return 0, false, err
	}
	if count <= perMinute {
		return 0, true, nil
	}
	return time.Duration(left * float64(time.Second)), false, nil
}

// -------------------------------------------------------------------- route

// handleAgentRights is the checklist the panel draws: every right the caller
// holds that an agent key may carry, in the engine's order, with a module's own
// words beside its rights because the panel has no table for them.
//
// A caller who is not a person — the static token, a key — gets an empty list,
// since it could not issue an agent key with any of them.
func (a *App) handleAgentRights(w http.ResponseWriter, r *http.Request) {
	su := SuperuserFrom(r.Context())
	out := []RightCatalogue{}
	if su != nil && su.ID != 0 && su.KeyID == 0 {
		words := map[Right]RightCatalogue{}
		for _, c := range a.RightsCatalogue() {
			words[c.Right] = c
		}
		for _, right := range a.Rights() {
			if !AgentMayHold(right) || !su.Has(right) {
				continue
			}
			entry, ok := words[right]
			if !ok {
				entry = RightCatalogue{Right: right}
			}
			out = append(out, entry)
		}
	}
	Respond(w, http.StatusOK, out)
}

// ---------------------------------------------------------------- migration

// M71 — agent keys (ADR 0003, D105).
//
// Columns on api_keys rather than a table of their own, because an agent key
// authenticates, routes to its store, is listed and is revoked exactly as an
// integration key is; what differs is what it may do, and that is four
// columns. Every existing key becomes an integration key and behaves as it did.
//
// The CHECK is what keeps the two kinds from blurring: an agent key has every
// one of its terms and an integration key none of them, so a NULL can never
// mean "an agent with no rights", which Resolve would have to guess about.
//
// api_key_rates is one row per agent key: the minute it is counting and how
// many requests it has seen in it (allowAgent). It is store-owned, so it goes
// under the row policy with the next migration.
const migration0071AgentKeys = `
ALTER TABLE api_keys
    ADD COLUMN kind            text        NOT NULL DEFAULT 'integration'
                                           CHECK (kind IN ('integration', 'agent')),
    ADD COLUMN rights          text[],
    ADD COLUMN expires_at      timestamptz,
    ADD COLUMN rate_per_minute integer     CHECK (rate_per_minute BETWEEN 1 AND 600),
    ADD CONSTRAINT api_keys_kind_terms CHECK (CASE kind
        WHEN 'agent' THEN coalesce(cardinality(rights), 0) > 0
                      AND expires_at IS NOT NULL AND rate_per_minute IS NOT NULL
        ELSE rights IS NULL AND expires_at IS NULL AND rate_per_minute IS NULL
    END);

CREATE TABLE api_key_rates (
    key_id       bigint      PRIMARY KEY REFERENCES api_keys (id) ON DELETE CASCADE,
    window_start timestamptz NOT NULL,
    count        integer     NOT NULL
);
`
