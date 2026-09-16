package credential_test

import (
	"fmt"
	"testing"

	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// Every scope and every access the domain defines.
var (
	scopes   = []credential.Scope{credential.ScopeAccount, credential.ScopeDeployment}
	accesses = []credential.Access{credential.ReadOnly, credential.ReadWrite}
)

// The schema constrains a credential's scope and access to the same lists
// this package defines. Two places hold each list, so this is what keeps them
// together: a value added here without a migration fails, and so does one
// dropped from the schema while the domain still has it.
func TestSchema_AcceptsEveryScopeAndAccessTheDomainDefinesAndNoOther(t *testing.T) {
	t.Parallel()
	pool := opened(t, postgrestest.Fresh(t))

	insert := func(t *testing.T, i int, scope, access string) error {
		t.Helper()
		// The schema ties the account to the scope, so a scope is tried with
		// the account it calls for, and refused for the scope alone.
		var account *credential.AccountID
		if scope == string(credential.ScopeAccount) {
			account = new(first)
		}
		_, err := pool.Exec(t.Context(), `
			insert into credentials (id, account_id, scope, access, hash, key_id, created_at)
			values ($1, $2, $3, $4, $5, 'k', now())`,
			fmt.Sprintf("00000000-0000-0000-0000-%012d", i), account, scope, access, []byte{byte(i)})
		return err
	}

	for i, scope := range scopes {
		t.Run("scope "+string(scope), func(t *testing.T) {
			if err := insert(t, i, string(scope), string(credential.ReadOnly)); err != nil {
				t.Errorf("the schema refused scope %s, which the domain defines: %v", scope, err)
			}
		})
	}
	for i, access := range accesses {
		t.Run("access "+string(access), func(t *testing.T) {
			if err := insert(t, 10+i, string(credential.ScopeAccount), string(access)); err != nil {
				t.Errorf("the schema refused access %s, which the domain defines: %v", access, err)
			}
		})
	}
	t.Run("a scope the domain does not define", func(t *testing.T) {
		if err := insert(t, 20, "tenant", string(credential.ReadOnly)); err == nil {
			t.Error("the schema accepted a scope the domain does not define")
		}
	})
	t.Run("an access the domain does not define", func(t *testing.T) {
		if err := insert(t, 21, string(credential.ScopeAccount), "admin"); err == nil {
			t.Error("the schema accepted an access the domain does not define")
		}
	})
}
