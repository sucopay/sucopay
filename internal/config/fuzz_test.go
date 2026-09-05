package config_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

// A configuration document is the one input this package reads that somebody
// else wrote, and it has held a database password twice. What is checked here
// is not that a document parses: most of these will not. It is that failing to
// parse one never crashes the process and never repeats what it read.
// position is the [line:column] a parse error carries.
var position = regexp.MustCompile(`\[\d+:\d+\]`)

func FuzzDecode(f *testing.F) {
	for _, seed := range []string{
		"",
		"listen:\n  port: 7826\n",
		"database:\n  url: postgres://admin:hunter2@db/x\n",
		"listen:\n  port: \"unclosed\n",
		"a: &x [*x, *x]\n",
		"\x00\x01\x02",
		strings.Repeat("a:\n ", 200),
		"listen:\n  host: \"\\u202e\"\n",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, document string) {
		doc, err := config.Decode([]byte(document))

		if err == nil {
			if doc == nil {
				t.Error("decoded a document into nothing, with no error")
			}
			return
		}
		// The document may hold a password. An error made from one reaches a
		// terminal and a log, so nothing of it may be quoted back.
		//
		// Any line holding a colon is a setting, and a short secret under a
		// short key is an ordinary shape: a threshold on length would let
		// `x: hunter2` through.
		// Nothing of the document may come back. The position the parser
		// reports is [line:column], which a document line of "1:1" matches by
		// coincidence rather than by leaking, so it is taken out first.
		//
		// A threshold on length would not do: `x: hunter2` is an ordinary
		// shape, and the parser has been seen quoting fragments as short as
		// three characters into a message of its own.
		message := position.ReplaceAllString(err.Error(), "")
		for _, line := range strings.Split(document, "\n") {
			line = strings.TrimSpace(line)
			if len(line) > 2 && strings.Contains(message, line) {
				t.Errorf("the error repeats part of the document:\n%q\nin\n%q", line, message)
			}
		}
	})
}

// holds reports whether a key or a value in v, as Decode produced it, contains
// marker. The document's text is not the thing to ask: the decoder drops a
// tab inside a plain key, so a text spelling h<tab>unter2 gives the key
// hunter2, and a problem naming that key would look as if it had read the
// variable.
func holds(v any, marker string) bool {
	switch v := v.(type) {
	case string:
		return strings.Contains(v, marker)
	case map[string]any:
		for key, child := range v {
			if strings.Contains(key, marker) || holds(child, marker) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if holds(child, marker) {
				return true
			}
		}
	}
	return false
}

// Resolve reads what Decode produced, and every string in it may be a
// reference to an environment variable. Nothing it is handed should panic it,
// and a problem it reports should never carry a value from a secret setting.
func FuzzResolve(f *testing.F) {
	// secret is what the environment holds under SECRET. Its marker, hunter2,
	// is in no seed, so a problem or a report line carrying it took it from
	// the variable. written is the marker of a secret a seed writes into the
	// document.
	const (
		secret  = "postgres://admin:hunter2@db.internal/x"
		written = "swordfish"
	)
	for _, seed := range []string{
		"listen:\n  port: ${PORT}\n",
		"database:\n  managed: false\n  url: ${SECRET}\n",
		"networks:\n  local:\n    kind: simulated\n    rpc: ${SECRET}\n",
		"listen:\n  host: ${SECRET}\n",
		"listen:\n  host: [\"a\\nb\"]\n",
		"listen:\n  port: [\"a\\u001b[31mb\"]\n",
		"database:\n  managed: {a: \"a\\nb\"}\n",
		// A secret written into the document rather than supplied by the
		// environment: what a variable holds is out of the fuzzer's reach, so
		// nothing it generated could ever exercise the redaction. It carries
		// its own marker, since the fuzzer will move it to a path that is not
		// secret, where quoting it back is right.
		"database:\n  managed: false\n  url: [\"postgres://admin:" + written + "@db/x\"]\n",
		// The credentials key cannot carry the marker: it has to decode as
		// hexadecimal to reach a report at all. What this seed exercises is
		// the other check, that a secret path reads as whether it is set.
		"credentials:\n  key: " + strings.Repeat("ab", 32) + "\n  key_id: k\n",
	} {
		f.Add(seed)
	}

	env := func(name string) (string, bool) {
		if name == "SECRET" {
			return secret, true
		}
		return "", false
	}

	f.Fuzz(func(t *testing.T, document string) {
		doc, err := config.Decode([]byte(document))
		if err != nil {
			return
		}

		resolved, err := config.Resolve(doc, env)

		// The environment's marker has come from the environment only when
		// the document does not hold it. No seed does, but the fuzzer may
		// arrive at it, and a problem naming a key it wrote is right to name
		// it.
		leaked := func(s string) bool {
			return strings.Contains(s, "hunter2") && !holds(doc, "hunter2")
		}
		// The paths are written out rather than taken from config.Secret: the
		// report decides what to hide with that same function, and one list
		// driving both sides would hide a change to it from both at once.
		secretPaths := map[string]bool{"database.url": true, "credentials.key": true}
		if err != nil {
			if leaked(err.Error()) {
				t.Errorf("a problem carries the secret it was given: %v", err)
			}
			for _, p := range problems(t, err) {
				if secretPaths[p.Path] && strings.Contains(p.Message, written) {
					t.Errorf("a problem at a secret path carries what the document wrote there: %v", p)
				}
			}
			// A problem reaches a terminal too, and a document can put a list
			// or a mapping where a number belongs. Problems are listed one per
			// line, so each line is checked rather than the whole message.
			for _, line := range strings.Split(err.Error(), "\n") {
				if invisibleIn(line) {
					t.Errorf("a problem carries something a terminal would act on: %q", line)
				}
			}
			return
		}
		for _, line := range resolved.Report() {
			if leaked(line.Value) {
				t.Errorf("the report shows a secret setting's value: %v", line)
			}
			if secretPaths[line.Path] && line.Value != "set" && line.Value != "not set" {
				t.Errorf("%s reads %q rather than whether it is set", line.Path, line.Value)
			}
			if invisibleIn(line.Path) || invisibleIn(line.Value) {
				t.Errorf("a report line reaches a terminal unquoted: %+v", line)
			}
		}
	})
}

func invisibleIn(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
