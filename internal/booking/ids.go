package booking

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
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

// Crockford's alphabet, so a reference read aloud at a counter cannot confuse
// I with 1 or O with 0. 256 is a multiple of 32, so the modulo is unbiased.
const refAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const refGroups, refGroupLen = 2, 4

func newBookingRef() (string, error) {
	b := make([]byte, refGroups*refGroupLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random booking reference: %w", err)
	}

	out := make([]byte, 0, len("CB-")+refGroups*refGroupLen+refGroups-1)
	out = append(out, 'C', 'B')
	for i, v := range b {
		if i%refGroupLen == 0 {
			out = append(out, '-')
		}
		out = append(out, refAlphabet[int(v)%len(refAlphabet)])
	}
	return string(out), nil
}
