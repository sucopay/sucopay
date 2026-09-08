package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/sucopay/sucopay/internal/observe"
	"github.com/sucopay/sucopay/internal/payment"
)

// paymentAwait makes a payment payable and prints what a payer signs to pay
// it: the seven values of an EIP-3009 authorisation, one to a line.
//
// The nonce is the attempt's key, which the observer matches a transfer to.
// Whoever runs this holds it until the payer has signed, so the output goes to
// a person at a terminal and nowhere that keeps it: anybody who reads it can
// sign for the payer if they also hold the payer's key, and can tell what is
// being paid where in any case.
func paymentAwait(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("payment await takes the identifier of a payment, got %d arguments", len(args))
	}
	id, err := payment.ParseID(args[0])
	if err != nil {
		return err
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
	store := payment.NewPostgres(o.db.Conns())
	service := payment.NewService(store, store, observe.NewCursors(o.db.Conns()), time.Now)
	// A payment is made payable once, and this command is run again whenever
	// the first run stopped before the key was issued: on a network nothing
	// reads yet, on a deployment whose position was not set. Making a payment
	// payable that already is would be refused, and would leave the operator
	// with a payment nothing here could ever issue against.
	held, err := service.Find(ctx, payment.AccountID(account), id)
	if err != nil {
		return err
	}
	if held.Status() == payment.Created {
		if _, err := service.Await(ctx, payment.AccountID(account), id); err != nil {
			return err
		}
	}
	a, p, err := service.Issue(ctx, payment.AccountID(account), id)
	if err != nil {
		return err
	}

	// The chain identifies itself with a number an EIP-3009 signature is bound
	// to. A network running inside the server has none, and prints 0.
	var chain uint64
	if n, ok := o.networks[string(p.Network())]; ok {
		chain = n.ChainID
	}
	for _, line := range [][2]string{
		{"contract", p.Asset().Reference()},
		{"chainId", fmt.Sprint(chain)},
		{"to", p.Destination().String()},
		{"value", p.Amount().Amount().String()},
		{"validAfter", "0"},
		{"validBefore", fmt.Sprint(a.ValidBefore().Unix())},
		{"nonce", "0x" + a.Key()},
	} {
		fmt.Fprintf(stdout, "%s %s\n", line[0], line[1])
	}
	return nil
}
