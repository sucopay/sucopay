package config_test

import (
	"errors"
	"fmt"
	"slices"
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
	t.Parallel()
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
	t.Parallel()
	doc := map[string]any{"listen": map[string]any{"port": uint64(9999)}}

	got := mustResolve(t, doc, noEnv).Config

	if want := "http://localhost:9999"; got.Listen.BaseURL != want {
		t.Errorf("base_url = %q, want %q", got.Listen.BaseURL, want)
	}
}

func TestResolve_AReferenceThatDoesNotResolveIsAProblem(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	doc := map[string]any{"listen": map[string]any{"base_url": "https://${SUCO_HOST}/pay"}}

	_, err := config.Resolve(doc, envOf(map[string]string{"SUCO_HOST": "example.com"}))

	wantProblemAt(t, err, "listen.base_url")
}

func TestResolve_ReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()
	doc := map[string]any{
		"listen":   map[string]any{"port": uint64(70000), "base_url": "not a url"},
		"database": map[string]any{"managed": true, "url": "postgres://x/y"},
		"networks": map[string]any{"local": map[string]any{"kind": "carrier-pigeon"}},
	}

	_, err := config.Resolve(doc, noEnv)

	want := []string{"database", "listen.base_url", "listen.port", "networks.local.kind"}
	var got []string
	for _, p := range problems(t, err) {
		got = append(got, p.Path)
	}
	if !slices.Equal(got, want) {
		t.Errorf("problem paths = %v, want exactly %v", got, want)
	}
}

func TestResolve_NamesTheVariableWhenItsValueHasTheWrongType(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"listen": map[string]any{"port": "${SUCO_LISTEN_PORT}"}}
	env := envOf(map[string]string{"SUCO_LISTEN_PORT": "not-a-number"})

	_, err := config.Resolve(doc, env)

	p := wantProblemAt(t, err, "listen.port")
	if !strings.Contains(p.Message, "SUCO_LISTEN_PORT") {
		t.Errorf("message %q does not name the variable", p.Message)
	}
}

// A document may put a variable at any setting, and the variable may hold a
// secret the document meant for another one. A problem about the setting then
// says which variable to look at, and nothing of what it holds.
func TestResolve_NamesTheVariableRatherThanWhatItHoldsInAProblem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path  string
		doc   map[string]any
		value string
	}{
		{"database.managed", map[string]any{"database": map[string]any{"managed": "${SECRET}"}}, "hunter2"},
		{"listen.port", map[string]any{"listen": map[string]any{"port": "${SECRET}"}}, "hunter2"},
		{"listen.base_url", map[string]any{"listen": map[string]any{"base_url": "${SECRET}"}}, "gopher://hunter2"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			t.Parallel()
			env := envOf(map[string]string{"SECRET": c.value})

			_, err := config.Resolve(c.doc, env)

			p := wantProblemAt(t, err, c.path)
			if !strings.Contains(p.Message, "SECRET") {
				t.Errorf("message %q does not name the variable", p.Message)
			}
			if strings.Contains(p.Message, "hunter2") {
				t.Errorf("message %q carries what the variable holds", p.Message)
			}
		})
	}
}

func TestResolve_RecordsWhereEachValueCameFrom(t *testing.T) {
	t.Parallel()
	doc := map[string]any{
		"listen":      map[string]any{"port": uint64(9000)},
		"database":    map[string]any{"managed": false, "url": "${SUCO_DATABASE_URL}"},
		"credentials": map[string]any{"key": testKey, "key_id": "testkey"},
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
	t.Parallel()
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

func TestResolve_RejectsABaseURLThatIsNotReachableOverHTTP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		url  string
		says string
	}{
		{"no scheme", "localhost:7826", "scheme"},
		{"unsupported scheme", "ftp://localhost:7826", "scheme"},
		{"scheme with no host", "http://", "host"},
		{"port with no host", "http://:7826", "host"},
		{"not a URL at all", "://x", "not a URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := map[string]any{"listen": map[string]any{"base_url": c.url}}

			_, err := config.Resolve(doc, noEnv)

			p := wantProblemAt(t, err, "listen.base_url")
			if !strings.Contains(p.Message, c.says) {
				t.Errorf("message %q does not say %q", p.Message, c.says)
			}
		})
	}
}

