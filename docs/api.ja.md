# API

English: [api.md](api.md)

このページは、支払い（`payment`）を作り、読み戻し、返金し、その知らせ先を管理する HTTP API の
リファレンスです。

API を呼ぶ加盟店の開発者向けです。`suco credential new` が書いた資格情報を前提にします。

## 支払いの流れ

パスはすべて `suco.yaml` の `listen` の `base_url` からの相対です。省くと
`http://localhost:7826` です。

1. 加盟店のサーバーが `POST /payments` で支払いを作ります。レスポンスに `checkout_url` が
   入ります。
2. 支払者を `checkout_url` へ送ります。そこが支払いページで、何をするかは
   [checkout.ja.md](checkout.ja.md) にあります。
3. ページが支払者の署名するものを初めて発行し、支払いが支払い可能になります。運用者の
   `suco payment await <id>` でも同じです。ページの script が公開されるまでは、このコマンド
   だけが支払者に署名するものを渡す手段です。ほかにまだ無いものは
   [ROADMAP.ja.md](../ROADMAP.ja.md) にあります。
4. 支払者のウォレットが、ちょうどの金額を `destination` へ送る EIP-3009 の
   `TransferWithAuthorization` に署名します。ウォレットが取引を送ります。
5. suco がチェーンを読んで送金を見つけ、`transfer` に返します。まだ確定していません。
6. チェーンが送金を確定させ、支払いが `succeeded` になります。品物を渡すのはこのときです。
   suco が加盟店のサーバーにどう知らせるかは [webhooks.ja.md](webhooks.ja.md) にあります。

## 支払いの状態

| `status` | 入り方 | 出方 | 終点 |
|---|---|---|---|
| `created` | `POST /payments` が支払いを作った | `awaiting_payment` | いいえ |
| `awaiting_payment` | ページが支払者の署名するものを初めて発行したか、運用者が `suco payment await` を実行した | `succeeded`、`failed`、`awaiting_finality` | いいえ |
| `awaiting_finality` | 支払い可能でなくなり、飛んでいる途中の送金が届くかどうかが決まっていない | `succeeded`、`expired` | いいえ |
| `succeeded` | チェーンがその支払いの送金を確定させた | なし | はい |
| `failed` | この build には、ここへ移すものがありません。確定せず、待っても変わらない支払いのための状態です | なし | はい |
| `expired` | 支払い可能でなくなるまでに何も届かなかった | なし | はい |

チェーンが送金を見せる時点と、その送金が残ると分かる時点は別で、支払いは 2 つ目で
`succeeded` になります。期限の直前に認可された送金は、期限の後に届くことがあります。
`awaiting_finality` は支払いが 2 つ目の事実を待つ場所で、注文の終わりではありません。

## 認証

リクエストはすべて資格情報を持ちます。`Authorization: Bearer <token>` で、トークンは
`suco credential new` が書いたものです。資格情報は 1 つの account に属し、読むか、
読み書きするかのどちらかです。account は 1 つです。`suco serve` が初めて起動するとき、表と
一緒にデータベースへ書きます。複数の account を扱うことは実装していません。

何かを変える API エンドポイントは、書ける資格情報を求めます。読むだけの API エンドポイントは
どちらでも通ります。どちらかは下の一覧にあります。資格情報の無いリクエストと、効力を失った
資格情報のリクエストは `401` です。その API エンドポイントのすることを資格情報がしてよくない
リクエストは `403` です。

## 資産と金額

資産は、1 つの `network` 上の 1 つのトークンです。Polygon の JPYC がそうです。通貨ではありません。
資産を識別するのは `network` と reference で、reference はそのチェーンがトークンを識別する値です。
EVM のチェーンではアドレスです。symbol は何も識別しません。1 つの `network` に同じ symbol の
トークンが 2 つあり得ますし、2 つの `network` の同じ symbol は 2 つのトークンです。

