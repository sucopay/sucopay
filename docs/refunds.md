# Refunds

日本語: [refunds.ja.md](refunds.ja.md)

Refunds return all or part of a settled payment to the address that sent it. This guide covers
creating, signing, and tracking a refund.

You need a `succeeded` payment and access to the wallet registered as its destination. The
wallet must support EIP-712 typed-data signing.

## Availability

> **Pre-alpha:** the browser module that connects the signing page to a wallet has not been
> released. The current page displays refund details but cannot sign or submit a transaction.

The refund API, state tracking, and page API described below are available now. Check the
[roadmap](../ROADMAP.md) before planning a production integration.

## Refund flow on the page

1. Your server creates a refund with `POST /payments/{id}/refunds`.
2. The response includes `refund_url`. Open it for the operator who controls the receiving
   wallet.
3. The page loads the refund from `GET /refund/{token}/state`.
4. The receiving wallet signs the EIP-712 authorisation and submits the transaction.
5. suco Pay detects the transfer and waits for it to settle.
6. The refund reaches `succeeded`, and the funds return to the address that paid.

suco Pay does not hold funds or submit the refund transaction.

## Refund states

| `status` | Entered when | Leaves to | Final |
|---|---|---|---|
| `created` | The API created the refund and it is available to sign | `succeeded`, `awaiting_finality` | no |
| `awaiting_finality` | The deadline passed while a submitted transfer may still settle | `succeeded`, `expired` | no |
| `succeeded` | The refund transfer reached finality | — | yes |
| `expired` | No qualifying transfer settled by the deadline | — | yes |

Refund states do not move backwards. A detected transfer is not enough to complete a refund;
the refund reaches `succeeded` only after the transfer settles.

| Event | When |
|---|---|
| `refund.succeeded` | The chain settled the transfer |
| `refund.expired` | The refund expired without a settled transfer |

The event body contains the same refund object as the read endpoint, without `refund_url`. suco
Pay does not send events for `created` or `awaiting_finality`; retrieve the refund to inspect
those states. See [Webhooks](webhooks.md) for delivery behavior.

## What the page shows

The signing page displays the amount, asset, signing wallet, refund address, payment ID,
deadline, status, and any detected refund transfer.

## Create and track a refund

### The wallet that signs

The receiving wallet must support EIP-712 typed-data signing because it signs the refund.
`suco asset accept <name> <address>` registers that wallet. An address without a signing key can
receive payments, but you cannot refund them.

### Opening a refund

```http
POST /payments/{id}/refunds
Authorization: Bearer <token>
Content-Type: application/json

{"amount": "250"}
```

`amount` is a string in the asset's display unit and is the only supported field. To refund the
entire remaining balance, send an empty body or `{}`.

Only a `succeeded` payment can be refunded. That status confirms the payment transfer has
settled.

