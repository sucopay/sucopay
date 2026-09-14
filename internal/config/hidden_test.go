package config_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

const theSecret = "postgres://suco:hunter2@db.internal/suco"

// Every way fmt renders a value, and a Hidden inside a struct and an error,
// which is how one reaches a log without anybody writing it on purpose.
func TestHidden_ShowsNothingToFmt(t *testing.T) {
	t.Parallel()
	h := config.Hidden(theSecret)
	type holder struct{ URL config.Hidden }
	for name, rendered := range map[string]string{
		"%v":            fmt.Sprintf("%v", h),
		"%s":            fmt.Sprintf("%s", h),
		"%q":            fmt.Sprintf("%q", h),
		"%#v":           fmt.Sprintf("%#v", h),
		"%d":            fmt.Sprintf("%d", h),
		"%x":            fmt.Sprintf("%x", h),
		"Sprint":        fmt.Sprint(h),
		"in a struct":   fmt.Sprintf("%+v", holder{URL: h}),
		"in a slice":    fmt.Sprintf("%v", []config.Hidden{h}),
		"in an error":   fmt.Errorf("opening %v", h).Error(),
		"wrapped error": fmt.Errorf("open: %w", errors.New(fmt.Sprint(h))).Error(),
	} {
		if strings.Contains(rendered, "hunter2") {
			t.Errorf("%s shows the secret: %s", name, rendered)
		}
		if !strings.Contains(rendered, "[redacted]") {
			t.Errorf("%s does not say it hid something: %s", name, rendered)
		}
	}
}

func TestHidden_ShowsNothingToSlog(t *testing.T) {
	t.Parallel()
	h := config.Hidden(theSecret)
	for name, handler := range map[string]func(*bytes.Buffer) slog.Handler{
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		var out bytes.Buffer
		slog.New(handler(&out)).Info("opening", "url", h, "urls", []config.Hidden{h})
		if strings.Contains(out.String(), "hunter2") {
			t.Errorf("the %s handler shows the secret: %s", name, out.String())
		}
	}
}

// encoding/json is what a handler that dumps a Config would use, and it
// marshals a string type as its text unless told otherwise.
func TestHidden_ShowsNothingToJSON(t *testing.T) {
	t.Parallel()
	cfg := config.Config{
		Database:    config.Database{URL: theSecret},
		Credentials: config.Credentials{Key: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		Networks: map[string]config.Network{
			"polygon": {RPC: config.Endpoints{Own: "https://a:b@own.internal", Others: []config.Hidden{"https://c:d@other.internal"}}},
		},
	}

	out, err := json.Marshal(cfg)

	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hunter2", "0123456789abcdef", "a:b@", "c:d@"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Errorf("json shows %q: %s", secret, out)
		}
	}
}

func TestHidden_ExposeIsTheText(t *testing.T) {
	t.Parallel()
	if got := config.Hidden(theSecret).Expose(); got != theSecret {
		t.Errorf("Expose = %q, want the text as it was", got)
	}
}

// exposers are the files that may call Expose: where a credential is used,
// which is opening a database, reading a key, and reaching a chain, and the
// validation in this package. Any other call is one to look at.
var exposers = []string{
	"cmd/suco/chains.go",
	"cmd/suco/credential.go",
	"cmd/suco/doctor.go",
	"cmd/suco/serve.go",
	"internal/config/resolve.go",
}

func TestExpose_IsCalledOnlyWhereACredentialIsUsed(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Expose" {
				seen[rel] = true
				if !slices.Contains(exposers, rel) {
					t.Errorf("%s calls Expose, and is not on the list in this test", rel)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range exposers {
		if !seen[name] {
			t.Errorf("%s calls Expose nowhere; the list in this test is stale", name)
		}
	}
}