`suco.yaml` が、そのインスタンスが受け取る資産を並べ、それぞれに名前を付けます。リクエストは
その名前で指します。支払いが持つのは `network` と reference です。文書がそのトークンを別の名前で
載せ直しても、載せるのをやめても、支払いは作ったときのトークンのままです。

資産は 1 単位を最小単位へ分けていて、その桁数が `decimals` です。JPYC は 18 なので、1 JPYC は
最小単位の 1000000000000000000 です。チェーンは最小単位で数えます。この API は資産の単位で
数えるので、`"1000"` は 1000 JPYC です。

金額は文字列です。多くのクライアントは JSON の数を浮動小数点で読みます。1000 JPYC を最小単位で
書くと 1000000000000000000000 で、double では正確に持てません。

## POST /payments

資格情報が名指す account の支払いを作ります。

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
| `asset` | string | 必須 | `suco.yaml` がその資産に付けた名前です。account がその資産を受け付けている必要があります。受け付けと、その資産の受取アドレスは `suco asset accept <name> <address>` が記録します |
| `amount` | string | 必須 | 資産の単位で書いた数です。`"1000"` は 1000 JPYC です。数字と、小数点があればその後に数字だけです。符号も指数も付けません。小数の桁は資産の `decimals` まで、最小単位に直して 78 桁までです。0 は断ります |
| `expires_at` | string | 任意 | RFC 3339 の時刻です。現在より後で、30 日後までです。省くと 15 分後です |
| `return_url` | string | 任意 | 支払いページが支払者を戻す先です。`https` の URL で、2048 バイトまで、username と password を持たないものです。`http://localhost` と `http://127.0.0.1` も通します。ページがこれをどう使うかは [checkout.ja.md](checkout.ja.md) にあります |
| `metadata` | object | 任意 | 文字列のキーと文字列の値です。20 件まで、キーは 64 バイトまで、値は 512 バイトまでです。送ったとおりに返り、省くと `{}` です。suco のログには残しません |

これ以外のキーは拒みます。本文は 64 KiB までです。

レスポンスは `201` で、`Location` に支払いのパスを、本文に支払いを返します。

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
| `id` | string | ほかの API エンドポイントでその支払いを指す名前 |
| `status` | string | その支払いがどこまで進んだか。状態は上の表 |
| `asset` | object | トークンを `network` と `reference` で識別し、`symbol` と `decimals` で説明する。`suco.yaml` で付けた名前は返らない |
| `amount` | string | その支払いが求める額。資産の単位 |
| `received` | string か null | 届いた額。資産の単位。何かが届くまでは `null` |
| `destination` | string | その支払いの受取アドレス。`suco asset accept` がその資産に記録したもの |
| `metadata` | object | リクエストが送ったもの。送らなければ `{}` |
| `expires_at` | string | 支払い可能でなくなる時刻。UTC |
| `created_at` | string | 支払いを作った時刻。UTC |
| `return_url` | string か null | ページが支払者を戻す先。リクエストが省いたときは `null` |
| `checkout_url` | string | 支払者を送る先。インスタンスがページを配信する前に作った支払いには入らない |
| `transfer` | object か null | その支払いを払った送金。下の節 |
| `refunded` | string | その支払いの返金が押さえている額。資産の単位。1 つも作っていなければ `"0"` |

`checkout_url` を持つ人は支払いの結果を読めます。ログに残さないでください。suco も自分の
ログと Webhook の event には載せません。

## GET /payments/{id}

資格情報が名指す account の支払いを 1 つ読み、`POST /payments` が返したのと同じ本文を `200` で
返します。

ほかの account の支払い、誰も作っていない支払い、識別子の形をしていないものは、どれも同じ本文の
`404` です。`404` はその 3 つのどれであるかを言いません。

## 返金

返金は、支払いが受け取ったものを、届いた元のアドレスへ送り返します。その支払いを受け取った
ウォレットから出る送金に加盟店が署名し、suco がそれを記録してチェーンを見ます。全体と、返金の
進む状態は [refunds.ja.md](refunds.ja.md) にあります。

