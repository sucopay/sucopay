package webhook_test

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/payment"
	"github.com/sucopay/sucopay/internal/postgres"
	"github.com/sucopay/sucopay/internal/postgres/postgrestest"
	"github.com/sucopay/sucopay/internal/webhook"
)

// The account the schema creates, and a second one made here. One account
// can never show that an endpoint is of an account.
const (
	first = payment.AccountID("00000000-0000-0000-0000-000000000001")
	other = payment.AccountID("00000000-0000-0000-0000-000000000002")
)

var now = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// opened returns a migrated database with the second account in it, and
// the pool over it.
func opened(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.Open(t.Context(), postgrestest.Fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Conns().Exec(t.Context(),
		`insert into accounts (id, name) values ($1, 'second')`, other); err != nil {
		t.Fatal(err)
	}
	return pool.Conns()
}

func store(t *testing.T) (*webhook.Postgres, *pgxpool.Pool) {
	t.Helper()
	cipher, err := webhook.NewCipher([32]byte{7})
	if err != nil {
		t.Fatal(err)
	}
	pool := opened(t)
	return webhook.NewPostgres(pool, cipher, "k1"), pool
}

func registration(url string) webhook.Registration {
	return webhook.Registration{URL: url, Description: "orders", Events: []string{"payment.succeeded"}}
}

func created(t *testing.T, s *webhook.Postgres, account payment.AccountID, url string) (webhook.Endpoint, webhook.Secret) {
	t.Helper()
	e, secret, err := s.Create(t.Context(), account, registration(url), now)
	if err != nil {
		t.Fatal(err)
	}
	return e, secret
}

func TestPostgres_ListsAndReadsAnEndpointForItsAccountAlone(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	e, _ := created(t, s, first, "https://hooks.example/a")

	got, err := s.Get(t.Context(), first, e.ID)
	if err != nil || !reflect.DeepEqual(got, e) {
		t.Errorf("Get = %+v, %v; want %+v", got, err, e)
	}
	if _, err := s.Get(t.Context(), other, e.ID); !errors.Is(err, webhook.ErrNotFound) {
		t.Errorf("another account's Get = %v, want ErrNotFound", err)
	}
	if list, err := s.List(t.Context(), first); err != nil || !reflect.DeepEqual(list, []webhook.Endpoint{e}) {
		t.Errorf("List = %+v, %v; want the one endpoint", list, err)
	}
	if list, err := s.List(t.Context(), other); err != nil || len(list) != 0 {
		t.Errorf("another account's List = %+v, %v; want none", list, err)
	}
}

func TestPostgres_HoldsTheSecretSealedAndOpensItForTheSigner(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	e, secret := created(t, s, first, "https://hooks.example/a")

	var sealed []byte
	if err := pool.QueryRow(t.Context(), `select secret from webhook_endpoints where id = $1`, e.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), string(secret)) {
		t.Error("the row holds the secret in the clear")
	}
	secrets, err := s.Secrets(t.Context(), e.ID, now)
	if err != nil || len(secrets) != 1 || secrets[0] != secret {
		t.Errorf("Secrets = %d, %v; want the one shown at registration", len(secrets), err)
	}
}

func TestPostgres_RefusesANinthEndpoint(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	for range webhook.MaxEndpoints {
		created(t, s, first, "https://hooks.example/a")
	}

	_, _, err := s.Create(t.Context(), first, registration("https://hooks.example/a"), now)

	if !errors.Is(err, webhook.ErrTooMany) {
		t.Errorf("err = %v, want ErrTooMany", err)
	}
	// Another account has room of its own.
	created(t, s, other, "https://hooks.example/b")
}

func TestPostgres_UpdatesWhatChangesNameAndNothingElse(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	e, _ := created(t, s, first, "https://hooks.example/a")
	url := "https://hooks.example/b"
	off := false

	got, err := s.Update(t.Context(), first, e.ID, webhook.Changes{URL: &url, Enabled: &off})

	if err != nil {
		t.Fatal(err)
	}
	if got.URL != url || got.Enabled || got.Description != e.Description || !reflect.DeepEqual(got.Events, e.Events) {
		t.Errorf("Update = %+v, want url and enabled changed and the rest kept", got)
	}
	all := []string(nil)
	if got, err := s.Update(t.Context(), first, e.ID, webhook.Changes{Events: &all}); err != nil || got.Events != nil {
		t.Errorf("Update to every event = %v, %v; want nil events", got.Events, err)
	}
	if _, err := s.Update(t.Context(), other, e.ID, webhook.Changes{URL: &url}); !errors.Is(err, webhook.ErrNotFound) {
		t.Errorf("another account's Update = %v, want ErrNotFound", err)
	}
}

