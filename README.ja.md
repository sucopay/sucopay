<h1 align="center">suco Pay</h1>

<p align="center">
  <b>ステーブルコイン決済のオープンソース基盤</b>
</p>

<p align="center">
  <a href="https://github.com/sucopay/sucopay/discussions">Discussions</a> ·
  <a href="ROADMAP.ja.md">ロードマップ</a> ·
  <a href="README.md">English</a>
</p>

<p align="center">
  <a href="https://github.com/sucopay/sucopay/actions/workflows/ci.yml"><img src="https://github.com/sucopay/sucopay/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/status-pre--alpha-orange" alt="Status: pre-alpha">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License: Apache-2.0"></a>
</p>

---

支払いを作り、オンチェーンで受け取り、確定を知り、返金し、Webhook を受け取ります。

suco Pay は、自社のインフラで動かすソフトウェアです。鍵を預かりません。手数料を取りません。
支払いは顧客のウォレットから加盟店のウォレットへ、間に何も挟まずに移動します。suco Pay は
チェーンを読み、支払いが届いたことを知らせます。

> **開発初期です。** 今できることは、下の「いまの状態」にあります。

## はじめかた

Go 1.26 以上と、接続先の PostgreSQL が必要です。

```bash
git clone https://github.com/sucopay/sucopay && cd sucopay
go build -o suco ./cmd/suco
./suco init
```

`suco init` は、読んでコミットできる `suco.yaml` と、所有者だけが読める鍵のファイルを
書き出します。資格情報は、`suco init` が書き出した鍵で保存します。設定ファイルには鍵の値ではなく、
鍵を読む環境変数の名前が入ります。

データベースを `suco.yaml` に足します。

```yaml
database:
  managed: false
  url: ${SUCO_DATABASE_URL}
```

設定を確かめてから起動します。

```bash
export SUCO_DATABASE_URL="postgres://suco:secret@localhost:5432/suco"
export SUCO_CREDENTIALS_KEY="$(cat -- 'credentials-<key_id>.key')"   # init がこの行を出力します
./suco doctor
./suco serve
```

- `suco doctor` は、解決した設定と、それぞれの値の出所と、インスタンス（`suco serve` の
  プロセス 1 つ）が何に届くかを出力します。秘密の設定については、設定されているかどうかだけを
  出力します。
- `suco serve` は `http://localhost:7826` で待ち受け、していることを stdout に出力します。
  `/healthz` はプロセスが動いていることを答えます。`/readyz` は仕事ができるかを答えます。

## 支払いの受け取り

支払いが届く `network` と、その資産を書きます。

```yaml
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

`suco serve` は `suco.yaml` を起動時に 1 度だけ読みます。
`networks` と `assets` を足したら再起動してください。次に、API の資格情報と、
支払いを受け取るウォレットを登録し、支払いを試します。

```bash
./suco credential new --read-write          # API の資格情報のファイルを書き出します
./suco asset accept jpyc 0xYourWalletHere   # jpyc の受取アドレス
./suco payment await <id>                   # suco Checkout で払えるようになるまで、支払者が署名する値を出力します
```

`asset accept` には、EIP-712 の typed data に署名できるウォレットを渡してください。返金は、
支払いを受け取ったウォレットが署名します。誰も署名できないアドレスは、支払いを受け取れます。
返金はできません。登録のときに、資産のコントラクトに受取アドレスへの送金を拒むかを
問い合わせます。コントラクトが拒むアドレスは登録しません。残りは
[docs/operating.ja.md](docs/operating.ja.md) にあります。

加盟店のサーバーは API で支払いを作り、読み戻します。`suco serve` はチェーンを `round`
（チェーンを読む 1 回）ごとに読み、支払いに払われた送金を記録します。

## いまの状態

送金が見つかり、突き合わされ、記録され、確定し、支払いが `succeeded` に届きます。支払いが
`succeeded` に届くまでの流れは、プロセスの中のチェーンに対しては端から端まで、Polygon
に対しては実際の支払い 1 件で 1 段ずつ確かめました。今できることとできないことは
[ROADMAP.ja.md](ROADMAP.ja.md) にあります。

## ドキュメント

| | |
|---|---|
| [開発者向けドキュメント](docs/README.ja.md) | 推奨する組み込み手順の入口 |
| [設定](docs/configuration.ja.md) | `suco.yaml` の全設定 |
| [API](docs/api.ja.md) | 支払いの作成と取得 |
| [Checkout](docs/checkout.ja.md) | 支払者を支払いページへ案内する方法 |
| [Webhook](docs/webhooks.ja.md) | 支払いと返金のイベント受信 |
| [返金](docs/refunds.ja.md) | 返金の作成、署名、追跡 |
| [運用](docs/operating.ja.md) | 稼働準備状態の監視とインスタンスの復旧 |
| [ROADMAP.ja.md](ROADMAP.ja.md) | 今できることとできないこと |

## コントリビュート

[CONTRIBUTING.ja.md](CONTRIBUTING.ja.md) を読んでください。セキュリティの問題は
[SECURITY.ja.md](SECURITY.ja.md) の手順で報告してください。

## ライセンス

[Apache-2.0](LICENSE)
