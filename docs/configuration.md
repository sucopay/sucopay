# Configuration

日本語: [configuration.ja.md](configuration.ja.md)

This page is every setting `suco.yaml` takes, with what each one does and what it defaults to.

It is written for the operator running an instance. It assumes `suco init` has written a
document, and that you can run `suco doctor` against it.

## Terms

| Term | Meaning |
|---|---|
| instance | One `suco serve` process |
| configuration file (`suco.yaml`) | The file an instance's settings are written in. `suco init` writes one |
| secret | A setting whose value never prints. `suco doctor` says only whether it is set |
| credential | The token sent in `Authorization` on an API call. `suco credential new` writes one |
| network | One chain the instance reads. Its name is a key of `suco.yaml` |
| asset | One token on one network. Its name is a key of `suco.yaml` |
| round | One read of a chain |
| RPC endpoint | The URL an instance calls to read a chain |
| provider | The third party running an RPC endpoint |
| transfer | A movement of an asset on the chain, as suco reads it |
| settled | The chain will no longer take a transfer back |
| cursor | How far a round has read a chain |
| reference | What a chain identifies a token by. On an EVM chain, a contract address |

## The configuration file

`suco` reads one document. `suco init` writes it, and every command reads it from `suco.yaml`,
or from the file `SUCO_CONFIG` names.

`suco doctor` prints every value it resolved and where each came from, which is how to check a
document without starting a server.

A setting `suco.yaml` leaves out takes its default. A key `suco.yaml` holds that nothing
reads is refused.

## Secrets

A value may be written as `${NAME}`, which reads the environment variable of that name. The
reference is the whole value or none of it, so `https://${HOST}/rpc` is refused.

These settings never print. `suco doctor` says of each only whether it is set, and no error or
log line carries one.

- `database.url`
- `credentials.key`
- `networks.*.rpc.own`
- `networks.*.rpc.others`

Every other value prints in full. A URL carrying a username or password at a setting that is
not secret is refused, and so is a value of 64 hexadecimal characters, which is the shape of a
key. Neither refusal prints any part of the value.

## listen

| Setting | Default | Description |
|---|---|---|
| `listen.host` | `127.0.0.1` | The interface to bind. The default leaves the instance closed to other machines |
| `listen.port` | `7826` | 1 to 65535 |
| `listen.base_url` | `http://localhost:<port>` | How others reach the instance, which differs from `host` behind a proxy. An `http` or `https` URL naming a host |

## log

| Setting | Default | Description |
|---|---|---|
| `log.level` | `info` | `debug`, `info`, `warn` or `error` |
| `log.format` | `text` | `text` to read in a terminal, `json` for whatever collects it |

## database

| Setting | Default | Description |
|---|---|---|
| `database.managed` | `true` | A database suco runs itself. Not implemented |
| `database.url` | none | A PostgreSQL connection string. Secret |

The two are mutually exclusive. Write `managed: false` and give a URL:

```yaml
database:
  managed: false
  url: ${SUCO_DATABASE_URL}
```

A document that writes `managed: true` is refused by every command that reads it, and so is
`managed: false` with no `url`. A document that says nothing about a database leaves `managed`
at its default: `suco serve` answers `/healthz` and `/readyz`, reads no chain and keeps
nothing, and every command that needs a database refuses.

A URL reaching another machine has to ask for a connection that checks who answered:
`sslmode=verify-full`, or `verify-ca` where the name cannot match. Anything less offers TLS and
then connects in the clear if the server declines, and whoever can see the traffic reads and
rewrites payment state. A loopback address and a unix socket reach no other machine, and are
left alone.

## credentials

| Setting | Default | Description |
|---|---|---|
| `credentials.key` | none | 64 hexadecimal characters, being the 32 bytes a stored credential is hashed under. Secret |
| `credentials.key_id` | none | Which key a stored credential was made under. Not a secret |

Both are required once `database.url` is set. `suco init` writes a key to a file and prints the
line that sets the variable from it.

Swapping the key revokes nothing. A credential made under the old one is still in force and can
no longer be presented. `suco credential list` shows which key each was made under.

## networks

A network is one chain the instance reads. Its name is a key of `suco.yaml`, and is what an
asset refers to.

| Setting | Default | Description |
|---|---|---|
| `networks.<name>.kind` | `evm` | `evm` or `simulated` |
| `networks.<name>.chain_id` | none | What `eth_chainId` has to answer. 1 or more. Required by `evm`, refused by `simulated` |
| `networks.<name>.rpc.own` | none | A node you run yourself. A round reads it when it is set. Secret |
| `networks.<name>.rpc.others` | none | A list of third-party RPC endpoints. With no `own`, a round reads the first and the settling asks them in order. Secret |
| `networks.<name>.poll` | `12s` | How long between rounds. At least `1s` |
| `networks.<name>.width` | `1000` | The most blocks one request for logs asks about. 10 to 10000 |
| `networks.<name>.finality.recheck` | `1m` | How long between asking the RPC endpoints again about what was recorded. At least `1s` |
| `networks.<name>.finality.misses` | `10` | How many times in a row the RPC endpoints have to find nothing before a transfer is treated as gone. At least `2` |

