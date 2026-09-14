# Configuration

日本語: [configuration.ja.md](configuration.ja.md)

`suco` reads one document. `suco init` writes it, and every command reads it from `suco.yaml`, or
from the file `SUCO_CONFIG` names. `suco doctor` prints every value it resolved and where each
came from, which is how to check a document without starting a server.

A setting the document leaves out takes its default. A key the document holds that nothing reads
is refused, so a misspelt one stops the instance rather than leaving its default in place.

## Secrets

A value may be written as `${NAME}`, which reads the environment variable of that name. The
reference is the whole value or none of it: `https://${HOST}/rpc` is refused, because a value with
two origins has none.

These settings never print. `suco doctor` says of each only whether it is set, and no error or log
line carries one.

- `database.url`
- `credentials.key`
- `networks.*.rpc.own`
- `networks.*.rpc.others`

Every other value prints in full, which is what makes refusing a credential inside one safe: a URL
carrying a username or password at any other setting is refused, and so is a value of 64
hexadecimal characters, which is the shape of a key, and no part of either is printed.

## listen

| Key | Default | |
|---|---|---|
| `listen.host` | `127.0.0.1` | The interface to bind. Reaching the instance from another machine is a deployment decision, and the default leaves it closed |
| `listen.port` | `7826` | 1 to 65535 |
| `listen.base_url` | `http://localhost:<port>` | How others reach the instance, which differs from `host` behind a proxy. An `http` or `https` URL naming a host |

## log

| Key | Default | |
|---|---|---|
| `log.level` | `info` | `debug`, `info`, `warn` or `error` |
| `log.format` | `text` | `text` to read in a terminal, `json` for whatever collects it |

## database

| Key | Default | |
|---|---|---|
| `database.managed` | `true` | A database suco runs itself. Not implemented: set it to `false` and give a URL |
| `database.url` | none | A PostgreSQL connection string. Secret |

The two are mutually exclusive. A URL reaching another machine has to ask for a connection that
checks who answered: `sslmode=verify-full`, or `verify-ca` where the name cannot match. Anything
less offers TLS and then connects in the clear if the server declines, and whoever is on the wire
can read and rewrite payment state. A loopback address or a unix socket never reaches a wire and
is left alone.

## credentials

| Key | Default | |
|---|---|---|
| `credentials.key` | none | 64 hexadecimal characters, being the 32 bytes a stored credential is hashed under. Secret |
| `credentials.key_id` | none | Which key a stored credential was made under. Not a secret |

Both are required once `database.url` is set, which is when a credential can be stored and read
back. `suco init` writes a key to a file and prints the line that sets the variable from it.

Swapping the key does not revoke anything: a credential made under the old one is still in force
and can no longer be presented. `suco credential list` shows which key each was made under.

## networks

A network is one chain the instance reads. Its name is a key of the document, and is what an
asset refers to.

| Key | Default | |
|---|---|---|
| `networks.<name>.kind` | `evm` | `evm` or `simulated` |
| `networks.<name>.chain_id` | none | What `eth_chainId` has to answer. 1 or more. Required by `evm`, refused by `simulated` |
| `networks.<name>.rpc.own` | none | A node the operator runs themselves. A round reads it when it is set. Secret |
| `networks.<name>.rpc.others` | none | A list of third-party endpoints. With no `own`, a round reads the first and the settling asks all of them. Secret |
| `networks.<name>.poll` | `12s` | How long between rounds. At least `1s` |
| `networks.<name>.width` | `1000` | The most blocks one request for logs asks about. 10 to 10000 |
| `networks.<name>.finality.recheck` | `1m` | How long between asking the endpoints again about what was recorded. At least `1s` |
| `networks.<name>.finality.misses` | `10` | How many times in a row the endpoints have to find nothing before a transfer is treated as gone. At least `2` |

`simulated` is a chain inside the process, for trying the rest of a deployment out. It makes one
block and nothing arrives on it.

An endpoint is `https` anywhere, or `http` to `localhost` or a loopback address. A key on the URL
travels in the clear over `http`, and so does the receipt that comes back. The host is taken as
written, so a name that resolves to a loopback address is still refused.

`poll` decides what the deployment takes from an endpoint it does not run. A round is several
calls, so 12 seconds comes to about forty thousand a day per network. A deployment reading a node
of its own can set it down; 3 seconds is four times that.

`width` is capped by the provider, each at its own value. A provider that refuses a span makes the
next round ask for half as much, down to 10 blocks, and 100 rounds later it doubles back up.

The two under `finality` decide when a transfer on the chain counts as having paid.

There is no setting for how long a payment past its deadline waits. It waits until the network
has been read past the deadline, which is a fact about the chain rather than the clock: once the
position sits on a block stamped at or after the deadline, every transfer that could have paid
the payment has been read. A deployment that has stopped reading expires nothing, however long
it has been stopped.

`recheck` is how long between rounds. It is longer than `poll` because a round asks two questions
of every transfer it is deciding about.

`misses` is a count, not a length of time. A transfer the endpoints are asked about and do not
find, that many times in a row, is treated as gone. It cannot be 1: not finding a transfer once
may be that endpoint reading a state it has not finished replacing.

A network with an `own` node is asked only there. A node the operator runs is the one they
already trust. A network without one asks the `others` in order, and counts a transfer as having
paid once two of them agree. One that does not answer is skipped, so a longer list is more to
fall back on rather than more answers that have to agree; fewer than two answering leaves the
network `too-few` until another does. One answer that differs leaves the payment where it is, and
no third endpoint is asked to break the tie. The transfer stays recorded either way, and the
funds are at the merchant's address.

Reading blocks and deciding what settled are two different things. A round reads one endpoint: a
cursor is a place in a chain as one provider tells it, so changing providers part way would leave
the position meaning something else.

A network no asset refers to is refused. Nothing would read it.

## assets

An asset is one token on one network. Its name is a key of the document, and is what a request
and the CLI say.

| Key | |
|---|---|
| `assets.<name>.network` | The name of a network the document declares |
| `assets.<name>.reference` | What that chain identifies the token by. On `evm`, an address |
| `assets.<name>.symbol` | What to show a person. It does not identify the asset |
| `assets.<name>.decimals` | How many places divide one unit into the smallest unit. 0 to 36 |

All four are required. Two names for one token are refused: a transfer seen on the chain is in
the token, and would have no one name to be recorded under.

A reference is read into the form its chain compares. An EVM chain writes an account in either
case, so the mixed-case form a block explorer shows is read as the lower-case one a transfer
carries. That form also carries a checksum, and a reference whose checksum does not hold is
refused. It is the last moment a mistyped one can be caught, since a chain does not give funds
back.

## Names

A network name and an asset name become part of the dotted paths in a report and the keys a probe
answers with. A name holding a dot is refused, and so is one holding a bracket, which a path uses
to name a place in a list. So is one holding a character a reader cannot see: two names that
render alike would be two names.

## Bounds on the document

At most 256 kilobytes. Anchors and aliases are refused: expanding one copies what it names, and
nothing bounds how often, so a document of a few hundred bytes can name a copy of a copy until
memory runs out.

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
```
