# コントリビュート

English: [CONTRIBUTING.md](CONTRIBUTING.md)

## 開発環境

```bash
make dev
make test
make lint
```

`make dev` は開発用スタックを起動します。Go 1.26 以降と Docker が必要です。web パッケージを
ビルドする場合は、Active LTS である Node.js 24 も必要です。

## コード、テスト、コミット、コメント

それぞれ別の問いに答えます。答えを置く場所を分けてください。

| | 答えるもの |
|---|---|
| コード | How（どう実現するか） |
| テスト | What（何を約束するか） |
| コミットメッセージ | Why（なぜ変えたか） |
| コメント | Why not（なぜその書き方を採らなかったか） |

## アーキテクチャ

バックエンドと CLI のどちらも、Clean Architecture の依存の規則に従います。内側のコードは外側を
参照しません。ドメインの型はリポジトリの実装、HTTP ハンドラ、Chain Adapter を import しません。

パッケージは境界づけられたコンテキストごとに切り、層はその中でファイルに分けます。

```
internal/payment/
  payment.go      集約、値オブジェクト、不変条件
  service.go      ユースケース
  repository.go   リポジトリのインターフェース
  postgres.go     リポジトリの実装
  http.go         ハンドラ
```

- `domain` `application` `infrastructure` `common` `utils` `helpers` という名前の
  パッケージを作らないでください。
- インターフェースは呼び出す側のパッケージに、呼び出し箇所の近くで宣言してください。
- 依存の注入は `main` だけで行ってください。
- コンテキストの内部はそのコンテキストに閉じてください。コンテキストをまたぐ処理は
  ドメインイベントか公開したサービスを経由します。他のコンテキストのリポジトリを直接
  呼ばないでください。

依存の向きはテストで検証します。

## ドメインモデル

ドメイン駆動設計（DDD）の要素を次のように対応させます。

| DDD | suco Pay |
|---|---|
| 集約ルート | `Payment`（attempt を保持）、`Refund` |
| エンティティ | `Payment`、`Refund`、`Transaction` |
| 値オブジェクト | `Money`、`Address`、`Network`、`Status`、`Nonce`、`ConfirmationPolicy` |
| リポジトリ | 集約ルートごとに 1 つ |
| ドメインサービス | 確定判定 |
| アプリケーションサービス | Payment の作成、Transaction の観測、Webhook の配送 |
| ドメインイベント | `payment.succeeded` を同一トランザクションで outbox へ書き込み |

- 不変条件は集約に持たせてください。コンストラクタが不正な状態の `Payment` を返せてはいけません。
- 状態遷移は集約のメソッドとして書いてください。サービスの中で status を `switch` している
  場合、その遷移は集約に属します。
- 1 つのデータベーストランザクションで変更する集約は 1 つだけにしてください。
- リポジトリを持つのは集約ルートだけです。

### 金額

`Money` は値オブジェクトです。asset の最小単位で表した整数の金額と、asset の組で保持します。

- 金額に `float32` と `float64` を使わないでください。
- asset が異なる `Money` を加算したり比較したりしないでください。
- JPYC の `decimals` は 18 です。金額は `int64` に収まりません。

## コメント

[Go の doc コメント規約](https://go.dev/doc/comment)に従います。エクスポートした名前にはすべて
doc コメントを書きます。名前で始め、完全な文で書いてください。ゼロ値の意味が自明でない場合と、
並行安全性が既定と異なる場合は明記してください。

関数の中では、読み手が別のコードを想定するであろう箇所にコメントを書きます。採らなかった書き方を
名指ししてください。

```go
// Not sql.LevelSerializable: the observer re-reads the same rows every tick,
// and serialisation failures cost more than the version check they replace.
tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
```

```go
// Not comparing with ==: callers send JPYC amounts with trailing zeros.
if got.Cmp(want) != 0 {
```

コードを言い換えただけのコメントは、レビュアーが削除します。コメントがないと読めないコードは、
コードのほうを直してください。

未着手の作業には `TODO(username):` を使います。

## テスト

パッケージのテスト名を並べて読んだときに、そのパッケージが何を約束するかが分かる名前を
付けてください。

```go
func TestPayment_ExpiresOnlyFromAwaiting(t *testing.T)
func TestPayment_ConcurrentUpdatesLoseOnStaleVersion(t *testing.T)
func TestObserver_SameTransactionObservedTwiceCreatesOneRow(t *testing.T)
```

- テーブル駆動テストで書き、ケースごとに `t.Run` を使ってください。アサーションのヘルパーには
  `t.Helper()` を入れてください。
- すべての状態遷移に、失敗経路のテストを書いてください。
- account でスコープされるものは、2 つの account を作って分離を検証してください。
- パッケージが公開している面を通してふるまいを検証してください。エクスポートしていない状態に
  触れないでください。

## コミットと PR

Conventional Commits に従います（`feat:` `fix:` `docs:` `refactor:` `test:` `chore:`）。
1 つの PR には 1 つの論理的な変更だけを含めてください。

何を変えたかは diff が示します。本文には、変更の動機、検討して採らなかった案、受け入れた
トレードオフを書いてください。

```
fix: hold the outbox write inside the payment transaction

Delivery could report payment.succeeded for a payment whose update later
rolled back, so a merchant saw a payment we did not have.

Considered publishing after commit and reconciling the gap. Rejected: the
window is unbounded when the process dies between the two writes.
```

## PR の事前確認

- API、Payment の状態機械、Core の依存を変更する場合は、先に issue を立ててください。
- `make lint test` を実行してください。

## ドキュメント

ドキュメントを書くときの表記と文体は [WRITING-STYLE.ja.md](WRITING-STYLE.ja.md) にあります。

## ライセンス

コントリビュートしたコードはプロジェクトと同じ [Apache-2.0](LICENSE) になります。
コントリビューターライセンス同意書（CLA）はありません。

## セキュリティ

公開の issue を立てないでください。[SECURITY.ja.md](SECURITY.ja.md) を参照してください。
