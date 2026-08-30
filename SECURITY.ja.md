# セキュリティポリシー

English: [SECURITY.md](SECURITY.md)

## 脆弱性の報告

GitHub の[非公開の脆弱性報告](https://docs.github.com/code-security/security-advisories/guidance-on-reporting-and-writing/privately-reporting-a-security-vulnerability)から
報告してください。脆弱性の詳細を issue、Discussions、その他の公開の場に書かないでください。

## 対象範囲

次の報告を優先して扱います。

- 資金が動いていないのに Payment が succeeded になる経路
- Webhook の偽造とリプレイ
- API の認証と認可の回避
- 秘密鍵などの機密情報の露出
- 冪等性の不備による二重処理
- チェーンの再編成や確定判定の扱いを悪用できる経路
- インストールスクリプト、リリース成果物、コンテナイメージ
- 運用者が何も変更していない時点で安全でない既定設定
- リリースにコードを混入させうる依存関係とビルド手順

次のものは対象外です。

- 個別の環境の設定の誤り
- ホスト、データベース、ウォレットの鍵をすでに掌握していることを前提とするもの

## 対象バージョン

修正を反映するのは `main` だけです。
