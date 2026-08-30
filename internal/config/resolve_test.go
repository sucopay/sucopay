package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

// noEnv is an environment with nothing set.
func noEnv(string) (string, bool) { return "", false }

// envOf returns a [config.Lookup] over a fixed set of variables.
func envOf(vars map[string]string) config.Lookup {
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

func problems(t *testing.T, err error) config.Problems {
	t.Helper()
	if err == nil {
		t.Fatal("want problems, got none")
	}
	var ps config.Problems
	if !errors.As(err, &ps) {
		t.Fatalf("want config.Problems, got %T: %v", err, err)
	}
	return ps
}

func wantProblemAt(t *testing.T, err error, path string) config.Problem {
	t.Helper()
	for _, p := range problems(t, err) {
		if p.Path == path {
			return p
		}
	}
	t.Fatalf("want a problem at %q, got %v", path, err)
	return config.Problem{}
}

func mustResolve(t *testing.T, doc map[string]any, env config.Lookup) config.Resolved {
	t.Helper()
	r, err := config.Resolve(doc, env)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return r
}

func TestResolve_UsesDefaultsWhenTheDocumentIsEmpty(t *testing.T) {
	got := mustResolve(t, map[string]any{}, noEnv).Config

	if got.Listen.Host != config.DefaultHost {
		t.Errorf("host = %q, want %q", got.Listen.Host, config.DefaultHost)
	}
	if got.Listen.Port != config.DefaultPort {
		t.Errorf("port = %d, want %d", got.Listen.Port, config.DefaultPort)
	}
	if want := "http://localhost:7826"; got.Listen.BaseURL != want {
		t.Errorf("base_url = %q, want %q", got.Listen.BaseURL, want)
	}
	if !got.Database.Managed {
		t.Error("database.managed = false, want true")
	}
	if len(got.Networks) != 0 {
		t.Errorf("networks = %v, want none", got.Networks)
	}
}

func TestResolve_BaseURLFollowsThePortWhenOnlyThePortIsSet(t *testing.T) {
	doc := map[string]any{"listen": map[string]any{"port": uint64(9999)}}

	got := mustResolve(t, doc, noEnv).Config

	if want := "http://localhost:9999"; got.Listen.BaseURL != want {
		t.Errorf("base_url = %q, want %q", got.Listen.BaseURL, want)
	}
}

func TestResolve_AReferenceThatDoesNotResolveIsAProblem(t *testing.T) {
	cases := []struct {
		name string
		env  config.Lookup
		says string
	}{
		{"unset", noEnv, "not set"},
		{"empty", envOf(map[string]string{"SUCO_DATABASE_URL": ""}), "empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := map[string]any{"database": map[string]any{
				"managed": false,
				"url":     "${SUCO_DATABASE_URL}",
			}}

			_, err := config.Resolve(doc, c.env)

			p := wantProblemAt(t, err, "database.url")
			if !strings.Contains(p.Message, "SUCO_DATABASE_URL") {
				t.Errorf("message %q does not name the variable", p.Message)
			}
			if !strings.Contains(p.Message, c.says) {
				t.Errorf("message %q does not say %q", p.Message, c.says)
			}
		})
	}
}

func TestResolve_RejectsAReferenceMixedWithLiteralText(t *testing.T) {
	doc := map[string]any{"listen": map[string]any{"base_url": "https://${SUCO_HOST}/pay"}}

	_, err := config.Resolve(doc, envOf(map[string]string{"SUCO_HOST": "example.com"}))

	wantProblemAt(t, err, "listen.base_url")
}

func TestResolve_ReportsEveryProblemAtOnce(t *testing.T) {
	doc := map[string]any{
		"listen":   map[string]any{"port": uint64(70000), "base_url": "not a url"},
		"database": map[string]any{"managed": true, "url": "postgres://x/y"},
		"networks": map[string]any{"local": map[string]any{"kind": "carrier-pigeon"}},
	}

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	for _, path := range []string{"listen.port", "listen.base_url", "database", "networks.local.kind"} {
		wantProblemAt(t, err, path)
	}
	if len(got) < 4 {
		t.Errorf("got %d problems, want at least 4: %v", len(got), got)
	}
}

func TestResolve_NamesTheVariableWhenItsValueHasTheWrongType(t *testing.T) {
	doc := map[string]any{"listen": map[string]any{"port": "${SUCO_LISTEN_PORT}"}}
	env := envOf(map[string]string{"SUCO_LISTEN_PORT": "not-a-number"})

	_, err := config.Resolve(doc, env)

	p := wantProblemAt(t, err, "listen.port")
	if !strings.Contains(p.Message, "SUCO_LISTEN_PORT") {
		t.Errorf("message %q does not name the variable", p.Message)
	}
}

