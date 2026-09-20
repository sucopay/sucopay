# Checkout

English: [checkout.md](checkout.md)

このページは、支払者が払うページ suco Checkout の仕組みと、組み込みに要ることを説明します。

組み込む加盟店の開発者向けです。[api.ja.md](api.ja.md) のとおりに支払い（`payment`）を作れる
ことを前提にします。

## ページでの支払いの流れ

1. 加盟店のサーバーが支払いを作り、支払者を `checkout_url` へ送ります。
2. ページが `GET /checkout/{token}/state` で支払いを読み、見せます。
3. ページが `POST /checkout/{token}/attempts` で支払者の署名するものを求め、支払いが
   支払い可能になります。
4. 支払者のウォレットが、ちょうどの金額を受取アドレスへ送る EIP-3009 の
   `TransferWithAuthorization` に署名します。
5. 支払者のウォレットが取引を送り、gas は支払者が払います。
6. suco がチェーンを読んで送金を見つけます。ページはその結果を見せます。支払いが進む状態は
   [api.ja.md](api.ja.md) にあります。
7. `return_url` があれば、ページが支払者をそこへ送ります。

支払者には、支払いの `network` で、鍵を持ち typed data に署名できるウォレットが要ります。
ウォレットが正しい `network` にいるか、残高が足りるか、送金を許されているかは、ページが
ウォレットから読みます。suco は読みません。

## ページが見せるもの

加盟店の名前で、これは account の名前です。金額と資産。期限。状態。その支払いに送金が
見つかっていれば、その送金。そして戻り先です。

`metadata` は見せません。受取アドレスはページに出ません。ウォレットが署名するものの中に
あり、支払者が写して取引所から送れる場所にはありません。

## 組み込み

`POST /payments` のレスポンスに `checkout_url` があり、`GET /payments/{id}` も同じものを返します。
そこへ支払者を送ります。リダイレクトでもリンクでも構いません。URL は
`<listen.base_url>/checkout/<token>` で、token が支払者を通します。token を知らない人はページを
読めず、資格情報は求めません。

支払いが持つ URL は 1 つで、suco は作り直しません。失くしたら `GET /payments/{id}` で読み直して
ください。インスタンスがページを配信する前に作った支払いには無く、キーごと省きます。この URL は
支払者以外に送らないでください。理由は [api.ja.md](api.ja.md) にあります。

`POST /payments` の `return_url` は、支払者が払い終えたとき、または払えないときに、ページが
支払者を送る先です。払えないのは、期限の後と、ページが対応しないウォレットのときです。任意です。
`https` の URL で、2048 バイトまで、username と password を持たないものです。`http://localhost`
と `http://127.0.0.1` も開発のために通します。

suco は `return_url` へ何も送らず、何も付け足しません。支払者が払ったかどうかを言うのは
Webhook と `GET /payments/{id}` です。支払者が来たことではなく、加盟店のサーバーから読んだもので
判断してください。

## ページの API エンドポイント

`/checkout/` の下の API エンドポイントは、パスの中の token が呼ぶ人を通します。資格情報は
求めません。レスポンスに付く header と Content-Security-Policy は [api.ja.md](api.ja.md) に
あります。`/checkout-assets/` の下の script と style は token を取らず、付くのは
`X-Content-Type-Options: nosniff` だけです。

| パス | 説明 |
|---|---|
| `GET /checkout/{token}` | ページの HTML。通らない token には `404` のページを返す |
| `GET /checkout/{token}/state` | ページが見せるもの。JSON |
| `POST /checkout/{token}/attempts` | 支払者が署名するもの。発行するか、発行済みのものを返す |
| `GET /checkout-assets/{path}` | ページの script と style。それを持つインスタンスだけ |

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
| `status` | `awaiting_payment`、`awaiting_finality`、`succeeded`、`expired`、`failed` のどれか。まだ `created` の支払いは `awaiting_payment` として見せる。支払者には同じこと |
| `amount`、`asset` | `GET /payments/{id}` が返すものに、ウォレットが切り替える `chain_id` を足したもの |
| `expires_at` | 支払いの期限 |
| `merchant.name` | account の名前 |
| `return_url` | 加盟店が支払者を戻したい先。無ければ `null` |
| `attempt` | 有効な支払い試行（`attempt`）の署名の材料。無ければ `null` |
| `result` | その支払いに見つかった送金。無ければ `null` |
| `reason` | 署名するものを出せない理由。出せる間は `null`。語は下の表 |
| `slower` | インスタンスが支払いの `network` をいつもどおりに読めていない間 `true`。確認にいつもより時間がかかる |

