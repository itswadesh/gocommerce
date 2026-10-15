package gocommerce

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"time"
)

// crockford is ULID's alphabet: no I, L, O or U, so an id read aloud or
// copied by hand cannot be mistaken for another.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewStoreID returns "store_" + a ULID: 48 bits of milliseconds, then 80
// random bits. The time prefix makes ids sort by creation, which is the
// order a store list is read in; kitcommerce-api's ids have the same shape,
// so a client written for it accepts ours.
func NewStoreID() (string, error) {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli())<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		return "", err
	}
	// 128 bits → 26 base-32 characters, most significant first.
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return "store_" + string(out[:]), nil
}

// ValidStoreID reports whether s has the exact shape NewStoreID produces. It
// gates every id that arrives from outside, so a header can never smuggle
// anything else into a query parameter or a log line.
func ValidStoreID(s string) bool {
	const prefix = "store_"
	if len(s) != len(prefix)+26 || s[:len(prefix)] != prefix {
		return false
	}
	for i := len(prefix); i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') || c == 'I' || c == 'L' || c == 'O' || c == 'U' {
			return false
		}
	}
	return true
}

type storeKey struct{}

// WithStore binds ctx to a store. Every database statement run with the
// returned context is scoped to that store (see storedriver.go).
func WithStore(ctx context.Context, storeID string) context.Context {
	return context.WithValue(ctx, storeKey{}, storeID)
}

// StoreFrom returns the store ctx is bound to, if any.
func StoreFrom(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(storeKey{}).(string)
	return s, ok && s != ""
}
