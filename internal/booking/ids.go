package booking

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

// 128 bits, base32, unpadded: 26 characters over the alphabet the holds.token
// check constraint allows.
const holdTokenBytes = 16

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func newHoldToken() (string, error) {
	b := make([]byte, holdTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random hold token: %w", err)
	}
	return tokenEncoding.EncodeToString(b), nil
}

// A malformed token cannot name a hold, and some malformed input, such as a
// NUL byte or invalid UTF-8, makes Postgres raise rather than find nothing.
func validHoldToken(s string) bool {
	if len(s) != tokenEncoding.EncodedLen(holdTokenBytes) {
		return false
	}
	for i := range len(s) {
		if c := s[i]; !('A' <= c && c <= 'Z' || '2' <= c && c <= '7') {
			return false
		}
	}
	return true
}

// Crockford's alphabet, so a reference read aloud at a counter cannot confuse
// I with 1 or O with 0. 256 is a multiple of 32, so the modulo is unbiased.
const refAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	refPrefix              = "CB"
	refGroups, refGroupLen = 2, 4
	bookingRefLen          = len(refPrefix) + refGroups*(1+refGroupLen)
)

func newBookingRef() (string, error) {
	b := make([]byte, refGroups*refGroupLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random booking reference: %w", err)
	}

	out := make([]byte, 0, bookingRefLen)
	out = append(out, refPrefix...)
	for i, v := range b {
		if i%refGroupLen == 0 {
			out = append(out, '-')
		}
		out = append(out, refAlphabet[int(v)%len(refAlphabet)])
	}
	return string(out), nil
}

func validBookingRef(s string) bool {
	if len(s) != bookingRefLen || !strings.HasPrefix(s, refPrefix) {
		return false
	}
	for i := len(refPrefix); i < len(s); i++ {
		if (i-len(refPrefix))%(1+refGroupLen) == 0 {
			if s[i] != '-' {
				return false
			}
		} else if strings.IndexByte(refAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}
