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

Create a payment, get paid on chain, know when it settled, refund it, and get a webhook.

suco Pay is software you run on your own infrastructure. It holds no keys and charges no fee.
A payment goes from the customer's wallet to yours with nothing in between. suco Pay watches the
chain and tells you it arrived.

> **Pre-alpha.** What works today is under *Where things stand*, below.

## Getting started

You need Go 1.26 or later and a PostgreSQL to connect to.

```bash
git clone https://github.com/sucopay/sucopay && cd sucopay
go build -o suco ./cmd/suco
./suco init
```

`suco init` writes a `suco.yaml` you can read and commit, and a key file only its owner can
read. Credentials are stored under that key. `suco.yaml` names the environment variable the key
is read from and does not hold the key itself.

Add the database to `suco.yaml`:

```yaml
database:
  managed: false
  url: ${SUCO_DATABASE_URL}
```

Then check the settings and start:

```bash
export SUCO_DATABASE_URL="postgres://suco:secret@localhost:5432/suco"
export SUCO_CREDENTIALS_KEY="$(cat -- 'credentials-<key_id>.key')"   # init prints this line
./suco doctor
./suco serve
```

- `suco doctor` prints every setting it resolved, where each value came from, and what the
  instance reaches. For a secret it prints only whether it is set.
- `suco serve` listens on `http://localhost:7826` and writes what it is doing to stdout.
  `/healthz` says the process is up. `/readyz` says whether it can serve.

## Taking a payment

List the network a payment arrives on and the asset it is in:

```yaml
networks:
  polygon:
    kind: evm
    chain_id: 137
    rpc:
      own: ${SUCO_POLYGON_RPC_URL}
assets:
  jpyc:
    network: polygon
    reference: "0xE7C3D8C9a439feDe00D2600032D5dB0Be71C3c29"
    symbol: JPYC
    decimals: 18
    eip712:
      name: JPY Coin
      version: "1"
```

`suco serve` reads `suco.yaml` once, at start, so restart it after this change. Then register a
credential for the API and the wallet payments are paid to, and try a payment:

```bash
./suco credential new --read-write          # writes a token file for the API
./suco asset accept jpyc 0xYourWalletHere   # where a payment in jpyc is paid to
./suco payment await <id>                   # prints what a payer signs, until suco Checkout takes payments
```

Give `asset accept` a wallet that can sign EIP-712 typed data. Refunds are signed by the wallet
the payment was paid to, so an address nobody can sign for can receive payments and cannot
refund them. Registration asks the asset's contract whether it refuses transfers to the address,
and refuses an address the contract refuses. [docs/operating.md](docs/operating.md) has the rest.

Your server opens payments and reads them back over the API. `suco serve` reads the chain round
after round and records the transfers that pay them.

## Where things stand

A transfer is seen, matched, recorded and settled, and a payment reaches `succeeded`. That
path has run end to end against a chain inside the process, and step by step against Polygon
on one real payment. [ROADMAP.md](ROADMAP.md) says what works today and what does not.

## Documentation

| | |
|---|---|
| [Developer documentation](docs/README.md) | Start here for the recommended integration path |
| [Configuration](docs/configuration.md) | Every setting in `suco.yaml` |
| [API](docs/api.md) | Create and retrieve payments |
| [Checkout](docs/checkout.md) | Send a payer to the payment page |
| [Webhooks](docs/webhooks.md) | Receive payment and refund events |
| [Refunds](docs/refunds.md) | Create, sign and track a refund |
| [Operations](docs/operating.md) | Monitor readiness and recover an instance |
| [ROADMAP.md](ROADMAP.md) | What works today and what does not |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues go through
[SECURITY.md](SECURITY.md) instead.

## License

[Apache-2.0](LICENSE)
