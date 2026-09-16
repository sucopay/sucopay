package webhook

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sucopay/sucopay/internal/payment"
)

// KeyPurpose is what the key the secrets are sealed under is derived for.
// A constant, because a secret sealed under a key derived for one name
// opens under that name alone: changing it is a migration, not an edit.
const KeyPurpose = "suco webhook secret"

// storeTimeout bounds one call to the database.
const storeTimeout = 5 * time.Second

// Postgres keeps endpoints in the database, their secrets sealed.
//
// Every secret this writes is sealed under the cipher and says so in key_id,
// as a credential's hash says which key made it. A deployment whose key was
// swapped holds secrets this cannot open; [Postgres.Secrets] says so for
// one, and the rows say so for all.
type Postgres struct {
	pool   *pgxpool.Pool
	cipher Cipher
	keyID  string
}

// NewPostgres stores endpoints behind pool, sealing secrets with cipher and
// marking them as sealed under keyID.
func NewPostgres(pool *pgxpool.Pool, cipher Cipher, keyID string) *Postgres {
	return &Postgres{pool: pool, cipher: cipher, keyID: keyID}
}

// Registration is what a merchant supplies for a new endpoint. The URL has
// passed [Check] by the time it arrives here; the store holds what it was
// given.
type Registration struct {
	URL         string
	Description string
	// Events is what the endpoint receives, and nil for everything.
	Events []string
}

// columns is what an Endpoint is read from, in the order scan reads them.
const columns = `id, scope, account_id, url, description, events, enabled, created_at`

// Create registers an endpoint of one account and returns its secret, which
// then exists nowhere in the clear: the row holds it sealed, and the
// response that shows it is the last time it is shown.
//
// The account's row is held while its endpoints are counted, so that two
// registrations at once cannot both find room for one more.
func (s *Postgres) Create(ctx context.Context, account payment.AccountID, r Registration, now time.Time) (_ Endpoint, _ Secret, err error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	id, err := NewID()
	if err != nil {
		return Endpoint{}, "", err
	}
	secret := NewSecret()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Endpoint{}, "", fmt.Errorf("endpoint %s: %w", id, err)
	}
	defer func() { err = unwind(ctx, tx, "endpoint "+id.String(), err) }()

	if _, err := tx.Exec(ctx, `select 1 from accounts where id = $1 for update`, account); err != nil {
		return Endpoint{}, "", fmt.Errorf("endpoint %s: %w", id, err)
	}
	var held int
	if err := tx.QueryRow(ctx, `
		select count(*) from webhook_endpoints
		 where account_id = $1 and deleted_at is null`, account).Scan(&held); err != nil {
		return Endpoint{}, "", fmt.Errorf("endpoint %s: %w", id, err)
	}
	if held >= MaxEndpoints {
		return Endpoint{}, "", ErrTooMany
	}
	if _, err := tx.Exec(ctx, `
		insert into webhook_endpoints
			(id, scope, account_id, url, description, events, enabled, secret, key_id, created_at)
		values ($1, $2, $3, $4, $5, $6, true, $7, $8, $9)`,
		id, ScopeAccount, account, r.URL, r.Description, r.Events, s.cipher.Seal(secret), s.keyID, now); err != nil {
		return Endpoint{}, "", fmt.Errorf("endpoint %s: %w", id, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Endpoint{}, "", fmt.Errorf("endpoint %s: %w", id, err)
	}
	return Endpoint{ID: id, Scope: ScopeAccount, Account: account, URL: r.URL,
		Description: r.Description, Events: r.Events, Enabled: true, CreatedAt: now.UTC()}, secret, nil
}

