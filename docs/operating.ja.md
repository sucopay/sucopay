# 運用

English: [operating.md](operating.md)

このページは、インスタンスが自分について答えることと、インスタンスを直す 4 つの副命令を
説明します。

インスタンスを動かす運用者向けです。[configuration.ja.md](configuration.ja.md) のとおりに
設定ファイルを書けることを前提にします。

## このページの語

| 語 | 意味 |
|---|---|
| インスタンス | `suco serve` のプロセス 1 つ |
| `network` | インスタンスが読むチェーン 1 本。名前は設定ファイルのキー |
| 資産（`asset`） | 1 つの `network` 上の 1 つのトークン。名前は設定ファイルのキー |
| 発行者 | 資産のコントラクトを動かし、資産の送金を止められる相手 |
| 送金（`transfer`） | チェーン上で資産が動いた記録。suco がチェーンから読む |
| 確定 | チェーンが送金をもう取り消さないと分かった状態 |
| 通知（`deliveries`） | 支払いか返金の変化を、1 つの Webhook エンドポイントへ 1 回送ること |
| `round` | チェーンを読む 1 回 |
| RPC エンドポイント | インスタンスがチェーンを読むために呼ぶ URL |
| RPC プロバイダー | RPC エンドポイントを動かす側。運用者自身のノードか、第三者 |
| `cursor` | `round` がチェーンをどこまで読んだかの位置 |
| lease | 1 本の `network` を受け持つ権利。1 つのインスタンスが持ち、残りは待機になる |
| worker | `round` を繰り返す、インスタンスの中のループ |
| 受信側 | Webhook エンドポイントの URL で `POST` を受け取る、加盟店のプログラム |
| 受取アドレス（`destination`） | 支払者が払う先のウォレットのアドレス。`suco asset accept` が資産ごとに記録する |
| domain | コントラクトが署名に使う値。`assets.<name>.eip712` と `network` の `chain_id` が決める |

## /healthz と /readyz

`/healthz` と `/readyz` は資格情報を求めません。ポートに届く誰もが読めます。レスポンスは、
理由も、ホスト名も、RPC エンドポイントの断片も持ちません。

### /healthz

`GET /healthz` はプロセスが動いている間 `200` を返します。何も参照しません。liveness probe は
`/healthz` に向けてください。ほかの API エンドポイントには向けないでください。データベースに
届かないことで失敗する probe は、動いているインスタンスを orchestrator に再起動させます。
再起動してもデータベースは戻りません。

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
| `credentials` | 資格情報の store が何を持っているか。store が無ければ出ない |
| `networks` | `network` ごとに 1 語。下の表 |
| `assets` | 資産ごとに 1 語。下の表 |
| `paused` | 資産ごとに、発行者が資産の送金を全部止めているか |
| `finality` | `network` ごとに 1 語。下の表 |
| `webhooks` | suco Pay 全体で 1 語。下の表 |

データベースを設定していないインスタンスは、チェーンを読まず、確定も判定せず、通知も
送りません。`networks`、`assets`、`paused`、`finality`、`webhooks` は出ません。

`status` が `unavailable` になるのは、次のどちらかのときです。`unavailable` のレスポンスは
`503` です。

- データベースに届かない
- 設定した `network` の全部が `unreachable`、`stalled`、`chain-mismatch`、`no-finalized` のどれか

`networks` の残りの 4 語では `ok` のままです。API は動いていて、動けるのは運用者か
RPC プロバイダーです。`finality` と `webhooks` の語は `status` を `unavailable` にしません。
確定を判定していないインスタンスでも、送金は見つかり、記録されます。額はどちらにしても
加盟店のアドレスにあります。

### networks

| 語 | 意味 | 対処 |
|---|---|---|
| `observing` | 60 秒以内に `round` が終わった。終わるとは、確定済みの範囲を読んで結果を書くこと。確定より先の読みの失敗は数えない | なし |
| `no-cursor` | チェーンが答え、`suco.yaml` が指定するチェーンでもあり、まだ 1 回も `round` が終わっていない | 最初の `round` を待つ |
| `unreachable` | 最後の head の読みが失敗した | RPC プロバイダーと RPC エンドポイントを確かめる。インスタンスが何に届くかは `suco doctor` が出力する |
| `stalled` | 60 秒の間 `round` が終わっていない。`cursor` のある範囲の履歴を捨てた RPC プロバイダーに取り残された `network` も `stalled` になる | RPC プロバイダーを確かめる。履歴が消えていれば、下の `suco network cursor` で `cursor` を動かす |
| `chain-mismatch` | `eth_chainId` が `suco.yaml` と違う。読みも書きもしない | `network` の `chain_id` か RPC エンドポイントを直す |
| `no-finalized` | RPC プロバイダーが確定ブロックを返さない。読みも書きもしない | `finalized` のブロックを答える RPC プロバイダーにする |
| `finalized-changed` | `cursor` のいるブロックをチェーンが持っていない。誰かが `cursor` を置き直すまで動かない | 下の `suco network cursor` で `cursor` を動かす |
| `finalized-behind` | RPC プロバイダーの確定ブロックが `cursor` より低い。RPC プロバイダーが遅れていて、追いつけば読みが再開する | 待つか、追いついている RPC プロバイダーにする |

