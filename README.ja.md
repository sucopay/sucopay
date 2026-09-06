<h1 align="center">suco Pay</h1>

<p align="center">
  <b>ステーブルコイン決済のためのオープンソース決済基盤</b>
</p>

<p align="center">
  <a href="https://github.com/sucopay/sucopay/discussions">Discussions</a> ·
  <a href="README.md">English</a>
</p>

<p align="center">
  <a href="https://github.com/sucopay/sucopay/actions/workflows/ci.yml"><img src="https://github.com/sucopay/sucopay/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/status-pre--alpha-orange" alt="Status: pre-alpha">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License: Apache-2.0"></a>
</p>

---

Payment を作成し、オンチェーンで受け取り、確定を判定し、返金し、Webhook を受け取ります。
suco Pay は自社のインフラで動作します。資金は顧客のウォレットから加盟店のウォレットへ直接
移動します。

> **開発初期です。** 現時点であるのは CLI だけです。
> [Discussions](https://github.com/sucopay/sucopay/discussions) で進捗を追えます。

- [ ] Payment: ライフサイクル、期限、過払いと不足、冪等性
- [ ] Finality: ネットワークごとに設定する確定判定
- [ ] 支払い画面: ホスト型と埋め込み型
- [ ] Webhook: 署名、リトライ、送信履歴
- [ ] Refund: 記録と送金指示の作成
- [ ] 照合: 内部状態とチェーンの定期突合
- [ ] Console: Payment と Transaction の照会、返金、Webhook 送信履歴
- [x] CLI: `init` `serve` `doctor` `credential` `asset`

## インストール

```bash
git clone https://github.com/sucopay/sucopay && cd sucopay
go build -o suco ./cmd/suco
./suco init   # 下の export の行を、書き出した鍵ファイルの名前入りで印字します
export SUCO_CREDENTIALS_KEY="$(cat -- 'credentials-<key_id>.key')"
./suco doctor
./suco serve
```

`suco init` が `suco.yaml` と鍵のファイルを書き出します。`suco.yaml` は読んでコミットできる
設定で、鍵のファイルは所有者だけが読めます。資格情報は `suco` が鍵を使って保存します。`suco.yaml` には
鍵を読む環境変数の名前だけが入り、鍵の値は入りません。その環境変数は `suco` を動かすシェルで
設定してください。`suco doctor` は解決後の設定値と、それぞれの出所を表示します。秘密のキーについては、値ではなく設定されて
いるかどうかだけが出ます。`suco serve` は `http://localhost:7826` で待ち受けます。`/healthz` はプロセスが動いていることを、
`/readyz` は必要なものに届いているかと、有効な資格情報に書き込みできるものがあるかを答えます。
走っている間に起きたことは stdout に書きます。
データベースを設定した配備では、`suco credential new --read-only` か `--read-write` が API の
資格情報を 1 本作り、トークンを所有者だけが読めるファイルへ書きます。`suco credential list` は
有効な資格情報を最終使用の新しい順に表示し、`suco credential revoke <id>` は 1 本を失効させます。

Payment を作るには、それが届くネットワークと、その資産を `suco.yaml` に載せてください。

```yaml
networks:
  local:
    kind: simulated
assets:
  jpyc:
    network: local
    reference: "0x0000000000000000000000000000000000000001"
    symbol: JPYC
    decimals: 18
```

ネットワークの `kind` は今のところ `simulated` だけを受け付けます。チェーンを見るものはまだ無いので、
Payment は `created` のままです。`suco asset accept <name> <address>` は、`suco.yaml` にその名前で
載せた資産の支払いを受け取るアドレスを記録します。`suco asset list` は `suco.yaml` に載せた資産を
すべて表示し、受け付けているものにはアドレスを、まだのものには `not accepted` を添えます。加盟店の
サーバは [docs/api.ja.md](docs/api.ja.md) の API で Payment を作り、読みます。

インストールスクリプトとビルド済みバイナリは最初のリリースで用意します。上の一覧にある機能は
まだありません。

## コントリビュート

[CONTRIBUTING.ja.md](CONTRIBUTING.ja.md) を参照してください。
脆弱性の報告は [SECURITY.ja.md](SECURITY.ja.md) の手順に従ってください。

## ライセンス

[Apache-2.0](LICENSE)
