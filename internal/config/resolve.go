package config

import (
	"cmp"
	"errors"
	"fmt"
	"math"
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
	r := &reader{doc: doc, env: env, sources: map[string]Source{}, seen: map[string]bool{}}

	host := r.text("listen.host", DefaultHost)
	port := r.integer("listen.port", DefaultPort)
	baseURL := r.text("listen.base_url", defaultBaseURL(port))
	managed := r.boolean("database.managed", true)
	dbURL := r.text("database.url", "")
	networks := r.networks()

	cfg := Config{
		Listen:   Listen{Host: host, Port: port, BaseURL: baseURL},
		Database: Database{Managed: managed, URL: dbURL},
		Networks: networks,
	}
	r.validate(cfg)
	r.reportUnknownKeys()

	if len(r.problems) > 0 {
		slices.SortStableFunc(r.problems, func(a, b Problem) int {
			return cmp.Compare(a.Path, b.Path)
		})
		return Resolved{}, Problems(r.problems)
	}
	return Resolved{Config: cfg, Sources: r.sources}, nil
}

type reader struct {
	doc       map[string]any
	env       Lookup
	sources   map[string]Source
	seen      map[string]bool
	problems  []Problem
	truncated bool
}

func (r *reader) fail(path, format string, args ...any) {
	switch {
	case len(r.problems) < maxProblems:
		r.problems = append(r.problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
	case !r.truncated:
		r.truncated = true
		r.problems = append(r.problems, Problem{
			Message: fmt.Sprintf("more problems follow; only the first %d are listed", maxProblems),
		})
	}
}

// defaultBaseURL is how others reach an instance that says nothing about it.
func defaultBaseURL(port int) string {
	return fmt.Sprintf("http://localhost:%d", port)
}

// shown returns a value as it may appear in a problem. A problem reaches an
// operator's terminal and their logs, so a value at a secret path is replaced
// rather than quoted.
func shown(path string, v any) any {
	if Secret(path) {
		return "(secret)"
	}
	if text, isString := v.(string); isString {
		return Quote(text)
	}
	return v
}

// failed reports whether a path already has a problem. Validation skips such a
// path so that a report holds causes and not the consequences they leave: a
// reference that did not resolve leaves the key empty, and saying it is also
// required sends the reader after the wrong thing.
func (r *reader) failed(path string) bool {
	for _, p := range r.problems {
		if p.Path == path {
			return true
		}
	}
	return false
}

// raw returns the value at a dotted path, after resolving a reference. The
// second result is false when the key is absent or the reference failed.
func (r *reader) raw(path string) (any, bool) {
	r.seen[path] = true
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
		r.fail(path, "%s: %v", err, shown(path, s))
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
		r.fail(path, "want text, got %s: %v", kindOf(v), shown(path, v))
		return def
	}
	r.refuseCredentials(path, s)
	return s
}

// refuseCredentials keeps a username or password out of every setting except
// the ones declared secret. Secrecy is decided by the key, so a document is
// free to name any variable at any key, and a report prints in full whatever
// arrives at a key that is not secret. Refusing the value is what makes that
// rule safe: credentials reach the paths built to hold them or they do not
// start the instance.
//
// The message holds no part of the value.
func (r *reader) refuseCredentials(path, value string) {
	if Secret(path) {
		return
	}
	if u, err := url.Parse(value); err == nil && u.User != nil {
		r.fail(path, "carries a username or password, which only a secret setting may hold")
	}
}

func (r *reader) integer(path string, def int) int {
	v, ok := r.raw(path)
	if !ok {
		return def
	}
	n, err := toInt(v)
	if err != nil {
		r.fail(path, "%s: %v%s", err, shown(path, v), suppliedBy(r.sources[path]))
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
			r.fail(path, "want true or false: %v%s", shown(path, b), suppliedBy(r.sources[path]))
			return def
		}
		return parsed
	}
	r.fail(path, "want true or false, got %s: %v", kindOf(v), shown(path, v))
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
		// A name becomes a segment of the dotted paths that carry provenance
		// and mark secrets, so a dot inside one would split it in two and take
		// networks.*.rpc out of the secret set.
		if strings.Contains(name, ".") {
			r.fail("networks", "network name %q contains a dot", name)
			// Nothing under a rejected name is read, and reporting each of its
			// keys as unknown would bury the name that caused it.
			for _, path := range leaves(r.doc, "") {
				if strings.HasPrefix(path, "networks."+name+".") {
					r.seen[path] = true
				}
			}
			continue
		}
		r.seen["networks."+name] = true
		out[name] = Network{
			Kind: r.text("networks."+name+".kind", ""),
			RPC:  r.text("networks."+name+".rpc", ""),
		}
	}
	return out
}

