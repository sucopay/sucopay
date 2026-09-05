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
	"regexp"
	"time"
)

// Credential is a stored credential as it is read back: what handling a
// request needs to know about the one presented with it, and none of the
// token. A row holds the token's hash, and nothing reads that back either.
//
// Whose it is is Scope, and never whether Account is empty. Account is empty
// for a credential of the deployment, and a reader going by that would take
// a row whose account was left out for the wider grant.
type Credential struct {
	ID         ID
	Scope      Scope
	Account    AccountID
	Capability Capability
	// KeyID names the key the row was made under. What [Postgres.FindByToken]
	// returns was made under its own, so this is for [Postgres.List], where a
	// row made under another key is the answer to why every request fails.
	KeyID string
	// LastUsedAt is when a request last presented this credential, and zero
	// when none has.
	LastUsedAt time.Time
}

// ID identifies one credential. It is what revoke takes and list shows, so it
// is typed into shells and stays in their histories, and it is derived from
// nothing: not the token and not the hash. Knowing an ID is knowing nothing
// else.
//
// A UUID, because the column is one, and lowercase, because that is how
// PostgreSQL writes one back and an ID is compared as text.
type ID string

// NewID mints an identifier: 16 bytes from crypto/rand, with the version and
// variant bits of a random UUID set so that a tool reading the column sees
// the kind it is. Nothing here reads them.
//
// It returns no error, where payment.NewID returns one it can never have:
// crypto/rand.Read has had none to return since Go 1.24. See [New].
func NewID() ID {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return ID(fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]))
}

// idShape is a UUID as PostgreSQL writes one, and so as [NewID] does.
var idShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ParseID reads an ID as list shows one, refusing anything that is not the
// shape [NewID] produces. Lowercase alone, as payment.ParseID takes: an ID
// is compared as text, and two spellings of one would be two IDs.
//
// Like [ParseKey] it repeats nothing of what it refused. What is typed in an
// ID's place may be the token, by somebody holding the file and not list,
// and an error is what a CI log keeps.
func ParseID(s string) (ID, error) {
	if !idShape.MatchString(s) {
		return "", errors.New("credential ID: not a UUID in lowercase, as list shows one")
	}
	return ID(s), nil
}

// AccountID names the account a credential is of. It is opaque here, as
// payment's is there, and it is not payment's: a credential says which
// account a request is from, and what the request then does with the account
// is for the handler to say, which converts. Sharing the type would have the
// context that authenticates depend on the one that pays.
type AccountID string

// Scope says whose a credential is: one account's, or the deployment's.
//
// A word in the row and a word here, and never the absence of an account.
// Null arrives by omission, and where null meant the wider grant, an insert
// that left the column out would widen a credential without saying so.
type Scope string

const (
	// ScopeAccount is a credential of one account, which it names.
	ScopeAccount Scope = "account"
	// ScopeDeployment is a credential of the whole deployment, naming no
	// account. Nothing makes one yet: what issues credentials issues them to
	// one account, and a row of the deployment is one a test writes by hand.
	ScopeDeployment Scope = "deployment"
)

// Capability says what a credential may do.
type Capability string

const (
	// ReadOnly may read and not write.
	ReadOnly Capability = "read"
	// ReadWrite may write, and so read. There is no write without read: a
	// caller that could create a payment and not see whether it had would
	// create it again.
	ReadWrite Capability = "write"
)

// InForce is what the credentials in force add up to, in one word for
// whatever reports on a deployment. A deployment holding none, or only ones
// that read, is one no client can create a payment in, and one every other
// measure of health finds well.
type InForce string

const (
	// NoneInForce is a deployment with no credential in force: a new one,
	// or one whose every credential was revoked.
	NoneInForce InForce = "none"
	// ReadOnlyInForce is a deployment whose credentials in force may all
	// read and none write.
	ReadOnlyInForce InForce = "read-only"
	// ReadWriteInForce is a deployment where at least one credential in
	// force may write.
	ReadWriteInForce InForce = "read-write"
)

// ErrNotFound reports that no unrevoked credential holds that token under
// this key, or that none has that ID. For a token, whether it was never
// issued, revoked, or issued under another key is not said, on purpose: see
// [Postgres.FindByToken].
var ErrNotFound = errors.New("credential: no such credential")

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

// NewKey mints a key: 32 bytes from crypto/rand, for init to write to a
// file. It takes nothing, as [New] takes nothing.
func NewKey() Key {
	var k Key
	rand.Read(k.bytes[:])
	return k
}

// Hex is a key as configuration carries it, 64 lowercase hexadecimal
// characters, for the file init writes and [ParseKey] reads back. The name
// is one nothing calls on its own: String is what fmt calls, MarshalText
// what slog and encoding/json call, and a Key handed to any of them shows
// nothing of itself.
func (k Key) Hex() string {
	return hex.EncodeToString(k.bytes[:])
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
