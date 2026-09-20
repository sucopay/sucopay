# 設定

English: [configuration.md](configuration.md)

このページは、`suco.yaml` が取る設定の全てと、それぞれが何を決めるか、既定値が何かを
説明します。

インスタンスを動かす運用者向けです。`suco init` が設定ファイルを書き出してあり、`suco doctor` を
実行できることを前提にします。

## 設定ファイル

`suco` が読む設定ファイルは 1 つです。`suco init` が書き出し、どの副命令も `suco.yaml`、または
`SUCO_CONFIG` が指すファイルから読みます。

`suco doctor` は、解決した値とそれぞれの出所を出力します。サーバーを起動せずに設定ファイルを
確かめられます。

書かなかった設定は既定値になります。設定ファイルにあって誰も読まないキーは断ります。

## 秘密

値は `${NAME}` と書けます。その名前の環境変数を読みます。参照は値の全体か、値でないかの
どちらかで、`https://${HOST}/rpc` は断ります。

次の設定は出力しません。`suco doctor` は設定されているかどうかだけを言い、エラーにもログにも
値は出ません。

- `database.url`
- `credentials.key`
- `networks.*.rpc.own`
- `networks.*.rpc.others`

これ以外の値はそのまま出力します。秘密ではない設定にユーザー名やパスワードを持つ URL が
あれば断ります。鍵の形をした 16 進 64 文字の値も断ります。どちらも、値の一部を出力しません。

## listen

| 設定 | 既定値 | 説明 |
|---|---|---|
| `listen.host` | `127.0.0.1` | 待ち受けるインターフェース。既定では他の機械から届きません |
| `listen.port` | `7826` | 1 から 65535 |
| `listen.base_url` | `http://localhost:<port>` | 他からこのインスタンスへ届く先。proxy の後ろでは `host` と違います。`http` か `https` で、ホスト名を持つ URL |

## log

| 設定 | 既定値 | 説明 |
|---|---|---|
| `log.level` | `info` | `debug`、`info`、`warn`、`error` |
| `log.format` | `text` | 端末で読むなら `text`、収集するものに渡すなら `json` |

## database

| 設定 | 既定値 | 説明 |
|---|---|---|
| `database.managed` | `true` | suco 自身が動かすデータベース。未実装です |
| `database.url` | なし | PostgreSQL の接続文字列。秘密 |

2 つは排他です。`managed: false` を書き、URL を渡してください。

```yaml
database:
  managed: false
  url: ${SUCO_DATABASE_URL}
```

`managed: true` と書いた設定ファイルは、設定ファイルを読むどの副命令も断ります。`url` の無い
`managed: false` も断ります。データベースについて何も書かない設定ファイルは、`managed` が既定値の
ままです。`suco serve` は `/healthz` と `/readyz` に答え、チェーンを読まず、何も保存せず、
データベースが要る副命令は全て断ります。

他の機械へ届く URL は、答えたのが誰かを確かめる接続を求めてください。`sslmode=verify-full`、
名前が一致し得ない環境では `verify-ca` です。これより弱い設定は、TLS を提案して、サーバーが
断れば平文で繋ぎます。通信の途中でこれを見られる人が、支払いの状態を読み書きできます。
loopback アドレスと unix socket は他の機械に届きません。そのままにします。

## credentials

| 設定 | 既定値 | 説明 |
|---|---|---|
| `credentials.key` | なし | 16 進 64 文字。保存された資格情報を hash する 32 バイトです。秘密 |
| `credentials.key_id` | なし | 保存された資格情報がどの鍵で作られたか。秘密ではありません |

`database.url` を書いた時点で、両方が必要になります。`suco init` が鍵をファイルへ書き、
そこから環境変数を設定する行を出力します。

鍵を差し替えても失効はしません。古い鍵で作った資格情報は有効なままで、提示できなくなる
だけです。`suco credential list` が、どの鍵で作られたかを行ごとに出力します。

## networks

