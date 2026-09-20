# API

English: [api.md](api.md)

このページは、支払い（`payment`）を作り、読み戻し、返金し、Webhook エンドポイントを管理する
HTTP API のリファレンスです。

API を呼ぶ加盟店の開発者向けです。`suco credential new` が書いた資格情報（`credential`）を
持っていることを前提にします。

## このページの語

| 語 | 意味 |
|---|---|
| 支払い（`payment`） | 加盟店が API で作る、受け取り 1 回分の記録。`id` と `status` を持つ |
| 資格情報（`credential`） | 資格情報を求める API エンドポイントを呼ぶときに `Authorization` に載せるトークン。`suco credential new` が書く |
| 支払いページ | 支払者が払うページ。suco が配信し、URL は支払いの `checkout_url` |
| 支払い試行（`attempt`） | 支払いページが支払者に発行する、署名データと id の組 |
| 署名データ | 支払い試行が持つ typed data。支払者のウォレットが署名する。中身は EIP-3009 の `TransferWithAuthorization` |
| 受取アドレス（`destination`） | 支払者が払う先のウォレットのアドレス。`suco asset accept` が資産ごとに記録する |
| 送金（`transfer`） | チェーン上で資産が動いた記録。suco がチェーンから読む |
| 確定 | チェーンが送金をもう取り消さないと分かった状態 |
| account | 資格情報、支払い、返金、Webhook エンドポイントが属する単位。1 つのインスタンスに 1 つ |
| 資産（`asset`） | 1 つの `network` 上の 1 つのトークン。`suco.yaml` が名前を付ける |

## 支払いの流れ

パスはすべて `suco.yaml` の `listen` の `base_url` からの相対です。`base_url` を省くと
`http://localhost:7826` です。

1. 加盟店のサーバーが `POST /payments` で支払いを作ります。レスポンスの `checkout_url` が
   支払いページの URL です。
2. 加盟店のサーバーが支払者を支払いページへ送ります。支払いページの仕組みは
   [checkout.ja.md](checkout.ja.md) にあります。
3. 支払いページが支払い試行を初めて発行したとき、支払いは支払い可能（`awaiting_payment`）に
   なります。運用者が `suco payment await <id>` を実行しても同じです。支払いページの script を
   公開するまでは、`suco payment await` だけが支払者に署名する値を渡せます。未実装の機能は
   [ROADMAP.ja.md](../ROADMAP.ja.md) にあります。
4. 支払者のウォレットが署名データに署名し、取引を送ります。署名データの中身は、ちょうどの
   金額を受取アドレスへ送る EIP-3009 の `TransferWithAuthorization` です。
5. suco がチェーンを読んで送金を見つけ、支払いの `transfer` に書きます。送金はまだ確定して
   いません。
6. チェーンが送金を確定させると、支払いは `succeeded` になります。品物を渡すのは `succeeded`
   になったときです。suco が加盟店のサーバーに知らせる方法は [webhooks.ja.md](webhooks.ja.md)
   にあります。

## 支払いの状態

| `status` | 入り方 | 出方 | 終点 |
|---|---|---|---|
| `created` | `POST /payments` が支払いを作った | `awaiting_payment` | いいえ |
| `awaiting_payment` | 支払いページが支払い試行を初めて発行したか、運用者が `suco payment await` を実行した | `succeeded`、`failed`、`awaiting_finality` | いいえ |
| `awaiting_finality` | 期限が過ぎた。suco がチェーンを期限まで読み終えたかどうか、支払いを払った送金が確定するかどうかは、まだ決まっていない | `succeeded`、`expired` | いいえ |
| `succeeded` | チェーンが送金を確定させた | なし | はい |
| `failed` | `failed` に移す処理は今の build にない。確定せず、待っても変わらない支払いのための状態 | なし | はい |
| `expired` | suco がチェーンを期限の時刻より後まで読んでも、支払いを払った送金が無かった | なし | はい |

チェーンは、送金を見せた後で、送金が残ると分かる状態になります。支払いが `succeeded` になる
のは、送金が残ると分かったときです。suco がチェーンを読む位置は時計より遅れるので、期限の前の
ブロックに入った送金を、期限の後に読むことがあります。支払いは `awaiting_finality` で確定を
待ちます。`awaiting_finality` は注文の終わりではありません。

## 認証

