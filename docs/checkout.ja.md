# Checkout

English: [checkout.md](checkout.md)

suco Checkout は、支払者が開く支払いページです。支払者を Checkout へ案内し、加盟店のサイトへ
戻す方法と、ページ API の仕様を説明します。

[API リファレンス](api.ja.md) に従って支払いを作成できることを前提にします。

## 提供状況

> **重要:** Checkout とウォレットを接続するブラウザー用モジュールは未公開です。現在のページは
> 支払いの内容を表示しますが、署名の要求や取引の送信はできません。署名フローの確認には
> `suco payment await <id>` を使ってください。

以下で説明するページ API と支払いの状態管理は利用できます。本番導入を計画する前に
[ロードマップ](../ROADMAP.ja.md) を確認してください。

## 支払いページでの流れ

1. 加盟店のサーバーが支払いを作成し、`checkout_url` へ支払者をリダイレクトします。
2. Checkout が `GET /checkout/{token}/state` で支払いを取得して表示します。
3. Checkout が `POST /checkout/{token}/attempts` から支払い試行を取得します。
4. 支払者のウォレットが、指定額の EIP-3009 `TransferWithAuthorization` に署名して取引を
   送信します。ガス代は支払者が負担します。
5. suco Pay が送金を検出し、確定を待ちます。Checkout は現在の
   [支払い状態](api.ja.md#支払いの状態) を表示します。
6. `return_url` が設定されていれば、Checkout が支払者をリダイレクトします。

支払者には、支払い先の `network` で EIP-712 typed data に署名できるウォレットが必要です。
Checkout は、ネットワーク、残高、送金できる状態かをウォレットから確認します。suco Pay が
支払者の秘密鍵を受け取ることはありません。

## 支払いページの表示項目

Checkout は、加盟店名、金額、資産、期限、状態、戻り先を表示します。送金を検出した後は、
送金情報も表示します。

`metadata` と受取アドレスは表示しません。受取アドレスはウォレットへ渡す typed data に含まれ、
取引所から手動送金するためのアドレスとしては表示されません。

## リダイレクトと戻り先

`POST /payments` と `GET /payments/{id}` は、同じ `checkout_url` を返します。支払者を
リダイレクトするか、リンクを提供してください。URL は
`<listen.base_url>/checkout/<token>` です。パスのトークンがアクセスを許可するため、
API の資格情報は求めません。

1 つの支払いが持つ Checkout URL は 1 つです。URL を取得できなくなった場合は
`GET /payments/{id}` で支払いを
取得してください。インスタンスがページ配信に対応する前に作成した支払いには、`checkout_url` が
ありません。

> **重要:** `checkout_url` を持つ人は支払い結果を参照できます。支払者以外には共有せず、
> ログやアクセス解析にも記録しないでください。

`POST /payments` の `return_url` は、支払いフローを終えた支払者の戻り先です。期限切れや
未対応のウォレットなど、支払いを続行できない場合にも使います。指定は任意です。URL は
`https` を使い、ユーザー名とパスワードを含めず、2048 バイト以内としてください。開発用には
`http://localhost` と `http://127.0.0.1` も利用できます。

suco Pay は `return_url` にクエリパラメーターやデータを追加しません。ブラウザーが戻り先へ
到達しても、支払い成功の証明にはなりません。加盟店のサーバーで Webhook または
`GET /payments/{id}` を使って結果を確認してください。

## 支払いページの API エンドポイント

`/checkout/` 以下の API エンドポイントは、資格情報の代わりにパスのトークンを使います。
レスポンスのセキュリティヘッダーと Content-Security-Policy は、
[API エンドポイントの一覧](api.ja.md#api-エンドポイントの一覧) を参照してください。
`/checkout-assets/` 以下の静的ファイルは公開され、`X-Content-Type-Options: nosniff` だけが
付きます。

| パス | 説明 |
|---|---|
| `GET /checkout/{token}` | Checkout の HTML を返す。無効なトークンには `404` ページを返す |
| `GET /checkout/{token}/state` | Checkout が表示するデータを JSON で返す |
| `POST /checkout/{token}/attempts` | 支払者の署名データを作成または取得する |
| `GET /checkout-assets/{path}` | ブラウザー用モジュールがある場合に、Checkout のスクリプトまたはスタイルシートを返す |

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

| 項目 | 説明 |
|---|---|
| `status` | `awaiting_payment`、`awaiting_finality`、`succeeded`、`expired`、`failed` のいずれか。支払者が支払いを始める必要がある点は同じため、`created` は `awaiting_payment` として表示する |
| `amount`、`asset` | 支払いの金額と資産。ウォレットのネットワーク選択に使う `chain_id` を含む |
| `expires_at` | 支払いの期限 |
| `merchant.name` | アカウントの名前 |
| `return_url` | 戻り先。無ければ `null` |
| `attempt` | 有効な支払い試行の署名データ。無ければ `null` |
| `result` | 支払いに見つかった送金。無ければ `null` |
| `reason` | Checkout が支払い試行を作成できない理由。作成できる場合は `null`。値は下の表 |
| `slower` | ネットワークの読み取りが遅れ、通常より確認に時間がかかる場合は `true` |

`result` は `tx`、`block_height`、`block_time`、資産の最小単位で表した `value`、
`confirming`、`received_at` を含みます。支払いが成功するまで `confirming` は `true` です。
成功すると `received_at` が入ります。

| `reason` | 意味 |
|---|---|
| `expired` | 期限を過ぎた |
| `closing` | 残りが 2 分未満で、署名にかかる時間より短い |
| `paid` | 送金が見つかり、確定を待っている |
| `done` | 支払いが終わった |
| `not_ready` | インスタンスが支払いの `network` をまだ読んでいない |
| `reissued` | 支払いごとに 1 回だけ作成できる代替の支払い試行を、すでに作成した |
| `again` | 複数のリクエストが競合し、有効な支払い試行を返せなかった。リクエストを再送する。attempts の API エンドポイントだけが返す |

### attempts

現在の支払い試行を作成または取得する場合は、空の本文を送ります。新しい支払い試行へ置き換える
場合は `{"reissue": "<attempt_id>"}` を送ります。ほかの本文には `400` を返します。

1. 有効な支払い試行があり、`reissue` がなければ、`200` でその支払い試行を返します。
2. `reissue` が有効な支払い試行を指していなければ、`400` を返します。
3. 支払い試行を作成できない場合は `409 {"error": "<reason>"}` を返し、支払いは変更しません。
4. それ以外の場合は支払い試行を作成し、`201` で返します。`created` の支払いは、先に
   `awaiting_payment` へ変更します。

Checkout を再読み込みしても、支払い試行は増えません。後続のリクエストは、既存の有効な
支払い試行を返します。

### 失敗

| ステータス | `error` | いつ | 対処 |
|---|---|---|---|
| 400 | `invalid` | `attempts` の本文が空でも `reissue` でもない | 空の本文か `{"reissue": "<attempt_id>"}` を送る |
| 404 | `not_found` | トークンが不正、存在しない、または保持期間を過ぎている。HTML の API エンドポイントは代わりに `404` ページを返す | `GET /payments/{id}` が返した `checkout_url` と照合する |
| 409 | `reason` の値 | Checkout が支払い試行を作成できない | 上の表から `reason` に応じた対応を確認する |
| 413 | `too_large` | `attempts` の本文が 1 KiB を超えている | 空の本文、または 1 つの `reissue` 識別子を送る |
| 503 | `unavailable` | suco Pay がデータベースに接続できない | [運用ガイド](operating.ja.md)に従って対処する |

## 署名

支払い試行には、Checkout が `eth_signTypedData_v4` に渡す typed data が入ります。`from` だけは
含まれず、Checkout が支払者のアドレスで補います。`domain` と `message` のキー名は仕様どおりの
camelCase です。`id` は表示と `reissue` に使う識別子で、署名対象には含まれません。

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

`value` は `amount` と異なり、資産の最小単位で表します。`validBefore` は支払期限の Unix
タイムスタンプです。`nonce` は再利用を防ぎます。`domain` は資産の `eip712` 設定から取得し、
`suco asset accept` がコントラクトと照合します。

各 `nonce` は 1 回だけ使用できます。ウォレットが、suco Pay まで届かなかった取引で `nonce` を
消費した場合、Checkout は支払いごとに 1 回だけ代替の支払い試行を作成できます。その代替も
利用できない場合は、支払者を加盟店へ戻します。

## 期限

| 期限 | いつ | 過ぎると |
|---|---|---|
| 支払いの期限 | `expires_at` | Checkout は支払い試行の作成を止め、期限切れを表示する。送信済みの取引があれば結果を引き続き表示する |
| ページの保持期間 | 支払いが最終状態になってから 30 日 | `checkout_url` が `404` を返す |

支払いは `succeeded`、`expired`、`failed` のどれかで終わります。支払いページは、支払いが
終わるまでと、終わってから 30 日のあいだ読めます。`checkout_url` を持っている支払者が結果を
読めるようにするためです。終わっていない支払いに支払いページの期限は無く、支払いの期限の後に
`expired` か `succeeded` になります。

## セキュリティ

Checkout は iframe 内では開きません。トップレベルのページとして開いてください。ページの
API エンドポイントが返すセキュリティヘッダーは、
[API エンドポイントの一覧](api.ja.md#api-エンドポイントの一覧)を参照してください。

## 次のステップ

- [Webhook](webhooks.ja.md) を実装し、加盟店のサーバーで最終的な支払い状態を確認します。
- 支払いフロー全体を確認した後、[返金](refunds.ja.md) を組み込みます。
