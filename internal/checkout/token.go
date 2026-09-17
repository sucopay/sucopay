// Package checkout is the side of suco Checkout the deployment serves: the
// token a payer's page is reached by, what the page reads, and what a payer
// is given to sign.
package checkout

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/sucopay/sucopay/internal/payment"
)

// KeyPurpose is what the key tokens are derived under is derived for. A
// constant: a token derived under a key derived for one name is found under
// that name alone.
const KeyPurpose = "suco checkout token"

// tokenBytes is how many bytes a token carries, and tokenHex how many
// characters it is written in.
const (
	tokenBytes = 32
	tokenHex   = tokenBytes * 2
)

// Token is what a payer's URL carries: 32 bytes as 64 hexadecimal
// characters. Whoever holds one can read the payment's outcome and, while
// the payment is payable, be given something to sign, and nothing else.
//
// Derived from the deployment's key and the payment's id rather than drawn
// at random, so that the row holds only a hash of it and the URL can still
// be answered again by a read of the payment. fmt and slog are given
// [redacted], as a credential's token is.
type Token string

const redacted = "[redacted]"

// Format is what fmt does with a Token, whatever the verb: [redacted].
func (t Token) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, redacted) //nolint:errcheck // fmt's own buffer, and nothing to report it to
}

// LogValue is what slog does with a Token: [redacted].
func (t Token) LogValue() slog.Value { return slog.StringValue(redacted) }

// ParseToken reads a token as a path carries it. Anything but 64 lowercase
// hexadecimal characters is not one, and is told apart from an unknown
// token by nothing: both are answered as not found.
func ParseToken(s string) (Token, error) {
	if len(s) != tokenHex {
		return "", errors.New("checkout token: not a token")
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", errors.New("checkout token: not a token")
		}
	}
	return Token(s), nil
}

// Derive is the token of one payment under key: HMAC-SHA256 of the
// payment's id. The same key and id give the same token, which is what lets
// a read of the payment answer with the URL it was made with.
func Derive(key [32]byte, id payment.ID) Token {
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(id))
	return Token(hex.EncodeToString(mac.Sum(nil)))
}

// Hash is a token as a row holds it. SHA-256 alone: the token is itself the
// output of a keyed function, so a row read on its own gives nothing to
// invert and nothing to guess against.
func Hash(t Token) []byte {
	sum := sha256.Sum256([]byte(t))
	return sum[:]
}

// Links makes a payment's checkout URL and what its row keeps of the token,
// for the payment package, which declares what it takes from this as
// [payment.Links].
type Links struct {
	key     [32]byte
	keyID   string
	baseURL string
}

// NewLinks derives tokens under key, marked as derived under keyID, and
// writes URLs under baseURL, with or without its trailing slash.
func NewLinks(key [32]byte, keyID, baseURL string) Links {
	return Links{key: key, keyID: keyID, baseURL: strings.TrimRight(baseURL, "/")}
}

// Checkout is what a payment's row keeps of its token: the hash, and which
// key derived it.
func (l Links) Checkout(id payment.ID) payment.Checkout {
	return payment.Checkout{Hash: Hash(Derive(l.key, id)), KeyID: l.keyID}
}

// CheckoutURL is where a merchant sends the payer of the payment.
func (l Links) CheckoutURL(id payment.ID) string {
	return l.baseURL + "/checkout/" + string(Derive(l.key, id))
}
