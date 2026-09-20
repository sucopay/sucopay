# 設定

English: [configuration.md](configuration.md)

`suco.yaml` の全設定、既定値、設定変更による動作を説明します。suco Pay のインスタンスを
構成する運用者向けのリファレンスです。

## 設定の流れ

1. `suco init` で `suco.yaml` と資格情報の鍵ファイルを作成します。
2. `database.managed: false` を設定し、`database.url` を環境変数で渡します。
3. ネットワークと資産を追加します。メインネットへ接続する前に
   [テストネットの例](#テストネット) を使ってください。
4. `suco doctor` を実行し、報告されたエラーを解消します。
5. `suco serve` でインスタンスを起動します。

設定全体をコピーして始める場合は、[設定例](#例) を参照してください。

## 設定ファイルの読み込み

すべての `suco` コマンドは、1 つの設定ファイルを読みます。既定のパスは `suco.yaml` です。
別のパスを使う場合は `SUCO_CONFIG` を設定してください。

`suco doctor` を使うと、サーバーを起動せずに設定ファイルを検証できます。解決後の各設定と
値の取得元を出力します。

省略した設定には既定値を使います。タイプミスを見逃さないよう、未知のキーは拒否します。

## 秘密

環境変数を参照する値は `${NAME}` と書きます。参照は値全体でなければなりません。
`https://${HOST}/rpc` のような文字列への埋め込みは拒否します。

次の設定は機密情報です。`suco doctor` は設定の有無だけを報告し、値は出力しません。エラーや
ログにも値を含めません。

- `database.url`
- `credentials.key`
- `networks.*.rpc.own`
- `networks.*.rpc.others`

機密情報以外の値はそのまま出力します。suco Pay は、機密設定ではない URL に含まれる
ユーザー名やパスワードと、機密設定ではない項目にある 16 進 64 文字の値を拒否します。
エラーにも値の一部を含めません。

## listen

| 設定 | 既定値 | 説明 |
|---|---|---|
| `listen.host` | `127.0.0.1` | 待ち受けるインターフェース。既定値ではローカルマシンからの接続だけを受け付ける |
| `listen.port` | `7826` | 1 から 65535 |
| `listen.base_url` | `http://localhost:<port>` | インスタンスの公開 URL。`http` または `https` を使う。プロキシーの背後では `listen.host` と異なる |

## log

| 設定 | 既定値 | 説明 |
|---|---|---|
| `log.level` | `info` | `debug`、`info`、`warn`、`error` |
| `log.format` | `text` | 端末で読むなら `text`、ログ収集ツールに渡すなら `json` |

## database

| 設定 | 既定値 | 説明 |
|---|---|---|
| `database.managed` | `true` | suco Pay がデータベースを管理するか。現在は未実装 |
| `database.url` | なし | PostgreSQL の接続文字列。機密情報 |

`managed` と `url` は排他です。`managed: false` を書き、URL を渡してください。

```yaml
database:
  managed: false
  url: ${SUCO_DATABASE_URL}
```

マネージドデータベースは未実装です。`managed: true` を設定した場合と、`url` なしで
`managed: false` を設定した場合は、設定の検証に失敗します。

`database` セクションを省略すると、`suco serve` は `/healthz` と `/readyz` だけを起動します。
チェーンの読み取りとデータの保存は行わず、データベースが必要なコマンドは失敗します。

別のマシンにあるデータベースには `sslmode=verify-full` を使ってください。証明書の名前と
接続先のホスト名を一致させられない場合に限り、`verify-ca` を使います。これより弱い設定では
平文通信へ切り替わり、ネットワーク上の第三者に支払いデータを読み書きされるおそれがあります。
ループバックアドレスと Unix ソケットでは、`sslmode` を変更する必要はありません。

## credentials

| 設定 | 既定値 | 説明 |
|---|---|---|
| `credentials.key` | なし | 16 進 64 文字で表した 32 バイトの機密鍵。保存する資格情報のハッシュに使う |
| `credentials.key_id` | なし | 保存する資格情報に使った鍵の識別子。機密情報ではない |

`database.url` を設定する場合は、両方の設定が必要です。`suco init` は鍵をファイルに保存し、
環境変数へ読み込むコマンドを出力します。

鍵を差し替えても、既存の資格情報は失効しません。ただし、インスタンスは古い鍵で作った
資格情報を検証できなくなります。`suco credential list` で、資格情報ごとの鍵 ID を確認できます。

## networks

`network` はインスタンスが読むチェーン 1 本です。名前は設定ファイルのキーです。資産は
`assets.<name>.network` で `network` の名前を指します。チェーンを読む 1 回を `round` と
呼びます。

| 設定 | 既定値 | 説明 |
|---|---|---|
| `networks.<name>.kind` | `evm` | `evm` か `simulated` |
| `networks.<name>.chain_id` | なし | `eth_chainId` の期待値。1 以上。`evm` では必須、`simulated` では指定不可 |
| `networks.<name>.rpc.own` | なし | 自社運用ノードの RPC エンドポイント。設定した場合、チェーンの読み取りに使う。機密情報 |
| `networks.<name>.rpc.others` | なし | 第三者の RPC エンドポイント。`own` がない場合の読み取りと確定判定に使う。機密情報 |
| `networks.<name>.poll` | `12s` | チェーンを読み取る `round` の間隔。`1s` 以上 |
| `networks.<name>.width` | `1000` | 1 回のログ取得で指定する最大ブロック範囲。10〜10000 |
| `networks.<name>.finality.recheck` | `1m` | 記録済みの送金を確定判定する間隔。`1s` 以上 |
| `networks.<name>.finality.misses` | `10` | 送金を消失とみなすまで、連続して検出できなかった回数。`2` 以上 |

`simulated` は、インスタンスのほかの機能をテストするためのプロセス内チェーンです。空の
ブロックを 1 つ作成します。

各 `network` は、少なくとも 1 つの資産から参照される必要があります。

### rpc

RPC エンドポイントには HTTPS を使ってください。HTTP は `localhost` またはループバックアドレスに
限り許可します。HTTP では URL の資格情報と RPC レスポンスが平文で送信されます。ループバックへ
名前解決されるホスト名は拒否するため、アドレスを直接指定してください。

`rpc.own` を設定すると、その RPC エンドポイントだけを使います。設定しない場合は
`rpc.others` を順に呼び出し、2 つのレスポンスが一致した時点で送金を確定とみなします。応答しない
RPC エンドポイントは飛ばすため、追加するほど冗長性が上がり、一致に必要な数は増えません。

応答が 2 つ未満の場合、準備状態は `too-few` になります。2 つのレスポンスが食い違う場合、
支払いの状態は進まず、3 つ目のレスポンスで多数決は行いません。どちらの場合も送金の記録は
残ります。

### poll と width

`poll` は RPC リクエストの頻度を決めます。1 回の `round` は複数のリクエストを行うため、
既定の 12 秒では `network` ごとに 1 日約 4 万回になります。3 秒にすると 4 倍です。
RPC プロバイダーが処理できる場合だけ短くしてください。

RPC プロバイダーごとに、取得できるブロック範囲の上限が異なります。現在の `width` が
拒否されると、次の `round` では範囲を半分にし、最小 10 ブロックまで縮めます。その後
100 回の `round` が成功すると、設定した上限まで 2 倍ずつ戻します。

### finality

`finality` の設定は、検出した送金をいつ確定とみなすかを決めます。

`recheck` は確定判定を行う `round` の間隔です。1 回の `round` では、判定中の送金 1 件につき
RPC エンドポイントを 2 回呼び出します。

`misses` は時間ではなく回数です。連続して検出できなかった回数がこの値に達すると、送金が
消失したとみなします。RPC プロバイダーの状態更新中に 1 回だけ検出できない場合があるため、
最小値は 2 です。

### cursor

ブロックの取り込みと確定判定は別の処理です。取り込みの `round` は 1 つの
RPC エンドポイントを読みます。`cursor` は、その RPC プロバイダーが返したブロック位置です。

支払いの期限切れは、時計ではなくチェーンの読み取り位置で判断します。`cursor` が期限以後の
タイムスタンプを持つブロックに達するまで待つことで、期限内に成立し得た送金をすべて確認します。
チェーンの読み取りが止まっている間、その `network` の支払いは期限切れになりません。

## assets

資産は、1 つの `network` 上にある 1 つのトークンです。`suco.yaml` のキーが、API リクエストと
CLI コマンドで使う資産名になります。

| 設定 | 既定値 | 説明 |
|---|---|---|
| `assets.<name>.network` | なし | 設定ファイルが宣言している `network` の名前 |
| `assets.<name>.reference` | なし | チェーン固有のトークン識別子。`evm` ではコントラクトアドレス |
| `assets.<name>.symbol` | なし | 表示用のシンボル。資産の一意な識別子にはならない |
| `assets.<name>.decimals` | なし | 表示単位と最小単位の間の小数桁数。0 から 36 |
| `assets.<name>.eip712.name` | なし | コントラクトが使う EIP-712 ドメインのトークン名。JPYC は `JPY Coin`。`version` と同時に指定するか、両方とも省略する |
| `assets.<name>.eip712.version` | なし | EIP-712 ドメインのバージョン。JPYC は `1` |

最初の 4 項目は必須です。Checkout を使う場合は、2 つの `eip712` 項目も必要です。同じ
トークンを 2 つの名前で設定することはできません。

suco Pay は、`reference` をチェーンが使う形式へ正規化します。EVM アドレスは小文字で保存します。
大文字と小文字が混在するアドレスを指定した場合は、チェックサムが一致する必要があります。この検証で、
送金前に誤ったコントラクトアドレスを検出できます。

## テストネット

メインネットへ接続する前に、Polygon Amoy のウォレットと
[JPYC の Faucet](https://faucet.jpyc.co.jp/) を使ってテストしてください。設定例は次のとおりです。

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

テストネットと本番の名前を分けてください。JPYC は Amoy と Polygon で同じコントラクトアドレスを
使うため、どちらも `polygon` と名付けると設定ミスを見つけにくくなります。`suco doctor` は、
ローカルな名前にかかわらず、既知のテストネットを表示します。

## 名前と上限

`network` 名と資産名は、点区切りの設定パスと準備状態レスポンスのキーに使います。点、
角括弧、不可視文字は使用できません。

設定ファイルは 256 KiB までです。YAML のアンカーとエイリアスは利用できません。

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

## 次のステップ

1. `suco doctor` で設定を検証します。
2. [`suco asset accept`](operating.ja.md#suco-asset-accept) で受取ウォレットを登録します。
3. [運用](operating.ja.md) に従い、ヘルスチェックと障害対応を準備します。
