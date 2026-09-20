# Webhook

English: [webhooks.md](webhooks.md)

支払いや返金の状態が変わると、suco Pay は登録済みの URL へ HTTP `POST` を送ります。
Webhook エンドポイントの登録、署名検証、重複処理の防止、再送、通知履歴の確認方法を説明します。

外部から HTTPS で接続できる受信側と、read-write の API トークンが必要です。先に
[API リファレンス](api.ja.md) に従ってテスト用の支払いを作成してください。

## 組み込みの順序

1. 受信側の URL を登録し、署名シークレットをシークレット管理サービスに保存します。
2. Standard Webhooks のライブラリで各リクエストの署名を検証します。
3. `webhook-id` を永続化してから `2xx` を返し、イベントを非同期に処理します。
4. テスト用のイベントを送り、通知履歴を確認します。
5. アプリケーションが処理するイベントだけを購読します。

## 受信側の要件

1. **署名は、使用言語向けの Standard Webhooks ライブラリで検証してください。** 独自に
   署名検証を実装しないでください。timestamp の許容差は 5 分です。ヘッダーは
   `webhook-id`、`webhook-timestamp`、`webhook-signature` です。署名シークレットは、
   登録時に 1 度だけ返される `whsec_…` の値です。
2. **`webhook-id` をキーにして、同じ通知を 2 度処理しないでください。** `webhook-id` は
   同じ通知のどの試行でも同じで、再送と手動の再送でも変わりません。
3. **イベントを永続化するかキューへ入れてから `2xx` を返してください。** 接続時間を含む
   20 秒以内に両方を完了し、業務処理は非同期で実行します。20 秒を過ぎたレスポンスは
   失敗した試行として扱い、再送します。
4. **商品やサービスを提供するのは、`payment.succeeded` を受けた後です。**
   `attempt.confirming` は送金の検出だけを示します。検出した送金が確定しない場合もあります。
5. **注文を終了するのは、`payment.expired` を受けた後です。** 加盟店側の時計だけで期限切れを
   判断しないでください。期限前に送信された送金が、まだチェーンに反映されていない場合があります。
6. **イベントの到着順ではなく、`data.status` で判断してください。** 再送や手動の再送により、
   古いイベントが新しいイベントより後に届く場合があります。判断できない場合は
   `GET /payments/{id}` で最新状態を取得してください。
7. **Webhook エンドポイントのパスを CSRF 保護の対象外にしてください。** Webhook はブラウザーの
   Cookie やフォームトークンではなく署名で検証するため、一般的な CSRF ミドルウェアでは
   拒否される場合があります。
8. **署名シークレットを保護してください。** 機密情報として保存し、ログには記録しません。
   紛失または漏えいした場合は更新してください。更新のレスポンスにも新しい値が 1 度だけ出ます。

## イベント

イベントは、支払いまたは返金の 1 回の変化を表します。1 つのイベントを 1 つの
Webhook エンドポイントへ送る単位が通知（delivery）で、通知ごとの HTTP `POST` が
通知の試行（attempt）です。イベントの本文は次の形です。

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
| `type` | string | 下表に示すイベントの種類 |
| `timestamp` | string | イベントが発生した時刻。RFC 3339 形式、UTC |
| `account` | string | 支払いか返金が属するアカウント |
| `data` | object | 支払いか返金。API が返すのと同じ形 |

`payment.` と `attempt.` のイベントの `data` は、`GET /payments/{id}` が返すのと同じ形の
支払いで、`metadata` も入ります。`checkout_url` は入りません。`checkout_url` を持つ人は
支払いの結果を読めます。受信側のログにも残さないでください。`refund.` のイベントの `data` は、
`GET /payments/{id}/refunds/{refund}` が返すのと同じ形の返金です。同じ理由で `refund_url` も
入りません。返金オブジェクトの項目は [返金](refunds.ja.md)を参照してください。

項目は増えるだけで、消えたり名前が変わったりしません。必要な項目だけを読み、ほかは
無視してください。

| `type` | いつ | `data` |
|---|---|---|
| `payment.awaiting_payment` | 支払いが可能になった | 支払い |
| `attempt.confirming` | suco Pay が送金を検出したが、まだ確定していない | 支払い |
| `payment.succeeded` | 支払いの送金が確定した | 支払い |
| `payment.expired` | 対象の送金がないまま支払いが期限切れになった | 支払い |
| `payment.failed` | 支払いを確定できない状態になった | 支払い |
| `refund.succeeded` | 返金の送金が確定した | 返金 |
| `refund.expired` | 送金が確定せずに返金が期限切れになった | 返金 |
| `endpoint.test` | `POST /webhook_endpoints/{id}/test` を呼んだ | `{}` |

送金が見つかると、`data` は `transfer` を持ちます。`transfer` は `tx`、`block_height`、
`block_hash`、`block_time`、`from`、`value` で、加盟店が自分のノードで検証できる値です。
`transfer` を最初に持つイベントが `attempt.confirming` です。`attempt.confirming` は送金が
見えたことだけを示し、確定したことは示しません。送金は再編成（reorg）で消えることがあります。
支払いの確定を示すのは `payment.succeeded` です。`value` は `amount` や
`received` と同じ単位ではありません。項目の意味と単位の規則は [api.ja.md](api.ja.md) に
あります。

