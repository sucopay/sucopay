# Webhooks

日本語: [webhooks.ja.md](webhooks.ja.md)

suco sends an HTTP `POST` to a URL you register whenever a payment or a refund changes. This
page says what your receiver has to do, and how endpoints and deliveries are managed over the
API.

It is written for the developer running the receiver. It assumes you can already create a
payment, as [api.md](api.md) describes, and that the same credential is at hand: registering an
endpoint, reading what was sent, and rotating a secret all take it.

## Terms

| Term | Meaning |
|---|---|
| endpoint | A URL you register over the API, which suco sends a `POST` to |
| receiver | Your program, which takes the `POST` at the endpoint's URL |
| signing secret | What your receiver verifies a signature with. Shown once, in the answer to the registration |
| event | One notice that a payment or a refund changed |
| delivery | One event sent to one endpoint |
| attempt | One `POST` of one delivery |
| retry | Another attempt suco makes at a delivery that got no `2xx` |
| manual resend | One more sending of a delivered or failed delivery, which you ask for |

## Receiver requirements

1. **Verify the signature with a Standard Webhooks library** for your language. Do not verify
   it by hand. Allow 5 minutes of tolerance on the timestamp. The headers are the standard's:
   `webhook-id`, `webhook-timestamp` and `webhook-signature`. The secret is the `whsec_…`
   string the registration answered with.
2. **Process each delivery once, keyed by `webhook-id`.** The id is the same on every attempt at
   one delivery, retries and manual resends included.
3. **Answer `2xx` first and do the work after.** You have 20 seconds, counting the connection.
   A slower answer counts as a failed attempt and is retried.
4. **Hand over the goods on `payment.succeeded` only.** `attempt.confirming` says money was
   seen, and nothing may follow it.
5. **Close the order on `payment.expired` only.** A deadline on your own clock is not the
   deadline. A transfer sent before the deadline may still be on its way.
6. **Act on `data.status`, not on the order events arrive in.** Retries and resends can put a
   later event before an earlier one. When in doubt, read `GET /payments/{id}`.
7. **Exempt the webhook path from CSRF protection.** The request carries no cookie and no form,
   so a CSRF check refuses it.
8. **Keep the secret.** It is shown once, in the answer to the registration. A lost secret is
   replaced by rotating it, which shows a new one once.

## Event

Every event is one `POST` with a JSON body of one shape.

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
| `type` | string | What happened. One of the types below |
| `timestamp` | string | When it happened, RFC 3339, UTC |
| `account` | string | The account the payment or refund belongs to |
| `data` | object | The payment or the refund, as the API answers it |

For a `payment.` or `attempt.` event, `data` is the payment as `GET /payments/{id}` answers it,
`metadata` included. `checkout_url` is left out: whoever has that URL can read the payment's
outcome, and a receiver's log is not where it belongs. For a `refund.` event, `data` is the refund
as `GET /payments/{id}/refunds/{refund}` answers it, without `refund_url` for the same reason.
[refunds.md](refunds.md) lists its fields.

Fields are added and never removed or renamed. Read the fields you need and ignore the rest.

| `type` | When | `data` |
|---|---|---|
| `payment.awaiting_payment` | The payment can be paid | the payment |
| `attempt.confirming` | A transfer for it was seen on the chain, before it settled | the payment |
| `payment.succeeded` | The payment settled | the payment |
| `payment.expired` | Nothing arrived before the deadline | the payment |
| `payment.failed` | The payment will not settle | the payment |
| `refund.succeeded` | A refund settled, and the money is back with whoever paid | the refund |
| `refund.expired` | A refund's deadline passed and nothing settled | the refund |
| `endpoint.test` | You called `POST /webhook_endpoints/{id}/test` | `{}` |

Once a transfer has been seen for the payment, `data` carries `transfer`: `tx`, `block_height`,
`block_hash`, `block_time`, `from` and `value`, which you can check against a node of your own.
`attempt.confirming` is the first event to carry it. It says a transfer was seen, not that the
payment settled: a transfer can be reorganised away, and `payment.succeeded` is what says the
money is yours. `value` is not in the unit `amount` and `received` are in. [api.md](api.md)
describes the fields and the units.

## Endpoint registration

