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

suco Pay is software you run on your own infrastructure. It holds no keys and charges no fee.
A payment goes from the customer's wallet to yours with nothing in between; what suco Pay does is
watch the chain and tell you it arrived.

> **Pre-alpha.** A transfer is seen, matched, recorded, and decided, and a payment reaches
> `succeeded`. That path has been run end to end against a chain inside the process, and one
> step at a time against Polygon on a payment somebody made. [ROADMAP.md](ROADMAP.md) says
> what works today and what does not.

## Getting started

Requires Go 1.26+ and a PostgreSQL to point it at.

```bash
git clone https://github.com/sucopay/sucopay && cd sucopay
go build -o suco ./cmd/suco
./suco init
export SUCO_CREDENTIALS_KEY="$(cat -- 'credentials-<key_id>.key')"   # init prints this line
./suco doctor
./suco serve
```

- `suco init` writes a `suco.yaml` you can read and commit, and a key file only its owner can
  read. Credentials are stored under that key, and the document names the environment variable it
  is read from rather than holding it.
- `suco doctor` prints every setting it resolved, where each value came from, and what the
  instance reaches. Of a secret it says only whether it is set.
- `suco serve` listens on `http://localhost:7826` and writes what it is doing to stdout.
  `/healthz` says the process is up; `/readyz` says whether it can serve.

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
```

Then, with a database configured:

```bash
./suco credential new --read-write          # writes a token file for the API
./suco asset accept jpyc 0xYourWalletHere   # where a payment in jpyc is paid to
./suco payment await <id>                   # prints what a payer signs, until Checkout exists
```

Your server opens payments and reads them back over the API. `suco serve` reads the chain round
after round and records the transfers that answer them.

## Documentation

| | |
|---|---|
| [docs/api.md](docs/api.md) | The HTTP API a merchant's server calls |
| [docs/configuration.md](docs/configuration.md) | Every setting `suco.yaml` takes |
| [docs/operating.md](docs/operating.md) | `/readyz`, `suco doctor`, and putting a stopped instance right |
| [ROADMAP.md](ROADMAP.md) | What works today and what does not |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues go through
[SECURITY.md](SECURITY.md) instead.

## License

[Apache-2.0](LICENSE)
