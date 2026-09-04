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
