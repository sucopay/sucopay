package postgres

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A host can accept a connection and then say nothing, which would leave an
// operator watching a process that never reports why it has not started.
const (
	connectTimeout = 10 * time.Second
	queryTimeout   = 10 * time.Second
)

// Pool is a set of connections to one PostgreSQL.
type Pool struct {
	pool *pgxpool.Pool
}

// Open connects to the database the URL names and confirms the connection
// before returning. A pool hands out its first connection lazily, so an
// unreachable database would otherwise surface at whichever request happened
// to run first rather than at startup.
//
// The URL is a secret and no error here carries it.
func Open(ctx context.Context, url string) (*Pool, error) {
	if url == "" {
		// An empty string means "use the defaults" to the driver, which
		// reaches a local socket as whatever account runs the process. A
		// configuration naming no database would quietly connect to one.
		return nil, errors.New("database: no url")
	}
	cfg, err := configure(url)
	if err != nil {
		return nil, err
	}

	// The pool keeps this context for as long as it lives, opening on its own
	// goroutine whatever idle connections the URL asks for. A deadline
	// belonging to Open would cancel that work the moment Open returned.
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}

	ping, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := pool.Ping(ping); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: %w", cause(err))
	}
	return &Pool{pool: pool}, nil
}

// configure reads a URL into a pool configuration and refuses the ones this
// process will not connect over.
func configure(url string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database: the url could not be read: %w", cause(err))
	}
	if err := requireVerifiedTLS(cfg); err != nil {
		return nil, err
	}
	// Without this the constant governs only the connection Ping asks for, and
	// every later one falls to the driver's own two minutes.
	cfg.ConnConfig.ConnectTimeout = connectTimeout
	// Every transaction this process opens reads what the transaction beside
	// it has just committed, and several of them are only correct because of
	// it: a refund measures what a payment has left after locking the payment
	// row, and reads the refunds of whoever committed first. Under a level
	// that holds one snapshot for the whole transaction, that read is of the
	// state before the neighbour's write, and the check passes twice.
	//
	// Set here rather than on each transaction so that the next one written
	// cannot be the one that forgets. The driver hands an unrecognised query
	// parameter of the url to the server as a session setting, so a url
	// asking for another level would otherwise decide this; written after the
	// url is read, this is what the server is told.
	cfg.ConnConfig.RuntimeParams["default_transaction_isolation"] = "read committed"
	return cfg, nil
}

// cause returns what a parse failure was about, without the connection string
// the driver puts in front of it.
//
// The driver redacts a password written as userinfo and nothing else. One
// given as ?password= is repeated in full, and one holding a colon is cut at
// the colon and the first half repeated. The url is the setting this project
// declares secret, so none of that message is passed on.
func cause(err error) error {
	var parse *pgconn.ParseConfigError
	if errors.As(err, &parse) {
		if inner := errors.Unwrap(parse); inner != nil {
			return inner
		}
		return errors.New("the url is not a connection string")
	}
	// A failure to connect names the address, the user and the database it
	// tried, and not the password: the driver keeps that out and a test here
	// holds it to that. Those three stay.
	//
	// The rule that a secret setting never renders is about a report, where
	// the whole url would print and nothing needs it to. A failure nobody can
	// tell apart from any other failure is not a report at all: which host
	// refused, and which user it refused, is the whole of what an operator
	// reads.
	return err
}

// requireVerifiedTLS refuses a URL that would reach another machine over a
// connection nothing authenticated.
//
// The driver follows libpq: saying nothing about sslmode means "prefer", which
// offers TLS and then connects in the clear if the server declines, and even
// sslmode=require accepts any certificate presented to it. Whoever is on the
// wire can read and rewrite payment state either way. Only verify-ca and
// verify-full check who answered.
//
// A loopback address or a unix socket never reaches a wire, so a database on
// the same machine is left alone.
func requireVerifiedTLS(cfg *pgxpool.Config) error {
	type target struct {
		host string
		tls  *tls.Config
	}
	targets := []target{{cfg.ConnConfig.Host, cfg.ConnConfig.TLSConfig}}
	for _, f := range cfg.ConnConfig.Fallbacks {
		targets = append(targets, target{f.Host, f.TLSConfig})
	}

	for _, t := range targets {
		switch {
		case onThisMachine(t.host):
		case t.tls == nil:
			return errors.New("database: the url would connect in the clear to another machine. " +
				"Ask for sslmode=verify-full")
		case t.tls.InsecureSkipVerify && t.tls.VerifyPeerCertificate == nil:
			return errors.New("database: the url asks for TLS without checking who answered. " +
				"Ask for sslmode=verify-full, or verify-ca where the name cannot match")
		}
	}
	return nil
}

// onThisMachine reports whether a host is one nothing on a network can stand
// between. A name is not resolved here. localhost is the one every local
// install writes, and resolving names would make what a URL means depend on
// when it was read.
func onThisMachine(host string) bool {
	if strings.HasPrefix(host, "/") {
		return true
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Conns hands out the driver's pool, for the one package that runs queries
// against it. This type owns connecting, confirming and closing; it does not
// own what anybody asks the database, and a wrapper method per query would
// make this package a copy of every repository that exists.
func (p *Pool) Conns() *pgxpool.Pool { return p.pool }

// Ping reports whether the database still answers. [Open] asks once so that a
// start fails rather than the first request; this is for asking again while
// the instance runs.
func (p *Pool) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	if err := p.pool.Ping(ctx); err != nil {
		return fmt.Errorf("database: %w", cause(err))
	}
	return nil
}

// Close releases every connection.
func (p *Pool) Close() {
	p.pool.Close()
}

// ServerVersion reports the version of the server on the other end, for a
// report telling an operator which database an instance actually reached.
//
// The value is whatever the server sends. A caller putting it in front of a
// person is the one that has to bound and quote it.
func (p *Pool) ServerVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var version string
	if err := p.pool.QueryRow(ctx, "show server_version").Scan(&version); err != nil {
		return "", fmt.Errorf("database: %w", err)
	}
	return version, nil
}