`network` はインスタンスが読むチェーン 1 本です。名前は設定ファイルのキーで、資産が指す先です。
チェーンを読む 1 回を `round` と呼びます。

| 設定 | 既定値 | 説明 |
|---|---|---|
| `networks.<name>.kind` | `evm` | `evm` か `simulated` |
| `networks.<name>.chain_id` | なし | `eth_chainId` が答えるべき値。1 以上。`evm` は必須、`simulated` は持てません |
| `networks.<name>.rpc.own` | なし | 運用者が自分で動かしているノード。設定すると `round` はここを読みます。秘密 |
| `networks.<name>.rpc.others` | なし | 第三者の RPC エンドポイントの一覧。`own` が無いとき、`round` は先頭を読み、確定の判定は順に問い合わせます。秘密 |
| `networks.<name>.poll` | `12s` | `round` と `round` の間隔。`1s` 以上 |
| `networks.<name>.width` | `1000` | log を 1 回問い合わせる範囲の上限ブロック数。10 から 10000 |
| `networks.<name>.finality.recheck` | `1m` | 記録した送金を RPC エンドポイントに問い直す間隔。`1s` 以上 |
| `networks.<name>.finality.misses` | `10` | 続けて見つからなかった回数がこれに達すると、その送金を消えたものとして扱います。`2` 以上 |

`simulated` はプロセスの中で動くチェーンです。インスタンスの残りを試すためのもので、ブロックを
1 つ作り、そこに何も届きません。

資産が指さない `network` は断ります。

### rpc

RPC エンドポイントは、`https` ならどこでも、`http` なら `localhost` か loopback アドレスに
限ります。`http` では URL の鍵が平文で流れ、返ってくる receipt も平文です。ホストは書かれた
まま見ます。loopback へ解決する名前も断ります。

`own` を書いた `network` は、その 1 本にだけ問い合わせます。`own` が無い `network` は
`others` に順に問い合わせ、2 本の答えが一致したときに払われたと認めます。答えない 1 本は
飛ばします。並べるほど、揃えるべき答えが増えるのではなく、代わりが増えます。答えるのが
2 本を切れば、別の 1 本が答えるまでその `network` は `too-few` です。この語は
[operating.ja.md](operating.ja.md) にあります。違うことを言う 1 本があれば、支払いはその場に
留まり、3 本目の RPC エンドポイントには裁定させません。どちらでも送金は記録されたままで、
額は加盟店のアドレスにあります。

### poll と width

`poll` は、自分で動かしていない RPC エンドポイントからどれだけ取るかを決めます。1 回の
`round` は複数回の呼び出しです。12 秒は `network` あたり 1 日およそ 4 万回になります。自分の
ノードを読むなら下げられます。3 秒はその 4 倍です。

`width` はプロバイダーが上限を持っていて、値はそれぞれ違います。範囲を断られると、次の
`round` は半分を求めます。10 ブロックで止まります。100 回の `round` が通ると倍に戻ります。

### finality

`finality` の 2 つは、チェーンに載った送金をいつ払われたと認めるかを決めます。

`recheck` は `round` と `round` の間隔で、既定値は `poll` より長く置いてあります。1 回の
`round` は、判定の対象になっている送金 1 件につき、RPC エンドポイントへ 2 回問い合わせます。

`misses` は回数で、時間ではありません。RPC エンドポイントに問い直して見つからなかったことが
続けてこの数に達したら、その送金を消えたものとして扱います。1 にはできません。1 回見つから
ないことは、その RPC エンドポイントが入れ替え中の状態を読んでいるだけのことがあります。

### cursor

ブロックを読むことと、確定を判定することは別です。1 回の `round` が読む RPC エンドポイントは
1 本で、`cursor` は 1 つのプロバイダーが言うチェーンの位置です。

期限を過ぎた支払いがどれだけ待つかの設定はありません。`network` を期限より後まで読み終える
まで待ちます。時計ではなく、チェーンの事実です。位置が期限以後の時刻のブロックに達したとき、
その支払いを払えた送金は全て読み終えています。読むのを止めているインスタンスは、どれだけ
止まっていても何も期限切れにしません。