リクエストはすべて資格情報を持ちます。資格情報は `Authorization: Bearer <token>` で送ります。
トークンは `suco credential new` が書きます。資格情報は 1 つの account に属し、read-only か
read-write のどちらかです。account は 1 つです。`suco serve` が初めて起動するとき、表と一緒に
account をデータベースへ書きます。複数の account を扱う機能は実装していません。

何かを変える API エンドポイント（支払いの作成、返金の作成、Webhook エンドポイントの操作）は
read-write の資格情報を求めます。読むだけの API エンドポイントは、read-only でも read-write
でも通ります。API エンドポイントごとの区別は下の一覧にあります。資格情報の無いリクエストと、
失効した資格情報のリクエストは `401` です。資格情報に、呼んだ API エンドポイントの操作をする
権限が無ければ `403` です。read-only の資格情報で書く操作を呼んだときがそうです。

## 資産と金額

資産は、1 つの `network` 上の 1 つのトークンです。Polygon の JPYC が 1 つの資産です。資産は
通貨ではありません。資産を識別するのは `network` と `reference` です。`reference` は、
チェーンがトークンを識別する値で、EVM のチェーンではコントラクトのアドレスです。`symbol` は
資産を識別しません。1 つの `network` に同じ `symbol` のトークンが 2 つあり得ます。2 つの
`network` にある同じ `symbol` のトークンは、2 つの資産です。

`suco.yaml` が、インスタンスが受け取る資産を並べ、資産ごとに名前を付けます。リクエストは
資産を名前で指します。支払いが持つのは `network` と `reference` です。`suco.yaml` がトークンの
名前を変えても、トークンを外しても、支払いは作ったときのトークンのままです。

資産には最小単位があり、1 単位が最小単位の何桁分かを `decimals` が言います。JPYC は
`decimals` が 18 で、1 JPYC は最小単位の 1000000000000000000 です。チェーンは最小単位で
数えます。API は資産の単位で数えます。`"1000"` は 1000 JPYC です。

金額は文字列です。多くのクライアントは JSON の数を浮動小数点で読みます。1000 JPYC を
最小単位で書くと 1000000000000000000000 で、double では正確に持てません。

## POST /payments

資格情報の account に支払いを作ります。

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
| `asset` | string | 必須 | `suco.yaml` が資産に付けた名前。account が資産を受け付けている必要がある。受け付けと受取アドレスは `suco asset accept <name> <address>` が記録する |
| `amount` | string | 必須 | 資産の単位で書いた数。`"1000"` は 1000 JPYC。数字と、小数点があれば小数点の後に数字だけ。符号と指数は付けない。小数の桁は資産の `decimals` まで、最小単位に直して 78 桁まで。0 は断る |
| `expires_at` | string | 任意 | 期限。RFC 3339 の時刻で、現在より後、30 日後まで。省くと 15 分後 |
| `return_url` | string | 任意 | 支払いページが支払者を戻す URL。`https` で、2048 バイトまで、username と password を持たない。`http://localhost` と `http://127.0.0.1` も通す。支払いページの使い方は [checkout.ja.md](checkout.ja.md) にある |
| `metadata` | object | 任意 | 文字列のキーと文字列の値。20 件まで、キーは 64 バイトまで、値は 512 バイトまで。送ったとおりに返り、省くと `{}`。suco のログには残さない |

ほかのキーは拒みます。本文は 64 KiB までです。

レスポンスは `201` です。`Location` に支払いのパスを、本文に支払いを返します。

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
| `status` | string | 支払いの状態。上の表 |
| `asset` | object | 支払いの資産。`network` と `reference` で識別し、`symbol` と `decimals` を添える。`suco.yaml` で付けた名前は返らない |
| `amount` | string | 支払いが求める額。資産の単位 |
| `received` | string か null | 届いた額。資産の単位。何かが届くまでは `null` |
| `destination` | string | 受取アドレス。`suco asset accept` が資産に記録したアドレス |
| `metadata` | object | リクエストの `metadata` と同じ。省くと `{}` |
| `expires_at` | string | 期限。期限を過ぎると支払い可能でなくなる。UTC |
| `created_at` | string | 支払いを作った時刻。UTC |
| `return_url` | string か null | 支払いページが支払者を戻す URL。リクエストで省くと `null` |
| `checkout_url` | string | 支払いページの URL。インスタンスが支払いページを配信する前に作った支払いには入らない |
| `transfer` | object か null | 支払いを払った送金。下の節 |
| `refunded` | string | 返金が押さえている額。資産の単位。返金が無ければ `"0"` |

