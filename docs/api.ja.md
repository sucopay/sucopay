# API

English: [api.md](api.md)

suco は加盟店のサーバに HTTP と JSON で応答します。経路は、Payment を作るものと読むもの、それを
サーバに知らせる先を扱うものです。どれも `suco.yaml` の `listen` の `base_url` からの相対で、
省くと `http://localhost:7826` です。支払者が払うページの経路は [checkout.ja.md](checkout.ja.md)
にあります。

## 認証

すべての要求は資格情報を持ちます。`Authorization: Bearer <token>` で、トークンは
`suco credential new` が書いたものです。資格情報は 1 つの account に属し、読むか、読み書きするか
のどちらかです。account は今のところ 1 つです。`suco serve` が初めて起動するときに、表と一緒に作ります。
`POST /payments` は書ける資格情報を求め、`GET /payments/{id}` はどちらでも通ります。
資格情報の無い要求と、効力を失った資格情報の要求は `401` です。経路のすることをその資格情報が
してよくない要求は `403` です。

## 資産と金額

資産は、1 つの network 上の 1 つのトークンです。Polygon の JPYC がそうです。通貨ではありません。
1 つの network に同じ symbol のトークンが 2 つあり得ますし、2 つの network の同じ symbol は 2 つの
トークンです。資産を識別するのは network と reference で、reference はそのチェーンがトークンを
識別する値です。EVM のチェーンではアドレスです。symbol は識別しません。

`suco.yaml` が、そのインスタンスが受け取る資産を並べ、それぞれに名前を付けます。要求はその名前で
指します。Payment が持つのは network と reference なので、文書がそのトークンを別の名前で載せ直しても、
載せるのをやめても、Payment は作ったときのトークンのままです。

資産は 1 単位を最小単位へ分けていて、その桁数が `decimals` です。JPYC は 18 なので、1 JPYC は
最小単位の 1000000000000000000 です。チェーンは最小単位で数え、この API は資産の単位で数えます。
`"1000"` は 1000 JPYC です。

