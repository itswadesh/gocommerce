package webhooks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// endpointColumns reads events through to_jsonb for the reason catalog.go
// gives: pgx's database/sql driver hands a text[] back as its raw PostgreSQL
// literal, and this package has no business parsing quoted array syntax.
const endpointColumns = `id, url, secret, to_jsonb(events), active, created_at, updated_at`

func scanEndpoint(row interface{ Scan(...any) error }) (*Endpoint, error) {
	var e Endpoint
	var events []byte
	var secret string
	if err := row.Scan(&e.ID, &e.URL, &secret, &events, &e.Active, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(events, &e.Events); err != nil {
		return nil, err
	}
	e.SecretHint = hint(secret)
	return &e, nil
}

// arrayLiteral renders a []string as a PostgreSQL array literal, the same way
// core/movements.go does for its kind filter.
func arrayLiteral(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
	}
	return "{" + strings.Join(quoted, ",") + "}"
}

// newSecret is 32 bytes from crypto/rand, prefixed so that a secret found
// somewhere it should not be is recognisable as one.
func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "whsec_" + hex.EncodeToString(b), nil
}

type endpointInput struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
	Active *bool    `json:"active"`
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in endpointInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := validURL(in.URL); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	events, err := cleanEvents(in.Events)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	secret, err := newSecret()
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}

	row := m.db.QueryRowContext(r.Context(), `
		INSERT INTO webhook_endpoints (url, secret, events, active)
		VALUES ($1, $2, $3::text[], $4)
		RETURNING `+endpointColumns,
		strings.TrimSpace(in.URL), secret, arrayLiteral(events), active)
	e, err := scanEndpoint(row)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// The one moment the secret travels. After this the store can sign with it
	// and nobody can read it back.
	e.Secret = secret
	gocommerce.Respond(w, http.StatusCreated, e)
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var total int
	if err := m.db.QueryRowContext(r.Context(),
		`SELECT count(*) FROM webhook_endpoints`).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	rows, err := m.db.QueryContext(r.Context(),
		`SELECT `+endpointColumns+` FROM webhook_endpoints ORDER BY id DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()

	out := []*Endpoint{}
	for rows.Next() {
		e, err := scanEndpoint(rows)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	e, err := m.endpoint(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, e)
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in endpointInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	set := []string{}
	args := []any{id}
	if in.URL != "" {
		if err := validURL(in.URL); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		args = append(args, strings.TrimSpace(in.URL))
		set = append(set, "url = $"+strconv.Itoa(len(args)))
	}
	if in.Events != nil {
		events, err := cleanEvents(in.Events)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		args = append(args, arrayLiteral(events))
		set = append(set, "events = $"+strconv.Itoa(len(args))+"::text[]")
	}
	if in.Active != nil {
		args = append(args, *in.Active)
		set = append(set, "active = $"+strconv.Itoa(len(args)))
	}
	if len(set) == 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("nothing to change"))
		return
	}

	row := m.db.QueryRowContext(r.Context(),
		`UPDATE webhook_endpoints SET `+strings.Join(set, ", ")+`, updated_at = now()
		 WHERE id = $1 RETURNING `+endpointColumns, args...)
	e, err := scanEndpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no webhook endpoint %d", id))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, e)
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// The deliveries go with it: they are a log of what this store told that
	// endpoint, and the endpoint is gone. ON DELETE CASCADE does it.
	res, err := m.db.ExecContext(r.Context(), `DELETE FROM webhook_endpoints WHERE id = $1`, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no webhook endpoint %d", id))
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleRotate is the second and last time a secret is readable. It exists so
// that a leaked secret has an answer that is not "delete the endpoint and tell
// the merchant to re-register".
func (m *Module) handleRotate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	secret, err := newSecret()
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	row := m.db.QueryRowContext(r.Context(),
		`UPDATE webhook_endpoints SET secret = $2, updated_at = now()
		 WHERE id = $1 RETURNING `+endpointColumns, id, secret)
	e, err := scanEndpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no webhook endpoint %d", id))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	e.Secret = secret
	gocommerce.Respond(w, http.StatusOK, e)
}

func (m *Module) endpoint(ctx context.Context, id int64) (*Endpoint, error) {
	row := m.db.QueryRowContext(ctx,
		`SELECT `+endpointColumns+` FROM webhook_endpoints WHERE id = $1`, id)
	e, err := scanEndpoint(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no webhook endpoint %d", id)
	}
	return e, err
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.Validationf("id must be a positive integer")
	}
	return id, nil
}
