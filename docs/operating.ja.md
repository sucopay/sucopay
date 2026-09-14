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
 "finality":{"polygon":"deciding"}}
```

データベースを設定していないインスタンスはチェーンを読まず、確定も判定しないので、`networks`
と `assets` と `finality` は出ません。資格情報の store が無ければ `credentials` も出ません。

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

## suco doctor

解決した設定とそれぞれの出所を印字し、続けて何に届くかを印字します。

```
suco.yaml

  assets.jpyc.decimals       18                                      file
  assets.jpyc.network        polygon                                 file
  ...
  networks.polygon.rpc.own   set                                     ${SUCO_POLYGON_RPC_URL}

database: PostgreSQL 17.5, schema 0007_worker_indexes
credentials: read-write

networks:
  polygon  evm  chain 137, latest 78123, final 78100, position 78090, behind 10, 2 waiting to settle
    jpyc        implementation 0xa1b2c3...
```

`waiting to settle` は、エンドポイントへの問い直しを待っている記録済みの送金の数です。worker は
serve のプロセスの中にいるので、報告は worker に訊く代わりにデータベースが持っているものを
数えます。報告のたびに増えていく配備は、確定の判定が進まなくなった配備です。どの語なのかは
`/readyz` が言います。

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
払われなかったものとして期限切れになります。そのため、network に `awaiting_payment` か
`awaiting_finality` の payment があるあいだ、この副命令は前へ進めることを拒み、件数を言います。
`--force` を付けると進め、飛ばした payment の件数をログに書きます。後ろへ戻すのは拒みません。
何も飛ばさず、周がその範囲を読み直すからです。

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
