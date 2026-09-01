package postgres

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The refusal is checked here rather than through Open because every URL that
// gets past it then tries to connect, and a host that is not on this machine
// has no fast answer to that.

func TestRequireVerifiedTLS_AcceptsOnlyTheModesThatCheckWhoAnswered(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		mode   string
		accept bool
	}{
		// Saying nothing means prefer, which offers TLS and connects in the
		// clear if the server declines.
		{"", false},
		{"?sslmode=disable", false},
		{"?sslmode=allow", false},
		{"?sslmode=prefer", false},
		// require encrypts and accepts any certificate presented to it.
		{"?sslmode=require", false},
		{"?sslmode=verify-ca", true},
		{"?sslmode=verify-full", true},
	} {
		t.Run(c.mode, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig("postgres://u:p@db.example.com:5432/x" + c.mode)
			if err != nil {
				t.Fatal(err)
			}

			err = requireVerifiedTLS(cfg)

			if c.accept && err != nil {
				t.Errorf("refused a verified connection: %v", err)
			}
			if !c.accept && err == nil {
				t.Error("accepted a connection nothing authenticated")
			}
			if err != nil && !strings.Contains(err.Error(), "sslmode") {
				t.Errorf("error does not say what the url has to ask for: %v", err)
			}
		})
	}
}

func TestRequireVerifiedTLS_LeavesADatabaseOnThisMachineAlone(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
		t.Run(host, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig("postgres://u:p@" + host + ":5432/x")
			if err != nil {
				t.Fatal(err)
			}

			if err := requireVerifiedTLS(cfg); err != nil {
				t.Errorf("refused a database on this machine: %v", err)
			}
		})
	}
}

func TestOnThisMachine_AcceptsASocketAndALoopbackAddressAndNothingElse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		host string
		want bool
	}{
		{"/var/run/postgresql", true},
		{"127.0.0.1", true},
		{"127.0.0.53", true},
		{"::1", true},
		{"localhost", true},
		{"db.example.com", false},
		{"10.0.0.1", false},
		{"192.0.2.1", false},
		{"", false},
	} {
		t.Run(c.host, func(t *testing.T) {
			if got := onThisMachine(c.host); got != c.want {
				t.Errorf("onThisMachine(%q) = %v, want %v", c.host, got, c.want)
			}
		})
	}
}

func TestConfigure_BoundsEveryConnectionAndNotOnlyTheFirst(t *testing.T) {
	t.Parallel()
	cfg, err := configure("postgres://u:p@127.0.0.1:5432/x")
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.ConnConfig.ConnectTimeout; got != connectTimeout {
		t.Errorf("ConnectTimeout = %v, want %v: the pool opens connections after the first one, "+
			"and those fall to the driver's own default", got, connectTimeout)
	}
}
