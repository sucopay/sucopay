# 運用

English: [operating.md](operating.md)

支払いや Webhook が進まない場合に、suco Pay のインスタンスを監視して復旧するための
手順書です。[`suco.yaml`](configuration.ja.md) を検証済みの運用者を対象にしています。

インスタンスは、1 つの `suco serve` プロセスです。複数のインスタンスがデータベースを共有する
場合、ネットワークごとに 1 つがリースを取得して処理し、ほかは待機します。

## 症状から調べる

| 症状 | 最初の確認 | 次に読む箇所 |
|---|---|---|
| プロセスが応答しない | `GET /healthz` | プロセスのログとサービス管理ツール |
| API が `503` を返す | `GET /readyz` | 以下にある該当項目の説明 |
| 支払いの状態が進まない | `/readyz` の `networks` と `finality` | RPC プロバイダーと `cursor` を確認する `suco doctor` |
| Webhook が届かない | `/readyz` の `webhooks` | 通知履歴と `suco doctor` |
| 資産の停止やアップグレードが疑われる | `/readyz` の `assets` と `paused` | 資産の発行者に変更を確認 |

設定の変更や `cursor` の移動前に `suco doctor` を実行してください。`suco doctor` はデータを
変更せず、解決後の設定、データベース、RPC プロバイダー、資産、滞留している通知を報告します。

## /healthz と /readyz

`/healthz` と `/readyz` は認証を必要とせず、ポートに接続できる人は誰でも呼び出せます。
インフラ情報を漏らさないよう、レスポンスにはエラーの詳細、ホスト名、RPC URL を含めません。

### /healthz

`GET /healthz` はプロセスの実行中に `200` を返し、依存サービスは確認しません。死活監視には
この API エンドポイントを使ってください。データベース障害で正常なプロセスを再起動しても、
データベースは復旧しません。

### /readyz

`GET /readyz` は、インスタンスが API リクエストと支払いを処理できるかを報告します。

```json
{"status":"ok","database":"reachable","credentials":"read-write",
 "networks":{"polygon":"observing"},"assets":{"jpyc":"unchanged"},"paused":{"jpyc":false},
 "finality":{"polygon":"deciding"},"webhooks":"delivering"}
```

| 項目 | 説明 |
|---|---|
| `status` | `ok` か `unavailable` |
| `database` | `reachable` か `unreachable`。データベースを設定していないインスタンスでは `none configured` |
| `credentials` | 資格情報ストアのアクセスレベル。ストアがなければ省略 |
| `networks` | `network` ごとの取り込み状態。下の表で定義 |
| `assets` | 資産ごとのコントラクトコードの状態。下の表で定義 |
| `paused` | 資産ごとに、発行者が資産の送金を全部止めているか |
| `finality` | `network` ごとの確定判定ワーカーの状態。下の表で定義 |
| `webhooks` | Webhook ワーカー全体の状態。下の表で定義 |

データベースがない場合は、チェーンの取り込み、確定判定、Webhook 通知を行わないため、
`networks`、`assets`、`paused`、`finality`、`webhooks` を省略します。

`status` が `unavailable` になるのは、次のどちらかのときです。`unavailable` のレスポンスは
`503` です。

- データベースに届かない
- 設定した `network` の全部が `unreachable`、`stalled`、`chain-mismatch`、`no-finalized` のどれか

その他のネットワーク状態では `ok` のままです。`finality` と `webhooks` の状態も、全体の
`status` を変更しません。ワーカーに問題があっても、API は記録済みのデータを返せるためです。

### networks

`round` はチェーンを 1 回読む処理です。各 `network` の `cursor` には、`round` が最後に
読み終えたブロックを記録します。

