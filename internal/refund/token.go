// Package refund is the side of a refund the deployment serves: the token the
// merchant's signing page is reached by, what that page reads, and what the
// merchant is given to sign.
//
// The page is the merchant's, where suco Checkout's is the payer's. What they
// share is the shape of the token, written out here rather than borrowed, so
// that either page can change without moving the other.
package refund

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

// KeyPurpose is what the key tokens are derived under is derived for.
const KeyPurpose = "suco refund token"

// tokenBytes is how many bytes a token carries, and tokenHex how many
// characters it is written in.
const (
	tokenBytes = 32
	tokenHex   = tokenBytes * 2
)

// Token is what a merchant's URL carries: 32 bytes as 64 hexadecimal
// characters. Whoever holds one can read the refund and what it would take to
// sign it, and nothing else: signing takes the key to the payment's
// destination, which is the merchant's.
//
// Derived from the deployment's key and the refund's identifier rather than
// drawn at random, so that the row holds only a hash of it and the URL can
// still be answered again by a read of the refund. fmt and slog are given
// [redacted].
type Token string

const redacted = "[redacted]"

// Format is what fmt does with a Token, whatever the verb: [redacted].
func (t Token) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, redacted) //nolint:errcheck // fmt's own buffer, and nothing to report it to
}

// LogValue is what slog does with a Token: [redacted].
func (t Token) LogValue() slog.Value { return slog.StringValue(redacted) }

// ParseToken reads a token as a path carries it. Anything but 64 lowercase
// hexadecimal characters is not one, and is told apart from an unknown token
// by nothing: both are answered as not found.
func ParseToken(s string) (Token, error) {
	if len(s) != tokenHex {
		return "", errors.New("refund token: not a token")
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", errors.New("refund token: not a token")
		}
	}
	return Token(s), nil
}

// Derive is the token of one refund under key: HMAC-SHA256 of the refund's
// identifier. The same key and identifier give the same token, which is what
// lets a read of the refund answer with the URL it was made with.
func Derive(key [32]byte, id payment.RefundID) Token {
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

// Links makes a refund's page URL and what its row keeps of the token.
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

// Token is what a refund's row keeps of its page's token.
func (l Links) Token(id payment.RefundID) payment.PageToken {
	return payment.PageToken{Hash: Hash(Derive(l.key, id)), KeyID: l.keyID}
}

// URL is where the merchant signs the refund.
func (l Links) URL(id payment.RefundID) string {
	return l.baseURL + "/refund/" + string(Derive(l.key, id))
}