func TestResolve_RecordsWhereEachValueCameFrom(t *testing.T) {
	doc := map[string]any{
		"listen":   map[string]any{"port": uint64(9000)},
		"database": map[string]any{"managed": false, "url": "${SUCO_DATABASE_URL}"},
	}
	env := envOf(map[string]string{"SUCO_DATABASE_URL": "postgres://localhost/suco"})

	got := mustResolve(t, doc, env).Sources

	cases := []struct {
		path   string
		origin config.Origin
		envVar string
	}{
		{"listen.host", config.FromDefault, ""},
		{"listen.port", config.FromFile, ""},
		{"listen.base_url", config.FromDefault, ""},
		{"database.managed", config.FromFile, ""},
		{"database.url", config.FromEnv, "SUCO_DATABASE_URL"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			s := got[c.path]
			if s.Origin != c.origin {
				t.Errorf("origin = %v, want %v", s.Origin, c.origin)
			}
			if s.Var != c.envVar {
				t.Errorf("var = %q, want %q", s.Var, c.envVar)
			}
		})
	}
}

func TestResolve_RejectsAPortOutsideTheValidRange(t *testing.T) {
	cases := []struct {
		name string
		port uint64
	}{
		{"zero", 0},
		{"above the maximum", 65536},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := map[string]any{"listen": map[string]any{"port": c.port}}

			_, err := config.Resolve(doc, noEnv)

			wantProblemAt(t, err, "listen.port")
		})
	}
}

func TestResolve_RejectsABaseURLWithoutAHost(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"no scheme", "localhost:7826"},
		{"unsupported scheme", "ftp://localhost:7826"},
		{"scheme only", "http://"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := map[string]any{"listen": map[string]any{"base_url": c.url}}

			_, err := config.Resolve(doc, noEnv)

			wantProblemAt(t, err, "listen.base_url")
		})
	}
}

func TestResolve_RejectsADatabaseThatIsBothManagedAndGivenAURL(t *testing.T) {
	doc := map[string]any{"database": map[string]any{
		"managed": true,
		"url":     "postgres://localhost/suco",
	}}

	_, err := config.Resolve(doc, noEnv)

	wantProblemAt(t, err, "database")
}

func TestResolve_RequiresAURLWhenTheDatabaseIsNotManaged(t *testing.T) {
	doc := map[string]any{"database": map[string]any{"managed": false}}

	_, err := config.Resolve(doc, noEnv)

	wantProblemAt(t, err, "database.url")
}

func TestResolve_RejectsAnUnknownNetworkKind(t *testing.T) {
	doc := map[string]any{"networks": map[string]any{
		"local": map[string]any{"kind": "evm"},
	}}

	_, err := config.Resolve(doc, noEnv)

	p := wantProblemAt(t, err, "networks.local.kind")
	if !strings.Contains(p.Message, "simulated") {
		t.Errorf("message %q does not name the kinds that are accepted", p.Message)
	}
}

func TestResolve_AcceptsASimulatedNetwork(t *testing.T) {
	doc := map[string]any{"networks": map[string]any{
		"local": map[string]any{"kind": "simulated"},
	}}

	got := mustResolve(t, doc, noEnv).Config

	if got.Networks["local"].Kind != "simulated" {
		t.Errorf("networks.local.kind = %q, want simulated", got.Networks["local"].Kind)
	}
}

func TestResolve_RejectsAnEmptyHost(t *testing.T) {
	doc := map[string]any{"listen": map[string]any{"host": ""}}

	_, err := config.Resolve(doc, noEnv)

	wantProblemAt(t, err, "listen.host")
}

func TestResolve_KeepsTheHostFromTheDocument(t *testing.T) {
	doc := map[string]any{"listen": map[string]any{"host": "0.0.0.0"}}

	got := mustResolve(t, doc, noEnv).Config

	if got.Listen.Host != "0.0.0.0" {
		t.Errorf("host = %q, want 0.0.0.0", got.Listen.Host)
	}
	if want := "http://localhost:7826"; got.Listen.BaseURL != want {
		t.Errorf("base_url = %q, want %q; the bind address is not how others reach it", got.Listen.BaseURL, want)
	}
}