func TestResolve_RejectsADatabaseThatIsBothManagedAndGivenAURL(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"database": map[string]any{
		"managed": true,
		"url":     "postgres://localhost/suco",
	}}

	_, err := config.Resolve(doc, noEnv)

	wantProblemAt(t, err, "database")
}

func TestResolve_RequiresAURLWhenTheDatabaseIsNotManaged(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"database": map[string]any{"managed": false}}

	_, err := config.Resolve(doc, noEnv)

	wantProblemAt(t, err, "database.url")
}

func TestResolve_RejectsAnUnknownNetworkKind(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"networks": map[string]any{
		"local": map[string]any{"kind": "carrier-pigeon"},
	}}

	_, err := config.Resolve(doc, noEnv)

	p := wantProblemAt(t, err, "networks.local.kind")
	for _, kind := range []string{"evm", "simulated"} {
		if !strings.Contains(p.Message, kind) {
			t.Errorf("message %q does not name %s among the kinds that are accepted", p.Message, kind)
		}
	}
	if got := problems(t, err); len(got) != 1 {
		t.Errorf("got %d problems, want the kind alone: %v", len(got), got)
	}
}

func TestResolve_AcceptsASimulatedNetwork(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"networks": map[string]any{
		"local": map[string]any{"kind": "simulated"},
	}}

	got := mustResolve(t, doc, noEnv).Config

	if got.Networks["local"].Kind != "simulated" {
		t.Errorf("networks.local.kind = %q, want simulated", got.Networks["local"].Kind)
	}
}

func TestResolve_RejectsAnEmptyHost(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"listen": map[string]any{"host": ""}}

	_, err := config.Resolve(doc, noEnv)

	wantProblemAt(t, err, "listen.host")
}

func TestResolve_KeepsTheHostFromTheDocument(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	doc := map[string]any{
		"listen":   map[string]any{"host": "0.0.0.0", "port": uint64(9000), "base_url": "https://pay.example"},
		"database": map[string]any{"managed": true},
		"networks": map[string]any{"polygon": map[string]any{
			"kind": "evm", "chain_id": uint64(137), "poll": "3s", "width": uint64(1000),
			"rpc": map[string]any{
				"own":    "https://bor.internal:8545",
				"others": []any{"https://polygon.example/"},
			},
		}},
	}

	got := mustResolve(t, doc, noEnv)

	for _, path := range []string{
		"listen.host", "listen.port", "listen.base_url",
		"database.managed", "database.url",
		"networks.polygon.kind", "networks.polygon.chain_id",
		"networks.polygon.rpc.own", "networks.polygon.rpc.others", "networks.polygon.rpc.others[0]",
		"networks.polygon.poll", "networks.polygon.width",
	} {
		if _, ok := got.Sources[path]; !ok {
			t.Errorf("no source recorded for %s", path)
		}
	}
}