func TestPostgres_KeepsAReplacedSecretForTheGraceAndNoLonger(t *testing.T) {
	t.Parallel()
	s, _ := store(t)
	e, old := created(t, s, first, "https://hooks.example/a")

	fresh, err := s.Rotate(t.Context(), first, e.ID, now)

	if err != nil || fresh == old {
		t.Fatalf("Rotate = %v, want a new secret", err)
	}
	within, err := s.Secrets(t.Context(), e.ID, now.Add(webhook.RotationGrace-time.Second))
	if err != nil || len(within) != 2 || within[0] != fresh || within[1] != old {
		t.Errorf("Secrets within the grace = %d, %v; want the new then the old", len(within), err)
	}
	after, err := s.Secrets(t.Context(), e.ID, now.Add(webhook.RotationGrace))
	if err != nil || len(after) != 1 || after[0] != fresh {
		t.Errorf("Secrets after the grace = %d, %v; want the new alone", len(after), err)
	}
	if _, err := s.Rotate(t.Context(), other, e.ID, now); !errors.Is(err, webhook.ErrNotFound) {
		t.Errorf("another account's Rotate = %v, want ErrNotFound", err)
	}
}

// A key swapped under a deployment leaves every secret it sealed closed.
// Rotation seals a new one under the key in force; the old one, which the
// grace would keep, is left out rather than failing the signer.
func TestPostgres_SignsUnderTheKeyInForceAfterTheKeyWasSwapped(t *testing.T) {
	t.Parallel()
	before, pool := store(t)
	e, _ := created(t, before, first, "https://hooks.example/a")
	swapped, err := webhook.NewCipher([32]byte{8})
	if err != nil {
		t.Fatal(err)
	}
	after := webhook.NewPostgres(pool, swapped, "k2")

	if _, err := after.Secrets(t.Context(), e.ID, now); err == nil {
		t.Error("a secret sealed under the old key opened under the new")
	}
	fresh, err := after.Rotate(t.Context(), first, e.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := after.Secrets(t.Context(), e.ID, now)
	if err != nil || len(secrets) != 1 || secrets[0] != fresh {
		t.Errorf("Secrets after rotation = %d, %v; want the new one alone", len(secrets), err)
	}
	var keyID string
	if err := pool.QueryRow(t.Context(), `select key_id from webhook_endpoints where id = $1`, e.ID).Scan(&keyID); err != nil || keyID != "k2" {
		t.Errorf("key_id = %q, %v; want the key in force", keyID, err)
	}
}

func TestPostgres_DeleteTakesAnEndpointOutOfTheListAndFailsWhatWasPending(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	e, _ := created(t, s, first, "https://hooks.example/a")
	if _, err := pool.Exec(t.Context(), `
		insert into webhook_deliveries (id, endpoint_id, account_id, type, occurred_at, payload, state, next_at, created_at)
		values ('d1', $1, $2, 'endpoint.test', $3, '{}', 'pending', $3, $3)`, e.ID, first, now); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(t.Context(), other, e.ID, now); !errors.Is(err, webhook.ErrNotFound) {
		t.Fatalf("another account's Delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete(t.Context(), first, e.ID, now); err != nil {
		t.Fatal(err)
	}

	if list, err := s.List(t.Context(), first); err != nil || len(list) != 0 {
		t.Errorf("List after Delete = %+v, %v; want none", list, err)
	}
	if _, err := s.Get(t.Context(), first, e.ID); !errors.Is(err, webhook.ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete(t.Context(), first, e.ID, now); !errors.Is(err, webhook.ErrNotFound) {
		t.Errorf("a second Delete = %v, want ErrNotFound", err)
	}
	var state string
	if err := pool.QueryRow(t.Context(), `select state from webhook_deliveries where id = 'd1'`).Scan(&state); err != nil || state != "failed" {
		t.Errorf("the pending delivery is %q, %v; want failed", state, err)
	}
	// Room is given back: the deleted one no longer counts.
	for range webhook.MaxEndpoints {
		created(t, s, first, "https://hooks.example/a")
	}
}

func TestPostgres_ReadsTheAllowanceTheOperatorWrote(t *testing.T) {
	t.Parallel()
	s, pool := store(t)
	e, _ := created(t, s, first, "https://erp.internal/in")

	if allowed, err := s.Allowed(t.Context(), e.ID); err != nil || len(allowed) != 0 {
		t.Errorf("Allowed = %v, %v; want none", allowed, err)
	}
	if _, err := pool.Exec(t.Context(), `update webhook_endpoints set allowed = '{10.0.5.0/24}' where id = $1`, e.ID); err != nil {
		t.Fatal(err)
	}
	allowed, err := s.Allowed(t.Context(), e.ID)
	if err != nil || len(allowed) != 1 || allowed[0] != netip.MustParsePrefix("10.0.5.0/24") {
		t.Errorf("Allowed = %v, %v; want the one prefix", allowed, err)
	}
}

// The schema keeps scope and account together: an account's endpoint names
// its account, and a deployment's names none.
func TestSchema_RefusesAnEndpointWhoseScopeAndAccountDisagree(t *testing.T) {
	t.Parallel()
	_, pool := store(t)
	for what, row := range map[string][]any{
		"account without an account": {"e1", "account", nil},
		"deployment with an account": {"e2", "deployment", first},
	} {
		if _, err := pool.Exec(t.Context(), `
			insert into webhook_endpoints (id, scope, account_id, url, secret, created_at)
			values ($1, $2, $3, 'https://hooks.example/a', '\x00', now())`, row...); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
}
