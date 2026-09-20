# Checkout

English: [checkout.md](checkout.md)

このページは、支払者が払う支払いページ suco Checkout の仕組みと、組み込みに要ることを
説明します。

組み込む加盟店の開発者向けです。[api.ja.md](api.ja.md) のとおりに支払い（`payment`）を作れる
ことを前提にします。

## このページの語

| 語 | 意味 |
|---|---|
| 支払いページ（`checkout_url`） | 支払者が払うページ。suco が配信する |
| token | `checkout_url` に入る文字列。持っている人を支払いページに通す |
| 支払い試行（`attempt`） | 支払者が払おうとする 1 回。支払いページが発行する |
| 署名データ | 支払い試行が持つ typed data。支払者のウォレットが署名する |
| `nonce` | 署名データに 1 つ入る値。1 回だけ使える |
| 戻り先（`return_url`） | 支払者が払い終えたときと払えないときに、支払いページが支払者を送る URL |
| module | ウォレットとやりとりする script と style。まだ公開していない |

## 支払いページでの流れ

1. 加盟店のサーバーが支払いを作り、支払者を `checkout_url` へ送ります。
2. 支払いページが `GET /checkout/{token}/state` で支払いを読み、見せます。
3. 支払いページが `POST /checkout/{token}/attempts` で署名データを求め、支払いが
   支払い可能になります。
4. 支払者のウォレットが、ちょうどの金額を受取アドレスへ送る EIP-3009 の
   `TransferWithAuthorization` に署名します。
5. 支払者のウォレットが取引を送り、gas は支払者が払います。
6. suco がチェーンを読んで送金を見つけます。支払いページは送金の結果を見せます。支払いが進む
   状態は [api.ja.md](api.ja.md) にあります。
7. `return_url` があれば、支払いページが支払者を戻り先へ送ります。

支払者には、支払いの `network` で、鍵を持ち typed data に署名できるウォレットが要ります。
ウォレットが正しい `network` にいるか、残高が足りるか、送金を許されているかは、支払いページが
ウォレットから読みます。suco は読みません。

## 支払いページの表示項目

加盟店の名前。account の名前です。金額と資産。期限。状態。支払いに送金が見つかっていれば、
送金。そして戻り先です。

`metadata` は見せません。受取アドレスは支払いページに出ません。受取アドレスは署名データの中に
あり、支払者が写して取引所から送れる場所にはありません。

## 組み込み

`POST /payments` のレスポンスに `checkout_url` があり、`GET /payments/{id}` も同じ
`checkout_url` を返します。`checkout_url` へ支払者を送ってください。リダイレクトでもリンクでも
構いません。`checkout_url` は `<listen.base_url>/checkout/<token>` で、token が支払者を
通します。token を知らない人は支払いページを読めず、資格情報は求めません。

1 つの支払いが持つ `checkout_url` は 1 つで、suco は作り直しません。失くしたら
`GET /payments/{id}` で読み直してください。インスタンスが支払いページを配信する前に作った
支払いに `checkout_url` は無く、キーごと省きます。`checkout_url` は支払者以外に
送らないでください。理由は [api.ja.md](api.ja.md) にあります。

`POST /payments` の `return_url` は戻り先です。支払者が払い終えたときと、払えないときに、
支払いページが支払者を `return_url` へ送ります。払えないのは、期限の後と、支払いページが
対応しないウォレットのときです。`return_url` は任意です。`https` の URL で、2048 バイトまで、
username と password を持ちません。`http://localhost` と `http://127.0.0.1` も開発のために
通します。

suco は `return_url` へ何も送らず、何も付け足しません。支払者が払ったかどうかを言うのは
Webhook と `GET /payments/{id}` です。支払者が戻り先に来たことではなく、加盟店のサーバーが
読んだ支払いで判断してください。

## 支払いページの API エンドポイント

`/checkout/` の下の API エンドポイントは、パスの中の token が呼ぶ人を通します。資格情報は
求めません。レスポンスに付く header と Content-Security-Policy は [api.ja.md](api.ja.md) に
あります。`/checkout-assets/` の下の script と style は token を取らず、付くのは
`X-Content-Type-Options: nosniff` だけです。

| パス | 説明 |
|---|---|
| `GET /checkout/{token}` | 支払いページの HTML。通らない token には `404` のページを返す |
| `GET /checkout/{token}/state` | 支払いページの表示項目。JSON |
| `POST /checkout/{token}/attempts` | 署名データ。発行するか、発行済みの署名データを返す |
| `GET /checkout-assets/{path}` | 支払いページの script と style。module を持つインスタンスだけ |

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
| `amount`、`asset` | `GET /payments/{id}` が返す `amount` と `asset` に、ウォレットが切り替える `chain_id` を足した値 |
| `expires_at` | 支払いの期限 |
| `merchant.name` | account の名前 |
| `return_url` | 戻り先。無ければ `null` |
| `attempt` | 有効な支払い試行の署名データ。無ければ `null` |
| `result` | 支払いに見つかった送金。無ければ `null` |
| `reason` | 署名データを出せない理由。出せる間は `null`。語は下の表 |
| `slower` | インスタンスが支払いの `network` をいつもどおりに読めていない間 `true`。確認にいつもより時間がかかる |