`result` は支払者が自分で調べられる形の送金です。`tx`、`block_height`、`block_time`、資産の
最小単位の `value`、`confirming`、`received_at` です。`confirming` は支払いが `succeeded` に
なるまで `true` で、なると `received_at` が入ります。

| `reason` | 意味 |
|---|---|
| `expired` | 期限を過ぎた |
| `closing` | 残りが 2 分未満で、署名にかかる時間より短い |
| `paid` | 送金が見つかり、確定を待っている |
| `done` | 支払いが終わった |
| `not_ready` | インスタンスが支払いの `network` をまだ読んでいない |
| `reissued` | 1 回の新しい鍵をもう渡した |
| `again` | 同じページの 2 つのリクエストが競い、答えに使える支払い試行が無い。もう一度求めれば答える。attempts の API エンドポイントだけが返す |

### attempts

`POST /checkout/{token}/attempts` の本文は空か、有効な鍵の代わりに新しい鍵を求める
`{"reissue": "<attempt の id>"}` です。それ以外は `400` です。

1. 有効な支払い試行があり、`reissue` が無ければ、それを `200` で返します。
2. `reissue` が有効な支払い試行を指していなければ `400` です。
3. 署名するものを出せない理由があれば、`409` で `{"error": "<reason>"}` を返し、支払いは
   そのままです。
4. それ以外は支払い試行を発行して `201` で返します。まだ `created` の支払いは、先に
   `awaiting_payment` に移します。

ページを読み直しても鍵は増えません。2 度目のリクエストは 1 度目の支払い試行を読み返します。

### 失敗

| ステータス | `error` | いつ | 対処 |
|---|---|---|---|
| 400 | `invalid` | `attempts` の本文が空でも `reissue` でもない | 空の本文か `{"reissue": "<attempt の id>"}` を送る |
| 404 | `not_found` | token が無い、期限が過ぎた、形を成さない。HTML の API エンドポイントはページで答える | `GET /payments/{id}` が返す `checkout_url` と照らす |
| 409 | `reason` の語 | 署名するものを出せない | `reason` に応じて動く。語は上の表 |
| 413 | `too_large` | `attempts` の本文が 1 KiB を越えた | 空の本文か、支払い試行を 1 つ指す `reissue` を送る |
| 503 | `unavailable` | suco がデータベースに届かない | 運用者に頼む。[operating.ja.md](operating.ja.md) |

## 署名

支払い試行は、ページが `eth_signTypedData_v4` に渡す typed data から `from` を除いたもので、
`from` はページが支払者のアドレスで埋めます。`domain` と `message` のキーは仕様のとおり
camelCase です。`id` は支払い試行の名前で、表示と `reissue` に使い、署名には入りません。

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

`value` は `amount` と違って資産の最小単位です。`validBefore` は支払いの期限を秒で書いた
ものです。`nonce` が鍵です。`domain` は資産のコントラクトが署名に使うもので、`suco.yaml` の
`eip712` に書き、`suco asset accept` がコントラクトと照合したものです。

支払者が署名する鍵は 1 回だけ使えます。ウォレットがその鍵を届かないものに使ってしまったとき、
ページは新しい鍵を求めます。支払いごとに 1 回です。それ以上要る支払者は加盟店へ戻します。

## 期限

| 期限 | いつ | 過ぎると |
|---|---|---|
| 支払いの期限 | `expires_at` | もう署名するものを出しません。ページはそう言い、送信済みのものがあればその結果を見せます |
| ページの期限 | 支払いが終わってから 30 日 | URL が `404` になります |

支払いは `succeeded`、`expired`、`failed` のどれかで終わります。ページはそれまでと、その後
30 日のあいだ読めます。URL を持っている支払者が結果を読めるようにするためです。終わっていない
支払いにこの期限は無く、期限の後に `expired` か `succeeded` になります。

## セキュリティ

ウォレットとやりとりする script と style は別の module で、まだ公開していません。公開するまで、
ページは支払いを見せるだけで受け付けません。module の無いインスタンスは素のページを配信します。
加盟店の名前、金額、期限、状態、戻り先です。module が呼ぶ API エンドポイントは今でも配信して
います。

ページは iframe の中では開きません。ページとして開いてください。

## 関連

- [api.ja.md](api.ja.md): ページが見せる支払いと、それを作り読み戻す API エンドポイント
- [refunds.ja.md](refunds.ja.md): 加盟店が返金に署名するページ。同じ仕組みです
- [webhooks.ja.md](webhooks.ja.md): 支払いが変わったときに suco が加盟店のサーバーへ送るもの
