package config

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/problem"
)

const (
	// DefaultPort is the port suco Pay listens on when the document sets none.
	DefaultPort = 7826
	// DefaultHost is the interface suco Pay binds when the document sets none.
	// Reaching an instance from another machine is a deployment decision, so it
	// is made in the document rather than assumed.
	DefaultHost = "127.0.0.1"
	// DefaultNetworkKind is the kind of a network whose document names none.
	DefaultNetworkKind = "evm"
	// DefaultPoll is how often a network is asked for new blocks when the
	// document sets no poll. Not shorter: a round is several calls, and what
	// a deployment takes from an endpoint it does not run is somebody else's
	// to give. A deployment reading a node of its own sets it down.
	DefaultPoll = 12 * time.Second
	// DefaultWidth is the widest span, in blocks, one request for logs is
	// given when the document sets no width. Providers cap the span, each at
	// its own value: between 50 and 10000 blocks on the public endpoints
	// measured.
	DefaultWidth = 1000
	// MinWidth is the narrowest span a document may set, a fifth of the
	// smallest cap measured.
	MinWidth = 10
	// MaxWidth is the widest span a document may set, the largest cap
	// measured.
	MaxWidth = 10000
	// DefaultFinalityRecheck is how long the worker waits between rounds when
	// the document sets none. Longer than the poll, because a round asks two
	// questions of every transfer it is deciding about, and a block does not
	// become final any sooner for being asked about more often.
	DefaultFinalityRecheck = time.Minute
	// DefaultFinalityMisses is how many rounds in a row have to find nothing
	// before a recorded transfer is given up on, when the document sets none.
	// Ten rounds of the default recheck is ten minutes of a transfer being
	// nowhere, which no endpoint that is merely behind stays for.
	DefaultFinalityMisses = 10
	// MinFinalityMisses is the fewest a document may set. One would give up on
	// a transfer the first time an endpoint did not have it, and an endpoint
	// can be reading a state it has not finished replacing.
	MinFinalityMisses = 2
)

// Origin says where a resolved value came from.
type Origin int

const (
	// FromDefault means the document did not set the key.
	FromDefault Origin = iota
	// FromFile means the document held the value.
	FromFile
	// FromEnv means the document referenced an environment variable.
	FromEnv
)

// String names the origin.
func (o Origin) String() string {
	switch o {
	case FromDefault:
		return "default"
	case FromFile:
		return "file"
	case FromEnv:
		return "env"
	}
	return fmt.Sprintf("Origin(%d)", int(o))
}

// Source records where one resolved value came from. Its zero value reads as a
// default with no variable behind it, which is also what a map returns for a
// path nobody recorded: use [Resolved.SourceOf] to tell the two apart.
type Source struct {
	// Origin says whether the value came from the document, an environment
	// variable the document named, or a built-in default.
	Origin Origin
	// Var is the environment variable that supplied the value. It is empty
	// unless Origin is FromEnv.
	Var string
}

// String names where a value came from, for a report an operator reads. An
// environment origin carries the variable with it, because that is the part
// telling an operator where to go and change the value.
//
// Var needs no quoting here. [Resolve] accepts a reference only to a name of
// the shape an environment variable has, so nothing a document writes reaches
// a report through this.
func (s Source) String() string {
	if s.Origin == FromEnv {
		return "${" + s.Var + "}"
	}
	return s.Origin.String()
}

// Log is how an instance writes what it is doing. Level is the least severe
// it will report; Format is "json" for a collector to read or "text" for a
// person.
//
// This is a setting rather than an environment variable, which is where most
// of Go's logging advice puts it: here the environment supplies secrets and
// the document declares settings, and one mechanism is enough.
type Log struct {
	Level  string
	Format string
}

var (
	// LogLevels are the levels a document may ask for.
	LogLevels = []string{"debug", "info", "warn", "error"}
	// LogFormats are the formats a document may ask for.
	LogFormats = []string{"json", "text"}
)

const (
	// DefaultLogLevel is what an instance reports when the document says
	// nothing.
	DefaultLogLevel = "info"
	// DefaultLogFormat is text, because the first thing anyone does is run
	// this in a terminal. A deployment writes json into its document.
	DefaultLogFormat = "text"
)

// Listen is where the instance serves. Host is the interface to bind; BaseURL
// is how others reach the instance, which differs from Host behind a proxy or
// inside a container.
type Listen struct {
	Host    string
	Port    int
	BaseURL string
}

// Hidden is a setting whose value nothing shows: a key, or a URL that may
// carry a password. fmt, slog and encoding/json are each given [redacted]
// instead, so that a Hidden inside an error, a log line or a marshalled
// Config carries none of it. [Hidden.Expose] is the text, for the place that
// uses it.
//
// The type is what hides the value; [Secret] is what a report asks about a
// path. Both, because a Config assembled by hand can put a secret at a path
// Secret does not match, and a value at a secret path can be read out of the
// document by something that never asks.
type Hidden string

// redacted is what a Hidden turns into on the way to a log, a terminal, or an
// error.
const redacted = "[redacted]"

// Expose is the text, for the place that opens a connection with it. Nothing
// else calls it: a Hidden is handed on as it is, and whatever it reaches
// prints [redacted]. Go cannot stop string(h), so that is looked for by hand.
func (h Hidden) Expose() string { return string(h) }

// Format is what fmt does with a Hidden, whatever the verb: it writes
// [redacted] and none of the text. String would leave %#v and %d, which
// print a string type as Go syntax and as a complaint with the value inside.
func (h Hidden) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, redacted) //nolint:errcheck // fmt's own buffer, and nothing to report it to
}

