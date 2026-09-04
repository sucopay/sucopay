package credential_test

import (
	"fmt"
	"testing"

	"github.com/sucopay/sucopay/internal/credential"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
)

// Every scope and every capability the domain defines.
var (
	scopes       = []credential.Scope{credential.ScopeAccount, credential.ScopeDeployment}
	capabilities = []credential.Capability{credential.ReadOnly, credential.ReadWrite}
)

// The schema constrains a credential's scope and capability to the same lists
// this package defines. Two places hold each list, so this is what keeps them
// together: a value added here without a migration fails, and so does one
// dropped from the schema while the domain still has it.
func TestSchema_AcceptsEveryScopeAndCapabilityTheDomainDefinesAndNoOther(t *testing.T) {
	t.Parallel()
	pool := opened(t, postgrestest.Fresh(t))

	insert := func(t *testing.T, i int, scope, capability string) error {
		t.Helper()
		// The schema ties the account to the scope, so a scope is tried with
		// the account it calls for, and refused for the scope alone.
		var account *credential.AccountID
		if scope == string(credential.ScopeAccount) {
			account = new(first)
		}
		_, err := pool.Exec(t.Context(), `
			insert into credentials (id, account_id, scope, capability, hash, key_id, created_at)
			values ($1, $2, $3, $4, $5, 'k', now())`,
			fmt.Sprintf("00000000-0000-0000-0000-%012d", i), account, scope, capability, []byte{byte(i)})
		return err
	}

	for i, scope := range scopes {
		t.Run("scope "+string(scope), func(t *testing.T) {
			if err := insert(t, i, string(scope), string(credential.ReadOnly)); err != nil {
				t.Errorf("the schema refused scope %s, which the domain defines: %v", scope, err)
			}
		})
	}
	for i, capability := range capabilities {
		t.Run("capability "+string(capability), func(t *testing.T) {
			if err := insert(t, 10+i, string(credential.ScopeAccount), string(capability)); err != nil {
				t.Errorf("the schema refused capability %s, which the domain defines: %v", capability, err)
			}
		})
	}
	t.Run("a scope the domain does not define", func(t *testing.T) {
		if err := insert(t, 20, "tenant", string(credential.ReadOnly)); err == nil {
			t.Error("the schema accepted a scope the domain does not define")
		}
	})
	t.Run("a capability the domain does not define", func(t *testing.T) {
		if err := insert(t, 21, string(credential.ScopeAccount), "admin"); err == nil {
			t.Error("the schema accepted a capability the domain does not define")
		}
	})
}
