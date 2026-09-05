<h1 align="center">suco Pay</h1>

<p align="center">
  <b>Open-source payment infrastructure for stablecoins</b>
</p>

<p align="center">
  <a href="https://github.com/sucopay/sucopay/discussions">Discussions</a> ·
  <a href="README.ja.md">日本語</a>
</p>

<p align="center">
  <a href="https://github.com/sucopay/sucopay/actions/workflows/ci.yml"><img src="https://github.com/sucopay/sucopay/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/status-pre--alpha-orange" alt="Status: pre-alpha">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License: Apache-2.0"></a>
</p>

---

Create a payment, get paid on chain, know when it's final, refund it, and get a webhook.
suco Pay runs on your own infrastructure. Funds go straight from the customer's wallet to yours.

> **Pre-alpha.** Only the CLI exists so far. Watch the repository or join
> [Discussions](https://github.com/sucopay/sucopay/discussions).

- [ ] Payments: lifecycle, expiry, under/overpayment, idempotency
- [ ] Finality: confirmation policy per network
- [ ] Checkout: hosted and embeddable
- [ ] Webhooks: signed, retried, with delivery history
- [ ] Refunds: records and transfer intents
- [ ] Reconciliation: periodic diff between internal state and the chain
- [ ] Console: payments, transactions, refunds, webhook deliveries
- [x] CLI: `init` `serve` `doctor` `credential` `asset`

## Getting started

```bash
git clone https://github.com/sucopay/sucopay && cd sucopay
go build -o suco ./cmd/suco
./suco init   # prints the export line below, with the name of the key file it wrote
export SUCO_CREDENTIALS_KEY="$(cat -- 'credentials-<key_id>.key')"
./suco doctor
./suco serve
```

`suco init` writes a `suco.yaml` you can read and commit, and a key file only its owner can read.
Credentials are stored under the key; `suco.yaml` names the environment variable the key is read
from and holds no key, so set the variable in the shell that runs `suco`. `suco doctor` prints
the settings it resolved and where each value came from, saying of a secret only whether it is
set. `suco serve`
listens on `http://localhost:7826`, where `/healthz` says the process is up and `/readyz` says
whether it can reach what it needs and whether any credential in force can write. It writes what
it is doing to stdout. With a database
configured, `suco credential new --read-only` or `--read-write` makes a credential for the API and
writes its token to a file only its owner can read. `suco credential list` shows the credentials
in force, the most recently used first, and `suco credential revoke <id>` takes one out of force.
`suco asset accept <name> <address>` records the address a payment in the asset `suco.yaml` lists
under that name is paid to, and `suco asset list` shows every asset the document lists, each with
its address or `not accepted`.

An install script and released binaries arrive with the first release. Everything in the list
above is still to come.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues go through
[SECURITY.md](SECURITY.md) instead.

## License

[Apache-2.0](LICENSE)
