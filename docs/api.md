# API

日本語: [api.ja.md](api.ja.md)

This page is the reference for suco's HTTP API: opening a payment, reading it back, refunding it,
and the routes that manage where suco tells your server about all three.

It is written for the developer calling it, and assumes a credential `suco credential new` wrote.

## Terms

| Term | Meaning |
|---|---|
| payment | The record of one receipt of money, which you create over the API. It has an `id` and a `status` |
| credential | The token sent in `Authorization` on every call to a route that asks for one. `suco credential new` writes one |
| payment page | The page a payer pays on, served by suco. Its URL is the payment's `checkout_url` |
| attempt | What the payment page issues for the payer to sign, with an id. The payer's wallet signs the EIP-3009 `TransferWithAuthorization` inside it |
| `destination` | The wallet address a payment is paid to. `suco asset accept` records one per asset |
| transfer | A movement of the asset on the chain, as suco reads it |
| settled | The chain will no longer take the transfer back |
| account | What credentials, payments, refunds and webhook endpoints belong to. One per instance |
| asset | One token on one network. `suco.yaml` gives it a name |

## Payment flow

Every path below is relative to the `base_url` `suco.yaml` gives under `listen`, which is
`http://localhost:7826` by default.

1. Your server opens a payment with `POST /payments`. The answer carries `checkout_url`.
2. You send the payer to `checkout_url`. That is the payment page, and
   [checkout.md](checkout.md) says what it does.
3. The page issues what the payer signs for the first time, and the payment becomes payable. An
   operator's `suco payment await <id>` does the same. Until the page's script is released, that
   command is the way a payer is given anything to sign, and [ROADMAP.md](../ROADMAP.md) says
   what else is not built.
4. The payer's wallet signs an EIP-3009 `TransferWithAuthorization` of the exact amount to
   `destination`. The wallet sends the transaction.
5. suco reads the chain, sees the transfer, and answers it in `transfer`. The payment has not
   settled yet.
6. The chain settles the transfer and the payment reaches `succeeded`. Hand over the goods then.
   [webhooks.md](webhooks.md) says how suco tells your server.

## Payment states

| `status` | Entered when | Leaves to | Final |
|---|---|---|---|
| `created` | `POST /payments` opened the payment | `awaiting_payment` | no |
| `awaiting_payment` | The page issued what the payer signs for the first time, or an operator ran `suco payment await` | `succeeded`, `failed`, `awaiting_finality` | no |
| `awaiting_finality` | The payment stopped being payable, and whether a transfer already in flight arrives is not settled | `succeeded`, `expired` | no |
| `succeeded` | The chain settled a transfer for the payment | nothing | yes |
| `failed` | No path in this build enters it. It is the state for a payment that will not settle, where waiting longer will not change that | nothing | yes |
| `expired` | Nothing arrived before the payment stopped being payable | nothing | yes |

A chain says a transfer happened before it says the transfer will stay, and a payment reaches
`succeeded` on the second. A transfer authorised a moment before the deadline can still be
arriving after it, so `awaiting_finality` is where the payment waits for the second fact rather
than the end of the order.

## Authentication

Every request carries a credential: `Authorization: Bearer <token>`, with a token
`suco credential new` wrote. A credential belongs to one account and either reads or writes. An
instance has one account, written into the database along with the tables the first time
`suco serve` runs. Handling more than one is not implemented.

A route that changes something asks for a credential that writes. A route that only reads takes
either, and the routes below say which is which. A request without a credential, or with one that
is not in force, is answered `401`. A request whose credential may not do what the route does is
answered `403`.

## Assets and amounts

An asset is one token on one network, such as JPYC on Polygon. It is not a currency. What
identifies an asset is its network and its reference, which is what that chain knows the token by
and on an EVM chain is an address. A symbol identifies nothing: one network can carry two tokens
under the same symbol, and one symbol on two networks is two tokens.

`suco.yaml` lists the assets an instance accepts and gives each a name. A request names one by
that name. A payment carries the network and the reference instead, so it stays in the token it
was opened in even if the document later lists that token under another name, or stops listing
it.

