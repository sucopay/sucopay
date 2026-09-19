# Refunds

日本語: [refunds.ja.md](refunds.ja.md)

A refund sends back what a payment received, to the address it came from. It is the payment with
the direction reversed: the merchant signs a transfer out of the wallet the payment was paid to,
sends it, and the same reading of the chain that settled the payment settles this. suco holds no
money and sends no transaction.

**Today the signing page shows a refund and signs none.** The script and style that talk to a
wallet are a module of their own, not yet released, and a deployment without them serves the
plain page: the amount, the two addresses, the deadline and the status. The routes that module
will call are served all the same, and are below.

## The wallet that signs

The wallet a payment is paid to has to be able to sign EIP-712 typed data, because that wallet
is what signs the refund. `suco asset accept <name> <address>` records that address. An address
nobody can sign for takes payments and returns none.

## Opening one

```http
POST /payments/{id}/refunds
Content-Type: application/json

{"amount": "250"}
```

`amount` is in the asset's units, written as a string, and is the only key the body may carry.
Leave the body empty, or send `{}`, to send back everything the payment has left.

Only a payment that is `succeeded` can be refunded. What arrived is taken back when a transfer
stops being on the chain, and `succeeded` is the one status that keeps it.

The answer is `201`, with `Location` at the refund and the refund as the body:

```json
{
  "id": "5f3c9d1e7a0b4826c1d5e9f3a7b02c48",
  "payment": "9c2f7b1a4e8d0356f9a1c4e7b2d508fa",
  "status": "created",
  "amount": "250",
  "destination": "0xab…",
  "expires_at": "2026-09-20T09:42:11Z",
  "created_at": "2026-09-20T09:12:11Z",
  "transfer": null,
  "refund_url": "https://pay.example/refund/7d1c…"
}
```

`Idempotency-Key` is read here as it is on `POST /payments`, and under the same rules
([api.md](api.md)). Make a new one for every refund you open. A key this account has used
already opens nothing more: a request that passes every other rule is answered with the refund
that key opened, whichever payment it named.

`GET /payments/{id}/refunds/{refund}` reads one back, and answers the same body. There is no
route that lists them. Keep the identifier the answer gives, or read it off the webhook event.

## Where the money goes

`destination` is not the caller's to choose. suco reads it off the transfer that paid the
payment and copies it onto the refund when the refund is opened, so that a later reading of the
chain cannot move where a signature sends money. A payment nothing has been seen for has nowhere
to send a refund, and opening one is refused.

## How much is left

A payment can be refunded up to what arrived, counting every refund of it that has not expired.
A refund holds its amount from the moment it is opened, whether or not anyone has signed it.

`GET /payments/{id}` answers `refunded`, the total those refunds hold, in the asset's units:

```json
{"amount": "1000", "received": "1000", "refunded": "250"}
```

What can still be refunded is `received` less `refunded`. Asking for more is refused, and the
refusal says what is left. A refund that expires gives its amount back. One whose key was
already spent on the chain out of the payment's destination does not expire at all, whatever
the rules made of that transfer: money that has gone is not handed back.

## Signing it

`refund_url` is where the merchant signs. It is `<listen.base_url>/refund/<token>`, and the
token is what admits whoever opens it: no credential is asked for. It is answered to the
account's own credentials and to nobody else, and suco keeps it out of its own log and out of
webhook events. Keep it out of yours.

The page refuses to be framed. Open it as a page of its own.

## Two deadlines

| | | Once past |
|---|---|---|
| The refund's | `expires_at` | Nothing more can be signed. Open another refund |
| The page's | 30 days after the refund ends | The URL answers `404` |

A refund is good for 30 minutes from the moment it is opened: long enough to open the page,
reach a wallet, sign and send. The asset itself stops honouring the signature at the same
moment, so a transfer carried after it moves nothing.

## The life of a refund

| `status` | |
|---|---|
| `created` | Signable. Nothing has settled |
| `awaiting_finality` | Past its deadline, and whether a transfer already in flight settles is not decided |
| `succeeded` | The chain settled the transfer. The money is back |
| `expired` | Past its deadline, and nothing settled |

