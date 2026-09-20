# コントリビュート

English: [CONTRIBUTING.md](CONTRIBUTING.md)

## 開発環境

```bash
make dev     # Docker でデータベースを起動します
make test
make check   # CI が走らせる検査の全部です
```

Go 1.26 以降が必要です。`make dev` は Docker、`make check` は golangci-lint を使います。

テストは `make dev` が起動するデータベースを使います。データベースが無いときは失敗します。
飛ばしたテストは、走っていないのに成功として数えられるためです。接続先は
`SUCO_TEST_DATABASE_URL` に書きます。既定値は Makefile にあり、`make dev` が起動する
データベースを指しています。

## コード、テスト、コミット、コメント

それぞれ別の問いに答えます。答えを置く場所を分けてください。

| | 答える問い |
|---|---|
| コード | How（どう実現するか） |
| テスト | What（何を約束するか） |
| コミットメッセージ | Why（なぜ変えたか） |
| コメント | Why not（なぜその書き方を採らなかったか） |

## アーキテクチャ

バックエンドと CLI のどちらも、Clean Architecture の依存の規則に従います。内側のコードは外側を
参照しません。ドメインの型はリポジトリの実装、HTTP ハンドラー、チェーンのアダプターを
import しません。

ドメインのパッケージは境界づけられたコンテキストごとに切ります。層はコンテキストの中で
ファイルに分けます。

```
internal/payment/
  payment.go      集約、値オブジェクト、不変条件
  service.go      ユースケース
  repository.go   リポジトリのインターフェース
  postgres.go     リポジトリの実装
  http.go         ハンドラー
```

- `domain` `application` `infrastructure` `common` `utils` `helpers` という名前の
  パッケージを作らないでください。パッケージ名は何を担うかを表し、どの層に属するかは表しません。
- 1 つのコンテキストではなくプロセス全体に関わるパッケージは、担う関心で名前を付けます。
  例は `config` です。
- `api` はサーバーと、コンテキストを取り付けるルーティングだけを持ちます。コンテキスト固有の
  ハンドラーは `internal/<コンテキスト>/http.go` に置いてください。
- インターフェースは呼び出す側のパッケージに、呼び出し箇所の近くで宣言してください。
  複数の呼び出し側が使い、どのアダプターも実装するインターフェースは例外で、全部が
  import できる専用のパッケージに置きます。例は `internal/adapter/chain` です。
- 依存の注入は `main` だけで行ってください。
- コンテキストの内部はそのコンテキストに閉じてください。コンテキストをまたぐ処理は
  ドメインイベントか公開したサービスを経由します。他のコンテキストのリポジトリを直接
  呼ばないでください。

向きはテストで確かめます。パッケージ内のファイルの import を読み、内側のファイルに
許していない import があれば失敗するテストです。`internal/accepted`、`internal/adapter/chain`、
`internal/adapter/chain/evm`、`internal/checkout`、`internal/config`、`internal/credential`、
`internal/finality`、`internal/observe`、`internal/payment`、`internal/postgres`、
`internal/refund`、`internal/webhook` にそれぞれあります。

## ドメインモデル

ドメイン駆動設計（DDD）の要素を次のように対応させます。

| DDD | suco Pay |
|---|---|
| 集約ルート | `Payment`、`Attempt`（支払いを指す 1 回の支払い試行）、`Refund` |
| エンティティ | `Payment`、`Refund`、`Transaction`、`Attempt` |
| 値オブジェクト | `Money`、`Address`、`Network`、`Status`、`Nonce`、`ConfirmationPolicy` |
| リポジトリ | 集約ルートごとに 1 つ |
| ドメインサービス | 確定判定 |
| アプリケーションサービス | 支払いの作成、送金の観測、通知の送信 |
| ドメインイベント | `payment.succeeded` を同一トランザクションで outbox へ書き込み |

- 不変条件は集約に持たせてください。コンストラクターが不正な状態の `Payment` を
  返せてはいけません。
- 状態遷移は集約のメソッドとして書いてください。サービスの中で status を `switch` している
  場合、その遷移は集約に属します。
