package webhook

import (
	"context"
	"encoding/json"
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
	return allowances(text), nil
}

// allowances reads what the operator wrote in an endpoint's allowed column.
// An entry that is not a prefix allows nothing: read as an error, one
// mistyped entry would stop every delivery the round reads, and read as a
// wider allowance it would open what the operator did not mean to.
func allowances(text []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(text))
	for _, t := range text {
		if p, err := netip.ParsePrefix(t); err == nil {
			out = append(out, p)
		}
	}
	return out
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

// Expand turns up to limit rows of the outbox, oldest first, into
// deliveries: one per endpoint that receives the event, among the enabled
// endpoints of the payment's account and of the deployment. Each row is
// removed once its deliveries exist, and a row no endpoint receives is
// removed with none. It answers with how many rows it took.
//
// Row by row in a transaction of its own, so that a row that cannot be
// expanded stops nothing but itself. Rows are taken with the lock skipped,
// so that two instances do not expand one twice; the lease makes that rare
// rather than impossible.
func (s *Postgres) Expand(ctx context.Context, now time.Time, limit int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	taken := 0
	for ; taken < limit; taken++ {
		more, err := s.expandOne(ctx, now)
		if err != nil {
			return taken, err
		}
		if !more {
			return taken, nil
		}
	}
	return taken, nil
}