`checkout_url` を持つ人は支払いの結果を読めます。`checkout_url` をログに残さないでください。
suco も自分のログと Webhook の event には載せません。

## GET /payments/{id}

資格情報の account の支払いを 1 つ読み、`POST /payments` が返したのと同じ本文を `200` で
返します。

ほかの account の支払い、存在しない支払い、識別子の形をしていない `{id}` は、どれも同じ本文の
`404` です。`404` は 3 つのどれかを言いません。

## 返金

返金は、支払いが受け取った額を、送金元のアドレスへ送り返します。加盟店が、受取アドレスの
ウォレットで送金に署名します。suco は署名された送金を記録し、チェーンで確定を見ます。返金の
仕組みと状態は [refunds.ja.md](refunds.ja.md) にあります。

`POST /payments/{id}/refunds` は、資産の単位の `amount` を取ります。残り全部を送り返すときは
`amount` を省きます。返金先アドレスは加盟店が選べません。suco が、支払いを払った送金の
送金元を返金先アドレスにします。`Idempotency-Key` は返金でも下の規則で読みます。

レスポンスは `refund_url` を持ちます。`refund_url` は、加盟店が署名する署名ページの URL です。
`refund_url` を持つ人は返金を読めます。`refund_url` をログに残さないでください。suco も自分の
ログと Webhook の event には載せません。

## Idempotency-Key

同じ内容で支払いを 2 回作ると、支払いが 2 つできます。2 回目が 1 回目の送り直しだと伝えるには、
`Idempotency-Key` header を付けます。

```http
Idempotency-Key: 8e03978e-40d5-43e8-bc93-6894a57f9324
```