func (r *reader) validate(cfg Config) {
	if !r.failed("listen.host") && cfg.Listen.Host == "" {
		r.fail("listen.host", "empty")
	}
	if !r.failed("listen.port") && (cfg.Listen.Port < 1 || cfg.Listen.Port > 65535) {
		r.fail("listen.port", "outside 1-65535: %d", cfg.Listen.Port)
	}
	if !r.failed("listen.base_url") {
		r.validateBaseURL(cfg.Listen.BaseURL)
	}
	if !r.failed("database.managed") && !r.failed("database.url") {
		r.validateDatabase(cfg.Database)
	}
	for name, n := range cfg.Networks {
		path := "networks." + name + ".kind"
		if !r.failed(path) && !slices.Contains(knownNetworkKinds, n.Kind) {
			r.fail(path, "unknown kind %v, want one of %s", shown(path, n.Kind), strings.Join(knownNetworkKinds, ", "))
		}
	}
}

// reportUnknownKeys names every leaf in the document that nothing read. A
// misspelt key would otherwise leave its default in place and say nothing,
// which is the mistake a configuration document invites most.
func (r *reader) reportUnknownKeys() {
	for _, path := range leaves(r.doc, "") {
		if !r.seen[path] {
			r.fail(path, "unknown key")
		}
	}
}

// leaves lists the dotted path of every value in doc that is not itself a
// mapping. An empty mapping counts as a leaf so that it is not silently
// accepted.
//
// A secret path is a leaf whatever it holds. Descending into one would put the
// keys a secret is written with into a report, which is the shape of the secret
// even when its values are hidden.
func leaves(doc map[string]any, prefix string) []string {
	return leavesTo(doc, prefix, maxDepth)
}

func leavesTo(doc map[string]any, prefix string, depth int) []string {
	var out []string
	for key, value := range doc {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		nested, isMapping := value.(map[string]any)
		if isMapping && len(nested) > 0 && !Secret(path) && depth > 0 {
			out = append(out, leavesTo(nested, path, depth-1)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

func (r *reader) validateBaseURL(raw string) {
	const path = "listen.base_url"
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		r.fail(path, "not a URL: %v", shown(path, raw))
	case u.Scheme != "http" && u.Scheme != "https":
		r.fail(path, "needs an http or https scheme: %v", shown(path, raw))
	case u.Host == "":
		r.fail(path, "has no host: %v", shown(path, raw))
	}
}

func (r *reader) validateDatabase(db Database) {
	switch {
	case db.Managed && db.URL != "":
		r.fail("database", "managed and url are mutually exclusive")
	case !db.Managed && db.URL == "":
		r.fail("database.url", "required when database.managed is false")
	}
}

// maxProblems bounds a report. A document can hold thousands of keys nothing
// reads, and a reader stops using a list long before it reaches the end of one
// that long.
const maxProblems = 50

// maxDepth bounds how far leaves descends. A document deep enough to exhaust
// the stack fits well inside [MaxDocumentBytes].
const maxDepth = 32

var (
	errPartialReference = errors.New("a reference has to be the whole value")
	errEmptyReference   = errors.New("a reference names no variable")
	errNotAVariableName = errors.New("a reference names something that is not a variable")
)

// isVariableName reports whether s has the shape an environment variable name
// has. A reference is held to it so that a document cannot reach a report
// through [Source.Var] or through the message naming an unset variable, both
// of which print the name as it was written.
func isVariableName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// reference reports the variable named by a whole ${NAME} string. A string
// that mixes literal text with a reference is an error, so that every value
// has exactly one origin.
//
// The errors carry no part of the string. The caller holds the path and is the
// only one that can decide whether the value may be shown.
func reference(s string) (name string, isRef bool, err error) {
	i := strings.Index(s, "${")
	if i < 0 {
		return "", false, nil
	}
	if i != 0 || !strings.HasSuffix(s, "}") || strings.Count(s, "${") > 1 {
		return "", false, errPartialReference
	}
	name = s[2 : len(s)-1]
	if name == "" {
		return "", false, errEmptyReference
	}
	if !isVariableName(name) {
		return "", false, errNotAVariableName
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

var (
	errNotWhole  = errors.New("want a whole number")
	errNotNumber = errors.New("want a number")
)

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case uint64:
		if n > math.MaxInt64 {
			return 0, errNotNumber
		}
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, errNotWhole
		}
		return int(n), nil
	case string:
		parsed, err := strconv.Atoi(n)
		if err != nil {
			return 0, errNotNumber
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
