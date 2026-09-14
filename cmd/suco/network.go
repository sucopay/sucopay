package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/sucopay/sucopay/internal/config"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/observe"
	"github.com/sucopay/sucopay/internal/payment"
)

// networkCursor puts a network's cursor where an operator says.
//
// A cursor is what moves and a position is where it is: the row holds a height
// and the hash of the block at it, and this is what moves the row.
//
// It is how a deployment that has stopped is started again. A chain that no
// longer holds the block the cursor sits on is one no round will read past,
// and how far back to go is not something a round can work out: whoever put
// the cursor there is who puts it somewhere else.
//
// The block at the height is read from the chain and its hash written with it.
// A height alone does not say which chain it was on, and a reader carrying on
// from a height whose block has been replaced would be reading a chain the
// records it holds did not come off.
//
// No lease is taken. A round advances on a condition of the position it read,
// so one under way when this writes does not commit its advance, and the round
// after it reads from where this put it.
func networkCursor(ctx context.Context, args []string, stdout io.Writer) error {
	force := false
	switch {
	case len(args) == 3 && args[2] == "--force":
		force = true
	case len(args) == 3:
		// Counted, and the one word this takes is named: no error repeats a
		// word typed after suco, for the reason at errUnknown.
		return errors.New("network cursor takes the name of a network, a height, and at most --force")
	case len(args) != 2:
		return fmt.Errorf("network cursor takes the name of a network and a height, got %d arguments", len(args))
	}
	height, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil {
		return errors.New("network cursor takes a height, which is a whole number of blocks")
	}
	o, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer o.db.Close()

	// The two an adapter is opened from. The rest of what the document holds
	// is the credential key, which nothing here has a use for.
	networks, err := openChains(config.Config{Networks: o.networks, Assets: o.assets})
	if err != nil {
		return err
	}
	read, found := observe.Network{}, false
	for _, n := range networks {
		if n.Name == args[0] {
			read, found = n, true
		}
	}
	if !found {
		return fmt.Errorf("%s declares no network under that name that an asset settles on. "+
			"Run `suco doctor` for the ones it does", o.document)
	}
	// Forward skips blocks nobody reads, and a payment still open on the
	// network may have been paid in one of them: once the cursor is past its
	// deadline it expires as unpaid. So forward over an open payment is
	// refused unless the operator, having counted, forces it. Back skips
	// nothing, and is never refused. A network with no cursor yet has had
	// nothing read, so putting one anywhere skips every block below it. The
	// check comes before the chain is asked, so that the answer is about the
	// payments and not about the block.
	network := payment.Network(read.Name)
	cursors := observe.NewCursors(o.db.Conns())
	current, had, err := cursors.Get(ctx, network)
	if err != nil {
		return err
	}
	skipped := 0
	if !had || height > current.Height {
		skipped, err = payment.NewPostgres(o.db.Conns()).Open(ctx, network)
		if err != nil {
			return err
		}
		if skipped > 0 && !force {
			return fmt.Errorf("%s has %d payment(s) still open that may have been paid in the blocks "+
				"this would skip; they expire as unpaid once the cursor is past their deadline. "+
				"Add --force to skip them anyway", read.Name, skipped)
		}
	}

	// The height came through ParseUint, so what the chain says about one it
	// has not got carries a number and nothing that could be a token.
	block, err := read.Chain.Block(ctx, height)
	if err != nil {
		return err
	}

	log, err := newLogger(o.log, stdout)
	if err != nil {
		return err
	}
	before, had, err := cursors.Set(ctx, network,
		observe.Position{Height: block.Height, Hash: block.Hash, Time: block.Time}, time.Now())
	if err != nil {
		return err
	}

	// Where the cursor was and where it is now, on one line. Where it was is
	// what puts it back, for an operator who has just found out they moved it
	// to the wrong place.
	moved := []any{
		slog.String("network", read.Name),
		slog.Uint64("height", block.Height),
		// A hash is longer than a version: an EVM chain writes 66 characters,
		// and one cut short is one nobody can put the cursor back from.
		slog.String("hash", invisible.Shown(block.Hash, payment.MaxTransferField)),
	}
	if had {
		moved = append(moved,
			slog.Uint64("from_height", before.Height),
			slog.String("from_hash", invisible.Shown(before.Hash, payment.MaxTransferField)))
	}
	if skipped > 0 {
		moved = append(moved, slog.Int("payments_skipped", skipped))
	}
	log.InfoContext(ctx, "the cursor was put where it was asked for", moved...)
	return nil
}
