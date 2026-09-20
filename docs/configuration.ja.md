# 設定

English: [configuration.md](configuration.md)

このページは、`suco.yaml` が取る設定の全てと、それぞれが何を決めるか、既定値が何かを
説明します。

インスタンスを動かす運用者向けです。`suco init` が設定ファイルを書き出してあり、`suco doctor` を
実行できることを前提にします。

## このページの語

| 語 | 意味 |
|---|---|
| インスタンス | `suco serve` のプロセス 1 つ |
| 設定ファイル（`suco.yaml`） | インスタンスの設定を書いたファイル。`suco init` が書き出す |
| 秘密 | 値を出力しない設定。`suco doctor` は設定されているかどうかだけを言う |
| 資格情報（`credential`） | API を呼ぶときに `Authorization` に載せるトークン。`suco credential new` が書く |
| `network` | インスタンスが読むチェーン 1 本。名前は設定ファイルのキー |
| 資産（`asset`） | 1 つの `network` 上の 1 つのトークン。名前は設定ファイルのキー |
| `round` | チェーンを読む 1 回 |
| RPC エンドポイント | インスタンスがチェーンを読むために呼ぶ URL |
| RPC プロバイダー | RPC エンドポイントを動かす側。運用者自身のノードか、第三者 |
| 送金（`transfer`） | チェーン上で資産が動いた記録。suco がチェーンから読む |
| 確定 | チェーンが送金をもう取り消さないと分かった状態 |
| `cursor` | `round` がチェーンをどこまで読んだかの位置 |
| `reference` | チェーンがトークンを識別する値。EVM のチェーンではコントラクトのアドレス |

## 設定ファイル

`suco` が読む設定ファイルは 1 つです。`suco init` が書き出し、どの副命令も `suco.yaml`、または
`SUCO_CONFIG` が指すファイルから読みます。

`suco doctor` は、解決した値とそれぞれの出所を出力します。サーバーを起動せずに設定ファイルを
確かめられます。

書かなかった設定は既定値になります。設定ファイルにあって誰も読まないキーは断ります。

## 秘密

値は `${NAME}` と書けます。`NAME` と同じ名前の環境変数を読みます。`${NAME}` は値の全体を
占めなければなりません。値の一部に書いた `https://${HOST}/rpc` は断ります。

次の設定は出力しません。`suco doctor` は、設定されているかどうかだけを言います。エラーにも
ログにも値は出ません。

- `database.url`
- `credentials.key`
- `networks.*.rpc.own`
- `networks.*.rpc.others`

秘密ではない設定の値は、そのまま出力します。ユーザー名やパスワードを持つ URL は、
秘密ではない設定に書くと断ります。鍵の形をした 16 進 64 文字の値も断ります。断るときは、
値の一部も出力しません。

## listen

| 設定 | 既定値 | 説明 |
|---|---|---|
| `listen.host` | `127.0.0.1` | 待ち受けるインターフェース。既定では他の機械から届きません |
| `listen.port` | `7826` | 1 から 65535 |
| `listen.base_url` | `http://localhost:<port>` | 他の機械がインスタンスを呼ぶときの URL。proxy の後ろでは `host` と違います。`http` か `https` で、ホスト名を持つ URL |

## log

| 設定 | 既定値 | 説明 |
|---|---|---|
| `log.level` | `info` | `debug`、`info`、`warn`、`error` |
| `log.format` | `text` | 端末で読むなら `text`、ログ収集ツールに渡すなら `json` |

## database

| 設定 | 既定値 | 説明 |
|---|---|---|
| `database.managed` | `true` | suco 自身が動かすデータベース。未実装です |
| `database.url` | なし | PostgreSQL の接続文字列。秘密 |

`managed` と `url` は排他です。`managed: false` を書き、URL を渡してください。

```yaml
database:
  managed: false
  url: ${SUCO_DATABASE_URL}
```

`managed: true` と書いた設定ファイルは、設定ファイルを読むどの副命令も断ります。`url` の無い
`managed: false` も断ります。データベースについて何も書かない設定ファイルは、`managed` が
既定値のままです。`suco serve` は `/healthz` と `/readyz` に答え、チェーンを読まず、何も
保存せず、データベースが要る副命令は全て断ります。

