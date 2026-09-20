# Operating

日本語: [operating.ja.md](operating.ja.md)

What an instance says about itself, and the four commands for putting it right.

## /healthz and /readyz

Neither route asks for a credential. Anyone who can reach the port can read them, so neither
carries a reason, a hostname, or any part of an RPC endpoint.

### /healthz

`GET /healthz` answers `200` while the process runs. It consults nothing. Point a liveness probe
here and nowhere else: a probe that failed because the database was unreachable would have the
orchestrator restart a working instance, which does not bring the database back.

### /readyz

`GET /readyz` answers whether the instance can serve.

```json
{"status":"ok","database":"reachable","credentials":"read-write",
 "networks":{"polygon":"observing"},"assets":{"jpyc":"unchanged"},"paused":{"jpyc":false},
 "finality":{"polygon":"deciding"},"webhooks":"delivering"}
```

| Field | Description |
|---|---|
| `status` | `ok` or `unavailable` |
| `database` | `reachable`, `unreachable`, or `none configured` for an instance without a database |
| `credentials` | What the store of credentials holds. Left out where there is no store |
| `networks` | One word per network, below |
| `assets` | One word per asset, below |
| `paused` | Per asset, whether its issuer has stopped every transfer of it |
| `finality` | One word per network, below |
| `webhooks` | One word for the deployment, below |

An instance configured without a database reads no chain, settles nothing and delivers nothing,
so it leaves out `networks`, `assets`, `paused`, `finality` and `webhooks`.

`status` is `unavailable`, and the response `503`, when the database cannot be reached or when
every configured network is `unreachable`, `stalled`, `chain-mismatch` or `no-finalized`. The
other four words leave it `ok`: the API is running, and the one who can act is you or the
provider. No word under `finality` or `webhooks` makes it `unavailable`. An instance that
settles nothing still sees payments arrive and records them, and the funds are at the merchant's
address either way.

### networks

| Word | Meaning | What to do |
|---|---|---|
| `observing` | A round finished within the last 60 seconds. Finishing is reading the finalised range and writing what it found. A failure ahead of finality does not count | Nothing |
| `no-cursor` | The chain answers and is the one `suco.yaml` names, and no round has finished yet | Wait for the first round |
| `unreachable` | The last read of the head failed | Check the provider and the RPC endpoint. `suco doctor` says what the instance reaches |
| `stalled` | No round has finished for 60 seconds. A provider that has dropped the history the cursor sits in leaves a network here | Check the provider. If the history is gone, move the cursor with `suco network cursor`, below |
| `chain-mismatch` | `eth_chainId` is not what `suco.yaml` names. Nothing is read or written | Fix the network's `chain_id` or its RPC endpoint |
| `no-finalized` | The provider will not say which block is final. Nothing is read or written | Use a provider that answers for the `finalized` block |
| `finalized-changed` | The chain no longer holds the block the cursor sits on. Nothing moves until somebody puts the cursor where it does | Move the cursor with `suco network cursor`, below |
| `finalized-behind` | The provider's final block is below the cursor. The provider is behind, and reading resumes when it catches up | Wait, or switch to a provider that has caught up |

The 60 seconds are twice the 30-second lease an instance holds on a network.

While a network is not being read, no payment on it expires. That holds for `unreachable`,
`stalled`, `chain-mismatch`, `no-finalized` and `finalized-changed` alike. Expiry follows the
block time of the position read, not the clock. [configuration.md](configuration.md) describes
the rule.

### finality

Reading a chain and deciding what settled are two things, and `finality` answers for the second.

| Word | Meaning | What to do |
|---|---|---|
| `deciding` | A round asked the endpoints and wrote what their answers settled | Nothing |
| `no-round` | No round has finished since this instance started. Nothing is wrong yet | Wait for the first round |
| `waiting` | Another instance holds the network. This one is the spare, and the other is settling | Nothing |
| `too-few` | Fewer endpoints answered the last round than agreement takes. What the round asked about stays where it was until another endpoint answers. `suco doctor` says how many answer | Add or repair endpoints under `rpc.others`. [configuration.md](configuration.md) says how many agreement takes |
| `unreachable` | The last round did not finish. An endpoint that does not answer no longer ends a round, so what stopped it is the database, and `database` says so | See `database` |
| `stalled` | Rounds have stopped finishing. A round cannot end the loop it is in, so this is a worker stuck inside one | Restart the instance |

