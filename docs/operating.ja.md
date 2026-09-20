# 運用

English: [operating.md](operating.md)

インスタンスが自分について答えること、そして直すための 4 つの副命令です。

## /healthz と /readyz

どちらも資格情報を求めません。ポートに届く誰もが読めます。どちらも理由も、ホスト名も、
RPC エンドポイントの断片も持ちません。

### /healthz

`GET /healthz` はプロセスが動いている間 `200` を返します。何も参照しません。liveness probe は
ここに向け、ほかに向けないでください。データベースに届かないことで失敗する probe は、動いている
インスタンスを orchestrator に再起動させます。それでデータベースは戻りません。

### /readyz

`GET /readyz` は、インスタンスが仕事をできるかを答えます。

```json
{"status":"ok","database":"reachable","credentials":"read-write",
 "networks":{"polygon":"observing"},"assets":{"jpyc":"unchanged"},"paused":{"jpyc":false},
 "finality":{"polygon":"deciding"},"webhooks":"delivering"}
```

| 項目 | 説明 |
|---|---|
| `status` | `ok` か `unavailable` |
| `database` | `reachable` か `unreachable`。データベースを設定していないインスタンスでは `none configured` |
| `credentials` | 資格情報の store が持つもの。store が無ければ出ない |
| `networks` | `network` ごとに 1 語。下の表 |
| `assets` | 資産ごとに 1 語。下の表 |
| `paused` | 資産ごとに、発行者がその資産の送金を全部止めているか |
| `finality` | `network` ごとに 1 語。下の表 |
| `webhooks` | suco Pay 全体で 1 語。下の表 |

データベースを設定していないインスタンスは、チェーンを読まず、確定も判定せず、Webhook も
送りません。`networks`、`assets`、`paused`、`finality`、`webhooks` は出ません。

`status` が `unavailable` になるのは、データベースに届かないとき、または設定した `network` の
全部が `unreachable`、`stalled`、`chain-mismatch`、`no-finalized` のどれかであるときです。
そのときレスポンスは `503` です。残りの 4 語では `ok` のままです。API は動いていて、動けるのは運用者か
プロバイダーです。`finality` と `webhooks` の語は `unavailable` にしません。判定が動いていない
インスタンスでも、送金は見つかり、記録されます。額はどちらにしても加盟店のアドレスにあります。

### networks

| 語 | 意味 | 対処 |
|---|---|---|
| `observing` | 60 秒以内に `round`（チェーンを読む 1 回）が終わった。終わるとは、確定済みの範囲を読んで結果を書くこと。確定より先の読みの失敗は数えない | なし |
| `no-cursor` | チェーンが答え、`suco.yaml` が名指すチェーンでもあり、まだ 1 回も `round` が終わっていない | 最初の `round` を待つ |
| `unreachable` | 最後の head の読みが失敗した | プロバイダーと RPC エンドポイントを確かめる。インスタンスが何に届くかは `suco doctor` が出力する |
| `stalled` | 60 秒の間 `round` が終わっていない。`cursor` のある範囲の履歴を捨てたプロバイダーに取り残された `network` もここ | プロバイダーを確かめる。履歴が消えていれば、下の `suco network cursor` で `cursor` を動かす |
| `chain-mismatch` | `eth_chainId` が `suco.yaml` と違う。読みも書きもしない | `network` の `chain_id` か RPC エンドポイントを直す |
| `no-finalized` | プロバイダーが確定ブロックを言わない。読みも書きもしない | `finalized` のブロックを答えるプロバイダーにする |
| `finalized-changed` | `cursor` のいるブロックをチェーンが持っていない。誰かが `cursor` を置き直すまで動かない | 下の `suco network cursor` で `cursor` を動かす |
| `finalized-behind` | プロバイダーの確定ブロックが `cursor` より低い。プロバイダーが遅れていて、追いつけば読みが再開する | 待つか、追いついているプロバイダーにする |

60 秒は、インスタンスが `network` に対して持つ 30 秒の lease の 2 倍です。

読みが止まっている間は、その `network` の支払いは期限切れになりません。`unreachable`、
`stalled`、`chain-mismatch`、`no-finalized`、`finalized-changed` のどれでも同じです。期限切れを
決めるのは、時計ではなく、読んだ位置のブロックの時刻です。規則は
[configuration.ja.md](configuration.ja.md) にあります。

### finality

チェーンを読むことと、確定を判定することは別です。後者を答えるのが `finality` です。

