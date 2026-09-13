# kagerou

Ephemeral environments on AWS.

**PR や E2E で使い捨てる環境を、既存のコンテナ定義から作成するツールです。**
同じイメージを Lambda(Web Adapter)で包んで、自分の AWS アカウントに
生やして、使って、消す。SAM や CloudFormation の知識は要りません。

Named after 陽炎 — a heat shimmer — and 蜉蝣, the mayfly that lives for a day.
Environments that appear, flicker, and are gone.

> Status: alpha. `init` / `validate` / `up` / `down` / `url` / `list` / `reap` /
> `iam-policy`、composite action、PR デモ 2 種まで動作。長期運用の実績はまだ。
> The first adapter is GitHub pull requests; the core is not PR-specific.

## 導入は init から

前提は 2 つだけ: **`aws` CLI(ログイン済み)と `gh` CLI(`gh auth login` 済み)**。
無ければ init の最初に確認して、その場でログインに誘導します。
AWS プロファイルが複数あれば**どれで作業するかを最初に選べます**し、
AWS に何かを作る直前には「**誰として・どのアカウントに作るか**」と、
その資格情報で足りない権限を表示してから y/n を取ります
(権限が足りなければ `kagerou-setup.sh` を残せるので、権限のある人に渡せます)。

```bash
kagerou init
```

リポジトリを読んで(git remote・フレームワーク・DB 依存・Dockerfile・AWS 認証)、
質問に答えるだけで一式が生成される:

- 既存の **Dockerfile を検出したら Lambda Web Adapter を 1 行注入**
  (Lambda の外では no-op。ローカルの docker build も ECS もそのまま)
- listen ポートは `EXPOSE` / compose から検出します
- AWS 側の準備(OIDC ロール・ECR・Variables)は **その場で適用 / スクリプト保存 / 手動**から選択
- mysql2 等を検出したら [sashiki](https://github.com/rikukadev/sashiki)
  (使い捨て DB ブランチ)との連携が既定の選択肢になる

他のリポジトリで**診断だけ**したいときは:

```bash
kagerou diagnose --dir ../some-app        # 何も書かず、AWS も呼ばない
kagerou diagnose --dir ../some-app --auth # 「ログイン必須」を仮定した場合
kagerou diagnose --json                   # 機械可読
```

```
kagerou diagnose  acme/shop

detected
  framework    go
  services     3
  port         8080

recommended
❯ apigateway  複数サービスを固定費ゼロで動かせる(リクエストは 30 秒まで)
  alb         上限は無いが、月 18 ドル前後の固定費がかかる
  lambda      単一コンテナ向け(複数サービスを検出)
```

```bash
kagerou validate          # 契約(CONTRACT)をデプロイ前に検査
kagerou iam-policy --with-ecr > ci-policy.json   # CI ロールの最小権限を構成別に生成
```

## 手元から

E2E・動作確認・デモ用の環境は、その場で生やしてその場で消せる:

```bash
kagerou up --name e2e-1     # 使い捨て環境を作る(冪等)
kagerou url --name e2e-1    # URL を取る(E2E のターゲットに)
kagerou down --name e2e-1   # 消す。忘れても TTL(既定 72h)+ reap が回収
```

## CI から(composite action)

PR を開くと環境が生え、URL コメントが付き、閉じると消える:

```yaml
- uses: rikukadev/kagerou/action@v0
  with:
    name: pr-${{ github.event.pull_request.number }}
```

## しくみ(30 秒版)

- 環境 = CloudFormation スタック 1 個。**専用の状態ストアは持たず**、
  タグ(`kagerou:name` / `kagerou:expires-at`)が真実の源
- すべての環境は **TTL 必須**。イベント駆動の削除が失敗しても `reap` が回収する
- アプリへの値は環境変数で注入(`--env DB_HOST` → テンプレートの `EnvDbHost`)。
  DB が sashiki でも RDS でも SQLite でも「env の出どころが違うだけ」
- 詳細な公開契約は [docs/CONTRACT.md](docs/CONTRACT.md)、設計は
  [docs/DESIGN.md](docs/DESIGN.md)

## 動く例

| リポジトリ | 構成 |
|---|---|
| [kagerou-ssr-demo](https://github.com/rikukadev/kagerou-ssr-demo) | React Router(SSR)を 1 Lambda で受ける最小構成 |
| [kagerou-3tier-demo](https://github.com/rikukadev/kagerou-3tier-demo) | React SPA / Go API / RDB を別オリジンで分離した構成 |

Sibling project: [sashiki](https://github.com/rikukadev/sashiki) — disposable database branches.
