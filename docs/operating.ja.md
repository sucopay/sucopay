# 運用

English: [operating.md](operating.md)

このページは、インスタンスが自分について答えることと、それを直す 4 つの副命令を説明します。

インスタンスを動かす運用者向けです。[configuration.ja.md](configuration.ja.md) のとおりに
設定ファイルを書けることを前提にします。

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

`status` が `unavailable` になるのは、次のどちらかのときです。そのときレスポンスは `503` です。

- データベースに届かない
- 設定した `network` の全部が `unreachable`、`stalled`、`chain-mismatch`、`no-finalized` のどれか

残りの 4 語では `ok` のままです。API は動いていて、動けるのは運用者かプロバイダーです。
`finality` と `webhooks` の語は `unavailable` にしません。判定が動いていないインスタンスでも、
送金は見つかり、記録されます。額はどちらにしても加盟店のアドレスにあります。

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

`paused` は資産ごとに、発行者がその資産の送金を全部止めているかを、コントラクトが答えた
とおりに言います。1 分に 1 度読みます。`paused` に無い資産は、この 2 分の間に読めなかった
ものです。理由は 3 つのどれかです。コントラクトが答えなかったか、プロバイダーが呼び出しを
運ばなかったか、このインスタンスが読んでいないかです。無いことは「止まっていない」では
ありません。止まっている資産があっても `status` は変わりません。API は動いていて、動けるのは
発行者だけです。

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

`suco doctor` は、解決した設定とそれぞれの出所を出力し、続けて何に届くかを出力します。チェーンは
起動時と同じ読み方で読むので、報告は起動したインスタンスが出会うものと同じです。何も
書きません。

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

| 語 | 意味 | 対処 |
|---|---|---|
| `2 waiting to settle` | RPC エンドポイントへの問い直しを待っている、記録済みの送金の数 | なし。報告のたびに増えていくなら、確定の判定が進まなくなっている。どの語なのかは `/readyz` が言う |
| `1 disagreed about` | RPC エンドポイントが食い違っている送金の数。待っている数の後に、あるときだけ出る | そのままにしない。食い違いからは何も確定せず、放っておいても直らない |
| `3 of 4 others answer` | `others` だけで届く `network` で、その何本が答えるか | なし |
| `no spare` | 答える本数が、一致に要る数とちょうど同じ | `rpc.others` に RPC エンドポイントを足す |
| `too few to settle` | 答える本数が、一致に要る数に足りない | `rpc.others` の RPC エンドポイントを足すか直す |
| `behind 10` | 読んだ位置が確定ブロックよりどれだけ下か。latest ではなく確定ブロックからの差で、`round` が読むのは確定済みの範囲 | なし |
| `could not be read: …` | 読めなかった `network`。数字の代わりに出る。プロバイダー自身の code と文言が入り、長さは切られていて、RPC エンドポイントの断片は入らない | プロバイダーと RPC エンドポイントを確かめる |
| `chain 80002 (Polygon Amoy, a testnet)` | その `network` が答える chain id がテストネットのもの。設定ファイルがその `network` を何と呼んでいても出る | なし。本番向けの設定ファイルなら、本番の前の段から写している |
| `paused` | 発行者がその資産の送金を全部止めている | 発行者に確かめる。止まっていない資産の行には何も出ない |
| `paused not read` | その資産のコントラクトが、止まっているかどうかを答えなかった | プロバイダーを確かめる |
| `paid to 0x…, which is blocklisted, the provider says` | その資産のコントラクトが、account の受取アドレスへの送金を拒む | 別のアドレスで `suco asset accept` を実行する |
| `2 pending, 1 failed to 7c1d…` | データベースが持つ、1 つの account の配送。次の試行を待つ `pending` と、最後の試行の後に諦めた `failed`。`failed` には、その配送の Webhook エンドポイントの id を添える | なし |
| `nothing pending, nothing failed` | 待っている配送も、諦めた配送も無い | なし |
| `3 endpoints hold a secret sealed under another key` | 今と違う鍵で暗号化した署名シークレットを持つ Webhook エンドポイントの数。`credentials.key` を入れ替えた後に残るもので、あるときだけ account の後に出る | なし。その加盟店が署名シークレットを更新すれば、配送は続く |

配送を送る worker 自身が何をしているかは、`/readyz` の `webhooks` の語が言います。

資産の受取アドレスは、問い合わせのときに `network` の RPC エンドポイントへ渡ります。運用者の
`own` のノードか、`others` の最初の 1 つです。読めなかった `network` の受取アドレスには問い
合わせません。データベースの account がちょうど 1 つでないときも問い合わせません。account を
名指すことは実装していません。

データベースの無いインスタンスでも、schema を当てていないインスタンスでも、チェーンは読み
ます。言えなくなるのは、それぞれをどこまで読んだかだけです。

## suco asset accept

```bash
suco asset accept <name> <address>
```

その名前で設定ファイルが挙げている資産を、そのアドレスへの支払いとして account が受け付けることを
記録します。先に、その資産のコントラクトへ 2 つ問い合わせます。1 つは何に対して署名するかで、
支払者のウォレットも同じものに署名します。もう 1 つはそのアドレスへの送金を拒むかで、発行者は
account ごとに止められます。

