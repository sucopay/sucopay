# Refund

English: [refunds.md](refunds.md)

Refund は、Payment が受け取ったものを、届いた元のアドレスへ送り返します。Payment の向きを逆に
したものです。加盟店が、Payment の宛先のウォレットから出る送金に署名して送り、Payment を確定
させたのと同じチェーンの読みが、これを確定させます。suco は金を預からず、取引も送りません。

**今日の署名のページは、Refund を見せるだけで署名は受け付けません。** ウォレットとやりとりする
script と style は別の module で、まだ公開していません。それの無い配備は素のページを配信します。
額、2 つのアドレス、期限、状態です。その module が呼ぶ経路は今でも配信していて、下にあります。

## 署名するウォレット

Payment の宛先のウォレットが EIP-712 の typed data に署名できる必要があります。Refund に署名する
のがそのウォレットだからです。宛先は `suco asset accept <name> <address>` が記録します。誰も署名
できないアドレスは、受け取れますが返せません。

## Refund を作る

```http
POST /payments/{id}/refunds
Content-Type: application/json

{"amount": "250"}
```

`amount` は資産の単位で、文字列で書きます。本文が持てるキーはこれだけです。Payment の残り全部を
送り返すなら、本文を空にするか `{}` を送ります。

Refund を作れるのは `succeeded` の Payment だけです。チェーンから送金が消えると suco は着金を
取り消すので、着金を保つ状態は `succeeded` だけだからです。

応答は `201` で、`Location` が Refund を指し、本文は Refund です。

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

`Idempotency-Key` は `POST /payments` と同じ規則で読みます（[api.ja.md](api.ja.md)）。鍵は 1 つ
の要求を指し、Payment もその要求の一部です。本文ではなく経路が持っていても同じで、その account
がほかの Payment に使った鍵は断り、header を名指す problem を返します。同じ Payment へ同じ鍵と
同じ本文をもう一度送ると、その鍵が作った Refund を答えます。

`GET /payments/{id}/refunds/{refund}` が 1 つ読み返し、同じ本文を答えます。一覧の経路はありま
せん。応答が返す識別子を控えるか、Webhook の event から読んでください。

## 送り先

`destination` は呼ぶ側が選ぶものではありません。Payment を払った送金から suco が読み取り、Refund
を作るときに写します。後からチェーンを読み直しても、署名の送り先が動かないようにするためです。
何も見つかっていない Payment には送り返す先が無く、Refund を作れません。

## 残り

Payment は着金した額まで返せます。期限切れでない Refund は、その額を数に入れます。Refund は作った
時点から額を押さえ、誰かが署名したかどうかは関係ありません。

`GET /payments/{id}` は、それらが押さえている合計を資産の単位で `refunded` に答えます。

```json
{"amount": "1000", "received": "1000", "refunded": "250"}
```

まだ返せるのは `received` から `refunded` を引いた額です。それを超える要求は断り、断りが残りを
言います。期限切れになった Refund は額を戻します。ただし、鍵が Payment の宛先から既にチェーンで
使われている Refund は、その送金を規則がどう判定したかに関わらず、期限切れになりません。出て
行った金は戻しません。

## 署名する

`refund_url` が加盟店の署名する先です。`<listen.base_url>/refund/<token>` で、開いた人を通すのは
token です。資格情報は求めません。その account の資格情報にだけ答え、ほかの誰にも答えません。
ログに残さないでください。suco も自分のログと Webhook の event には載せません。

ページは iframe の中では開きません。ページとして開いてください。

## 2 つの期限

| | | 過ぎると |
|---|---|---|
| Refund の期限 | `expires_at` | もう署名できません。新しい Refund を作ります |
| ページの期限 | Refund が終わってから 30 日 | URL が `404` になります |

Refund は作ってから 30 分です。ページを開き、ウォレットに繋ぎ、署名して送るまでの時間です。
資産のコントラクト自身も同じ時刻で署名を受け付けなくなるので、その後に載った送金は何も動かし
ません。

## Refund の一生

| `status` | |
|---|---|
| `created` | 署名できます。まだ何も確定していません |
| `awaiting_finality` | 期限を過ぎ、送信済みの送金が確定するかどうかが決まっていません |
| `succeeded` | チェーンが送金を確定させました。金は戻っています |
| `expired` | 期限を過ぎ、何も確定しませんでした |