## Webhook エンドポイントの登録

```http
POST /webhook_endpoints
Authorization: Bearer <token>
Content-Type: application/json

{"url": "https://shop.example/webhooks/suco", "description": "orders", "events": ["payment.succeeded", "payment.expired"]}
```

| パラメータ | 型 | 必須 | 説明 |
|---|---|---|---|
| `url` | string | 必須 | HTTPS URL。2048 バイトまで。ユーザー名とパスワードは指定できない。suco Pay が動いているネットワーク内のアドレスへ解決される URL は拒否する |
| `description` | string | 任意 | 管理用のメモ。200 バイトまで |
| `events` | string の配列 | 任意 | 受信するイベントの種類。省略すると、今後追加される種類を含めてすべて受信する |

レスポンスは `201` で、Webhook エンドポイントと署名シークレット（`secret`）が入ります。
API が署名シークレットを返すのは 1 度だけです。レスポンスを破棄する前に保存してください。
1 つのアカウントが持てる Webhook エンドポイントは 8 つまでです。

無効にした Webhook エンドポイント（`"enabled": false`）には新しい通知を作成しません。
送信待ちの通知はキューに残り、エンドポイントを再び有効にすると送信を再開します。

## Webhook エンドポイントの API

| パス | 資格情報 | 説明 |
|---|---|---|
| `POST /webhook_endpoints` | read-write | 登録し、署名シークレットを受け取る |
| `GET /webhook_endpoints` | read-only | 一覧。署名シークレットは入らない |
| `GET /webhook_endpoints/{id}` | read-only | 1 つ読む |
| `PATCH /webhook_endpoints/{id}` | read-write | `url`、`description`、`events`、`enabled` を変える |
| `POST /webhook_endpoints/{id}/secret` | read-write | 署名シークレットを更新する。古い署名シークレットはあと 24 時間、検証に使える |
| `DELETE /webhook_endpoints/{id}` | read-write | 消す。待っていた通知は `failed` になる |
| `POST /webhook_endpoints/{id}/test` | read-write | `endpoint.test` を 1 件送る。`202` で通知 ID を返す。前のテストが pending の間は次のテストを拒否する |
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

10 回目の試行後、最初の送信から 3 日強で通知は `failed` になり、自動再送を終了します。
`3xx` のリダイレクトは追跡せず、失敗した試行として扱います。

同じ Webhook エンドポイントへの同じ支払いの通知は、イベントの起きた順に送ります。後の通知は、
先の通知が delivered か failed になるまで待ちます。別の支払いの通知は待ちません。

失敗が続いても Webhook エンドポイントは無効になりません。原因は通知履歴で確認できます。
`suco doctor` も failed と pending の通知数を表示します。

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
| `type` | string | イベントの種類 |
| `payment` | string または null | イベントが指す支払い。`endpoint.test` では `null` |
| `occurred_at` | string | イベントが起きた時刻 |
| `state` | string | `pending`、`delivered`、`failed` のどれか |
| `attempts` | array | 通知の全部の試行。古い順 |
| `attempts[].at` | string | 試行の時刻 |
| `attempts[].status` | integer または null | 受信側が返した HTTP ステータス。レスポンスが無かったときは `null` |
| `attempts[].reason` | string | レスポンスが無かった理由。`status` が `null` のときだけ |
| `attempts[].response` | string | 受信側が返した本文の先頭 256 バイト |
| `attempts[].took_ms` | integer | 試行にかかった時間。ミリ秒 |
| `next_at` | string または null | 次の試行の予定。pending の間だけ |
| `delivered_at` | string または null | `2xx` を受け取った時刻 |

| `reason` | 意味 |
|---|---|
| `timeout` | 20 秒以内にレスポンスが無かった |
| `connection` | 接続できなかったか、途中で切れた |
| `destination` | URL が登録時の検査に通らなくなった |
| `secret` | 署名シークレットを suco Pay が読めなくなった。下の節を参照 |

delivered と failed の通知は 30 日残ります。

## 手動の再送

`POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` は、delivered または failed の通知を
再送します。`webhook-id` は変えず、新しい `webhook-timestamp` と署名を付け、既存の試行履歴へ
追加します。停止していた受信側を復旧したときや、`2xx` を返した後に保存済みデータを失った
ときに使います。`webhook-id` を永続化する受信側は、処理済みの再送を無視できます。pending の
通知は自動送信中のため、手動では再送できません。

## 運用者による鍵の入れ替え

署名シークレットは、`credentials.key` から導いた鍵で暗号化して保存されています。運用者が
`credentials.key` を入れ替えると、以前の署名シークレットを復号できなくなります。その
エンドポイントへの送信試行には `secret` が記録され、`suco doctor` が影響を受ける件数を表示
します。`POST /webhook_endpoints/{id}/secret` で署名シークレットを更新すると、現在の鍵で新しい
値を暗号化し、次の試行から送信を再開します。

## 次のステップ

- [返金](refunds.ja.md)を組み込むときに、`refund.succeeded` と `refund.expired` の処理を
  追加します。
- Webhook の失敗を調査できるよう、運用者に
  [`suco doctor` の通知診断](operating.ja.md#suco-doctor) を共有します。
