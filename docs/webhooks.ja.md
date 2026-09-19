# Webhook

suco は、payment が変わったことを、登録した URL への HTTP `POST` で加盟店のサーバーに知らせ
ます。宛先の登録、送ったものの確認、秘密の更新は、API の他の経路と同じ資格情報で行います。

## 受け取るもの

event は 1 つの `POST` で、本文は 1 つの形の JSON です。

```json
{
  "type": "payment.succeeded",
  "timestamp": "2026-09-16T01:23:45Z",
  "account": "3f9c2c1e-6a1b-4a1e-9f4e-2f0f2c5a7b11",
  "data": { "id": "…", "status": "succeeded", "…": "…" }
}
```

`type` は何が起きたか、`timestamp` はいつ起きたか、`account` は誰の payment か、`data` は
`GET /payments/{id}` が返すのと同じ形の payment で、`metadata` も入ります。`checkout_url` だけは
入りません。payment の結果を読める鍵で、受け取り側のログに残すものではないからです。それを除け
ば、知らされるものと読めるものは同じです。`refund.` の event が持つのは Refund で、
`GET /payments/{id}/refunds/{refund}` が返すのと同じ形から `refund_url` を除いたものです。あれも
同じ意味の鍵だからです。項目は [refunds.ja.md](refunds.ja.md) にあります。

| `type` | いつ | `data` |
|---|---|---|
| `payment.awaiting_payment` | 支払い可能になった | payment |
| `attempt.confirming` | チェーン上で送金が見えた。確定の前 | payment |
| `payment.succeeded` | 確定した | payment |
| `payment.expired` | 期限までに何も届かなかった | payment |
| `payment.failed` | 確定しないことが決まった | payment |
| `refund.succeeded` | Refund が確定し、金が払った人に戻った | Refund |
| `refund.expired` | Refund の期限が過ぎ、何も確定しなかった | Refund |
| `endpoint.test` | `POST /webhook_endpoints/{id}/test` を呼んだ | `{}` |

payment は、送金が見つかると `transfer` を持ちます。`tx`、`block_height`、`block_hash`、
`block_time`、`from`、`value` で、自分の node で確かめられる値です。最初にこれを持つ event が
`attempt.confirming` で、送金が見えたことを言うだけで、確定したとは言いません。送金は reorg で
消えることがあり、資金が加盟店のものになったと言うのは `payment.succeeded` です。項目の意味と、
`value` を `amount` や `received` と違う書き方にしている理由は [api.ja.md](api.ja.md) にあります。

項目は増えるだけで、消えたり名前が変わったりしません。要る項目だけを読み、他は無視してくだ
さい。

## 受け取り側がすること 8 つ

1. **署名は自分の言語の Standard Webhooks の library で検証します。** 手で書きません。
   timestamp の許容差は 5 分です。header は標準の `webhook-id`、`webhook-timestamp`、
   `webhook-signature` で、秘密は登録の応答にあった `whsec_…` の文字列です。
2. **`webhook-id` を鍵にして、同じ配送を 2 度処理しません。** id は同じ配送のどの試行でも
   同じで、再送と手での再送でも変わりません。
3. **先に `2xx` を返し、処理は後で行います。** 受け取り側の持ち時間は接続を含めて 20 秒で、
   それより遅い応答は失敗の試行として数え、再送します。
4. **品物を渡すのは `payment.succeeded` を受けたときだけです。** `attempt.confirming` は
   送金が見えたという知らせで、その後に何も来ないことがあります。
5. **注文を閉じるのは `payment.expired` を受けたときだけです。** 自分の時計の期限は期限では
   ありません。期限の前に送られた送金がまだ届く途中のことがあります。
6. **event の届いた順ではなく `data.status` で動きます。** 再送と手での再送で、後の event が
   先の event より前に届くことがあります。迷ったら `GET /payments/{id}` で読みます。
7. **webhook の経路を CSRF の保護から外します。** 要求は cookie も form も持たず、CSRF の
   検査は要求を断ります。
8. **秘密は登録の応答に 1 度だけ出ます。** 失くしたら更新します。更新の応答に新しい秘密が
   1 度だけ出ます。

## 宛先の登録

