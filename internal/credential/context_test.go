package credential_test

import (
	"testing"

	"github.com/sucopay/sucopay/internal/credential"
)

func TestFromContext_ReturnsWhatNewContextPutIn(t *testing.T) {
	t.Parallel()
	c := credential.Credential{ID: "4a2f", Scope: credential.ScopeAccount, Account: "acct", Access: credential.ReadWrite, KeyID: "k1"}

	got, ok := credential.FromContext(credential.NewContext(t.Context(), c))

	if !ok {
		t.Fatal("reports no credential in a context that was given one")
	}
	if got != c {
		t.Errorf("got %+v, want %+v", got, c)
	}
}

func TestFromContext_ReportsAContextThatCarriesNone(t *testing.T) {
	t.Parallel()
	got, ok := credential.FromContext(t.Context())

	if ok {
		t.Errorf("reports %+v in a context that was given none", got)
	}
}
