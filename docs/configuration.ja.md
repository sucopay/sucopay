# 設定

English: [configuration.md](configuration.md)

`suco` が読む文書は 1 つです。`suco init` が書き出し、どの副命令も `suco.yaml`、または
`SUCO_CONFIG` が指すファイルから読みます。`suco doctor` が解決した値とそれぞれの出所を印字
するので、サーバを起動せずに文書を確かめられます。

書かなかった設定は既定値になります。文書にあって誰も読まない鍵は断ります。綴りを間違えた鍵が
既定値のまま黙って残ることを避けるためです。

## 秘密

値は `${NAME}` と書けます。その名前の環境変数を読みます。参照は値の全体か、値でないかの
どちらかです。`https://${HOST}/rpc` は断ります。出所が 2 つある値には出所がありません。

3 つの設定は印字しません。`suco doctor` は設定されているかどうかだけを言い、エラーにもログにも
値は出ません。

- `database.url`
- `credentials.key`
- `networks.*.rpc`

これ以外の値はそのまま印字します。だからこそ、秘密でない設定に資格情報が入っていれば断ります。
ユーザ名やパスワードを持つ URL は、印字せずに断ります。

## listen

| 鍵 | 既定 | |
|---|---|---|
| `listen.host` | `127.0.0.1` | 待ち受けるインタフェース。他の機械から届くようにするかは配備の判断なので、既定では開けません |
| `listen.port` | `7826` | 1 から 65535 |
| `listen.base_url` | `http://localhost:<port>` | 他からこのインスタンスへ届く先。proxy の後ろでは `host` と違います。`http` か `https` で、ホスト名を持つ URL |

## log

| 鍵 | 既定 | |
|---|---|---|
| `log.level` | `info` | `debug`、`info`、`warn`、`error` |
| `log.format` | `text` | 端末で読むなら `text`、収集するものに渡すなら `json` |

## database

| 鍵 | 既定 | |
|---|---|---|
| `database.managed` | `true` | suco 自身が動かすデータベース。未実装です。`false` にして URL を書いてください |
| `database.url` | なし | PostgreSQL の接続文字列。秘密 |

2 つは排他です。他の機械へ届く URL は、応答したのが誰かを確かめる接続を求める必要があります。
`sslmode=verify-full`、名前が一致し得ない環境では `verify-ca` です。これより弱い設定は、TLS を
提案して server が断れば平文で繋ぎます。経路にいる者が payment の状態を読み書きできます。
loopback アドレスと unix socket は経路に出ないので、そのままにします。

## credentials

| 鍵 | 既定 | |
|---|---|---|
| `credentials.key` | なし | 16 進 64 文字。保存された資格情報を hash する 32 バイトです。秘密 |
| `credentials.key_id` | なし | 保存された資格情報がどの鍵で作られたか。秘密ではありません |

`database.url` を書いた時点で両方が必要になります。そこが、資格情報を保存して読み戻せるように
なる時点です。`suco init` が鍵をファイルへ書き、そこから環境変数を設定する行を印字します。

鍵を差し替えても失効はしません。古い鍵で作った資格情報は有効なままで、提示できなくなるだけです。
`suco credential list` が、どの鍵で作られたかを行ごとに出します。

## networks

network はインスタンスが読むチェーン 1 本です。名前は文書の鍵で、asset が指す先です。

| 鍵 | 既定 | |
|---|---|---|
| `networks.<name>.kind` | `evm` | `evm` か `simulated` |
| `networks.<name>.chain_id` | なし | `eth_chainId` が答えるべき値。1 以上。`evm` は必須、`simulated` は持てません |
| `networks.<name>.rpc` | なし | チェーンに届く先。`evm` は必須、`simulated` は持てません。秘密 |
| `networks.<name>.poll` | `12s` | 周と周の間隔。`1s` 以上 |
| `networks.<name>.width` | `1000` | log を 1 回問い合わせる範囲の上限ブロック数。10 から 10000 |

`simulated` はプロセスの中で動くチェーンです。配備の残りを試すためのもので、ブロックを 1 つ
作り、そこに何も届きません。

エンドポイントは `https` ならどこでも、`http` なら `localhost` か loopback アドレスに限ります。
`http` では URL の鍵が平文で流れ、返ってくる receipt も平文です。ホストは書かれたまま見るので、
loopback へ解決する名前も断ります。

`poll` は、自分で動かしていないエンドポイントからどれだけ取るかを決めます。1 周が複数回の
呼び出しなので、12 秒は network あたり 1 日およそ 4 万回です。自分のノードを読む配備は下げられ
ます。3 秒はその 4 倍です。

`width` はプロバイダが上限を持っていて、値はそれぞれ違います。範囲を断られると次の周が半分を
求め、10 ブロックで止まります。100 周通ると倍に戻ります。

asset が指さない network は断ります。読むものがありません。

## assets

asset は 1 つの network 上の 1 つのトークンです。名前は文書の鍵で、要求と CLI が言う語です。

| 鍵 | |
|---|---|
| `assets.<name>.network` | 文書が宣言している network の名前 |
| `assets.<name>.reference` | そのチェーンがトークンを識別する値。`evm` ではアドレス |
| `assets.<name>.symbol` | 人に見せる語。asset を識別しません |
| `assets.<name>.decimals` | 1 単位を最小単位へ分ける桁数。0 から 36 |

4 つとも必須です。1 つのトークンに 2 つの名前は断ります。チェーンで見つかる送金はトークンの
ものなので、どちらの名前で記録するかが決まりません。

reference は、そのチェーンが比較する形に読み込みます。EVM のチェーンはアカウントを大文字小文字
どちらでも書くので、ブロックエクスプローラが見せる大文字混じりの形を、送金が持つ小文字の形と
して読みます。大文字混じりの形は checksum を持っていて、合わない reference は断ります。打ち
間違いを捕まえる最後の瞬間です。チェーンは資金を返しません。

## 名前

network の名前と asset の名前は、報告の中の点区切りの経路と、probe が答える鍵になります。点を
含む名前は断ります。読み手に見えない文字を含む名前も断ります。同じに見える 2 つの名前が別物に
なるからです。

## 文書の上限

256 キロバイトまでです。anchor と alias は断ります。alias を展開すると指した先が複製され、
その回数に上限がありません。数百バイトの文書が、複製の複製をメモリが尽きるまで名乗れます。

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
    rpc: ${SUCO_POLYGON_RPC_URL}

assets:
  jpyc:
    network: polygon
    reference: "0xE7C3D8C9a439feDe00D2600032D5dB0Be71C3c29"
    symbol: JPYC
    decimals: 18
```