An asset divides one unit into a smallest unit, and `decimals` says by how many places. JPYC has
18, so one JPYC is 1000000000000000000 of its smallest unit. A chain counts in the smallest unit.
This API counts in the asset's units, so `"1000"` is 1000 JPYC.

Amounts are strings. Most clients read a JSON number as floating point, and 1000 JPYC in the
smallest unit is 1000000000000000000000, which a double cannot hold exactly.

## POST /payments

Opens a payment for the account the credential names.

```http
POST /payments
Authorization: Bearer <token>
Content-Type: application/json

{
  "asset": "jpyc",
  "amount": "1000",
  "expires_at": "2026-09-07T12:00:00Z",
  "return_url": "https://shop.example/orders/A-1",
  "metadata": {"order": "A-1"}
}
```

| Parameter | Type | Required | Description |
|---|---|---|---|
| `asset` | string | yes | The name `suco.yaml` lists the asset under. The account has to accept it, which `suco asset accept <name> <address>` records along with the address a payment of it is paid to |
| `amount` | string | yes | A number in the asset's units: `"1000"` is 1000 JPYC. Digits, and after a point more digits. No sign and no exponent. At most as many places after the point as the asset has decimals, and at most 78 digits once written in the asset's smallest unit. Zero is refused |
| `expires_at` | string | no | An RFC 3339 time, after now and at most 30 days ahead. Fifteen minutes ahead when left out |
| `return_url` | string | no | Where the payment page sends the payer back to. An `https` URL of at most 2048 bytes with no username or password. `http://localhost` and `http://127.0.0.1` are accepted too. What the page does with it is in [checkout.md](checkout.md) |
| `metadata` | object | no | Strings under string keys: up to 20 entries, keys up to 64 bytes, values up to 512 bytes. Returned as sent, `{}` when left out, and kept out of suco's log |

Any other key is refused. The body is at most 64 KiB.

The answer is `201`, with the payment's path in `Location` and the payment in the body.

```http
HTTP/1.1 201 Created
Location: /payments/6a5c95937c5ea5c727ebffb682c12a38
Content-Type: application/json

{
  "id": "6a5c95937c5ea5c727ebffb682c12a38",
  "status": "created",
  "asset": {
    "network": "local",
    "reference": "0x0000000000000000000000000000000000000001",
    "symbol": "JPYC",
    "decimals": 18
  },
  "amount": "1000",
  "received": null,
  "destination": "0x00000000000000000000000000000000000000aa",
  "metadata": {"order": "A-1"},
  "expires_at": "2026-09-07T12:00:00Z",
  "created_at": "2026-09-05T23:08:53.514971Z",
  "return_url": "https://shop.example/orders/A-1",
  "checkout_url": "http://localhost:7826/checkout/2c2f…",
  "transfer": null,
  "refunded": "0"
}
```

| Field | Type | Description |
|---|---|---|
| `id` | string | What names the payment on every other route |
| `status` | string | Where the payment has got to. The states are above |
| `asset` | object | The token, by `network` and `reference`, with the `symbol` and `decimals` that describe it. The name `suco.yaml` lists it under is not returned |
| `amount` | string | What the payment asks for, in the asset's units |
| `received` | string or null | What arrived, in the asset's units. `null` until something does |
| `destination` | string | The address the payment is paid to, which `suco asset accept` recorded for the asset |
| `metadata` | object | What the request sent, and `{}` when it sent none |
| `expires_at` | string | When the payment stops being payable, UTC |
| `created_at` | string | When the payment was opened, UTC |
| `return_url` | string or null | Where the page sends the payer back to. `null` when the request left it out |
| `checkout_url` | string | Where you send the payer. Left out of a payment opened before the instance served pages |
| `transfer` | object or null | The transfer that paid the payment. Below |
| `refunded` | string | What the payment's refunds hold, in the asset's units. `"0"` until one is opened |

Whoever has `checkout_url` can read the payment's outcome. Keep it out of your logs, as suco
keeps it out of its own and out of webhook events.

## GET /payments/{id}

Reads one payment of the account the credential names, and answers `200` with the body
`POST /payments` answered.