```
POST /webhook_endpoints
{"url": "https://shop.example/webhooks/suco", "description": "orders", "events": ["payment.succeeded", "payment.expired"]}
```

| Parameter | Type | Required | Description |
|---|---|---|---|
| `url` | string | yes | `https` only, at most 2048 bytes, without a username or password. A URL that resolves to an address inside the deployment is refused |
| `description` | string | no | A note to yourself, at most 200 bytes |
| `events` | array of string | no | The types to receive. Left out, the endpoint receives every type, including types added later |

The answer is `201` with the endpoint and, this once, its `secret`. An account holds at most 8
endpoints.

A disabled endpoint (`"enabled": false`) receives nothing and has no deliveries made for it.
What was pending waits, and is sent once the endpoint is enabled again.

## Endpoint routes

| Path | Credential | Description |
|---|---|---|
| `POST /webhook_endpoints` | read-write | Register an endpoint and receive its secret |
| `GET /webhook_endpoints` | read-only | List the endpoints, without secrets |
| `GET /webhook_endpoints/{id}` | read-only | Read one endpoint |
| `PATCH /webhook_endpoints/{id}` | read-write | Change `url`, `description`, `events` or `enabled` |
| `POST /webhook_endpoints/{id}/secret` | read-write | Rotate the secret. The old one verifies for 24 more hours |
| `DELETE /webhook_endpoints/{id}` | read-write | Remove the endpoint. Its pending deliveries become `failed` |
| `POST /webhook_endpoints/{id}/test` | read-write | Send one `endpoint.test`. Answers `202` with the delivery's id. While the last test is pending, the next is refused |
| `GET /webhook_endpoints/{id}/deliveries` | read-only | The newest 100 deliveries and every attempt at each |
| `POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` | read-write | Send a delivered or failed delivery again. Answers `202` |

While a rotated secret is within its 24 hours, `webhook-signature` carries two signatures
separated by a space, one under each secret. A library verifies against either.

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

After the tenth attempt, a little more than 3 days in, the delivery is `failed` and is not tried
again. A `3xx` answer is not followed and counts as a failure.

Deliveries to one endpoint about one payment are sent in the order the events happened. A later
delivery waits until the earlier one is delivered or failed. Deliveries about different payments
do not wait for each other.

Nothing disables an endpoint for failing. What is failing is in the delivery log, and the
operator's `suco doctor` counts it.

## Delivery log

`GET /webhook_endpoints/{id}/deliveries` answers with the newest 100 deliveries to the endpoint,
each with every attempt at it.

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
| `attempts[].status` | integer or null | The HTTP status you answered, or `null` when you did not answer |
| `attempts[].reason` | string | Why there was no answer. Present only when `status` is `null` |
| `attempts[].response` | string | The first 256 bytes of the body you answered |
| `attempts[].took_ms` | integer | How long the attempt took, in milliseconds |
| `next_at` | string or null | When the next attempt is due, while the delivery is pending |
| `delivered_at` | string or null | When a `2xx` was received |

| `reason` | Meaning |
|---|---|
| `timeout` | No answer within 20 seconds |
| `connection` | The connection could not be made, or was lost |
| `destination` | The URL no longer passes the check made at registration |
| `secret` | The deployment can no longer read the secret to sign with. See below |

Delivered and failed deliveries are kept for 30 days.

## Manual resend

`POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` sends a delivered or failed
delivery once more, under the same `webhook-id`, with a fresh `webhook-timestamp` and
signature. Its attempts go on from where they were. Use it after your receiver was down and is
back, or after it answered `2xx` and then lost what it took. A receiver that keeps the ids it
has seen drops the resend of a delivery it already took. A pending delivery cannot be resent: it
is on its way already.

## Key rotation by the operator

Secrets are stored encrypted under a key derived from the deployment's `credentials.key`. If the
operator replaces that key, existing secrets cannot be read. Deliveries to your endpoint are
then attempted with the reason `secret`, and `suco doctor` counts the endpoints affected.
Rotating your secret (`POST /webhook_endpoints/{id}/secret`) stores a new one under the current
key, and deliveries go on from the next attempt.

## Related

- [api.md](api.md): payments, `transfer`, and the routes that read a payment back
- [refunds.md](refunds.md): the refund a `refund.` event carries
- [operating.md](operating.md): what `suco doctor` says about failing deliveries
