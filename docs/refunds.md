# Refunds

日本語: [refunds.ja.md](refunds.ja.md)

A refund sends back what a payment received, to the address it came from, and this page says how
you open one and sign it.

It is written for the developer integrating it. It assumes you can already open a payment and
read it back, as [api.md](api.md) describes.

## Terms

| Term | Meaning |
|---|---|
| refund | A record that sends back what a payment received, to the address it came from |
| signing page | The page you sign a refund on, served by suco. Its URL is `refund_url` |
| signing data | The typed data the signing page hands the wallet the payment was paid to |
| refund address | Where a refund sends the money back. suco reads it off the transfer that paid the payment |
| token | The string in `refund_url`. Whoever has it is admitted to the page |
| what is left | What a payment can still be refunded, which is `received` less `refunded` |
| `nonce` | The one value in the signing data. The asset honours it once |
| module | The script and style that talk to a wallet |

## Refund flow on the page

1. Your server opens a refund with `POST /payments/{id}/refunds`. The answer carries
   `refund_url`.
2. You open `refund_url`. The page reads the refund from `GET /refund/{token}/state` and shows
   it.
3. The wallet the payment was paid to signs the EIP-712 authorisation the page hands it.
4. You send the transaction.
5. suco reads the chain and finds the transfer. The same reading that settled the payment settles
   this.
6. The chain settles the transfer, the refund reaches `succeeded`, and the money is back with
   whoever paid.

A refund is the payment with the direction reversed. suco holds no money and sends no
transaction.

## Refund states

| `status` | Entered when | Leaves to | Final |
|---|---|---|---|
| `created` | `POST /payments/{id}/refunds` opened the refund. It is signable, and nothing has settled | `succeeded`, `awaiting_finality` | no |
| `awaiting_finality` | The refund passed its deadline, and whether a transfer already sent settles is not decided | `succeeded`, `expired` | no |
| `succeeded` | The chain settled the transfer, and the money is back | nothing | yes |
| `expired` | The refund passed its deadline and nothing settled | nothing | yes |

A refund never goes back. A transfer that is seen for it moves nothing by being seen, and the
chain settling one is what moves the refund.

| Event | When |
|---|---|
| `refund.succeeded` | The chain settled the transfer |
| `refund.expired` | The deadline passed and nothing settled |

The body is the refund as a read of it answers, less `refund_url`. Nothing is sent for `created`
or `awaiting_finality`, and a read says when a refund is at one of them.
[webhooks.md](webhooks.md) has the rest.

## What the page shows

The amount and the asset. The wallet that has to sign, and where the money goes back to. The
payment the refund is against. The deadline. The status. And the transfer seen for the refund,
once one has been.

## Integrating

### The wallet that signs

The wallet a payment is paid to has to be able to sign EIP-712 typed data, because that wallet is
what signs the refund. `suco asset accept <name> <address>` records that address. An address
nobody can sign for takes payments and returns none.

### Opening a refund

```http
POST /payments/{id}/refunds
Content-Type: application/json

{"amount": "250"}
```

`amount` is in the asset's units, written as a string, and is the only key the body may carry.
Leave the body empty, or send `{}`, to send back everything the payment has left.

Only a payment that is `succeeded` can be refunded. What arrived is taken back when a transfer
stops being on the chain, and `succeeded` is the one status that keeps it.

The answer is `201`, with `Location` at the refund and the refund as the body.

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

| Field | Type | Description |
|---|---|---|
| `id` | string | What names the refund on the route that reads it back |
| `payment` | string | The payment the refund is against |
| `status` | string | Where the refund has got to. The states are above |
| `amount` | string | What is being sent back, in the asset's units |
| `destination` | string | Where the money goes back to |
| `expires_at` | string | When the refund stops being signable, UTC |
| `created_at` | string | When the refund was opened, UTC |
| `transfer` | object or null | The transfer suco matched to the refund, in the shape [api.md](api.md) gives. `null` until one has been seen, and a candidate until `status` is `succeeded` |
| `refund_url` | string | Where you sign. Left out of a webhook event |

`GET /payments/{id}/refunds/{refund}` reads one back, and answers the same body. There is no
route that lists them, so keep the identifier the answer gives, or read it off the webhook event.

`Idempotency-Key` is read here as it is on `POST /payments`, and under the same rules
([api.md](api.md)). A key names one request, and the payment is part of that request even though
the path rather than the body carries it: a key this account used on another payment is refused,
with a problem naming the header. The same key and body sent to the same payment again are
answered with the refund that key opened.

`refund_url` is `<listen.base_url>/refund/<token>`, and the token is what admits whoever opens
it. Whoever has it can read the refund, so keep it out of your logs, as suco keeps it out of its
own and out of webhook events. The field itself is answered to the account's own credentials and
to nobody else.

### Where the money goes

`destination` is not the caller's to choose. suco reads it off the transfer that paid the payment
and copies it onto the refund when the refund is opened, so that a later reading of the chain
cannot move where a signature sends money. A payment nothing has been seen for has nowhere to
send a refund, and opening one is refused.

### How much is left