func TestResolve_RejectsANetworkNameWithADot(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
			name: "a reference mixed with literal text at an own endpoint",
			doc: map[string]any{"networks": map[string]any{
				"polygon": map[string]any{"chain_id": uint64(137),
					"rpc": map[string]any{"own": "https://rpc/" + secret + "${X}"}},
			}},
			env: noEnv,
		},
		{
			name: "an endpoint of others the URL rule refuses",
			doc: map[string]any{"networks": map[string]any{
				"polygon": map[string]any{"chain_id": uint64(137),
					"rpc": map[string]any{"others": []any{"http://user:" + secret + "@rpc.example/"}}},
			}},
			env: noEnv,
		},
		{
			name: "an rpc that is not a URL",
			doc: map[string]any{"networks": map[string]any{
				"polygon": map[string]any{"chain_id": uint64(137), "rpc": "http://[::1]-not:8545/" + secret},
			}},
			env: noEnv,
		},
		{
			name: "an rpc with no host",
			doc: map[string]any{"networks": map[string]any{
				"polygon": map[string]any{"chain_id": uint64(137), "rpc": "https:///" + secret},
			}},
			env: noEnv,
		},
		{
			name: "an rpc on a kind that has no such setting",
			doc: map[string]any{"networks": map[string]any{
				"local": map[string]any{"kind": "simulated", "rpc": "https://rpc/" + secret},
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
	t.Parallel()
	doc := map[string]any{"listen": map[string]any{"base_url": "gopher://localhost"}}

	_, err := config.Resolve(doc, noEnv)

	p := wantProblemAt(t, err, "listen.base_url")
	if !strings.Contains(p.Message, "gopher://localhost") {
		t.Errorf("message %q hides a value that is not a secret", p.Message)
	}
}

func TestResolve_AcceptsThePortsAtTheEdgesOfTheRange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		port uint64
	}{
		{"the lowest", 1},
		{"the highest", 65535},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc := map[string]any{"listen": map[string]any{"port": c.port}}

			got := mustResolve(t, doc, noEnv).Config

			if got.Listen.Port != int(c.port) {
				t.Errorf("port = %d, want %d", got.Listen.Port, c.port)
			}
		})
	}
}

func TestResolve_RejectsAValueOfTheWrongShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		doc  map[string]any
		path string
	}{
		{
			name: "text where a number belongs",
			doc:  map[string]any{"listen": map[string]any{"port": "seven"}},
			path: "listen.port",
		},
		{
			name: "a number where text belongs",
			doc:  map[string]any{"listen": map[string]any{"host": uint64(8080)}},
			path: "listen.host",
		},
		{
			name: "text that is not true or false",
			doc:  map[string]any{"database": map[string]any{"managed": "maybe"}},
			path: "database.managed",
		},
		{
			name: "a number where true or false belongs",
			doc:  map[string]any{"database": map[string]any{"managed": uint64(1)}},
			path: "database.managed",
		},
		{
			name: "a list where networks belong",
			doc:  map[string]any{"networks": []any{"local"}},
			path: "networks",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := config.Resolve(c.doc, noEnv)

			wantProblemAt(t, err, c.path)
		})
	}
}

func TestResolve_ReadsTrueAndFalseWrittenAsText(t *testing.T) {
	t.Parallel()
	doc := map[string]any{
		"credentials": map[string]any{"key": testKey, "key_id": "testkey"},
		"database": map[string]any{
			"managed": "false",
			"url":     "postgres://localhost/suco",
		}}

	got := mustResolve(t, doc, noEnv).Config

	if got.Database.Managed {
		t.Error("database.managed = true, want false")
	}
}

func TestResolve_KeepsOneNetworksProblemOutOfAnother(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"networks": map[string]any{
		"good": map[string]any{"kind": "simulated"},
		"bad":  map[string]any{"kind": "carrier-pigeon"},
	}}

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 {
		t.Fatalf("got %d problems, want 1: %v", len(got), got)
	}
	if got[0].Path != "networks.bad.kind" {
		t.Errorf("path = %q, want networks.bad.kind", got[0].Path)
	}
}

func TestResolve_ResolvesEveryNetworkInTheDocument(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"networks": map[string]any{
		"one": map[string]any{"kind": "simulated"},
		"two": map[string]any{"kind": "simulated"},
	}}

	got := mustResolve(t, doc, noEnv).Config

	if len(got.Networks) != 2 {
		t.Fatalf("got %d networks, want 2: %v", len(got.Networks), got.Networks)
	}
	for _, name := range []string{"one", "two"} {
		if got.Networks[name].Kind != "simulated" {
			t.Errorf("networks.%s.kind = %q, want simulated", name, got.Networks[name].Kind)
		}
	}
}