## assets

資産は 1 つの `network` 上の 1 つのトークンです。名前は設定ファイルのキーで、リクエストと
CLI が言う語です。

| 設定 | 既定値 | 説明 |
|---|---|---|
| `assets.<name>.network` | なし | 設定ファイルが宣言している `network` の名前 |
| `assets.<name>.reference` | なし | そのチェーンがトークンを識別する値。`evm` ではアドレス |
| `assets.<name>.symbol` | なし | 人に見せる語。資産を識別しません |
| `assets.<name>.decimals` | なし | 1 単位を最小単位へ分ける桁数。0 から 36 |
| `assets.<name>.eip712.name` | なし | トークンのコントラクトが署名に使う名前。支払者のウォレットが送金に署名するのに要ります。JPYC は `JPY Coin`。`version` と一緒に書くか、どちらも書きません |
| `assets.<name>.eip712.version` | なし | それと対の版。JPYC は `1` |

先の 4 つは必須です。`eip712` は suco Checkout を動かすときに書きます。1 つのトークンに
2 つの名前は断ります。

`reference` は、そのチェーンが比較する形に読み込みます。EVM のチェーンはアカウントを大文字
小文字どちらでも書きます。ブロックエクスプローラーが見せる大文字混じりの形を、送金が持つ
小文字の形として読みます。大文字混じりの形は checksum を持っていて、合わない `reference` は
断ります。打ち間違いを捕まえる最後の瞬間です。チェーンは、送った額を返しません。

## テストネット

本番の前の段は、Polygon Amoy で本物のウォレットを使い、JPYC は
[JPYC の faucet](https://faucet.jpyc.co.jp/) から受け取ります。suco が起動するものではなく、
設定です。

```yaml
networks:
  polygon-amoy:
    kind: evm
    chain_id: 80002
    rpc:
      own: ${SUCO_POLYGON_AMOY_RPC_URL}
assets:
  jpyc:
    network: polygon-amoy
    reference: "0xE7C3D8C9a439feDe00D2600032D5dB0Be71C3c29"
    symbol: JPYC
    decimals: 18
    eip712:
      name: JPY Coin
      version: "1"
```

`network` の名前は `polygon-amoy` で、値だけ変えた `polygon` にはしません。JPYC は Amoy でも
Polygon と同じアドレスにあります。本番の名前で書いたテストネットの設定ファイルは、それ自体では
矛盾せず、誤ったままです。`doctor` は設定ファイルの名前に関わらず、`network` の行で
テストネットを名指します。

## 名前と上限

`network` の名前と資産の名前は、報告の中の点区切りのパスと、probe が答えるキーになります。
点を含む名前は断ります。角括弧を含む名前も断ります。パスは角括弧で、一覧の中の位置を指し
ます。読み手に見えない文字を含む名前も断ります。同じに見える 2 つの名前が別物になるからです。

設定ファイルは 256 キロバイトまでです。anchor と alias は断ります。alias を展開すると指した先が
複製され、その回数に上限がありません。

## 例

```yaml
listen:
  port: 7826
  base_url: https://pay.example.com

log:
  level: info
  format: json

database:
  managed: false
  url: ${SUCO_DATABASE_URL}

credentials:
  key: ${SUCO_CREDENTIALS_KEY}
  key_id: "0123456789abcdef"

networks:
  polygon:
    kind: evm
    chain_id: 137
    rpc:
      own: ${SUCO_POLYGON_RPC_URL}

assets:
  jpyc:
    network: polygon
    reference: "0xE7C3D8C9a439feDe00D2600032D5dB0Be71C3c29"
    symbol: JPYC
    decimals: 18
    eip712:
      name: JPY Coin
      version: "1"
```

## 関連

- [operating.ja.md](operating.ja.md): `/readyz`、`suco doctor`、インスタンスを直す副命令
- [webhooks.ja.md](webhooks.ja.md): `credentials.key` を入れ替えた後に加盟店がすること
