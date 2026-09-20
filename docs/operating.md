# Operating

日本語: [operating.ja.md](operating.ja.md)

Use this runbook to monitor a suco Pay instance and recover it when payments or webhooks stop
progressing. It is for operators who already have a validated
[`suco.yaml`](configuration.md).

An instance is one `suco serve` process. Multiple instances can share a database; one holds the
lease for each network while the others wait as spares.

## Start with the symptom

| Symptom | First check | Continue with |
|---|---|---|
| The process does not answer | `GET /healthz` | Process logs and your service manager |
| The API returns `503` | `GET /readyz` | The matching readiness field below |
| Payments stop progressing | `networks` and `finality` in `/readyz` | `suco doctor` for provider and cursor details |
| Webhooks stop arriving | `webhooks` in `/readyz` | Delivery records and `suco doctor` |
| An asset may be paused or upgraded | `assets` and `paused` in `/readyz` | Confirm the change with the asset issuer |

Run `suco doctor` before changing configuration or moving a network cursor. It performs read-only
checks and reports the resolved configuration, database, RPC providers, assets, and webhook
backlog.

## /healthz and /readyz

Neither route requires authentication. Anyone who can reach the port can call them. To avoid
leaking infrastructure details, their responses omit error messages, hostnames, and RPC URLs.

### /healthz

`GET /healthz` returns `200` while the process is running and checks no dependencies. Use it for
liveness probes. A database failure should not restart a healthy process because restarting the
process cannot restore the database.

### /readyz

`GET /readyz` reports whether the instance can serve API traffic and process payments.

```json
{"status":"ok","database":"reachable","credentials":"read-write",
 "networks":{"polygon":"observing"},"assets":{"jpyc":"unchanged"},"paused":{"jpyc":false},
 "finality":{"polygon":"deciding"},"webhooks":"delivering"}
```

| Field | Description |
|---|---|
| `status` | `ok` or `unavailable` |
| `database` | `reachable`, `unreachable`, or `none configured` for an instance without a database |
| `credentials` | Credential-store access level. Omitted when no store is configured |
| `networks` | Ingestion state for each network, defined below |
| `assets` | Contract-code state for each asset, defined below |
| `paused` | Per asset, whether its issuer has stopped every transfer of it |
| `finality` | Finality-worker state for each network, defined below |
| `webhooks` | Webhook-worker state for the deployment, defined below |

Without a database, the instance omits `networks`, `assets`, `paused`, `finality`, and `webhooks`
because it does not ingest chains, settle transfers, or deliver webhooks.

The route returns `503` with `status: unavailable` when the database is unreachable or every
configured network is in `unreachable`, `stalled`, `chain-mismatch`, or `no-finalized`.
Other network states leave the overall status `ok`. The `finality` and `webhooks` fields do not
change the overall status because the API can still read recorded data while those workers are
degraded.

### networks

A round is one scan of a chain. Each network has a cursor that records the last block a round
completed.

| Word | Meaning | What to do |
|---|---|---|
| `observing` | A round ingested and stored the finalised range within the last 60 seconds | None |
| `no-cursor` | The RPC endpoint is reachable and has the expected chain ID, but no round has completed | Wait for the first round |
| `unreachable` | The latest head request failed | Check the RPC endpoint and provider. Run `suco doctor` for details |
| `stalled` | No round has completed for 60 seconds | Check the provider. If it removed the cursor's history, use `suco network cursor` |
| `chain-mismatch` | `eth_chainId` differs from the configured `chain_id`. Ingestion is stopped | Correct `chain_id` or the RPC endpoint |
| `no-finalized` | The provider does not support the `finalized` block. Ingestion is stopped | Use a provider that supports `finalized` |
| `finalized-changed` | The chain no longer contains the block at the cursor | Move the cursor with `suco network cursor` |
| `finalized-behind` | The provider's finalised block is behind the cursor | Wait for the provider to catch up or switch providers |

The 60 seconds are twice the 30-second lease an instance holds on a network.

