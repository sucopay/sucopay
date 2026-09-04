package config_test

import (
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

func TestProblems_ErrorListsEveryProblemOnePerLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   config.Problems
		want []string
	}{
		{
			name: "one problem stays on a single line",
			in:   config.Problems{{Path: "listen.port", Message: "outside 1-65535: 0"}},
			want: []string{"configuration: listen.port: outside 1-65535: 0"},
		},
		{
			name: "several are counted and indented",
			in: config.Problems{
				{Path: "listen.port", Message: "m1"},
				{Path: "database.url", Message: "m2"},
			},
			want: []string{"configuration: 2 problems", "  listen.port: m1", "  database.url: m2"},
		},
		{
			name: "a problem without a path shows only its message",
			in:   config.Problems{{Message: "the document is not a mapping"}},
			want: []string{"configuration: the document is not a mapping"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Split(c.in.Error(), "\n")

			if len(got) != len(c.want) {
				t.Fatalf("got %d lines, want %d:\n%s", len(got), len(c.want), c.in.Error())
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Errorf("line %d = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestProblem_StringCarriesThePathWhenThereIsOne(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   config.Problem
		want string
	}{
		{"with a path", config.Problem{Path: "listen.port", Message: "empty"}, "listen.port: empty"},
		{"without one", config.Problem{Message: "empty"}, "empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.in.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestOrigin_StringNamesTheOriginAndAdmitsAnUnknownOne(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   config.Origin
		want string
	}{
		{config.FromDefault, "default"},
		{config.FromFile, "file"},
		{config.FromEnv, "env"},
		{config.Origin(9), "Origin(9)"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := c.in.String(); got != c.want {
				t.Errorf("String() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSecret_MatchesTheSecretPathsAndNothingElse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"database.url", true},
		{"credentials.key", true},
		{"credentials.key_id", false},
		{"networks.local.rpc", true},
		{"networks.polygon.rpc", true},
		{"listen.port", false},
		{"listen.host", false},
		{"database.managed", false},
		{"networks.local.kind", false},
		{"networks.rpc", false},
		{"database", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := config.Secret(c.path); got != c.want {
				t.Errorf("Secret(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// TestProblem_QuotesAPathThatCarriesControlCharacters keeps a key from forging
// a line of a report. A document names its own keys, and a newline inside one
// would otherwise print as a problem of its own.
func TestProblem_QuotesAPathThatCarriesControlCharacters(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		path  string
		quote bool
	}{
		{"a newline", "listen.a\nlisten.host: 0.0.0.0 is safe", true},
		{"a carriage return", "listen.a\rOVERWRITTEN", true},
		{"an escape sequence", "listen.\x1b[31mRED\x1b[0m", true},
		{"an ordinary key", "listen.port", false},
		{"a key with a space", "listen.a key", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := config.Problem{Path: c.path, Message: "unknown key"}.String()

			if strings.ContainsAny(got, "\n\r\x1b") {
				t.Errorf("the path reached the output unquoted: %q", got)
			}
			if quoted := strings.HasPrefix(got, `"`); quoted != c.quote {
				t.Errorf("quoted = %v, want %v: %s", quoted, c.quote, got)
			}
		})
	}
}

func TestSource_StringNamesTheOriginAndTheVariableBehindIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		source config.Source
		want   string
	}{
		{"default", config.Source{Origin: config.FromDefault}, "default"},
		{"file", config.Source{Origin: config.FromFile}, "file"},
		{"env", config.Source{Origin: config.FromEnv, Var: "SUCO_DATABASE_URL"}, "${SUCO_DATABASE_URL}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.source.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSourceOf_TellsAPathNobodyReadFromOneThatTookItsDefault(t *testing.T) {
	t.Parallel()
	// Both answer with the zero Source, so a misspelt path would otherwise
	// read as a setting that was simply left alone.
	got, err := config.Resolve(map[string]any{}, noEnv)
	if err != nil {
		t.Fatal(err)
	}

	if source, ok := got.SourceOf("listen.port"); !ok || source.Origin != config.FromDefault {
		t.Errorf("listen.port: source %v, read %v; want a recorded default", source, ok)
	}
	if _, ok := got.SourceOf("listen.prot"); ok {
		t.Error("a path nobody read came back as one that was")
	}
}
