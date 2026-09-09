<h1 align="center">suco Pay</h1>

<p align="center">
  <b>Open-source payment infrastructure for stablecoins</b>
</p>

<p align="center">
  <a href="https://github.com/sucopay/sucopay/discussions">Discussions</a> ·
  <a href="ROADMAP.md">Roadmap</a> ·
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

> **Pre-alpha.** Only the CLI exists so far. [ROADMAP.md](ROADMAP.md) says what works today
> and what does not.

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

To open payments, list in `suco.yaml` the network they arrive on and the asset they are in:

```yaml
networks:
  local:
    kind: simulated
assets:
  jpyc:
    network: local
    reference: "0x0000000000000000000000000000000000000001"
    symbol: JPYC
    decimals: 18
```

`simulated` is the one kind of network accepted so far. Nothing observes a chain yet, so no payment
reaches `succeeded`. `suco asset accept <name> <address>` records the address a payment in the asset
`suco.yaml` lists under that name is paid to, and `suco asset list` shows every asset the document
lists, each with its address or `not accepted`. `suco payment await <id>` makes one payment payable
and prints the seven values a payer signs to pay it, which is what Checkout will do once there is a
Checkout; what it prints stays on the terminal that asked for it. A merchant's server opens payments
and reads them back over the API in [docs/api.md](docs/api.md).

An install script and released binaries arrive with the first release. Everything in the list
above is still to come.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues go through
[SECURITY.md](SECURITY.md) instead.

## License

[Apache-2.0](LICENSE)
