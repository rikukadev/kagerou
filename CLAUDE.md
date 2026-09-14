# kagerou

CONTRIBUTING.md を読むこと。特に次の 2 つ:

- **実装を始める前に issue にコメントする。** このリポジトリは複数のエージェントが
  同時に触るので、宣言が無いと同じ issue を二重に実装する(実際に起きた)。
- **IAM の権限は `internal/iampolicy` の生成器に教える。** `ci-policy.json` を手で
  編集して終わりにしない(CI の `iam-policy-drift` が落ちる)。

設計の背景は `docs/DESIGN.md`、外部が依存してよい契約は `docs/CONTRACT.md`。