func TestResolve_RejectsABaseURLCarryingCredentials(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"listen": map[string]any{
		"base_url": "http://admin:PASSWORD@pay.example.com",
	}}

	_, err := config.Resolve(doc, noEnv)

	p := wantProblemAt(t, err, "listen.base_url")
	if strings.Contains(p.Message, "PASSWORD") {
		t.Fatalf("the credentials reached the message: %v", p)
	}
}

// TestResolve_StopsListingProblemsPastTheLimit keeps a document of many
// unreadable keys from turning into a report nobody can read and a string
// nobody asked to hold in memory.
func TestResolve_StopsListingProblemsPastTheLimit(t *testing.T) {
	t.Parallel()
	keys := map[string]any{}
	for i := range 500 {
		keys[fmt.Sprintf("k%d", i)] = 1
	}

	_, err := config.Resolve(keys, noEnv)

	got := problems(t, err)
	if len(got) > 60 {
		t.Errorf("got %d problems, want the report capped", len(got))
	}
	if !strings.Contains(err.Error(), "more problems follow") {
		t.Errorf("the report does not say it was cut short:\n%v", err)
	}
}

func TestResolve_RefusesCredentialsAtASettingThatIsNotSecret(t *testing.T) {
	t.Parallel()
	// A document names any variable at any key, and a report prints in full
	// whatever reaches a key that is not secret. Refusing the value is what
	// keeps a password out of the report.
	const dsn = "postgres://admin:hunter2@db.internal/sucopay"

	for _, c := range []struct {
		name string
		doc  map[string]any
	}{
		{"written in the document", map[string]any{
			"listen": map[string]any{"host": dsn},
		}},
		{"supplied by a variable", map[string]any{
			"listen": map[string]any{"host": "${SUCO_DATABASE_URL}"},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := config.Resolve(c.doc, envOf(map[string]string{"SUCO_DATABASE_URL": dsn}))

			p := wantProblemAt(t, err, "listen.host")
			if !strings.Contains(p.Message, "username or password") {
				t.Errorf("message = %q, want it to name what it refused", p.Message)
			}
			if strings.Contains(p.String(), "hunter2") {
				t.Errorf("the problem carries the password: %s", p.String())
			}
		})
	}
}

func TestResolve_AcceptsCredentialsAtASecretSetting(t *testing.T) {
	t.Parallel()
	const dsn = "postgres://admin:hunter2@db.internal/sucopay"

	got, err := config.Resolve(map[string]any{
		"database":    map[string]any{"managed": false, "url": dsn},
		"credentials": map[string]any{"key": testKey, "key_id": "testkey"},
	}, noEnv)

	if err != nil {
		t.Fatalf("err = %v, want none: a database URL is where credentials belong", err)
	}
	if got.Config.Database.URL.Expose() != dsn {
		t.Errorf("database.url = %q, want it kept whole", got.Config.Database.URL.Expose())
	}
}

func TestResolve_RefusesAReferenceToSomethingThatIsNotAVariableName(t *testing.T) {
	t.Parallel()
	// The name is printed back in the report and in the message naming an
	// unset variable, so it may hold only what a variable name holds.
	for _, name := range []string{
		"A\n  listen.base_url: looks fine",
		"A\x1b[2K\rlisten.host is fine",
		"HAS-A-DASH",
		"1STARTS_WITH_A_DIGIT",
		"HAS A SPACE",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Resolve(map[string]any{
				"listen": map[string]any{"host": "${" + name + "}"},
			}, envOf(map[string]string{name: "127.0.0.1"}))

			p := wantProblemAt(t, err, "listen.host")
			if strings.Contains(p.String(), "\n") {
				t.Errorf("the problem spans more than its own line: %q", p.String())
			}
			if strings.Contains(p.String(), "\x1b") {
				t.Errorf("the problem carries an escape sequence: %q", p.String())
			}
		})
	}
}