`result` は支払者が自分で調べられる形の送金です。`tx`、`block_height`、`block_time`、資産の
最小単位の `value`、`confirming`、`received_at` です。`confirming` は支払いが `succeeded` に
なるまで `true` です。`succeeded` になると `received_at` が入ります。

| `reason` | 意味 |
|---|---|
| `expired` | 期限を過ぎた |
| `closing` | 残りが 2 分未満で、署名にかかる時間より短い |
| `paid` | 送金が見つかり、確定を待っている |
| `done` | 支払いが終わった |
| `not_ready` | インスタンスが支払いの `network` をまだ読んでいない |
| `reissued` | 支払いごとに 1 回だけ渡せる新しい支払い試行を、もう渡した |
| `again` | 同じ支払いページの 2 つのリクエストが競い、答えに使える支払い試行が無い。もう一度求めれば答える。attempts の API エンドポイントだけが返す |

### attempts

`POST /checkout/{token}/attempts` の本文は空か、有効な支払い試行の代わりに新しい支払い試行を
求める `{"reissue": "<attempt の id>"}` です。ほかの本文は `400` です。

1. 有効な支払い試行があり、`reissue` が無ければ、有効な支払い試行を `200` で返します。
2. `reissue` が有効な支払い試行を指していなければ `400` です。
3. 署名データを出せない理由があれば、`409` で `{"error": "<reason>"}` を返し、支払いは
   変わりません。
4. ほかの場合は支払い試行を発行して `201` で返します。まだ `created` の支払いは、先に
   `awaiting_payment` に移します。

支払いページを読み直しても支払い試行は増えません。2 度目のリクエストは 1 度目の支払い試行を
読み返します。

### 失敗

| ステータス | `error` | いつ | 対処 |
|---|---|---|---|
| 400 | `invalid` | `attempts` の本文が空でも `reissue` でもない | 空の本文か `{"reissue": "<attempt の id>"}` を送る |
| 404 | `not_found` | token が無い、期限が過ぎた、形を成さない。`GET /checkout/{token}` は支払いページで答える | `GET /payments/{id}` が返す `checkout_url` と照らす |
| 409 | `reason` の語 | 署名データを出せない | `reason` に応じて動く。語は上の表 |
| 413 | `too_large` | `attempts` の本文が 1 KiB を越えた | 空の本文か、支払い試行を 1 つ指す `reissue` を送る |
| 503 | `unavailable` | suco がデータベースに届かない | 運用者に頼む。[operating.ja.md](operating.ja.md) |

## 署名

支払い試行の署名データは、支払いページが `eth_signTypedData_v4` に渡す typed data から
`from` を除いた値です。`from` は支払いページが支払者のアドレスで埋めます。`domain` と
`message` のキーは仕様のとおり camelCase です。`id` は支払い試行の名前で、表示と `reissue` に
使い、署名には入りません。

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

`value` は `amount` と違って資産の最小単位です。`validBefore` は、支払いの期限を秒で書いた
値です。`nonce` は 1 回だけ使える値です。`domain` は、資産のコントラクトが署名に使う値です。
`suco.yaml` の `eip712` にあり、`suco asset accept` がコントラクトと照合します。

支払者が署名する `nonce` は 1 回だけ使えます。ウォレットが `nonce` を、届かなかった取引に
使ってしまったとき、支払いページは新しい支払い試行を求めます。求められるのは支払いごとに
1 回です。支払いページは、2 回目が要る支払者を加盟店へ戻します。

## 期限

| 期限 | いつ | 過ぎると |
|---|---|---|
| 支払いの期限 | `expires_at` | 支払いページはもう署名データを出しません。支払いページは期限が過ぎたことを見せ、送信済みの取引があれば結果を見せます |
| 支払いページの期限 | 支払いが終わってから 30 日 | `checkout_url` が `404` になります |

支払いは `succeeded`、`expired`、`failed` のどれかで終わります。支払いページは、支払いが
終わるまでと、終わってから 30 日のあいだ読めます。`checkout_url` を持っている支払者が結果を
読めるようにするためです。終わっていない支払いに支払いページの期限は無く、支払いの期限の後に
`expired` か `succeeded` になります。

## セキュリティ

ウォレットとやりとりする script と style は別の module で、まだ公開していません。公開するまで、
支払いページは支払いを見せるだけで、支払いを受け付けません。module の無いインスタンスは素の
支払いページを配信します。素の支払いページが見せるのは、加盟店の名前、金額、期限、状態、
戻り先です。module が呼ぶ API エンドポイントは今でも配信しています。

支払いページは iframe の中では開きません。ページとして開いてください。

## 関連

- [api.ja.md](api.ja.md): 支払いページが見せる支払いと、支払いを作り読み戻す API エンドポイント
- [refunds.ja.md](refunds.ja.md): 加盟店が返金に署名する署名ページ。支払いページと同じ仕組み
- [webhooks.ja.md](webhooks.ja.md): 支払いが変わったときに suco が加盟店のサーバーへ送る通知
