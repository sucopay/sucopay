package config_test

import (
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

func lineAt(t *testing.T, lines []config.ReportLine, path string) config.ReportLine {
	t.Helper()
	for _, l := range lines {
		if l.Path == path {
			return l
		}
	}
	t.Fatalf("want a line for %q, got %#v", path, lines)
	return config.ReportLine{}
}

func TestReport_ReducesASecretToWhetherItIsSet(t *testing.T) {
	t.Parallel()
	const dsn = "postgres://user:hunter2@db.internal/suco"
	cases := []struct {
		name string
		doc  map[string]any
		env  config.Lookup
		want string
	}{
		{
			name: "supplied",
			doc:  withKey(map[string]any{"database": map[string]any{"managed": false, "url": "${SUCO_DATABASE_URL}"}}),
			env:  envOf(map[string]string{"SUCO_DATABASE_URL": dsn}),
			want: "set",
		},
		{
			name: "absent",
			doc:  map[string]any{},
			env:  noEnv,
			want: "not set",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := mustResolve(t, c.doc, c.env).Report()

			line := lineAt(t, lines, "database.url")
			if !line.Secret {
				t.Error("database.url is not marked secret")
			}
			if line.Value != c.want {
				t.Errorf("value = %q, want %q", line.Value, c.want)
			}
			for _, l := range lines {
				if strings.Contains(l.Value, "hunter2") {
					t.Fatalf("the secret appears in the report at %s", l.Path)
				}
			}
		})
	}
}

func TestReport_KeepsTheVariableNameOfASecretVisible(t *testing.T) {
	t.Parallel()
	doc := withKey(map[string]any{"database": map[string]any{"managed": false, "url": "${SUCO_DATABASE_URL}"}})
	env := envOf(map[string]string{"SUCO_DATABASE_URL": "postgres://localhost/suco"})

	lines := mustResolve(t, doc, env).Report()

	if got := lineAt(t, lines, "database.url").Source.Var; got != "SUCO_DATABASE_URL" {
		t.Errorf("source var = %q, want the variable name to stay visible", got)
	}
}

func TestReport_ShowsTheValueAndSourceForEverythingElse(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"listen": map[string]any{"port": "${SUCO_LISTEN_PORT}"}}
	env := envOf(map[string]string{"SUCO_LISTEN_PORT": "9000"})

	lines := mustResolve(t, doc, env).Report()

	port := lineAt(t, lines, "listen.port")
	if port.Secret {
		t.Error("listen.port is marked secret")
	}
	if port.Value != "9000" {
		t.Errorf("value = %q, want %q", port.Value, "9000")
	}
	if port.Source.Origin != config.FromEnv || port.Source.Var != "SUCO_LISTEN_PORT" {
		t.Errorf("source = %#v, want env SUCO_LISTEN_PORT", port.Source)
	}

	base := lineAt(t, lines, "listen.base_url")
	if base.Source.Origin != config.FromDefault {
		t.Errorf("base_url origin = %v, want default", base.Source.Origin)
	}
}

func TestReport_IsSortedByPath(t *testing.T) {
	t.Parallel()
	lines := mustResolve(t, map[string]any{}, noEnv).Report()

	for i := 1; i < len(lines); i++ {
		if lines[i-1].Path > lines[i].Path {
			t.Fatalf("line %d (%s) sorts after %s", i, lines[i].Path, lines[i-1].Path)
		}
	}
}

// A report is laid out in columns, and a value holding a newline and a tab
// writes a row of its own, which reads as a setting nobody set.
//
// The value, and not the path: every path a report holds is either written
// here or built from a name the document chose, and such a name is refused
// before it becomes one. What a problem is written under is another matter,
// and TestProblem_QuotesAPathThatCarriesControlCharacters is about that.
func TestReport_QuotesAValueThatWouldOtherwiseForgeALine(t *testing.T) {
	t.Parallel()
	got, err := config.Resolve(map[string]any{
		"listen": map[string]any{"host": "127.0.0.1\n  listen.port\t1\tdefault"},
	}, noEnv)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	for _, line := range got.Report() {
		if strings.ContainsAny(line.Value, "\n\t\x1b") {
			t.Errorf("value %q reaches a report unquoted", line.Value)
		}
	}
}

// withKey adds the credentials a document naming a database has to carry, so
// that a case about something else does not have to say so itself.
func withKey(doc map[string]any) map[string]any {
	doc["credentials"] = map[string]any{"key": testKey, "key_id": "testkey"}
	return doc
}

// The credentials key is held to the same rule as the database URL. Sharing
// the code that hides them is why this passes; sharing it is also why nothing
// would notice if the paths stopped matching.
func TestReport_ReducesTheCredentialsKeyToWhetherItIsSet(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"credentials": map[string]any{"key": testKey, "key_id": "testkey"}}

	lines := mustResolve(t, doc, noEnv).Report()

	if got := lineAt(t, lines, "credentials.key").Value; got != "set" {
		t.Errorf("credentials.key reads %q, want set", got)
	}
	if got := lineAt(t, lines, "credentials.key_id").Value; got != "testkey" {
		t.Errorf("credentials.key_id reads %q, want its value", got)
	}
}