func TestResolve_RefusesALogLevelOrFormatNothingDefines(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		doc  map[string]any
		path string
	}{
		{"a level", map[string]any{"log": map[string]any{"level": "loud"}}, "log.level"},
		{"a format", map[string]any{"log": map[string]any{"format": "yaml"}}, "log.format"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Resolve(c.doc, noEnv)

			p := wantProblemAt(t, err, c.path)
			if !strings.Contains(p.Message, "want one of") {
				t.Errorf("message = %q, want it to name what it accepts", p.Message)
			}
		})
	}
}

func TestResolve_DefaultsToInfoAndText(t *testing.T) {
	t.Parallel()
	got, err := config.Resolve(map[string]any{}, noEnv)
	if err != nil {
		t.Fatal(err)
	}

	if got.Config.Log.Level != "info" || got.Config.Log.Format != "text" {
		t.Errorf("log = %+v, want info and text", got.Config.Log)
	}
}

// testKey is 32 bytes as the setting carries them.
const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// withDatabase returns a document naming a database, which is what makes the
// credentials key required.
func withDatabase(credentials map[string]any) map[string]any {
	doc := map[string]any{"database": map[string]any{
		"managed": false,
		"url":     "postgres://localhost/suco",
	}}
	if credentials != nil {
		doc["credentials"] = credentials
	}
	return doc
}

func TestResolve_RequiresACredentialsKeyWhenThereIsADatabase(t *testing.T) {
	t.Parallel()
	_, err := config.Resolve(withDatabase(nil), noEnv)

	wantProblemAt(t, err, "credentials.key")
}

// Without a database there is no table to hold a credential and nothing to
// hash, so a deployment that only answers /healthz does not have to be handed
// a secret before it will start.
func TestResolve_AsksForNoCredentialsKeyWithoutADatabase(t *testing.T) {
	t.Parallel()
	got := mustResolve(t, map[string]any{}, noEnv).Config

	if got.Credentials.Key != "" {
		t.Errorf("key = %q, want none", got.Credentials.Key.Expose())
	}
}

func TestResolve_RejectsACredentialsKeyThatIsNotThirtyTwoBytes(t *testing.T) {
	t.Parallel()
	doc := withDatabase(map[string]any{"key": testKey[:32], "key_id": "abcd1234"})

	_, err := config.Resolve(doc, noEnv)

	wantProblemAt(t, err, "credentials.key")
}

func TestResolve_RejectsACredentialsKeyThatIsNotHexadecimal(t *testing.T) {
	t.Parallel()
	doc := withDatabase(map[string]any{"key": strings.Repeat("z", 64), "key_id": "abcd1234"})

	_, err := config.Resolve(doc, noEnv)

	wantProblemAt(t, err, "credentials.key")
}

// The identifier says which key a stored credential was made with. A key
// without one leaves a deployment given the wrong key looking exactly like a
// deployment whose credentials were all revoked.
func TestResolve_RequiresAnIdentifierAlongsideTheKey(t *testing.T) {
	t.Parallel()
	doc := withDatabase(map[string]any{"key": testKey})

	_, err := config.Resolve(doc, noEnv)

	wantProblemAt(t, err, "credentials.key_id")
}

// A key that is not asked for is still checked when it is given. A deployment
// that starts without a database, and is later given one, would otherwise
// fail on a key that was accepted the day before.
func TestResolve_ChecksAKeyItDidNotAskForAlike(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		credentials map[string]any
		at          string
	}{
		{"short", map[string]any{"key": testKey[:32], "key_id": "abcd1234"}, "credentials.key"},
		{"without an identifier", map[string]any{"key": testKey}, "credentials.key_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Resolve(map[string]any{"credentials": tc.credentials}, noEnv)

			wantProblemAt(t, err, tc.at)
		})
	}
}