| 語 | 意味 | 対処 |
|---|---|---|
| `observing` | 60 秒以内に `round` が確定済みの範囲を取り込み、保存した | なし |
| `no-cursor` | RPC エンドポイントへ接続でき、チェーン ID も一致するが、`round` がまだ完了していない | 最初の `round` を待つ |
| `unreachable` | 最新ブロックの取得に失敗した | RPC エンドポイントとプロバイダーを確認する。詳細は `suco doctor` で調べる |
| `stalled` | 60 秒間 `round` が完了していない | RPC プロバイダーを確認する。`cursor` の履歴が削除されていれば `suco network cursor` を使う |
| `chain-mismatch` | `eth_chainId` が設定した `chain_id` と異なる。取り込みは停止 | `chain_id` または RPC エンドポイントを修正する |
| `no-finalized` | RPC プロバイダーが `finalized` ブロックに対応していない。取り込みは停止 | `finalized` に対応するプロバイダーを使う |
| `finalized-changed` | `cursor` のブロックがチェーンに存在しない | `suco network cursor` で `cursor` を移動する |
| `finalized-behind` | RPC プロバイダーの確定済みブロックが `cursor` より前にある | 追いつくまで待つか、プロバイダーを切り替える |

60 秒は、インスタンスが `network` に対して取得する 30 秒のリースの 2 倍です。

