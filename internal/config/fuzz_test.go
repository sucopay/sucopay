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

// Resolve reads what Decode produced, and every string in it may be a
// reference to an environment variable. Nothing it is handed should panic it,
// and a problem it reports should never carry a value from a secret setting.
func FuzzResolve(f *testing.F) {
	for _, seed := range []string{
		"listen:\n  port: ${PORT}\n",
		"database:\n  managed: false\n  url: ${SECRET}\n",
		"networks:\n  local:\n    kind: simulated\n    rpc: ${SECRET}\n",
		"listen:\n  host: ${SECRET}\n",
		"listen:\n  host: [\"a\\nb\"]\n",
		"listen:\n  port: [\"a\\u001b[31mb\"]\n",
		"database:\n  managed: {a: \"a\\nb\"}\n",
		// The secret written into the document rather than supplied by the
		// environment: what a variable holds is out of the fuzzer's reach, so
		// nothing it generated could ever exercise the redaction.
		"database:\n  managed: false\n  url: [\"postgres://admin:hunter2@db/x\"]\n",
	} {
		f.Add(seed)
	}

	const secret = "postgres://admin:hunter2@db.internal/x"
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

		if err != nil {
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("a problem carries the secret it was given: %v", err)
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
		// The paths are written out rather than taken from config.Secret: the
		// report decides what to hide with that same function, and one list
		// driving both sides would hide a change to it from both at once.
		secretPaths := map[string]bool{"database.url": true}
		for _, line := range resolved.Report() {
			if strings.Contains(line.Value, "hunter2") {
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