| 語 | 意味 | 対処 |
|---|---|---|
| `deciding` | `round` が RPC エンドポイントに問い合わせ、その答えが決めたことを書いた | なし |
| `no-round` | このインスタンスが起きてから `round` が 1 度も終わっていない。まだ何も起きていない | 最初の `round` を待つ |
| `waiting` | 別のインスタンスがその `network` を持っている。こちらは控えで、判定は向こうで動いている | なし |
| `too-few` | 直前の `round` で答えた RPC エンドポイントが、一致に要る数に足りない。問い合わせたものはその場に残り、別の RPC エンドポイントが答えるまでそのまま。何本が答えるかは `suco doctor` が出力する | `rpc.others` の RPC エンドポイントを足すか直す。一致に要る数は [configuration.ja.md](configuration.ja.md) にある |
| `unreachable` | 直前の `round` が終わらなかった。答えない RPC エンドポイントはもう `round` を終わらせない。止めたのはデータベースで、`database` が別に言う | `database` を見る |
| `stalled` | `round` が終わらなくなった。`round` は自分の入っているループを終われない。これは `round` の中で止まった worker | インスタンスを再起動する |

`deciding` と `too-few` は、`networks.<name>.finality.recheck` の 3 回分の `round` の間
立っています。1 回遅れただけで語が変わらないようにするためです。

### assets と paused

`assets` は資産ごとに `unchanged` か `changed` です。そのチェーンが資産に対して動かすコードが、
インスタンスの起動時のものと違えば `changed` になります。proxy の差し替えがこれにあたります。
その資産を使い続ける前に、変更を発行者に確かめてください。

`paused` は資産ごとに、発行者がその資産の送金を全部止めているかを、コントラクトが答えたとおりに
言います。1 分に 1 度読みます。`paused` に無い資産は、この 2 分の間に読めなかったものです。
理由は 3 つのどれかです。コントラクトが答えなかったか、プロバイダーが呼び出しを運ばなかった
か、このインスタンスが読んでいないかです。無いことは「止まっていない」ではありません。止まっている資産が
あっても `status` は変わりません。API は動いていて、動けるのは発行者だけです。

### webhooks

`webhooks` は suco Pay 全体で 1 語です。全部の Webhook エンドポイントへの配送を 1 つの worker が
送るからです。

| 語 | 意味 | 対処 |
|---|---|---|
| `delivering` | `round` が、支払いの変化を配送にし、時刻の来たものを送った | なし |
| `no-round` | このインスタンスが起きてから `round` が 1 度も終わっていない | 最初の `round` を待つ |
| `waiting` | 別のインスタンスが配送を持っている。こちらは控え | なし |
| `unreachable` | 直前の `round` が終わらなかった。止めたのはデータベース。答えない受信側は試行として記録され、`round` を止めない | `database` を見る |
| `stalled` | `round` が終わらなくなった | インスタンスを再起動する |

`delivering` は 15 秒、5 秒の `round` の 3 回分、立っています。この語も `unavailable` に
しません。加盟店に伝わらなかったことは、加盟店が読めます。

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

asset の行は、発行者が止めていれば `paused`、契約が答えなければ `paused not read` を足します。
止まっていない asset には何も足しません。配備の唯一の account がその asset を受け付けていれば、
その宛先への送金を契約が拒むかを訊き、拒めば `paid to 0x…, which is blocklisted, the provider
says` と書きます。読めなかった network の宛先は訊かず、account が 2 つあるデータベースでも
訊きません。名指しは実装していません。訊くときは宛先のアドレスが network の endpoint に渡ります。
運用者の own の node か、others の最初の 1 つです。

チェーンは、起動時と同じアダプタで読みます。ここで出るものが、インスタンスが実際に出会うもの
です。何も書きません。

データベースの無い配備でも、schema を当てていない配備でも、チェーンは読みます。言えなくなるのは
それぞれをどこまで読んだかだけです。

## suco asset accept

```bash
suco asset accept <name> <address>
```

account がその名前の asset を、そのアドレスへの支払いとして受け付けることを記録します。先に
asset の契約に 2 つ訊きます。何に対して署名するかと、そのアドレスへの送金を拒むかです。前者は
支払者のウォレットが同じものに署名するため、後者は発行者が account ごとに止められるためです。
署名するものが文書の値と違えば登録を断り、契約がそのアドレスを拒んでも断ります。答えを
読めなくても断ります。問いを運ばない provider を根拠に、アドレスを登録しません。
`DOMAIN_SEPARATOR()` を持たない契約は、答えるものと照合します。JPYC がそうです。どの
チェーンに居るかを network の `chain_id` と照合し、契約が名乗る名前を domain の `name` と
照合します。`version` は文書の値を取り、登録を確認する行にそう出ます。訊く先は network の
endpoint で、運用者の own の node か others の最初の 1 つです。アドレスはその問いの中で
provider に渡ります。支払いが 1 つ届けば公開になるものです。

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
