package webhook

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Secret is what a delivery to an endpoint is signed under, as Standard
// Webhooks writes one: whsec_ and 32 bytes of base64. It is shown to the
// merchant once, at registration, and read back by nothing but the signer.
//
// string(s) is the text, for the signer and the response that shows it. fmt
// and slog are given [redacted] instead, as a credential's token is.
type Secret string

// secretBytes is how much crypto/rand goes into a secret. The specification
// allows 24 to 64; 32 is what its reference implementation makes.
const secretBytes = 32

// secretPrefix is what marks a secret as one, so that a merchant pasting it
// into the wrong field sees what it is.
const secretPrefix = "whsec_"

// redacted is what a Secret turns into on the way to a log or an error.
const redacted = "[redacted]"

// NewSecret mints a secret. crypto/rand.Read cannot fail on the Go this
// module builds with, as [credential.New] relies on too.
func NewSecret() Secret {
	var b [secretBytes]byte
	rand.Read(b[:])
	return Secret(secretPrefix + base64.StdEncoding.EncodeToString(b[:]))
}

// Format is what fmt does with a Secret, whatever the verb: [redacted].
func (s Secret) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, redacted) //nolint:errcheck // fmt's own buffer, and nothing to report it to
}

// LogValue is what slog does with a Secret: [redacted].
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// Key is the bytes a receiver signs with: the base64 after the prefix,
// decoded, which is what the specification's verification hands to HMAC.
func (s Secret) Key() ([]byte, error) {
	text, found := strings.CutPrefix(string(s), secretPrefix)
	if !found {
		return nil, errors.New("webhook secret: not a secret")
	}
	return base64.StdEncoding.DecodeString(text)
}

// Cipher keeps secrets at rest. AES-256-GCM under a key derived for this
// purpose alone, with a nonce of its own for every secret sealed.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher opens a cipher under key.
func NewCipher(key [32]byte) (Cipher, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return Cipher{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Cipher{}, err
	}
	return Cipher{aead: aead}, nil
}

// Seal is a secret as the row holds it: the nonce, then the ciphertext.
func (c Cipher) Seal(s Secret) []byte {
	nonce := make([]byte, c.aead.NonceSize())
	rand.Read(nonce)
	return c.aead.Seal(nonce, nonce, []byte(s), nil)
}

// Open is the secret a row holds, or an error for bytes this cipher did not
// seal, which is a row written under another key.
func (c Cipher) Open(sealed []byte) (Secret, error) {
	size := c.aead.NonceSize()
	if len(sealed) < size {
		return "", errors.New("webhook secret: sealed under another key")
	}
	text, err := c.aead.Open(nil, sealed[:size], sealed[size:], nil)
	if err != nil {
		return "", errors.New("webhook secret: sealed under another key")
	}
	return Secret(text), nil
}
