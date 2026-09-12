# Roadmap

日本語: [ROADMAP.ja.md](ROADMAP.ja.md)

suco Pay is pre-alpha. This says what it does today and what it does not, so that nobody has to
find out by running it.

## What it does today

- Reads a configuration document, and reports every value it resolved and where each came from.
- Issues credentials for the API, lists them, and takes one out of force.
- Records which assets an account accepts and where each is paid to.
- Opens a payment over the API and reads it back.
- Issues the key a payer signs against a payment, as an EIP-3009 authorisation.
- Reads an EVM chain round after round, matches transfers against the keys it issued, and records
  each with what the rules made of it.
- Answers what it can reach and what each network it watches has come to.

## What it does not do yet

A payment reaches `succeeded`. The confirmation policy is set per network, and a payment past
its deadline waits before it becomes `expired`. That path has been run against a chain inside
the process and not yet against a live one.

- [ ] Payments: lifecycle, expiry, under/overpayment, idempotency
- [x] Finality: confirmation policy per network
- [ ] Checkout: hosted and embeddable
- [ ] Webhooks: signed, retried, with delivery history
- [ ] Refunds: records and transfer intents
- [ ] Reconciliation: periodic diff between internal state and the chain
- [ ] Console: payments, transactions, refunds, webhook deliveries
- [x] CLI: `init` `serve` `doctor` `credential` `asset` `payment` `network`

## What is settled and what is not

The order above is the order it is being built in, and the list is not a schedule: nothing here
is a commitment about a date. An install script and released binaries arrive with the first
release.

Watch the repository, or say what you need in
[Discussions](https://github.com/sucopay/sucopay/discussions).
