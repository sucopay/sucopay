package credential

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
)

// Token is a credential as it is presented: 64 hexadecimal characters, being
// 32 bytes from crypto/rand.
//
// It is the text and not the bytes. The text is the only form that goes
// anywhere: it is written to a file for whoever asked for it, it comes back
// in an Authorization header, and it is hashed as it came. Decoding it first
// would give a malformed one a failure of its own, and the answer to a
// credential that is not one is the answer to one that does not exist. So a
// token is its exact text: the same bytes spelled in capitals are another
// token, and one that exists nowhere.
//
// string(t) is the text, for the one place that writes it. fmt and slog are
// given [redacted] instead: see [Token.Format].
type Token string

// tokenBytes is how much crypto/rand goes into a token. Enough that guessing
// one is not a strategy, which is what lets the stored form be a fast hash.
const tokenBytes = 32

// New mints a token.
//
// It takes nothing. A token made from an account, a time, or anything else a
// caller could know is one that caller has a start on. It returns no error:
// crypto/rand.Read has had none to return since Go 1.24, and crashes the
// program rather than fill a token short.
func New() Token {
	var b [tokenBytes]byte
	rand.Read(b[:])
	return Token(hex.EncodeToString(b[:]))
}

// redacted is what a Token or a Key turns into on the way to a log, a
// terminal, or an error.
const redacted = "[redacted]"

// Format is what fmt does with a Token, whatever the verb: it writes
// [redacted] and none of the token. %v in an error is how a token reaches a
// log, and a token in a log is one whoever reads the log holds. String would
// leave %#v and %d, which print a string type as Go syntax and as a complaint
// with the value inside it.
func (t Token) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, redacted) //nolint:errcheck // fmt's own buffer, and nothing to report it to
}

// LogValue is what slog does with a Token: [redacted]. slog's JSON handler
// does not go through fmt; it marshals, and a string type marshals as its
// text. A Key marshals as {}, and needs none of this.
func (t Token) LogValue() slog.Value { return slog.StringValue(redacted) }

// keyBytes is how much material a key holds. [ParseKey] counts the decoded
// bytes against it and not the text, which is twice as long: a count of the
// text against this number would pass half a key.
const keyBytes = 32

// Key is what the stored form of a token is hashed under.
//
// Its bytes are unexported, so that a Key is one [ParseKey] accepted and
// [Hash] has nothing to refuse. The zero Key is a key, the all-zero one, and
// nothing here makes it.
type Key struct {
	bytes [keyBytes]byte
}

// Format is what fmt does with a Key, whatever the verb: it writes [redacted]
// and none of the bytes. Unexported is not hidden. fmt reads a field it cannot
// call methods on, so %v would print all 32 as numbers, and so would an error
// wrapped around one. String would leave %#v and %d, which print them as Go
// syntax and as numbers.
//
// A Key in an unexported field of some other struct prints its bytes all the
// same, since fmt reads that field as it would this one. What this covers is
// the Key on its own.
func (k Key) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, redacted) //nolint:errcheck // fmt's own buffer, and nothing to report it to
}

// ParseKey reads a key as configuration carries it: 64 hexadecimal
// characters, in either case. What it was given is a secret, and its errors
// do not repeat it.
//
// Either case, where payment.ParseID takes lowercase alone: an ID is compared
// as text, and two spellings of one would be two IDs. A key is bytes from
// here on, and its text is not seen again.
func ParseKey(s string) (Key, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return Key{}, errors.New("credential key: not hexadecimal")
	}
	if len(raw) != keyBytes {
		return Key{}, fmt.Errorf("credential key: want %d bytes, got %d", keyBytes, len(raw))
	}
	var k Key
	copy(k.bytes[:], raw)
	return k, nil
}

// Hash is the form a token is stored in: HMAC-SHA256 of the token under the
// key. A row holds this and never the token.
//
// Keyed rather than plain, because a table leaks in ways that a key does not
// go with: a backup in the wrong bucket, a replica with loose grants, a query
// log that keeps its parameters. A plain hash of a token would let whoever
// read the table check a guess at the token on their own. No guess at 32
// random bytes lands, but that rests on [New] staying as it is, and the key
// is for the day it does not.
//
// Not bcrypt or argon2. Those slow down a guess at something a person chose,
// and nothing guesses 32 bytes from crypto/rand, so the milliseconds they
// cost on every request would buy nothing.
func Hash(k Key, t Token) []byte {
	mac := hmac.New(sha256.New, k.bytes[:])
	mac.Write([]byte(t))
	return mac.Sum(nil)
}

// keyIDBytes is how much crypto/rand goes into a key identifier. It has to
// tell one deployment's key from another's in a list that people read, and
// no more.
const keyIDBytes = 8

// NewKeyID mints an identifier for a key: 16 hexadecimal characters, not a
// secret, so that configuration can carry it in plain text.
//
// It takes nothing, and in particular not the key. An identifier derived from
// the key, even through a keyed function, would let whoever read the table
// check a guess at the key against the column, with no credential in hand.
func NewKeyID() string {
	var b [keyIDBytes]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
