# 返金

English: [refunds.md](refunds.md)

返金は、確定した支払いの全額または一部を、支払元のアドレスへ戻します。返金の作成、署名、
状態の追跡方法を説明します。

`succeeded` の支払いと、受取アドレスとして登録したウォレットが必要です。ウォレットは
EIP-712 typed data の署名に対応している必要があります。

## 提供状況

> **重要:** 署名ページとウォレットを接続するブラウザー用モジュールは未公開です。現在のページは
> 返金内容を表示しますが、署名や取引の送信はできません。

以下で説明する返金 API、状態管理、ページ API は利用できます。本番導入を計画する前に
[ロードマップ](../ROADMAP.ja.md) を確認してください。

## 署名ページでの流れ

1. 加盟店のサーバーが `POST /payments/{id}/refunds` で返金を作成します。
2. レスポンスの `refund_url` を、受取ウォレットを管理する担当者が開きます。
3. 署名ページが `GET /refund/{token}/state` で返金を取得します。
4. 受取ウォレットが EIP-712 の `authorization` に署名し、取引を送信します。
5. suco Pay が送金を検出し、確定を待ちます。
6. 返金が `succeeded` になると、支払元のアドレスへ資金が戻ります。

suco Pay は資金を預からず、返金取引も送信しません。

## 返金の状態

| `status` | 入り方 | 出方 | 終点 |
|---|---|---|---|
| `created` | API が返金を作成し、署名できる状態になった | `succeeded`、`awaiting_finality` | いいえ |
| `awaiting_finality` | 期限を過ぎたが、送信済みの送金が確定する可能性がある | `succeeded`、`expired` | いいえ |
| `succeeded` | 返金の送金が確定した | なし | はい |
| `expired` | 期限までに対象の送金が確定しなかった | なし | はい |

返金の状態は前に戻りません。送金を検出しただけでは返金は完了せず、送金の確定後に
`succeeded` になります。

| イベント | いつ |
|---|---|
| `refund.succeeded` | チェーンが送金を確定させた |
| `refund.expired` | 送金が確定せずに返金が期限切れになった |

イベントの本文には、取得 API と同じ返金オブジェクトが入り、`refund_url` は含まれません。
`created` と `awaiting_finality` ではイベントを送らないため、取得 API で状態を確認してください。
通知の仕組みは [Webhook](webhooks.ja.md)を参照してください。

## 署名ページの表示項目

署名ページは、金額、資産、署名するウォレット、返金先アドレス、支払い ID、期限、状態を
表示します。返金の送金を検出した後は、送金情報も表示します。

## 返金の作成と追跡

### 署名するウォレット

支払いを受け取るウォレットが、EIP-712 の typed data に署名できる必要があります。返金に署名
するのが、支払いを受け取るウォレットだからです。受取アドレスは
`suco asset accept <name> <address>` が記録します。署名できない受取アドレスでも支払いは
受け取れますが、返金はできません。

### 返金を作る

```http
POST /payments/{id}/refunds
Authorization: Bearer <token>
Content-Type: application/json

{"amount": "250"}
```

`amount` は資産の表示単位で表した文字列です。本文に指定できる項目は `amount` だけです。
返金可能額の全額を返す場合は、空の本文または `{}` を送ります。

返金を作れるのは `succeeded` の支払いだけです。この状態は、支払いの送金が確定したことを
示します。

レスポンスは `201` です。`Location` に返金のパス、本文に返金オブジェクトが入ります。

```json
{
  "id": "5f3c9d1e7a0b4826c1d5e9f3a7b02c48",
  "payment": "9c2f7b1a4e8d0356f9a1c4e7b2d508fa",
  "status": "created",
  "amount": "250",
  "destination": "0xab…",
  "expires_at": "2026-09-20T09:42:11Z",
  "created_at": "2026-09-20T09:12:11Z",
  "transfer": null,
  "refund_url": "https://pay.example/refund/7d1c…"
}
```

