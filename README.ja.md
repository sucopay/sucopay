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

> **開発初期です。** まだ動作しません。進捗は
> [Discussions](https://github.com/sucopay/sucopay/discussions) でお知らせします。

- [ ] Payment: ライフサイクル、期限、過払いと不足、冪等性
- [ ] Finality: ネットワークごとに設定する確定判定
- [ ] Checkout: ホスト型と埋め込み型
- [ ] Webhook: 署名、リトライ、配送履歴
- [ ] Refund: 記録と送金指示の作成
- [ ] Reconciliation: 内部状態とチェーンの定期照合
- [ ] Console: Payment と Transaction の照会、返金、Webhook 配送履歴
- [ ] CLI: `init` `dev` `listen` `migrate` `doctor` `upgrade`

## インストール

```bash
curl -fsSL https://get.sucopay.dev | sh
suco init
cd suco && suco dev
```

Console は `http://localhost:8080` で起動します。ここで最初の Payment を作成します。

```bash
curl -X POST http://localhost:8080/v1/payments \
  -H 'Authorization: Bearer sk_test_...' \
  -d '{"amount":"1000","asset":"JPYC","network":"polygon"}'

suco listen --forward http://localhost:3000/webhooks
```

## コントリビュート

[CONTRIBUTING.md](CONTRIBUTING.md) を参照してください。
脆弱性の報告は [SECURITY.md](SECURITY.md) の手順に従ってください。

## ライセンス

[Apache-2.0](LICENSE)