金額は文字列です。JSON の数を多くのクライアントが浮動小数点で読み、1000 JPYC を最小単位で書くと
1000000000000000000000 で、double では正確に持てません。桁を 1 つ間違えた金額も正しい形をしています。

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
  "return_url": "https://shop.example/orders/A-1",
  "metadata": {"order": "A-1"}
}
```

| キー | | |
|---|---|---|
| `asset` | 必須 | `suco.yaml` がその資産に付けた名前です。account がその資産を受け付けていることが要ります。受け付けと、その資産の支払いを受け取るアドレスは、`suco asset accept <名前> <アドレス>` が記録します |
| `amount` | 必須 | 資産の単位で書いた数を、文字列で書きます。`"1000"` は 1000 JPYC です。数字と、小数点があればその後に数字だけで、符号も指数も付けません。小数の桁は資産の `decimals` まで、最小単位に直したときに 78 桁までです。`0` は Payment ではありません |
| `expires_at` | 任意 | RFC 3339 の時刻です。現在より後で、30 日後までです。省くと 15 分後です |
| `return_url` | 任意 | 支払いのページが支払者を戻す先です。`https` の URL で、2048 バイトまで、username と password を持たないものです。`http://localhost` と `http://127.0.0.1` も通します。ページがこれをどう使うかは [checkout.ja.md](checkout.ja.md) にあります |
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
  "created_at": "2026-09-05T23:08:53.514971Z",
  "return_url": "https://shop.example/orders/A-1",
  "checkout_url": "http://localhost:7826/checkout/2c2f…",
  "transfer": null
}
```

`asset` はトークンをネットワークと reference で識別し、記述します。`suco.yaml` で付けた名前は
返しません。運用者は名前を後で変えられ、Payment は作ったときのトークンのままです。`destination` は
その Payment の支払いを受け取るアドレスで、`suco asset accept` がその資産に記録したものです。`amount` と
`received` は資産の単位です。`received` は何かが届くまで `null` です。時刻は UTC です。
`return_url` は省いたとき `null` です。`checkout_url` は加盟店が支払者を送る先で、Payment の結果を
読める鍵です。ログに残さないでください。suco も自分のログと Webhook の event には載せません。
インスタンスがページを配信する前に作った Payment には無く、キーごと省きます。

`POST /payments` が作る Payment の `status` は `created` です。

### transfer

`transfer` は、その Payment が払われたチェーン上の送金です。見つかるまでは `null` です。

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

加盟店が自分の node で送金を引くための値です。`status` が `succeeded` になるまでは候補で、
送金が reorg でチェーンから無くなれば、ここにも出なくなります。

`value` はチェーンが動かした額で、資産の最小単位です。`amount` と `received` は資産の単位です。
同じ額が、`amount` では `"1000"`、`transfer` では `"1000000000000000000000"` になります。

チェーンと送り先はここに重ねません。チェーンは `asset.network` で、この Payment を払った送金の
送り先は必ず `destination` です。

`amount` より少ない額の送金は、この Payment のものになりません。届いた額を `received` に書き、
ここには出ません。`received` が入っていて `transfer` が `null` のことがあります。

### Idempotency-Key

Payment を 2 回作ると Payment が 2 つできます。2 回目が 1 回目の送り直しだと言うには、この
header を付けます。

```http
Idempotency-Key: 8e03978e-40d5-43e8-bc93-6894a57f9324
```

鍵は乱数にして、Payment ごとに新しく作ってください。多くのクライアントは UUID を送ります。
引用符で囲んだ値も読み、引用符は鍵に含めません。鍵は印字できる ASCII で 255 バイトまで、
引用符と `\` は入れられません。空も断ります。header は 1 回だけ送ってください。2 つあると
要求が 2 つになるので断ります。

その account が既に使った鍵の要求には、その鍵が作った Payment を返します。`201` と同じ
`Location` で、応答に `Idempotent-Replayed: true` が付きます。本文は最初の応答の写しではなく、
そのときの Payment です。その後に支払い可能になっていれば、そう返ります。

2 つの要求は、本文がバイト単位で同じである必要があります。JSON のキーの順を変えるだけで別の
本文です。別の本文で届いた鍵は `400` で、`problems` が `Idempotency-Key` を名指します。最初に
送った本文を送るか、まだ使っていない鍵を送ってください。この header の元になった IETF の
draft はここに `422` を挙げていますが、suco は要求の他の誤りと同じく `400` で答えます。

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

鍵は、それが作った Payment と同じだけ残ります。suco が断った要求は鍵を残さないので、同じ鍵で
正しい本文を送り直せます。同じ鍵の 2 つの要求が同時に届いても構いません。2 つ目は 1 つ目を
待ち、その Payment を返します。

鍵を読むのは `POST /payments` だけです。

鍵を送らない場合、送ったのに応答の無かった要求は、送り直す前に、加盟店の側で `metadata` に
入れたものを手がかりに探すことになります。

## GET /payments/{id}

資格情報が名指す account の Payment を 1 つ読み、`POST /payments` が返したのと同じ本文を `200` で
返します。

他の account の Payment、誰も作っていない Payment、識別子の形をしていないものは、どれも同じ本文の
`404` です。応答は何が存在するかを言いません。

## Checkout

支払いのページが呼ぶ経路です。`checkout_url` の token で通し、資格情報は要りません。何を返すかは
[checkout.ja.md](checkout.ja.md) にあります。

| 経路 | |
|---|---|
| `GET /checkout/{token}` | ページの HTML |
| `GET /checkout/{token}/state` | ページが見せるもの。JSON |
| `POST /checkout/{token}/attempts` | 支払者が署名するもの。発行するか、発行済みのものを返す |

ページ自身の script と style は、それを持つ配備では `/checkout-assets/` の下にあります。

## Webhook の宛先

payment が変わったことを加盟店のサーバーに知らせる先です。宛先の登録、一覧、変更、秘密の
更新、削除、試験の送信と、届けたものの確認と再送は `/webhook_endpoints` の下の経路で、どれも
資格情報が指す account のものです。何を送るか、どう署名するか、受け取り側が何をするかは
[webhooks.ja.md](webhooks.ja.md) にあります。

| 経路 | 資格情報 | |
|---|---|---|
| `POST /webhook_endpoints` | read-write | 登録し、秘密を受け取る |
| `GET /webhook_endpoints` | read-only | 一覧。秘密は入らない |
| `GET /webhook_endpoints/{id}` | read-only | 1 つ読む |
| `PATCH /webhook_endpoints/{id}` | read-write | `url`、`description`、`events`、`enabled` を変える |
| `POST /webhook_endpoints/{id}/secret` | read-write | 秘密を更新する |
| `DELETE /webhook_endpoints/{id}` | read-write | 消す |
| `POST /webhook_endpoints/{id}/test` | read-write | `endpoint.test` を 1 つ送る |
| `GET /webhook_endpoints/{id}/deliveries` | read-only | 新しい順に 100 件の配送と試行 |
| `POST /webhook_endpoints/{id}/deliveries/{delivery}/resend` | read-write | もう 1 度送る |

他 account の宛先、無い宛先、形を成さない識別子は、payment の経路と同じく `404` です。

## Payment の一生

| `status` | |
|---|---|
| `created` | 作られました。まだ何も払えません |
| `awaiting_payment` | 支払い可能です。支払者に署名するものを渡せます |
| `awaiting_finality` | もう払えず、飛んでいる途中の送金が届くかどうかが決まっていません |
| `succeeded` | 払われ、確定しました |
| `failed` | 確定しません。待っても変わりません |
| `expired` | 支払い可能でなくなるまでに何も届きませんでした |

Payment は `created` から `awaiting_payment` へ進み、そこから `succeeded`、`failed`、
`awaiting_finality` のどれかへ進みます。`awaiting_finality` からは `succeeded` か `expired` です。
後ろの 3 つが終点で、その先はありません。

`awaiting_finality` が状態として要るのは、チェーンが「送金があった」と言う時点と「その送金は残る」と
言う時点が別だからです。2 つの事実があり、Payment は 2 つ目で `succeeded` になります。期限の直前に
認可された送金は期限のあとに届き得ます。この状態が無ければ、その送金の置き場は既に expired と
呼ばれた Payment だけになり、そこは終点です。

Payment が支払い可能になるのは、ページが支払者の署名するものを初めて発行したときです。それは
[checkout.ja.md](checkout.ja.md) の `POST /checkout/{token}/attempts` か、運用者が実行する
`suco payment await <id>` です。ページの script が公開されるまでは、後者だけが支払者に署名するもの
を渡す手段です。ほかにまだ無いものは [ROADMAP.ja.md](../ROADMAP.ja.md) にあります。

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
| 400 | `invalid` | 本文が JSON のオブジェクトでない、API が読まないキーがある、型が違う、値が規則の外にあるときです。`Idempotency-Key` が鍵の形でないときと、別の本文で届いたときもです。どれかは `problems` が言います |
| 401 | `unauthorized` | 資格情報が無いか、効力を失っているときです |
| 403 | `forbidden` | 経路のすることをその資格情報がしてよくないときです |
| 404 | `not_found` | その識別子の Payment か Webhook の宛先がこの account に無いときです |
| 409 | 理由の語 | その Payment に署名するものを発行できないときです。返すのは `POST /checkout/{token}/attempts` だけで、語は [checkout.ja.md](checkout.ja.md) にあります |
| 413 | `too_large` | 本文が 64 KiB を越えたときです |
| 503 | `unavailable` | suco がデータベースに届かなかったときです。理由は suco の記録にあり、本文にはありません |

`problems` を持つのは `400` だけです。

### 問題

各問題は、要求のどこについてかを `field` で、値がどの規則を破ったかを `message` で言います。
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

`message` が要求の値を繰り返すときは、その値を 128 バイトで切ります。`field` は要求が選んだキーの
ことがあり、`field` と `message` はそれぞれ 512 バイトで切り、制御文字や見えない文字を含むときは
それらをエスケープにして引用符で囲みます。

問題には段階があり、本文は最初に引っかかった段階について答えます。鍵の形でない
`Idempotency-Key` は最初の段階で答えるので、header の問題と本文の問題は一緒に返ります。
まず本文の形の問題です。知らない
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
