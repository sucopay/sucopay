# Webhooks

suco tells your server when a payment changes, by sending an HTTP `POST` to a URL you
register. Registering, reading what was sent, and rotating the secret are done over the API
with the same credential the rest of the API takes.

## What you receive

Every event is one `POST` with a JSON body of one shape:

```json
{
  "type": "payment.succeeded",
  "timestamp": "2026-09-16T01:23:45Z",
  "account": "3f9c2c1e-6a1b-4a1e-9f4e-2f0f2c5a7b11",
  "data": { "id": "…", "status": "succeeded", "…": "…" }
}
```

`type` is what happened, `timestamp` when it happened, `account` whose payment it is, and
`data` the payment as `GET /payments/{id}` answers it, `metadata` included and `checkout_url`
left out, since that is a key to the payment's outcome and a receiver's log is not where one
belongs. What you are told and what you can read are otherwise the same thing.

| `type` | When | `data` |
|---|---|---|
| `payment.awaiting_payment` | The payment can be paid | the payment |
| `attempt.confirming` | A transfer for it was seen on the chain, before it is settled | the payment |
| `payment.succeeded` | The payment settled | the payment |
| `payment.expired` | Nothing arrived before the deadline | the payment |
| `payment.failed` | It will not settle | the payment |
| `endpoint.test` | You called `POST /webhook_endpoints/{id}/test` | `{}` |

The payment carries `transfer` once a transfer has been seen for it: `tx`, `block_height`,
`block_hash`, `block_time`, `from` and `value`, which is what you can check against a node of
your own. `attempt.confirming` is the first event to carry one, and it says a transfer was seen,
not that the payment settled; a transfer can be reorganised away, and `payment.succeeded` is
what says the money is yours. What the fields mean, and why `value` is not written the way
`amount` and `received` are, is in [api.md](api.md).

Fields are added and never removed or renamed. Read the fields you need and ignore the rest.

## Eight things a receiver does

1. **Verify the signature with a Standard Webhooks library** for your language rather than by
   hand, with a tolerance of five minutes on the timestamp. The headers are the standard's:
   `webhook-id`, `webhook-timestamp` and `webhook-signature`, and the secret is the `whsec_…`
   string the registration answered with.
2. **Use `webhook-id` to process each delivery once.** The id is the same on every attempt at
   one delivery, retries and manual resends included.
3. **Answer `2xx` first and do the work after.** A receiver has 20 seconds, counting the
   connection; a slower answer is a failed attempt and is retried.
4. **Hand over the goods on `payment.succeeded` only.** `attempt.confirming` is notice that
   money was seen, and can be followed by nothing.
5. **Close the order on `payment.expired` only.** A deadline on your own clock is not the
   deadline: a transfer sent before it may still be on its way.
6. **Act on `data.status`, not on the order events arrive in.** Retries and resends can put a
   later event before an earlier one. When in doubt, read `GET /payments/{id}`.
7. **Exempt the webhook path from CSRF protection.** The request carries no cookie and no
   form; a CSRF check refuses it.
8. **The secret is shown once, in the answer to the registration.** A lost secret is replaced
   by rotating it, which shows a new one once.

## Registering an endpoint

```
POST /webhook_endpoints
{"url": "https://shop.example/webhooks/suco", "description": "orders", "events": ["payment.succeeded", "payment.expired"]}
```

| Key | | |
|---|---|---|
| `url` | required | `https` only, at most 2048 bytes, no username or password in it. A URL that resolves to an address inside the deployment is refused |
| `description` | optional | A note to yourself, at most 200 bytes |
| `events` | optional | The types to receive. Left out, the endpoint receives every type, including ones added later |

The answer is `201` with the endpoint and, this once, its `secret`. An account holds at most
eight endpoints.

| Route | Credential | |
|---|---|---|
| `POST /webhook_endpoints` | read-write | Register one and receive its secret |
| `GET /webhook_endpoints` | read-only | List them, without secrets |
| `GET /webhook_endpoints/{id}` | read-only | Read one |
| `PATCH /webhook_endpoints/{id}` | read-write | Change `url`, `description`, `events` or `enabled` |
| `POST /webhook_endpoints/{id}/secret` | read-write | Rotate the secret; the old one verifies for 24 more hours |
| `DELETE /webhook_endpoints/{id}` | read-write | Remove it. Pending deliveries to it are failed |
| `POST /webhook_endpoints/{id}/test` | read-write | Send one `endpoint.test`; answers `202` with the delivery's id. One at a time: while the last test is pending, the next is refused |
| `GET /webhook_endpoints/{id}/deliveries` | read-only | The newest 100 deliveries and every attempt at each |
| `POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` | read-write | Send a delivered or failed delivery again; answers `202` |

A disabled endpoint (`"enabled": false`) receives nothing and has no deliveries made for it;
what was pending waits, and is sent once it is enabled again.

While a rotated secret is within its 24 hours, `webhook-signature` carries two signatures
separated by a space, one under each secret; a library verifies against either.

## Retries

An attempt that gets no `2xx` is tried again: after 5 seconds, 5 minutes, 30 minutes, 2 hours,
5 hours, 10 hours, 14 hours, 20 hours and 24 hours, each with up to 10% added at random. Ten
attempts over a little more than three days, and then the delivery is `failed` and not tried
again. A `3xx` is not followed and counts as a failure.

Deliveries to one endpoint about one payment are sent in the order the events happened: a later
one waits until the earlier one is delivered or failed. Deliveries about different payments do not
wait for each other.

Nothing disables an endpoint for failing. What is failing is in the list of deliveries, and the
operator's `suco doctor` counts it.

## Reading what was sent

`GET /webhook_endpoints/{id}/deliveries` answers with the newest 100 deliveries to the endpoint,
each with every attempt at it:

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

`state` is `pending`, `delivered` or `failed`. An attempt has a `status` when the receiver
answered, and a `reason` when it did not: `timeout`, `connection`, `destination` when the URL no
longer passes its check, or `secret` when the deployment can no longer read the secret to sign
with. `response` is the first 256 bytes of what the receiver answered. Deliveries that are
delivered or failed are kept for 30 days.

## Resending by hand

`POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` sends a delivery that is delivered
or failed once more: under the same `webhook-id`, with a fresh `webhook-timestamp` and
signature, and with its attempts going on from where they were. It is for a receiver that was
down and is back, or one that answered `2xx` and then lost what it took. A receiver that keeps
the ids it has seen drops a resend of a delivery it already took. A delivery still pending is
refused; it is on its way already.

## When the operator swaps the deployment's key

Secrets are stored encrypted under a key derived from the deployment's `credentials.key`. If the
operator replaces that key, existing secrets cannot be read, deliveries to your endpoint are
attempted with the reason `secret`, and `suco doctor` counts the endpoints affected. Rotating
your secret (`POST /webhook_endpoints/{id}/secret`) stores a new one under the current key, and
deliveries go on from the next attempt.
