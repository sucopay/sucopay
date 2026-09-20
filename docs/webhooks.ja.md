# Webhook

English: [webhooks.md](webhooks.md)

suco は、支払いか返金が変わるたびに、加盟店が登録した URL へ HTTP `POST` を送ります。
このページは、受信側の要件と、Webhook エンドポイントと通知を API で管理する方法を説明します。

受信側を作る加盟店の開発者向けです。[api.ja.md](api.ja.md) のとおりに支払い（`payment`）を
作れることと、支払いを作った資格情報が手元にあることを前提にします。

## このページの語

| 語 | 意味 |
|---|---|
| Webhook エンドポイント（`webhook_endpoints`） | suco が `POST` を送る URL。加盟店が API で登録する |
| 受信側 | Webhook エンドポイントの URL で `POST` を受け取る、加盟店のプログラム |
| 署名シークレット（`whsec_…`） | 受信側が署名を検証するときに使う秘密。登録のレスポンスに 1 度だけ出る |
| event | 支払いか返金が 1 回変わったという知らせ |
| 通知（`deliveries`） | 1 つの event を 1 つの Webhook エンドポイントへ送ること |
| 通知の試行（`attempts`） | 1 つの通知の 1 回の `POST` |
| 再送 | `2xx` を得られなかった通知の試行を、suco がやり直すこと |
| 手動の再送 | delivered か failed の通知を、加盟店の求めに応じて suco がもう 1 度送ること |

## 受信側の要件

1. **署名は、自分の言語の Standard Webhooks のライブラリで検証してください。** 手で
   検証しないでください。timestamp の許容差は 5 分です。ヘッダーは規格どおりの
   `webhook-id`、`webhook-timestamp`、`webhook-signature` です。署名シークレットは、
   登録のレスポンスが返す `whsec_…` の文字列です。
2. **`webhook-id` をキーにして、同じ通知を 2 度処理しないでください。** `webhook-id` は
   同じ通知のどの試行でも同じで、再送と手動の再送でも変わりません。
3. **先に `2xx` を返し、後で処理してください。** 制限時間は、接続を含めて 20 秒です。
   20 秒を過ぎたレスポンスは、失敗した試行として数え、再送します。
4. **品物を渡すのは `payment.succeeded` を受けたときだけです。** `attempt.confirming` は、
   送金が見えたという知らせです。`attempt.confirming` の後に何も来ないことがあります。
5. **注文を閉じるのは `payment.expired` を受けたときだけです。** 加盟店が自分で測った期限は、
   支払いの期限ではありません。期限の前に送られた送金が、まだ届く途中のことがあります。
6. **event が届いた順ではなく、`data.status` で動いてください。** 再送と手動の再送で、後の
   event が先の event より前に届くことがあります。迷ったら `GET /payments/{id}` で支払いを
   読んでください。
7. **Webhook エンドポイントのパスを CSRF の保護から外してください。** リクエストは cookie も
   form も持ちません。CSRF の検査は、cookie も form も無いリクエストを拒否します。
8. **署名シークレットは保管してください。** 署名シークレットは、登録のレスポンスに 1 度だけ
   出ます。失くしたら更新してください。更新のレスポンスに、新しい署名シークレットが 1 度だけ
   出ます。

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
| `account` | string | 支払いか返金が属する account |
| `data` | object | 支払いか返金。API が返すのと同じ形 |

`payment.` と `attempt.` の event の `data` は、`GET /payments/{id}` が返すのと同じ形の
支払いで、`metadata` も入ります。`checkout_url` は入りません。`checkout_url` を持つ人は
支払いの結果を読めます。受信側のログにも残さないでください。`refund.` の event の `data` は、
`GET /payments/{id}/refunds/{refund}` が返すのと同じ形の返金です。同じ理由で `refund_url` も
入りません。返金の項目は [refunds.ja.md](refunds.ja.md) にあります。

項目は増えるだけで、消えたり名前が変わったりしません。必要な項目だけを読み、ほかは
無視してください。

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

送金が見つかると、`data` は `transfer` を持ちます。`transfer` は `tx`、`block_height`、
`block_hash`、`block_time`、`from`、`value` で、加盟店が自分のノードで確かめられる値です。
`transfer` を最初に持つ event が `attempt.confirming` です。`attempt.confirming` は送金が
見えたことだけを示し、確定したことは示しません。送金は再編成（reorg）で消えることがあります。
額が加盟店のものになったことを示すのは `payment.succeeded` です。`value` は `amount` や
`received` と同じ単位ではありません。項目の意味と単位の規則は [api.ja.md](api.ja.md) に
あります。

## Webhook エンドポイントの登録

```
POST /webhook_endpoints
{"url": "https://shop.example/webhooks/suco", "description": "orders", "events": ["payment.succeeded", "payment.expired"]}
```

| パラメータ | 型 | 必須 | 説明 |
|---|---|---|---|
| `url` | string | 必須 | `https` だけ。2048 バイトまで。ユーザー名とパスワードは入れられません。suco Pay が動いているネットワークの内側のアドレスに解決する URL は拒否します |
| `description` | string | 任意 | 自分のための覚え書き。200 バイトまで |
| `events` | string の配列 | 任意 | 受ける種類。省くと、後から足される種類も含めて全部受けます |

