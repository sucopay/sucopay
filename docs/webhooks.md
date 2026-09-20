# Webhooks

日本語: [webhooks.ja.md](webhooks.ja.md)

suco Pay sends an HTTP `POST` when a payment or refund changes. This guide covers endpoint
registration, signature verification, idempotent processing, retries, and delivery diagnostics.

You need a public HTTPS receiver and a read-write API token. Create a test payment first by
following the [API reference](api.md).

## Integration sequence

1. Register the receiver URL and store the signing secret in your secret manager.
2. Verify each request with a Standard Webhooks library.
3. Persist each `webhook-id` before returning `2xx`, then process the event asynchronously.
4. Send a test event and inspect its delivery record.
5. Subscribe only to the event types your application handles.

## Receiver requirements

1. **Verify the signature with a Standard Webhooks library** for your language. Do not implement
   signature verification yourself. Allow 5 minutes of tolerance on the timestamp. The headers are:
   `webhook-id`, `webhook-timestamp` and `webhook-signature`. The secret is the `whsec_…`
   value returned once during registration.
2. **Process each delivery once, keyed by `webhook-id`.** The id is the same on every attempt at
   one delivery, retries and manual resends included.
3. **Persist or enqueue the event, then return `2xx`.** Complete both within 20 seconds,
   including connection time. Process business logic asynchronously. A later response counts as
   a failed attempt and is retried.
4. **Fulfil the order only after `payment.succeeded`.** `attempt.confirming` means a transfer
   was detected, but that transfer may never settle.
5. **Mark the order expired only after `payment.expired`.** Do not decide from your own clock.
   A transfer submitted before the deadline may not have appeared on chain yet.
6. **Act on `data.status`, not arrival order.** Retries and manual resends can deliver an older
   event after a newer one. When in doubt, read `GET /payments/{id}`.
7. **Exempt the webhook path from CSRF protection.** Webhook requests use signatures rather than
   browser cookies or form tokens, so conventional CSRF middleware may reject them.
8. **Protect the signing secret.** Store it as a secret, never log it, and rotate it if it is
   lost or exposed. A rotation response shows the new value once.

## Event

An event records one payment or refund change. Sending an event to one endpoint creates a
delivery; each HTTP `POST` for that delivery is an attempt. Every event body has the following
shape.

```json
{
  "type": "payment.succeeded",
  "timestamp": "2026-09-16T01:23:45Z",
  "account": "3f9c2c1e-6a1b-4a1e-9f4e-2f0f2c5a7b11",
  "data": { "id": "…", "status": "succeeded", "…": "…" }
}
```

| Field | Type | Description |
|---|---|---|
| `type` | string | Event type, as listed below |
| `timestamp` | string | Event time in RFC 3339 format and UTC |
| `account` | string | The account the payment or refund belongs to |
| `data` | object | The payment or refund object returned by the API |

For a `payment.` or `attempt.` event, `data` contains the payment returned by
`GET /payments/{id}`, including `metadata`. It excludes `checkout_url` because that URL grants
access to the payment outcome and must not appear in receiver logs. For a `refund.` event,
`data` contains the refund returned by `GET /payments/{id}/refunds/{refund}`, without
`refund_url`. [Refunds](refunds.md) lists its fields.

Fields are added and never removed or renamed. Read the fields you need and ignore the rest.

| `type` | When | `data` |
|---|---|---|
| `payment.awaiting_payment` | The payment became payable | the payment |
| `attempt.confirming` | suco Pay detected a transfer but it has not reached finality | the payment |
| `payment.succeeded` | The payment transfer reached finality | the payment |
| `payment.expired` | The payment expired without a qualifying transfer | the payment |
| `payment.failed` | The payment cannot settle | the payment |
| `refund.succeeded` | The refund transfer reached finality | the refund |
| `refund.expired` | The refund expired without a settled transfer | the refund |
| `endpoint.test` | You called `POST /webhook_endpoints/{id}/test` | `{}` |