キーは乱数にして、支払いごとに新しく作ってください。多くのクライアントは UUID を送ります。
引用符で囲んだ値も読み、引用符はキーに含めません。キーは印字可能な ASCII で 255 バイトまで
です。空のキーは断ります。引用符と `\` は入れられません。header は 1 回だけ送ってください。
2 回送ると、リクエストが 2 つあると見なして断ります。

account が既に使ったキーで届いたリクエストには、最初のリクエストが作った支払いを返します。
ステータスは `201`、`Location` も同じで、レスポンスに `Idempotent-Replayed: true` が付きます。
本文は最初のレスポンスの写しではなく、返す時点の支払いです。支払い可能になった後なら
`awaiting_payment` で返ります。

2 つのリクエストの本文は、バイト単位で同じである必要があります。JSON のキーの順を変えると別の
本文です。既に使ったキーが別の本文で届くと `400` で、`problems` が `Idempotency-Key` を
名指します。最初の本文を送るか、未使用のキーを送ってください。`Idempotency-Key` の元になった
IETF の draft は `422` を挙げています。suco はリクエストのほかの誤りと同じく `400` で答えます。

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

キーは、キーで作った支払いと同じ期間残ります。suco が断ったリクエストはキーを残しません。
同じキーで正しい本文を送り直せます。同じキーのリクエストが 2 つ同時に届いても構いません。
後のリクエストは先のリクエストを待ち、先のリクエストが作った支払いを返します。

キーを読む API エンドポイントは `POST /payments` と `POST /payments/{id}/refunds` だけです。
キーを送らずにレスポンスを受け取れなかったとき、加盟店は送り直す前に、`metadata` を手がかりに
支払いを探すことになります。

## transfer

`transfer` は、支払いを払ったチェーン上の送金です。送金が見つかるまでは `null` です。

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
| `tx` | string | 取引のハッシュ。加盟店が自分のノードで送金を引くための値 |
| `block_height` | integer | 送金が入っているブロックの高さ |
| `block_hash` | string | ブロックのハッシュ |
| `block_time` | string | ブロックの時刻。UTC |
| `from` | string | 送金元のアドレス。返金先アドレスになる |
| `value` | string | チェーンが動かした額。資産の最小単位 |

`status` が `succeeded` になるまで、`transfer` は候補です。再編成（reorg）で送金がチェーンから
消えると、`transfer` からも消えます。

`value` の単位は、`amount` と `received` の単位と違います。`amount` と `received` は資産の
単位です。同じ額が、`amount` では `"1000"`、`value` では `"1000000000000000000000"` です。

`transfer` にチェーンと受取アドレスは入りません。チェーンは `asset.network` にあり、受取
アドレスは `destination` にあります。支払いを払った送金の行き先は必ず `destination` です。

`amount` より少ない額の送金は、支払いを払った送金になりません。suco は届いた額を `received`
に書き、`transfer` には書きません。`received` が入っていて `transfer` が `null` のことが
あります。

## 失敗

成功のレスポンスは支払いです。失敗のレスポンスは、どの API エンドポイントでも 1 つの形です。

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
| 400 | `invalid` | 本文が JSON のオブジェクトでない、API が読まないキーがある、型が違う、値が規則の外にある。`Idempotency-Key` がキーの形でない、別の本文で届いた | `problems` が名指す箇所を直して送り直す |
| 401 | `unauthorized` | 資格情報が無いか、失効している | 有効な資格情報を送る |
| 403 | `forbidden` | 資格情報に、呼んだ API エンドポイントの操作をする権限が無い。read-only の資格情報で書く操作を呼んだときなど | 権限のある資格情報を送る |
| 404 | `not_found` | 識別子の支払い、返金、Webhook エンドポイントが account に無い | 識別子と、資格情報の account を確かめる |
| 409 | 理由の語 | 支払いに支払い試行を発行できない。返すのは `POST /checkout/{token}/attempts` だけ | 理由の語は [checkout.ja.md](checkout.ja.md) にある |
| 413 | `too_large` | 本文が上限を越えた。`POST /payments` は 64 KiB、`POST /checkout/{token}/attempts` は 1 KiB | 小さい本文を送る |
| 503 | `unavailable` | suco がデータベースに届かなかった | 運用者に頼む。理由は suco のログにあり、本文には入らない。追い方は [operating.ja.md](operating.ja.md) にある |

`problems` を持つのは `400` だけです。

### 問題

1 つの問題は、`field` でリクエストのどの部分かを、`message` でどの規則を破ったかを言います。
`field` は本文のキーです。header が悪いときは、header の名前です。本文全体についての問題は
`field` を持ちません。

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

`message` がリクエストの値を繰り返すとき、値は 128 バイトで切ります。`field` はリクエストが
選んだキーのことがあります。`field` と `message` は 512 バイトで切ります。制御文字か見えない
文字を含む `field` と `message` は、文字をエスケープにして、全体を引用符で囲みます。

問題には段階があり、レスポンスは最初に引っかかった段階の問題だけを返します。キーの形でない
`Idempotency-Key` は最初の段階で答えるので、header の問題と本文の形の問題は一緒に返ります。

1. 本文の形。知らないキー、型の違う値、`suco.yaml` に無い資産、数や時刻として読めない値、
   無い必須キーです。
2. account が資産を受け付けているか。
3. 値。0 の金額、範囲の外の期限、上限を越えた `metadata` です。

同じ段階の問題はすべて列挙します。クライアントは 1 つの段階を 1 往復で直せます。

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

支払いページと署名ページの API エンドポイントは、パスの token で呼ぶ人を通し、資格情報を
求めません。`/checkout/` と `/refund/` の下のレスポンスには、次の header が付きます。

- `Cache-Control: no-store`
- `Referrer-Policy: no-referrer`
- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`
- Content-Security-Policy。ページが自分のインスタンスにだけ届き、iframe の中で開かれない
  ように制限する

CORS の header は付きません。呼ぶのはページ自身だけです。`/checkout-assets/` と
`/refund-assets/` の下の script と style は誰でも読めます。キャッシュしてよく、header は
`nosniff` だけです。

ほかの account の Webhook エンドポイント、存在しない Webhook エンドポイント、形を成さない
識別子は、支払いと同じく `404` です。

## 関連

- [checkout.ja.md](checkout.ja.md): 支払いページ、支払い試行、署名データ
- [refunds.ja.md](refunds.ja.md): 返金、返金の状態、加盟店が署名する署名ページ
- [webhooks.ja.md](webhooks.ja.md): suco が加盟店のサーバーに送る通知と、受信側の要件
- [operating.ja.md](operating.ja.md): 運用者が実行するコマンドと、`suco doctor` の出力