- 1 つのデータベーストランザクションで変更する集約は 1 つだけにしてください。
- リポジトリを持つのは集約ルートだけです。

### 金額

`Money` は値オブジェクトです。資産の最小単位で表した整数の金額と、資産の組で保持します。

- 金額に `float32` と `float64` を使わないでください。
- 資産が異なる `Money` を加算したり比較したりしないでください。
- JPYC の `decimals` は 18 です。金額は `int64` に収まりません。

## 既定値

すべての設定に、一般的な用途でそのまま動く既定値を持たせます。

- 適切な既定値が存在しない設定だけを必須にしてください。受取アドレスに妥当な既定値は
  ありません。確定判定にはあります。
- コマンドが選んだ値は、生成する設定ファイルに書き出してください。組み込みの既定値と同じ値も
  書き出します。
- 必須にする設定やファイルには、生成するコマンドを用意してください。`suco serve` が
  `suco.yaml` を必須にするので、`suco init` が `suco.yaml` を書きます。

## 設定

設定は、持っている値の名前を付けます。

- 最上位は何を設定するかです。`listen`、`log`、`database`、`credentials`、`networks`、`assets`。
  `networks` と `assets` の下は、設定ファイルが選んだ名前です。
- 葉は名詞、切り替えなら何を入れるかを表す語です。`port`、`kind`、`width`、`poll`、`managed`。
  動詞を含む語句にはしません。`give_up_after` のような名前は時間の長さに読めて、中身は回数です。
- 語をつなぐのは下線です。`base_url`、`chain_id`、`key_id`。
- 葉は単位を持ちません。単位は値が持ち、どれなのかは設定の一覧が示します。`poll` は `12s`、
  `width` はブロックの数です。
- 1 つの判断に属する設定は名詞の下にまとめます。例は `rpc.own` と `rpc.others` です。離れて
  並んだ葉が同じことについてだと読み手が知っていなければならなくなるなら、まとめます。
- 足した設定は同じ変更で `docs/configuration.md` と `docs/configuration.ja.md` の両方に書きます。
  表とコードが合っているかを確かめる検査はありません。片方から漏れた設定は、読み手の半分に
  見つけられません。

## 移行

適用された移行は書き換えません。適用した移行ごとに checksum を記録していて、変わった移行が
見つかれば起動を拒否します。書き換える前と後の、どちらの移行をデータベースが持っているのかが
分からなくなるからです。新しい移行を足してください。

移行は記録と同じ 1 つのトランザクションで走り、途中で失敗すれば何も残りません。
`CREATE INDEX CONCURRENTLY` はトランザクションの中で走りません。1 行目が
`-- suco: index concurrently` の移行はトランザクションの外で走ります。本文は
`create [unique] index concurrently <名前> on ...` の 1 文だけで、ほかの移行と同じく小文字で
書き、`if not exists` は書きません。先に同じ名前のインデックスを削除し、カタログが
インデックスを有効と示してから記録します。失敗した実行や途中で切れた実行が残すのは、同じ名前の
有効でないインデックスです。

## エラー

`suco` の後ろに打たれた語を、エラーの文に繰り返しません。`credential new` はトークンをファイルに
書きます。スクリプトが手前の語を落とすと、トークンが `suco` の後ろに来ることがあります。
エラーの文は CI の記録に残ります。コマンドが取るフラグを示し、余分な語は数で挙げてください。

```
credential new takes --read-only or --read-write, got neither
serve takes no arguments, got 1
```

## コメント

