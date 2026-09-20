# API

English: [api.md](api.md)

HTTP API で支払いを作成し、状態を取得して、返金を開始できます。加盟店のサーバーを
suco Pay と組み込む開発者向けのリファレンスです。

## 始める前に

[はじめかた](../README.ja.md#はじめかた) を完了し、次の準備をしてください。

- `suco serve` が起動している
- `suco asset accept` で資産と受取ウォレットを登録している
- `suco credential new --read-write` で read-write の API トークンを作成している

以下のパスはすべて `listen.base_url` からの相対パスです。ローカル開発時の既定値は
`http://localhost:7826` です。

> **重要:** Checkout とウォレットを接続するブラウザー用モジュールは未公開です。開発初期の間は、
> `suco payment await <id>` で支払いフローを確認してください。

## 支払いの流れ

1. 加盟店のサーバーが `POST /payments` で支払いを作成します。
2. レスポンスの `checkout_url` へ支払者を案内します。
3. Checkout が、EIP-712 typed data を含む支払い試行を作成します。開発初期の間は、代わりに
   `suco payment await <id>` を実行します。
4. 支払者のウォレットが、指定額の EIP-3009 `TransferWithAuthorization` に署名して送信します。
5. suco Pay が送金を検出し、支払いの `transfer` に記録して確定を待ちます。
6. 送金が確定すると、支払いは `succeeded` になります。API で `succeeded` を取得するか、
   `payment.succeeded` の通知を受け取ってから商品やサービスを提供してください。

支払者側の動作は [Checkout](checkout.ja.md)、サーバー側で確実に結果を受け取る方法は
[Webhook](webhooks.ja.md) を参照してください。

## 支払いの状態

| `status` | 入り方 | 出方 | 終点 |
|---|---|---|---|
| `created` | API が支払いを作成した | `awaiting_payment` | いいえ |
| `awaiting_payment` | Checkout が最初の支払い試行を作成したか、運用者が `suco payment await` を実行した | `succeeded`、`failed`、`awaiting_finality` | いいえ |
| `awaiting_finality` | 支払期限を過ぎたが、送信済みの送金が確定する可能性がある | `succeeded`、`expired` | いいえ |
| `succeeded` | 支払いの送金が確定した | なし | はい |
| `failed` | 確定できない支払いのための予約済み状態。現在のリリースではこの状態へ遷移しない | なし | はい |
| `expired` | 支払期限までのブロックを処理しても、対象の送金が見つからなかった | なし | はい |

検出した送金は、まだ確定していません。チェーンの再編成（reorg）で消える可能性があります。
支払いが `succeeded` になるのは、送金が確定した後です。

期限直前に送信した送金は、期限後に到着する場合があります。その場合、suco Pay は
`awaiting_finality` で確定を待ちます。加盟店側の時計だけで注文を終了しないでください。

## 認証

認証が必要なリクエストには、API トークンを送ります。

```http
Authorization: Bearer <token>
```

API トークンは `suco credential new` で作成します。各トークンは 1 つのアカウントに属し、
read-only または read-write の権限を持ちます。現在、1 つのインスタンスが扱えるアカウントは
1 つです。

データを変更する API エンドポイントには read-write 権限が必要です。読み取り専用の
API エンドポイントは、どちらの権限でも呼び出せます。トークンがない場合や無効な場合は `401`、
権限が足りない場合は `403` を返します。

## 資産と金額

資産は、1 つの `network` 上の 1 つのトークンです。Polygon 上の JPYC が例です。資産は
`network` と `reference` で識別します。EVM の `reference` はトークンコントラクトのアドレスです。
表示用の `symbol` は重複する可能性があるため、識別子として使わないでください。

`suco.yaml` は、設定した資産にローカルな名前を付けます。リクエストではその名前を使います。
保存済みの支払いは `network` と `reference` を保持するため、後から設定を変更しても資産は
変わりません。

API の金額は資産の表示単位です。たとえば、`"1000"` は 1000 JPYC を表します。チェーン上の
送金額は最小単位です。`decimals` が 18 の場合、1 JPYC は最小単位の
1000000000000000000 です。

浮動小数点の丸めを避けるため、金額は JSON の文字列で表します。

## POST /payments

API トークンに紐づくアカウントの支払いを作成します。

```http
POST /payments
Authorization: Bearer <token>
Content-Type: application/json

{
  "asset": "jpyc",
  "amount": "1000",
  "expires_at": "2026-09-07T12:00:00Z",
  "return_url": "https://shop.example/orders/A-1",
  "metadata": {"order": "A-1"}
}
```

| パラメータ | 型 | 必須 | 説明 |
|---|---|---|---|
| `asset` | string | 必須 | `suco.yaml` に定義した資産名。事前に `suco asset accept <name> <address>` でアカウントへ登録する |
| `amount` | string | 必須 | 資産の表示単位で表した金額。たとえば `"1000"` は 1000 JPYC。符号と指数を使わず、0 より大きい 10 進数を指定する。小数部は `decimals` 以下、最小単位へ変換した値は 78 桁以下 |
| `expires_at` | string | 任意 | 現在より後、30 日以内の RFC 3339 形式の時刻。既定値は作成から 15 分後 |
| `return_url` | string | 任意 | Checkout から支払者を戻す URL。`https` を使い、ユーザー名とパスワードを含めず、2048 バイト以内とする。`http://localhost` と `http://127.0.0.1` も利用できる。詳しくは [Checkout](checkout.ja.md) を参照 |
| `metadata` | object | 任意 | 文字列のキーと値を 20 件まで指定できる。キーは 64 バイト、値は 512 バイトまで。API は内容を変更せずに返し、省略時は `{}` とする。suco Pay のログには記録しない |

未知のキーには `400` を返します。リクエスト本文は 64 KiB までです。

レスポンスは `201` です。`Location` に支払いのパス、本文に支払いオブジェクトが入ります。

```http
HTTP/1.1 201 Created
Location: /payments/6a5c95937c5ea5c727ebffb682c12a38
Content-Type: application/json

{
  "id": "6a5c95937c5ea5c727ebffb682c12a38",
  "status": "created",
  "asset": {
    "network": "local",
    "reference": "0x0000000000000000000000000000000000000001",
    "symbol": "JPYC",
    "decimals": 18
  },
  "amount": "1000",
  "received": null,
  "destination": "0x00000000000000000000000000000000000000aa",
  "metadata": {"order": "A-1"},
  "expires_at": "2026-09-07T12:00:00Z",
  "created_at": "2026-09-05T23:08:53.514971Z",
  "return_url": "https://shop.example/orders/A-1",
  "checkout_url": "http://localhost:7826/checkout/2c2f…",
  "transfer": null,
  "refunded": "0"
}
```

| 項目 | 型 | 説明 |
|---|---|---|
| `id` | string | 支払いの識別子。`GET /payments/{id}` などのパスに入れる |
| `status` | string | 現在の支払い状態。上の表のいずれか |
| `asset` | object | 支払いの資産。`network` と `reference` で識別し、`symbol` と `decimals` を添える。`suco.yaml` で付けた名前は返らない |
| `amount` | string | 請求額。資産の表示単位 |
| `received` | string または null | 受取額。資産の表示単位。送金を検出するまでは `null` |
| `destination` | string | 受取アドレス。`suco asset accept` が資産に記録したアドレス |
| `metadata` | object | リクエストで指定したメタデータ。省略時は `{}` |
| `expires_at` | string | 期限。期限を過ぎると支払い可能でなくなる。UTC |
| `created_at` | string | 支払いを作った時刻。UTC |
| `return_url` | string または null | 支払いページが支払者を戻す URL。リクエストで省くと `null` |
| `checkout_url` | string | 支払いページの URL。インスタンスが Checkout ページを配信する前に作成した支払いでは省略される |
| `transfer` | object または null | 支払いに対応する送金。詳細は後述 |
| `refunded` | string | 返金用に確保した金額。資産の表示単位。返金を作成するまでは `"0"` |

`checkout_url` を知っている人は支払い結果を参照できます。この URL は秘密情報として扱い、
ログに残さないでください。suco Pay も自身のログと Webhook イベントから除外します。

## GET /payments/{id}

認証したアカウントの支払いを 1 件返します。成功時は `200` で、`POST /payments` と同じ形式の
オブジェクトが入ります。

無効な ID、存在しない支払い、別のアカウントが所有する支払いには、同じ `404` レスポンスを
返します。

## 返金

返金では、支払いで受け取った資金を元の送金元アドレスへ戻します。受取ウォレットで返金に署名
すると、suco Pay が送金を記録して確定まで監視します。詳しい手順と状態遷移は
[返金](refunds.ja.md)を参照してください。

`POST /payments/{id}/refunds` の `amount` は、資産の表示単位で指定します。未返金額の全額を
返す場合は省略します。呼び出し側は返金先を指定できません。suco Pay は支払い送金の送金元を
使います。返金 API も、以下の規則で `Idempotency-Key` を処理します。

レスポンスの `refund_url` で、運用者が返金に署名します。URL を持つ人は返金を参照できるため、
ログには記録しないでください。suco Pay もログと Webhook イベントから除外します。

## Idempotency-Key

再送による支払いまたは返金の重複作成を防ぐには、`Idempotency-Key` ヘッダーを付けます。

```http
Idempotency-Key: 8e03978e-40d5-43e8-bc93-6894a57f9324
```

新しいリクエストごとにランダムなキーを生成してください。UUID が一般的です。引用符で囲んだ
ヘッダー値も受け付けますが、引用符自体はキーに含めません。値は印字可能な ASCII で
1〜255 バイトとし、引用符と `\` は使えません。ヘッダーを重複して送ると `400` を返します。

使用済みのキーを再送すると、最初のリクエストが作成したリソースを返します。ステータスは
`201`、`Location` は初回と同じで、`Idempotent-Replayed: true` が付きます。本文には初回の
コピーではなく、リソースの現在の状態が入ります。

再送時の本文は、初回とバイト単位で一致する必要があります。JSON キーの順序を変えた場合も別の
本文です。使用済みのキーに異なる本文を付けると、`Idempotency-Key` を指す `problems` と
`400` を返します。初回と同じ本文を再送するか、新しいキーを使ってください。IETF draft が
推奨する `422` ではなく、suco Pay は `400` を使います。

```json
{
  "error": "invalid",
  "problems": [
    {
      "field": "Idempotency-Key",
      "message": "already used for another body. Send that body, or a key nothing has used"
    }
  ]
}
```

キーは、作成したリソースと同じ期間保持します。拒否したリクエストのキーは保持しないため、
本文を修正して再送できます。同じキーのリクエストが同時に届いた場合、一方が処理を待ち、
もう一方が作成したリソースを返します。

キーを読む API エンドポイントは `POST /payments` と `POST /payments/{id}/refunds` だけです。
キーを付けないリクエストでレスポンスを受け取れなかった場合は、再送前に自社の `metadata` を
使ってリソースを検索してください。

## transfer

`transfer` は、支払いに対応するチェーン上の送金です。対象の送金を検出するまでは `null` です。

```json
{
  "tx": "0x9f1e…",
  "block_height": 78123,
  "block_hash": "0x4c2a…",
  "block_time": "2026-09-05T23:10:44Z",
  "from": "0xab…",
  "value": "1000000000000000000000"
}
```

| 項目 | 型 | 説明 |
|---|---|---|
| `tx` | string | ノードで送金を検索するためのトランザクションハッシュ |
| `block_height` | integer | 送金が入っているブロックの高さ |
| `block_hash` | string | ブロックのハッシュ |
| `block_time` | string | ブロックの時刻。UTC |
| `from` | string | 送金元のアドレス。返金先アドレスになる |
| `value` | string | チェーンが動かした額。資産の最小単位 |

`status` が `succeeded` になるまで、`transfer` は確定前です。再編成（reorg）で送金が
チェーンから消えると、`transfer` は `null` に戻ります。

`value` の単位は、`amount` と `received` の単位と違います。`amount` と `received` は資産の
単位です。同じ額が、`amount` では `"1000"`、`value` では `"1000000000000000000000"` です。

`transfer` にチェーンと受取アドレスは入りません。チェーンは `asset.network` にあり、受取
アドレスは `destination` にあります。支払いを払った送金の行き先は必ず `destination` です。

`amount` より少ない送金では支払いは完了しません。送金額は `received` に加算されますが、
`transfer` は `null` のままです。

## 失敗

すべての API エラーは次の形式で返します。

```json
{
  "error": "invalid",
  "problems": [
    {
      "field": "amount",
      "message": "\"1e3\" is not an amount: digits, and after a point more digits"
    }
  ]
}
```

| ステータス | `error` | いつ | 対処 |
|---|---|---|---|
| 400 | `invalid` | リクエスト本文、項目の値、または `Idempotency-Key` が不正 | `problems` に示された箇所を修正して再送する |
| 401 | `unauthorized` | API トークンがないか、無効になっている | 有効なトークンを送る |
| 403 | `forbidden` | トークンに必要な権限がない | 書き込み操作には read-write トークンを使う |
| 404 | `not_found` | 指定した支払い、返金、Webhook エンドポイントがアカウントに存在しない | 識別子と、トークンに紐づくアカウントを確認する |
| 409 | 理由別 | Checkout が支払い試行を作成できない | 理由は [Checkout](checkout.ja.md) を参照。`POST /checkout/{token}/attempts` だけが返す |
| 413 | `too_large` | リクエスト本文が上限を超えている | 本文を小さくする |
| 503 | `unavailable` | suco Pay がデータベースに接続できない | サービスのログを確認し、[運用ガイド](operating.ja.md)に従って対処する |

`problems` が入るのは `400` レスポンスだけです。

### 問題

各問題の `field` は、不正な本文のキーまたはヘッダーを示します。`message` は違反した制約を
説明します。本文全体の問題には `field` がありません。

```json
{
  "error": "invalid",
  "problems": [
    {
      "message": "body ends inside the JSON"
    }
  ]
}
```

`message` 内にリクエストの値を含める場合、その値は 128 バイトで切り詰めます。`field` と
`message` はそれぞれ 512 バイトまでです。制御文字や不可視文字を含む場合は、値全体を引用符で
囲み、該当する文字をエスケープします。

検証は段階ごとに行います。最初に失敗した段階の問題をすべて返し、それ以降は検証しません。
ヘッダーの構文は最初の段階で検証するため、本文の形式エラーと同じレスポンスに入る場合があります。

1. 本文の構造。未知のキー、不正な型、`suco.yaml` にない資産、不正な金額や時刻、必須キーの
   欠落を検証します。
2. アカウントが資産を受け付けているか。
3. 値の制約。0 の金額、許容範囲外の期限、上限を超えた `metadata` を検証します。

最初に失敗した段階で見つかった問題はすべて返すため、クライアントはまとめて修正できます。

```json
{
  "error": "invalid",
  "problems": [
    {
      "field": "note",
      "message": "unknown key"
    },
    {
      "field": "expires_at",
      "message": "yesterday is not an RFC 3339 time"
    }
  ]
}
```

## API エンドポイントの一覧

| パス | 資格情報 | 説明 |
|---|---|---|
| `POST /payments` | read-write | 支払いを作る |
| `GET /payments/{id}` | read-only | 支払いを 1 つ読み戻す |
| `POST /payments/{id}/refunds` | read-write | `succeeded` の支払いに返金を 1 つ作る |
| `GET /payments/{id}/refunds/{refund}` | read-only | 返金を 1 つ読み戻す |
| `/webhook_endpoints…` | read-only か read-write | Webhook エンドポイントの登録、変更、通知の確認。一覧と、API エンドポイントごとの資格情報は [webhooks.ja.md](webhooks.ja.md) |
| `/checkout/…` と `/checkout-assets/…` | 不要 | 支払いページと、支払いページが呼ぶ API エンドポイント。一覧は [checkout.ja.md](checkout.ja.md) |
| `/refund/…` と `/refund-assets/…` | 不要 | 署名ページと、署名ページが呼ぶ API エンドポイント。一覧は [refunds.ja.md](refunds.ja.md) |
| `GET /healthz` | 不要 | プロセスが動いているか。詳細は [operating.ja.md](operating.ja.md) |
| `GET /readyz` | 不要 | インスタンスが仕事をできるか。詳細は [operating.ja.md](operating.ja.md) |

支払いページと署名ページの API エンドポイントは、パスのトークンを持つ人からの呼び出しを
受け付け、資格情報を求めません。`/checkout/` と `/refund/` の下のレスポンスには、
次のヘッダーが付きます。

- `Cache-Control: no-store`
- `Referrer-Policy: no-referrer`
- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`
- Content-Security-Policy。ページが自分のインスタンスにだけ届き、iframe の中で開かれない
  ように制限する

CORS のヘッダーは付きません。呼ぶのはページ自身だけです。`/checkout-assets/` と
`/refund-assets/` の下のスクリプトとスタイルは誰でも読めます。キャッシュしてよく、ヘッダーは
`nosniff` だけです。

ほかのアカウントの Webhook エンドポイント、存在しない Webhook エンドポイント、形を成さない
識別子は、支払いと同じく `404` です。

## 次のステップ

1. [Checkout](checkout.ja.md) に従い、支払者を支払いページへ案内します。
2. [Webhook](webhooks.ja.md) を実装し、加盟店のサーバーで支払い結果を確定します。
3. 支払いフロー全体を確認した後、[返金](refunds.ja.md) を組み込みます。
