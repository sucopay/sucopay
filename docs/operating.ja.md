# 運用

English: [operating.md](operating.md)

インスタンスが自分について答えること、そして直すための 2 つの副命令です。

## /healthz と /readyz

どちらも資格情報を求めません。ポートに届く誰もが読むので、どちらも理由も、ホスト名も、
エンドポイントの断片も持ちません。

`GET /healthz` はプロセスが動いているあいだ `200` を返します。何も参照しません。データベースに
届かないことで失敗する liveness probe は、動いているインスタンスを orchestrator に再起動させ
ます。それでデータベースは戻りません。

`GET /readyz` は、インスタンスが仕事をできるかを答えます。

```json
{"status":"ok","database":"reachable","credentials":"read-write",
 "networks":{"polygon":"observing"},"assets":{"jpyc":"unchanged"},
 "finality":{"polygon":"deciding"},"webhooks":"delivering"}
```

データベースを設定していないインスタンスはチェーンを読まず、確定も判定せず、Webhook も送ら
ないので、`networks` と `assets` と `finality` と `webhooks` は出ません。資格情報の store が
無ければ `credentials` も出ません。

network ごとに 1 語です。

| | |
|---|---|
| `observing` | 60 秒以内に周が成功しました。成功は確定済みの範囲を読み終えて書いたことで、先読みの失敗は数えません |
| `no-cursor` | チェーンが答え、文書が名指すチェーンでもあり、まだ 1 周も終えていません |
| `unreachable` | 最後の head の読みが失敗しました |
| `stalled` | 60 秒のあいだ周が成功していません。cursor のある範囲の履歴を捨てたプロバイダに取り残された network もここです |
| `chain-mismatch` | `eth_chainId` が文書と違います。読みも書きもしません |
| `no-finalized` | プロバイダが確定ブロックを言いません。読みも書きもしません |
| `finalized-changed` | cursor のいるブロックをチェーンが持っていません。誰かが cursor を置き直すまで動きません |
| `finalized-behind` | プロバイダの確定ブロックが cursor より低い状態です。遅れているだけで、追いつけば読みが再開します |

60 秒は、1 つのインスタンスが network に対して持つ lease の期限の 2 倍です。引き継いだ側が
最初の 1 周を終える時間が入っています。

チェーンを読むことと、確定を判定することは別です。後者を答えるのが `finality` で、これも
network ごとに 1 語です。

| | |
|---|---|
| `deciding` | 周がエンドポイントに問い直し、その答えが決めたことを書きました |
| `no-round` | このインスタンスが起きてから周が 1 度も終わっていません。異常ではなく、まだ何も起きていない状態です |
| `waiting` | 別のインスタンスがその network を持っています。こちらは控えで、配備としては判定が動いています |
| `too-few` | 直前の周で答えたエンドポイントが、一致に要る数に足りませんでした。問うたものはその場に残り、別のエンドポイントが答えるまでそのままです。何本が答えるかは `doctor` が言います |
| `unreachable` | 直前の周が終わりませんでした。答えないエンドポイントはもう周を終わらせないので、止めたのはこちら側、つまりデータベースです。`database` が別に言います |
| `stalled` | 周が終わらなくなりました。周は自分の入っているループを終われないので、これは周の中で止まった worker です |

`deciding` と `too-few` が立っているのは `networks.<name>.finality.recheck` の 3 周ぶんです。1 周
遅れただけの配備が言葉を失わない長さにしてあります。

asset ごとにも 1 語で、`unchanged` か `changed` です。そのチェーンが asset に対して動かすコードが、
インスタンスの起動時のものと違えば `changed` になります。proxy の差し替えがこれにあたります。

`status` は、データベースに届かないとき、または設定した network の全部が `unreachable`、
`stalled`、`chain-mismatch`、`no-finalized` のどれかであるときに `unavailable` になり、応答は
`503` です。残りの 4 語では `ok` のままです。API は動いていて、動けるのは運用者かプロバイダ
だからです。

期限を過ぎた payment は、network をその期限より後まで読み終えて初めて期限切れになります。決める
のは時計ではなく位置のブロックの時刻です。読むのを止めている配備は、どれだけ止まっていても何も
期限切れにしません。期限の前に運ばれた送金が、まだ読んでいないブロックにあるかもしれないからです。
`doctor` が network ごとの待っている件数を出します。

