# API

日本語: [api.ja.md](api.ja.md)

suco serves a merchant's server over HTTP, in JSON. Two routes exist so far: one opens a payment,
the other reads it back. Both are relative to the `base_url` `suco.yaml` gives under `listen`;
by default that is `http://localhost:7826`.

## Authentication

Every request carries a credential: `Authorization: Bearer <token>`, with a token
`suco credential new` wrote. A credential belongs to one account and either reads or writes. So
far an instance has one account, made along with the tables the first time `suco serve` starts.
`POST /payments` asks for a credential that writes; `GET /payments/{id}` is satisfied with either.
A request without a credential, or with one that is not in force, is answered `401`. A request
whose credential may not do what the route does is answered `403`.

## Assets and amounts

An asset is one token on one network: JPYC on Polygon, say. It is not a currency. Two tokens on
one network can present the same symbol, and one symbol on two networks is two tokens, so what
identifies an asset is its network and its reference — what that chain knows the token by, which
on an EVM chain is an address — and never its symbol.

`suco.yaml` lists the assets an instance accepts and gives each a name. A request names one by
that name. A payment carries the network and the reference instead, so it stays in the token it
was opened in even if the document later lists that token under another name, or stops listing it.

An asset divides one unit into a smallest unit, and `decimals` says by how many places. JPYC has
18, so one JPYC is 1000000000000000000 of its smallest unit. A chain counts in the smallest unit;
this API counts in the asset's units, so `"1000"` is 1000 JPYC.

Amounts are strings. JSON numbers are read as floating point by most clients, and 1000 JPYC in
the smallest unit is 1000000000000000000000, which a double cannot hold exactly — an amount off
by a factor of ten is a well-formed one.

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
  "metadata": {"order": "A-1"}
}
```

| Key | | |
|---|---|---|
| `asset` | required | The name `suco.yaml` lists the asset under. The account has to accept it, which `suco asset accept <name> <address>` records along with where a payment of it is paid to. |
| `amount` | required | A number in the asset's units, written as a string: `"1000"` is 1000 JPYC. Digits, and after a point more digits; no sign, no exponent. At most as many places after the point as the asset has decimals, and at most 78 digits once written in the asset's smallest unit. `0` is not a payment. |
| `expires_at` | optional | An RFC 3339 time, after now and at most 30 days ahead. Fifteen minutes ahead when left out. |
| `metadata` | optional | Strings under string keys: up to 20 entries, keys up to 64 bytes, values up to 512 bytes. Returned as sent, `{}` when left out, and kept out of suco's log. |

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
  "created_at": "2026-09-05T23:08:53.514971Z"
}
```

`asset` identifies the token by its network and reference, and describes it. The name
`suco.yaml` lists it under is not returned: an operator may rename it, and a payment stays in
the token it was opened in. `destination` is the address the payment is paid to, the one
`suco asset accept` recorded for the asset. `amount` and `received` are in the asset's units;
`received` is `null` until something arrives. Times are UTC.

A payment is opened with `status` `created`.

Opening a payment twice opens two payments. There is no idempotency key yet: a request that
was sent and not answered has to be looked up on the merchant's side, by whatever the merchant
put in `metadata`, before it is sent again.

## GET /payments/{id}

Reads one payment of the account the credential names, and answers `200` with the body
`POST /payments` answered.

A payment of another account, a payment nobody opened, and an identifier of no shape are all
answered `404`, with one body. The answer says nothing about what exists.

## Webhook endpoints

Where suco tells your server that a payment changed. Registering, listing, changing, rotating
the secret of, deleting and testing an endpoint, and reading what was delivered to it, are the
routes under `/webhook_endpoints`, all for the account the credential names. What is sent, how
it is signed and what a receiver does with it is in [webhooks.md](webhooks.md).

| Route | Credential | |
|---|---|---|
| `POST /webhook_endpoints` | read-write | Register one and receive its secret |
| `GET /webhook_endpoints` | read-only | List them, without secrets |
| `GET /webhook_endpoints/{id}` | read-only | Read one |
| `PATCH /webhook_endpoints/{id}` | read-write | Change `url`, `description`, `events` or `enabled` |
| `POST /webhook_endpoints/{id}/secret` | read-write | Rotate the secret |
| `DELETE /webhook_endpoints/{id}` | read-write | Remove it |
| `POST /webhook_endpoints/{id}/test` | read-write | Send one `endpoint.test` |
| `GET /webhook_endpoints/{id}/deliveries` | read-only | The newest 100 deliveries and their attempts |

An endpoint of another account, none, and an identifier of no shape are answered `404`, as a
payment's route answers them.

## The life of a payment

| `status` | |
|---|---|
| `created` | Opened. Nothing can be paid against it yet |
| `awaiting_payment` | Payable. A payer can be given something to sign |
| `awaiting_finality` | No longer payable, and whether a transfer already in flight arrives is not settled |
| `succeeded` | Paid, and settled |
| `failed` | It will not settle, and waiting longer will not change that |
| `expired` | Nothing arrived before it stopped being payable |

A payment goes from `created` to `awaiting_payment`, and from there to `succeeded`, to `failed`,
or to `awaiting_finality`; and from `awaiting_finality` to `succeeded` or to `expired`. The last
three are final, and nothing follows them.

`awaiting_finality` is a state and not a detail, because a chain says a transfer happened before
it says the transfer will stay. Those are two facts and a payment reaches `succeeded` on the
second. A transfer authorised a moment before the deadline can still be arriving after it, and
without this state the only place to put one would be a payment already called expired, which is
final.

**Today a payment goes no further than `awaiting_payment`.** Nothing over this API makes one
payable yet: `suco payment await <id>` does, and prints what a payer signs. A transfer is then
seen, matched and recorded, and deciding it has settled is not built.
[ROADMAP.md](../ROADMAP.md) says what else is not.

## Responses

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

| Status | `error` | When |
|---|---|---|
| 400 | `invalid` | The body is not a JSON object, holds a key the API does not read, gives a value of another type, or gives a value outside its rules. `problems` says which. |
| 401 | `unauthorized` | No credential, or one not in force. |
| 403 | `forbidden` | The credential may not do what the route does. |
| 404 | `not_found` | No payment, or no webhook endpoint, of the account under that identifier. |
| 413 | `too_large` | The body is over 64 KiB. |
| 503 | `unavailable` | suco could not reach its database. The reason is in its log and not in the body. |

Only `400` carries `problems`.

### Problems

Each problem names the request key it is about in `field`, and says in `message` which rule the
value broke. A problem with the body as a whole has no `field`:

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
a message are each cut to 512 bytes, and quoted, with the characters written as escapes, when they
hold a control or an invisible character.

Problems come in stages, and a body is answered for the first stage it fails. First everything
wrong with the body's shape: unknown keys, values of another type, an asset `suco.yaml` does not
list, an amount or a time that does not read as one, a required key left out. Then whether the
account accepts the asset. Then everything wrong with the values: an amount of zero, an expiry
outside its range, metadata over its limits. A body wrong at one stage lists every problem of
that stage, so that a client fixes them in one round.

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
