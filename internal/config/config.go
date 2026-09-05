package config

import (
	"fmt"
	"strings"

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

// Database says where payment state is kept. Managed and URL are mutually
// exclusive: a managed database is one suco Pay runs itself.
type Database struct {
	Managed bool
	URL     string
}

// Credentials is what an instance needs to read a stored credential.
type Credentials struct {
	// Key is what the stored form of a credential is hashed under. Empty
	// unless a document sets one, and required once database.url is set,
	// because that is when a credential can be stored and read back.
	Key string
	// KeyID says which key a stored credential was made with. Not a secret,
	// and derived from nothing: a value taken from the key would let whoever
	// read the table test a guess at the key without a credential in hand.
	KeyID string
}

// Network is one chain the instance can observe. Kind names how to reach it;
// only "simulated", a chain that runs inside the server, is accepted so far.
type Network struct {
	Kind string
	RPC  string
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
	"networks.*.rpc",
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
		if p[i] != "*" && p[i] != q[i] {
			return false
		}
	}
	return true
}