60 秒は、インスタンスが `network` に対して持つ 30 秒の lease の 2 倍です。

読みが止まっている `network` では、支払いは期限切れになりません。`unreachable`、`stalled`、
`chain-mismatch`、`no-finalized`、`finalized-changed` のどれでも期限切れになりません。期限切れを
決めるのは、時計ではなく、読んだ位置のブロックの時刻です。規則は
[configuration.ja.md](configuration.ja.md) にあります。

### finality

チェーンを読むことと、確定を判定することは別です。`finality` が答えるのは、確定の判定です。

| 語 | 意味 | 対処 |
|---|---|---|
| `deciding` | `round` が RPC エンドポイントに問い合わせ、返った答えが決めたことを書いた | なし |
| `no-round` | インスタンスが起きてから `round` が 1 度も終わっていない。まだ何も起きていない | 最初の `round` を待つ |
| `waiting` | 別のインスタンスが `network` の lease を持っている。答えたインスタンスは待機で、判定は lease を持つインスタンスで動いている | なし |
| `too-few` | 直前の `round` で答えた RPC エンドポイントが、一致に必要な数に足りない。問い合わせた送金は、別の RPC エンドポイントが答えるまで動かない。何本が答えるかは `suco doctor` が出力する | `rpc.others` の RPC エンドポイントを足すか直す。一致に必要な数は [configuration.ja.md](configuration.ja.md) にある |
| `unreachable` | 直前の `round` が終わらなかった。答えない RPC エンドポイントはもう `round` を終わらせない。止めたのはデータベースで、`database` が別に示す | `database` を見る |
| `stalled` | `round` が終わらなくなった。`round` は自分の入っているループを終われない。`round` の中で worker が止まっている | インスタンスを再起動する |

`deciding` と `too-few` は、`networks.<name>.finality.recheck` の 3 回分の `round` の間
出ています。1 回遅れただけで語が変わらないようにするためです。

### assets と paused

`assets` は資産ごとに `unchanged` か `changed` です。`network` のチェーンが資産に対して動かす
コードが、インスタンスの起動時のコードと違えば `changed` になります。コードが変わるのは、
proxy を差し替えたときです。`changed` の資産を使い続ける前に、変更を発行者に
確かめてください。

`paused` は資産ごとに、発行者が資産の送金を全部止めているかを、コントラクトが答えたとおりに
出力します。1 分に 1 度読みます。`paused` に無い資産は、直前の 2 分の間に読めていません。理由は
3 つのどれかです。コントラクトが答えなかったか、RPC プロバイダーが呼び出しを運ばなかったか、
インスタンスが読んでいないかです。`paused` に無いことは「止まっていない」ではありません。
止まっている資産があっても `status` は変わりません。API は動いていて、動けるのは発行者だけです。

### webhooks

`webhooks` は suco Pay 全体で 1 語です。全部の Webhook エンドポイントへの通知を 1 つの worker が
送るからです。

| 語 | 意味 | 対処 |
|---|---|---|
| `delivering` | `round` が、支払いの変化を通知にし、送る時刻の来た通知を送った | なし |
| `no-round` | インスタンスが起きてから `round` が 1 度も終わっていない | 最初の `round` を待つ |
| `waiting` | 別のインスタンスが通知を持っている。答えたインスタンスは待機 | なし |
| `unreachable` | 直前の `round` が終わらなかった。止めたのはデータベース。答えない受信側は通知の試行として記録され、`round` を止めない | `database` を見る |
| `stalled` | `round` が終わらなくなった | インスタンスを再起動する |

`delivering` は 15 秒、5 秒の `round` の 3 回分、出ています。`webhooks` のどの語も `status` を
`unavailable` にしません。加盟店に伝わらなかったことは、加盟店が読めます。

## suco doctor