| 項目 | 型 | 説明 |
|---|---|---|
| `id` | string | 取得 API で使う返金 ID |
| `payment` | string | 返金対象の支払い ID |
| `status` | string | 現在の返金状態。上の表のいずれか |
| `amount` | string | 返金額。資産の表示単位 |
| `destination` | string | 返金先アドレス |
| `expires_at` | string | 署名できなくなる時刻。UTC |
| `created_at` | string | 返金を作った時刻。UTC |
| `transfer` | object または null | 返金に対応する送金。[API](api.ja.md#transfer)で説明する形式。送金を検出するまでは `null` で、`status` が `succeeded` になるまでは未確定 |
| `refund_url` | string | 運用担当者が返金へ署名する URL。Webhook イベントには含まれない |

`GET /payments/{id}/refunds/{refund}` は同じオブジェクトを返します。一覧 API はないため、
作成時のレスポンスにある返金 ID を保存するか、Webhook イベントから取得してください。

この API は `POST /payments` と同じ規則で `Idempotency-Key` を処理します。詳細は
[API](api.ja.md#idempotency-key) を参照してください。パスにある支払い ID も冪等なリクエストの
一部です。別の支払いにキーを再利用すると `400` を返します。同じ支払いへ同じキーと本文を
再送すると、最初に作成した返金を返します。

`refund_url` は `<listen.base_url>/refund/<token>` の形式です。パスのトークンがアクセスを
許可するため、URL を持つ人は返金を参照できます。ログには記録しないでください。suco Pay も
ログと Webhook イベントから除外し、返金を作成したアカウントにだけ返します。

### 返金先アドレス

呼び出し側は `destination` を指定できません。suco Pay は、支払いを作成した送金の送金元を
返金作成時にコピーします。後からチェーンを読み直しても、署名済みの返金先は変わりません。
支払いに送金がない場合は返金先を決められないため、リクエストを拒否します。

### 残り

支払いは `received` の金額まで返金できます。返金を作成すると、署名の有無にかかわらず、
その金額を直ちに確保します。

`GET /payments/{id}` の `refunded` は、期限切れでない返金が確保している合計です。金額は
資産の表示単位で返します。

```json
{"amount": "1000", "received": "1000", "refunded": "250"}
```

返金可能額は `received - refunded` です。返金可能額を超えるリクエストには `400` を返し、
レスポンスで現在の返金可能額を示します。返金が期限切れになると、確保した金額を解放します。
ただし、受取ウォレットが返金の `nonce` をチェーン上で使用済みの場合、送金が返金の条件と
一致しなくても確保した金額を解放しません。

### 失敗

[API のエラーリファレンス](api.ja.md#失敗) に共通のレスポンス形式があります。返金 API は、
次のエラーを追加します。

| ステータス | `error` | いつ | 対処 |
|---|---|---|---|
| 400 | `invalid` | 支払いが `succeeded` でない、送金がない、返金可能額が不足している、または `Idempotency-Key` が別の支払いに使われている | `problems` に示された箇所を修正して再送する |
| 404 | `not_found` | 指定した支払いまたは返金がアカウントに存在しない | 識別子と、トークンに紐づくアカウントを確認する |
| 413 | `too_large` | 本文が 64 KiB を越えた | `amount` だけの本文か、空の本文を送る |
| 503 | `unavailable` | suco Pay がデータベースに接続できない | [運用ガイド](operating.ja.md)に従って対処する |

## 署名ページの API エンドポイント

`/refund/` 以下の API エンドポイントは、資格情報の代わりにパスのトークンを使います。
レスポンスのセキュリティヘッダーと Content-Security-Policy は、
[API エンドポイントの一覧](api.ja.md#api-エンドポイントの一覧) を参照してください。
`/refund-assets/` 以下の静的ファイルは公開され、`X-Content-Type-Options: nosniff` だけが付きます。

| パス | 説明 |
|---|---|
| `GET /refund/{token}` | 署名ページの HTML を返す |
| `GET /refund/{token}/state` | 署名ページが表示するデータを JSON で返す |
| `GET /refund-assets/{path}` | ブラウザー用モジュールがある場合に、署名ページのスクリプトまたはスタイルシートを返す |

ブラウザー用モジュールがない場合も API エンドポイントは利用できます。無効なトークンには `404`
を返します。HTML の API エンドポイントはエラーページ、state の API エンドポイントは共通の
エラー本文、静的ファイルの API エンドポイントは平文を返します。

### state

```json
{
  "status": "created",
  "payment": "9c2f7b1a4e8d0356f9a1c4e7b2d508fa",
  "amount": "250",
  "asset": {"symbol": "JPYC", "decimals": 18, "network": "polygon", "chain_id": 137},
  "from": "0xab…",
  "to": "0xcd…",
  "expires_at": "2026-09-20T09:42:11Z",
  "authorization": {…},
  "result": null,
  "reason": null
}
```

| 項目 | 説明 |
|---|---|
| `status` | 現在の返金状態 |
| `payment` | 返金対象の支払い ID |
| `amount` | 返金額。資産の表示単位 |
| `asset` | 資産の情報。ウォレットでネットワークを選ぶための `chain_id` を含む |
| `from` | 返金に署名する受取ウォレット |
| `to` | 返金先アドレス |
| `expires_at` | 返金の期限 |
| `authorization` | 署名する EIP-712 typed data。署名できない場合は `null` |
| `result` | 返金に見つかった送金。無ければ `null` |
| `reason` | 署名できない理由。署名できる場合は `null`。値は下の表 |

`result` は `tx`、`block_height`、`block_time`、資産の最小単位で表した `value`、
`settling` を含みます。返金が成功するまで `settling` は `true` です。

`result` に入るのは、返金の条件と一致する送金だけです。別の送金が返金の `nonce` を使った場合、
[残り](#残り) の説明どおり金額の確保を続けますが、`result` は `null` のままです。返金は
`awaiting_finality` に留まります。

| `reason` | 意味 |
|---|---|
| `done` | 返金が終わった |
| `sent` | 送金が見つかり、確定を待っている |
| `expired` | 期限を過ぎた |

## 署名

`authorization` には、ウォレットが補う項目を除いた EIP-712 typed data が入ります。支払いの
署名データとの違いは以下のとおりです。共通する項目は [Checkout](checkout.ja.md#署名)を参照して
ください。

```json
{
  "domain": {"name": "JPY Coin", "version": "1", "chainId": 137,
             "verifyingContract": "0x431d…"},
  "message": {"from": "0xab…", "to": "0xcd…", "value": "250000000000000000000",
              "validAfter": "0", "validBefore": "1789561331", "nonce": "0x…"},
  "primaryType": "TransferWithAuthorization"
}
```

返金には `from` が含まれます。支払いを受け取ったウォレットだけが返金へ署名できるためです。
`validBefore` は `expires_at` の Unix タイムスタンプです。返金には支払い試行の `id` がなく、
再発行もできません。`nonce` は 1 回だけ使用できます。新しい `nonce` が必要な場合は、別の返金を
作成してください。

`value` は返金でも資産の最小単位で、`amount` は資産の単位です。

## 期限

| 期限 | いつ | 過ぎると |
|---|---|---|
| 返金の期限 | `expires_at` | 署名できなくなる。新しい返金を作成する |
| ページの保持期間 | 返金が最終状態になってから 30 日 | `refund_url` が `404` を返す |

返金は作成から 30 分間署名できます。同じ期限で資産のコントラクトも署名を受け付けなくなるため、
期限後に送信した取引では資金を移動できません。

## セキュリティ

署名ページは iframe 内では開きません。トップレベルのページとして開いてください。
[Checkout](checkout.ja.md#セキュリティ) と同じセキュリティヘッダーを使用します。

## 次のステップ

- [Webhook](webhooks.ja.md) に従い、`refund.succeeded` と `refund.expired` を購読します。
- 返金の失敗と署名ページの期限切れを [運用手順書](operating.ja.md) に追加します。
