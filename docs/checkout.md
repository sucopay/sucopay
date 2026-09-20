# Checkout

日本語: [checkout.ja.md](checkout.ja.md)

suco Checkout is the payer-facing page for a payment. This guide explains how to send a payer to
Checkout, return them to your site, and use the page API.

You need to be able to create a payment as described in the [API reference](api.md).

## Availability

> **Pre-alpha:** the browser module that connects Checkout to a wallet has not been released.
> The current page displays payment details but cannot request a signature or submit a
> transaction. Use `suco payment await <id>` to test the signing flow.

The page API and payment state model described below are available now. Check the
[roadmap](../ROADMAP.md) before planning a production integration.

## Payment flow on the page

1. Your server creates a payment and redirects the payer to its `checkout_url`.
2. Checkout reads and displays the payment with `GET /checkout/{token}/state`.
3. Checkout requests a payment attempt from `POST /checkout/{token}/attempts`.
4. The payer's wallet signs an EIP-3009 `TransferWithAuthorization` for the exact amount and
   submits the transaction. The payer pays the gas.
5. suco Pay detects the transfer and waits for it to settle. Checkout displays the current
   [payment status](api.md#payment-states).
6. If the payment has a `return_url`, Checkout sends the payer back to it.

The payer needs a wallet that can sign EIP-712 typed data on the payment's network. Checkout
checks the network, balance, and transfer eligibility through the wallet. suco Pay does not
receive the payer's private key.

## What the page shows

Checkout displays the merchant name, amount, asset, deadline, status, return link, and any
transfer detected for the payment.

Checkout does not display `metadata` or the destination address. The destination is part of the
typed data sent to the wallet; the page does not present it as an address for a manual exchange
withdrawal.

## Redirect and return behavior

`POST /payments` returns `checkout_url`, and `GET /payments/{id}` returns the same URL. Redirect
the payer to it or provide a link. Its form is `<listen.base_url>/checkout/<token>`. The token in
the path grants access, so the page does not request an API credential.

Each payment has one Checkout URL. If you lose it, retrieve the payment with
`GET /payments/{id}`. A payment created before the instance could serve pages has no
`checkout_url`.

> **Important:** anyone with `checkout_url` can read the payment outcome. Send it only to the
> payer, and keep it out of logs and analytics.

`return_url` in `POST /payments` is the destination for payers who finish the flow or cannot
continue, for example because the payment expired or their wallet is unsupported. It is
optional. The URL must use `https`, contain no username or password, and be at most 2048 bytes.
For local development, `http://localhost` and `http://127.0.0.1` are also accepted.

suco Pay does not add query parameters or send data to `return_url`. A browser arriving there
does not prove that payment succeeded. Confirm the result on your server through a webhook or
`GET /payments/{id}`.

## Page routes

Routes under `/checkout/` use the token in the path instead of an API credential. Responses use
the security headers and Content Security Policy listed in the
[API route summary](api.md#route-summary). Assets under `/checkout-assets/` are public and include
only `X-Content-Type-Options: nosniff`.

| Path | Description |
|---|---|
| `GET /checkout/{token}` | Return the Checkout HTML. An invalid token returns a `404` page |
| `GET /checkout/{token}/state` | Return the data displayed by Checkout as JSON |
| `POST /checkout/{token}/attempts` | Create or retrieve the payer's signing data |
| `GET /checkout-assets/{path}` | Return a Checkout script or stylesheet when the instance includes the browser module |

### state

```json
{
  "status": "awaiting_payment",
  "amount": "1000",
  "asset": {"symbol": "JPYC", "decimals": 18, "network": "polygon", "chain_id": 137},
  "expires_at": "2026-09-18T10:15:00Z",
  "merchant": {"name": "Example Shop"},
  "return_url": "https://shop.example/orders/42",
  "attempt": null,
  "result": null,
  "reason": null,
  "slower": false
}
```

| Field | Description |
|---|---|
| `status` | `awaiting_payment`, `awaiting_finality`, `succeeded`, `expired`, or `failed`. Checkout presents `created` as `awaiting_payment` because both states require the payer to begin payment |
| `amount`, `asset` | The payment amount and asset, plus `chain_id` for selecting the wallet network |
| `expires_at` | The payment's deadline |
| `merchant.name` | The account's name |
| `return_url` | Where you want the payer sent, or `null` |
| `attempt` | The signing material of the live attempt, or `null` |
| `result` | The transfer seen for the payment, or `null` |
| `reason` | Why Checkout cannot create an attempt, or `null` when it can. Values are listed below |
| `slower` | `true` when network reads are delayed and confirmation may take longer than usual |

`result` includes `tx`, `block_height`, `block_time`, `value` in the asset's smallest unit,
`confirming`, and `received_at`. `confirming` remains `true` until the payment succeeds;
`received_at` is set when it succeeds.

| `reason` | Meaning |
|---|---|
| `expired` | The deadline passed |
| `closing` | Under two minutes remain, which is less than a signature takes |
| `paid` | A transfer was seen, and is waiting to be settled |
| `done` | The payment ended |
| `not_ready` | The instance has not read the payment's network yet |
| `reissued` | The payment has already used its single allowed replacement attempt |
| `again` | Concurrent attempt requests left no live attempt to return. Retry the request. Only the attempts route returns this value |

### attempts

Send an empty body to create or retrieve the current attempt. To replace it, send
`{"reissue": "<attempt_id>"}`. Any other body returns `400`.

1. If a live attempt exists and `reissue` is absent, the endpoint returns it with `200`.
2. If `reissue` does not identify the live attempt, the endpoint returns `400`.
3. If Checkout cannot create an attempt, the endpoint returns
   `409 {"error": "<reason>"}` without changing the payment.
4. Otherwise, the endpoint creates an attempt and returns `201`. A `created` payment changes to
   `awaiting_payment` first.

Reloading Checkout does not create another attempt. Later requests return the existing live
attempt.

### Failures

| Status | `error` | When | What to do |
|---|---|---|---|
| 400 | `invalid` | The body of `attempts` is neither empty nor a `reissue` | Send an empty body, or `{"reissue": "<attempt_id>"}` |
| 404 | `not_found` | The token is invalid, unknown, or no longer retained. The HTML route returns a `404` page instead | Compare the token with the `checkout_url` returned by `GET /payments/{id}` |
| 409 | the `reason` | Checkout cannot create an attempt | Follow the action implied by the `reason` listed above |
| 413 | `too_large` | The `attempts` body exceeds 1 KiB | Send an empty body or one `reissue` identifier |
| 503 | `unavailable` | suco Pay cannot reach its database | Follow the [operations guide](operating.md) |

## Signing

The attempt contains the typed data passed to `eth_signTypedData_v4`, except for `from`, which
Checkout fills with the payer's address. Keys under `domain` and `message` follow the standard's
camelCase names. `id` identifies the attempt for display and `reissue`; it is not signed.

```json
{
  "id": "…",
  "domain": {"name": "JPY Coin", "version": "1", "chainId": 137,
             "verifyingContract": "0x431d…"},
  "message": {"to": "0xab…", "value": "1000000000000000000000",
              "validAfter": "0", "validBefore": "1789560000", "nonce": "0x…"},
  "primaryType": "TransferWithAuthorization"
}
```

`value` uses the asset's smallest unit, unlike `amount`. `validBefore` is the payment deadline as
a Unix timestamp. `nonce` prevents reuse. `domain` comes from the asset's `eip712` configuration
and is checked against the contract by `suco asset accept`.

Each `nonce` can be used only once. If a wallet consumed it in a transaction that did not reach
suco Pay, Checkout can request one replacement attempt per payment. If that replacement is also
unusable, Checkout returns the payer to the merchant.

## Deadlines

| Deadline | When | Once past |
|---|---|---|
| Payment deadline | `expires_at` | Checkout stops creating attempts, reports the expiry, and continues to show the result of any submitted transfer |
| Page retention | 30 days after the payment reaches a final state | The URL returns `404` |

A payment ends at `succeeded`, `expired` or `failed`. The page is readable until then and for 30
days after, so that a payer who kept the URL can read the outcome. A payment that has not ended
has no such limit, and reaches `expired` or `succeeded` after its deadline.

## Security

Checkout refuses to load in an iframe. Open it as a top-level page. The page routes also use the
security headers listed in the [API route summary](api.md#route-summary).

## Next steps

- Implement [Webhooks](webhooks.md) so your server can confirm the final payment status.
- After the payment flow works end to end, add [Refunds](refunds.md).
