package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// The notification log is open to anybody who may read orders, and its table
// to anybody who may run a saved report, and some of what the store sends is a
// key to somebody's account: an operator's reset link and code, an invitation,
// a shopper's reset or confirmation link, a sign-in code, a basket's token.
// Nobody reading the log needs another person's key — the person it was for
// has it in their inbox — so the key never reaches the log.
//
// Two layers. The sender names its credentials (Notification.Secrets) and the
// row is written with each of them hidden (withoutSecrets), so the table holds
// nothing to use, and a message whose key is gone cannot be sent again from
// the log (Notifications.Resend). Every read of the log also hides values by
// their name and by what they hold (hideNotificationCredentials), for a module
// that has not named its credentials.

// hiddenCredential stands in for a value the log does not keep or show.
// Non-empty on purpose: a template that branches on the value ({{if
// .reset_url}}) previews the branch the message took, and an empty value stays
// empty.
const hiddenCredential = "(hidden)"

// minCredentialLen is the shortest credential looked for inside other values,
// where a shorter one would match by accident and hide harmless text: a
// four-digit code turns up in prices and order numbers.
const minCredentialLen = 6

// credentialKey reports whether a data key names a credential. The name is the
// contract with whoever sends the message: a link is *_url or *_link, a token
// *_token, a one-time code *_code, and the log hides each of them.
func credentialKey(key string) bool {
	k := strings.ToLower(key)
	if k == "postal_code" {
		// An address: the one *_code a message carries that is not a secret.
		return false
	}
	for _, word := range []string{"token", "password", "secret"} {
		if strings.Contains(k, word) {
			return true
		}
	}
	for _, word := range []string{"code", "url", "link"} {
		if k == word || strings.HasSuffix(k, "_"+word) {
			return true
		}
	}
	return false
}

// queryParam is one parameter of a URL's query or fragment, so a link that
// carries its key as ?token= is caught under whatever name the link is kept.
var queryParam = regexp.MustCompile(`([?&#;])([A-Za-z0-9_.\-]+)=([^&#;\s"'<>]*)`)

func credentialParam(name string) bool {
	switch n := strings.ToLower(name); n {
	case "key", "sig", "signature", "auth", "otp":
		return true
	default:
		return credentialKey(n) || strings.HasSuffix(n, "_key")
	}
}

// hideKeys hides, in place, every non-empty value of data whose key secret
// reports, and returns the values it hid, longest first, so a link is
// replaced whole before the token inside it (hideIn).
func hideKeys(data map[string]string, secret func(key string) bool) []string {
	var hid []string
	for k, v := range data {
		if v == "" || !secret(k) {
			continue
		}
		hid = append(hid, v)
		data[k] = hiddenCredential
	}
	sort.Slice(hid, func(i, j int) bool { return len(hid[i]) > len(hid[j]) })
	return hid
}

// hideIn hides in s every copy of the values at least minLen long.
func hideIn(s string, values []string, minLen int) string {
	for _, v := range values {
		if len(v) >= minLen {
			s = strings.ReplaceAll(s, v, hiddenCredential)
		}
	}
	return s
}

// withoutSecrets is note's data and the backends' error as the log keeps
// them: each of note.Secrets hidden, and any copy of one elsewhere — a
// provider may quote the message it refused.
func withoutSecrets(note Notification, errText string) (map[string]string, string) {
	secret := map[string]bool{}
	for _, k := range note.Secrets {
		secret[k] = true
	}
	data := make(map[string]string, len(note.Data))
	for k, v := range note.Data {
		data[k] = v
	}
	hid := hideKeys(data, func(k string) bool { return secret[k] })
	for k, v := range data {
		if !secret[k] {
			data[k] = hideIn(v, hid, minCredentialLen)
		}
	}
	// The error is the provider's text about this message, so a four-digit
	// code it quotes is that code, not a chance match.
	return data, hideIn(errText, hid, 1)
}

// hideNotificationCredentials hides rec's credentials in place for a reader:
// every value whose key names one, every other value's copy of one, every
// credential a link carries in its query, and the same in the error.
func hideNotificationCredentials(rec *NotificationRecord) {
	hid := hideKeys(rec.Data, credentialKey)
	scrub := func(s string) string {
		return queryParam.ReplaceAllStringFunc(hideIn(s, hid, minCredentialLen), func(m string) string {
			p := queryParam.FindStringSubmatch(m)
			if p[3] == "" || !credentialParam(p[2]) {
				return m
			}
			return p[1] + p[2] + "=" + hiddenCredential
		})
	}
	for k, v := range rec.Data {
		if !credentialKey(k) {
			rec.Data[k] = scrub(v)
		}
	}
	rec.Error = scrub(rec.Error)
}

// carriedCredential reports whether a stored message had a credential taken
// out of it, so sending it again would send "(hidden)" where the key was.
func carriedCredential(data map[string]string) bool {
	for _, v := range data {
		if v == hiddenCredential {
			return true
		}
	}
	return false
}

// legacySecrets are the credentials the engine and its modules sent before
// Notification.Secrets existed, which migration 0078 hides in the rows
// already written.
var legacySecrets = []string{
	"reset_token", "reset_code", "reset_url", "invite_url", "invite_token",
	"verify_token", "verify_url", "cart_token", "recovery_url", "code",
}

// migrate0078HideLoggedCredentials hides the credentials in every store's
// existing notification rows, the way record writes new ones. Store by store,
// because row security shows a statement one store's rows.
func migrate0078HideLoggedCredentials(ctx context.Context, tx *sql.Tx) error {
	stores, err := tx.QueryContext(ctx, `SELECT id FROM stores`)
	if err != nil {
		return err
	}
	var ids []string
	for stores.Next() {
		var id string
		if err := stores.Scan(&id); err != nil {
			stores.Close()
			return err
		}
		ids = append(ids, id)
	}
	stores.Close()
	if err := stores.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := hideLoggedCredentialsIn(WithStore(ctx, id), tx); err != nil {
			return err
		}
	}
	return nil
}

func hideLoggedCredentialsIn(ctx context.Context, tx *sql.Tx) error {
	type row struct {
		id     int64
		data   []byte
		errMsg string
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, data, error FROM notifications
		WHERE jsonb_exists_any(data, ARRAY['`+strings.Join(legacySecrets, "', '")+`'])`)
	if err != nil {
		return err
	}
	var found []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.data, &r.errMsg); err != nil {
			rows.Close()
			return err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range found {
		note := Notification{Secrets: legacySecrets}
		if err := json.Unmarshal(r.data, &note.Data); err != nil {
			return err
		}
		data, errMsg := withoutSecrets(note, r.errMsg)
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notifications SET data = $2, error = $3 WHERE id = $1`,
			r.id, raw, errMsg); err != nil {
			return err
		}
	}
	return nil
}