他の機械へ届く URL は、答えたのが誰かを確かめる接続を求めてください。`sslmode=verify-full`、
名前が一致し得ない環境では `verify-ca` です。より弱い `sslmode` は、TLS を提案して、
サーバーが断れば平文で繋ぎます。平文の通信を見られる人は、支払いの状態を読み書きできます。
loopback アドレスと unix socket は他の機械に届きません。`sslmode` を変える必要はありません。

## credentials

| 設定 | 既定値 | 説明 |
|---|---|---|
| `credentials.key` | なし | 16 進 64 文字。保存された資格情報を hash する 32 バイトです。秘密 |
| `credentials.key_id` | なし | 保存された資格情報がどの鍵で作られたか。秘密ではありません |

`database.url` を書いた時点で、`credentials.key` と `credentials.key_id` の両方が必要です。
`suco init` は鍵をファイルへ書きます。ファイルから環境変数を設定する行も出力します。

鍵を差し替えても失効はしません。古い鍵で作った資格情報は有効なままで、
提示できなくなるだけです。`suco credential list` が、どの鍵で作られたかを行ごとに
出力します。

## networks

`network` はインスタンスが読むチェーン 1 本です。名前は設定ファイルのキーです。資産は
`assets.<name>.network` で `network` の名前を指します。チェーンを読む 1 回を `round` と
呼びます。

| 設定 | 既定値 | 説明 |
|---|---|---|
| `networks.<name>.kind` | `evm` | `evm` か `simulated` |
| `networks.<name>.chain_id` | なし | `eth_chainId` が答えるべき値。1 以上。`evm` は必須、`simulated` は持てません |
| `networks.<name>.rpc.own` | なし | 運用者が自分で動かしているノード。設定すると `round` は `own` のノードを読みます。秘密 |
| `networks.<name>.rpc.others` | なし | 第三者の RPC エンドポイントの一覧。`own` が無いとき、`round` は先頭を読み、確定の判定は順に問い合わせます。秘密 |
| `networks.<name>.poll` | `12s` | `round` と `round` の間隔。`1s` 以上 |
| `networks.<name>.width` | `1000` | log を 1 回問い合わせる範囲の上限ブロック数。10 から 10000 |
| `networks.<name>.finality.recheck` | `1m` | 記録した送金を RPC エンドポイントに問い直す間隔。`1s` 以上 |
| `networks.<name>.finality.misses` | `10` | RPC エンドポイントが続けて見つけられなかった回数が `misses` に達すると、送金は消えたとして扱います。`2` 以上 |

`simulated` はプロセスの中で動くチェーンです。インスタンスの残りを試すために使います。
ブロックを 1 つ作り、ブロックには何も届きません。

資産が指さない `network` は断ります。

### rpc

RPC エンドポイントは、`https` ならどこでも、`http` なら `localhost` か loopback アドレスに
限ります。`http` では URL の鍵が平文で流れ、返ってくる receipt も平文です。ホストは書かれた
まま見ます。loopback へ解決する名前も断ります。

`own` を書いた `network` は、`own` のノードにだけ問い合わせます。`own` が無い `network` は
`others` に順に問い合わせ、2 本の答えが一致したときに払われたと認めます。答えない 1 本は
飛ばします。並べるほど、揃えるべき答えが増えるのではなく、代わりが増えます。答えるのが
2 本を切れば、別の 1 本が答えるまで `network` は `too-few` です。`too-few` の意味は
[operating.ja.md](operating.ja.md) にあります。違うことを言う 1 本があれば、支払いは
動きません。3 本目の RPC エンドポイントには裁定させません。一致しても食い違っても、送金は
記録されたままです。額は加盟店のアドレスにあります。

### poll と width

`poll` は、自分で動かしていない RPC エンドポイントからどれだけ取るかを決めます。1 回の
`round` は複数回の呼び出しです。12 秒は `network` あたり 1 日およそ 4 万回になります。自分の
ノードを読むなら `poll` を下げられます。3 秒にすると、呼び出しの数は 4 倍になります。