ネットワークの取り込みが止まっている間、その `network` の支払いは期限切れになりません。
期限切れは時計ではなく、`cursor` のブロック時刻で判断します。詳細は
[設定](configuration.ja.md#cursor) を参照してください。

### finality

`finality` は、検出済みの送金が確定したかを判断するワーカーの状態です。

| 語 | 意味 | 対処 |
|---|---|---|
| `deciding` | 確定判定の `round` が完了し、結果を保存した | なし |
| `no-round` | 起動後に確定判定の `round` が完了していない | 最初の `round` を待つ |
| `waiting` | 別のインスタンスが `network` のリースを持ち、確定判定を実行している | なし |
| `too-few` | 一致に必要な数の `rpc.others` が応答しなかった | RPC エンドポイントを追加または修復する。応答数は `suco doctor` で確認する |
| `unreachable` | `round` がデータベースへ接続できなかった | `database` を確認する |
| `stalled` | 確定判定の `round` が完了しなくなった | インスタンスを再起動する |

`deciding` と `too-few` は、1 回の遅延で状態が頻繁に変わらないよう、`finality.recheck` の
3 回分の間表示します。

### assets と paused

`assets` は資産ごとに `unchanged` または `changed` を返します。`changed` は、コントラクトの
実装が起動時から変わったことを示します。プロキシーのアップグレードなどが原因です。資産の受け入れを
続ける前に、発行者へ変更内容を確認してください。

`paused` は、各資産のコントラクトが全送金を停止しているかを示し、1 分ごとに更新します。
一覧にない資産は直前の 2 分間に取得できていません。省略を `false` と解釈しないでください。
停止中の資産があっても、送金を再開できるのは発行者だけなので全体の準備状態は変わりません。

### webhooks

1 つのワーカーがすべての Webhook エンドポイントへ通知するため、`webhooks` は suco Pay 全体で
1 つの状態を返します。

| 語 | 意味 | 対処 |
|---|---|---|
| `delivering` | `round` が状態変化から通知を作成し、送信時刻を迎えた通知を送った | なし |
| `no-round` | インスタンスが起きてから `round` が 1 度も終わっていない | 最初の `round` を待つ |
| `waiting` | 別のインスタンスが Webhook のリースを持ち、このインスタンスは待機している | なし |
| `unreachable` | `round` がデータベースへ接続できなかった | `database` を確認する |
| `stalled` | `round` が終わらなくなった | インスタンスを再起動する |

`delivering` は 15 秒間、つまり 5 秒の `round` 3 回分表示します。Webhook に問題があっても、
加盟店は API から現在の状態を取得できるため、全体の `status` は `unavailable` になりません。

## suco doctor

`suco doctor` はデータを変更しない診断コマンドです。解決後の各設定と取得元を出力した後、
`suco serve` と同じ接続方法でデータベース、RPC プロバイダー、資産、Webhook の滞留を確認します。

```
suco.yaml

  assets.jpyc.decimals       18                                      file
  assets.jpyc.network        polygon                                 file
  ...
  networks.polygon.rpc.own   set                                     ${SUCO_POLYGON_RPC_URL}

database: PostgreSQL 17.5, schema 0010_credential_access
credentials: read-write
webhooks:
  3f9c2c1e-6a1b-4a1e-9f4e-2f0f2c5a7b11  2 pending, 1 failed to 7c1d…

networks:
  polygon  evm  chain 137, latest 78123, final 78100, position 78090, behind 10, 2 waiting to settle
    jpyc        implementation 0xa1b2c3...
```

| 語 | 意味 | 対処 |
|---|---|---|
| `2 waiting to settle` | 確定判定を待つ記録済みの送金数 | 増え続ける場合は、`/readyz` で `finality` の状態を確認する |
| `1 disagreed about` | RPC エンドポイントの結果が食い違う送金数 | RPC プロバイダーを調査する。食い違う送金は自動では確定しない |
| `3 of 4 others answer` | 設定した `rpc.others` のうち応答した数 | なし |
| `no spare` | 答える本数が、一致に必要な数とちょうど同じ | `rpc.others` に RPC エンドポイントを足す |
| `too few to settle` | 答える本数が、一致に必要な数に足りない | `rpc.others` の RPC エンドポイントを足すか直す |
| `behind 10` | `cursor` が確定済みブロックから 10 ブロック遅れている | 差が増え続けなければ対処不要 |
| `could not be read: …` | RPC プロバイダーがエラーを返した。RPC URL は表示しない | RPC プロバイダーとエンドポイントを確認する |
| `chain 80002 (Polygon Amoy, a testnet)` | RPC エンドポイントが既知のテストネットのチェーン ID を返した | テスト環境として意図した設定か確認する |
| `paused` | 発行者が資産の全送金を停止している | 発行者へ問い合わせる |
| `paused not read` | 資産のコントラクトが、止まっているかどうかを答えなかった | RPC プロバイダーを確かめる |
| `paid to 0x…, which is blocklisted, the provider says` | 資産のコントラクトが、アカウントの受取アドレスへの送金を拒む | 別のアドレスで `suco asset accept` を実行する |
| `2 pending, 1 failed to 7c1d…` | pending と failed の通知数、および該当する Webhook エンドポイント ID | 想定外の失敗なら、エンドポイントの通知履歴を確認する |
| `nothing pending, nothing failed` | pending または failed の通知がない | なし |
| `3 endpoints hold a secret sealed under another key` | Webhook エンドポイントの署名シークレットが以前の `credentials.key` で暗号化されている | 加盟店に署名シークレットの更新を依頼する |

通知ワーカー自体の状態は、`/readyz` の `webhooks` で確認します。

資産の検査では、受取アドレスを `rpc.own` または `rpc.others` の先頭へ送信します。
`network` を読めない場合と、データベース内のアカウントが 1 つでない場合は検査を省略します。

データベースのスキーマが初期化されていない場合も、設定したチェーンへの接続は確認できます。
ただし、保存済みの `cursor` は報告できません。

## suco asset accept

```bash
suco asset accept <name> <address>
```

設定済みの資産 `<name>` に対し、`<address>` を受取ウォレットとして登録します。保存前に、
コントラクトの EIP-712 ドメインと、発行者がアドレスへの送金を拒否していないかを確認します。

| 拒否するとき | 対処 |
|---|---|
| コントラクトの EIP-712 ドメインが設定と異なる | `assets.<name>.eip712` と `network` の `chain_id` をコントラクトに合わせる |
| コントラクトが `<address>` への送金を拒む | 別のアドレスにする |
| どちらかのコントラクト検査に失敗した | RPC プロバイダーを確認する。アドレスは登録されない |

JPYC は `DOMAIN_SEPARATOR()` を公開していません。この場合はチェーン ID とドメイン名を検証し、
`version` には設定値を使います。成功時の出力は、`version` が設定値であることを示します。

検査では `<address>` を `rpc.own` または `rpc.others` の先頭へ送信します。このアドレスは、
支払いを受け取った時点でも公開情報になります。

## suco network cursor

```bash
suco network cursor <name> <height> [--force]
```

各 `network` の `cursor` は、`round` が最後に取り込んだブロックの高さとハッシュを持ちます。

`cursor` のブロックがチェーンから消えた場合に、このコマンドで存在するブロックへ移動します。
その間、`/readyz` は `finalized-changed` を返し、取り込みを自動では再開できません。

コマンドは指定した高さのブロックを読み、高さとハッシュを保存します。これにより、別のチェーンの
同じ高さを誤って保存することを防ぎます。

コマンドは `network` のリースを取得しません。同時に動いている `round` は `cursor` の更新を
確定できず、次の `round` が新しい位置から読み始めます。

> **重要:** `cursor` を前へ移動すると、飛ばしたブロックを今後取り込みません。飛ばした
> ブロックに支払いがあると、入金済みでも未払いとして期限切れになります。返金があると、
> 送金後に確保額を解放し、同じ金額を再度返金できる状態になる可能性があります。

これを防ぐため、対象ネットワークに `awaiting_payment` または `awaiting_finality` の支払い、
`created` または `awaiting_finality` の返金がある間は前進を拒否し、状態ごとの件数を表示します。
`--force` はこの検査を回避し、飛ばす各状態の件数をログに記録します。後方への移動では、以後の
`round` が同じ範囲を読み直すため、この検査を行いません。

`--force` を使うのは、RPC プロバイダーが必要な履歴を削除し、影響する未完了の支払いと返金を
すべて照合した場合に限ってください。

ログには移動前後の `cursor` を記録するため、必要に応じて元の位置へ戻せます。

## suco payment await

```bash
suco payment await <id>
```

支払いを支払い可能な状態にし、支払者が署名する 7 つの値を出力します。Checkout のブラウザー
モジュールがリリースされるまでは、このコマンドで支払いをテストできます。

```
contract 0xe7c3d8c9a439fede00d2600032d5db0be71c3c29
chainId 137
to 0x1234567890123456789012345678901234567890
value 1000000000000000000
validAfter 0
validBefore 1788972899
nonce 0x11becaaf611be4cfb6bd8a5287bf3688f85dbbbd2af45aa10ac31a7b4513ae01
```

`nonce` は送金と支払いを対応付けます。`validBefore` は支払期限を秒単位に切り捨てた値です。
署名データにこれより遅い期限を指定できる場合でも、表示された期限以降に送信された送金は
支払いを完了させません。

> **重要:** この出力は、信頼できる経路で支払者だけに送ってください。支払いの詳細が含まれます。
> 支払者の秘密鍵も持つ人は、この情報を使って支払いに署名できます。

支払いは `awaiting_payment` で、インスタンスが対象ネットワークの `round` を 1 回以上完了して
いる必要があります。同じ支払いでもう一度実行しても 2 つ目の `nonce` は作らず、既に発行済み
であることを表示します。

## 内側のネットワークにある Webhook エンドポイント

Webhook の登録時と送信時には、プライベートアドレスまたはループバックアドレスへ解決する URL
を拒否します。これは、デプロイ先ネットワークへの SSRF を防ぐためです。管理下にある内部の
受信側を許可するには、そのエンドポイントのデータベース行にアドレス範囲を設定します。

```sql
update webhook_endpoints set allowed = '{10.0.5.0/24}' where id = '<endpoint id>';
```

許可はホスト名ではなく、1 つのエンドポイントとアドレス範囲に適用されます。まず外部 URL で
登録し、データベースを更新してから、`PATCH /webhook_endpoints/{id}` で内部 URL に変更します。
変更時には、許可した範囲に対して新しい URL を検証します。無効なプレフィックスを設定しても、
どのアドレスも許可されません。

## 次のステップ

- `status: unavailable`、確定待ちの増加、Webhook 送信失敗を監視してください。
- 本番環境で `--force` を使う前に、テスト環境で `cursor` の復旧手順を確認してください。
- 連携担当者に [Webhook 受信側の要件](webhooks.ja.md#受信側の要件)を共有してください。