A refund never goes back. A transfer that is seen for it moves nothing by being seen; only the
chain settling one does.

## The routes the page calls

The two routes under `/refund/` are admitted by the token and by no credential. Their answers
carry `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options:
nosniff`, and a Content-Security-Policy under which the page reaches nothing but its own
deployment and is framed by nobody. No CORS header is sent: the page is the only caller. The
script and style are open to anyone and may be cached, and carry `nosniff` alone.

| Route | |
|---|---|
| `GET /refund/{token}` | The page, as HTML. A token that leads nowhere is answered with a page |
| `GET /refund/{token}/state` | What the page shows, as JSON |
| `GET /refund-assets/{path}` | The page's script and style, in a deployment that has them |

### State

```json
{
  "status": "created",
  "payment": "9c2f7b1a4e8d0356f9a1c4e7b2d508fa",
  "amount": "250",
  "asset": {"symbol": "JPYC", "decimals": 18, "network": "polygon", "chain_id": 137},
  "from": "0xab…",
  "to": "0xcd…",
  "expires_at": "2026-09-20T09:42:11Z",
  "authorization": {…},
  "result": null,
  "reason": null
}
```

`from` is the wallet that has to sign, which is where the payment was paid, and `to` is where
the money goes back. `payment` says which payment this is against. `authorization` is what the
merchant signs, and `null` when nothing can be signed. `result` is the transfer seen for the
refund, and `null` until one has been: `tx`, `block_height`, `block_time`, `value` in the
asset's smallest unit, and `settling`, which is `true` until the refund succeeds.

`result` is the transfer that matched the refund. One that spent the refund's key and was not
what the refund authorised holds the refund open, as **How much is left** says, and is not
answered here: `result` stays `null` while the refund sits at `awaiting_finality`.

`reason` says why nothing can be signed, and is `null` while something can.

| `reason` | |
|---|---|
| `done` | The refund has ended |
| `sent` | A transfer has been seen, and is waiting to settle |
| `expired` | Past the deadline |

### What the merchant signs

`authorization` is the EIP-712 typed data, less the parts a wallet fills in. The keys under
`domain` and `message` are the standard's, in camelCase.

```json
{
  "domain": {"name": "JPY Coin", "version": "1", "chainId": 137,
             "verifyingContract": "0x431d…"},
  "message": {"from": "0xab…", "to": "0xcd…", "value": "250000000000000000000",
              "validAfter": "0", "validBefore": "1789561331", "nonce": "0x…"},
  "primaryType": "TransferWithAuthorization"
}
```

Unlike what a payer signs, this carries `from`: a payer may pay from wherever they hold the
money, and a refund can only be signed by the wallet the payment was paid to. `value` is in the
asset's smallest unit, where `amount` is in its units. `validBefore` is `expires_at` in seconds.
`nonce` is the key this refund spends, and the asset honours it once.

## Events

| Event | |
|---|---|
| `refund.succeeded` | The chain settled the transfer |
| `refund.expired` | The deadline passed and nothing settled |

The body is the refund as a read of it answers, less `refund_url`. The two statuses in between
are the merchant's own doing, and a read says what they are. [webhooks.md](webhooks.md) has the
rest.

## Responses

The shape of a failure, and every rule a request is held to, are in [api.md](api.md). What a
refund adds to them:

| Status | `error` | When |
|---|---|---|
| 400 | `invalid` | The payment is not `succeeded`, nothing has been seen for it, or the amount is more than it has left. `problems` says which |
| 404 | `not_found` | No payment or refund of the account under that identifier |
| 413 | `too_large` | The body is over 64 KiB |
| 503 | `unavailable` | suco could not reach its database |

A token that leads nowhere is `404` on all three of the page's routes, and each says it in its
own way: `GET /refund/{token}` answers a page, `GET /refund/{token}/state` answers the body
above, and `/refund-assets/` answers plain text.
