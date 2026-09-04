package credential_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/credential"
)

// hexKey is what a key is: 64 hexadecimal characters. Written out, so that
// ParseKey is not asked whether ParseKey is right.
var hexKey = regexp.MustCompile(`\A[0-9a-fA-F]{64}\z`)

// uuid is what an ID is: a UUID in lowercase, as NewID writes one. Written
// out, as hexKey is.
var uuid = regexp.MustCompile(`\A[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\z`)

// A key comes from configuration, where whoever wrote it may have pasted the
// wrong thing, and the wrong thing is a secret too.
func FuzzParseKey(f *testing.F) {
	for _, seed := range []string{
		"", key, strings.ToUpper(key), key[:32], key[:62], key + "20", key + "zz",
		strings.Repeat("z", 64), "\x00", "postgres://admin:hunter2@db/x", "a\u202eb",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		_, err := credential.ParseKey(s)

		if err == nil && !hexKey.MatchString(s) {
			t.Errorf("ParseKey accepted %q, which is not 64 characters of hexadecimal", s)
		}
		if err != nil && hexKey.MatchString(s) {
			t.Errorf("ParseKey refused a key: %v", err)
		}
		// A key of the wrong length is still the key, or most of it. Sixteen
		// characters, because the messages hold no run of hexadecimal that
		// long, and a message that quotes what it was given quotes the start.
		if err != nil && len(s) >= 16 && strings.Contains(err.Error(), s[:16]) {
			t.Errorf("the error repeats what it was given: %v", err)
		}
	})
}

// An ID is typed after revoke, and what is typed there may be the token: the
// file next to the command holds one, and list is a step away.
func FuzzParseID(f *testing.F) {
	minted := string(credential.NewID())
	for _, seed := range []string{
		"", minted, strings.ToUpper(minted), minted[1:], minted + "\n", " " + minted,
		"credential-" + minted + ".token", string(credential.New()), "\x00", "a\u202eb",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		id, err := credential.ParseID(s)

		if err == nil && !uuid.MatchString(s) {
			t.Errorf("ParseID accepted %q, which is not a UUID in lowercase", s)
		}
		if err != nil && uuid.MatchString(s) {
			t.Errorf("ParseID refused an ID: %v", err)
		}
		if err == nil && string(id) != s {
			t.Errorf("ParseID(%q) = %q", s, id)
		}
		// Sixteen characters, as FuzzParseKey reads: the message holds no
		// run of hexadecimal that long.
		if err != nil && len(s) >= 16 && strings.Contains(err.Error(), s[:16]) {
			t.Errorf("the error repeats what it was given: %v", err)
		}
	})
}