`suco doctor` は、解決した設定とそれぞれの出所を出力し、続けて何に届くかを出力します。
チェーンは起動時と同じ読み方で読みます。報告の内容は、起動したインスタンスが出会う内容と
同じです。何も書きません。

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
| `2 waiting to settle` | RPC エンドポイントへの問い直しを待っている、記録済みの送金の数 | なし。報告のたびに増えていくなら、確定の判定が進まなくなっている。どの語なのかは `/readyz` が示す |
| `1 disagreed about` | RPC エンドポイントが食い違っている送金の数。待っている数の後に、あるときだけ出る | 対処する。食い違いからは何も確定せず、放っておいても直らない |
| `3 of 4 others answer` | `others` だけで届く `network` で、`others` の何本が答えるか | なし |
| `no spare` | 答える本数が、一致に必要な数とちょうど同じ | `rpc.others` に RPC エンドポイントを足す |
| `too few to settle` | 答える本数が、一致に必要な数に足りない | `rpc.others` の RPC エンドポイントを足すか直す |
| `behind 10` | 読んだ位置が確定ブロックよりどれだけ下か。latest ではなく確定ブロックからの差で、`round` が読むのは確定済みの範囲 | なし |
| `could not be read: …` | 読めなかった `network`。数字の代わりに出る。RPC プロバイダー自身の code と文言が入り、長さは切られていて、RPC エンドポイントの断片は入らない | RPC プロバイダーと RPC エンドポイントを確かめる |
| `chain 80002 (Polygon Amoy, a testnet)` | `network` が答える chain id が、テストネットの chain id。設定ファイルが `network` を何と呼んでいても出る | なし。本番向けの設定ファイルなら、本番の前の段から写している |
| `paused` | 発行者が資産の送金を全部止めている | 発行者に確かめる。止まっていない資産の行には何も出ない |
| `paused not read` | 資産のコントラクトが、止まっているかどうかを答えなかった | RPC プロバイダーを確かめる |
| `paid to 0x…, which is blocklisted, the provider says` | 資産のコントラクトが、account の受取アドレスへの送金を拒む | 別のアドレスで `suco asset accept` を実行する |
| `2 pending, 1 failed to 7c1d…` | データベースが持つ、1 つの account の通知。次の試行を待つ `pending` と、最後の試行の後に諦めた `failed`。`failed` には、通知を送る Webhook エンドポイントの id を添える | なし |
| `nothing pending, nothing failed` | 待っている通知も、諦めた通知も無い | なし |
| `3 endpoints hold a secret sealed under another key` | 今と違う鍵で暗号化した署名シークレットを持つ Webhook エンドポイントの数。`credentials.key` を入れ替えた後に残る。1 つ以上あるときだけ account の後に出る | なし。加盟店が署名シークレットを更新すれば、通知は続く |

通知を送る worker 自身が何をしているかは、`/readyz` の `webhooks` の語が示します。

資産の受取アドレスは、問い合わせのときに `network` の RPC エンドポイントへ渡ります。運用者の
`own` のノードか、`others` の最初の RPC エンドポイントです。読めなかった `network` の
受取アドレスには問い合わせません。データベースの account がちょうど 1 つでないときも
問い合わせません。account を指定することは実装していません。

データベースの無いインスタンスでも、schema を当てていないインスタンスでも、チェーンは
読みます。失われるのは、`network` ごとにどこまで読んだかの記録だけです。

## suco asset accept

```bash
suco asset accept <name> <address>
```

設定ファイルが `<name>` で挙げている資産を、`<address>` への支払いとして account が
受け付けることを記録します。先に、資産のコントラクトへ 2 つ問い合わせます。1 つは、
コントラクトが署名に使う domain です。支払者のウォレットも同じ domain で署名します。
もう 1 つは、`<address>` への送金を拒むかどうかです。発行者は account ごとに送金を
止められます。

| 拒否するとき | 対処 |
|---|---|
| コントラクトの domain が、設定ファイルの値と違う | `assets.<name>.eip712` と `network` の `chain_id` を、コントラクトと照らして直す |
| コントラクトが `<address>` への送金を拒む | 別のアドレスにする |
| 2 つのうち、どちらかの答えを読めない | RPC プロバイダーを確かめる。問い合わせを運ばない RPC プロバイダーを根拠に、アドレスを登録しない |

JPYC のように `DOMAIN_SEPARATOR()` を持たないコントラクトは、コントラクトが答える値と
照合します。どのチェーンに居るかを `network` の `chain_id` と照合し、コントラクトが名乗る
名前を domain の `name` と照合します。`version` は設定ファイルの値を取ります。登録を確認する
行にも `version` が出ます。

問い合わせるのは `network` の RPC エンドポイントです。運用者の `own` のノードか、`others` の
最初の RPC エンドポイントです。`<address>` は、問い合わせの中で RPC プロバイダーへ渡ります。
支払いが 1 つ届けば公開になります。