| 断るとき | 対処 |
|---|---|
| コントラクトが署名するものが、設定ファイルの値と違う | `assets.<name>.eip712` と `network` の `chain_id` を、コントラクトと照らして直す |
| コントラクトがそのアドレスへの送金を拒む | 別のアドレスにする |
| 2 つのうち、どちらかの答えを読めない | プロバイダーを確かめる。問い合わせを運ばないプロバイダーを根拠に、アドレスを登録しない |

`DOMAIN_SEPARATOR()` を持たないコントラクトは、答えるものと照合します。JPYC がそうです。
どのチェーンに居るかを `network` の `chain_id` と照合し、コントラクトが名乗る名前を domain の
`name` と照合します。`version` は設定ファイルの値を取り、登録を確認する行にそう出ます。

問い合わせる先は `network` の RPC エンドポイントで、運用者の `own` のノードか `others` の
最初の 1 つです。アドレスはその問い合わせの中でプロバイダーへ渡ります。支払いが 1 つ届けば
公開になるものです。

## suco network cursor

```bash
suco network cursor <name> <height> [--force]
```

`cursor` は動くもので、位置はその居場所です。`network` ごとに 1 つあり、`round` がどの高さ
まで読んだかと、そこのブロックのハッシュを持ちます。

`cursor` のいるブロックをチェーンが持たなくなると、どの `round` もそこから先へ進めません。
どこまで戻るかは `round` に決められません。チェーンが持っているブロックへ `cursor` を置き直す
のがこの副命令です。その状態のあいだ、`/readyz` はその `network` を `finalized-changed` と
言います。

指定した高さのブロックをチェーンから読み、そのハッシュと一緒に書きます。高さだけでは、どの
チェーンの高さかが決まりません。

lease は取りません。途中で置き直された `round` は前進を commit せず、次の `round` が置かれた
位置から読みます。

**飛ばしたブロックを読むものはありません。** まだどの `round` も読んでいないブロックより先へ
`cursor` を置くと、その範囲の送金は全て見られないままになり、後から拾う仕組みはありません。
その `network` でまだ開いている支払いが、飛ばしたブロックで払われていることがあります。
`cursor` が期限を越えた時点で、払われなかったものとして期限切れになります。まだ開いている
返金が、飛ばしたブロックで送られていることもあります。額が出たまま期限切れになり、その額が
もう一度返金できる状態に戻ります。同じ額が二度出ていきます。そのため、`network` に
`awaiting_payment` か `awaiting_finality` の支払いがあるあいだは、この副命令は前へ進める
ことを拒みます。`created` か `awaiting_finality` の返金があるあいだも同じです。拒むときは、
それぞれの件数を言います。`--force` を付けると進め、飛ばしたそれぞれの件数をログに書きます。
後ろへ戻すのは拒みません。何も飛ばさず、`round` がその範囲を読み直します。

履歴を捨てたプロバイダーがまさにこの副命令の対象で、そこでは前へ進むしか道がありません。
何を失うかを数えてから `--force` を使ってください。

`cursor` がどこにいたかと、どこへ動いたかをログに書きます。どこにいたかが、戻すための記録
です。

## suco payment await

```bash
suco payment await <id>
```

支払い 1 件を支払い可能にして、支払者が署名する 7 つの値を出力します。suco Checkout が
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

`nonce` は、送金をその支払いに突き合わせる鍵です。`validBefore` は支払いの期限を、チェーンが
比較する秒へ切り捨てた値です。署名するのは支払者です。ここに出た値より先の期限で署名された
送金が返ってくることがあります。ここに出た期限に達してから運ばれた送金は、支払いに充当せず
記録します。

**出力したものは、実行した端末の外へ出さないでください。** 読んだ人は何をどこへ払うのかが
分かり、支払者の鍵も持っていれば支払者として署名できます。

鍵を発行するには、支払いが `awaiting_payment` であり、その `network` が一度は読まれている
必要があります。一度も読まれていないチェーンに対して鍵を渡すと、署名され、支払われ、誰も
見ません。既に支払い可能な支払いでもう一度実行しても、2 本目の鍵は出ません。既に 1 本あると
言います。

## 内側のネットワークにある Webhook エンドポイント

private なネットワークや loopback など、suco Pay が動いているネットワークの内側のアドレスに
解決する Webhook の URL は、登録時と毎回の送信時に断ります。suco Pay からしか届かない先を、
加盟店が suco Pay に呼ばせることを防ぎます。運用者が自分の受け取り側を内側で動かすときは、
Webhook エンドポイントの行にアドレスか範囲を書いて、その 1 つだけに許します。

```sql
update webhook_endpoints set allowed = '{10.0.5.0/24}' where id = '<endpoint id>';
```

許可は Webhook エンドポイントとアドレスに結び付き、名前には結び付きません。名前を別の内側の
アドレスに向け直せば、前と同じく断ります。内側に解決する URL は登録できません。加盟店が外側に
解決する URL で登録し、運用者が許可を書き、加盟店が `PATCH /webhook_endpoints/{id}` で URL を
変えます。変更時の検査は許可込みです。prefix として読めない値は何も許さず、他の配送も止め
ません。

## 関連

- [configuration.ja.md](configuration.ja.md): 設定の意味と、`others` の何本が一致すればよいか
- [api.ja.md](api.ja.md): `network cursor` と `payment await` が挙げる支払いの状態
- [refunds.ja.md](refunds.ja.md): `network cursor` が挙げる返金の状態
- [webhooks.ja.md](webhooks.ja.md): `doctor` が数える配送について、受信側がすること
