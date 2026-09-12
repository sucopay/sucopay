package config

import (
	"cmp"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/payment"
)

// Lookup reports the value of an environment variable and whether it was set.
// It has the signature of os.LookupEnv.
type Lookup func(name string) (string, bool)

// networkKinds are the kinds a network may declare, each with the settings
// that kind alone has. A setting listed under a kind is required by it and
// refused under a kind that does not list it. Poll and width, which every
// kind has, are not listed.
var networkKinds = map[string][]string{
	"evm":       {"chain_id", "rpc"},
	"simulated": nil,
}

// NetworkKinds are the kinds a network may declare, in name order.
func NetworkKinds() []string {
	kinds := make([]string, 0, len(networkKinds))
	for kind := range networkKinds {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	return kinds
}

// takes reports whether a network of the kind has the setting.
func takes(kind, key string) bool {
	return slices.Contains(networkKinds[kind], key)
}

// minPoll is the shortest poll a document may set. A shorter one is spent
// asking a provider about blocks that have not been produced.
const minPoll = time.Second

// minFinalityWait is the shortest wait a document may set. Below it, a payment
// paid a moment before its deadline is ended before the chain could have
// carried the transfer, let alone stopped replacing the block it is in.
const minFinalityWait = time.Minute

// minFinalityRecheck is the shortest a document may set between rounds, for
// the reason minPoll gives: a round is several calls to somebody else's
// endpoint.
const minFinalityRecheck = time.Second

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
	logLevel := r.text("log.level", DefaultLogLevel)
	logFormat := r.text("log.format", DefaultLogFormat)
	managed := r.boolean("database.managed", true)
	dbURL := r.text("database.url", "")
	credentialsKey := r.text("credentials.key", "")
	credentialsKeyID := r.text("credentials.key_id", "")
	networks := r.networks()
	assets := r.assets(networks)

	cfg := Config{
		Listen:      Listen{Host: host, Port: port, BaseURL: baseURL},
		Log:         Log{Level: logLevel, Format: logFormat},
		Database:    Database{Managed: managed, URL: dbURL},
		Credentials: Credentials{Key: credentialsKey, KeyID: credentialsKeyID},
		Networks:    networks,
		Assets:      assets,
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
	// Rendered first, then quoted. Quoting only the string case left a list or
	// a mapping to be expanded by %v, which prints the bytes of every string
	// inside it and puts a document's newlines and escape sequences into a
	// message that reaches a terminal.
	return invisible.Quote(fmt.Sprint(v))
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

// failedUnder reports whether anything at or below path was refused. What a
// setting is missing is worth saying only when nothing under it was wrong:
// a list whose one element was refused has no endpoint left, and saying so
// would bury the element that caused it.
func (r *reader) failedUnder(path string) bool {
	for _, p := range r.problems {
		if p.Path == path || strings.HasPrefix(p.Path, path+".") {
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

// duration reads a value written with its unit, such as 3s. A bare number is
// refused rather than given a unit: 3 could as well have been meant as
// milliseconds or minutes.
func (r *reader) duration(path string, def time.Duration) time.Duration {
	v, ok := r.raw(path)
	if !ok {
		return def
	}
	s, isString := v.(string)
	if !isString {
		r.fail(path, "want a duration such as 3s, got %s: %v", kindOf(v), shown(path, v))
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		r.fail(path, "want a duration such as 3s: %v%s", shown(path, v), suppliedBy(r.sources[path]))
		return def
	}
	return d
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

// section returns the names under a top-level mapping of them, sorted, with
// what being the noun a message calls one entry.
//
// A name becomes a segment of the dotted paths that carry provenance and mark
// secrets, so a dot inside one would split it in two and take networks.*.rpc
// out of the secret set. It is also what a report and a probe are keyed by, so
// one holding a character a reader cannot see is two names that render alike.
// Either is refused, and nothing under such a name is read: reporting each of
// its keys as unknown would bury the name that caused it.
func (r *reader) section(section, what string) []string {
	v, ok := walk(r.doc, []string{section})
	if !ok {
		return nil
	}
	entries, isMap := v.(map[string]any)
	if !isMap {
		r.fail(section, "want a mapping of names to %ss, got %s", what, kindOf(v))
		// Read, if not understood: calling it an unknown key as well would
		// send the reader after the name when the shape is the mistake.
		r.seen[section] = true
		return nil
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		switch {
		case strings.Contains(name, "."):
			r.fail(section, "%s name %q contains a dot", what, name)
		case strings.ContainsAny(name, "[]"):
			// A path segment ending in [0] names a place in a list, so a name
			// written that way would be read as one and every setting under it
			// would go missing.
			r.fail(section, "%s name %q contains a bracket", what, name)
		case invisible.Has(name):
			// A name is what a report, a probe and a list are keyed by, and
			// each of those is read by somebody. A reference and a symbol are
			// already held to this where an asset is described; the name it is
			// described under was the one place left where two that render
			// alike could be two.
			r.fail(section, "%s name %s holds a character that does not show up",
				what, invisible.Quote(name))
		default:
			r.seen[section+"."+name] = true
			names = append(names, name)
			continue
		}
		r.under(section, name)
	}
	slices.Sort(names)
	return names
}

// under marks every key beneath a name as read, for a name that was refused.
// Reporting each of them as unknown would bury the name that caused it.
func (r *reader) under(section, name string) {
	r.seenUnder(section + "." + name)
}

// seenUnder marks every key below path as read, for a setting whose own
// problem stands for all of them.
func (r *reader) seenUnder(path string) {
	for _, leaf := range leaves(r.doc) {
		if strings.HasPrefix(leaf, path+".") {
			r.seen[leaf] = true
		}
	}
}

func (r *reader) networks() map[string]Network {
	out := map[string]Network{}
	for _, name := range r.section("networks", "network") {
		path := "networks." + name
		kind := r.text(path+".kind", DefaultNetworkKind)
		out[name] = Network{
			Kind:    kind,
			ChainID: r.chainID(path + ".chain_id"),
			RPC:     r.endpoints(path+".rpc", takes(kind, "rpc")),
			Poll:    r.duration(path+".poll", DefaultPoll),
			Width:   r.integer(path+".width", DefaultWidth),
			Finality: Finality{
				Wait:    r.duration(path+".finality.wait", DefaultFinalityWait),
				Recheck: r.duration(path+".finality.recheck", DefaultFinalityRecheck),
				Misses:  r.integer(path+".finality.misses", DefaultFinalityMisses),
			},
		}
	}
	return out
}

// endpoints reads where a network is reached. Anything but a mapping under
// rpc is refused there rather than leaving what is under it unread: the
// earlier shape of this setting was a URL written straight under rpc, and a
// document still written that way is told what takes its place.
func (r *reader) endpoints(path string, wanted bool) Endpoints {
	// Read without [reader.raw], which would take a value here for a single
	// setting and put it in the message when it is not one. What sits here is
	// a mapping whose leaves are secret, and nothing about it belongs in a
	// problem but its shape.
	if v, ok := walk(r.doc, strings.Split(path, ".")); ok {
		r.seen[path] = true
		r.sources[path] = Source{Origin: FromFile}
		// A kind that reaches no chain is told that, rather than what shape
		// the setting it does not take should have been written in.
		if _, isMapping := v.(map[string]any); wanted && !isMapping {
			r.fail(path, "want own, others, or both, not %s", kindOf(v))
			return Endpoints{}
		}
	}
	if !wanted {
		// What the kind does not take, it does not take the parts of. The one
		// problem says so, and reporting each key under it as unknown would
		// bury it.
		r.seenUnder(path)
		return Endpoints{}
	}
	return Endpoints{
		Own:    r.text(path+".own", ""),
		Others: r.texts(path + ".others"),
	}
}

// texts reads a list of values. Each element is read the way a single value
// is, so that an environment reference stands in for one and a secret list
// keeps its elements secret. A failure names the element by its place, so that
// a document with four endpoints says which one to fix. An element already
// refused is left out rather than carried on as an empty string, which would
// be refused a second time for its shape.
//
// The message holds no part of the value: a list of endpoints is a list of
// secrets.
func (r *reader) texts(path string) []string {
	v, ok := r.raw(path)
	if !ok {
		return nil
	}
	list, isList := v.([]any)
	if !isList {
		r.fail(path, "want a list, got %s", kindOf(v))
		return nil
	}
	out := make([]string, 0, len(list))
	for i := range list {
		at := fmt.Sprintf("%s[%d]", path, i)
		if s := r.text(at, ""); !r.failed(at) {
			out = append(out, s)
		}
	}
	return out
}

// chainID reads a chain id, which is 1 or more. Zero stands for none, and
// whether none is allowed is the kind's to say.
func (r *reader) chainID(path string) uint64 {
	n := r.integer(path, 0)
	if n > 0 {
		return uint64(n)
	}
	if r.sources[path].Origin != FromDefault && !r.failed(path) {
		r.fail(path, "want 1 or more: %d", n)
	}
	return 0
}

func (r *reader) assets(networks map[string]Network) Assets {
	out := Assets{}
	for _, name := range r.section("assets", "asset") {
		if asset, ok := r.asset("assets."+name, networks); ok {
			out[name] = asset
		}
	}
	return out
}

// asset reads one entry and returns the token it describes once payment
// accepts it, with every problem found on the way reported. The checks sit
// here rather than in validate because a payment.Asset exists only once
// payment.NewAsset has accepted it, so there is nothing to validate
// afterwards.
//
// A token belongs on a network the document declares: the section is a list
// of what the server observes, and an asset on a network nobody declared
// would be accepted in a request and never seen paid.
func (r *reader) asset(path string, networks map[string]Network) (payment.Asset, bool) {
	network := r.text(path+".network", "")
	reference := r.text(path+".reference", "")
	symbol := r.text(path+".symbol", "")
	decimals := r.integer(path+".decimals", 0)

	if p := path + ".network"; !r.failed(p) {
		if network == "" {
			r.fail(p, "required")
		} else if _, known := networks[network]; !known {
			r.fail(p, "no network named %v", shown(p, network))
		}
	}
	if p := path + ".reference"; !r.failed(p) && reference == "" {
		r.fail(p, "required")
	}
	if p := path + ".symbol"; !r.failed(p) && symbol == "" {
		r.fail(p, "required")
	}
	decimalsPath := path + ".decimals"
	size, fits := toByte(decimals)
	if !r.failed(decimalsPath) {
		switch {
		case r.sources[decimalsPath].Origin == FromDefault:
			r.fail(decimalsPath, "required")
		case !fits:
			r.fail(decimalsPath, "outside 0-%d: %d", math.MaxUint8, decimals)
		}
	}
	// NewAsset refuses an empty network or reference too, and would say so
	// again. It is still asked about the rest when only the symbol or the
	// network's existence is wrong, so that a report holds every problem.
	if network == "" || reference == "" || !fits || r.failed(decimalsPath) {
		return payment.Asset{}, false
	}
	asset, err := payment.NewAsset(payment.Network(network), reference, symbol, size)
	switch {
	case errors.Is(err, payment.ErrTooManyDecimals):
		r.fail(decimalsPath, "%v", err)
	case err != nil:
		r.fail(path, "%v", err)
	}
	return asset, err == nil
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
		r.validateCredentials(cfg.Database, cfg.Credentials)
	}
	r.validateLog(cfg.Log)
	for name, n := range cfg.Networks {
		r.validateNetwork("networks."+name, n)
	}
	r.validateAssets(cfg.Assets)
}

// validateNetwork holds a network's settings to its kind. An unknown kind is
// said on its own: which settings such a network has is unknown too.
func (r *reader) validateNetwork(path string, n Network) {
	if !r.failed(path+".poll") && n.Poll < minPoll {
		r.fail(path+".poll", "below %s: %s", minPoll, n.Poll)
	}
	if !r.failed(path+".width") && (n.Width < MinWidth || n.Width > MaxWidth) {
		r.fail(path+".width", "outside %d-%d: %d", MinWidth, MaxWidth, n.Width)
	}
	if !r.failed(path+".finality.wait") && n.Finality.Wait < minFinalityWait {
		r.fail(path+".finality.wait", "below %s: %s", minFinalityWait, n.Finality.Wait)
	}
	if !r.failed(path+".finality.recheck") && n.Finality.Recheck < minFinalityRecheck {
		r.fail(path+".finality.recheck", "below %s: %s", minFinalityRecheck, n.Finality.Recheck)
	}
	if !r.failed(path+".finality.misses") && n.Finality.Misses < MinFinalityMisses {
		r.fail(path+".finality.misses", "below %d: %d", MinFinalityMisses, n.Finality.Misses)
	}
	if r.failed(path + ".kind") {
		return
	}
	if _, known := networkKinds[n.Kind]; !known {
		r.fail(path+".kind", "unknown kind %v, want one of %s",
			shown(path+".kind", n.Kind), strings.Join(NetworkKinds(), ", "))
		return
	}
	for _, key := range []string{"chain_id", "rpc"} {
		p := path + "." + key
		if r.failed(p) {
			continue
		}
		set := r.sources[p].Origin != FromDefault
		switch {
		case !takes(n.Kind, key) && set:
			r.fail(p, "not a setting when kind is %s", n.Kind)
		// A key this kind does not take, and does not have. Without this the
		// next case would call it required.
		case !takes(n.Kind, key):
		case key == "rpc":
			// Which endpoints reach a chain, and whether any do, are both
			// this one's to say.
			r.validateEndpoints(p, n.RPC)
		case !set:
			r.fail(p, "required when kind is %s", n.Kind)
		}
	}
}

// validateEndpoints holds every endpoint a network names to its shape, and
// refuses a network that names none. A chain nothing can be asked over is one
// the instance cannot read, and an operator finds that out here rather than
// from a network that never answers.
func (r *reader) validateEndpoints(path string, e Endpoints) {
	own := r.sources[path+".own"].Origin != FromDefault
	if own {
		r.validateRPC(path+".own", e.Own)
	}
	for i, endpoint := range e.Others {
		r.validateRPC(fmt.Sprintf("%s.others[%d]", path, i), endpoint)
	}
	if !own && len(e.Others) == 0 && !r.failedUnder(path) {
		r.fail(path, "give own, others, or both")
	}
}

// validateRPC holds an endpoint to https, or to http on this machine: a key on
// the URL travels in the clear over http. The host is taken as written, the
// way the database URL's is, so "localhost" and a loopback address pass and a
// name that resolves to one does not. The endpoint is a secret, so no message
// carries it.
func (r *reader) validateRPC(path, raw string) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		r.fail(path, "not a URL")
	case u.Scheme == "https", u.Scheme == "http" && onThisMachine(u.Hostname()):
		if !hasHost(u) {
			r.fail(path, "has no host")
		}
	default:
		r.fail(path, "want https, or http to localhost or a loopback address")
	}
}

// onThisMachine is the check postgres makes on the database URL's host,
// repeated rather than imported: this package reaches nothing outside itself.
func onThisMachine(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// hasHost reports whether a URL names a machine. Hostname rather than Host:
// https://:8545/ has a Host of ":8545" and names none.
func hasHost(u *url.URL) bool {
	return u.Hostname() != ""
}

// validateAssets refuses two names for one token. Which of the two is the
// mistake is the document's to say, so both are named, in the document's
// sorted order so that a report reads the same each time.
func (r *reader) validateAssets(assets Assets) {
	names := make([]string, 0, len(assets))
	for name := range assets {
		names = append(names, name)
	}
	slices.Sort(names)
	for i, name := range names {
		for _, earlier := range names[:i] {
			if assets[earlier].Same(assets[name]) {
				r.fail("assets", "%q and %q name the same asset", earlier, name)
			}
		}
	}
}

// reportUnknownKeys names every leaf in the document that nothing read. A
// misspelt key would otherwise leave its default in place and say nothing,
// which is the mistake a configuration document invites most.
func (r *reader) reportUnknownKeys() {
	for _, path := range leaves(r.doc) {
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
func leaves(doc map[string]any) []string {
	return leavesTo(doc, "", maxDepth)
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
	case !hasHost(u):
		r.fail(path, "has no host: %v", shown(path, raw))
	}
}

func (r *reader) validateLog(l Log) {
	if !r.failed("log.level") && !slices.Contains(LogLevels, l.Level) {
		r.fail("log.level", "want one of %s, got %v",
			strings.Join(LogLevels, ", "), invisible.Quote(l.Level))
	}
	if !r.failed("log.format") && !slices.Contains(LogFormats, l.Format) {
		r.fail("log.format", "want one of %s, got %v",
			strings.Join(LogFormats, ", "), invisible.Quote(l.Format))
	}
}

// keyBytes is how much material a credential's stored form is hashed under.
// The setting carries it as hexadecimal, an environment variable being text.
const keyBytes = 32

// validateCredentials refuses a missing key and one an instance could not
// hash with, and refuses a key carrying no identifier.
//
// Required once a database is named, which is when a credential can be stored
// and read back. An instance answering only /healthz holds none, and asking it
// for a secret first would put one in front of finding out whether the thing
// runs at all.
//
// Named against database.url because that is what serve opens on. A database
// reached some other way would have to be named here too, or an instance would
// read credentials with no key checked.
func (r *reader) validateCredentials(db Database, c Credentials) {
	// Length is checked after decoding. Thirty-two characters of hexadecimal
	// are sixteen bytes, and a check on the text would take them for enough.
	if !r.failed("credentials.key") {
		switch raw, err := hex.DecodeString(c.Key); {
		case c.Key == "":
			// Not required until a database is named. A section contradicting
			// itself has not named one, so [reader.failed] is asked about the
			// whole of database as well as about its parts.
			if db.URL != "" && !r.failed("database") {
				r.fail("credentials.key", "required when a database is configured")
			}
		case err != nil:
			r.fail("credentials.key", "not hexadecimal")
		case len(raw) != keyBytes:
			r.fail("credentials.key", "want %d bytes of hexadecimal", keyBytes)
		}
	}
	if c.Key != "" && c.KeyID == "" && !r.failed("credentials.key_id") {
		r.fail("credentials.key_id", "required alongside credentials.key")
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
		name, place, indexed := splitIndex(segment)
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[name]
		if !ok {
			return nil, false
		}
		if !indexed {
			continue
		}
		list, isList := cur.([]any)
		if !isList || place >= len(list) {
			return nil, false
		}
		cur = list[place]
	}
	return cur, true
}

// splitIndex takes a path segment apart into the key it names and, for a
// segment such as others[0], the place in that key's list.
func splitIndex(segment string) (name string, place int, indexed bool) {
	open := strings.IndexByte(segment, '[')
	if open < 0 || !strings.HasSuffix(segment, "]") {
		return segment, 0, false
	}
	i, err := strconv.Atoi(segment[open+1 : len(segment)-1])
	if err != nil || i < 0 {
		return segment, 0, false
	}
	return segment[:open], i, true
}

var (
	errNotWhole  = errors.New("want a whole number")
	errNotNumber = errors.New("want a number")
)

// toByte returns v as the byte an asset counts decimals in, and false when
// it is outside one. Converting without looking would let 256 through as 0.
func toByte(v int) (uint8, bool) {
	if v < 0 || v > math.MaxUint8 {
		return 0, false
	}
	return uint8(v), true
}

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
