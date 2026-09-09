package evm

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The widths a chain writes its values at, in bytes.
const (
	hashBytes    = 32
	addressBytes = 20
	wordBytes    = 32
	// maxTopics is what one log may carry: the event, and three indexed
	// arguments.
	maxTopics = 4
)

// quantity is a number as the chain writes one: 0x, then hexadecimal digits
// with no leading zero.
type quantity uint64

// UnmarshalJSON reads a quantity, refusing anything written another way. A
// provider that writes a number, or pads it, is a provider whose other answers
// are worth doubting too.
//
// The case of the digits is not part of how a number is written: 0xFF and 0xff
// are one value. An address is read either way as well, and nothing here tells
// a correctly cased one from a mistyped one.
func (q *quantity) UnmarshalJSON(b []byte) error {
	text, err := unquote(b)
	if err != nil {
		return fmt.Errorf("quantity: %w", err)
	}
	digits, found := strings.CutPrefix(text, "0x")
	switch {
	case !found:
		return fmt.Errorf("quantity: %q does not start with 0x", cut(text))
	case digits == "":
		return fmt.Errorf("quantity: %q has no digits", cut(text))
	case len(digits) > 1 && digits[0] == '0':
		return fmt.Errorf("quantity: %q starts with a zero", cut(text))
	}
	n, err := strconv.ParseUint(digits, 16, 64)
	if err != nil {
		return fmt.Errorf("quantity: %q is not a number this reads", cut(text))
	}
	*q = quantity(n)
	return nil
}

// MarshalJSON writes a quantity the way the chain reads one.
func (q quantity) MarshalJSON() ([]byte, error) { return json.Marshal(q.String()) }

// String is the quantity as it goes into a request.
func (q quantity) String() string { return "0x" + strconv.FormatUint(uint64(q), 16) }

// hash identifies a block or a transaction.
type hash [hashBytes]byte

// UnmarshalJSON reads a hash of the one width a hash has.
func (h *hash) UnmarshalJSON(b []byte) error { return unmarshalBytes("hash", b, h[:]) }

// MarshalJSON writes a hash the way the chain reads one.
func (h hash) MarshalJSON() ([]byte, error) { return json.Marshal(h.String()) }

// String is the hash as the chain writes it.
func (h hash) String() string { return "0x" + hex.EncodeToString(h[:]) }

// address is an account on the chain.
type address [addressBytes]byte

// UnmarshalJSON reads an address of the one width an address has.
func (a *address) UnmarshalJSON(b []byte) error { return unmarshalBytes("address", b, a[:]) }

// MarshalJSON writes an address the way the chain reads one.
func (a address) MarshalJSON() ([]byte, error) { return json.Marshal(a.String()) }

// String is the address in lower case, which is how everything here compares
// one.
func (a address) String() string { return "0x" + hex.EncodeToString(a[:]) }

// word is one 32-byte slot of a log's data or a contract's storage.
type word [wordBytes]byte

// UnmarshalJSON reads a word of the one width a word has.
func (w *word) UnmarshalJSON(b []byte) error { return unmarshalBytes("word", b, w[:]) }

// String is the word as the chain writes it.
func (w word) String() string { return "0x" + hex.EncodeToString(w[:]) }

// topics are what a log is indexed under: the event, and up to three
// arguments. A log carrying more is not one this reads.
type topics []word

// UnmarshalJSON reads the topics of one log, refusing more than a log has.
func (t *topics) UnmarshalJSON(b []byte) error {
	var read []word
	if err := json.Unmarshal(b, &read); err != nil {
		return fmt.Errorf("topics: %w", err)
	}
	if len(read) > maxTopics {
		return fmt.Errorf("topics: %d of them, at most %d", len(read), maxTopics)
	}
	*t = read
	return nil
}

// unmarshalBytes reads a value of a fixed width out of the string a chain
// writes it as.
func unmarshalBytes(what string, b []byte, into []byte) error {
	text, err := unquote(b)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return decodeHex(what, text, into)
}

// decodeHex reads a value of a fixed width, written as 0x and twice as many
// hexadecimal digits. The digits are read in either case, because the case of
// a digit is not part of the value; the prefix is not, because everything
// that writes one writes it in lower case.
func decodeHex(what, text string, into []byte) error {
	digits, found := strings.CutPrefix(text, "0x")
	if !found {
		return fmt.Errorf("%s: %q does not start with 0x", what, cut(text))
	}
	if len(digits) != 2*len(into) {
		return fmt.Errorf("%s: %d digits, want %d", what, len(digits), 2*len(into))
	}
	if _, err := hex.Decode(into, []byte(digits)); err != nil {
		return fmt.Errorf("%s: %q is not hexadecimal", what, cut(text))
	}
	return nil
}

// unquote reads the string a value is written as.
func unquote(b []byte) (string, error) {
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return "", fmt.Errorf("%s is not a string", cut(string(b)))
	}
	return text, nil
}

// cut keeps a provider's own writing short where it goes into an error.
func cut(s string) string {
	if len(s) > 32 {
		return s[:32] + "..."
	}
	return s
}
