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
- [x] CLI: `init` `serve` `doctor` `credential`

## インストール

```bash
git clone https://github.com/sucopay/sucopay && cd sucopay
go build -o suco ./cmd/suco
./suco init
./suco doctor
./suco serve
```

`suco init` が `suco.yaml` を書き出します。読んでコミットできる設定です。`suco doctor` は
解決後の設定値と、それぞれの出所を表示します。秘密のキーについては、値ではなく設定されて
いるかどうかだけが出ます。`suco serve` は `http://localhost:7826` で待ち受けます。`/healthz` はプロセスが動いていることを、
`/readyz` は必要なものに届いているかを答えます。走っている間に起きたことは stdout に書きます。
データベースを設定した配備では、`suco credential new --read-only` か `--read-write` が API の
資格情報を 1 本作り、トークンを所有者だけが読めるファイルへ書きます。`suco credential list` は
有効な資格情報を最終使用の新しい順に表示し、`suco credential revoke <id>` は 1 本を失効させます。

インストールスクリプトとビルド済みバイナリは最初のリリースで用意します。上の一覧にある機能は
まだありません。

## コントリビュート

[CONTRIBUTING.ja.md](CONTRIBUTING.ja.md) を参照してください。
脆弱性の報告は [SECURITY.ja.md](SECURITY.ja.md) の手順に従ってください。

## ライセンス

[Apache-2.0](LICENSE)