`deciding` and `too-few` hold for three rounds of `networks.<name>.finality.recheck`, so that one
slow round does not change the word.

### assets and paused

`assets` carries `unchanged` or `changed` for each asset. It is `changed` once the code the chain
runs for that asset is not the code it ran when the instance started, which is what an upgrade
of a proxy does. Confirm the change with the issuer before trusting the asset further.

`paused` says, for each asset, whether its issuer has stopped every transfer of it, as the asset's
contract answers. It is read once a minute. An asset missing from `paused` was not read in the
last 2 minutes, for any of three reasons: the contract did not answer, the provider did not carry
the call, or this instance has not been reading. Absence is never "not paused". A paused asset
leaves `status` where it is: the API is up, and the one who can act is the issuer.

### webhooks

`webhooks` is one word for the deployment, since one worker sends every endpoint's deliveries.

| Word | Meaning | What to do |
|---|---|---|
| `delivering` | A round turned what the payments produced into deliveries and sent what was due | Nothing |
| `no-round` | No round has finished since this instance started | Wait for the first round |
| `waiting` | Another instance holds the deliveries. This one is the spare | Nothing |
| `unreachable` | The last round did not finish, which is the database. A receiver that does not answer is an attempt written down, not a round stopped | See `database` |
| `stalled` | Rounds have stopped finishing | Restart the instance |

`delivering` holds for 15 seconds, three of the 5-second rounds. No word here makes the instance
`unavailable`: what a merchant was not told, they can still read.

## suco doctor

Prints every setting it resolved and where each came from, then what it reaches.

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

`waiting to settle` is how many recorded transfers are waiting for the endpoints to be asked about
them. The worker is in the process that serves, so a report counts what the database holds for it
rather than asking a worker that is not there. A number that keeps growing between reports is a
deployment whose settling has stopped getting anywhere, and `/readyz` says which of the words
above it is in.

`webhooks` counts the deliveries the database holds, by account: `pending` ones waiting for
their next attempt, and `failed` ones given up on after their last, with the endpoints the
failed ones were to. `nothing pending, nothing failed` is the line when there are none. A count
of endpoints holding a secret sealed under another key follows when there are any, which is what
a swapped `credentials.key` leaves behind: those merchants rotate their secret, and deliveries go
on. What the worker itself is doing is the `webhooks` word of `/readyz`.

`doctor` names a testnet as one after its chain id, as `chain 80002 (Polygon Amoy, a testnet)`,
so that a document copied from the step before production is caught here.

A network reached only through `others` says how many of them answer, as `3 of 4 others answer`.
`no spare` follows when exactly as many answer as agreement takes, and `too few to settle` below
that. Transfers the endpoints disagree about are counted after what is waiting, as
`2 waiting to settle, 1 disagreed about`, and only when there are any. Nothing settles from a
disagreement and it does not resolve itself, so the count is the operator's to act on.

`behind` counts from the final block rather than the latest, because the finalised range is what
a round reads. A network that could not be read says so in place of the numbers, with the
provider's own code and words, cut short and with nothing of the endpoint in them.

Each asset's line says `paused` when its issuer has stopped it, and `paused not read` when the
contract would not say; an asset that is not paused adds nothing. Where the deployment's one
account is paid for that asset, the report asks the contract whether it refuses transfers to
that address, and says `paid to 0x…, which is blocklisted, the provider says` when it does. It
asks nothing of an address on a network it could not read, and nothing where the database holds
two accounts: naming one is not implemented. The address goes to the network's endpoint in the
asking, the operator's own node or the first of the others.

It reads the chains through the same adapters a start reads through, so what it says is what an
instance would meet. It writes nothing.

A deployment with no database, or one nothing has applied the schema to, still has its chains
read. What it cannot say is how far each has been read.

## suco asset accept

```bash
suco asset accept <name> <address>
```

