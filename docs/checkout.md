# Checkout

日本語: [checkout.ja.md](checkout.ja.md)

suco Checkout is the page a payer pays on, and this page says what it does and what integrating
it takes.

It is written for the developer integrating it. It assumes you can already open a payment, as
[api.md](api.md) describes.

## Terms

| Term | Meaning |
|---|---|
| payment page | The page a payer pays on, served by suco. Its URL is `checkout_url` |
| token | The string in `checkout_url`. Whoever has it is admitted to the page |
| attempt | One try at paying, which the payment page issues |
| signing data | The typed data an attempt carries, which the payer's wallet signs |
| `nonce` | The one value in the signing data. It can be spent once |
| `return_url` | Where the payment page sends the payer when they are done, and when they cannot pay |
| module | The script and style that talk to a wallet |

## Payment flow on the page

1. Your server opens a payment and sends the payer to `checkout_url`.
2. The page reads the payment from `GET /checkout/{token}/state` and shows it.
3. The page asks `POST /checkout/{token}/attempts` for what the payer signs, and the payment
   becomes payable.
4. The payer's wallet signs an EIP-3009 `TransferWithAuthorization` of the exact amount, to the
   address the payment is paid to.
5. The payer's wallet sends the transaction, and the payer pays its gas.
6. suco reads the chain and finds the transfer. The page shows what became of it, and
   [api.md](api.md) has the states the payment moves through.
7. The page sends the payer to `return_url`, where there is one.

A payer needs a wallet that holds a key and signs typed data, on the payment's network. Whether
the wallet is on the right network, holds enough, and may send is what the page reads from the
wallet. suco reads none of it.

## What the page shows

Your name, which is the account's. The amount and the asset. The deadline. The status. The
transfer seen for the payment, once one has been. And the way back.

Nothing of `metadata`. The address the payment is paid to does not appear on the page. It is in
what the wallet signs, and nowhere a payer could copy it from to send from an exchange.

## Integrating

`POST /payments` answers with `checkout_url`, and `GET /payments/{id}` answers the same one. Send
the payer there, by a redirect or a link. The URL is `<listen.base_url>/checkout/<token>`, and
the token is what admits the payer. Nobody without it reads the page, and no credential is asked
for.

A payment has one such URL and suco does not reissue it. Read it back with `GET /payments/{id}`
when you have lost it. A payment opened before its instance served pages has none, and the key is
left out. Send the URL to the payer and to nobody else, for the reason [api.md](api.md) gives.

`return_url` in `POST /payments` is where the page sends the payer when they are done, and when
they cannot pay: after the deadline, or with a wallet the page does not work with. It is
optional. It has to be `https`, at most 2048 bytes, with no username or password.
`http://localhost` and `http://127.0.0.1` are accepted too, for development.

suco sends nothing to `return_url`, and appends nothing to it. Whether the payer paid is what
webhooks and `GET /payments/{id}` say. Read it from your own server rather than from the payer
having arrived.

## Page routes

The token in the path admits whoever calls the routes under `/checkout/`, and no credential is
asked for. Their answers carry the headers and the content security policy [api.md](api.md)
lists. The script and style under `/checkout-assets/` take no token, and carry
`X-Content-Type-Options: nosniff` alone.

| Path | Description |
|---|---|
| `GET /checkout/{token}` | The page, as HTML. A token nothing answers to is a `404` page |
| `GET /checkout/{token}/state` | What the page shows, as JSON |
| `POST /checkout/{token}/attempts` | What the payer signs: issued, or read back |
| `GET /checkout-assets/{path}` | The page's script and style, in an instance that has them |

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
| `status` | `awaiting_payment`, `awaiting_finality`, `succeeded`, `expired` or `failed`. A payment still `created` is shown as `awaiting_payment`, which to the payer is the same thing |
| `amount`, `asset` | As `GET /payments/{id}` answers them, with `chain_id` for the wallet to switch to |
| `expires_at` | The payment's deadline |
| `merchant.name` | The account's name |
| `return_url` | Where you want the payer sent, or `null` |
| `attempt` | The signing material of the live attempt, or `null` |
| `result` | The transfer seen for the payment, or `null` |
| `reason` | Why nothing can be signed, or `null` while something can. The words are below |
| `slower` | `true` while the instance is not reading the payment's network as it usually does, and confirming takes longer |