// expandOne takes the oldest row of the outbox, and says whether there was
// one.
func (s *Postgres) expandOne(ctx context.Context, now time.Time) (more bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("outbox: %w", err)
	}
	defer func() { err = unwind(ctx, tx, "outbox", err) }()

	var (
		rowID      int64
		account    payment.AccountID
		paymentID  payment.ID
		eventType  string
		data       []byte
		occurredAt time.Time
	)
	err = tx.QueryRow(ctx, `
		select id, account_id, payment_id, event, payload, created_at
		  from outbox order by id limit 1 for update skip locked`).
		Scan(&rowID, &account, &paymentID, &eventType, &data, &occurredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("outbox: %w", err)
	}
	receiving, err := s.receiving(ctx, tx, account, eventType)
	if err != nil {
		return false, fmt.Errorf("outbox %d: %w", rowID, err)
	}
	if len(receiving) > 0 {
		body, err := Envelope(eventType, occurredAt, account, json.RawMessage(data))
		if err != nil {
			return false, fmt.Errorf("outbox %d: %w", rowID, err)
		}
		for _, endpoint := range receiving {
			id, err := NewID()
			if err != nil {
				return false, err
			}
			if _, err := tx.Exec(ctx, `
				insert into webhook_deliveries
					(id, endpoint_id, account_id, payment_id, type, occurred_at, payload,
					 state, attempts, next_at, created_at)
				values ($1, $2, $3, $4, $5, $6, $7, $8, 0, $9, $9)`,
				id, endpoint, account, paymentID, eventType, occurredAt, body, Pending, now); err != nil {
				return false, fmt.Errorf("outbox %d: %w", rowID, err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `delete from outbox where id = $1`, rowID); err != nil {
		return false, fmt.Errorf("outbox %d: %w", rowID, err)
	}
	return true, tx.Commit(ctx)
}

// receiving is the id of every endpoint an event of eventType about a
// payment of account goes to: the account's own and the deployment's, the
// enabled ones, and of those the ones whose list has the type or is no
// list.
func (s *Postgres) receiving(ctx context.Context, tx pgx.Tx, account payment.AccountID, eventType string) ([]ID, error) {
	rows, err := tx.Query(ctx, `
		select id, events from webhook_endpoints
		 where enabled and deleted_at is null
		   and (account_id = $1 or scope = $2)
		 order by created_at, id`, account, ScopeDeployment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ID
	for rows.Next() {
		var e Endpoint
		if err := rows.Scan(&e.ID, &e.Events); err != nil {
			return nil, err
		}
		e.Enabled = true
		if e.Receives(eventType) {
			out = append(out, e.ID)
		}
	}
	return out, rows.Err()
}

// Due is a delivery whose time has come, with what sending it takes from
// its endpoint.
type Due struct {
	Delivery Delivery
	URL      string
	Allowed  []netip.Prefix
}

// Due reads up to limit deliveries whose next attempt is due at now, and no
// more than perEndpoint to any one endpoint, so that one slow receiver does
// not take the round. Deliveries to a disabled or deleted endpoint are
// left where they are, and a delivery is due only once every earlier one
// to the same endpoint about the same payment is delivered or failed: the
// events of one payment reach a receiver in the order they happened.
//
// The bound per endpoint is applied before the bound on the round, in the
// query: applied after, a backlog to one endpoint longer than a round would
// fill the round with rows the bound then drops, and the other endpoints'
// deliveries would never be read.
func (s *Postgres) Due(ctx context.Context, now time.Time, limit, perEndpoint int) ([]Due, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select id, endpoint_id, account_id, payment_id, type, occurred_at,
		       payload, state, attempts, next_at, url, allowed
		  from (
		    select d.id, d.endpoint_id, d.account_id, d.payment_id, d.type, d.occurred_at,
		           d.payload, d.state, d.attempts, d.next_at, d.seq, e.url, e.allowed,
		           row_number() over (partition by d.endpoint_id order by d.next_at, d.seq) as nth
		      from webhook_deliveries d
		      join webhook_endpoints e on e.id = d.endpoint_id
		     where d.state = $1 and d.next_at <= $2
		       and e.enabled and e.deleted_at is null
		       and not exists (
		             select 1 from webhook_deliveries p
		              where p.endpoint_id = d.endpoint_id
		                and p.payment_id is not distinct from d.payment_id
		                and p.state = $1 and p.seq < d.seq)
		  ) due
		 where nth <= $4
		 order by next_at, seq
		 limit $3`, Pending, now, limit, perEndpoint)
	if err != nil {
		return nil, fmt.Errorf("deliveries due: %w", err)
	}
	defer rows.Close()
	var out []Due
	for rows.Next() {
		var (
			due       Due
			paymentID *string
			allowed   []string
		)
		d := &due.Delivery
		if err := rows.Scan(&d.ID, &d.Endpoint, &d.Account, &paymentID, &d.Type, &d.OccurredAt,
			&d.Body, &d.State, &d.Attempts, &d.NextAt, &due.URL, &allowed); err != nil {
			return nil, fmt.Errorf("deliveries due: %w", err)
		}
		if paymentID != nil {
			d.Payment = payment.ID(*paymentID)
		}
		d.OccurredAt, d.NextAt = d.OccurredAt.UTC(), d.NextAt.UTC()
		due.Allowed = allowances(allowed)
		out = append(out, due)
	}
	return out, rows.Err()
}

// Attempted writes down one attempt at d made at now, and what it leaves the
// delivery as: delivered on a 2xx, due again after the interval that follows
// this many attempts, or failed after the last. random is for the interval's
// jitter.
func (s *Postgres) Attempted(ctx context.Context, d Delivery, o Outcome, now time.Time, random func() float64) error {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delivery %s: %w", d.ID, err)
	}
	defer func() { err = unwind(ctx, tx, "delivery "+d.ID.String(), err) }()

	var status *int
	if o.Status != 0 {
		status = &o.Status
	}
	if _, err := tx.Exec(ctx, `
		insert into webhook_attempts (delivery_id, at, status, reason, response, took_ms)
		values ($1, $2, $3, $4, $5, $6)`,
		d.ID, now, status, o.Reason, o.Response, o.Took.Milliseconds()); err != nil {
		return fmt.Errorf("delivery %s: %w", d.ID, err)
	}
	attempts := d.Attempts + 1
	state, next, delivered := Failed, (*time.Time)(nil), (*time.Time)(nil)
	switch again, ok := NextAttempt(attempts, now, random); {
	case o.Delivered():
		state, delivered = Delivered, &now
	case ok:
		state, next = Pending, &again
	}
	if _, err := tx.Exec(ctx, `
		update webhook_deliveries
		   set state = $2, attempts = $3, next_at = $4, delivered_at = $5
		 where id = $1`, d.ID, state, attempts, next, delivered); err != nil {
		return fmt.Errorf("delivery %s: %w", d.ID, err)
	}
	return tx.Commit(ctx)
}

// Sweep removes the deliveries that were delivered or failed and were made
// earlier than the time given, and their attempts, up to limit of them; and
// answers with how many. Measured from when a delivery was made rather than
// when it was done with, which is at most three days later and is one
// column fewer to index. Pending ones stay whatever their age: one is
// pending for three days at the most, and a longer wait is a disabled
// endpoint, whose deliveries are kept for it to be enabled again.
func (s *Postgres) Sweep(ctx context.Context, before time.Time, limit int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("sweep: %w", err)
	}
	defer func() { err = unwind(ctx, tx, "sweep", err) }()
	rows, err := tx.Query(ctx, `
		select id from webhook_deliveries
		 where state <> $1 and created_at < $2
		 order by created_at limit $3`, Pending, before, limit)
	if err != nil {
		return 0, fmt.Errorf("sweep: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[ID])
	if err != nil {
		return 0, fmt.Errorf("sweep: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `delete from webhook_attempts where delivery_id = any($1)`, ids); err != nil {
		return 0, fmt.Errorf("sweep: %w", err)
	}
	tag, err := tx.Exec(ctx, `delete from webhook_deliveries where id = any($1)`, ids)
	if err != nil {
		return 0, fmt.Errorf("sweep: %w", err)
	}
	return int(tag.RowsAffected()), tx.Commit(ctx)
}

// Test makes one delivery of endpoint.test to one endpoint of one account,
// due at once, and answers with its id. Not through the outbox: the event
// is about no payment, and the outbox is a payment's. A disabled endpoint
// is refused, as the outbox's events pass it by.
func (s *Postgres) Test(ctx context.Context, account payment.AccountID, id ID, now time.Time) (ID, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	e, err := s.Get(ctx, account, id)
	if err != nil {
		return "", err
	}
	if !e.Enabled {
		return "", ErrDisabled
	}
	body, err := Envelope("endpoint.test", now, account, json.RawMessage("{}"))
	if err != nil {
		return "", err
	}
	delivery, err := NewID()
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `
		insert into webhook_deliveries
			(id, endpoint_id, account_id, payment_id, type, occurred_at, payload,
			 state, attempts, next_at, created_at)
		values ($1, $2, $3, null, 'endpoint.test', $4, $5, $6, 0, $4, $4)`,
		delivery, id, account, now, body, Pending); err != nil {
		return "", fmt.Errorf("endpoint %s: %w", id, err)
	}
	return delivery, nil
}