レスポンスは `201` で、Webhook エンドポイントと署名シークレット（`secret`）が入ります。
署名シークレットがレスポンスに出るのは、登録のときの 1 度だけです。1 つの account が持てる
Webhook エンドポイントは 8 つまでです。

無効にした Webhook エンドポイント（`"enabled": false`）には何も届かず、通知も作られません。
待っていた通知は待ち続け、Webhook エンドポイントを有効に戻すと送られます。

## Webhook エンドポイントの API

| パス | 資格情報 | 説明 |
|---|---|---|
| `POST /webhook_endpoints` | read-write | 登録し、署名シークレットを受け取る |
| `GET /webhook_endpoints` | read-only | 一覧。署名シークレットは入らない |
| `GET /webhook_endpoints/{id}` | read-only | 1 つ読む |
| `PATCH /webhook_endpoints/{id}` | read-write | `url`、`description`、`events`、`enabled` を変える |
| `POST /webhook_endpoints/{id}/secret` | read-write | 署名シークレットを更新する。古い署名シークレットはあと 24 時間、検証に使える |
| `DELETE /webhook_endpoints/{id}` | read-write | 消す。待っていた通知は `failed` になる |
| `POST /webhook_endpoints/{id}/test` | read-write | `endpoint.test` を 1 つ送る。`202` で通知の id を返す。前の test が pending の間は次を拒否する |
| `GET /webhook_endpoints/{id}/deliveries` | read-only | 新しい順に 100 件の通知と、それぞれの全部の試行 |
| `POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` | read-write | delivered か failed の通知をもう 1 度送る。`202` |

署名シークレットを更新してから 24 時間の間は、`webhook-signature` に 2 つの署名が空白で
区切って並びます。古い署名シークレットによる署名と、新しい署名シークレットによる署名です。
ライブラリは、どちらか一方の署名が合えば検証に成功します。

## 再送

`2xx` を得られなかった試行はやり直します。待ち時間には、最大 10 % の乱数を足します。

| 試行 | 直前の待ち時間 |
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

10 回目の試行の後、3 日と少しで通知は `failed` になり、suco はもう送りません。`3xx` は追わず、
失敗に数えます。

同じ Webhook エンドポイントへの同じ支払いの通知は、event の起きた順に送ります。後の通知は、
先の通知が delivered か failed になるまで待ちます。別の支払いの通知は待ちません。

失敗が続いても、suco は Webhook エンドポイントを止めません。何が失敗しているかは通知の記録に
あり、運用者の `suco doctor` が数を出力します。

## 通知の記録

`GET /webhook_endpoints/{id}/deliveries` は、1 つの Webhook エンドポイントへの通知を新しい順に
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
| `id` | string | 通知の id。`webhook-id` として送られる |
| `type` | string | event の種類 |
| `payment` | string か null | event が指す支払い。`endpoint.test` では `null` |
| `occurred_at` | string | event が起きた時刻 |
| `state` | string | `pending`、`delivered`、`failed` のどれか |
| `attempts` | array | 通知の全部の試行。古い順 |
| `attempts[].at` | string | 試行の時刻 |
| `attempts[].status` | integer か null | 受信側が返した HTTP ステータス。レスポンスが無かったときは `null` |
| `attempts[].reason` | string | レスポンスが無かった理由。`status` が `null` のときだけ |
| `attempts[].response` | string | 受信側が返した本文の先頭 256 バイト |
| `attempts[].took_ms` | integer | 試行にかかった時間。ミリ秒 |
| `next_at` | string か null | 次の試行の予定。pending の間だけ |
| `delivered_at` | string か null | `2xx` を受け取った時刻 |

| `reason` | 意味 |
|---|---|
| `timeout` | 20 秒以内にレスポンスが無かった |
| `connection` | 接続できなかったか、途中で切れた |
| `destination` | URL が登録時の検査に通らなくなった |
| `secret` | 署名シークレットを suco Pay が読めなくなった。下の節を参照 |

delivered と failed の通知は 30 日残ります。

## 手動の再送

`POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` は、delivered か failed の通知を
もう 1 度送ります。同じ `webhook-id` で、新しい `webhook-timestamp` と署名を付け、試行の数は
続きから数えます。止まっていた受信側が戻ったときや、`2xx` を返した後に受け取った通知を失った
ときに使います。受け取った `webhook-id` を覚えている受信側は、受け取り済みの通知の再送を
捨てます。pending の通知は再送できません。送る途中だからです。

## 運用者による鍵の入れ替え

署名シークレットは、`credentials.key` から導いた鍵で暗号化して保存されています。運用者が
`credentials.key` を入れ替えると、入れ替えの前の署名シークレットは読めなくなります。読めない
署名シークレットを持つ Webhook エンドポイントへの通知は、`secret` の理由の試行になります。
`suco doctor` は、読めなくなった署名シークレットを持つ Webhook エンドポイントの数を出力します。
`POST /webhook_endpoints/{id}/secret` で署名シークレットを更新すると、新しい署名シークレットが
今の鍵で暗号化して保存され、次の試行から通知が届きます。

## 関連

- [api.ja.md](api.ja.md): 支払い、`transfer`、支払いを読み戻す API エンドポイント
- [refunds.ja.md](refunds.ja.md): `refund.` の event が運ぶ返金
- [operating.ja.md](operating.ja.md): 失敗している通知についての `suco doctor` の出力