`width` の上限は RPC プロバイダーが決めます。上限の値は RPC プロバイダーごとに違います。
範囲を断られると、次の `round` は半分を求めます。10 ブロックで止まります。100 回の `round` が
通ると倍に戻ります。

### finality

`finality.recheck` と `finality.misses` は、チェーンに載った送金をいつ払われたと認めるかを
決めます。

`recheck` は `round` と `round` の間隔です。既定値は `poll` の既定値より長くしてあります。
1 回の `round` は、判定の対象になっている送金 1 件につき、RPC エンドポイントへ 2 回
問い合わせます。

`misses` は回数で、時間ではありません。RPC エンドポイントに問い直して見つからなかったことが
続けて `misses` に達したら、送金は消えたとして扱います。1 にはできません。1 回見つからない
だけなら、入れ替えの途中の状態を RPC エンドポイントが読んでいることがあります。

### cursor

ブロックを読むことと、確定を判定することは別です。1 回の `round` が読む RPC エンドポイントは
1 本です。`cursor` は、1 つの RPC プロバイダーが言うチェーンの位置です。

期限を過ぎた支払いがどれだけ待つかの設定はありません。支払いは、`network` を期限より後まで
読み終えるまで待ちます。期限切れを決めるのは、時計ではなく、チェーンの事実です。読んだ位置が
期限以後の時刻のブロックに達したとき、支払いを払えた送金は全て読み終えています。チェーンを
読むのを止めているインスタンスは、どれだけ止まっていても何も期限切れにしません。

## assets

資産は 1 つの `network` 上の 1 つのトークンです。名前は設定ファイルのキーで、リクエストと
CLI が言う語です。

| 設定 | 既定値 | 説明 |
|---|---|---|
| `assets.<name>.network` | なし | 設定ファイルが宣言している `network` の名前 |
| `assets.<name>.reference` | なし | `network` のチェーンがトークンを識別する値。`evm` ではアドレス |
| `assets.<name>.symbol` | なし | 人に見せる語。資産を識別しません |
| `assets.<name>.decimals` | なし | 1 単位を最小単位へ分ける桁数。0 から 36 |
| `assets.<name>.eip712.name` | なし | トークンのコントラクトが署名に使う名前。支払者のウォレットが送金に署名するのに要ります。JPYC は `JPY Coin`。`version` と一緒に書くか、どちらも書きません |
| `assets.<name>.eip712.version` | なし | `name` と対になる版。JPYC は `1` |

`network`、`reference`、`symbol`、`decimals` は必須です。`eip712` は suco Checkout を動かす
ときに書きます。1 つのトークンに 2 つの名前は断ります。

`reference` は、`network` のチェーンが比較する形に読み込みます。EVM のチェーンはアカウントを
大文字小文字どちらでも書きます。ブロックエクスプローラーが見せる大文字混じりの形を、送金が
持つ小文字の形として読みます。大文字混じりの形は checksum を持っていて、checksum の合わない
`reference` は断ります。打ち間違いを捕まえる最後の瞬間です。チェーンは、送った額を返しません。

## テストネット

本番の前の段は、Polygon Amoy で本物のウォレットを使い、JPYC は
[JPYC の faucet](https://faucet.jpyc.co.jp/) から受け取ります。テストネットは suco が起動する
チェーンではなく、設定ファイルに書く `network` です。

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
Polygon と同じアドレスにあります。本番の名前で書いたテストネットの設定ファイルは、中だけを
見れば矛盾せず、誤ったままです。`doctor` は設定ファイルの名前に関わらず、`network` の行で
テストネットを名指します。

## 名前と上限

`network` の名前と資産の名前は、報告の中の点区切りのパスと、probe が答えるキーになります。
点を含む名前は断ります。角括弧を含む名前も断ります。パスは角括弧で、一覧の中の位置を
指します。読み手に見えない文字を含む名前も断ります。同じに見える 2 つの名前が、
別の名前になるからです。

設定ファイルは 256 キロバイトまでです。anchor と alias は断ります。alias を展開すると、
alias が名指す値を複製します。複製の回数に上限がありません。

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