`POST /payments/{id}/refunds` は資産の単位の `amount` を取ります。支払いの残り全部を送り返すなら、
何も持たせません。返金先アドレスは呼ぶ側が選ぶものではありません。その支払いを払った送金から
suco が読み取ります。`Idempotency-Key` はここでも下の規則で読みます。

レスポンスは `refund_url` を持ちます。加盟店が署名する先です。この URL を持つ人は返金を読めます。
ログに残さないでください。suco も自分のログと Webhook の event には載せません。

## Idempotency-Key

支払いを 2 回作ると支払いが 2 つできます。2 回目が 1 回目の送り直しだと言うには、この header を
付けます。

```http
Idempotency-Key: 8e03978e-40d5-43e8-bc93-6894a57f9324
```

鍵は乱数にして、支払いごとに新しく作ってください。多くのクライアントは UUID を送ります。引用符で
囲んだ値も読み、引用符は鍵に含めません。鍵は印字可能な ASCII で 255 バイトまでです。空は断り、
引用符と `\` は入れられません。header は 1 回だけ送ってください。2 つあるとリクエストが 2 つに
なり、断ります。

その account が既に使った鍵のリクエストには、その鍵が作った支払いを返します。`201` と同じ
`Location` で、レスポンスに `Idempotent-Replayed: true` が付きます。本文は最初のレスポンスの
写しではなく、そのときの支払いです。その後に支払い可能になっていれば、そう返ります。

2 つのリクエストは、本文がバイト単位で同じである必要があります。JSON のキーの順を変えるだけで
別の本文です。別の本文で届いた鍵は `400` で、`problems` が `Idempotency-Key` を名指します。
最初に送った本文を送るか、まだ使っていない鍵を送ってください。この header の元になった IETF の
draft はここに `422` を挙げています。suco はリクエストのほかの誤りと同じく `400` で答えます。

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

鍵は、それが作った支払いと同じだけ残ります。suco が断ったリクエストは鍵を残さないので、同じ鍵で
正しい本文を送り直せます。同じ鍵の 2 つのリクエストが同時に届いても構いません。2 つ目は 1 つ目を
待ち、その支払いを返します。

鍵を読むのは `POST /payments` と `POST /payments/{id}/refunds` だけです。鍵を送らない場合、
送ったのにレスポンスの無かったリクエストは、送り直す前に、加盟店の側で `metadata` に入れたものを
手がかりに探すことになります。

## transfer

`transfer` は、その支払いが払われたチェーン上の送金です。見つかるまでは `null` です。

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
| `tx` | string | 加盟店が自分のノードで送金を引くための値 |
| `block_height` | integer | その送金が入っているブロック |
| `block_hash` | string | そのブロックのハッシュ |
| `block_time` | string | そのブロックの時刻。UTC |
| `from` | string | 送金の出たアドレス。この支払いの返金が戻る先 |
| `value` | string | チェーンが動かした額。資産の最小単位 |

`status` が `succeeded` になるまでは候補です。送金が再編成（reorg）でチェーンから無くなれば、
ここにも出なくなります。

`value` は `amount` や `received` と同じ書き方ではありません。`amount` と `received` は資産の
単位です。同じ額が、`amount` では `"1000"`、ここでは `"1000000000000000000000"` になります。

チェーンと、額の行った先はここに重ねません。チェーンは `asset.network` です。この支払いを払った
送金の行き先は必ず `destination` です。

`amount` より少ない額の送金は、この支払いのものになりません。届いた額を `received` に書き、
ここには出ません。`received` が入っていて `transfer` が `null` のことがあります。

## 失敗

成功は支払いです。失敗はどの API エンドポイントでも 1 つの形です。

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
| 400 | `invalid` | 本文が JSON のオブジェクトでない、API が読まないキーがある、型が違う、値が規則の外にある。`Idempotency-Key` が鍵の形でない、別の本文で届いた | `problems` が名指すものを直して送り直す |
| 401 | `unauthorized` | 資格情報が無いか、効力を失っている | 効力のある資格情報を送る |
| 403 | `forbidden` | その API エンドポイントのすることを資格情報がしてよくない | 書ける資格情報を送る |
| 404 | `not_found` | その識別子の支払い、返金、Webhook エンドポイントが account に無い | 識別子と、資格情報がそれを作った account のものかを確かめる |
| 409 | 理由の語 | その支払いに署名するものを発行できない。返すのは `POST /checkout/{token}/attempts` だけ | 語は [checkout.ja.md](checkout.ja.md) にある |
| 413 | `too_large` | 本文が 64 KiB を越えた | 小さい本文を送る |
| 503 | `unavailable` | suco がデータベースに届かなかった | 運用者に頼む。理由は suco のログにあり、本文には入らない。追い方は [operating.ja.md](operating.ja.md) にある |

`problems` を持つのは `400` だけです。

### 問題

各問題は、リクエストのどこについてかを `field` で、値がどの規則を破ったかを `message` で言います。
`field` は本文のキーか、header が悪いときはその header の名前です。本文全体についての問題は
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

`message` がリクエストの値を繰り返すときは、その値を 128 バイトで切ります。`field` はリクエストが
選んだキーのことがあります。`field` と `message` はそれぞれ 512 バイトで切ります。制御文字や
見えない文字を含むときは、それらをエスケープにして引用符で囲みます。

問題には段階があり、本文は最初に引っかかった段階について答えます。鍵の形でない
`Idempotency-Key` は最初の段階で答えるので、header の問題と本文の問題は一緒に返ります。

1. 本文の形の問題です。知らないキー、型の違う値、`suco.yaml` に無い資産、数や時刻として読めない
   値、無い必須キーです。
2. account がその資産を受け付けているかです。
3. 値の問題です。0 の金額、範囲の外の期限、上限を越えた `metadata` です。

1 つの段階にある問題はすべて列挙するので、クライアントはその段階を 1 往復で直せます。

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
| `/webhook_endpoints…` | read-only か read-write | Webhook エンドポイントの登録、変更、配送の確認。一覧と、それぞれが求める資格情報は [webhooks.ja.md](webhooks.ja.md) |
| `/checkout/…` と `/checkout-assets/…` | 不要 | 支払いページと、ページが呼ぶもの。一覧は [checkout.ja.md](checkout.ja.md) |
| `/refund/…` と `/refund-assets/…` | 不要 | 署名ページと、ページが呼ぶもの。一覧は [refunds.ja.md](refunds.ja.md) |
| `GET /healthz` | 不要 | プロセスが動いているか。詳細は [operating.ja.md](operating.ja.md) |
| `GET /readyz` | 不要 | インスタンスが仕事をできるか。詳細は [operating.ja.md](operating.ja.md) |

ページの API エンドポイントは、パスの中の token で通し、資格情報は求めません。`/checkout/` と
`/refund/` の下のレスポンスには、`Cache-Control: no-store`、`Referrer-Policy: no-referrer`、
`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY` が付きます。Content-Security-Policy
も付きます。ページは自分のインスタンスにだけ届き、誰にも iframe で開かれません。CORS の
header は付きません。呼ぶのはページ自身だけです。`/checkout-assets/` と `/refund-assets/` の
下の script と style は誰でも読め、キャッシュされてよく、付くのは `nosniff` だけです。

ほかの account の Webhook エンドポイント、無い Webhook エンドポイント、形を成さない識別子は、
支払いの API エンドポイントと同じく `404` です。

## 関連

- [checkout.ja.md](checkout.ja.md): 支払いページと、支払者が署名するもの
- [refunds.ja.md](refunds.ja.md): 返金、その状態、加盟店が署名するページ
- [webhooks.ja.md](webhooks.ja.md): suco が加盟店のサーバーに送るものと、受信側がすること
- [operating.ja.md](operating.ja.md): 運用者が実行するものと、`suco doctor` が出力するもの
