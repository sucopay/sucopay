package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/sucopay/sucopay/internal/accepted"
	"github.com/sucopay/sucopay/internal/adapter/chain"
	"github.com/sucopay/sucopay/internal/adapter/chain/evm"
	"github.com/sucopay/sucopay/internal/adapter/chain/kinds"
	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/payment"
)

// assetAccept records that the account takes one asset of the document, paid
// to an address on the asset's network, and says so with the address as it
// was stored. Accepting an asset again replaces the address.
//
// The address is read first, as [payment.ParseAddress] reads a request's
// destination, so that one that is refused needs no connection to be opened.
// The name is looked up once the document is read; a name the document does
// not list is not repeated, for the reason at [errUnknown].
func assetAccept(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 2 {
		return fmt.Errorf("asset accept takes the name of an asset and the address it is paid to, got %d arguments", len(args))
	}
	destination, err := payment.ParseAddress(args[1])
	if err != nil {
		return err
	}
	o, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer o.db.Close()

	asset, ok := o.assets.Asset(args[0])
	if !ok {
		return fmt.Errorf("%s lists no asset under that name. Run `suco asset list` for the names", o.document)
	}
	// What a transfer says it went to is compared with this as text, so the
	// address is stored the way the asset's chain writes one. A block explorer
	// hands an operator the mixed-case form to copy, and that form carries the
	// checksum that catches a digit typed wrong.
	written, err := normalize(o.networks[string(asset.Network())], string(destination))
	if err != nil {
		return err
	}
	destination = payment.Address(written)
	// What a payer's wallet will be asked to sign under has to be what the
	// contract signs under, and the contract is asked once, here.
	if err := checkDomain(ctx, o, args[0], asset); err != nil {
		return err
	}
	account, err := oneAccount(ctx, o.credentials)
	if err != nil {
		return err
	}
	store := accepted.NewPostgres(o.db.Conns())
	if err := store.Accept(ctx, payment.AccountID(account), asset, destination, time.Now()); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Accepted %s, paying to %s\n", asset, destination)
	return nil
}

// assetList shows every asset the document lists, by name, with the address
// a payment of it is paid to, or "not accepted" where the account does not
// accept it yet. An asset the database holds and the document no longer lists
// is not shown: nothing can be paid in it.
func assetList(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("asset list takes no arguments, got %d", len(args))
	}
	o, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer o.db.Close()

	account, err := oneAccount(ctx, o.credentials)
	if err != nil {
		return err
	}
	assets, err := accepted.NewPostgres(o.db.Conns()).List(ctx, payment.AccountID(account))
	if err != nil {
		return err
	}
	// Keyed by what identifies an asset, which is never its name: the document
	// may have renamed one since it was accepted.
	type identity struct {
		network   payment.Network
		reference string
	}
	destinations := map[identity]payment.Address{}
	for _, a := range assets {
		destinations[identity{a.Network, a.Reference}] = a.Destination
	}
	// Laid out in full before any of it is written, as credential list is.
	var table bytes.Buffer
	tw := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tASSET\tPAID TO")
	for _, name := range slices.Sorted(maps.Keys(o.assets)) {
		asset := o.assets[name]
		paidTo := "not accepted"
		if destination, ok := destinations[identity{asset.Network(), asset.Reference()}]; ok {
			paidTo = string(destination)
		}
		// The name is a key of the document, quoted as doctor quotes one.
		fmt.Fprintf(tw, "%s\t%s\t%s\n", invisible.Quote(name), asset, paidTo)
	}
	tw.Flush()
	if _, err := stdout.Write(table.Bytes()); err != nil {
		return fmt.Errorf("writing the list: %w", err)
	}
	return nil
}

// checkDomain compares the EIP-712 domain the document gives for an asset
// with what the asset's contract signs under, read from the chain, and
// refuses a registration whose two disagree: a payer's wallet would sign
// under the document's, and the contract would refuse the signature. A
// chain whose assets sign under no domain has nothing to compare, and a
// document that gives none for an asset whose contract has one is refused
// too.
func checkDomain(ctx context.Context, o opened, name string, asset payment.Asset) error {
	n := o.networks[string(asset.Network())]
	kind, known := kinds.Lookup(n.Kind)
	if !known {
		return fmt.Errorf("network %s is of kind %s, which this build cannot open", asset.Network(), n.Kind)
	}
	read, err := kind.Open(chain.Settings{Name: string(asset.Network()), ChainID: identity(n), RPC: endpoint(n).Expose()})
	if err != nil {
		return fmt.Errorf("network %s: %w", asset.Network(), err)
	}
	onChain, err := read.DomainSeparator(ctx, asset.Reference())
	if errors.Is(err, chain.ErrNoDomain) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading what %s signs under: %w", asset, err)
	}
	given, ok := o.domains[name]
	if !ok || !given.IsSet() {
		return fmt.Errorf("%s gives no assets.%s.eip712 for %s, and a payer's wallet cannot sign a transfer of it without one", o.document, name, asset)
	}
	expected, err := evm.Domain(given.Name, given.Version, n.ChainID, asset.Reference())
	if err != nil {
		return err
	}
	if expected != onChain {
		return fmt.Errorf("assets.%s.eip712 names %q version %q, which is not what the contract at %s signs under; check them, and the network's chain_id, against the contract",
			name, given.Name, given.Version, asset.Reference())
	}
	return nil
}
