# kagerou 公開契約 v0.1

> 外部(Backstage プラグイン、CI、他ツール)が依存してよい安定インターフェース。
> **ここに書いたものは v0.x の間も後方互換を守る(変更はフィールド/タグの追加のみ)**。
> 設計の背景は docs/DESIGN.md。

## 1. タグスキーマ

kagerou が管理する AWS リソースには以下のタグが付く。stack driver では CFN
スタックに付け、配下リソースへは CFN の伝播に任せる。スタック外リソースを持つ
driver は個別に付ける責務を負う。

| キー | 値 | 備考 |
|---|---|---|
| `kagerou:managed` | `"true"` | 走査時の第一フィルタ |
| `kagerou:name` | 環境名(例 `pr-42`) | |
| `kagerou:project` | プロジェクト名(例 `todo`) | kagerou.yaml の `project`。未設定なら省略 |
| `kagerou:driver` | `stack` 等 | |
| `kagerou:expires-at` | RFC3339 UTC(例 `2026-09-15T00:00:00Z`)または `none` | `none` = 明示的な無期限 |
| `kagerou:url` | `url_template` で作成前に確定した環境 URL | 未設定なら省略。§5 参照 |
| `kagerou:source` | opaque 文字列。URI 形式を推奨(例 `github_pr://rikukadev/todo/42`) | adapter が `--source` で渡す。kagerou は解釈しない。CFN タグ値の文字種制約(英数字と ` +-=._:/@`)により JSON は入らない |
| `kagerou:version` | 作成した kagerou のバージョン | |

## 2. 環境名の制約

```
^[a-z]([a-z0-9-]*[a-z0-9])?$   かつ 63 文字以下
```

小文字英字始まり・小文字英数字とハイフン・末尾ハイフン禁止。Route53 の
サブドメインラベル(63 文字)と S3 プレフィックスにそのまま使えるよう最初から狭くする。

## 3. Environment JSON

`kagerou up|list|url --output json` が返す形。以後の変更はフィールド追加のみ。

```json
{
  "name": "pr-42",
  "project": "todo",
  "driver": "stack",
  "state": "ready",
  "url": "https://...",
  "created_at": "2026-09-12T01:00:00Z",
  "expires_at": "2026-09-15T01:00:00Z",
  "source": "github_pr://rikukadev/todo/42",
  "stack": { "name": "todo-pr-42", "status": "UPDATE_COMPLETE" }
}
```

- `state`: `creating | updating | ready | deleting | failed`
- `expires_at`: TTL なしのときは `null`
- `source`: opaque 文字列(URI 形式推奨)。未指定のときは `null`。kagerou は解釈しない
- driver 固有の情報は driver 名のキー(`stack` 等)の下に入れ子にする

## 4. env の届け方(stack driver)

テンプレートが **`Env<Key>` という名前のパラメータを宣言していれば**、kagerou は
`--env` / kagerou.yaml の env をそこへ流す。宣言が受け取り口であり、kagerou は
テンプレートの中身(Resources)を解釈しない。

- パラメータ名変換: env キーを `_` で区切り、各部を先頭大文字化して連結し
  `Env` を前置する。例: `DB_HOST` → `EnvDbHost`、`DB_USER` → `EnvDbUser`
  (CFN パラメータ名に `_` が使えないための規約)
- 対応するパラメータが宣言されていない env キーが渡されたらエラー
  (黙って落とすと事故になるため)

```yaml
# template.yaml 側の受け取り口の例
Parameters:
  EnvDbUser: { Type: String, Default: "" }
Resources:
  Fn:
    Properties:
      Environment:
        Variables:
          DB_USER: !Ref EnvDbUser
```

## 5. URL の発行

環境 URL の解決順:

1. **`kagerou:url` タグ** — kagerou.yaml の `url_template`(例
   `"https://{name}.preview.example.com"`)で**作成前に確定**した URL。
   テンプレートが `EnvKagerouUrl` パラメータを宣言していれば同じ値が届くので、
   CORS 許可元・OAuth コールバック等を循環参照なしに書ける
2. CFN Outputs のキー `KagerouUrl`
3. CFN Outputs のキー `PreviewUrl`

`url_template` の URL に実際にトラフィックを到達させる仕組み(共有 CloudFront +
サブドメイン等)は利用者側のインフラの責務(kagerou#32 で共通化を検討中)。

## 6. 読み取り口(予定)

Backstage 等からの読み取りは `kagerou serve`(読み取り専用 HTTP、Lambda function
URL 想定)を提供予定。書き込み(down / TTL 延長)は AWS を直接叩かず、CI の
`workflow_dispatch` 経由で kagerou を起動する。

## 7. Hooks に渡る環境変数

kagerou.yaml の hooks(`pre_up` / `post_up` / `post_down`)は `sh -c` で実行され、
以下の環境変数を受け取る。以後の変更は追加のみ。

| 変数 | 中身 | 渡るフック |
|---|---|---|
| `KAGEROU_NAME` | 環境名 | すべて |
| `KAGEROU_URL` | 環境 URL(§5 の Output。無ければ未設定) | post_up |
| `KAGEROU_OUTPUT_<KEY>` | 任意の CFN Output。キーは大文字化(`ApiUrl` → `KAGEROU_OUTPUT_APIURL`) | post_up |
| `KAGEROU_ENVIRONMENT_JSON` | §3 の Environment JSON そのもの | post_up |

失敗時の扱い: `pre_up` / `post_up` の失敗は up を失敗させる(post_up は
「環境はあるが仕上がっていない」を green にしないため)。`post_down` の失敗は
記録して続行する。1 フックの実行上限は 10 分。

```yaml
# 例: SPA の config.json を生成して静的成果物を配置する(3 層構成の定型)
hooks:
  post_up: |
    printf '{"apiBaseUrl":"%s"}' "$KAGEROU_OUTPUT_APIURL" > web/dist/config.json
    aws s3 sync web/dist/ "s3://$KAGEROU_OUTPUT_WEBBUCKETNAME/" --delete
```