`finality` の語は `unavailable` にしません。判定が動いていない配備でも、送金は見つかり、記録され
ます。資金はどちらにしても加盟店のアドレスにあります。止まっているのは判断のほうで、そこで API
を止めると、まだ行われている支払いまで止まります。

`webhooks` は配備に 1 語です。全部の宛先への配送を 1 つの worker が送るからです。

| | |
|---|---|
| `delivering` | 周が、payment の変化を配送にし、時刻の来たものを送りました |
| `no-round` | このインスタンスが起きてから周が 1 度も終わっていません |
| `waiting` | 別のインスタンスが配送を持っています。こちらは控えです |
| `unreachable` | 直前の周が終わりませんでした。止めたのはデータベースです。答えない受け取り側は試行として記録され、周を止めません |
| `stalled` | 周が終わらなくなりました |

`delivering` が立っているのは 15 秒、5 秒の周の 3 周ぶんです。この語も `unavailable` にしません。
加盟店に伝わらなかったことは、加盟店が読めます。

## suco doctor

解決した設定とそれぞれの出所を印字し、続けて何に届くかを印字します。

```
suco.yaml

  assets.jpyc.decimals       18                                      file
  assets.jpyc.network        polygon                                 file
  ...
  networks.polygon.rpc.own   set                                     ${SUCO_POLYGON_RPC_URL}

database: PostgreSQL 17.5, schema 0010_credential_access
credentials: read-write
webhooks:
  3f9c2c1e-6a1b-4a1e-9f4e-2f0f2c5a7b11  2 pending, 1 failed to 7c1d…

networks:
  polygon  evm  chain 137, latest 78123, final 78100, position 78090, behind 10, 2 waiting to settle
    jpyc        implementation 0xa1b2c3...
```

`waiting to settle` は、エンドポイントへの問い直しを待っている記録済みの送金の数です。worker は
serve のプロセスの中にいるので、報告は worker に訊く代わりにデータベースが持っているものを
数えます。報告のたびに増えていく配備は、確定の判定が進まなくなった配備です。どの語なのかは
`/readyz` が言います。

`webhooks` は、データベースが持つ配送を account ごとに数えます。次の試行を待つ `pending`
と、最後の試行の後に諦めた `failed` で、failed には宛先の id を添えます。どちらも無ければ
`nothing pending, nothing failed` の 1 行です。今と違う鍵で暗号化した秘密を持つ宛先があれば、
その数が続きます。`credentials.key` を入れ替えた後に残るもので、その加盟店が秘密を更新すれば
配送は続きます。worker 自身が何をしているかは `/readyz` の `webhooks` の語が言います。

`doctor` はテストネットの chain id の後に `chain 80002 (Polygon Amoy, a testnet)` の形で名前を
添えます。本番の前の段から写した文書をここで捕まえるためです。

`others` だけで届く network は、その何本が答えるかを `3 of 4 others answer` の形で言います。答える
本数が一致に要る数とちょうど同じなら `no spare` が、足りなければ `too few to settle` が続きます。
エンドポイントが食い違っている送金は、待っている数の後に `2 waiting to settle, 1 disagreed about`
の形で、あるときだけ数えます。食い違いからは何も確定せず、放っておいても直らないので、この数は
運用者が動くためのものです。

`behind` は latest ではなく確定ブロックからの差です。周が読むのは確定済みの範囲だからです。
読めなかった network は、数字の代わりにその旨を出します。プロバイダ自身の code と文言が入り、
長さは切られていて、エンドポイントの断片は入りません。

チェーンは、起動時と同じアダプタで読みます。ここで出るものが、インスタンスが実際に出会うもの
です。何も書きません。

データベースの無い配備でも、schema を当てていない配備でも、チェーンは読みます。言えなくなるのは
それぞれをどこまで読んだかだけです。

## 配備の内側の webhook の宛先を許す

