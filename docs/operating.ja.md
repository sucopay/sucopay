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
 "networks":{"polygon":"observing"},"assets":{"jpyc":"unchanged"}}
```

データベースを設定していないインスタンスはチェーンを読まないので、`networks` と `assets` は
出ません。資格情報の store が無ければ `credentials` も出ません。

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

asset ごとにも 1 語で、`unchanged` か `changed` です。そのチェーンが asset に対して動かすコードが、
インスタンスの起動時のものと違えば `changed` になります。proxy の差し替えがこれにあたります。

`status` は、データベースに届かないとき、または設定した network の全部が `unreachable`、
`stalled`、`chain-mismatch`、`no-finalized` のどれかであるときに `unavailable` になり、応答は
`503` です。残りの 4 語では `ok` のままです。API は動いていて、動けるのは運用者かプロバイダ
だからです。

## suco doctor

解決した設定とそれぞれの出所を印字し、続けて何に届くかを印字します。

```
suco.yaml

  assets.jpyc.decimals   18                                          file
  assets.jpyc.network    polygon                                     file
  ...
  networks.polygon.rpc   set                                         ${SUCO_POLYGON_RPC_URL}

database: PostgreSQL 17.5, schema 0005_attempts_and_observations
credentials: read-write

networks:
  polygon  evm  chain 137, latest 78123, final 78100, position 78090, behind 10
    jpyc        implementation 0xa1b2c3...
```

`behind` は latest ではなく確定ブロックからの差です。周が読むのは確定済みの範囲だからです。
読めなかった network は、数字の代わりにその旨を出します。プロバイダ自身の code と文言が入り、
長さは切られていて、エンドポイントの断片は入りません。

チェーンは、起動時と同じアダプタで読みます。ここで出るものが、インスタンスが実際に出会うもの
です。何も書きません。

データベースの無い配備でも、schema を当てていない配備でも、チェーンは読みます。言えなくなるのは
それぞれをどこまで読んだかだけです。

## suco network cursor

```bash
suco network cursor <name> <height>
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
置くと、その範囲の送金は全て見られないままになり、後から拾う経路はありません。この副命令は
高さを現在の cursor と比べません。履歴を捨てたプロバイダがまさにこの副命令の対象で、そこでは前へ
進むしか道がないからです。

cursor がどこにいたかと、どこへ動いたかをログに書きます。どこにいたかが、戻すための記録です。

## suco payment await

```bash
suco payment await <id>
```

payment 1 件を支払い可能にして、支払者が署名する 7 つの値を印字します。支払い画面ができるまでの
代わりです。

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
する秒へ切り捨てた値です。

**印字したものは、実行した端末の外へ出さないでください。** 読んだ人は何をどこへ払うのかが
分かり、支払者の鍵も持っていれば支払者として署名できます。

鍵を発行するには、payment が `awaiting_payment` であり、その network が一度は読まれている必要が
あります。一度も読まれていないチェーンに対して鍵を渡すと、署名され、支払われ、誰も見ません。
既に支払い可能な payment でもう一度実行しても、2 本目の鍵は出ません。既に 1 本あると言います。
