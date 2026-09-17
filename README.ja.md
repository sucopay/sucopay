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

Payment を作成し、オンチェーンで受け取り、確定を判定し、返金し、Webhook を受け取ります。

suco Pay は、自社のインフラで動かすソフトウェアです。鍵を預かりません。手数料を取りません。
資金は顧客のウォレットから加盟店のウォレットへ、間に何も挟まずに移動します。suco Pay がするのは、
チェーンを見て、届いたと伝えることです。

> **開発初期です。** 送金は見つかり、突き合わされ、記録され、payment が `succeeded` に届く
> ところまで通ります。その道を通したのは、プロセスの中のチェーンに対しては端から端まで、
> Polygon に対しては実際にあった支払い 1 件で 1 段ずつです。今できることとできないことは
> [ROADMAP.ja.md](ROADMAP.ja.md) にあります。

## はじめかた

Go 1.26 以上と、向ける先の PostgreSQL が要ります。

```bash
git clone https://github.com/sucopay/sucopay && cd sucopay
go build -o suco ./cmd/suco
./suco init
export SUCO_CREDENTIALS_KEY="$(cat -- 'credentials-<key_id>.key')"   # init がこの行を印字します
./suco doctor
./suco serve
```

- `suco init` が、読んでコミットできる `suco.yaml` と、所有者だけが読める鍵のファイルを書き出し
  ます。資格情報はその鍵で保存し、文書には鍵の値ではなく、鍵を読む環境変数の名前が入ります。
- `suco doctor` が、解決した設定と、それぞれの値の出所と、インスタンスが何に届くかを印字します。
  秘密の設定については、設定されているかどうかだけを言います。
- `suco serve` が `http://localhost:7826` で待ち受け、していることを stdout に書きます。
  `/healthz` はプロセスが動いていることを、`/readyz` は仕事ができるかを答えます。

## Payment の受け取り

payment が届く network と、その資産を書きます。

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

データベースを設定したうえで、次を実行します。

```bash
./suco credential new --read-write          # API の資格情報のファイルを書き出します
./suco asset accept jpyc 0xYourWalletHere   # jpyc の payment を払う先
./suco payment await <id>                   # suco Checkout ができるまでの、署名する値の印字
```

加盟店のサーバは API で payment を作り、読み戻します。`suco serve` はチェーンを周ごとに読み、
それに応える送金を記録します。

## ドキュメント

| | |
|---|---|
| [docs/api.ja.md](docs/api.ja.md) | 加盟店のサーバが呼ぶ HTTP API |
| [docs/webhooks.ja.md](docs/webhooks.ja.md) | suco が加盟店のサーバに送るものと、その受け取り方 |
| [docs/checkout.ja.md](docs/checkout.ja.md) | 支払者が払うページと、支払者をどこへ送るか |
| [docs/configuration.ja.md](docs/configuration.ja.md) | `suco.yaml` が取る設定の全て |
| [docs/operating.ja.md](docs/operating.ja.md) | `/readyz`、`suco doctor`、止まったインスタンスの直し方 |
| [ROADMAP.ja.md](ROADMAP.ja.md) | 今できることとできないこと |

## コントリビュート

[CONTRIBUTING.ja.md](CONTRIBUTING.ja.md) を読んでください。セキュリティの問題は
[SECURITY.ja.md](SECURITY.ja.md) の手順で報告してください。

## ライセンス

[Apache-2.0](LICENSE)
