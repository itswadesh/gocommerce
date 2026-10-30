package shiprocket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/itswadesh/gocommerce/core"
)

// The transport: one login per store's account, and every call after it.
//
// Every endpoint here is from Shiprocket's published API collection,
// https://apidocs.shiprocket.in/ (Postman collection 8407119/SzYW1zB2); the
// comment on each wrapper names the section it comes from.

// session is one store's bearer token, and which credentials it came from:
// a store that changes its API user must not keep calling as the old one.
type session struct {
	key   string
	token string
	at    time.Time
}

// credentialKey identifies an account without keeping its password around in
// a second place.
func credentialKey(c *Config) string {
	sum := sha256.Sum256([]byte(c.Email + "\x00" + c.Password + "\x00" + c.BaseURL))
	return hex.EncodeToString(sum[:])
}

// login asks for a token. "Authentication API › Generate Token":
// POST /v1/external/auth/login {email, password} → {token}; the token is
// valid for ten days.
func (m *Module) login(ctx context.Context, c *Config) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	status, body, err := m.send(ctx, c, http.MethodPost, "/v1/external/auth/login", nil,
		map[string]string{"email": c.Email, "password": c.Password}, "")
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		// Never the body verbatim: the message is fine, but nothing here
		// should risk echoing what was sent.
		return "", &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "provider_refused",
			Message: "Shiprocket did not accept that API user's email and password (" + refusalText(status, body) +
				"). Use the API user made under Settings › API in Shiprocket, not the account owner's login."}
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return "", errors.New("shiprocket: log in returned no token")
	}
	return out.Token, nil
}

// token returns the store's bearer token, logging in when it has none, when
// the one it has is from other credentials, or when it is old enough that it
// might lapse in the middle of a booking.
func (m *Module) token(ctx context.Context, c *Config, fresh bool) (string, error) {
	store, _ := gocommerce.StoreFrom(ctx)
	key := credentialKey(c)
	m.mu.Lock()
	s, ok := m.sessions[store]
	m.mu.Unlock()
	if ok && !fresh && s.key == key && time.Since(s.at) < tokenLifetime {
		return s.token, nil
	}
	tok, err := m.login(ctx, c)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.sessions[store] = session{key: key, token: tok, at: time.Now()}
	m.mu.Unlock()
	return tok, nil
}

// call is one authenticated request. A 401 means the token lapsed early —
// Shiprocket may end a session before its ten days — so it logs in again and
// tries once more before giving up.
func (m *Module) call(ctx context.Context, method, path string, query url.Values, payload, out any) error {
	return m.callAs(ctx, m.conf(ctx), method, path, query, payload, out)
}

// callAs is call with an account given rather than the store's saved one, for
// trying credentials before they are saved.
func (m *Module) callAs(ctx context.Context, c *Config, method, path string, query url.Values, payload, out any) error {
	for attempt := 0; ; attempt++ {
		tok, err := m.token(ctx, c, attempt > 0)
		if err != nil {
			return err
		}
		status, body, err := m.send(ctx, c, method, path, query, payload, tok)
		if err != nil {
			return err
		}
		if status == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if status < 200 || status >= 300 {
			return refused(refusalText(status, body))
		}
		if out == nil || len(bytes.TrimSpace(body)) == 0 {
			return nil
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("shiprocket: %s: unexpected answer: %w", path, err)
		}
		return nil
	}
}

func (m *Module) send(ctx context.Context, c *Config, method, path string, query url.Values, payload any, token string) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	target := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("shiprocket: %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("shiprocket: %s: read the answer: %w", path, err)
	}
	return resp.StatusCode, body, nil
}

// refused is Shiprocket answering and saying no. 422 rather than a 5xx, so the
// operator reads Shiprocket's own words — "pincode not serviceable",
// "insufficient balance" — which is the answer they pressed the button for.
func refused(reason string) error {
	return &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "provider_refused",
		Message: "Shiprocket said: " + reason}
}

// refusalText is what Shiprocket said, from the shapes its documented errors
// take: {"message": "..."} and {"message": "Oops! Invalid Data.", "errors":
// {"field": ["..."]}}. The field messages are the useful part of the second.
func refusalText(status int, body []byte) string {
	var e struct {
		Message string              `json:"message"`
		Errors  map[string][]string `json:"errors"`
	}
	_ = json.Unmarshal(body, &e)
	var parts []string
	keys := make([]string, 0, len(e.Errors))
	for k := range e.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, e.Errors[k]...)
	}
	switch {
	case len(parts) > 0:
		return strings.Join(parts, " ")
	case e.Message != "":
		return e.Message
	default:
		return fmt.Sprintf("HTTP %d", status)
	}
}

// ----------------------------------------------------------------- numbers

// rupees renders minor units for Shiprocket, which takes rupees. Formatted
// from the integer rather than divided as a float, so ₹1,234.50 is exactly
// "1234.50" on the wire.
func rupees(minor int64) json.Number {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return json.Number(fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100))
}

// number is a figure Shiprocket sends as a JSON number in one response and a
// string in another ("rate": 54, "estimated_delivery_days": "4").
type number float64

func (n *number) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// A rate is shown to choose a courier, never stored as money, so an
		// unparseable one is zero rather than a failed quote.
		*n = 0
		return nil
	}
	*n = number(f)
	return nil
}

// minor is a rupee figure from Shiprocket in paise.
func (n number) minor() int64 {
	f := float64(n) * 100
	if f < 0 {
		return int64(f - 0.5)
	}
	return int64(f + 0.5)
}
