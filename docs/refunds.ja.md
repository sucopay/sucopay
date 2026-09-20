# 返金

English: [refunds.md](refunds.md)

このページは、支払いが受け取った額を送金元のアドレスへ送り返す返金について、作り方と署名の
仕方を説明します。

組み込む加盟店の開発者向けです。[api.ja.md](api.ja.md) のとおりに支払い（`payment`）を作り、
読み戻せることを前提にします。

## このページの語

| 語 | 意味 |
|---|---|
| 返金（`refund`） | 支払いが受け取った額を、送金元のアドレスへ送り返す記録 |
| 署名ページ（`refund_url`） | 加盟店が返金に署名するページ。suco が配信する |
| 署名データ（`authorization`） | 署名ページが渡す typed data。支払いを受け取ったウォレットが署名する |
| 返金先アドレス（`destination`） | 返金が額を戻すアドレス。suco が支払いを払った送金から読み取る |
| token | `refund_url` に入る文字列。署名ページを開けるのは token を持つ人だけ |
| 残り | 支払いがまだ返せる額。`received` から `refunded` を引いた額 |
| `nonce` | 署名データに 1 つ入る値。資産は 1 度だけ受け付ける |
| module | ウォレットとやりとりする script と style |

## 署名ページでの流れ

1. 加盟店のサーバーが `POST /payments/{id}/refunds` で返金を作ります。レスポンスに
   `refund_url` が入ります。
2. 加盟店が `refund_url` を開きます。署名ページは `GET /refund/{token}/state` で返金を読み、
   見せます。
3. 支払いを受け取ったウォレットが、署名ページの渡す署名データに署名します。
4. 加盟店が取引を送信します。
5. suco がチェーンを読んで送金を見つけます。支払いを確定させたのと同じ読みが、返金の送金を
   確定させます。
6. チェーンが送金を確定させ、返金が `succeeded` になり、額が払った人に戻ります。

返金は、支払いと向きが逆です。suco は資金を預からず、取引も送りません。

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

event の本文は、返金を読んだときと同じ返金で、`refund_url` は載せません。`created` と
`awaiting_finality` の返金には event を送りません。返金を読めば状態が分かります。ほかは
[webhooks.ja.md](webhooks.ja.md) にあります。

## 署名ページの表示項目

金額と資産。署名するウォレットと、返金先アドレス。どの支払いに対する返金か。期限。状態。
返金に送金が見つかっていれば、送金です。

## 組み込み

### 署名するウォレット

支払いを受け取るウォレットが、EIP-712 の typed data に署名できる必要があります。返金に署名
するのが、支払いを受け取るウォレットだからです。受取アドレスは
`suco asset accept <name> <address>` が記録します。誰も署名できない受取アドレスは、支払いを
受け取れますが返金を返せません。

### 返金を作る

```http
POST /payments/{id}/refunds
Content-Type: application/json

{"amount": "250"}
```

`amount` は資産の単位で、文字列で書きます。本文が持てるキーは `amount` だけです。支払いの
残り全部を送り返すなら、本文を空にするか `{}` を送ります。

返金を作れるのは `succeeded` の支払いだけです。チェーンから送金が消えると、suco は着金を
取り消します。着金を保つ状態は `succeeded` だけです。

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
| `id` | string | 返金を読み戻す API エンドポイントで返金を指す名前 |
| `payment` | string | どの支払いに対する返金か |
| `status` | string | 返金がどこまで進んだか。状態は上の表 |
| `amount` | string | 送り返す額。資産の単位 |
| `destination` | string | 返金先アドレス |
| `expires_at` | string | 署名できなくなる時刻。UTC |
| `created_at` | string | 返金を作った時刻。UTC |
| `transfer` | object か null | suco が返金に照合した送金。形は [api.ja.md](api.ja.md)。見つかるまでは `null` で、`status` が `succeeded` になるまでは候補 |
| `refund_url` | string | 署名ページの URL。Webhook の event には載らない |

`GET /payments/{id}/refunds/{refund}` が 1 つ読み返し、同じ本文を答えます。一覧を返す
API エンドポイントはありません。レスポンスが返す識別子を記録するか、Webhook の event から
読んでください。

`Idempotency-Key` は `POST /payments` と同じ規則で読みます（[api.ja.md](api.ja.md)）。キーは
1 つのリクエストを指し、支払いもリクエストの一部です。支払いを本文ではなくパスが持っていても
同じです。同じ account がほかの支払いに使ったキーは拒否し、header を指す problem を返します。
同じ支払いへ同じキーと同じ本文をもう一度送ると、キーが作った返金を答えます。

`refund_url` は `<listen.base_url>/refund/<token>` で、署名ページを開けるのは token を持つ人です。
`refund_url` を持つ人は返金を読めます。`refund_url` をログに残さないでください。suco も自分の
ログと Webhook の event には載せません。`refund_url` の項目は、返金を作った account の
資格情報にだけ返します。

### 返金先アドレス

`destination` は呼ぶ側が選べません。支払いを払った送金から suco が読み取り、返金を作るときに
写します。後からチェーンを読み直しても、署名が額を送るアドレスが動かないようにするためです。
送金が何も見つかっていない支払いには返金先アドレスが無く、返金を作れません。

### 残り

支払いは着金した額まで返せます。着金した額には、期限切れでない返金の額も数に入ります。返金は
作った時点から額を確保します。誰かが署名したかどうかは関係ありません。

`GET /payments/{id}` は、期限切れでない返金が確保している合計を、資産の単位で `refunded` に
答えます。

```json
{"amount": "1000", "received": "1000", "refunded": "250"}
```

