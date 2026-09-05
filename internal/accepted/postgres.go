package accepted

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sucopay/sucopay/internal/payment"
)

// storeTimeout bounds one call, counting the wait for a connection. Each
// method is one statement over the primary key, so a call without its
// answer by now is waiting on the database's availability, not on its work.
const storeTimeout = 3 * time.Second

// Postgres stores in PostgreSQL what each account accepts.
//
// No transactions. Each method is one statement, and nothing has to be
// written along with a row here: payment.Postgres owns transactions for the
// rows that must be written with a payment or not at all.
type Postgres struct {
	pool *pgxpool.Pool
}

// NewPostgres stores accepted assets in the database behind pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Asset is one asset an account accepts, and where a payment of it is paid
// to. The asset is named the way payments name one, by its network and
// reference, and not by the name a document lists it under.
//
// Its own type rather than a [payment.Asset]: that one carries a symbol and
// decimals this package does not store, and has no place for a destination.
type Asset struct {
	Network     payment.Network
	Reference   string
	Destination payment.Address
	UpdatedAt   time.Time
}

// Accept records that account takes asset, paid to destination, as of now.
// Accepting an asset again replaces the destination and moves UpdatedAt,
// and keeps when the asset was first accepted.
//
// The zero Asset and the empty Address are refused. Neither comes out of
// [payment.NewAsset] or [payment.ParseAddress], and a row of empty strings
// would read back as an asset the account accepts.
func (s *Postgres) Accept(ctx context.Context, account payment.AccountID, asset payment.Asset,
	destination payment.Address, now time.Time) error {
	switch {
	case !asset.IsSet():
		return errors.New("accepted asset: none given")
	case destination == "":
		return errors.New("accepted asset: no destination")
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	_, err := s.pool.Exec(ctx, `
		insert into accepted_assets (
			account_id, asset_network, asset_reference, destination, created_at, updated_at
		) values ($1, $2, $3, $4, $5, $5)
		on conflict (account_id, asset_network, asset_reference) do update
		set destination = excluded.destination, updated_at = excluded.updated_at`,
		account, asset.Network(), asset.Reference(), destination, now)
	if err != nil {
		return fmt.Errorf("accepted asset %s on %s: %w", asset.Reference(), asset.Network(), err)
	}
	return nil
}

// Destination reads where a payment of asset to account is paid, and whether
// account accepts asset at all. Not an error when it does not: whoever asked
// has its own words for that, and an error here would have to be told apart
// from the database failing.
func (s *Postgres) Destination(ctx context.Context, account payment.AccountID, asset payment.Asset) (payment.Address, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	var destination payment.Address
	err := s.pool.QueryRow(ctx, `
		select destination from accepted_assets
		where account_id = $1 and asset_network = $2 and asset_reference = $3`,
		account, asset.Network(), asset.Reference()).Scan(&destination)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("accepted asset %s on %s: %w", asset.Reference(), asset.Network(), err)
	}
	return destination, true, nil
}

// List reads every asset account accepts, by network and then by reference,
// so that two reads of one account list them in one order.
func (s *Postgres) List(ctx context.Context, account payment.AccountID) ([]Asset, error) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		select asset_network, asset_reference, destination, updated_at from accepted_assets
		where account_id = $1 order by asset_network, asset_reference`, account)
	if err != nil {
		return nil, fmt.Errorf("accepted assets: %w", err)
	}
	assets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Asset, error) {
		var a Asset
		err := row.Scan(&a.Network, &a.Reference, &a.Destination, &a.UpdatedAt)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("accepted assets: %w", err)
	}
	return assets, nil
}
