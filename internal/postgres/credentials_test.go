package postgres_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// column is one column of a credentials row and what goes in it.
type column struct {
	name  string
	value any
}

// credential is a row the schema accepts: of one account, readable, with
// every column stated. Each test below changes one thing about it.
func credential(i int) []column {
	return []column{
		{"id", uuid(i)},
		{"account_id", "00000000-0000-0000-0000-000000000001"},
		{"scope", "account"},
		{"capability", "read"},
		// Distinct per row, as the hash of a distinct token would be.
		{"hash", []byte(uuid(i))},
		{"key_id", "k"},
		{"created_at", time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)},
	}
}

// deployment is credential with its scope moved to the deployment, which
// names no account.
func deployment(i int) []column {
	return with(with(credential(i), "scope", "deployment"), "account_id", nil)
}

func uuid(i int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
}

// without returns the row with one column left out, which is not the same as
// setting it to null: a default answers for the column left out and not for
// the null.
func without(row []column, name string) []column {
	return slices.DeleteFunc(slices.Clone(row), func(c column) bool { return c.name == name })
}

func with(row []column, name string, value any) []column {
	return append(without(row, name), column{name, value})
}

// insert writes one row with exactly the columns given. The statement is built
// from them, because which columns a row may leave out is what is under test.
func insert(t *testing.T, pool *pgxpool.Pool, row []column) error {
	t.Helper()
	names := make([]string, len(row))
	marks := make([]string, len(row))
	values := make([]any, len(row))
	for i, c := range row {
		names[i] = c.name
		marks[i] = fmt.Sprintf("$%d", i+1)
		values[i] = c.value
	}
	_, err := pool.Exec(t.Context(),
		`insert into credentials (`+strings.Join(names, ", ")+`) values (`+strings.Join(marks, ", ")+`)`,
		values...)
	return err
}

func TestSchema_AcceptsEachScopeAndCapabilityAndNoOther(t *testing.T) {
	t.Parallel()
	pool := migrated(t).Conns()

	for _, c := range []struct {
		name string
		row  []column
		want bool
	}{
		{"scope account", credential(1), true},
		{"scope deployment", deployment(2), true},
		// Both ways, because a rule that only compares the scope with the
		// account would let an unknown scope in as long as it names none.
		{"scope tenant, naming an account", with(credential(3), "scope", "tenant"), false},
		{"scope tenant, naming none", with(deployment(4), "scope", "tenant"), false},
		{"capability read", credential(5), true},
		{"capability write", with(credential(6), "capability", "write"), true},
		{"capability admin", with(credential(7), "capability", "admin"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := insert(t, pool, c.row)
			if c.want && err != nil {
				t.Errorf("refused a credential of %s: %v", c.name, err)
			}
			if !c.want && err == nil {
				t.Errorf("accepted a credential of %s, which nothing defines", c.name)
			}
		})
	}
}

func TestSchema_RefusesACredentialWithAColumnLeftOut(t *testing.T) {
	t.Parallel()
	// What a credential may leave out is what it does not have yet: its
	// revocation, its last use, and, for the deployment, its account. Nothing
	// else has a default to answer for it.
	pool := migrated(t).Conns()

	for i, name := range []string{"id", "scope", "capability", "hash", "key_id", "created_at"} {
		t.Run("without "+name, func(t *testing.T) {
			if err := insert(t, pool, without(credential(i), name)); err == nil {
				t.Errorf("accepted a credential that did not say its %s", name)
			}
		})
	}
}

func TestSchema_RefusesACredentialOfAnAccountThatDoesNotExist(t *testing.T) {
	t.Parallel()
	pool := migrated(t).Conns()

	err := insert(t, pool, with(credential(1), "account_id", uuid(9)))

	if err == nil {
		t.Error("accepted a credential of an account the database does not hold")
	}
}

func TestSchema_RefusesACredentialWhoseScopeAndAccountDisagree(t *testing.T) {
	t.Parallel()
	// The scope is what says which. A row where the two differ is one that a
	// reader going by the scope and a reader going by the account take
	// differently, and the wider reading is the one that costs.
	pool := migrated(t).Conns()

	t.Run("of one account, naming none", func(t *testing.T) {
		if err := insert(t, pool, with(credential(1), "account_id", nil)); err == nil {
			t.Error("accepted a credential of one account that names no account")
		}
	})
	t.Run("of the deployment, naming one", func(t *testing.T) {
		if err := insert(t, pool, with(credential(2), "scope", "deployment")); err == nil {
			t.Error("accepted a credential of the deployment that names an account")
		}
	})
}

func TestSchema_HoldsEachHashOnce(t *testing.T) {
	t.Parallel()
	// A lookup by hash expects one row. Two tokens do not hash alike, so a
	// second row with the same hash is a mistake rather than a collision.
	pool := migrated(t).Conns()
	if err := insert(t, pool, credential(1)); err != nil {
		t.Fatal(err)
	}

	err := insert(t, pool, with(credential(2), "hash", []byte(uuid(1))))

	if err == nil {
		t.Error("accepted a second credential with the hash of the first")
	}
}