// LogValue is what slog does with a Hidden: [redacted]. slog's JSON handler
// does not go through fmt; it marshals, and a string type marshals as its
// text.
func (h Hidden) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON is what encoding/json does with a Hidden: the text [redacted],
// as a string.
func (h Hidden) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText is what every other encoder does with a Hidden.
func (h Hidden) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Database says where payment state is kept. Managed and URL are mutually
// exclusive: a managed database is one suco Pay runs itself.
type Database struct {
	Managed bool
	URL     Hidden
}

// Credentials is what an instance needs to read a stored credential.
type Credentials struct {
	// Key is what the stored form of a credential is hashed under. Empty
	// unless a document sets one, and required once database.url is set,
	// because that is when a credential can be stored and read back.
	Key Hidden
	// KeyID says which key a stored credential was made with. Not a secret,
	// and derived from nothing: a value taken from the key would let whoever
	// read the table test a guess at the key without a credential in hand.
	KeyID string
}

// Endpoints are where a network is reached. Own is a node the operator runs
// themselves, and Others are third parties.
//
// They are held apart because how far an answer can be believed depends on who
// runs the endpoint. Nothing acts on that difference yet: a round reads one
// endpoint, and comparing what several of them say belongs to deciding that a
// payment is final, which this build does not do.
//
// Every value here may carry a credential, and nothing that reads one writes
// it anywhere.
type Endpoints struct {
	Own    Hidden
	Others []Hidden
}

// Network is one chain the instance can observe. Kind names how to reach it:
// "evm", a chain spoken to over JSON-RPC at the endpoints in RPC, whose
// eth_chainId has to be ChainID; or "simulated", a chain that runs inside the
// server and has neither. Poll is how often the chain is asked for new blocks
// and Width the widest span, in blocks, one request for logs is given.
type Network struct {
	Kind     string
	ChainID  uint64
	RPC      Endpoints
	Poll     time.Duration
	Width    int
	Finality Finality
}

// Finality is how a network's payments are settled and given up on: how long
// the worker waits between rounds, and how many rounds in a row have to find
// nothing before a recorded transfer is treated as gone. How long a payment
// waits after its deadline is not a setting: it waits until the network has
// been read past the deadline.
type Finality struct {
	Recheck time.Duration
	Misses  int
}

// Assets are the tokens the instance accepts payment in, keyed by the name a
// document gives each. The name is a label for requests and the CLI to say;
// the token itself is its network and reference. A document that gives two
// names to one token is refused: a transfer seen on the chain is in the
// token, and would have no one name to be recorded under.
type Assets map[string]payment.Asset

// Asset returns the token a name stands for, and whether there is one.
func (a Assets) Asset(name string) (payment.Asset, bool) {
	asset, ok := a[name]
	return asset, ok
}

// Config is the boot-time configuration of one instance. [Resolve] validates a
// document before it returns one; a Config assembled any other way has not been
// checked.
type Config struct {
	Listen      Listen
	Log         Log
	Database    Database
	Credentials Credentials
	Networks    map[string]Network
	Assets      Assets
}

// Resolved is a Config together with where each of its values came from.
type Resolved struct {
	Config Config
	// Sources is keyed by dotted path, such as "listen.port". Read it through
	// [Resolved.SourceOf] rather than directly: a path nobody recorded gives
	// back the same zero Source a defaulted one does.
	Sources map[string]Source
}

// SourceOf returns where the value at a dotted path came from, and whether the
// path is one [Resolve] read at all. A misspelt path is a mistake in the
// caller rather than a setting that took its default, and the two are
// otherwise the same answer.
func (r Resolved) SourceOf(path string) (Source, bool) {
	source, ok := r.Sources[path]
	return source, ok
}

// Problem is one thing wrong with a configuration document. Path is the dotted
// path of the offending key, and is empty when the problem is not about one
// key.
type Problem struct {
	Path    string
	Message string
}

// String returns the problem with its path, or the message alone when the
// problem is not about one key.
//
// The path is quoted. A key comes from the document, so an unquoted one could
// carry a newline and forge a line of its own in a report, or an escape
// sequence a terminal would act on. [invisible.Quote] holds that list.
func (p Problem) String() string {
	return problem.Line(p.Path, p.Message)
}

// Problems is every problem found in one document. [Resolve] reports all of
// them together so that a misconfigured instance does not have to be started
// once per mistake.
type Problems []Problem

// Error lists every problem, one per line.
func (ps Problems) Error() string {
	lines := make([]string, 0, len(ps))
	for _, p := range ps {
		lines = append(lines, p.String())
	}
	return problem.List("configuration", lines)
}

// secretPaths are the dotted paths whose values never appear in a report. A
// path ending in * matches any single segment in that position.
var secretPaths = []string{
	"credentials.key",
	"database.url",
	"networks.*.rpc.own",
	"networks.*.rpc.others",
}

// Secret reports whether the value at a dotted path is a secret. Secrecy
// belongs to the key, not to how the value was supplied: a port stays
// printable when an environment variable supplies it.
func Secret(path string) bool {
	for _, pattern := range secretPaths {
		if matchPath(pattern, path) {
			return true
		}
	}
	return false
}

func matchPath(pattern, path string) bool {
	p, q := strings.Split(pattern, "."), strings.Split(path, ".")
	if len(p) != len(q) {
		return false
	}
	for i := range p {
		if p[i] != "*" && p[i] != withoutIndex(q[i]) {
			return false
		}
	}
	return true
}

// withoutIndex is a path segment with any place in a list taken off, so that
// others[0] reads as others. What is secret about a list is secret about each
// of its elements. It reads a segment the way walking a document does, so that
// a segment one of them takes apart is not one the other leaves whole.
func withoutIndex(segment string) string {
	name, _, _ := splitIndex(segment)
	return name
}