private なネットワークや loopback など、配備の内側のアドレスに解決する webhook の URL は、
登録時と毎回の送信時に断ります。加盟店が、配備からしか届かない先を配備に呼ばせられないため
です。運用者が自分の受け取り側を内側で動かすときは、宛先の行にアドレスか範囲を書いて、その
宛先だけに許します。

```sql
update webhook_endpoints set allowed = '{10.0.5.0/24}' where id = '<宛先の id>';
```

許可は宛先とアドレスに結び付き、名前には結び付きません。名前を別の内側のアドレスに向け直せば
前と同じく断ります。内側に解決する URL は登録できないので、加盟店は外側に解決する URL で登録
し、運用者が許可を書き、加盟店が `PATCH /webhook_endpoints/{id}` で URL を変えます。変更時の
検査は許可込みで行います。prefix として読めない値は何も許さず、他の配送も止めません。

## suco network cursor

```bash
suco network cursor <name> <height> [--force]
```

cursor が動かすもので、位置がその居場所です。network ごとに 1 つあり、周がどの高さまで読んだかと、
そこのブロックのハッシュを持ちます。

cursor のいるブロックをチェーンが持たなくなると、どの周もそこから先へ進めません。どこまで戻るかは
周に決められないので、チェーンが持っているブロックへ誰かが cursor を置き直します。その状態の
あいだ、`/readyz` はその network を `finalized-changed` と言います。

指定した高さのブロックをチェーンから読み、そのハッシュと一緒に書きます。高さだけでは、どの
チェーンの高さかが決まりません。

lease は取りません。周の前進は読んだときの位置を条件にした更新なので、途中で置き直された周は
前進を commit せず、次の周が置かれた位置から読みます。

**飛ばしたブロックを読むものはありません。** まだどの周も読んでいないブロックより先へ cursor を
置くと、その範囲の送金は全て見られないままになり、後から拾う経路はありません。その network で
まだ開いている payment は、飛ばしたブロックで払われていたかもしれず、cursor が期限を越えた時点で
払われなかったものとして期限切れになります。まだ開いている refund は、飛ばしたブロックで
送られていたかもしれず、金が出たまま期限切れになり、その額がもう一度返金できる状態に戻ります。
同じ金が二度出ていくことになります。そのため、network に `awaiting_payment` か
`awaiting_finality` の payment があるあいだ、または `created` か `awaiting_finality` の refund が
あるあいだ、この副命令は前へ進めることを拒み、それぞれの件数を言います。`--force` を付けると進め、
飛ばしたそれぞれの件数をログに書きます。後ろへ戻すのは拒みません。何も飛ばさず、周がその範囲を
読み直すからです。

履歴を捨てたプロバイダがまさにこの副命令の対象で、そこでは前へ進むしか道がありません。
`--force` はそのためのもので、何を失うかを運用者が数えてから使います。

cursor がどこにいたかと、どこへ動いたかをログに書きます。どこにいたかが、戻すための記録です。

## suco payment await

```bash
suco payment await <id>
```

payment 1 件を支払い可能にして、支払者が署名する 7 つの値を印字します。suco Checkout が
できるまでの代わりです。

```
contract 0xe7c3d8c9a439fede00d2600032d5db0be71c3c29
chainId 137
to 0x1234567890123456789012345678901234567890
value 1000000000000000000
validAfter 0
validBefore 1788972899
nonce 0x11becaaf611be4cfb6bd8a5287bf3688f85dbbbd2af45aa10ac31a7b4513ae01
```

nonce は、送金を突き合わせるときの鍵です。`validBefore` は payment の期限を、チェーンが比較
する秒へ切り捨てた値です。署名するのは支払者なので、ここに出た値より先の期限で署名された送金が
返ってくることがあります。ここに出た期限に達してから運ばれた送金は、payment に充当せず記録
します。

**印字したものは、実行した端末の外へ出さないでください。** 読んだ人は何をどこへ払うのかが
分かり、支払者の鍵も持っていれば支払者として署名できます。

鍵を発行するには、payment が `awaiting_payment` であり、その network が一度は読まれている必要が
あります。一度も読まれていないチェーンに対して鍵を渡すと、署名され、支払われ、誰も見ません。
既に支払い可能な payment でもう一度実行しても、2 本目の鍵は出ません。既に 1 本あると言います。