Records that the account takes the asset the document lists under that name, paid to the
address, and asks the asset's contract two things first: what it signs under, so that a payer's
wallet signs under the same, and whether it refuses transfers to the address, which an issuer
does to one account at a time. A domain that is not the document's refuses the registration,
and so does an address the contract refuses. So does an answer that could not be read: an
address is not registered on the strength of a provider that would not carry it. A contract
with no `DOMAIN_SEPARATOR()` to answer, which JPYC is, is checked by what it does answer: the
chain it is on against the network's `chain_id`, and what it calls itself against the domain's
`name`. The `version` is then the document's word, and the line that confirms the registration
says so.
The provider asked is the network's endpoint, the operator's own node or the first of the
others, and the address goes to it in the asking. It is public the moment a payment reaches it.

## Allowing a webhook endpoint inside the deployment

A webhook URL that resolves to an address inside the deployment, such as a private network or
the loopback, is refused at registration and at every send, so that a merchant cannot make the
deployment call what only it can reach. An operator running a receiver of their own inside can
allow one endpoint to reach one address or range, by writing it on the endpoint's row:

```sql
update webhook_endpoints set allowed = '{10.0.5.0/24}' where id = '<endpoint id>';
```

The allowance is bound to the endpoint and to the address, not to the name: a name moved to
another inside address is refused as before. Since a URL that resolves inside cannot be
registered, the merchant registers a URL that resolves outside, the operator writes the
allowance, and the merchant then changes the URL with `PATCH /webhook_endpoints/{id}`, which
checks it again with the allowance in force. An entry that is not a prefix allows nothing and
stops nothing else.

## suco network cursor

```bash
suco network cursor <name> <height> [--force]
```

A cursor is what moves and a position is where it is. Each network has one, and it holds the
height a round has read to and the hash of the block there.

A chain that no longer holds the block the cursor sits on is one no round reads past. How far back
to go is not something a round can work out, so this is how somebody puts the cursor where the
chain does hold a block. `/readyz` says `finalized-changed` while a network is in that state.

The block at that height is read from the chain and its hash is written with it. A height alone
does not say which chain it was on.

No lease is taken. A round advances on a condition of the position it read, so one under way when
this writes does not commit its advance, and the round after it reads from where this put it.

**Nothing reads the blocks it skips.** Putting the cursor forward past blocks no round has read
means every transfer in them goes unseen, and there is no later pass that finds them. A payment
still open on the network may have been paid in one of those blocks, and once the cursor is past
its deadline it expires as unpaid. A refund still open may have been sent in one of them, and
expires with its amount refundable again while the money is already gone, which lets the same
money go out twice. So the command refuses to move forward while the network has a payment that
is `awaiting_payment` or `awaiting_finality`, or a refund that is `created` or
`awaiting_finality`, and says how many of each. `--force` moves it anyway, and the log line says
how many of each were skipped. Moving the cursor back is never refused: nothing is skipped, and
the rounds read the range again.

A provider that has dropped its history is exactly the case this exists for, and there the only
way on is forward; `--force` is for that, once the operator has counted what it costs.

Where the cursor was and where it is now both go to the log. Where it was is what puts it back.

## suco payment await

```bash
suco payment await <id>
```

Makes one payment payable and prints the seven values a payer signs to pay it. It stands in for
suco Checkout until there is one.

```
contract 0xe7c3d8c9a439fede00d2600032d5db0be71c3c29
chainId 137
to 0x1234567890123456789012345678901234567890
value 1000000000000000000
validAfter 0
validBefore 1788972899
nonce 0x11becaaf611be4cfb6bd8a5287bf3688f85dbbbd2af45aa10ac31a7b4513ae01
```

The nonce is the key the reader matches a transfer to. `validBefore` is the payment's deadline,
truncated to the second the chain compares. What the payer signs is theirs to write, so a transfer
can come back carrying a later deadline than the one printed here. One carried at or after the
deadline printed here is recorded against the payment and does not pay it.

**What it prints goes to a person at a terminal and nowhere that keeps it.** Anybody who reads it
can tell what is being paid where, and can sign for the payer if they also hold the payer's key.

A payment has to be `awaiting_payment` and its network has to have been read before a key is
issued: a key handed out for a chain nothing has ever read would be signed, paid, and never seen.
Running the command again on a payment that is already payable issues no second key, and says the
payment already has one.
