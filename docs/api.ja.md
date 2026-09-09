# API

English: [api.md](api.md)

suco は加盟店のサーバに HTTP と JSON で応答します。経路は今のところ 2 つで、Payment を作るものと、
それを読むものです。どちらも `suco.yaml` の `listen` の `base_url` からの相対で、省くと
`http://localhost:7826` です。

## 認証

すべての要求は資格情報を持ちます。`Authorization: Bearer <token>` で、トークンは
`suco credential new` が書いたものです。資格情報は 1 つの account に属し、読むか、読み書きするか
のどちらかです。account は今のところ 1 つです。`suco serve` が初めて起動するときに、表と一緒に作ります。
`POST /payments` は書ける資格情報を求め、`GET /payments/{id}` はどちらでも通ります。
資格情報の無い要求と、効力を失った資格情報の要求は `401` です。経路のすることをその資格情報が
してよくない要求は `403` です。

## POST /payments

資格情報が名指す account の Payment を作ります。

```http
POST /payments
Authorization: Bearer <token>
Content-Type: application/json

{
  "asset": "jpyc",
  "amount": "1000",
  "expires_at": "2026-09-07T12:00:00Z",
  "metadata": {"order": "A-1"}
}
```

| キー | | |
|---|---|---|
| `asset` | 必須 | `suco.yaml` がその資産に付けた名前です。account がその資産を受け付けていることが要ります。受け付けと、その資産の支払いを受け取るアドレスは、`suco asset accept <名前> <アドレス>` が記録します |
| `amount` | 必須 | 資産の単位で書いた数を、文字列で書きます。`"1000"` は 1000 JPYC です。数字と、小数点があればその後に数字だけで、符号も指数も付けません。小数の桁は資産の `decimals` まで、最小単位に直したときに 78 桁までです。`0` は Payment ではありません |
| `expires_at` | 任意 | RFC 3339 の時刻です。現在より後で、30 日後までです。省くと 15 分後です |
| `metadata` | 任意 | 文字列のキーと文字列の値です。20 件まで、キーは 64 バイトまで、値は 512 バイトまでです。送ったとおりに返り、省くと `{}` で返ります。suco の記録には残しません |

これ以外のキーは拒みます。本文は 64 KiB までです。

応答は `201` で、`Location` に Payment の経路を、本文に Payment を返します。

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
  "created_at": "2026-09-05T23:08:53.514971Z"
}
```

`asset` はトークンをネットワークと reference で識別し、記述します。`suco.yaml` で付けた名前は
返しません。運用者は名前を後で変えられ、Payment は作ったときのトークンのままです。`destination` は
その Payment の支払いを受け取るアドレスで、`suco asset accept` がその資産に記録したものです。`amount` と
`received` は資産の単位です。`received` は何かが届くまで `null` です。時刻は UTC です。

`POST /payments` が作る Payment の `status` は `created` です。その先の状態はまだ提供していません。

`amount` が文字列なのは、それが 10 進の数で、JSON の数を多くのクライアントが浮動小数点で
読むからです。1000 JPYC を最小単位で書くと `1000000000000000000000` で、double では正確に持てず、
桁を 1 つ間違えた金額も正しい形をしています。

Payment を 2 回作ると、Payment が 2 つできます。冪等性キーはまだありません。送ったのに応答の
無かった要求は、送り直す前に、加盟店の側で `metadata` に入れたものを手がかりに探すことになります。

## GET /payments/{id}

資格情報が名指す account の Payment を 1 つ読み、`POST /payments` が返したのと同じ本文を `200` で
返します。

他の account の Payment、誰も作っていない Payment、識別子の形をしていないものは、どれも同じ本文の
`404` です。応答は何が存在するかを言いません。

## 応答

成功は Payment です。失敗はどの経路でも 1 つの形です。

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

| 状態 | `error` | いつ |
|---|---|---|
| 400 | `invalid` | 本文が JSON のオブジェクトでない、API が読まないキーがある、型が違う、値が規則の外にあるときです。どれかは `problems` が言います |
| 401 | `unauthorized` | 資格情報が無いか、効力を失っているときです |
| 403 | `forbidden` | 経路のすることをその資格情報がしてよくないときです |
| 404 | `not_found` | その識別子の Payment がこの account に無いときです |
| 413 | `too_large` | 本文が 64 KiB を越えたときです |
| 503 | `unavailable` | suco がデータベースに届かなかったときです。理由は suco の記録にあり、本文にはありません |

`problems` を持つのは `400` だけです。

### 問題

各問題は、どのキーについてかを `field` で、値がどの規則を破ったかを `message` で言います。本文
全体についての問題は `field` を持ちません。

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

`message` が要求の値を繰り返すときは、その値を 128 バイトで切ります。`field` は要求が選んだキーの
ことがあり、`field` と `message` はそれぞれ 512 バイトで切り、制御文字や見えない文字を含むときは
それらをエスケープにして引用符で囲みます。

問題には段階があり、本文は最初に引っかかった段階について答えます。まず本文の形の問題です。知らない
キー、型の違う値、`suco.yaml` に無い資産、数や時刻として読めない値、無い必須キーです。次に、account が
その資産を受け付けているかです。最後に値の問題です。0 の金額、範囲の外の期限、上限を越えた metadata
です。1 つの段階にある問題はすべて列挙するので、クライアントはその段階を 1 往復で直せます。

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
