# Checkout

日本語: [checkout.ja.md](checkout.ja.md)

suco Checkout is the page a payer pays on. A merchant's server opens a payment over the API and
sends the payer to the URL the answer carries. The page asks the payer's wallet to sign a
transfer of the amount to the merchant's address, the wallet sends it, and the page shows what
became of it.

**Today the page shows a payment and takes none.** The script and style that talk to a wallet
are a module of their own, not yet released, and a deployment without them serves the plain
page: the merchant's name, the amount, the deadline, the status, and the way back. The routes
that module will call are served all the same, and are below.

## Sending a payer

`POST /payments` answers with `checkout_url`, and `GET /payments/{id}` answers with the same one.
Send the payer there, by a redirect or a link. The URL is `<listen.base_url>/checkout/<token>`,
and the token is what admits the payer: nobody without it reads the page, and no credential is
asked for.

The page refuses to be framed. Open it as a page of its own.

A payment has one URL, and it is not reissued. A merchant who lost it reads it back with
`GET /payments/{id}`. A payment opened before its deployment served pages has none, and the key
is omitted.

The URL is a key to the payment's outcome. Keep it out of logs and out of messages to anyone but
the payer. suco keeps it out of its own log, and out of webhook events.

## Coming back

`return_url` in `POST /payments` is where the page sends the payer when they are done, and when
they cannot pay: after the deadline, or with a wallet the page does not work with. It is
optional. It has to be `https`, at most 2048 bytes, with no username or password;
`http://localhost` and `http://127.0.0.1` are accepted too, for development.

suco sends nothing to `return_url`, and appends nothing to it. Whether the payer paid is what
webhooks and `GET /payments/{id}` say, and a page at `return_url` reads that from its own server
rather than from the payer having arrived.

## Two deadlines

| | | Once past |
|---|---|---|
| The payment's | `expires_at` | Nothing more can be signed. The page says so, and shows what became of anything already sent |
| The page's | 30 days after the payment ends | The URL answers `404` |

A payment ends at `succeeded`, `expired` or `failed`. Its page is readable until then and for
30 days after, so that a payer who kept the URL can read the outcome. A payment not yet ended
has no such limit; it reaches `expired` or `succeeded` after its deadline.

## What the page shows

The merchant's name, which is the account's; the amount and the asset; the deadline; the
status; the transfer seen for the payment, once one has been; and the way back. Nothing of
`metadata`. The address the payment is paid to is not written out: it is in what the wallet
signs, and nowhere a payer would copy it from to send from an exchange.

## What a payer needs

A wallet that holds a key and signs typed data, on the payment's network. The payer signs an
EIP-3009 `TransferWithAuthorization` of the exact amount, sends the transaction themselves, and
pays its gas. Whether the wallet is on the right network, holds enough, and may send is what the
page reads from the wallet. suco reads none of it.

The key a payer signs under can be used once. When the wallet spent it on something that did
not arrive, the page asks for a new one, once per payment. A payer who would need another is
sent back to the merchant.

## The routes the page calls

The three routes under `/checkout/` are admitted by the token and by nothing else. Each answers
with `Cache-Control: no-store`, `Referrer-Policy: no-referrer`,
`X-Content-Type-Options: nosniff`, and a content security policy under which the page reaches
its own deployment and nothing else, and is framed by nobody. None carries CORS headers: only
the page itself calls them. The script and style are open to anyone and may be cached, and
carry `nosniff` alone.

| Route | |
|---|---|
| `GET /checkout/{token}` | The page, as HTML. A token nothing answers to is a `404` page |
| `GET /checkout/{token}/state` | What the page shows, as JSON |
| `POST /checkout/{token}/attempts` | What the payer signs: issued, or read back |
| `GET /checkout-assets/{path}` | The page's script and style, in a deployment that has them |

### State

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

| Key | |
|---|---|
| `status` | `awaiting_payment`, `awaiting_finality`, `succeeded`, `expired` or `failed`. A payment still `created` is shown as `awaiting_payment`: to the payer they are the same |
| `amount`, `asset` | As `GET /payments/{id}` answers them, with `chain_id` for the wallet to switch to |
| `expires_at` | The payment's deadline |
| `merchant.name` | The account's name |
| `return_url` | Where the merchant wants the payer sent, or `null` |
| `attempt` | The signing material of the live attempt, or `null` |
| `result` | The transfer seen for the payment, or `null` |
| `reason` | Why nothing can be signed, or `null` while something can. The words are below |
| `slower` | `true` while the deployment is not reading the payment's network as it usually does, and confirming takes longer |

`result` is the transfer as a payer can look it up: `tx`, `block_height`, `block_time`, `value`
in the asset's smallest unit, `confirming`, and `received_at`. `confirming` is `true` until the
payment succeeded, and `received_at` is set once it did.

| `reason` | |
|---|---|
| `expired` | The deadline passed |
| `closing` | Under two minutes remain, which is less than a signature takes |
| `paid` | A transfer was seen, and is waiting to be settled |
| `done` | The payment ended |
| `not_ready` | The deployment has not read the payment's network yet |
| `reissued` | The one new key was already given |
| `again` | Two requests of one page raced, and nothing is live to answer with. Asked again, it is answered. Only the attempts route says it |

### Attempts

`POST /checkout/{token}/attempts` takes an empty body, or `{"reissue": "<attempt id>"}` to ask
for a new key in place of the live one. Anything else is `400`.

1. With a live attempt and no `reissue`, the live attempt is answered, `200`.
2. With a `reissue` naming no live attempt, `400`.
3. With a reason nothing can be signed, `409` with `{"error": "<reason>"}`, and the payment is
   left as it was.
4. Otherwise an attempt is issued and answered, `201`. A payment still `created` becomes
   `awaiting_payment` first.

Reloading the page does not add keys: the second request reads the first one's attempt back.

### What the payer signs

The attempt is the typed data the page hands to `eth_signTypedData_v4`, less `from`, which the
page fills in with the payer's address. The keys under `domain` and `message` are the
standard's, in camelCase. `id` names the attempt, for showing and for `reissue`, and is not
signed.

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

`value` is in the asset's smallest unit, unlike `amount`. `validBefore` is the payment's
deadline in seconds. `nonce` is the key, and `domain` is what the asset's contract signs under,
which `suco.yaml` gives as `eip712` and `suco asset accept` checked against the contract.

## Responses

| Status | `error` | When |
|---|---|---|
| 400 | `invalid` | The body of `attempts` is neither empty nor a `reissue` |
| 404 | `not_found` | No such token, one that expired, or one of no shape. The HTML route answers with a page instead |
| 409 | the `reason` | Nothing can be signed |
| 413 | `too_large` | The body of `attempts` is over 1 KiB |
| 503 | `unavailable` | suco could not reach its database |