## suco network cursor

```bash
suco network cursor <name> <height> [--force]
```

`cursor` は `network` ごとに 1 つあります。`round` がどの高さまで読んだかと、その高さにある
ブロックのハッシュを持ちます。位置は `cursor` のいる高さです。

`cursor` のいるブロックをチェーンが持たなくなると、どの `round` も先へ進めません。どこまで
戻るかは `round` に決められません。`suco network cursor` は、チェーンが持っているブロックへ
`cursor` を置き直します。`cursor` のいるブロックをチェーンが持たない間、`/readyz` は
`network` は `finalized-changed` です。

指定した高さのブロックをチェーンから読み、そのハッシュと一緒に書きます。高さだけでは、どの
チェーンの高さかが決まりません。

lease は取りません。書き込みの途中にいた `round` は前進を commit しません。次の `round` は、
置き直した位置から読みます。

**飛ばしたブロックは、どの `round` も読みません。** まだどの `round` も読んでいない
ブロックより先へ `cursor` を置くと、飛ばしたブロックの送金は全て見られないままになります。
後から拾う仕組みはありません。`network` でまだ開いている支払いが、飛ばしたブロックで
払われていることがあります。`cursor` が期限を越えた時点で、支払いは払われなかったとして
期限切れになります。まだ開いている返金が、飛ばしたブロックで送られていることもあります。
返金は、額が出たまま期限切れになります。額はもう一度返金できる状態に戻ります。同じ額が
二度出ていきます。`suco network cursor` は、`network` に `awaiting_payment` か
`awaiting_finality` の支払いがあるあいだ、前へ進めることを拒否します。`created` か
`awaiting_finality` の返金があるあいだも拒否します。拒否するときは、それぞれの件数を出力します。
`--force` を付けると進め、飛ばしたそれぞれの件数をログに書きます。後ろへ戻すのは拒否しません。
何も飛ばしません。`round` が戻した範囲を読み直します。

履歴を捨てた RPC プロバイダーが、`suco network cursor` の必要な場面です。前へ進むしか道が
ありません。何を失うかを数えてから `--force` を使ってください。

`cursor` がどこにいたかと、どこへ動いたかをログに書きます。どこにいたかが、戻すための
記録です。

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

`nonce` は、送金を支払いに突き合わせる鍵です。`validBefore` は支払いの期限を、チェーンが
比較する秒へ切り捨てた値です。署名するのは支払者です。出力した `validBefore` より先の期限で
署名された送金が返ってくることがあります。`validBefore` の時刻に達してから運ばれた送金は、
支払いに充当せず記録します。

**出力した 7 つの値は、実行した端末の外へ出さないでください。** 読んだ人は何をどこへ払うのかが
分かり、支払者の鍵も持っていれば支払者として署名できます。

鍵を発行するには、支払いが `awaiting_payment` であり、支払いの `network` が一度は
読まれている必要があります。一度も読まれていないチェーンに対して鍵を渡すと、署名され、
支払われ、誰も見ません。既に支払い可能な支払いでもう一度実行しても、2 本目の鍵は出ません。
既に 1 本あると出力します。

## 内側のネットワークにある Webhook エンドポイント

private なネットワークや loopback など、suco Pay が動いているネットワークの内側のアドレスに
解決する Webhook の URL は、登録時と毎回の送信時に拒否します。suco Pay からしか届かない
アドレスを、加盟店が suco Pay に呼ばせることを防ぎます。運用者が自分の受信側を内側で動かす
ときは、Webhook エンドポイントの行にアドレスか範囲を書きます。許可は、行を書いた
Webhook エンドポイント 1 つだけに効きます。

```sql
update webhook_endpoints set allowed = '{10.0.5.0/24}' where id = '<endpoint id>';
```

許可は Webhook エンドポイントとアドレスに結び付き、名前には結び付きません。名前を別の内側の
アドレスに向け直せば、拒否します。内側に解決する URL は登録できません。加盟店が
外側に解決する URL で登録し、運用者が許可を書き、加盟店が `PATCH /webhook_endpoints/{id}` で
URL を変えます。変更時の検査は許可込みです。prefix として読めない値は何も許さず、他の通知も
止めません。

## 関連

- [configuration.ja.md](configuration.ja.md): 設定の意味と、`others` の何本が一致すればよいか
- [api.ja.md](api.ja.md): `network cursor` と `payment await` が挙げる支払いの状態
- [refunds.ja.md](refunds.ja.md): `network cursor` が挙げる返金の状態
- [webhooks.ja.md](webhooks.ja.md): `doctor` が数える通知について、受信側がすること