`simulated` is a chain inside the process, for trying the rest of a deployment out. It makes
one block and nothing arrives on it.

A network no asset refers to is refused.

### rpc

An RPC endpoint is `https` anywhere, or `http` to `localhost` or a loopback address. A key on
the URL travels in the clear over `http`, and so does the receipt that comes back. The host is
taken as written, so a name that resolves to a loopback address is still refused.

A network with an `own` node is asked only there. A network without one asks the `others` in
order, and counts a transfer as having paid once two of them agree. One that does not answer is
skipped, so a longer list is more to fall back on rather than more answers that have to agree.
Fewer than two answering leaves the network `too-few`, which [operating.md](operating.md)
describes, until another answers. One answer that differs leaves the payment where it is, and
no third RPC endpoint is asked to break the tie. The transfer stays recorded either way, and
the funds are at the merchant's address.

### poll and width

`poll` decides what a deployment takes from an RPC endpoint it does not run. A round is
several calls, so 12 seconds comes to about 40,000 a day per network. A deployment reading a
node of its own can set it down, and 3 seconds is four times that.

`width` is capped by the provider, each at its own value. A provider that refuses a span makes
the next round ask for half as much, down to 10 blocks, and 100 rounds later it doubles back
up.

### finality

The two under `finality` decide when a transfer on the chain counts as having paid.

`recheck` is how long between rounds, and its default is longer than `poll`'s. A round asks the
RPC endpoints two questions about every transfer it is deciding about.

`misses` is a count, not a length of time. A transfer the RPC endpoints are asked about and do
not find, that many times in a row, is treated as gone. It cannot be 1: not finding a transfer
once may be that RPC endpoint reading a state it has not finished replacing.

### cursor

Reading blocks and deciding what settled are two different things. A round reads one RPC
endpoint, and a cursor is a place in a chain as one provider tells it.

There is no setting for how long a payment past its deadline waits. It waits until the network
has been read past the deadline, which is a fact about the chain rather than the clock: once
the position sits on a block stamped at or after the deadline, every transfer that could have
paid the payment has been read. A deployment that has stopped reading expires nothing, however
long it has been stopped.

## assets

An asset is one token on one network. Its name is a key of `suco.yaml`, and is what a request
and the CLI say.

| Setting | Default | Description |
|---|---|---|
| `assets.<name>.network` | none | The name of a network `suco.yaml` declares |
| `assets.<name>.reference` | none | What that chain identifies the token by. On `evm`, an address |
| `assets.<name>.symbol` | none | What to show a person. It does not identify the asset |
| `assets.<name>.decimals` | none | How many places divide one unit into the smallest unit. 0 to 36 |
| `assets.<name>.eip712.name` | none | The name the token's contract signs under, which a payer's wallet needs to sign a transfer of it. JPYC's is `JPY Coin`. Given with `version` or not at all |
| `assets.<name>.eip712.version` | none | The version beside it. JPYC's is `1` |

The first four are required, and `eip712` is what a deployment that runs suco Checkout
gives. Two names for one token are refused.

A reference is read into the form its chain compares. An EVM chain writes an account in either
case, so the mixed-case form a block explorer shows is read as the lower-case one a transfer
carries. That form also carries a checksum, and a reference whose checksum does not hold is
refused. It is the last moment a mistyped one can be caught, since a chain does not give funds
back.

## A testnet

The step before production is a real wallet on Polygon Amoy, with JPYC from
[JPYC's faucet](https://faucet.jpyc.co.jp/). It is a configuration, not something suco starts:

```yaml
networks:
  polygon-amoy:
    kind: evm
    chain_id: 80002
    rpc:
      own: ${SUCO_POLYGON_AMOY_RPC_URL}
assets:
  jpyc:
    network: polygon-amoy
    reference: "0xE7C3D8C9a439feDe00D2600032D5dB0Be71C3c29"
    symbol: JPYC
    decimals: 18
    eip712:
      name: JPY Coin
      version: "1"
```

The network is named `polygon-amoy` and never `polygon` with other values. JPYC sits at the
same address on Amoy as on Polygon, so a testnet document written under the production name
would be consistent with itself and wrong. `doctor` names a testnet as one on the network's
line, whatever `suco.yaml` calls it.

## Names and bounds

A network name and an asset name become part of the dotted paths in a report and the keys a
probe answers with. A name holding a dot is refused, and so is one holding a bracket, which a
path uses to name a place in a list. So is one holding a character a reader cannot see: two
names that render alike would be two names.

A document is at most 256 kilobytes. Anchors and aliases are refused: expanding one copies what
it names, and nothing bounds how often.

## An example

```yaml
listen:
  port: 7826
  base_url: https://pay.example.com

log:
  level: info
  format: json

database:
  managed: false
  url: ${SUCO_DATABASE_URL}

credentials:
  key: ${SUCO_CREDENTIALS_KEY}
  key_id: "0123456789abcdef"

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

## Related

- [operating.md](operating.md): `/readyz`, `suco doctor`, and the commands that put an instance
  right
- [webhooks.md](webhooks.md): what swapping `credentials.key` leaves a merchant to do
