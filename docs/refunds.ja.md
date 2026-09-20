# 返金

English: [refunds.md](refunds.md)

このページは、支払いが受け取ったものを届いた元のアドレスへ送り返す返金について、作り方と
署名の仕方を説明します。

組み込む加盟店の開発者向けです。[api.ja.md](api.ja.md) のとおりに支払い（`payment`）を作り、
読み戻せることを前提にします。

## ページでの返金の流れ

1. 加盟店のサーバーが `POST /payments/{id}/refunds` で返金を作ります。レスポンスに
   `refund_url` が入ります。
2. `refund_url` を開きます。ページは `GET /refund/{token}/state` で返金を読み、見せます。
3. その支払いを受け取ったウォレットが、ページの渡す EIP-712 の認可に署名します。
4. 加盟店が取引を送信します。
5. suco がチェーンを読んで送金を見つけます。支払いを確定させたのと同じ読みが、これを確定
   させます。
6. チェーンが送金を確定させ、返金が `succeeded` になり、額が払った人に戻ります。

返金は支払いの向きを逆にしたものです。suco は資金を預からず、取引も送りません。

## 返金の状態

| `status` | 入り方 | 出方 | 終点 |
|---|---|---|---|
| `created` | `POST /payments/{id}/refunds` が返金を作った。署名でき、まだ何も確定していない | `succeeded`、`awaiting_finality` | いいえ |
| `awaiting_finality` | 期限を過ぎ、送信済みの送金が確定するかどうかが決まっていない | `succeeded`、`expired` | いいえ |
| `succeeded` | チェーンが送金を確定させ、額が戻った | なし | はい |
| `expired` | 期限を過ぎ、何も確定しなかった | なし | はい |

返金は戻りません。送金が見つかっただけでは何も動かず、チェーンが確定させたときに返金が動きます。

| event | いつ |
|---|---|
| `refund.succeeded` | チェーンが送金を確定させた |
| `refund.expired` | 期限が過ぎ、何も確定しなかった |

本文は返金を読んだときと同じもので、`refund_url` は載せません。`created` と
`awaiting_finality` では何も送らず、返金がそこにいることは読めば分かります。残りは
[webhooks.ja.md](webhooks.ja.md) にあります。

## ページが見せるもの

金額と資産。署名するウォレットと、返金先アドレス。どの支払いに対する返金か。期限。状態。
その返金に送金が見つかっていれば、その送金です。

## 組み込み

### 署名するウォレット

支払いを受け取るウォレットが EIP-712 の typed data に署名できる必要があります。返金に署名
するのがそのウォレットだからです。そのアドレスは `suco asset accept <name> <address>` が
記録します。誰も署名できないアドレスは、受け取れますが返せません。

### 返金を作る

```http
POST /payments/{id}/refunds
Content-Type: application/json

{"amount": "250"}
```

`amount` は資産の単位で、文字列で書きます。本文が持てるキーはこれだけです。支払いの残り全部を
送り返すなら、本文を空にするか `{}` を送ります。

返金を作れるのは `succeeded` の支払いだけです。チェーンから送金が消えると suco は着金を
取り消すので、着金を保つ状態は `succeeded` だけだからです。

レスポンスは `201` で、`Location` が返金を指し、本文は返金です。

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
| `id` | string | 読み戻す API エンドポイントでその返金を指す名前 |
| `payment` | string | どの支払いに対する返金か |
| `status` | string | その返金がどこまで進んだか。状態は上の表 |
| `amount` | string | 送り返す額。資産の単位 |
| `destination` | string | 返金先アドレス |
| `expires_at` | string | 署名できなくなる時刻。UTC |
| `created_at` | string | 返金を作った時刻。UTC |
| `transfer` | object か null | suco がその返金に照合した送金。形は [api.ja.md](api.ja.md)。見つかるまでは `null` で、`status` が `succeeded` になるまでは候補 |
| `refund_url` | string | 加盟店が署名する先。Webhook の event には載らない |

`GET /payments/{id}/refunds/{refund}` が 1 つ読み返し、同じ本文を答えます。一覧を返す
API エンドポイントはありません。レスポンスが返す識別子を控えるか、Webhook の event から
読んでください。

`Idempotency-Key` は `POST /payments` と同じ規則で読みます（[api.ja.md](api.ja.md)）。鍵は
1 つのリクエストを指し、支払いもそのリクエストの一部です。本文ではなくパスが持っていても
同じで、その account がほかの支払いに使った鍵は断り、header を名指す problem を返します。
同じ支払いへ同じ鍵と同じ本文をもう一度送ると、その鍵が作った返金を答えます。

`refund_url` は `<listen.base_url>/refund/<token>` で、開いた人を通すのは token です。この URL を
持つ人は返金を読めます。ログに残さないでください。suco も自分のログと Webhook の event には
載せません。`refund_url` という項目は、その account の資格情報にだけ返します。

### 返金先アドレス

`destination` は呼ぶ側が選ぶものではありません。支払いを払った送金から suco が読み取り、返金を
作るときに写します。後からチェーンを読み直しても、署名の送り先が動かないようにするためです。
何も見つかっていない支払いには返金先アドレスが無く、返金を作れません。

### 残り

支払いは着金した額まで返せます。期限切れでない返金は、その額を数に入れます。返金は作った時点
から額を押さえ、誰かが署名したかどうかは関係ありません。

`GET /payments/{id}` は、それらが押さえている合計を資産の単位で `refunded` に答えます。

```json
{"amount": "1000", "received": "1000", "refunded": "250"}
```

