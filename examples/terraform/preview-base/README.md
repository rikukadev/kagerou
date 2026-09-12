# preview base — Terraform reference

kagerou の per-app preview base(共有 CloudFront / ワイルドカード証明書 / DNS /
S3)を **Terraform で作る**リファレンス。CFN 版(`kagerou init` が書き出す
`deploy/preview-base.yaml`)と同じものを作り、**同じ SSM データ契約**
(CONTRACT §9)を書く:

```
/kagerou/base/<project>/domain
/kagerou/base/<project>/bucket
/kagerou/base/<project>/distribution
```

kagerou はベースを **SSM 経由でしか見ない**ので、CFN で作っても Terraform で
作っても同じに見える。既に Terraform を回しているチームはこのモジュールを
自分たちの構成に取り込めばよく、`kagerou` 本体が terraform を実行することはない。

## 使い方

```hcl
provider "aws" {
  region = "us-east-1" # 必須: CloudFront 用 ACM 証明書は us-east-1 のみ
}

module "kagerou_preview_base" {
  source         = "github.com/rikukadev/kagerou//examples/terraform/preview-base"
  project        = "todo"
  domain_name    = "todo.example.com" # URL は pr-42.todo.example.com になる
  hosted_zone_id = "Z0123456789ABC"   # domain_name の親を持つ公開ゾーン
}
```

適用後、そのリポジトリで `kagerou init` を実行すると SSM からベースを検出して
ドメインの質問は出ない(相乗り)。

## 注意

- **us-east-1 で適用すること**(証明書と SSM 契約の置き場所。CFN 版と同じ制約)。
- ここは参照実装であり CI では検証していない。適用前に `terraform plan` を確認すること。
- キャッシュは CachingDisabled(プレビューは正しさ優先、invalidation 不要)。
