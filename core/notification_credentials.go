package gocommerce

import (
	"regexp"
	"sort"
	"strings"
)

// The notification log is open to anybody who may read orders, and some of
// what the store sends is a key to somebody's account: an operator's reset
// link and code, an invitation, a shopper's reset or confirmation link, a
// basket's token. Nobody reading the log needs another person's key — the
// person it was for has it in their inbox — so every read of the log shows
// those values as hiddenCredential, whoever is reading, the owner included.
//
// The row itself keeps them, because Resend delivers the stored data again to
// the same recipient and a resent reset link must still work. Resend is the
// one reader that sees them (Notifications.stored), and it hands them to the
// delivery backends, never to the caller. Hiding on the way out rather than
// on the way in is also what covers the rows written before this rule.

// hiddenCredential stands in for a value the log does not show. Non-empty on
// purpose: a template that branches on the value ({{if .reset_url}}) previews
// the branch the message took, and an empty value stays empty.
const hiddenCredential = "(hidden)"

// minCredentialLen is the shortest credential looked for inside other values.
// The six-digit reset code is the shortest the engine sends; anything shorter
// would match by accident and hide harmless text.
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

// hideNotificationCredentials hides rec's credentials in place: every value
// whose key names one, every other value's copy of one, every credential a
// link carries in its query, and the same in the error a backend returned —
// a provider may quote the message it refused.
func hideNotificationCredentials(rec *NotificationRecord) {
	var known []string
	for k, v := range rec.Data {
		if v == "" || !credentialKey(k) {
			continue
		}
		if len(v) >= minCredentialLen {
			known = append(known, v)
		}
		rec.Data[k] = hiddenCredential
	}
	// Longest first, so a link is replaced whole before the token inside it.
	sort.Slice(known, func(i, j int) bool { return len(known[i]) > len(known[j]) })
	scrub := func(s string) string {
		for _, c := range known {
			s = strings.ReplaceAll(s, c, hiddenCredential)
		}
		return queryParam.ReplaceAllStringFunc(s, func(m string) string {
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