まだ返せるのは `received` から `refunded` を引いた額です。それを超えるリクエストは断り、断りが
残りを言います。期限切れになった返金は額を戻します。ただし、鍵が支払いの `destination` から
既にチェーンで使われている返金は、その送金を規則がどう判定したかに関わらず、期限切れになりません。

### 失敗

断りの形と、リクエストが守る規則の全ては [api.ja.md](api.ja.md) にあります。返金が足すのは
次です。

| ステータス | `error` | いつ | 対処 |
|---|---|---|---|
| 400 | `invalid` | 支払いが `succeeded` でない、何も見つかっていない、額が残りより多い、`Idempotency-Key` をほかの支払いの返金に使った。`problems` がどれかを言う | `problems` が名指すものを直して送り直す |
| 404 | `not_found` | その識別子の支払いも返金も account に無い | 識別子と、資格情報がその支払いを作った account のものかを確かめる |
| 413 | `too_large` | 本文が 64 KiB を越えた | `amount` だけの本文か、空の本文を送る |
| 503 | `unavailable` | suco がデータベースに届かない | 運用者に頼む。[operating.ja.md](operating.ja.md) |

## ページの API エンドポイント

`/refund/` の下の API エンドポイントは、パスの中の token が呼ぶ人を通します。資格情報は
求めません。レスポンスに付く header と Content-Security-Policy は [api.ja.md](api.ja.md) に
あります。`/refund-assets/` の下の script と style は token を取らず、付くのは
`X-Content-Type-Options: nosniff` だけです。

| パス | 説明 |
|---|---|
| `GET /refund/{token}` | ページの HTML |
| `GET /refund/{token}/state` | ページが見せるもの。JSON |
| `GET /refund-assets/{path}` | ページの script と style。それを持つインスタンスだけ |

module が呼ぶ API エンドポイントは今でも配信しています。

通らない token は、ページのどの API エンドポイントでも `404` です。答え方はそれぞれ違います。
`GET /refund/{token}` はページを、`GET /refund/{token}/state` は断りの本文を、
`/refund-assets/` は平文を返します。

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
| `status` | その返金がどこまで進んだか |
| `payment` | どの支払いに対する返金か |
| `amount` | 送り返す額。資産の単位 |
| `asset` | 資産と、ウォレットが切り替える `chain_id` |
| `from` | 署名するウォレット。その支払いが払われた先 |
| `to` | 返金先アドレス |
| `expires_at` | 返金の期限 |
| `authorization` | 加盟店が署名するもの。署名できないあいだは `null` |
| `result` | その返金に見つかった送金。無ければ `null` |
| `reason` | 署名できない理由。署名できるあいだは `null`。語は下の表 |

`result` はここに出る形の送金です。`tx`、`block_height`、`block_time`、資産の最小単位の
`value`、そして返金が `succeeded` になるまで `true` の `settling` を持ちます。

これは suco が返金に照合した送金です。返金の鍵を使い、返金が許したものではなかった送金は、
「残り」に書いたとおり返金を開いたままにしますが、ここには出ません。その返金は `result` が
`null` のまま `awaiting_finality` にいます。

| `reason` | 意味 |
|---|---|
| `done` | 返金が終わった |
| `sent` | 送金が見つかり、確定を待っている |
| `expired` | 期限を過ぎた |

## 署名

`authorization` は EIP-712 の typed data から、ウォレットが埋めるものを除いたものです。支払者が
署名するものとの違いは 3 つで、ほかは [checkout.ja.md](checkout.ja.md) にあります。

```json
{
  "domain": {"name": "JPY Coin", "version": "1", "chainId": 137,
             "verifyingContract": "0x431d…"},
  "message": {"from": "0xab…", "to": "0xcd…", "value": "250000000000000000000",
              "validAfter": "0", "validBefore": "1789561331", "nonce": "0x…"},
  "primaryType": "TransferWithAuthorization"
}
```

これは `from` を持ちます。支払者は額を持っているところから払えますが、返金は支払いが払われた
ウォレットからしか署名できないからです。`validBefore` は返金の `expires_at` を秒で書いた
ものです。`id` は無く、作り直すものもありません。`nonce` がこの返金の使う 1 つの鍵で、資産は
それを 1 度だけ受け付けます。2 つ目の鍵が要るなら、別の返金を作ります。

`value` はここでも資産の最小単位で、`amount` は資産の単位です。

## 期限

| 期限 | いつ | 過ぎると |
|---|---|---|
| 返金の期限 | `expires_at` | もう署名できません。新しい返金を作ります |
| ページの期限 | 返金が終わってから 30 日 | URL が `404` になります |

返金は作ってから 30 分です。ページを開き、ウォレットに繋ぎ、署名して送るまでの時間です。資産の
コントラクト自身も同じ時刻で署名を受け付けなくなるので、その後に載った送金は何も動かしません。

## セキュリティ

署名ページは支払いページと同じ規則の下にあります。規則は [checkout.ja.md](checkout.ja.md) に
あります。ウォレットとやりとりする module はまだ公開しておらず、ページは iframe の中では
開きません。公開するまで、ページは返金を見せるだけで署名を受け付けません。module が無いと、
素のページが見せるのは金額、2 つのアドレス、期限、状態です。

## 関連

- [api.ja.md](api.ja.md): 支払い、返金を作り読み戻す API エンドポイント、`Idempotency-Key`
- [checkout.ja.md](checkout.ja.md): 支払いページと、ページがウォレットに渡す typed data
- [webhooks.ja.md](webhooks.ja.md): 返金が確定したときと期限切れになったときに suco が送るもの
