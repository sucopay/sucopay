# Checkout

English: [checkout.md](checkout.md)

suco Checkout は支払者が払うページです。加盟店のサーバが API で Payment を作り、応答に入っている
URL へ支払者を送ります。ページは支払者のウォレットに、加盟店のアドレスへの送金の署名を求め、
ウォレットがそれを送信し、ページがその結果を見せます。

**今日のページは Payment を見せるだけで、支払いは受け付けません。** ウォレットとやりとりする
script と style は別の module で、まだ公開していません。それの無い配備は素のページを配信します。
加盟店の名前、金額、期限、状態、戻り先です。その module が呼ぶ経路は今でも配信していて、下に
あります。

## 支払者の送り先

`POST /payments` の応答に `checkout_url` があり、`GET /payments/{id}` も同じものを返します。
そこへ支払者を送ります。リダイレクトでもリンクでも構いません。URL は
`<listen.base_url>/checkout/<token>` で、token が支払者を通します。token を知らない人はページを
読めず、資格情報は求めません。

ページは iframe の中では開きません。ページとして開いてください。

Payment の URL は 1 つで、作り直しません。失くした加盟店は `GET /payments/{id}` で読み直せます。
配備がページを配信する前に作った Payment には無く、キーごと省きます。

URL は Payment の結果を読める鍵です。ログに残さず、支払者以外に送らないでください。suco も
自分のログと Webhook の event には載せません。

## 戻り先

`POST /payments` の `return_url` は、支払者が払い終えたとき、または払えないときに、ページが
支払者を送る先です。払えないのは、期限の後と、ページが対応しないウォレットのときです。任意です。
`https` の URL で、2048 バイトまで、username と password を持たないものです。`http://localhost`
と `http://127.0.0.1` も開発のために通します。

suco は `return_url` へ何も送らず、何も付け足しません。支払者が払ったかどうかは Webhook と
`GET /payments/{id}` が言います。`return_url` のページは、支払者が来たことではなく、自分のサーバ
から読んだ Payment で判断してください。

## 2 つの期限

| | | 過ぎると |
|---|---|---|
| Payment の期限 | `expires_at` | もう署名するものを出しません。ページはそう言い、送信済みのものがあればその結果を見せます |
| ページの期限 | Payment が終わってから 30 日 | URL が `404` になります |

Payment は `succeeded`、`expired`、`failed` のどれかで終わります。ページはそれまでと、その後
30 日のあいだ読めるので、URL を持っている支払者は結果を読めます。終わっていない Payment に
この期限は無く、期限の後に `expired` か `succeeded` になります。

## ページが見せるもの

加盟店の名前で、これは account の名前です。金額と資産、期限、状態、その Payment に見つかった
送金があればそれ、戻り先です。`metadata` は見せません。支払いを受け取るアドレスは文字として
置きません。ウォレットが署名するものの中にはあり、支払者が写して取引所から送る場所には
ありません。

## 支払者に要るもの

Payment の network で、鍵を持ち typed data に署名できるウォレットです。支払者は EIP-3009 の
`TransferWithAuthorization` にちょうどの金額で署名し、取引を自分で送信し、gas を払います。
ウォレットが正しい network にいるか、残高が足りるか、送金を許されているかは、ページが
ウォレットから読みます。suco は読みません。

支払者が署名する鍵は 1 回だけ使えます。ウォレットがその鍵を届かないものに使ってしまったとき、
ページは新しい鍵を求めます。Payment ごとに 1 回です。それ以上要る支払者は加盟店へ戻します。

## ページが呼ぶ経路

`/checkout/` の下の 3 つの経路は token だけで通します。応答には `Cache-Control: no-store`、
`Referrer-Policy: no-referrer`、`X-Content-Type-Options: nosniff` と、ページが自分の配備にだけ
届き、誰にも iframe で開かれない Content-Security-Policy が付きます。CORS の header は付きません。
呼ぶのはページ自身だけだからです。script と style は誰でも読め、キャッシュされてよく、付くのは
`nosniff` だけです。