func TestResolve_ReportsTheCauseNotItsConsequence(t *testing.T) {
	doc := map[string]any{"database": map[string]any{
		"managed": false,
		"url":     "${SUCO_DATABASE_URL}",
	}}

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 {
		t.Fatalf("got %d problems, want 1: %v", len(got), got)
	}
	if !strings.Contains(got[0].Message, "SUCO_DATABASE_URL") {
		t.Errorf("message %q does not name the variable that failed to resolve", got[0].Message)
	}
	if strings.Contains(got[0].Message, "required") {
		t.Errorf("message %q reports the consequence instead of the cause", got[0].Message)
	}
}

func TestResolve_NamesAKeyNothingReads(t *testing.T) {
	cases := []struct {
		name string
		doc  map[string]any
		path string
	}{
		{
			name: "misspelt inside a section",
			doc:  map[string]any{"listen": map[string]any{"prot": uint64(9000)}},
			path: "listen.prot",
		},
		{
			name: "at the top level",
			doc:  map[string]any{"lisen": map[string]any{"port": uint64(9000)}},
			path: "lisen.port",
		},
		{
			name: "inside a network",
			doc: map[string]any{"networks": map[string]any{
				"local": map[string]any{"kind": "simulated", "url": "x"},
			}},
			path: "networks.local.url",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := config.Resolve(c.doc, noEnv)

			wantProblemAt(t, err, c.path)
		})
	}
}

func TestResolve_AcceptsADocumentWhereEveryKeyIsRead(t *testing.T) {
	doc := map[string]any{
		"listen":   map[string]any{"host": "0.0.0.0", "port": uint64(9000), "base_url": "https://pay.example"},
		"database": map[string]any{"managed": true},
		"networks": map[string]any{"local": map[string]any{"kind": "simulated", "rpc": ""}},
	}

	mustResolve(t, doc, noEnv)
}

func TestResolve_RejectsANetworkNameWithADot(t *testing.T) {
	doc := map[string]any{"networks": map[string]any{
		"eth.mainnet": map[string]any{"kind": "simulated"},
	}}

	_, err := config.Resolve(doc, noEnv)

	p := wantProblemAt(t, err, "networks")
	if !strings.Contains(p.Message, "eth.mainnet") {
		t.Errorf("message %q does not name the network", p.Message)
	}
}

func TestResolve_DoesNotCallTheKeysOfARejectedNetworkUnknown(t *testing.T) {
	doc := map[string]any{"networks": map[string]any{
		"eth.mainnet": map[string]any{"kind": "simulated", "rpc": "x"},
	}}

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 {
		t.Fatalf("got %d problems, want 1: %v", len(got), got)
	}
	if got[0].Path != "networks" {
		t.Errorf("path = %q, want networks", got[0].Path)
	}
}

// TestResolve_NeverPutsASecretInAProblem walks the paths marked secret through
// every failure that embeds a value. A problem reaches a terminal and a log, so
// one carrying a DSN password or an RPC key is a disclosure.
func TestResolve_NeverPutsASecretInAProblem(t *testing.T) {
	const secret = "hunter2-do-not-print"

	cases := []struct {
		name string
		doc  map[string]any
		env  config.Lookup
	}{
		{
			name: "a reference mixed with literal text at database.url",
			doc: map[string]any{"database": map[string]any{
				"managed": false,
				"url":     "postgres://user:" + secret + "@db/suco${SUFFIX}",
			}},
			env: noEnv,
		},
		{
			name: "a reference mixed with literal text at networks rpc",
			doc: map[string]any{"networks": map[string]any{
				"local": map[string]any{"kind": "simulated", "rpc": "https://rpc/" + secret + "${X}"},
			}},
			env: noEnv,
		},
		{
			name: "a reference naming no variable",
			doc: map[string]any{"database": map[string]any{
				"managed": false,
				"url":     "${}" + secret,
			}},
			env: noEnv,
		},
		{
			name: "a value of the wrong type at a secret path",
			doc: map[string]any{"database": map[string]any{
				"managed": false,
				"url":     map[string]any{secret: "x"},
			}},
			env: noEnv,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := config.Resolve(c.doc, c.env)

			if err == nil {
				t.Fatal("want a problem, got none")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("the secret reached the problem:\n%v", err)
			}
			for _, p := range problems(t, err) {
				if strings.Contains(p.String(), secret) {
					t.Fatalf("the secret reached %s", p.Path)
				}
			}
		})
	}
}

func TestResolve_StillShowsTheValueAtAPathThatIsNotSecret(t *testing.T) {
	doc := map[string]any{"listen": map[string]any{"base_url": "gopher://localhost"}}

	_, err := config.Resolve(doc, noEnv)

	p := wantProblemAt(t, err, "listen.base_url")
	if !strings.Contains(p.Message, "gopher://localhost") {
		t.Errorf("message %q hides a value that is not a secret", p.Message)
	}
}
