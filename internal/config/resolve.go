package config

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Lookup reports the value of an environment variable and whether it was set.
// It has the signature of os.LookupEnv.
type Lookup func(name string) (string, bool)

// knownNetworkKinds are the kinds a network may declare.
//
// Not accepting an evm kind yet: no adapter observes one, so a document naming
// it would start a server that silently ignores the network.
var knownNetworkKinds = []string{"simulated"}

// Resolve builds a [Config] from a decoded configuration document and the
// environment. It reports every problem it finds as one [Problems] error.
//
// A string in the document is either a literal or a whole reference of the
// form ${NAME}. Resolve returns a problem for a reference whose variable is
// unset or empty, and for a string that mixes literal text with a reference.
func Resolve(doc map[string]any, env Lookup) (Resolved, error) {
	r := &reader{doc: doc, env: env, sources: map[string]Source{}}

	port := r.integer("listen.port", DefaultPort)
	baseURL := r.text("listen.base_url", fmt.Sprintf("http://localhost:%d", port))
	managed := r.boolean("database.managed", true)
	dbURL := r.text("database.url", "")
	networks := r.networks()

	cfg := Config{
		Listen:   Listen{Port: port, BaseURL: baseURL},
		Database: Database{Managed: managed, URL: dbURL},
		Networks: networks,
	}
	r.validate(cfg)

	if len(r.problems) > 0 {
		slices.SortStableFunc(r.problems, func(a, b Problem) int {
			return strings.Compare(a.Path, b.Path)
		})
		return Resolved{}, Problems(r.problems)
	}
	return Resolved{Config: cfg, Sources: r.sources}, nil
}

type reader struct {
	doc      map[string]any
	env      Lookup
	sources  map[string]Source
	problems []Problem
}

func (r *reader) fail(path, format string, args ...any) {
	r.problems = append(r.problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

// raw returns the value at a dotted path, after resolving a reference. The
// second result is false when the key is absent or the reference failed.
func (r *reader) raw(path string) (any, bool) {
	v, ok := walk(r.doc, strings.Split(path, "."))
	if !ok {
		r.sources[path] = Source{Origin: FromDefault}
		return nil, false
	}
	s, isString := v.(string)
	if !isString {
		r.sources[path] = Source{Origin: FromFile}
		return v, true
	}
	name, isRef, err := reference(s)
	switch {
	case err != nil:
		r.fail(path, "%s", err)
		return nil, false
	case !isRef:
		r.sources[path] = Source{Origin: FromFile}
		return s, true
	}
	value, set := r.env(name)
	if !set {
		r.fail(path, "%s is not set", name)
		return nil, false
	}
	if value == "" {
		r.fail(path, "%s is empty", name)
		return nil, false
	}
	r.sources[path] = Source{Origin: FromEnv, Var: name}
	return value, true
}

func (r *reader) text(path, def string) string {
	v, ok := r.raw(path)
	if !ok {
		return def
	}
	s, isString := v.(string)
	if !isString {
		r.fail(path, "want text, got %s", kindOf(v))
		return def
	}
	return s
}

func (r *reader) integer(path string, def int) int {
	v, ok := r.raw(path)
	if !ok {
		return def
	}
	n, err := toInt(v)
	if err != nil {
		r.fail(path, "%s%s", err, suppliedBy(r.sources[path]))
		return def
	}
	return n
}

func (r *reader) boolean(path string, def bool) bool {
	v, ok := r.raw(path)
	if !ok {
		return def
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		parsed, err := strconv.ParseBool(b)
		if err != nil {
			r.fail(path, "want true or false, got %q%s", b, suppliedBy(r.sources[path]))
			return def
		}
		return parsed
	}
	r.fail(path, "want true or false, got %s", kindOf(v))
	return def
}

func (r *reader) networks() map[string]Network {
	v, ok := walk(r.doc, []string{"networks"})
	if !ok {
		return map[string]Network{}
	}
	entries, isMap := v.(map[string]any)
	if !isMap {
		r.fail("networks", "want a mapping of names to networks, got %s", kindOf(v))
		return map[string]Network{}
	}
	out := make(map[string]Network, len(entries))
	for name := range entries {
		out[name] = Network{
			Kind: r.text("networks."+name+".kind", ""),
			RPC:  r.text("networks."+name+".rpc", ""),
		}
	}
	return out
}

func (r *reader) validate(cfg Config) {
	if cfg.Listen.Port < 1 || cfg.Listen.Port > 65535 {
		r.fail("listen.port", "outside 1-65535: %d", cfg.Listen.Port)
	}
	if u, err := url.Parse(cfg.Listen.BaseURL); err != nil {
		r.fail("listen.base_url", "not a URL: %q", cfg.Listen.BaseURL)
	} else if u.Scheme != "http" && u.Scheme != "https" {
		r.fail("listen.base_url", "needs an http or https scheme: %q", cfg.Listen.BaseURL)
	} else if u.Host == "" {
		r.fail("listen.base_url", "has no host: %q", cfg.Listen.BaseURL)
	}

	switch {
	case cfg.Database.Managed && cfg.Database.URL != "":
		r.fail("database", "managed and url are mutually exclusive")
	case !cfg.Database.Managed && cfg.Database.URL == "":
		r.fail("database.url", "required when database.managed is false")
	}

	for name, n := range cfg.Networks {
		if !slices.Contains(knownNetworkKinds, n.Kind) {
			r.fail("networks."+name+".kind",
				"unknown kind %q, want one of %s", n.Kind, strings.Join(knownNetworkKinds, ", "))
		}
	}
}

// reference reports the variable named by a whole ${NAME} string. A string
// that mixes literal text with a reference is an error rather than a partial
// expansion, so that every value has exactly one origin.
func reference(s string) (name string, isRef bool, err error) {
	i := strings.Index(s, "${")
	if i < 0 {
		return "", false, nil
	}
	if i != 0 || !strings.HasSuffix(s, "}") || strings.Count(s, "${") > 1 {
		return "", false, fmt.Errorf("a reference has to be the whole value: %q", s)
	}
	name = s[2 : len(s)-1]
	if name == "" {
		return "", false, fmt.Errorf("empty reference: %q", s)
	}
	return name, true, nil
}

func walk(doc map[string]any, path []string) (any, bool) {
	var cur any = doc
	for _, segment := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[segment]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case uint64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("want a whole number, got %v", n)
		}
		return int(n), nil
	case string:
		parsed, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("want a number, got %q", n)
		}
		return parsed, nil
	}
	return 0, fmt.Errorf("want a number, got %s", kindOf(v))
}

func suppliedBy(s Source) string {
	if s.Origin == FromEnv {
		return " (from " + s.Var + ")"
	}
	return ""
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case bool:
		return "true or false"
	case string:
		return "text"
	case map[string]any:
		return "a mapping"
	case []any:
		return "a list"
	}
	return fmt.Sprintf("%T", v)
}
