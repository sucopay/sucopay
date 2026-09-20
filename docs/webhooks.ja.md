# Webhook

English: [webhooks.md](webhooks.md)

suco は、支払いか返金が変わるたびに、登録した URL へ HTTP `POST` を送ります。このページは、
受信側がしなければならないことと、Webhook エンドポイントと配送を API でどう管理するかを説明
します。

受信側を作る加盟店の開発者向けです。[api.ja.md](api.ja.md) のとおりに支払い（`payment`）を
作れることと、その資格情報が手元にあることを前提にします。

## 受信側の要件

1. **署名は、自分の言語の Standard Webhooks のライブラリで検証してください。** 手で検証
   しないでください。timestamp の許容差は 5 分です。ヘッダーは規格どおりの `webhook-id`、
   `webhook-timestamp`、`webhook-signature` です。シークレットは登録のレスポンスにあった
   `whsec_…` の文字列です。
2. **`webhook-id` をキーにして、同じ配送を 2 度処理しないでください。** id は同じ配送の
   どの試行でも同じで、再送と手動の再送でも変わりません。
3. **先に `2xx` を返し、後で処理してください。** 持ち時間は接続を含めて 20 秒です。それより
   遅いレスポンスは失敗した試行として数え、再送します。
4. **品物を渡すのは `payment.succeeded` を受けたときだけです。** `attempt.confirming` は送金が
   見えたという知らせで、その後に何も来ないことがあります。
5. **注文を閉じるのは `payment.expired` を受けたときだけです。** 自分の時計で測った期限は
   期限ではありません。期限の前に送られた送金が、まだ届く途中のことがあります。
6. **event が届いた順ではなく、`data.status` で動いてください。** 再送と手動の再送で、後の
   event が先の event より前に届くことがあります。迷ったら `GET /payments/{id}` で読みます。
7. **Webhook のパスを CSRF の保護から外してください。** リクエストは cookie も form も持ち
   ません。CSRF の検査はそれを断ります。
8. **シークレットは保管してください。** 登録のレスポンスに 1 度だけ出ます。失くしたら更新
   します。更新のレスポンスに新しいシークレットが 1 度だけ出ます。

## event

event は 1 つの `POST` で、本文は 1 つの形の JSON です。

```json
{
  "type": "payment.succeeded",
  "timestamp": "2026-09-16T01:23:45Z",
  "account": "3f9c2c1e-6a1b-4a1e-9f4e-2f0f2c5a7b11",
  "data": { "id": "…", "status": "succeeded", "…": "…" }
}
```

| 項目 | 型 | 説明 |
|---|---|---|
| `type` | string | 何が起きたか。下の種類のどれか |
| `timestamp` | string | いつ起きたか。RFC 3339、UTC |
| `account` | string | その支払いか返金が属する account |
| `data` | object | 支払いか返金。API が返すのと同じ形 |

`payment.` と `attempt.` の event の `data` は、`GET /payments/{id}` が返すのと同じ形の
支払いで、`metadata` も入ります。`checkout_url` は入りません。この URL を持つ人は支払いの
結果を読めます。受信側のログに残すものではありません。`refund.` の event の `data` は、
`GET /payments/{id}/refunds/{refund}` が返すのと同じ形の返金です。同じ理由で `refund_url` は
入りません。項目は [refunds.ja.md](refunds.ja.md) にあります。

項目は増えるだけで、消えたり名前が変わったりしません。要る項目だけを読み、ほかは無視して
ください。

| `type` | いつ | `data` |
|---|---|---|
| `payment.awaiting_payment` | 支払えるようになった | 支払い |
| `attempt.confirming` | チェーン上で送金が見えた。確定の前 | 支払い |
| `payment.succeeded` | 確定した | 支払い |
| `payment.expired` | 期限までに何も届かなかった | 支払い |
| `payment.failed` | 確定しないことが決まった | 支払い |
| `refund.succeeded` | 返金が確定し、額が払った人に戻った | 返金 |
| `refund.expired` | 返金の期限が過ぎ、何も確定しなかった | 返金 |
| `endpoint.test` | `POST /webhook_endpoints/{id}/test` を呼んだ | `{}` |

送金が見つかると、`data` は `transfer` を持ちます。`tx`、`block_height`、`block_hash`、
`block_time`、`from`、`value` で、自分のノードで確かめられる値です。最初にこれを持つ event が
`attempt.confirming` です。これは送金が見えたと言うだけで、確定したとは言いません。送金は
再編成（reorg）で消えることがあり、額が加盟店のものになったと言うのは `payment.succeeded`
です。`value` は `amount` や `received` と同じ単位ではありません。項目の意味と単位の規則は
[api.ja.md](api.ja.md) にあります。

## Webhook エンドポイントの登録

```
POST /webhook_endpoints
{"url": "https://shop.example/webhooks/suco", "description": "orders", "events": ["payment.succeeded", "payment.expired"]}
```

| パラメータ | 型 | 必須 | 説明 |
|---|---|---|---|
| `url` | string | 必須 | `https` だけ。2048 バイトまで。ユーザー名とパスワードは入れられません。suco Pay が動いているネットワークの内側のアドレスに解決する URL は断ります |
| `description` | string | 任意 | 自分のための覚え書き。200 バイトまで |
| `events` | string の配列 | 任意 | 受ける種類。省くと、後から足される種類も含めて全部受けます |