func TestResolve_KeepsTheCredentialsKeyFromTheEnvironment(t *testing.T) {
	t.Parallel()
	doc := withDatabase(map[string]any{"key": "${SUCO_CREDENTIALS_KEY}", "key_id": "abcd1234"})

	got := mustResolve(t, doc, envOf(map[string]string{"SUCO_CREDENTIALS_KEY": testKey})).Config

	if got.Credentials.Key != testKey {
		t.Errorf("key = %q, want it read from the environment", got.Credentials.Key.Expose())
	}
	if got.Credentials.KeyID != "abcd1234" {
		t.Errorf("key_id = %q, want abcd1234", got.Credentials.KeyID)
	}
}

// The same rule the database URL is held to: a reference that did not resolve
// leaves the key empty, and saying it is also required points at the empty
// string rather than at the variable.
func TestResolve_ReportsTheCauseWhenTheCredentialsKeyDoesNotResolve(t *testing.T) {
	t.Parallel()
	doc := withDatabase(map[string]any{"key": "${SUCO_CREDENTIALS_KEY}", "key_id": "testkey"})

	_, err := config.Resolve(doc, envOf(map[string]string{"SUCO_DATABASE_URL": "postgres://x/y"}))

	got := problems(t, err)
	if len(got) != 1 {
		t.Fatalf("got %d problems, want 1: %v", len(got), got)
	}
	if !strings.Contains(got[0].Message, "SUCO_CREDENTIALS_KEY") {
		t.Errorf("message %q does not name the variable that failed to resolve", got[0].Message)
	}
}

func TestResolve_ReportsTheCauseWhenTheIdentifierDoesNotResolve(t *testing.T) {
	t.Parallel()
	doc := withDatabase(map[string]any{"key": testKey, "key_id": "${SUCO_KEY_ID}"})

	_, err := config.Resolve(doc, noEnv)

	got := problems(t, err)
	if len(got) != 1 {
		t.Fatalf("got %d problems, want 1: %v", len(got), got)
	}
}

// A database that is both managed and given a URL has not settled which it is,
// so it has not named one to need a key for.
func TestResolve_AsksForNoKeyWhileTheDatabaseContradictsItself(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"database": map[string]any{
		"managed": true,
		"url":     "postgres://x/y",
	}}

	_, err := config.Resolve(doc, noEnv)

	for _, p := range problems(t, err) {
		if p.Path == "credentials.key" {
			t.Errorf("asked for a key on top of a database that has not settled: %v", p)
		}
	}
}

// There is no wait after the deadline any more: a payment waits until its
// network has been read past the deadline, which no setting decides. A
// document written for the older rule names a key the code does not have, and
// is told so by name rather than read past it.
func TestResolve_RejectsTheWaitSettingThatNoLongerExists(t *testing.T) {
	t.Parallel()
	doc := map[string]any{"networks": map[string]any{
		"local": map[string]any{"kind": "simulated", "finality": map[string]any{"wait": "2h"}},
	}}

	_, err := config.Resolve(doc, noEnv)

	p := wantProblemAt(t, err, "networks.local.finality.wait")
	if !strings.Contains(p.Message, "unknown key") {
		t.Errorf("message %q does not say the key is unknown", p.Message)
	}
	if got := problems(t, err); len(got) != 1 {
		t.Errorf("got %d problems, want the key alone: %v", len(got), got)
	}
}

// A key at a setting that is not secret would be printed by a report in
// full. The one it is most likely to land in is key_id, next to where it
// belongs.
func TestResolve_RefusesAKeyShapedValueOutsideASecretSetting(t *testing.T) {
	t.Parallel()
	doc := withDatabase(map[string]any{"key": testKey, "key_id": testKey})

	_, err := config.Resolve(doc, noEnv)

	p := wantProblemAt(t, err, "credentials.key_id")
	if strings.Contains(p.Message, testKey[:16]) {
		t.Errorf("the refusal carries the key: %s", p.Message)
	}
	if !strings.Contains(p.Message, "key") {
		t.Errorf("the refusal does not say what shape it saw: %s", p.Message)
	}
}
