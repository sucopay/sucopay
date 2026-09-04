package credential_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sucopay/sucopay/internal/credential"
)

// key is 32 bytes any reader can see: 0x00 through 0x1f.
const key = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func parsed(t *testing.T, s string) credential.Key {
	t.Helper()
	k, err := credential.ParseKey(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestNew_MakesThirtyTwoBytesWrittenAsHexadecimal(t *testing.T) {
	t.Parallel()
	token := credential.New()

	raw, err := hex.DecodeString(string(token))
	if err != nil {
		t.Fatalf("New made %q, which is not hexadecimal: %v", string(token), err)
	}
	if len(raw) != 32 {
		t.Errorf("New made %d bytes, want 32", len(raw))
	}
}

func TestNew_DoesNotRepeatItself(t *testing.T) {
	t.Parallel()
	// A token that came back twice would be one credential held by two
	// callers, and neither would know.
	seen := make(map[credential.Token]bool, 1000)
	for range 1000 {
		token := credential.New()
		if seen[token] {
			t.Fatalf("%q came back twice", string(token))
		}
		seen[token] = true
	}
}

// The compiler holds these already: nothing in this file that calls New,
// NewKeyID or NewID would build if any took an argument. They have names
// because what they say is what the rest rests on: a token, a key's
// identifier and a credential's, derived from nothing a caller could know.
func TestNew_TakesNothing(t *testing.T) {
	t.Parallel()
	takesNothing(t, "New", credential.New)
}

func TestNewKeyID_TakesNothing(t *testing.T) {
	t.Parallel()
	takesNothing(t, "NewKeyID", credential.NewKeyID)
}

func TestNewID_TakesNothing(t *testing.T) {
	t.Parallel()
	takesNothing(t, "NewID", credential.NewID)
}

func takesNothing(t *testing.T, name string, fn any) {
	t.Helper()
	if n := reflect.TypeOf(fn).NumIn(); n != 0 {
		t.Errorf("%s takes %d arguments; what it makes is meant to come from crypto/rand alone", name, n)
	}
}

func TestNewKeyID_DoesNotRepeatItself(t *testing.T) {
	t.Parallel()
	// Two deployments whose keys share an identifier would read a credential
	// made under the other's key as one made under their own.
	seen := make(map[string]bool, 1000)
	for range 1000 {
		id := credential.NewKeyID()
		if seen[id] {
			t.Fatalf("%q came back twice", id)
		}
		seen[id] = true
	}
}

// The column is a uuid, and what PostgreSQL writes back is compared as text
// with what was given, so the text has to be the one form it writes.
var randomUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewID_IsARandomUUIDWrittenInLowercase(t *testing.T) {
	t.Parallel()
	for range 100 {
		if id := credential.NewID(); !randomUUID.MatchString(string(id)) {
			t.Fatalf("NewID made %q", id)
		}
	}
}

func TestNewID_DoesNotRepeatItself(t *testing.T) {
	t.Parallel()
	seen := make(map[credential.ID]bool, 1000)
	for range 1000 {
		id := credential.NewID()
		if seen[id] {
			t.Fatalf("%q came back twice", id)
		}
		seen[id] = true
	}
}

func TestParseKey_AcceptsThirtyTwoBytesOfHexadecimal(t *testing.T) {
	t.Parallel()
	if _, err := credential.ParseKey(key); err != nil {
		t.Errorf("ParseKey refused a key of 32 bytes: %v", err)
	}
}

func TestParseKey_ReadsTheSameKeyInEitherCase(t *testing.T) {
	t.Parallel()
	if parsed(t, strings.ToUpper(key)) != parsed(t, key) {
		t.Error("ParseKey read a key spelled in capitals as another key")
	}
}

func TestParseKey_RefusesAnythingElse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, key string }{
		{"empty", ""},
		{"16 bytes, which 32 characters of hexadecimal are", key[:32]},
		{"31 bytes", key[:62]},
		{"33 bytes", key + "20"},
		{"not hexadecimal", strings.Repeat("z", 64)},
		{"32 bytes and then something else", key + "zz"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := credential.ParseKey(c.key); err == nil {
				t.Errorf("ParseKey accepted %q", c.key)
			}
		})
	}
}