まだ返せる残りは、`received` から `refunded` を引いた額です。残りを超えるリクエストは拒否し、
拒否のレスポンスが残りを示します。期限切れになった返金は額を戻します。ただし、`nonce` が支払いの
`destination` から既にチェーンで使われている返金は、使われた送金を規則がどう判定したかに関わらず、
期限切れになりません。

### 失敗

拒否の形と、リクエストが守る規則の全ては [api.ja.md](api.ja.md) にあります。返金が足すのは
次です。

| ステータス | `error` | いつ | 対処 |
|---|---|---|---|
| 400 | `invalid` | 支払いが `succeeded` でない、何も見つかっていない、額が残りより多い、`Idempotency-Key` をほかの支払いの返金に使った。`problems` がどれかを示す | `problems` が指す項目を直して送り直す |
| 404 | `not_found` | 指定した識別子の支払いも返金も account に無い | 識別子と、資格情報が支払いを作った account に属するかを確かめる |
| 413 | `too_large` | 本文が 64 KiB を越えた | `amount` だけの本文か、空の本文を送る |
| 503 | `unavailable` | suco がデータベースに届かない | 運用者に頼む。[operating.ja.md](operating.ja.md) |

## 署名ページの API エンドポイント

`/refund/` の下の API エンドポイントは、パスの中の token を持つ人からの呼び出しを受け付けます。
資格情報は求めません。レスポンスに付く header と Content-Security-Policy は [api.ja.md](api.ja.md)
にあります。`/refund-assets/` の下の script と style は token を取らず、付くのは
`X-Content-Type-Options: nosniff` だけです。

| パス | 説明 |
|---|---|
| `GET /refund/{token}` | 署名ページの HTML |
| `GET /refund/{token}/state` | 署名ページの表示項目。JSON |
| `GET /refund-assets/{path}` | 署名ページの script と style。module を持つインスタンスだけ |

module が呼ぶ API エンドポイントは今でも配信しています。

通らない token は、署名ページのどの API エンドポイントでも `404` です。答え方はそれぞれ
違います。`GET /refund/{token}` は署名ページを、`GET /refund/{token}/state` は拒否の本文を、
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
| `status` | 返金がどこまで進んだか |
| `payment` | どの支払いに対する返金か |
| `amount` | 送り返す額。資産の単位 |
| `asset` | 資産と、ウォレットが切り替える `chain_id` |
| `from` | 署名するウォレット。支払いが払われた受取アドレス |
| `to` | 返金先アドレス |
| `expires_at` | 返金の期限 |
| `authorization` | 署名データ。署名できないあいだは `null` |
| `result` | 返金に見つかった送金。無ければ `null` |
| `reason` | 署名できない理由。署名できるあいだは `null`。語は下の表 |

`result` は、署名ページに出る形の送金です。`tx`、`block_height`、`block_time`、資産の
最小単位の `value`、そして返金が `succeeded` になるまで `true` の `settling` を持ちます。

`result` に出るのは、suco が返金に照合した送金だけです。返金の `nonce` を使い、返金が許可した
内容と違う送金は、「残り」に書いたとおり返金を開いたままにします。`result` には出ません。返金は
`result` が `null` のまま `awaiting_finality` にいます。

| `reason` | 意味 |
|---|---|
| `done` | 返金が終わった |
| `sent` | 送金が見つかり、確定を待っている |
| `expired` | 期限を過ぎた |

## 署名

`authorization` は EIP-712 の typed data から、ウォレットが埋める項目を除いた値です。支払者の
署名データとの違いは 3 つです。ほかは [checkout.ja.md](checkout.ja.md) にあります。

```json
{
  "domain": {"name": "JPY Coin", "version": "1", "chainId": 137,
             "verifyingContract": "0x431d…"},
  "message": {"from": "0xab…", "to": "0xcd…", "value": "250000000000000000000",
              "validAfter": "0", "validBefore": "1789561331", "nonce": "0x…"},
  "primaryType": "TransferWithAuthorization"
}
```

返金の署名データは `from` を持ちます。支払者は額を持っているところから払えますが、返金は
支払いが払われたウォレットからしか署名できないからです。`validBefore` は、返金の `expires_at`
を秒で書いた値です。`id` は無く、作り直せる署名データもありません。`nonce` は返金が使う 1 つの
値で、資産は 1 度だけ受け付けます。2 つ目の `nonce` が必要なら、別の返金を作ります。

`value` は返金でも資産の最小単位で、`amount` は資産の単位です。

## 期限

| 期限 | いつ | 過ぎると |
|---|---|---|
| 返金の期限 | `expires_at` | もう署名できません。新しい返金を作ります |
| 署名ページの期限 | 返金が終わってから 30 日 | `refund_url` が `404` になります |

返金は作ってから 30 分です。署名ページを開き、ウォレットに繋ぎ、署名して送るまでの時間です。
資産のコントラクト自身も同じ時刻で署名を受け付けなくなります。期限の後にチェーンへ載った送金は
何も動かしません。

## セキュリティ

署名ページは支払いページと同じ規則の下にあります。規則は [checkout.ja.md](checkout.ja.md) に
あります。ウォレットとやりとりする module はまだ公開しておらず、署名ページは iframe の中では
開きません。公開するまで、署名ページは返金を見せるだけで、署名を受け付けません。module が
無いと、標準の署名ページが見せるのは、金額、2 つのアドレス、期限、状態です。

## 関連

- [api.ja.md](api.ja.md): 支払い、返金を作り読み戻す API エンドポイント、`Idempotency-Key`
- [checkout.ja.md](checkout.ja.md): 支払いページと、支払いページがウォレットに渡す typed data
- [webhooks.ja.md](webhooks.ja.md): 返金が確定したときと期限切れになったときに suco が送る通知
