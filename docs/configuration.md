# Configuration

日本語: [configuration.ja.md](configuration.ja.md)

This reference lists every `suco.yaml` setting, its default, and the operational effect of
changing it. It is for operators configuring a suco Pay instance.

## Configuration workflow

1. Run `suco init` to create `suco.yaml` and the credentials key file.
2. Set `database.managed: false` and provide `database.url` through an environment variable.
3. Add a network and an asset. Use the [testnet example](#a-testnet) before connecting mainnet.
4. Run `suco doctor` and resolve every reported error.
5. Start the instance with `suco serve`.

Start from the [complete example](#an-example) if you need a copyable configuration.

## Configuration loading

Every `suco` command reads one configuration file. The default path is `suco.yaml`; set
`SUCO_CONFIG` to use a different path.

`suco doctor` validates the file without starting the server. It prints each resolved setting
and its source.

Omitted settings use their defaults. Unknown keys are rejected so that misspellings cannot pass
silently.

## Secrets

A value can reference an environment variable as `${NAME}`. The reference must be the entire
value; interpolation such as `https://${HOST}/rpc` is rejected.

The following settings are secrets. `suco doctor` reports whether each one is set but never
prints its value. Errors and logs omit the values as well.

- `database.url`
- `credentials.key`
- `networks.*.rpc.own`
- `networks.*.rpc.others`

Other settings print in full. suco Pay rejects a username or password in a non-secret URL and
rejects a 64-character hexadecimal value in a non-secret setting. Rejection messages do not
include any part of the value.

## listen

| Setting | Default | Description |
|---|---|---|
| `listen.host` | `127.0.0.1` | Interface on which to listen. The default accepts connections only from the local machine |
| `listen.port` | `7826` | 1 to 65535 |
| `listen.base_url` | `http://localhost:<port>` | Public `http` or `https` URL of the instance. Behind a proxy, this differs from `listen.host` |

## log

| Setting | Default | Description |
|---|---|---|
| `log.level` | `info` | `debug`, `info`, `warn` or `error` |
| `log.format` | `text` | `text` for interactive use or `json` for structured log collection |

## database

| Setting | Default | Description |
|---|---|---|
| `database.managed` | `true` | Whether suco Pay manages the database. This mode is not implemented |
| `database.url` | none | A PostgreSQL connection string. Secret |

The two are mutually exclusive. Write `managed: false` and give a URL:

```yaml
database:
  managed: false
  url: ${SUCO_DATABASE_URL}
```

Managed databases are not implemented. Setting `managed: true`, or setting `managed: false`
without `url`, causes configuration validation to fail.

If the `database` section is absent, `suco serve` starts only the `/healthz` and `/readyz`
routes. It does not read chains or persist data, and commands that require a database fail.

For a database on another machine, use `sslmode=verify-full`. Use `verify-ca` only when the
certificate name cannot match the connection host. Weaker modes can fall back to cleartext and
expose payment data to anyone on the network path. Loopback addresses and Unix sockets do not
need an `sslmode` override.

## credentials

| Setting | Default | Description |
|---|---|---|
| `credentials.key` | none | A secret 32-byte key written as 64 hexadecimal characters. Used to hash stored credentials |
| `credentials.key_id` | none | The identifier of the key used for stored credentials. Not secret |

Both settings are required when `database.url` is present. `suco init` writes the key to a file
and prints the command that loads it into an environment variable.

Replacing the key does not revoke existing credentials, but the instance can no longer verify a
credential created under the old key. Use `suco credential list` to see the key ID associated
with each credential.

## networks

A network is one chain the instance reads. Its name is a key of `suco.yaml`, and is what an
asset refers to.

| Setting | Default | Description |
|---|---|---|
| `networks.<name>.kind` | `evm` | `evm` or `simulated` |
| `networks.<name>.chain_id` | none | Expected `eth_chainId`, at least 1. Required for `evm`; not allowed for `simulated` |
| `networks.<name>.rpc.own` | none | An RPC endpoint for a node you operate. When set, chain reads use this endpoint. Secret |
| `networks.<name>.rpc.others` | none | Third-party RPC endpoints. Used for chain reads and finality when `own` is absent. Secret |
| `networks.<name>.poll` | `12s` | Interval between chain-reading rounds. At least `1s` |
| `networks.<name>.width` | `1000` | Maximum block range in one log request. 10 to 10000 |
| `networks.<name>.finality.recheck` | `1m` | Interval between finality checks for recorded transfers. At least `1s` |
| `networks.<name>.finality.misses` | `10` | Consecutive missing observations before a transfer is treated as removed. At least `2` |

`simulated` runs an in-process chain for testing the rest of the instance. It creates one empty
block.

Every configured network must be referenced by at least one asset.

### rpc

RPC endpoints must use HTTPS. HTTP is allowed only for `localhost` or a literal loopback address.
HTTP exposes URL credentials and RPC responses in cleartext. A hostname that resolves to a
loopback address is still rejected; use the loopback address directly.

When `rpc.own` is set, suco Pay uses only that endpoint. Otherwise, it queries `rpc.others` in
order and requires two matching responses before treating a transfer as settled. Unavailable
endpoints are skipped, so adding endpoints increases redundancy without increasing the number
of matching responses required.

If fewer than two endpoints respond, readiness reports `too-few`. If two endpoints disagree,
the payment does not advance and a third endpoint does not break the tie. The transfer remains
recorded in both cases.

### poll and width

`poll` controls RPC request frequency. A round makes several calls; the default 12-second
interval produces roughly 40,000 calls per network each day. Reducing the interval to 3 seconds
quadruples that volume. Use a shorter interval only when the provider can support it.

RPC providers impose different block-range limits. If a provider rejects the current `width`,
the next round halves it, down to 10 blocks. After 100 successful rounds, the range doubles up to
the configured limit.

### finality

The `finality` settings control when a detected transfer is treated as settled.

`recheck` is the interval between finality rounds. Each round makes two RPC calls for every
transfer awaiting a decision.

`misses` is a count, not a duration. A transfer is treated as removed after this many consecutive
checks fail to find it. The minimum is 2 because one missing observation can occur while a
provider updates its state.

### cursor

Block ingestion and finality are separate processes. Each ingestion round reads one RPC
endpoint. The network cursor records the block position reported by that provider.

Payment expiry follows chain progress rather than wall-clock time. suco Pay waits until the
network cursor reaches a block timestamp at or after the deadline, ensuring it has read every
eligible transfer. If chain ingestion stops, payments on that network do not expire.

## assets

An asset is one token on one network. Its key in `suco.yaml` is the name used by API requests and
CLI commands.

| Setting | Default | Description |
|---|---|---|
| `assets.<name>.network` | none | The name of a network `suco.yaml` declares |
| `assets.<name>.reference` | none | Chain-specific token identifier. For `evm`, this is the contract address |
| `assets.<name>.symbol` | none | Display symbol. It does not uniquely identify the asset |
| `assets.<name>.decimals` | none | Number of decimal places between the display unit and smallest unit. 0 to 36 |
| `assets.<name>.eip712.name` | none | Token name in the EIP-712 domain used by the contract. JPYC uses `JPY Coin`. Specify it together with `version`, or omit both |
| `assets.<name>.eip712.version` | none | Version in the EIP-712 domain. JPYC uses `1` |

The first four settings are required. Checkout also requires both `eip712` settings. The same
token cannot be configured under two names.

suco Pay normalizes each reference to the form used by its chain. EVM addresses are stored in
lowercase. When you provide a mixed-case address, its checksum must be valid. This validation
helps catch an incorrect contract address before funds are sent.

## A testnet

Before mainnet, test with a real wallet on Polygon Amoy and JPYC from
[JPYC's faucet](https://faucet.jpyc.co.jp/). Configure the testnet as follows:

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

Keep the testnet name distinct from production. JPYC uses the same contract address on Amoy and
Polygon, so naming both networks `polygon` makes a testnet configuration difficult to detect.
`suco doctor` identifies known testnets regardless of the local network name.

## Names and bounds

A network or asset name becomes part of dotted configuration paths and readiness response keys.
Names cannot contain dots, brackets, or invisible characters.

The configuration file is limited to 256 KiB. YAML anchors and aliases are not supported.

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

## Next steps

1. Validate the configuration with `suco doctor`.
2. Register a receiving wallet with [`suco asset accept`](operating.md#suco-asset-accept).
3. Configure health probes and incident response with [Operations](operating.md).
