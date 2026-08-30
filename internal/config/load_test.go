package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

func writeDocument(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "suco.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDecode_AcceptsAnEmptyDocument(t *testing.T) {
	doc, err := config.Decode(nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(doc) != 0 {
		t.Errorf("doc = %v, want empty", doc)
	}
}

func TestDecode_ReportsWhereTheSyntaxErrorIs(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		line int
	}{
		{"a key indented under a value", "listen:\n  port: 7826\n   base_url: x\n", 2},
		{"a sequence that never ends", "a: 1\nb: [\n", 2},
		{"a key defined twice", "listen:\n  port: 1\nlisten:\n  port: 2\n", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := config.Decode([]byte(c.doc))

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if got := errorLine(t, err); got != c.line {
				t.Errorf("error points at line %d, want %d:\n%v", got, c.line, err)
			}
		})
	}
}

// errorLine reads the [line:column] a decoding error opens with. The excerpt
// that follows carries line numbers of its own, so matching a bare digit
// anywhere in the message would pass without the position being right.
func errorLine(t *testing.T, err error) int {
	t.Helper()
	m := regexp.MustCompile(`\[(\d+):\d+\]`).FindStringSubmatch(err.Error())
	if m == nil {
		t.Fatalf("error carries no [line:column]:\n%v", err)
	}
	line, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		t.Fatal(convErr)
	}
	return line
}

func TestLoad_ReadsTheDocumentAndResolvesIt(t *testing.T) {
	path := writeDocument(t, "listen:\n  port: 9000\ndatabase:\n  managed: false\n  url: ${SUCO_DATABASE_URL}\n")
	env := envOf(map[string]string{"SUCO_DATABASE_URL": "postgres://localhost/suco"})

	got, err := config.Load(path, env)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if got.Config.Listen.Port != 9000 {
		t.Errorf("port = %d, want 9000", got.Config.Listen.Port)
	}
	if got.Sources["database.url"].Var != "SUCO_DATABASE_URL" {
		t.Errorf("database.url source = %+v, want the variable name", got.Sources["database.url"])
	}
}

func TestLoad_NamesTheDocumentWhenItIsMissing(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"), noEnv)

	if err == nil {
		t.Fatal("want an error, got none")
	}
	if !strings.Contains(err.Error(), "absent.yaml") {
		t.Errorf("error does not name the file:\n%v", err)
	}
}

func TestPath_PrefersTheEnvironmentVariable(t *testing.T) {
	cases := []struct {
		name string
		env  config.Lookup
		want string
	}{
		{"unset", noEnv, "suco.yaml"},
		{"empty", envOf(map[string]string{config.PathVar: ""}), "suco.yaml"},
		{"set", envOf(map[string]string{config.PathVar: "/etc/suco/suco.yaml"}), "/etc/suco/suco.yaml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := config.Path(c.env, "suco.yaml"); got != c.want {
				t.Errorf("path = %q, want %q", got, c.want)
			}
		})
	}
}