```
POST /webhook_endpoints
{"url": "https://shop.example/webhooks/suco", "description": "orders", "events": ["payment.succeeded", "payment.expired"]}
```

| キー | | |
|---|---|---|
| `url` | 必須 | `https` だけ。2048 バイトまで。username と password は入れられません。配備の内側のアドレスに解決する URL は断られます |
| `description` | 任意 | 自分のための覚え書き。200 バイトまで |
| `events` | 任意 | 受ける種類。省略すると、後から足される種類も含めて全部受けます |

応答は `201` で、宛先と、この 1 度だけ `secret` が入ります。1 つの account が持てる宛先は
8 つまでです。

| 経路 | 資格情報 | |
|---|---|---|
| `POST /webhook_endpoints` | read-write | 登録し、秘密を受け取る |
| `GET /webhook_endpoints` | read-only | 一覧。秘密は入らない |
| `GET /webhook_endpoints/{id}` | read-only | 1 つ読む |
| `PATCH /webhook_endpoints/{id}` | read-write | `url`、`description`、`events`、`enabled` を変える |
| `POST /webhook_endpoints/{id}/secret` | read-write | 秘密を更新する。古い秘密はあと 24 時間検証に通る |
| `DELETE /webhook_endpoints/{id}` | read-write | 消す。待っていた配送は failed になる |
| `POST /webhook_endpoints/{id}/test` | read-write | `endpoint.test` を 1 つ送る。`202` で配送の id を返す。前の test が pending の間は次を断る |
| `GET /webhook_endpoints/{id}/deliveries` | read-only | 新しい順に 100 件の配送と、それぞれの全部の試行 |
| `POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` | read-write | delivered か failed の配送をもう 1 度送る。`202` |

無効にした宛先（`"enabled": false`）には何も届かず、配送も作られません。待っていた配送は
そのまま待ち、有効に戻すと送られます。

更新した秘密の 24 時間の間は、`webhook-signature` に 2 つの署名が空白で区切って並びます。
それぞれの秘密による署名で、library はどちらか一方で検証に通します。

## 再送

`2xx` を得られなかった試行は、5 秒、5 分、30 分、2 時間、5 時間、10 時間、14 時間、20 時間、
24 時間の後にやり直します。それぞれに最大 10 % の乱数を足します。10 回で 3 日と少しで、
その後は `failed` になり、それ以上は送りません。`3xx` は追わず、失敗に数えます。

同じ宛先への同じ payment の配送は、event の起きた順に送ります。後の配送は、先の配送が
delivered か failed になるまで待ちます。別の payment の配送は待ちません。

失敗が続いても宛先を止めることはしません。何が失敗しているかは配送の一覧にあり、運用者の
`suco doctor` が数を言います。

## 送ったものを読む

`GET /webhook_endpoints/{id}/deliveries` は、その宛先への配送を新しい順に 100 件、それぞれの
全部の試行と一緒に返します。

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

`state` は `pending`、`delivered`、`failed` のどれかです。試行は、受け取り側が答えたときは
`status` を持ち、答えなかったときは `reason` を持ちます。`timeout`、`connection`、URL が
検査に通らなくなった `destination`、配備が署名に使う秘密を読めなくなった `secret` です。
`response` は受け取り側が返した本文の先頭 256 バイトです。delivered と failed の配送は 30 日
残ります。

## 手での再送

`POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` は、delivered か failed の配送を
もう 1 度送ります。同じ `webhook-id` で、新しい `webhook-timestamp` と署名を付け、試行の数は
続きから数えます。落ちていた受け取り側が戻ったときや、`2xx` を返した後に受け取ったものを
失ったときのためです。見た id を覚えている受け取り側は、受け取り済みの配送の再送を捨てます。
pending の配送の再送は断ります。すでに送る途中だからです。

## 運用者が配備の鍵を入れ替えたとき

秘密は、配備の `credentials.key` から導いた鍵で暗号化して保存されています。運用者がその鍵を
入れ替えると、それまでの秘密は読めなくなり、その宛先への配送は `secret` の理由の試行になり、
`suco doctor` が影響を受けた宛先の数を言います。`POST /webhook_endpoints/{id}/secret` で秘密を
更新すると新しい秘密が今の鍵で保存され、次の試行から届きます。