[Go の doc コメント規約](https://go.dev/doc/comment)に従います。エクスポートした名前にはすべて
doc コメントを書きます。名前で始め、完全な文で書いてください。ゼロ値の意味が自明でない場合と、
並行安全性が既定と異なる場合は明記してください。

関数の中では、読み手が別のコードを想定するであろう箇所にコメントを書きます。採らなかった書き方を
指定してください。

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

- テストの名前が指す挙動を壊し、失敗することを確認してから残してください。名前は主張であり、
  壊しても通るテストは、検証せずに主張していることになります。
- テーブル駆動テストで書き、ケースごとに `t.Run` を使ってください。アサーションのヘルパーには
  `t.Helper()` を入れてください。
- すべての状態遷移に、失敗したときのテストを書いてください。
- account でスコープされる対象は、2 つの account を作って分離を検証してください。
- パッケージが公開している面を通してふるまいを検証してください。エクスポートしていない状態に
  触れないでください。
- 環境変数を設定しないテストには `t.Parallel()` を入れてください。他のテストが残した状態に
  依存しているテストが、黙って通らずに失敗するようになります。
- データベースが必要なテストは `postgrestest.Fresh` で自分専用のデータベースを取ってください。
  データベースが無いときにスキップはしません。スキップしたテストは、走っていないのに
  成功として数えられます。他人が動かしているチェーンを読むテストだけは例外です。
  リポジトリにチェーンを起動する手段が無いので、RPC エンドポイントを環境変数から取り、
  名前が無ければスキップします。

### ファジング

他人が書いた入力（設定ファイル、金額、アドレス、metadata）を読む箇所には、fuzz target を
置いてください。期待する出力ではなく、成り立つべき性質を書きます。生成される入力は
ほとんど拒否されます。拒否は失敗ではないためです。

```go
// panic しない。エラーが受け取った内容を書き戻さない。
// 端末に届くものが、端末の動作を変える文字を含まない。
```

**判定は、検証対象の関数を通さずに書いてください。** `Quote` の出力を `Has` で調べると、
両方が同じ内部判定を呼ぶので、その判定から文字が 1 つ抜けても両側が同時に見落とします。
何百万回走らせても見つかりません。

`slog` の text handler は、表示できないと判断した文字を自分で escape します。text 出力を見る
テストは、引用したかどうかに関係なく通ります。json handler が escape するのは JSON が
必要とする分だけで、ゼロ幅スペースも双方向オーバーライドも渡されたまま出ます。書き出す側が
json を使う以上、引用は json で検査してください。

`go test` は各 target の seed corpus を実行するので、一度見つかった入力はそのまま
検査され続けます。長く探すのは `make fuzz` です。失敗すると、Go が入力を `testdata/fuzz/` に
書き出します。書き出された入力を commit してください。

## コミットと PR

Conventional Commits に従います（`feat:` `fix:` `docs:` `refactor:` `test:` `chore:`）。
1 つの PR には 1 つの論理的な変更だけを含めてください。コミット前に `make check` を実行して
ください。`make hooks` を実行すると、`make check` が見ていない変更の commit を拒否する
フックが入ります。

読み手が参照できるのはリポジトリの中だけです。開けない文書を根拠に挙げても、理由があることが
分かるだけで、理由そのものは伝わりません。

何を変えたかは diff が示します。本文には、変更の動機、検討して採らなかった案、受け入れた
トレードオフを書いてください。

```
fix: hold the outbox write inside the payment transaction

The webhook could report payment.succeeded for a payment whose update later
rolled back, so a merchant saw a payment we did not have.

Considered publishing after commit and reconciling the gap. Rejected: the
window is unbounded when the process dies between the two writes.
```

## AI の道具

使って構いません。提出した変更は自分の変更です。読んだ上で、説明でき、責任を負います。
道具が手伝ったなら、何をどこまでかを PR に書いてください。commit そのものには何も足しません。

## PR の事前確認

- API と支払いの状態機械を変える変更、コアに依存を足す変更は、先に issue を立ててください。
- `make check` を実行してください。

## ドキュメント

ドキュメントを書くときの表記と文体は [WRITING-STYLE.ja.md](WRITING-STYLE.ja.md) にあります。
英語のドキュメントにも適用します。

## ライセンス

コントリビュートした変更はプロジェクトと同じ [Apache-2.0](LICENSE) になります。
コントリビューターライセンス同意書（CLA）はありません。

## セキュリティ

公開の issue を立てないでください。[SECURITY.ja.md](SECURITY.ja.md) を参照してください。