| 経路 | |
|---|---|
| `GET /checkout/{token}` | ページの HTML。通らない token には `404` のページを返す |
| `GET /checkout/{token}/state` | ページが見せるもの。JSON |
| `POST /checkout/{token}/attempts` | 支払者が署名するもの。発行するか、発行済みのものを返す |
| `GET /checkout-assets/{path}` | ページの script と style。それを持つ配備だけ |

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

| キー | |
|---|---|
| `status` | `awaiting_payment`、`awaiting_finality`、`succeeded`、`expired`、`failed` のどれか。まだ `created` の Payment は `awaiting_payment` として見せます。支払者には同じことだからです |
| `amount`、`asset` | `GET /payments/{id}` が返すものに、ウォレットが切り替える `chain_id` を足したもの |
| `expires_at` | Payment の期限 |
| `merchant.name` | account の名前 |
| `return_url` | 加盟店が支払者を戻したい先。無ければ `null` |
| `attempt` | 有効な attempt の署名の材料。無ければ `null` |
| `result` | その Payment に見つかった送金。無ければ `null` |
| `reason` | 署名するものを出せない理由。出せる間は `null`。語は下 |
| `slower` | 配備が Payment の network をいつもどおりに読めていない間 `true`。確認にいつもより時間がかかります |

`result` は支払者が自分で調べられる形の送金です。`tx`、`block_height`、`block_time`、資産の
最小単位の `value`、`confirming`、`received_at` です。`confirming` は Payment が `succeeded` に
なるまで `true` で、なると `received_at` が入ります。

| `reason` | |
|---|---|
| `expired` | 期限を過ぎた |
| `closing` | 残りが 2 分未満で、署名にかかる時間より短い |
| `paid` | 送金が見つかり、確定を待っている |
| `done` | Payment が終わった |
| `not_ready` | 配備が Payment の network をまだ読んでいない |
| `reissued` | 1 回の新しい鍵をもう渡した |
| `again` | 同じページの 2 つの要求が競い、答えに使える attempt が無い。もう一度求めれば答えます。attempts の経路だけが返します |

### attempts

`POST /checkout/{token}/attempts` の本文は空か、有効な鍵の代わりに新しい鍵を求める
`{"reissue": "<attempt の id>"}` です。それ以外は `400` です。

1. 有効な attempt があり、`reissue` が無ければ、それを `200` で返します。
2. `reissue` が有効な attempt を指していなければ `400` です。
3. 署名するものを出せない理由があれば、`409` で `{"error": "<reason>"}` を返し、Payment は
   そのままです。
4. それ以外は attempt を発行して `201` で返します。まだ `created` の Payment は、先に
   `awaiting_payment` に移します。

ページを読み直しても鍵は増えません。2 度目の要求は 1 度目の attempt を読み返します。

### 支払者が署名するもの

attempt は、ページが `eth_signTypedData_v4` に渡す typed data から `from` を除いたもので、
`from` はページが支払者のアドレスで埋めます。`domain` と `message` のキーは仕様のとおり
camelCase です。`id` は attempt の名前で、表示と `reissue` に使い、署名には入りません。

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

`value` は `amount` と違って資産の最小単位です。`validBefore` は Payment の期限を秒で書いたもの
です。`nonce` が鍵で、`domain` は資産のコントラクトが署名に使うものです。`suco.yaml` の `eip712`
に書き、`suco asset accept` がコントラクトと照合したものです。

## 応答

| 状態 | `error` | いつ |
|---|---|---|
| 400 | `invalid` | `attempts` の本文が空でも `reissue` でもない |
| 404 | `not_found` | token が無い、期限が過ぎた、形を成さない。HTML の経路はページで答えます |
| 409 | `reason` の語 | 署名するものを出せない |
| 413 | `too_large` | `attempts` の本文が 1 KiB を越えた |
| 503 | `unavailable` | suco がデータベースに届かない |