レスポンスは `201` で、Webhook エンドポイントと、この 1 度だけ `secret` が入ります。1 つの
account が持てる Webhook エンドポイントは 8 つまでです。

無効にした Webhook エンドポイント（`"enabled": false`）には何も届かず、配送も作られません。
待っていた配送はそのまま待ち、有効に戻すと送られます。

## Webhook エンドポイントの API

| パス | 資格情報 | 説明 |
|---|---|---|
| `POST /webhook_endpoints` | read-write | 登録し、シークレットを受け取る |
| `GET /webhook_endpoints` | read-only | 一覧。シークレットは入らない |
| `GET /webhook_endpoints/{id}` | read-only | 1 つ読む |
| `PATCH /webhook_endpoints/{id}` | read-write | `url`、`description`、`events`、`enabled` を変える |
| `POST /webhook_endpoints/{id}/secret` | read-write | シークレットを更新する。古いシークレットはあと 24 時間、検証に通る |
| `DELETE /webhook_endpoints/{id}` | read-write | 消す。待っていた配送は `failed` になる |
| `POST /webhook_endpoints/{id}/test` | read-write | `endpoint.test` を 1 つ送る。`202` で配送の id を返す。前の test が pending の間は次を断る |
| `GET /webhook_endpoints/{id}/deliveries` | read-only | 新しい順に 100 件の配送と、それぞれの全部の試行 |
| `POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` | read-write | delivered か failed の配送をもう 1 度送る。`202` |

更新したシークレットの 24 時間の間は、`webhook-signature` に 2 つの署名が空白で区切って
並びます。それぞれのシークレットによる署名で、ライブラリはどちらか一方で検証に通します。

## 再送

`2xx` を得られなかった試行はやり直します。それぞれの待ち時間に、最大 10 % の乱数を足します。

| 試行 | その前の待ち時間 |
|---|---|
| 2 回目 | 5 秒 |
| 3 回目 | 5 分 |
| 4 回目 | 30 分 |
| 5 回目 | 2 時間 |
| 6 回目 | 5 時間 |
| 7 回目 | 10 時間 |
| 8 回目 | 14 時間 |
| 9 回目 | 20 時間 |
| 10 回目 | 24 時間 |

10 回目の後、3 日と少しで配送は `failed` になり、それ以上は送りません。`3xx` は追わず、失敗に
数えます。

同じ Webhook エンドポイントへの同じ支払いの配送は、event の起きた順に送ります。後の配送は、
先の配送が delivered か failed になるまで待ちます。別の支払いの配送は待ちません。

失敗が続いても Webhook エンドポイントを止めることはしません。何が失敗しているかは配送の記録に
あり、運用者の `suco doctor` が数を出力します。

## 配送の記録

`GET /webhook_endpoints/{id}/deliveries` は、その Webhook エンドポイントへの配送を新しい順に
100 件、それぞれの全部の試行と一緒に返します。

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

| 項目 | 型 | 説明 |
|---|---|---|
| `id` | string | 配送の id。`webhook-id` として送られる |
| `type` | string | event の種類 |
| `payment` | string か null | その event の支払い。`endpoint.test` では `null` |
| `occurred_at` | string | event が起きた時刻 |
| `state` | string | `pending`、`delivered`、`failed` のどれか |
| `attempts` | array | その配送の全部の試行。古い順 |
| `attempts[].at` | string | 試行の時刻 |
| `attempts[].status` | integer か null | 受信側が返した HTTP ステータス。答えなかったときは `null` |
| `attempts[].reason` | string | 答えが無かった理由。`status` が `null` のときだけ |
| `attempts[].response` | string | 受信側が返した本文の先頭 256 バイト |
| `attempts[].took_ms` | integer | 試行にかかった時間。ミリ秒 |
| `next_at` | string か null | 次の試行の予定。pending の間だけ |
| `delivered_at` | string か null | `2xx` を受け取った時刻 |

| `reason` | 意味 |
|---|---|
| `timeout` | 20 秒以内に答えが無かった |
| `connection` | 接続できなかったか、途中で切れた |
| `destination` | URL が登録時の検査に通らなくなった |
| `secret` | 署名に使うシークレットを suco Pay が読めなくなった。下の節を参照 |

delivered と failed の配送は 30 日残ります。

## 手動の再送

`POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` は、delivered か failed の配送を
もう 1 度送ります。同じ `webhook-id` で、新しい `webhook-timestamp` と署名を付け、試行の数は
続きから数えます。落ちていた受信側が戻ったときや、`2xx` を返した後に受け取ったものを失った
ときに使います。見た id を覚えている受信側は、受け取り済みの配送の再送を捨てます。pending の
配送は再送できません。すでに送る途中だからです。

## 運用者による鍵の入れ替え

シークレットは、`credentials.key` から導いた鍵で暗号化して保存されています。運用者がその鍵を
入れ替えると、それまでのシークレットは読めなくなります。その Webhook エンドポイントへの配送は
`secret` の理由の試行になり、`suco doctor` が影響を受けた Webhook エンドポイントの数を出力
します。
`POST /webhook_endpoints/{id}/secret` でシークレットを更新すると、新しいシークレットが今の鍵で
保存され、次の試行から届きます。

## 関連

- [api.ja.md](api.ja.md): 支払い、`transfer`、支払いを読み戻す API エンドポイント
- [refunds.ja.md](refunds.ja.md): `refund.` の event が運ぶ返金
- [operating.ja.md](operating.ja.md): 失敗している配送について `suco doctor` が出力するもの