A payment of another account, a payment nobody opened, and an identifier of no shape are all
answered `404` with one body. A `404` does not say which of the three it is.

## Refunds

A refund sends back what a payment received, to the address it came from. You sign the transfer
out of the wallet the payment was paid to, and suco records it and watches the chain for it.
[refunds.md](refunds.md) has the whole of it, and the states a refund moves through.

`POST /payments/{id}/refunds` takes `amount` in the asset's units, or nothing at all to send back
everything the payment has left. Where the money goes is not the caller's to choose: suco reads
it off the transfer that paid the payment. `Idempotency-Key` is read here too, under the rules
below.

The answer carries `refund_url`, where you sign. Whoever has it can read the refund. Keep it out
of your logs, as suco keeps it out of its own and out of webhook events.

## Idempotency-Key

Opening a payment twice opens two payments, unless the second says it is the first one again.

```http
Idempotency-Key: 8e03978e-40d5-43e8-bc93-6894a57f9324
```

Make the key random, and make a new one for every payment you open. A UUID is what most clients
send. A value in quotes is read too, and the quotes are not part of the key. A key is at most 255
bytes of printable ASCII, is not empty, and carries no quote or backslash. Send the field once,
since two of them name two requests and are refused.

A request under a key this account has already used is answered with the payment that key opened:
`201`, the same `Location`, and `Idempotent-Replayed: true` on the answer. The body is the payment
as it stands rather than a copy of the first answer, so a payment that has become payable since
says so.

The two requests have to carry the same body, byte for byte. Reordering the keys of the JSON is
enough to make it another body. A key that arrives with another body is answered `400`, with a
problem naming `Idempotency-Key`. Send the body you sent the first time, or a key nothing has
used. The IETF draft this field comes from suggests `422` here, and suco answers `400`, as it
does for anything else wrong with a request.

```json
{
  "error": "invalid",
  "problems": [
    {
      "field": "Idempotency-Key",
      "message": "already used for another body. Send that body, or a key nothing has used"
    }
  ]
}
```

A key is kept as long as the payment it opened. A request suco refused keeps no key, so the same
key can be sent again with a body that is right. Two requests under one key that arrive together
are no trouble either: the second waits for the first, and is answered with its payment.

The key is read on `POST /payments` and on `POST /payments/{id}/refunds`, and nowhere else.
Without a key, a request that was sent and not answered has to be looked up on your side, by
whatever you put in `metadata`, before it is sent again.

## transfer

`transfer` is the transfer on the chain that this payment was paid by, and `null` until one has
been seen for it.

```json
{
  "tx": "0x9f1e…",
  "block_height": 78123,
  "block_hash": "0x4c2a…",
  "block_time": "2026-09-05T23:10:44Z",
  "from": "0xab…",
  "value": "1000000000000000000000"
}
```

| Field | Type | Description |
|---|---|---|
| `tx` | string | What the transfer is looked up by on a node of your own |
| `block_height` | integer | The block the transfer is in |
| `block_hash` | string | That block's hash |
| `block_time` | string | That block's time, UTC |
| `from` | string | The address the transfer came from, and where a refund of this payment goes back to |
| `value` | string | What the chain moved, in the asset's smallest unit |

Until `status` is `succeeded` this is a candidate, and a transfer that a chain reorganises away
stops being answered here.

`value` is not written the way `amount` and `received` are, which are in the asset's units: the
same money is `"1000"` there and `"1000000000000000000000"` here.

The chain and the address the money went to are not repeated in it. The chain is `asset.network`,
and a transfer that paid the payment went to `destination` or it would not have been this
payment's.

A transfer that moved less than the payment asks for is not the payment's either. It puts what it
moved in `received` and is not answered here, so a payment can carry a `received` with `transfer`
still `null`.

## Failures

A success is the payment. A failure is one shape, on every route.

```json
{
  "error": "invalid",
  "problems": [
    {
      "field": "amount",
      "message": "\"1e3\" is not an amount: digits, and after a point more digits"
    }
  ]
}
```