Refund は戻りません。送金が見つかっただけでは状態は動かず、チェーンが確定させたときだけ動きます。

## ページが呼ぶ経路

`/refund/` の下の 2 つの経路は token だけで通し、資格情報は求めません。応答には
`Cache-Control: no-store`、`Referrer-Policy: no-referrer`、`X-Content-Type-Options: nosniff` と、
ページが自分の配備にだけ届き、誰にも iframe で開かれない Content-Security-Policy が付きます。
CORS の header は付きません。呼ぶのはページ自身だけだからです。script と style は誰でも読め、
キャッシュされてよく、付くのは `nosniff` だけです。

| 経路 | |
|---|---|
| `GET /refund/{token}` | ページの HTML。通らない token にはページで答えます |
| `GET /refund/{token}/state` | ページが見せるもの。JSON |
| `GET /refund-assets/{path}` | ページの script と style。それを持つ配備だけ |

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

`from` が署名するウォレットで、Payment が払われた先です。`to` が金の戻る先です。`payment` は
どの Payment に対する Refund かを言います。`authorization` は加盟店が署名するもので、署名できない
あいだは `null` です。`result` はその Refund に見つかった送金で、見つかるまでは `null` です。
`tx`、`block_height`、`block_time`、資産の最小単位の `value`、そして Refund が `succeeded` に
なるまで `true` の `settling` を持ちます。

`result` は suco が Refund に照合した送金です。Refund の鍵を使い、Refund が許したものではなかっ
た送金は、「残り」に書いたとおり Refund を開いたままにしますが、ここには出ません。Refund が
`awaiting_finality` にいるあいだ `result` は `null` のままです。

`reason` は署名できない理由で、署名できるあいだは `null` です。

| `reason` | |
|---|---|
| `done` | Refund が終わった |
| `sent` | 送金が見つかり、確定を待っている |
| `expired` | 期限を過ぎた |

### 加盟店が署名するもの

`authorization` は EIP-712 の typed data から、ウォレットが埋めるものを除いたものです。`domain`
と `message` のキーは仕様のとおり camelCase です。

```json
{
  "domain": {"name": "JPY Coin", "version": "1", "chainId": 137,
             "verifyingContract": "0x431d…"},
  "message": {"from": "0xab…", "to": "0xcd…", "value": "250000000000000000000",
              "validAfter": "0", "validBefore": "1789561331", "nonce": "0x…"},
  "primaryType": "TransferWithAuthorization"
}
```

支払者が署名するものと違って、これは `from` を持ちます。支払者は金を持っているところから払えます
が、Refund は Payment が払われたウォレットからしか署名できないからです。`value` は資産の最小単位
で、`amount` は資産の単位です。`validBefore` は `expires_at` を秒で書いたものです。`nonce` がこの
Refund の使う鍵で、資産はそれを 1 度だけ受け付けます。

## event

| event | |
|---|---|
| `refund.succeeded` | チェーンが送金を確定させた |
| `refund.expired` | 期限が過ぎ、何も確定しなかった |

本文は Refund を読んだときと同じもので、`refund_url` は載せません。途中の 2 つの状態は加盟店自身
の操作なので、読めば分かります。残りは [webhooks.ja.md](webhooks.ja.md) にあります。

## 応答

断りの形と、要求が守る規則の全ては [api.ja.md](api.ja.md) にあります。Refund が足すのは次です。

| 状態 | `error` | いつ |
|---|---|---|
| 400 | `invalid` | Payment が `succeeded` でない、何も見つかっていない、額が残りより多い、`Idempotency-Key` をほかの Payment の Refund に使った。`problems` がどれかを言う |
| 404 | `not_found` | その識別子の Payment も Refund も account に無い |
| 413 | `too_large` | 本文が 64 KiB を越えた |
| 503 | `unavailable` | suco がデータベースに届かない |

通らない token は、ページの 3 つの経路のどれでも `404` です。答え方はそれぞれ違います。
`GET /refund/{token}` はページを、`GET /refund/{token}/state` は上の本文を、`/refund-assets/` は
平文を返します。
