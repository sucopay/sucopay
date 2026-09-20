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
	"github.com/sucopay/sucopay/internal/config"
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
	// contract signs under, and the contract is asked once, here. Whether the
	// contract would refuse transfers to the address is asked of the same
	// contract, over the same connection.
	read, err := openNetwork(o, asset)
	if err != nil {
		return err
	}
	caveat, err := checkDomain(ctx, o, read, args[0], asset)
	if err != nil {
		return err
	}
	if err := checkBlocklist(ctx, read, asset, destination); err != nil {
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
	fmt.Fprintf(stdout, "Accepted %s, paying to %s%s\n", asset, destination, caveat)
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
//
// A contract with no separator to answer is compared piece by piece,
// as far as it answers, and what it does not answer is taken from the
// document. What comes back is the tail of a sentence saying so, for the
// line that confirms the registration, and nothing where everything was
// checked.
func checkDomain(ctx context.Context, o opened, read chain.Chain, name string, asset payment.Asset) (string, error) {
	n := o.networks[string(asset.Network())]
	onChain, err := read.DomainSeparator(ctx, asset.Reference())
	if errors.Is(err, chain.ErrNoDomain) {
		return "", nil
	}
	unanswered := errors.Is(err, chain.ErrNoSeparator)
	if err != nil && !unanswered {
		return "", fmt.Errorf("reading what %s signs under: %w", asset, err)
	}
	given, ok := o.domains[name]
	if !ok || !given.IsSet() {
		return "", fmt.Errorf("%s gives no assets.%s.eip712 for %s, and a payer's wallet cannot sign a transfer of it without one", o.document, name, asset)
	}
	if unanswered {
		return checkPieces(ctx, o, read, name, asset, given)
	}
	expected, err := evm.Domain(given.Name, given.Version, n.ChainID, asset.Reference())
	if err != nil {
		return "", err
	}
	if expected != onChain {
		return "", fmt.Errorf("assets.%s.eip712 names %q version %q, which is not what the contract at %s signs under; check them, and the network's chain_id, against the contract",
			name, given.Name, given.Version, asset.Reference())
	}
	return "", nil
}

// checkPieces compares a document's domain with a contract that answers no
// separator, one piece at a time: the chain the provider says it is against
// the network's chain_id, and what the contract calls itself against the
// domain's name, which the tokens read so far give as one string. The
// contract is the address called. What is left is the version, which the
// contract does not answer and the document is taken at its word on.
func checkPieces(ctx context.Context, o opened, read chain.Chain, name string, asset payment.Asset, given config.EIP712) (string, error) {
	n := o.networks[string(asset.Network())]
	want := identity(n)
	id, err := read.Identity(ctx)
	if err != nil {
		return "", fmt.Errorf("asking network %s which chain it is: %w", asset.Network(), err)
	}
	if id != want {
		return "", fmt.Errorf("network %s gives chain_id %s, and the provider says it is chain %s; the contract at %s answers no separator, so the domain is checked against the chain it names",
			asset.Network(), want, invisible.Quote(id), asset.Reference())
	}
	called, err := read.Name(ctx, asset.Reference())
	if err != nil {
		return "", fmt.Errorf("reading what the contract at %s calls itself: %w", asset.Reference(), err)
	}
	if called != given.Name {
		return "", fmt.Errorf("assets.%s.eip712 names %q, and the contract at %s calls itself %s; it answers no separator, so the name is checked on its own",
			name, given.Name, asset.Reference(), invisible.Quote(called))
	}
	return fmt.Sprintf(", taking eip712.version %q from %s: the contract answers no separator to check it against", given.Version, o.document), nil
}

// openNetwork opens the chain an asset settles on, at the endpoint a round
// would read it through.
func openNetwork(o opened, asset payment.Asset) (chain.Chain, error) {
	n := o.networks[string(asset.Network())]
	kind, known := kinds.Lookup(n.Kind)
	if !known {
		return nil, fmt.Errorf("network %s is of kind %s, which this build cannot open", asset.Network(), n.Kind)
	}
	read, err := kind.Open(chain.Settings{Name: string(asset.Network()), ChainID: identity(n), RPC: endpoint(n).Expose()})
	if err != nil {
		return nil, fmt.Errorf("network %s: %w", asset.Network(), err)
	}
	return read, nil
}

// checkBlocklist asks the asset's contract whether it refuses transfers to
// the destination, and refuses a registration it does: every payment to that
// address would be refused by the contract, and the merchant would take
// nothing. What cannot be read refuses the registration too. The answer is
// the contract's to give, and an address is not registered on the strength
// of a provider that would not carry it, for the reason the domain is not
// taken on trust.
//
// The address goes to the provider in the asking, which is the first time one
// does. It is public the moment a payment reaches it.
func checkBlocklist(ctx context.Context, read chain.Chain, asset payment.Asset, destination payment.Address) error {
	listed, err := read.Blocklisted(ctx, asset.Reference(), string(destination))
	if err != nil {
		return fmt.Errorf("asking whether %s refuses transfers to %s: %w", asset, destination, err)
	}
	if listed {
		return fmt.Errorf("the contract of %s refuses transfers to %s, the provider says, so nothing paid there would arrive; give another address, or another provider if this one is wrong", asset, destination)
	}
	return nil
}