| Status | `error` | When | What to do |
|---|---|---|---|
| 400 | `invalid` | The body is not a JSON object, holds a key the API does not read, gives a value of another type, or gives a value outside its rules. Or `Idempotency-Key` is not a key, or arrived with another body | Fix what `problems` names, and send the request again |
| 401 | `unauthorized` | No credential, or one not in force | Send a credential that is in force |
| 403 | `forbidden` | The credential may not do what the route does | Send a credential that writes |
| 404 | `not_found` | No payment, refund, or webhook endpoint of the account under that identifier | Check the identifier, and that the credential is of the account that opened it |
| 409 | a reason | Nothing can be signed for the payment. Only `POST /checkout/{token}/attempts` answers it | [checkout.md](checkout.md) lists the reasons |
| 413 | `too_large` | The body is over 64 KiB | Send a smaller body |
| 503 | `unavailable` | suco could not reach its database | Ask the operator. The reason is in suco's log and not in the body, and [operating.md](operating.md) is where it is chased |

Only `400` carries `problems`.

### Problems

Each problem names the part of the request it is about in `field`, and says in `message` which
rule the value broke. That is a key of the body, or the name of a header where a header is what
was wrong. A problem with the body as a whole has no `field`.

```json
{
  "error": "invalid",
  "problems": [
    {
      "message": "body ends inside the JSON"
    }
  ]
}
```

A value a message repeats is cut to 128 bytes. A field, which may be a key the request chose, and
a message are each cut to 512 bytes. Both are quoted, with the characters written as escapes,
when they hold a control or an invisible character.

Problems come in stages, and a body is answered for the first stage it fails. A key that is not
one is answered with the first stage, so a bad header and a bad body come back together.

1. Everything wrong with the body's shape: unknown keys, values of another type, an asset
   `suco.yaml` does not list, an amount or a time that does not read as one, a required key left
   out.
2. Whether the account accepts the asset.
3. Everything wrong with the values: an amount of zero, an expiry outside its range, metadata
   over its limits.

A body wrong at one stage lists every problem of that stage, so that a client fixes them in one
round.

```json
{
  "error": "invalid",
  "problems": [
    {
      "field": "note",
      "message": "unknown key"
    },
    {
      "field": "expires_at",
      "message": "yesterday is not an RFC 3339 time"
    }
  ]
}
```

## Routes

| Path | Credential | Description |
|---|---|---|
| `POST /payments` | read-write | Open a payment |
| `GET /payments/{id}` | read-only | Read one payment back |
| `POST /payments/{id}/refunds` | read-write | Open a refund against a `succeeded` payment |
| `GET /payments/{id}/refunds/{refund}` | read-only | Read one refund back |
| `/webhook_endpoints…` | read-only or read-write | Register a webhook endpoint, change it, and read what was delivered to it. [webhooks.md](webhooks.md) lists the routes and what each asks for |
| `/checkout/…` and `/checkout-assets/…` | none | The payment page and what it calls. [checkout.md](checkout.md) lists them |
| `/refund/…` and `/refund-assets/…` | none | The signing page and what it calls. [refunds.md](refunds.md) lists them |
| `GET /healthz` | none | Whether the process runs. [operating.md](operating.md) has the detail |
| `GET /readyz` | none | Whether the instance can serve. [operating.md](operating.md) has the detail |

A page route is admitted by the token in the path and by no credential. Every answer under
`/checkout/` and `/refund/` carries `Cache-Control: no-store`, `Referrer-Policy: no-referrer`,
`X-Content-Type-Options: nosniff` and `X-Frame-Options: DENY`. Each carries a content security
policy as well, under which the page reaches its own instance and nothing else and is framed by
nobody. None of them carries a CORS header, and the page is the only caller. The script and style
under `/checkout-assets/` and `/refund-assets/` are open to anyone, may be cached, and carry
`nosniff` alone.

A webhook endpoint of another account, none, and an identifier of no shape are answered `404`, as
a payment's route answers them.

## Related

- [checkout.md](checkout.md): the payment page, and what the payer signs
- [refunds.md](refunds.md): refunds, their states, and the page you sign them on
- [webhooks.md](webhooks.md): what suco sends your server, and what a receiver does with it
- [operating.md](operating.md): what an operator runs, and what `suco doctor` says