The response is `201`. `Location` contains the refund path, and the response body contains the
refund.

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
| `id` | string | The refund identifier used by the read endpoint |
| `payment` | string | Identifier of the payment being refunded |
| `status` | string | The current refund state, as listed above |
| `amount` | string | Refund amount in the asset's display unit |
| `destination` | string | Address that receives the refund |
| `expires_at` | string | UTC timestamp after which the refund can no longer be signed |
| `created_at` | string | UTC timestamp at which the refund was created |
| `transfer` | object or null | Transfer matched to the refund, using the shape described in [API](api.md#transfer). It remains `null` until a transfer is detected and is not final until `status` is `succeeded` |
| `refund_url` | string | URL at which the operator signs the refund. Webhook events omit it |

`GET /payments/{id}/refunds/{refund}` returns the same object. There is no list endpoint, so
store the refund ID from the create response or retrieve it from a webhook event.

The endpoint supports `Idempotency-Key` under the same rules as `POST /payments`; see
[API](api.md#idempotency-key). The payment ID is part of the idempotent request even though it is
in the path. Reusing a key for another payment returns `400`. Repeating the same key and body for
the same payment returns the original refund.

`refund_url` has the form `<listen.base_url>/refund/<token>`. The token in the path grants access,
so anyone with the URL can read the refund. Keep it out of logs. suco Pay also excludes it from
its logs and webhook events, and returns it only to the account that created the refund.

### Where the money goes

The caller cannot choose `destination`. suco Pay copies the source address from the payment
transfer when it creates the refund. Later chain reads cannot change where the signed refund
sends funds. If the payment has no transfer, it has no refund destination and the API rejects the
request.

### How much is left

A payment can be refunded up to its `received` amount. Creating a refund immediately reserves
that amount, even before anyone signs it.

`GET /payments/{id}` returns the total reserved by non-expired refunds in `refunded`, using the
asset's display unit.

```json
{"amount": "1000", "received": "1000", "refunded": "250"}
```

The refundable balance is `received - refunded`. A request above that balance returns `400` and
reports the available amount. An expired refund releases its reservation. If the refund's
`nonce` has already been used on chain by the receiving wallet, the reservation does not expire,
regardless of whether the resulting transfer matched the refund.

### Failures

The [API error reference](api.md#failures) defines the common response format. Refund endpoints
add the following cases:

| Status | `error` | When | What to do |
|---|---|---|---|
| 400 | `invalid` | The payment is not `succeeded`, has no transfer, has insufficient refundable balance, or the `Idempotency-Key` belongs to another payment | Correct the fields listed in `problems`, then retry |
| 404 | `not_found` | The account has no payment or refund with that identifier | Check the identifier and the account associated with the token |
| 413 | `too_large` | The body is over 64 KiB | Send `amount` alone, or an empty body |
| 503 | `unavailable` | suco Pay cannot reach its database | Follow the [operations guide](operating.md) |

## Page routes

Routes under `/refund/` use the token in the path instead of an API credential. Responses use the
security headers and Content Security Policy listed in the
[API route summary](api.md#route-summary). Assets under `/refund-assets/` are public and include
only `X-Content-Type-Options: nosniff`.

| Path | Description |
|---|---|
| `GET /refund/{token}` | Return the signing page as HTML |
| `GET /refund/{token}/state` | Return the data displayed by the signing page as JSON |
| `GET /refund-assets/{path}` | Return a signing-page script or stylesheet when the instance includes the browser module |

The API routes are available even when the browser module is not installed. An invalid token
returns `404`: the HTML route returns an error page, the state route returns the standard error
body, and the asset route returns plain text.

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
| `status` | The current refund state |
| `payment` | Identifier of the payment being refunded |
| `amount` | Refund amount in the asset's display unit |
| `asset` | Asset details, including `chain_id` for selecting the wallet network |
| `from` | Receiving wallet that must sign the refund |
| `to` | Address that receives the refund |
| `expires_at` | The refund's deadline |
| `authorization` | The EIP-712 typed data to sign, or `null` when signing is unavailable |
| `result` | The transfer seen for the refund, or `null` |
| `reason` | Why signing is unavailable, or `null` when it is available. Values are listed below |

`result` contains `tx`, `block_height`, `block_time`, `value` in the asset's smallest unit, and
`settling`. The `settling` field remains `true` until the refund succeeds.

Only a transfer that matches the refund appears in `result`. If another transfer spends the
refund's `nonce`, the reservation remains in place as described in
[How much is left](#how-much-is-left), but `result` remains `null` and the refund stays
`awaiting_finality`.

| `reason` | Meaning |
|---|---|
| `done` | The refund has ended |
| `sent` | A transfer has been seen, and is waiting to settle |
| `expired` | Past the deadline |

## Signing

`authorization` contains the EIP-712 typed data, excluding fields supplied by the wallet. It
differs from a payment authorization in the ways described below; [Checkout](checkout.md#signing)
documents the shared fields.

```json
{
  "domain": {"name": "JPY Coin", "version": "1", "chainId": 137,
             "verifyingContract": "0x431d…"},
  "message": {"from": "0xab…", "to": "0xcd…", "value": "250000000000000000000",
              "validAfter": "0", "validBefore": "1789561331", "nonce": "0x…"},
  "primaryType": "TransferWithAuthorization"
}
```

It includes `from` because only the wallet that received the payment can sign the refund.
`validBefore` is the refund's `expires_at` as a Unix timestamp. A refund has no attempt `id` and
cannot be reissued. Its `nonce` can be used once; create another refund if a new `nonce` is
required.

`value` is in the asset's smallest unit here too, where `amount` is in its units.

## Deadlines

| Deadline | When | Once past |
|---|---|---|
| Refund deadline | `expires_at` | The authorization can no longer be signed. Create another refund |
| Page retention | 30 days after the refund reaches a final state | The URL returns `404` |

A refund remains signable for 30 minutes after creation. At the same deadline, the asset
contract stops accepting the authorization, so a transaction submitted afterwards cannot move
funds.

## Security

The signing page refuses to load in an iframe. Open it as a top-level page. It uses the same
security headers as [Checkout](checkout.md#security).

## Next steps

- Subscribe to `refund.succeeded` and `refund.expired` as described in
  [Webhooks](webhooks.md).
- Add refund failures and signing-page expiry to your [operations runbook](operating.md).