func TestParseKey_KeepsTheKeyOutOfItsError(t *testing.T) {
	t.Parallel()
	// An error is what gets logged. A key of the wrong length is still the
	// key, or most of it.
	for _, s := range []string{key[:62], key + "20", strings.Repeat("z", 64)} {
		_, err := credential.ParseKey(s)
		if err == nil {
			t.Fatalf("ParseKey accepted %q", s)
		}
		if strings.Contains(err.Error(), s[:16]) {
			t.Errorf("the error repeats the key: %v", err)
		}
	}
}

// The Key that TestKey_ShowsNothingOfItself prints is 32 of the byte 0xab,
// and the Token of TestToken_ShowsNothingOfItself is 32 of the text ab. What
// fmt or slog could show of either is then a run of ab, of 171, of the byte
// itself, or of 6162, which is the text ab in hexadecimal. shows looks for
// those in what was written, and not through the methods under test.
func shows(s string) bool {
	return strings.Contains(s, "\xab") || strings.Contains(s, "171") ||
		strings.Contains(s, "6162") || strings.Contains(strings.ToLower(s), "ab")
}

// verbs is every way fmt is asked to print something, with the two that show
// a struct's fields and the one that takes no string among them.
var verbs = []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"}

// slogged is the line each of slog's handlers writes of one attribute. The
// record carries no time, so that a line holds nothing but what was logged.
func slogged(t *testing.T, attr slog.Attr) []string {
	t.Helper()
	var text, json bytes.Buffer
	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "a credential", 0)
	record.AddAttrs(attr)
	for _, h := range []slog.Handler{slog.NewTextHandler(&text, nil), slog.NewJSONHandler(&json, nil)} {
		if err := h.Handle(t.Context(), record); err != nil {
			t.Fatal(err)
		}
	}
	return []string{text.String(), json.String()}
}

func TestKey_ShowsNothingOfItself(t *testing.T) {
	t.Parallel()
	k := parsed(t, strings.Repeat("ab", 32))

	for _, verb := range verbs {
		if got := fmt.Sprintf(verb, k); shows(got) {
			t.Errorf("%s of a Key is %q", verb, got)
		}
	}
	for _, line := range slogged(t, slog.Any("key", k)) {
		if shows(line) {
			t.Errorf("slog wrote %q of a Key", line)
		}
	}
}

func TestToken_ShowsNothingOfItself(t *testing.T) {
	t.Parallel()
	token := credential.Token(strings.Repeat("ab", 32))

	for _, verb := range verbs {
		if got := fmt.Sprintf(verb, token); shows(got) {
			t.Errorf("%s of a Token is %q", verb, got)
		}
	}
	for _, line := range slogged(t, slog.Any("token", token)) {
		if shows(line) {
			t.Errorf("slog wrote %q of a Token", line)
		}
	}
}

// What a row holds is pinned to the byte, because a row written under one
// release has to match under the next. A change here is a change that
// revokes every credential there is.
func TestHash_IsHMACSHA256OfTheTokenUnderTheKey(t *testing.T) {
	t.Parallel()
	token := credential.Token(strings.Repeat("ab", 32))
	want, err := hex.DecodeString("583630234f7e60ecfa2a6ae2e98ee37876e3ff6344457723cb4b42fcf77f7fc0")
	if err != nil {
		t.Fatal(err)
	}

	got := credential.Hash(parsed(t, key), token)

	if !bytes.Equal(got, want) {
		t.Errorf("Hash = %x, want %x", got, want)
	}
}

func TestHash_IsOneValuePerKeyAndToken(t *testing.T) {
	t.Parallel()
	k := parsed(t, key)
	token := credential.New()
	stored := credential.Hash(k, token)

	t.Run("the same again", func(t *testing.T) {
		if got := credential.Hash(k, token); !bytes.Equal(got, stored) {
			t.Errorf("Hash changed between calls: %x, then %x", stored, got)
		}
	})
	t.Run("under another key", func(t *testing.T) {
		other := parsed(t, strings.Repeat("ff", 32))
		if got := credential.Hash(other, token); bytes.Equal(got, stored) {
			t.Error("Hash is the same under another key, which means the key is not in it")
		}
	})
	t.Run("of another token", func(t *testing.T) {
		if got := credential.Hash(k, credential.New()); bytes.Equal(got, stored) {
			t.Error("Hash is the same for another token")
		}
	})
}