// List reads the endpoints of one account, oldest first. The deployment's
// endpoints are not among them: they are the operator's, and an account
// reading its own list learns nothing of them.
func (s *Postgres) List(ctx context.Context, account payment.AccountID) ([]Endpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select `+columns+`
		  from webhook_endpoints
		 where account_id = $1 and deleted_at is null
		 order by created_at, id`, account)
	if err != nil {
		return nil, fmt.Errorf("endpoints of %s: %w", account, err)
	}
	defer rows.Close()
	out := []Endpoint{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("endpoints of %s: %w", account, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get reads one endpoint of one account, or [ErrNotFound].
func (s *Postgres) Get(ctx context.Context, account payment.AccountID, id ID) (Endpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	e, err := scan(s.pool.QueryRow(ctx, `
		select `+columns+`
		  from webhook_endpoints
		 where id = $1 and account_id = $2 and deleted_at is null`, id, account))
	if errors.Is(err, pgx.ErrNoRows) {
		return Endpoint{}, ErrNotFound
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("endpoint %s: %w", id, err)
	}
	return e, nil
}

// Changes are what an update may set. A nil pointer leaves the field as it
// is; Events set to a nil slice means everything.
type Changes struct {
	URL         *string
	Description *string
	Events      *[]string
	Enabled     *bool
}

// Update sets what changes names on one endpoint of one account, and reads
// the endpoint back.
func (s *Postgres) Update(ctx context.Context, account payment.AccountID, id ID, changes Changes) (Endpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	// coalesce for the strings and the flag; events cannot go through it,
	// since null is a value there.
	tag, err := s.pool.Exec(ctx, `
		update webhook_endpoints
		   set url = coalesce($3, url),
		       description = coalesce($4, description),
		       events = case when $5::boolean then $6::text[] else events end,
		       enabled = coalesce($7, enabled)
		 where id = $1 and account_id = $2 and deleted_at is null`,
		id, account, changes.URL, changes.Description, changes.Events != nil, eventsOf(changes.Events), changes.Enabled)
	if err != nil {
		return Endpoint{}, fmt.Errorf("endpoint %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return Endpoint{}, ErrNotFound
	}
	return s.Get(ctx, account, id)
}

func eventsOf(events *[]string) []string {
	if events == nil {
		return nil
	}
	return *events
}

// Rotate replaces the secret of one endpoint of one account, keeping the
// one it replaced in force for [RotationGrace], and returns the new one.
func (s *Postgres) Rotate(ctx context.Context, account payment.AccountID, id ID, now time.Time) (Secret, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	secret := NewSecret()
	tag, err := s.pool.Exec(ctx, `
		update webhook_endpoints
		   set previous_secret = secret, previous_until = $3, secret = $4, key_id = $5
		 where id = $1 and account_id = $2 and deleted_at is null`,
		id, account, now.Add(RotationGrace), s.cipher.Seal(secret), s.keyID)
	if err != nil {
		return "", fmt.Errorf("endpoint %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return secret, nil
}

// Delete removes one endpoint of one account from what is sent to and
// listed. Its row stays, so that the deliveries made to it keep what they
// point at; and its pending deliveries fail, since nothing will send them.
func (s *Postgres) Delete(ctx context.Context, account payment.AccountID, id ID, now time.Time) (err error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("endpoint %s: %w", id, err)
	}
	defer func() { err = unwind(ctx, tx, "endpoint "+id.String(), err) }()
	tag, err := tx.Exec(ctx, `
		update webhook_endpoints
		   set deleted_at = $3, enabled = false
		 where id = $1 and account_id = $2 and deleted_at is null`, id, account, now)
	if err != nil {
		return fmt.Errorf("endpoint %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `
		update webhook_deliveries
		   set state = 'failed', next_at = null
		 where endpoint_id = $1 and state = 'pending'`, id); err != nil {
		return fmt.Errorf("endpoint %s: %w", id, err)
	}
	return tx.Commit(ctx)
}

// Secrets are what a delivery to an endpoint is signed under at now: the
// secret in force, and the one it replaced while that is still within its
// grace. The one in force comes first.
//
// A replaced secret that cannot be opened is left out rather than failing
// the call: it was sealed under a key since swapped, and the one in force
// was sealed by the rotation that replaced it. Only the one in force being
// unopenable is an error, since nothing can then be signed.
//
// By id alone, and of a deleted endpoint too: the signer holds a delivery,
// which names its endpoint, and a delivery made before the endpoint was
// deleted is still signed for it. Nothing that answers a merchant calls
// this; what a merchant sees of a secret is [Postgres.Create] and
// [Postgres.Rotate], which are of one account.
func (s *Postgres) Secrets(ctx context.Context, id ID, now time.Time) ([]Secret, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var current, previous []byte
	var until *time.Time
	err := s.pool.QueryRow(ctx, `
		select secret, previous_secret, previous_until
		  from webhook_endpoints where id = $1`, id).Scan(&current, &previous, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("endpoint %s: %w", id, err)
	}
	first, err := s.cipher.Open(current)
	if err != nil {
		return nil, fmt.Errorf("endpoint %s: %w", id, err)
	}
	out := []Secret{first}
	if previous != nil && until != nil && now.Before(*until) {
		if second, err := s.cipher.Open(previous); err == nil {
			out = append(out, second)
		}
	}
	return out, nil
}

// Allowed are the addresses the operator let one endpoint reach although
// they are inside the deployment.
//
// By id alone, as [Postgres.Secrets] is, and for the same caller. A handler
// answering a merchant settles first that the endpoint is theirs, through
// [Postgres.Get]: whether a URL is refused or allowed is otherwise an answer
// about another account's allowance.
func (s *Postgres) Allowed(ctx context.Context, id ID) ([]netip.Prefix, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var text []string
	err := s.pool.QueryRow(ctx, `select allowed from webhook_endpoints where id = $1`, id).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("endpoint %s: %w", id, err)
	}
	out := make([]netip.Prefix, 0, len(text))
	for _, t := range text {
		p, err := netip.ParsePrefix(t)
		if err != nil {
			return nil, fmt.Errorf("endpoint %s: allowance %w", id, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// unwind rolls a transaction back and reports the rollback under what, and
// only where nothing else went wrong; after a commit the transaction is
// already closed, which is the ordinary path. The same shape as the payment
// store's, for the same reasons.
func unwind(ctx context.Context, tx pgx.Tx, what string, err error) error {
	back, stop := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
	defer stop()
	rollback := tx.Rollback(back)
	if rollback != nil && !errors.Is(rollback, pgx.ErrTxClosed) && err == nil {
		return fmt.Errorf("%s: %w", what, rollback)
	}
	return err
}

// scan reads one endpoint from a row of columns.
func scan(row pgx.Row) (Endpoint, error) {
	var (
		e       Endpoint
		account *string
		events  []string
	)
	if err := row.Scan(&e.ID, &e.Scope, &account, &e.URL, &e.Description, &events, &e.Enabled, &e.CreatedAt); err != nil {
		return Endpoint{}, err
	}
	if account != nil {
		e.Account = payment.AccountID(*account)
	}
	e.Events = events
	e.CreatedAt = e.CreatedAt.UTC()
	return e, nil
}