A payment can be refunded up to what arrived, counting every refund of it that has not expired. A
refund holds its amount from the moment it is opened, whether or not anyone has signed it.

`GET /payments/{id}` answers `refunded`, the total those refunds hold, in the asset's units.

```json
{"amount": "1000", "received": "1000", "refunded": "250"}
```

What can still be refunded is `received` less `refunded`. Asking for more is refused, and the
refusal says what is left. A refund that expires gives its amount back. One whose key was already
spent on the chain out of the payment's destination does not expire at all, whatever the rules
made of that transfer.

### Failures

The shape of a failure, and every rule a request is held to, are in [api.md](api.md). What a
refund adds to them:

| Status | `error` | When | What to do |
|---|---|---|---|
| 400 | `invalid` | The payment is not `succeeded`, nothing has been seen for it, the amount is more than it has left, or `Idempotency-Key` was used to refund another payment. `problems` says which | Fix what `problems` names, and send the request again |
| 404 | `not_found` | No payment or refund of the account under that identifier | Check the identifier, and that the credential is of the account that opened the payment |
| 413 | `too_large` | The body is over 64 KiB | Send `amount` alone, or an empty body |
| 503 | `unavailable` | suco could not reach its database | Ask the operator, who has [operating.md](operating.md) |

## Page routes

The token in the path admits whoever calls the routes under `/refund/`, and no credential is
asked for. Their answers carry the headers and the content security policy [api.md](api.md)
lists. The script and style under `/refund-assets/` take no token, and carry
`X-Content-Type-Options: nosniff` alone.

| Path | Description |
|---|---|
| `GET /refund/{token}` | The page, as HTML |
| `GET /refund/{token}/state` | What the page shows, as JSON |
| `GET /refund-assets/{path}` | The page's script and style, in an instance that has them |

The routes the module will call are served all the same.

A token that leads nowhere is `404` on every route of the page, and each says it its own way:
`GET /refund/{token}` answers a page, `GET /refund/{token}/state` answers the failure body, and
`/refund-assets/` answers plain text.

### state

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

| Field | Description |
|---|---|
| `status` | Where the refund has got to |
| `payment` | Which payment this refund is against |
| `amount` | What is being sent back, in the asset's units |
| `asset` | The asset, with `chain_id` for the wallet to switch to |
| `from` | The wallet that has to sign, which is where the payment was paid |
| `to` | Where the money goes back to |
| `expires_at` | The refund's deadline |
| `authorization` | What you sign, and `null` when nothing can be signed |
| `result` | The transfer seen for the refund, or `null` |
| `reason` | Why nothing can be signed, and `null` while something can. The words are below |

`result` is the transfer as it is shown here: `tx`, `block_height`, `block_time`, `value` in the
asset's smallest unit, and `settling`, which is `true` until the refund succeeds.

It is the transfer that matched the refund. One that spent the refund's key and was not what the
refund authorised holds the refund open, as **How much is left** says, and is not answered here,
so such a refund sits at `awaiting_finality` with `result` still `null`.

| `reason` | Meaning |
|---|---|
| `done` | The refund has ended |
| `sent` | A transfer has been seen, and is waiting to settle |
| `expired` | Past the deadline |

## Signing

`authorization` is the EIP-712 typed data, less the parts a wallet fills in. It is what a payer
signs with three differences, and [checkout.md](checkout.md) describes the rest of it.

```json
{
  "domain": {"name": "JPY Coin", "version": "1", "chainId": 137,
             "verifyingContract": "0x431d…"},
  "message": {"from": "0xab…", "to": "0xcd…", "value": "250000000000000000000",
              "validAfter": "0", "validBefore": "1789561331", "nonce": "0x…"},
  "primaryType": "TransferWithAuthorization"
}
```

It carries `from`, because a payer may pay from wherever they hold the money and a refund can
only be signed by the wallet the payment was paid to. `validBefore` is the refund's `expires_at`
in seconds. There is no `id` and nothing to reissue. `nonce` is the one key this refund spends,
and the asset honours it once. Open another refund when a second key is needed.

`value` is in the asset's smallest unit here too, where `amount` is in its units.

## Deadlines

| Deadline | When | Once past |
|---|---|---|
| The refund's | `expires_at` | Nothing more can be signed. Open another refund |
| The page's | 30 days after the refund ends | The URL answers `404` |

A refund is good for 30 minutes from the moment it is opened, which is long enough to open the
page, reach a wallet, sign and send. The asset itself stops honouring the signature at the same
moment, so a transfer carried after it moves nothing.

## Security

The signing page is under the same rules as the payment page, which [checkout.md](checkout.md)
states: the module that talks to a wallet is not released, and the page refuses to be framed.
Until it is released, the page shows a refund and signs none. Without it, the plain page shows
the amount, the two addresses, the deadline and the status.

## Related

- [api.md](api.md): payments, the routes that open and read a refund, and `Idempotency-Key`
- [checkout.md](checkout.md): the payment page, and the typed data a signing page hands a wallet
- [webhooks.md](webhooks.md): what suco sends your server when a refund settles or expires