Payments do not expire while network ingestion is stopped. Expiry follows the block timestamp at
the cursor, not wall-clock time. See [Configuration](configuration.md#cursor).

### finality

The `finality` field reports the worker that decides whether detected transfers have settled.

| Word | Meaning | What to do |
|---|---|---|
| `deciding` | A finality round completed and stored its decisions | None |
| `no-round` | No finality round has completed since startup | Wait for the first round |
| `waiting` | Another instance holds the network lease and runs finality checks | None |
| `too-few` | Too few `rpc.others` endpoints responded to reach agreement | Add or repair endpoints. Run `suco doctor` to see the response count |
| `unreachable` | The round could not access the database | Check the `database` field |
| `stalled` | Finality rounds stopped completing | Restart the instance |

`deciding` and `too-few` remain visible for three `finality.recheck` intervals to avoid state
flapping after one slow round.

### assets and paused

For each asset, `assets` is `unchanged` or `changed`. `changed` means the contract implementation
differs from the one observed at startup, as it would after a proxy upgrade. Confirm the change
with the issuer before continuing to accept the asset.

`paused` reports whether each asset contract has stopped all transfers. The value is refreshed
once a minute. A missing asset was not read during the last 2 minutes; do not interpret absence
as `false`. A paused asset does not change the overall readiness status because only the issuer
can resume transfers.

### webhooks

One worker sends deliveries for all endpoints, so `webhooks` reports one deployment-wide state.

| Word | Meaning | What to do |
|---|---|---|
| `delivering` | A round created deliveries from state changes and sent those that were due | None |
| `no-round` | No round has finished since this instance started | Wait for the first round |
| `waiting` | Another instance holds the webhook lease and this instance is a spare | None |
| `unreachable` | The round could not access the database | Check the `database` field |
| `stalled` | Rounds have stopped finishing | Restart the instance |

`delivering` remains visible for 15 seconds, or three 5-second rounds. Webhook degradation does
not make the instance unavailable because merchants can still retrieve current state through the
API.

## suco doctor

`suco doctor` is a read-only diagnostic. It prints each resolved setting and its source, then
checks the database, RPC providers, assets, and webhook backlog using the same connections as
`suco serve`.

```
suco.yaml

  assets.jpyc.decimals       18                                      file
  assets.jpyc.network        polygon                                 file
  ...
  networks.polygon.rpc.own   set                                     ${SUCO_POLYGON_RPC_URL}

database: PostgreSQL 17.5, schema 0010_credential_access
credentials: read-write
webhooks:
  3f9c2c1e-6a1b-4a1e-9f4e-2f0f2c5a7b11  2 pending, 1 failed to 7c1d…

networks:
  polygon  evm  chain 137, latest 78123, final 78100, position 78090, behind 10, 2 waiting to settle
    jpyc        implementation 0xa1b2c3...
```

| In the report | Meaning | What to do |
|---|---|---|
| `2 waiting to settle` | Recorded transfers awaiting finality checks | Watch the count. If it continues to grow, inspect the network's `finality` state in `/readyz` |
| `1 disagreed about` | Transfers for which RPC endpoints returned different results | Investigate the providers. Disputed transfers do not settle automatically |
| `3 of 4 others answer` | Number of configured `rpc.others` endpoints that responded | None |
| `no spare` | Exactly as many of the `others` answer as agreement takes | Add an RPC endpoint under `rpc.others` |
| `too few to settle` | Fewer of the `others` answer than agreement takes | Add or repair the RPC endpoints under `rpc.others` |
| `behind 10` | The cursor is 10 blocks behind the finalised block | None unless the distance keeps growing |
| `could not be read: …` | The provider returned an error, shown without the RPC URL | Check the provider and RPC endpoint |
| `chain 80002 (Polygon Amoy, a testnet)` | The RPC endpoint returned a known testnet chain ID | Confirm that the environment is intended for testing |
| `paused` | The issuer has stopped all transfers for the asset | Contact the issuer |
| `paused not read` | The asset's contract would not say whether it is paused | Check the provider |
| `paid to 0x…, which is blocklisted, the provider says` | The asset's contract refuses transfers to the address the account is paid at | Run `suco asset accept` with another address |
| `2 pending, 1 failed to 7c1d…` | Pending and failed deliveries, followed by affected endpoint IDs | Inspect the endpoint's delivery log when failures are unexpected |
| `nothing pending, nothing failed` | No delivery is pending or failed | None |
| `3 endpoints hold a signing secret sealed under another key` | Endpoint secrets were encrypted with a previous `credentials.key` | Ask affected merchants to rotate their signing secrets |

Use the `webhooks` field in `/readyz` to inspect the delivery worker itself.

Asset checks send the receiving address to the network's RPC endpoint: `rpc.own` or the first
entry in `rpc.others`. suco Pay skips this check when it cannot read the network or when the
database does not contain exactly one account.

Without an initialized database schema, `suco doctor` can still query the configured chains but
cannot report their saved cursor positions.

## suco asset accept

```bash
suco asset accept <name> <address>
```

Registers `<address>` as the receiving wallet for the configured asset `<name>`. Before saving
it, suco Pay checks the contract's EIP-712 domain and whether the issuer blocks transfers to the
address.

| Refused when | What to do |
|---|---|
| The contract's EIP-712 domain differs from `suco.yaml` | Check `assets.<name>.eip712` and the network's `chain_id` against the contract |
| The contract refuses transfers to the address | Give another address |
| Either contract check fails | Check the RPC provider. The address is not registered |

JPYC does not expose `DOMAIN_SEPARATOR()`. For this contract, suco Pay verifies the chain ID and
domain name, then uses the configured `version`. The success output states that the version came
from configuration.

These checks send the address to `rpc.own` or the first endpoint in `rpc.others`. The address also
becomes public when it receives a payment.

## suco network cursor

```bash
suco network cursor <name> <height> [--force]
```

Each network has one cursor containing the height and hash of the last block a round ingested.

Use this command when the chain no longer contains the block at the cursor. In that state,
`/readyz` reports `finalized-changed` and ingestion cannot continue automatically.

The command reads the block at `<height>` and stores both its height and hash, preventing a
height from being associated with the wrong chain.

The command does not acquire the network lease. A concurrent round cannot commit its cursor
advance, and the following round starts from the new position.

> **Important:** moving the cursor forward permanently skips blocks. Transfers in skipped blocks
> are never ingested. A payment may expire as unpaid even though funds arrived, and a refund may
> release its reservation even though funds were sent, allowing a duplicate refund.

To prevent this, the command refuses to move forward while the network has payments in
`awaiting_payment` or `awaiting_finality`, or refunds in `created` or `awaiting_finality`. It
reports the count for each state. `--force` bypasses the check and logs the skipped counts.
Moving the cursor backwards is allowed because subsequent rounds read the range again.

Use `--force` only when a provider has removed required history and you have reconciled every
open payment and refund that may be affected.

The log records both cursor positions so you can restore the previous one if needed.

## suco payment await

```bash
suco payment await <id>
```

Makes a payment payable and prints the seven values the payer signs. Use it to test payments until
the Checkout browser module is released.

```
contract 0xe7c3d8c9a439fede00d2600032d5db0be71c3c29
chainId 137
to 0x1234567890123456789012345678901234567890
value 1000000000000000000
validAfter 0
validBefore 1788972899
nonce 0x11becaaf611be4cfb6bd8a5287bf3688f85dbbbd2af45aa10ac31a7b4513ae01
```

The `nonce` links a transfer to the payment. `validBefore` is the payment deadline truncated to
whole seconds. A modified signature may contain a later deadline, but a transfer submitted at or
after the printed deadline does not complete the payment.

> **Important:** send this output only to the payer through a trusted channel. It reveals the
> payment details. Anyone who also controls the payer's key can use it to sign the payment.

The payment must be `awaiting_payment`, and the instance must have completed a network round.
Running the command again does not create a second `nonce`; it reports that the payment already
has one.

## Webhook endpoints inside the deployment

Webhook registration and delivery reject URLs that resolve to private or loopback addresses.
This prevents server-side request forgery into the deployment network. To allow an internal
receiver you control, set an address range on that endpoint's database row:

```sql
update webhook_endpoints set allowed = '{10.0.5.0/24}' where id = '<endpoint id>';
```

The allowance applies to one endpoint and address range, not a hostname. First register an
external URL, apply the database update, then change the endpoint to its internal URL with
`PATCH /webhook_endpoints/{id}`. The patch validates the new URL against the allowance. An
invalid prefix grants no access.

## Next steps

- Add alerts for `status: unavailable`, growing finality backlogs, and failed webhook deliveries.
- Rehearse cursor recovery in a test environment before using `--force` in production.
- Share the [Webhook receiver requirements](webhooks.md#receiver-requirements) with integration
  teams.
