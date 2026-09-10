# Operating

日本語: [operating.ja.md](operating.ja.md)

What an instance says about itself, and the two commands for putting it right.

## /healthz and /readyz

Neither asks for a credential. Both are read by whoever can reach the port, so neither carries a
reason, a hostname, or anything of an endpoint.

`GET /healthz` answers `200` as long as the process is running. It consults nothing: a liveness
probe that failed because a database was unreachable would have an orchestrator restart an
instance that is working, which does not bring the database back.

`GET /readyz` answers whether the instance can serve.

```json
{"status":"ok","database":"reachable","credentials":"read-write",
 "networks":{"polygon":"observing"},"assets":{"jpyc":"unchanged"}}
```

`networks` and `assets` are left out by an instance configured without a database, which reads no
chain. `credentials` is left out where there is no store to ask.

One word for each network:

| | |
|---|---|
| `observing` | A round finished within the last 60 seconds. Finishing is reading the finalised range and writing what it found; a failure ahead of finality does not count |
| `no-cursor` | The chain answers and is the one the document names, and no round has finished yet |
| `unreachable` | The last read of the head failed |
| `stalled` | No round has finished for 60 seconds. A provider that has dropped the history the cursor sits in leaves a network here |
| `chain-mismatch` | `eth_chainId` is not what the document names. Nothing is read or written |
| `no-finalized` | The provider will not say which block is final. Nothing is read or written |
| `finalized-changed` | The chain no longer holds the block the cursor sits on. Nothing moves until somebody puts the cursor where it does |
| `finalized-behind` | The provider's final block is below the cursor. It is behind, and reading resumes when it catches up |

Sixty seconds is twice the term of the lease one instance holds on a network, which leaves whoever
takes over time to finish a round of their own.

One word for each asset, `unchanged` or `changed`. It is `changed` once the code the chain runs
for that asset is not the code it ran when the instance started, which is what an upgrade of a
proxy does.

`status` is `unavailable`, and the response `503`, when the database cannot be reached, or when
every configured network is `unreachable`, `stalled`, `chain-mismatch` or `no-finalized`. The
other four words leave it `ok`: the API is running, and the one who can act is the operator or
the provider.

## suco doctor

Prints every setting it resolved and where each came from, then what it reaches.

```
suco.yaml

  assets.jpyc.decimals   18                                          file
  assets.jpyc.network    polygon                                     file
  ...
  networks.polygon.rpc   set                                         ${SUCO_POLYGON_RPC_URL}

database: PostgreSQL 17.5, schema 0005_attempts_and_observations
credentials: read-write

networks:
  polygon  evm  chain 137, latest 78123, final 78100, position 78090, behind 10
    jpyc        implementation 0xa1b2c3...
```

`behind` counts from the final block rather than the latest, because the finalised range is what
a round reads. A network that could not be read says so in place of the numbers, with the
provider's own code and words, cut short and with nothing of the endpoint in them.

It reads the chains through the same adapters a start reads through, so what it says is what an
instance would meet. It writes nothing.

A deployment with no database, or one nothing has applied the schema to, still has its chains
read. What it cannot say is how far each has been read.

## suco network cursor

```bash
suco network cursor <name> <height>
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
means every transfer in them goes unseen, and there is no later pass that finds them. The command
does not compare the height against where the cursor is: a provider that has dropped its history
is exactly the case this exists for, and there the only way on is forward.

Where the cursor was and where it is now both go to the log. Where it was is what puts it back.

## suco payment await

```bash
suco payment await <id>
```

Makes one payment payable and prints the seven values a payer signs to pay it. It stands in for
Checkout until there is a Checkout.

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
truncated to the second the chain compares.

**What it prints goes to a person at a terminal and nowhere that keeps it.** Anybody who reads it
can tell what is being paid where, and can sign for the payer if they also hold the payer's key.

A payment has to be `awaiting_payment` and its network has to have been read before a key is
issued: a key handed out for a chain nothing has ever read would be signed, paid, and never seen.
Running the command again on a payment that is already payable issues no second key, and says the
payment already has one.