`result` is the transfer as a payer can look it up: `tx`, `block_height`, `block_time`, `value`
in the asset's smallest unit, `confirming`, and `received_at`. `confirming` is `true` until the
payment succeeded, and `received_at` is set once it did.

| `reason` | Meaning |
|---|---|
| `expired` | The deadline passed |
| `closing` | Under two minutes remain, which is less than a signature takes |
| `paid` | A transfer was seen, and is waiting to be settled |
| `done` | The payment ended |
| `not_ready` | The instance has not read the payment's network yet |
| `reissued` | The one new key was already given |
| `again` | Two requests of one page raced, and nothing is live to answer with. Asked again, it is answered. Only the attempts route says it |

### attempts

`POST /checkout/{token}/attempts` takes an empty body, or `{"reissue": "<attempt id>"}` to ask
for a new key in place of the live one. Anything else is `400`.

1. With a live attempt and no `reissue`, the live attempt is answered, `200`.
2. With a `reissue` naming no live attempt, `400`.
3. With a reason nothing can be signed, `409` with `{"error": "<reason>"}`, and the payment is
   left as it was.
4. Otherwise an attempt is issued and answered, `201`. A payment still `created` becomes
   `awaiting_payment` first.

Reloading the page does not add keys. The second request reads the first one's attempt back.

### Failures

| Status | `error` | When | What to do |
|---|---|---|---|
| 400 | `invalid` | The body of `attempts` is neither empty nor a `reissue` | Send an empty body, or `{"reissue": "<attempt id>"}` |
| 404 | `not_found` | No such token, one that expired, or one of no shape. The HTML route answers with a page instead | Check the token against the `checkout_url` `GET /payments/{id}` answers |
| 409 | the `reason` | Nothing can be signed | Act on the `reason`. The words are above |
| 413 | `too_large` | The body of `attempts` is over 1 KiB | Send an empty body, or a `reissue` naming one attempt |
| 503 | `unavailable` | suco could not reach its database | Ask the operator, who has [operating.md](operating.md) |

## Signing

The attempt is the typed data the page hands to `eth_signTypedData_v4`, less `from`, which the
page fills in with the payer's address. The keys under `domain` and `message` are the standard's,
in camelCase. `id` names the attempt, for showing and for `reissue`, and is not signed.

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

`value` is in the asset's smallest unit, unlike `amount`. `validBefore` is the payment's deadline
in seconds. `nonce` is the key, and `domain` is what the asset's contract signs under, which
`suco.yaml` gives as `eip712` and `suco asset accept` checked against the contract.

The key a payer signs under can be used once. When the wallet spent it on something that did not
arrive, the page asks for a new one, once per payment. A payer who would need another is sent
back to you.

## Deadlines

| Deadline | When | Once past |
|---|---|---|
| The payment's | `expires_at` | Nothing more can be signed. The page says so, and shows what became of anything already sent |
| The page's | 30 days after the payment ends | The URL answers `404` |

A payment ends at `succeeded`, `expired` or `failed`. The page is readable until then and for 30
days after, so that a payer who kept the URL can read the outcome. A payment that has not ended
has no such limit, and reaches `expired` or `succeeded` after its deadline.

## Security

The script and style that talk to a wallet are a module of their own, and it is not released.
Until it is, the page shows a payment and takes none. An instance without the module serves the
plain page: your name, the amount, the deadline, the status, and the way back. The routes the
module will call are served all the same.

The page refuses to be framed. Open it as a page of its own.

## Related

- [api.md](api.md): the payment the page shows, and the routes that open it and read it back
- [refunds.md](refunds.md): the page you sign a refund on, which works the same way
- [webhooks.md](webhooks.md): what suco sends your server when the payment changes