After suco Pay detects a transfer, `data.transfer` contains `tx`, `block_height`, `block_hash`,
`block_time`, `from`, and `value`. You can verify these fields against your own node.
`attempt.confirming` is the first event that includes the transfer, but it does not mean the
payment is final: a chain reorganisation can remove it. Wait for `payment.succeeded`. The
`value` unit differs from `amount` and `received`; see [API](api.md#transfer).

## Register an endpoint

```http
POST /webhook_endpoints
Authorization: Bearer <token>
Content-Type: application/json

{"url": "https://shop.example/webhooks/suco", "description": "orders", "events": ["payment.succeeded", "payment.expired"]}
```

| Parameter | Type | Required | Description |
|---|---|---|---|
| `url` | string | yes | HTTPS URL of at most 2048 bytes, without a username or password. URLs resolving to private deployment addresses are rejected |
| `description` | string | no | A note to yourself, at most 200 bytes |
| `events` | array of string | no | The types to receive. Left out, the endpoint receives every type, including types added later |

The response is `201` and includes the endpoint and its `secret`. The API returns the secret only
once, so store it before discarding the response. An account can have up to 8 endpoints.

A disabled endpoint (`"enabled": false`) receives no new deliveries. Pending deliveries remain
queued and resume when the endpoint is enabled again.

## Endpoint routes

| Path | Credential | Description |
|---|---|---|
| `POST /webhook_endpoints` | read-write | Register an endpoint and receive its secret |
| `GET /webhook_endpoints` | read-only | List the endpoints, without secrets |
| `GET /webhook_endpoints/{id}` | read-only | Read one endpoint |
| `PATCH /webhook_endpoints/{id}` | read-write | Change `url`, `description`, `events` or `enabled` |
| `POST /webhook_endpoints/{id}/secret` | read-write | Rotate the secret. The old one verifies for 24 more hours |
| `DELETE /webhook_endpoints/{id}` | read-write | Remove the endpoint. Its pending deliveries become `failed` |
| `POST /webhook_endpoints/{id}/test` | read-write | Send one `endpoint.test`. Returns `202` with the delivery ID. A new test is rejected while the previous one is pending |
| `GET /webhook_endpoints/{id}/deliveries` | read-only | The newest 100 deliveries and every attempt at each |
| `POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` | read-write | Send a delivered or failed delivery again. Answers `202` |

For 24 hours after rotation, `webhook-signature` contains two space-separated signatures: one
for the previous secret and one for the new secret. The library can verify either signature.

## Retries

An attempt that gets no `2xx` is tried again, with up to 10% added at random to each wait.

| Attempt | Wait before it |
|---|---|
| 2 | 5 seconds |
| 3 | 5 minutes |
| 4 | 30 minutes |
| 5 | 2 hours |
| 6 | 5 hours |
| 7 | 10 hours |
| 8 | 14 hours |
| 9 | 20 hours |
| 10 | 24 hours |

After the tenth attempt, slightly more than three days after the first, the delivery becomes
`failed` and is not retried automatically. suco Pay does not follow `3xx` redirects; they count
as failed attempts.

Deliveries to one endpoint about one payment are sent in the order the events happened. A later
delivery waits until the earlier one is delivered or failed. Deliveries about different payments
do not wait for each other.

Repeated failures do not disable an endpoint. Inspect the delivery log for the cause;
`suco doctor` also reports the number of failed and pending deliveries.

## Delivery log

`GET /webhook_endpoints/{id}/deliveries` returns the endpoint's 100 newest deliveries and every
attempt for each delivery.

```json
{
  "deliveries": [
    {
      "id": "…", "type": "payment.succeeded", "payment": "…",
      "occurred_at": "2026-09-16T01:23:45Z", "state": "pending",
      "attempts": [
        {"at": "2026-09-16T01:23:46Z", "status": 500, "response": "Internal Server Error", "took_ms": 120},
        {"at": "2026-09-16T01:23:51Z", "status": null, "reason": "timeout", "response": "", "took_ms": 20000}
      ],
      "next_at": "2026-09-16T01:28:51Z", "delivered_at": null
    }
  ]
}
```

| Field | Type | Description |
|---|---|---|
| `id` | string | The delivery's id, sent as `webhook-id` |
| `type` | string | The event's type |
| `payment` | string or null | The payment the event is about. `null` for `endpoint.test` |
| `occurred_at` | string | When the event happened |
| `state` | string | `pending`, `delivered` or `failed` |
| `attempts` | array | Every attempt at the delivery, oldest first |
| `attempts[].at` | string | When the attempt was made |
| `attempts[].status` | integer or null | HTTP response status, or `null` when no response was received |
| `attempts[].reason` | string | Reason no response was received. Present only when `status` is `null` |
| `attempts[].response` | string | First 256 bytes of the response body |
| `attempts[].took_ms` | integer | How long the attempt took, in milliseconds |
| `next_at` | string or null | When the next attempt is due, while the delivery is pending |
| `delivered_at` | string or null | When a `2xx` was received |

| `reason` | Meaning |
|---|---|
| `timeout` | No response within 20 seconds |
| `connection` | The connection could not be made, or was lost |
| `destination` | The URL no longer passes the check made at registration |
| `secret` | The deployment can no longer read the secret to sign with. See below |

Delivered and failed deliveries are kept for 30 days.

## Manual resend

`POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` sends a delivered or failed delivery
again. It keeps the same `webhook-id`, uses a fresh `webhook-timestamp` and signature, and appends
to the existing attempt history. Use it after restoring an unavailable receiver or recovering
from data loss after a `2xx` response. An idempotent receiver discards a resend it already
persisted. Pending deliveries cannot be resent because automatic delivery is still in progress.

## Key rotation by the operator

Secrets are stored encrypted under a key derived from the deployment's `credentials.key`. If the
operator replaces that key, existing secrets cannot be read. Deliveries to your endpoint are
then recorded with the reason `secret`, and `suco doctor` reports the affected endpoints.
Rotating the endpoint secret (`POST /webhook_endpoints/{id}/secret`) encrypts a new value with
the current key; delivery resumes on the next attempt.

## Next steps

- Add `refund.succeeded` and `refund.expired` handling when you implement
  [Refunds](refunds.md).
- Give operators the [delivery diagnostics](operating.md#suco-doctor) for investigating failed
  webhooks.
