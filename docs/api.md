# API

日本語: [api.ja.md](api.ja.md)

Use the HTTP API to create a payment, retrieve its status, and start a refund. This reference is
for developers integrating a merchant server with suco Pay.

## Before you start

Complete the [getting-started guide](../README.md#getting-started), then make sure you have:

- a running `suco serve` instance;
- an asset and receiving wallet registered with `suco asset accept`; and
- a read-write API token created with `suco credential new --read-write`.

Every path below is relative to `listen.base_url`. The default for local development is
`http://localhost:7826`.

> **Pre-alpha:** the browser module that connects Checkout to a wallet has not been released.
> Use `suco payment await <id>` to exercise the payment flow for now.

## Payment flow

1. Your server creates a payment with `POST /payments`.
2. The response includes a `checkout_url`. Send the payer to that URL.
3. Checkout creates a payment attempt containing the EIP-712 typed data to sign. During
   pre-alpha, run `suco payment await <id>` instead.
4. The payer's wallet signs an EIP-3009 `TransferWithAuthorization` for the exact amount and
   submits the transaction.
5. suco Pay detects the transfer and includes it in the payment's `transfer` field while it
   waits for finality.
6. The payment changes to `succeeded` after the transfer settles. Fulfil the order only after
   you read that status from the API or receive a `payment.succeeded` webhook.

[Checkout](checkout.md) covers the payer experience. [Webhooks](webhooks.md) covers reliable
server-side confirmation.

## Payment states

| `status` | Entered when | Leaves to | Final |
|---|---|---|---|
| `created` | The API created the payment | `awaiting_payment` | no |
| `awaiting_payment` | Checkout created the first payment attempt, or an operator ran `suco payment await` | `succeeded`, `failed`, `awaiting_finality` | no |
| `awaiting_finality` | The payment deadline passed while a submitted transfer may still settle | `succeeded`, `expired` | no |
| `succeeded` | A transfer for the payment reached finality | — | yes |
| `failed` | Reserved for a payment that cannot settle. This release does not transition payments into this state | — | yes |
| `expired` | No qualifying transfer arrived before processing passed the payment deadline | — | yes |

A detected transfer is not final. It can disappear in a chain reorganisation. The payment
reaches `succeeded` only after the transfer settles.

A transfer submitted just before the deadline may arrive after it. In that case, the payment
uses `awaiting_finality` while suco Pay determines whether the transfer settles. Do not close an
order based on your own clock.

## Authentication

Send an API token in every authenticated request:

```http
Authorization: Bearer <token>
```

Create tokens with `suco credential new`. Each token belongs to one account and has read-only or
read-write access. An instance currently supports one account.

Routes that change data require read-write access. Read-only routes accept either access level.
A missing or inactive token returns `401`; a token without the required access returns `403`.

## Assets and amounts

An asset is one token on one network, such as JPYC on Polygon. Its network and `reference`
identify it; on an EVM network, the reference is the token contract address. Do not use the
display symbol as an identifier because symbols are not unique.

`suco.yaml` assigns each configured asset a local name. Use that name in requests. A stored
payment keeps the network and reference, so later configuration changes do not change the asset
on an existing payment.

API amounts use the asset's display unit. For example, `"1000"` means 1000 JPYC. Chain transfer
values use the smallest unit; with 18 decimals, 1 JPYC is 1000000000000000000 smallest units.

Amounts are JSON strings to avoid floating-point rounding.

## POST /payments

Creates a payment for the account associated with the API token.

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
| `asset` | string | yes | The asset name from `suco.yaml`. Register it for the account first with `suco asset accept <name> <address>` |
| `amount` | string | yes | Amount in the asset's display unit; for example, `"1000"` means 1000 JPYC. Use an unsigned decimal without an exponent. It must be greater than zero, use no more fractional digits than `decimals`, and contain at most 78 digits after conversion to the smallest unit |
| `expires_at` | string | no | An RFC 3339 timestamp later than the current time and no more than 30 days ahead. Defaults to 15 minutes from creation |
| `return_url` | string | no | URL to which Checkout returns the payer. It must use `https`, contain no username or password, and be at most 2048 bytes. `http://localhost` and `http://127.0.0.1` are also accepted. See [Checkout](checkout.md) |
| `metadata` | object | no | Up to 20 string keys and string values. Keys may be 64 bytes and values 512 bytes. The API returns the object unchanged, defaults it to `{}`, and excludes it from suco Pay logs |

Unknown keys return `400`. The maximum request body is 64 KiB.

The response is `201`. `Location` contains the payment path, and the body contains the payment.

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
| `id` | string | Payment identifier used in other API paths |
| `status` | string | The current payment state, as listed above |
| `asset` | object | Token identity (`network` and `reference`) and display properties (`symbol` and `decimals`). The local name from `suco.yaml` is not returned |
| `amount` | string | Requested amount in the asset's display unit |
| `received` | string or null | Amount received in the asset's display unit, or `null` until a transfer is detected |
| `destination` | string | Receiving address registered by `suco asset accept` |
| `metadata` | object | Metadata from the request, or `{}` when omitted |
| `expires_at` | string | UTC timestamp after which the payment is no longer payable |
| `created_at` | string | UTC timestamp at which the payment was created |
| `return_url` | string or null | URL to which Checkout returns the payer, or `null` when omitted |
| `checkout_url` | string | URL to send to the payer. Omitted for payments created before the instance started serving Checkout pages |
| `transfer` | object or null | Transfer associated with the payment, as described below |
| `refunded` | string | Amount reserved by refunds, in the asset's display unit. `"0"` until a refund is created |

Anyone with `checkout_url` can read the payment result. Treat the URL as a secret and keep it out
of logs. suco Pay also excludes it from its own logs and webhook events.

## GET /payments/{id}

Returns one payment for the authenticated account. A successful response is `200` with the same
object shape as `POST /payments`.

An invalid ID, a missing payment, and a payment owned by another account all return the same
`404` response.

## Refunds

A refund returns funds to the address that paid the original transfer. You sign it with the
receiving wallet; suco Pay records the signed transfer and monitors it for finality. See
[Refunds](refunds.md) for the complete flow and state model.

`POST /payments/{id}/refunds` accepts `amount` in the asset's display unit. Omit it to refund the
remaining balance. The caller cannot choose the destination; suco Pay uses the source address of
the payment transfer. The endpoint supports `Idempotency-Key` under the rules below.

The response includes `refund_url`, where the operator signs the refund. Anyone with this URL can
read the refund. Keep it out of logs. suco Pay also excludes it from its logs and webhook events.

## Idempotency-Key

Use `Idempotency-Key` to prevent a retry from creating a second payment or refund.

```http
Idempotency-Key: 8e03978e-40d5-43e8-bc93-6894a57f9324
```

Generate a random key for each new request; a UUID is a common choice. A quoted header value is
accepted, and the surrounding quotes are not part of the key. The value must contain 1 to 255
bytes of printable ASCII, without a quote or backslash. Send the header once. Duplicate
`Idempotency-Key` headers return `400`.

Repeating a key returns the resource created by the original request: `201`, the same `Location`,
and `Idempotent-Replayed: true`. The body contains the resource's current state, not a copy of the
original response.

The retried body must match the original body byte for byte. Reordering JSON keys changes the
body. Reusing a key with a different body returns `400` with a problem for `Idempotency-Key`.
Retry with the original body or use a new key. suco Pay uses `400` rather than the `422` suggested
by the IETF draft.

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

suco Pay retains the key for as long as the resource it created. A rejected request does not
reserve the key, so you can correct the body and retry. If two requests with the same key arrive
concurrently, one waits and returns the resource created by the other.

The key is read on `POST /payments` and on `POST /payments/{id}/refunds`, and nowhere else.
If a request without a key does not receive a response, search for the resource using your own
`metadata` before retrying.

## transfer

`transfer` describes the on-chain transfer associated with the payment. It is `null` until suco
Pay detects a qualifying transfer.

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
| `tx` | string | Transaction hash used to look up the transfer on a node |
| `block_height` | integer | Height of the block containing the transfer |
| `block_hash` | string | Hash of that block |
| `block_time` | string | Block timestamp in UTC |
| `from` | string | Source address of the transfer and destination of any refund |
| `value` | string | Transferred amount in the asset's smallest unit |

Until `status` is `succeeded`, the transfer is only a candidate. If a chain reorganisation removes
it, `transfer` returns to `null`.

`value` uses the asset's smallest unit, while `amount` and `received` use its display unit. For
an 18-decimal asset, the same amount is `"1000"` in `amount` and
`"1000000000000000000000"` in `value`.

The object does not repeat the network or destination. Read them from `asset.network` and
`destination` on the payment.

A transfer below the requested amount does not complete the payment. Its value contributes to
`received`, but `transfer` remains `null`.

## Failures

All API errors use the following response shape.

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
| 400 | `invalid` | The request body, a field value, or `Idempotency-Key` is invalid | Correct the fields listed in `problems`, then retry |
| 401 | `unauthorized` | The API token is missing or inactive | Send an active token |
| 403 | `forbidden` | The token does not have the access level required by the route | Send a read-write token for write operations |
| 404 | `not_found` | The account has no payment, refund, or webhook endpoint with that identifier | Check the identifier and the account associated with the token |
| 409 | reason-specific | Checkout cannot create a payment attempt | See [Checkout](checkout.md) for the possible reasons. Only `POST /checkout/{token}/attempts` returns this status |
| 413 | `too_large` | The request body exceeds 64 KiB | Send a smaller body |
| 503 | `unavailable` | suco Pay cannot reach the database | Check the service logs and follow the [operations guide](operating.md) |

Only `400` responses include `problems`.

### Problems

Each entry uses `field` to identify the invalid body key or header and `message` to explain the
constraint. A problem with the entire body omits `field`.

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

Values repeated in a message are truncated to 128 bytes. Both `field` and `message` are truncated
to 512 bytes. If either contains a control or invisible character, the value is quoted and the
character is escaped.

Validation runs in stages. The response includes all problems from the first failing stage, then
stops. Header syntax is checked in the first stage, so header and body-shape problems may appear
together.

1. Body structure: unknown keys, incorrect types, an asset absent from `suco.yaml`, malformed
   amounts or timestamps, and missing required keys.
2. Whether the account accepts the asset.
3. Value constraints: a zero amount, an expiry outside the accepted range, and metadata over its
   limits.

The response lists every problem found in the first failing stage, allowing the client to fix
that stage in one retry.

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

## Route summary

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

Page routes use the token in the path instead of an API credential. Responses under `/checkout/`
and `/refund/` include `Cache-Control: no-store`, `Referrer-Policy: no-referrer`,
`X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and a Content Security Policy that
allows connections only to the same instance and prevents framing. They do not include CORS
headers because only the page itself calls them.

Scripts and styles under `/checkout-assets/` and `/refund-assets/` are public and cacheable. They
include `X-Content-Type-Options: nosniff`.

Webhook routes return the same `404` response for an invalid ID, a missing endpoint, and an
endpoint owned by another account.

## Next steps

1. Send the payer to the payment page by following [Checkout](checkout.md).
2. Confirm payment outcomes on your server by implementing [Webhooks](webhooks.md).
3. After successful payments work end to end, add [Refunds](refunds.md).
