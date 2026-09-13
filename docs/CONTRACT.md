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
| `kagerou:project` | プロジェクト名(例 `todo`) | kagerou.yaml の `project`。未設定なら省略。**`reap` / `list` の分離境界**(下記) |
| `kagerou:driver` | `stack` / `static` | |
| `kagerou:expires-at` | RFC3339 UTC(例 `2026-09-15T00:00:00Z`)または `none` | `none` = 明示的な無期限 |
| `kagerou:url` | `url_template` で作成前に確定した環境 URL | 未設定なら省略。§5 参照 |
| `kagerou:source` | opaque 文字列。URI 形式を推奨(例 `github_pr://rikukadev/todo/42`) | adapter が `--source` で渡す。kagerou は解釈しない。CFN タグ値の文字種制約(英数字と ` +-=._:/@`)により JSON は入らない |
| `kagerou:version` | 作成した kagerou のバージョン | |
| `kagerou:owner` | 作成者の IAM プリンシパル(例 `arn:aws:iam::123:role/deploy`) | assumed-role はセッション名を落としてロール ARN に正規化。**`up` の上書き境界**(下記) |

**`kagerou:project` は `reap` / `list` の分離境界**。同じ AWS アカウント/リージョンに
複数リポジトリ(プロジェクト)が同居しうるため:

- `list` / `reap` は既定で `kagerou:project == kagerou.yaml の project` の環境だけを対象にする。
- `--all-projects` で全プロジェクトを対象にできる(`list` の全件表示、`serve` 用)。
- `reap --all-projects` は **`post_down` hook を実行しない**(対象がどのリポジトリの環境か
  決められず、この kagerou.yaml の hook を他プロジェクトの環境名で実行してしまうため)。
  各プロジェクトの孤児は、そのプロジェクトの `reap` が hook 付きで回収する。
- `project` 未設定の kagerou.yaml で `reap` を実行すると、対象を絞れないため**拒否**する
  (明示的に `--all-projects` を付けた場合のみ全件を対象にする)。

**`kagerou:owner` は `up` の上書き境界**。同名の環境がすでにあるとき:

- owner が呼び出し元と同じ(または owner タグの無い旧環境)なら、従来どおり冪等に上書きする。
- **他人の環境なら名前衝突としてエラー**にする(誰の環境かをエラーに含める)。別の
  `--name` を選ぶか、所有者に `kagerou down` してもらう(放置しても TTL + `reap` が回収する)。
- CI は全員が同じデプロイロールを assume する = 同一 owner なので、PR への push で
  同名環境の更新が続く挙動は変わらない。`reap` は owner に関係なく期限切れを回収する。

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

- `state`: `creating | starting | updating | ready | deleting | failed`
  - `starting` はスタックは完成したが `readiness_path` がまだ 200 を返さない状態
    (kagerou.yaml で `readiness_path` を設定したときだけ現れる)
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

## 4.5 driver: static

compute を持たない環境(SSG / CSR の SPA)。テンプレートも `Env<Key>` も使わず、
ビルド成果物を preview base バケットの `{name}/` プレフィックスへ配置する。

```yaml
driver: static
url_template: "https://{name}.myapp.example.com" # static では必須(URL を出す Output が無い)
static:
  dist: dist                         # ビルド成果物のディレクトリ
  bucket: kagerou-base-myapp-123456  # preview base スタックの bucket Output
```

- 環境の実体は S3 プレフィックスだが、**環境 = CFN スタック 1 個の不変条件は維持する**
  (タグの担い手として実リソースを持たない極小スタックを作る)。list / reap /
  Environment JSON / iam-policy は stack driver と共通のまま
- `up` は毎回同期し、ローカルに無いファイルは S3 からも消す(`aws s3 sync --delete` 相当)
- **`post_up` は同期の前に走る**(static は「同期 = 公開」なので、環境固有の
  ファイル生成が同期より後だと反映されない)。URL は `url_template` で作成前に
  確定しているため、フックには `KAGEROU_URL` も渡る
- `down` はスタックとプレフィックス配下の両方を消す

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

## 6. 読み取り口(`kagerou serve`)

Backstage 等からの読み取りは `kagerou serve` が提供する**読み取り専用 HTTP**。
Lambda function URL に AWS Lambda Web Adapter(LWA)越しで置く想定(`$PORT` を見る)。
**書き込み(down / TTL 延長)は提供しない** — AWS を直接叩かず、CI の
`workflow_dispatch` 経由で kagerou を起動する(権限もロジックも CI 側に留める)。

エンドポイント(以後の変更はフィールド/エンドポイントの追加のみ):

| メソッド・パス | 返すもの |
|---|---|
| `GET /environments` | §3 の Environment JSON の**配列**(全プロジェクト横断)。`?project=<name>` で 1 プロジェクトに絞れる |
| `GET /environments/{name}` | §3 の Environment JSON 単体。無ければ `404` |
| `GET /healthz` | `{"status":"ok"}`(疎通確認) |

- 一覧は既定で**全プロジェクト**(`list --all-projects` 相当)。横断ビューが serve の役割で、
  プロジェクト分離は `?project=` で行う(`kagerou:project` タグ、§1)。
- `GET` 以外は `405`(`Allow: GET`)。エラーは `{"error": "..."}` を対応する 4xx/5xx で返す。
- 各リクエストは driver を都度読むため、返す状態は常にライブ。

## 7. Hooks に渡る環境変数

kagerou.yaml の hooks(`pre_up` / `post_up` / `pre_down` / `post_down`)は
`sh -c` で実行され、以下の環境変数を受け取る。以後の変更は追加のみ。

| 変数 | 中身 | 渡るフック |
|---|---|---|
| `KAGEROU_NAME` | 環境名 | すべて |
| `KAGEROU_URL` | 環境 URL(§5 の Output。無ければ未設定) | post_up / pre_down |
| `KAGEROU_OUTPUT_<KEY>` | 任意の CFN Output。キーは大文字化(`ApiUrl` → `KAGEROU_OUTPUT_APIURL`) | post_up / pre_down |
| `KAGEROU_ENVIRONMENT_JSON` | §3 の Environment JSON そのもの | post_up / pre_down |

**Outputs は「スタックが在る」フックにしか渡せない。** `post_down` の時点では
環境はもう無く、バケット名も URL も引けない。削除に絡む後始末で Outputs が
要るなら `pre_down` を使う。

失敗時の扱い:

| フック | 失敗したら |
|---|---|
| `pre_up` / `post_up` | up を失敗させる(post_up は「環境はあるが仕上がっていない」を green にしないため) |
| `pre_down` | **down を中止する**。受け持つのは「これをやらないと削除が失敗する」前処理なので、無視して進んでも分かりにくい CFN のエラーになるだけ |
| `post_down` | 記録して続行(環境自体は消えている) |

`pre_down` は**スタックが無ければ実行しない**。down は冪等であることを求められて
おり(close の再送や reap との競合で 2 回走る)、無い環境に対して後始末を
二重に走らせないため。

1 フックの実行上限は 10 分。

```yaml
# 例: 中身の入ったバケットを空にしてから消す(そうしないと DeleteStack が失敗する)
hooks:
  pre_down: |
    aws s3 rm "s3://$KAGEROU_OUTPUT_WEBBUCKETNAME" --recursive
```

```yaml
# 例: SPA の config.json を生成して静的成果物を配置する(3 層構成の定型)
hooks:
  post_up: |
    printf '{"apiBaseUrl":"%s"}' "$KAGEROU_OUTPUT_APIURL" > web/dist/config.json
    aws s3 sync web/dist/ "s3://$KAGEROU_OUTPUT_WEBBUCKETNAME/" --delete
```

## 8. IAM(デプロイロール / trust / boundary / execution)

「admin を付けてね」を避けるため、CI が使うロールに必要な IAM を
`kagerou iam-policy` が生成する。`--doc` で 4 つの文書を出す:

| `--doc` | 何 | 何にアタッチするか |
|---|---|---|
| `policy`(既定) | 最小権限ポリシー。**テンプレートの `Resources[].Type` から導出**(Lambda/API/VPC/S3/DynamoDB/SQS。対応を知らない型には警告して黙らない)。CI 自身がやることは `--with-*`(ECR/sashiki-ssm/CloudFront/Route53)と `--base-bucket`(共有 base への sync)で足す | デプロイロールの権限ポリシー |
| `trust` | GitHub Actions OIDC の信頼ポリシー(`--repo owner/name`) | デプロイロールの信頼ポリシー |
| `boundary` | 自己サーブ用の permissions boundary(`--regions`) | デプロイロール **と** それが作るロールの両方 |
| `execution` | preview の Lambda がランタイムで使う最小ポリシー(`--allow 'actions=resources'` で宣言、`--with-vpc` で ENI) | Lambda の実行(ランタイム)ロール |

`--check <file>` を付けると、生成する代わりに、実際に attach 済みのポリシー JSON を
読んで **生成される最小ポリシーとのアクション差分**を報告する(`policy` / `boundary` /
`execution` に対応。`trust` は対象外)。過剰権限(`extra`)があれば非 0 で終了するので、
CI の drift ガードに使える。判定はアクション集合の比較で、resource スコープの緩さは見ない
(Access Analyzer / Cloudsplaining の前段の軽い網)。

保証する性質(外部が依存してよい):

- **デプロイロールは OIDC の `sts:AssumeRoleWithWebIdentity` 前提**で、`sub` を
  `repo:<owner>/<name>:pull_request`(preview)と `repo:<owner>/<name>:ref:refs/heads/<branch>`
  (schedule の reap)だけに固定する。**fork の PR は sub が一致せず assume できない**
  (GitHub も fork PR の workflow に OIDC トークンを既定で渡さない)。`aud` は `sts.amazonaws.com` に固定。
- **権限ポリシーは `name_prefix`(= `kagerou:name` の接頭辞)で ARN をスコープ**する。
  CloudFormation スタック・Lambda・ロール・ロググループは `<name_prefix>*` に限定。
  ARN で絞れないアクション(`apigateway:*`、ENI 系、`cloudfront:CreateInvalidation` 等)は
  ワイルドカードが残る前提で、boundary が全体を封じる。
- **preview リソースは permissions boundary 配下で作られる**。boundary は上限
  (実効権限 = 権限ポリシー ∩ boundary)として次を強制する:
  - 許可した region 以外の regional アクションを Deny(`StringNotEqualsIfExists aws:RequestedRegion`。
    グローバルサービスは誤爆させない)
  - IAM 昇格(ユーザ/アクセスキー/グループ/ポリシー版/IdP の作成)を Deny
  - `name_prefix` 名前空間外のロールへの IAM 書込・`PassRole` を Deny
  - 新規ロール作成時に **同じ boundary の付与を必須化**(付けないと作れない=子ロールも同じ天井に)
  - 組織・アカウント・課金(`organizations:*` / `account:*` / `budgets:*` / `ce:*`)を Deny

これらは v0.x の間も後方互換(Deny の追加・スコープを狭める変更はしうるが、
出力の骨格と上記の性質は保つ)。`policy` はワイルドカードが残る箇所があるため、
アタッチ前にレビューすること(コマンドが stderr で注意する)。

## 9. preview base のデータ契約(SSM)

ベース(共有 CloudFront / 証明書 / DNS / バケット。§5・DESIGN §10)と環境の境界は
**SSM Parameter Store のキー契約**。ベースは作成時に次を書き、`kagerou init` は
これを読んで既存ベースを検出する(**us-east-1**。ベース自体と同じ置き場所):

```
/kagerou/base/<project>/domain         # 例 todo.example.com
/kagerou/base/<project>/bucket         # 成果物バケット名
/kagerou/base/<project>/distribution   # CloudFront distribution id
/kagerou/base/<project>/routing        # directory | spa(拡張子の無いパスの解決)

/kagerou/base/_shared/…                # 1 ドメインを複数アプリで共有するベース

# compute: ecs の共有 ALB ベース(deploy/alb-base.yaml。ALB は固定費があるので
# アカウント/VPC で 1 つを共有)。こちらは ALB と同じリージョンに書く
/kagerou/base/_shared-alb/domain
/kagerou/base/_shared-alb/listener_arn
/kagerou/base/_shared-alb/cluster
/kagerou/base/_shared-alb/vpc_id
/kagerou/base/_shared-alb/subnets              # タスクを置くサブネット(カンマ区切り)
/kagerou/base/_shared-alb/task_security_group
/kagerou/base/_shared-alb/assign_public_ip     # ENABLED | DISABLED
```

`compute: ecs` の環境テンプレートは `_shared-alb` のキーを CloudFormation の
動的参照(`{{resolve:ssm:…}}`)で読む — パラメータの手渡しは要らない。

**ネットワークはベース層の設定**。既定はパブリックサブネット + public IP
(NAT を立てない = 固定費ゼロ)。組織が public IP を禁止している、あるいは
NAT 経由 egress の本番乖離を縮めたい場合は、alb-base の `TaskSubnetIds` に
**既存の**プライベートサブネットを渡す — `subnets` がそちらを指し
`assign_public_ip` が `DISABLED` になる。kagerou が NAT や VPC エンドポイントを
作ることはない(それはチームのネットワークの持ち物)。per-env でトポロジを
作る・検証することもしない(preview の責務外)。

**共有ベース(`_shared`)**: 1 つの CloudFront / 証明書 / ドメインを全 project で
使う運用。**既定は per-app** で、共有はオプトイン(理由は DESIGN §11.3)。
解決順は **project 専用 → `_shared` → 旧 Exports**。

- URL は `<project>--<name>.<domain>`(例 `todo--pr-42.example.com`)。
  ワイルドカード証明書は **1 ラベルしか覆えない**ので、project を name と
  同じラベルに畳む。こうするとアプリを増やしても共有ベースを更新しなくてよい
  (`*.example.com` のまま)
- 成果物は `s3://<bucket>/<project>/<name>/…`(`static.prefix: "{project}/{name}"`)。
  project 境界がプレフィックスで分かれるので、`down` / `reap` は他 project の
  内容に触れない
- `_shared` は project 名として使えない文字(`_`)で始まるので、実在の
  project と衝突しない

### routing

拡張子の無いパスをどう解決するか。**ベースを作るときに決まり、環境ごとには
変えられない**(CloudFront Function に焼き込まれるため)。

| 値 | `/about` の解決先 | 向き |
|---|---|---|
| `directory`(既定) | `…/about/index.html` | 静的サイト生成器(`/about/index.html` を出すもの) |
| `spa` | `…/index.html` | クライアントルーター(`/about` の実体が無いもの) |

`…` はその環境のプレフィックス(per-app は `/<name>`、共有ベースは
`/<project>/<name>`)。SPA でも**そのプレフィックス配下の** index.html に写す —
ルートに落とすと共有ベースで別アプリの画面が出る。

SPA を `directory` のまま配ると、実体が無いので **403** になる(OAC 経由の S3 は
404 ではなく 403 を返す)。CloudFront のカスタムエラーページでは代替できない —
指せるのは固定パス 1 つで、環境ごとには切り替わらない。

利用者は `kagerou.yaml` の `static.routing` で宣言し、ベースのデプロイ時に渡る。
**後から変えるにはベースを deploy し直す**(環境の作り直しは不要)。

- **ベースの IaC はツール非依存**。同梱の CFN テンプレート(`deploy/preview-base.yaml`)も
  Terraform リファレンス(`examples/terraform/preview-base/`)も同じキーを書く。
  kagerou 本体はベースの IaC を実行しない — 依存するのはこのデータ契約だけ。
- 旧ベース(SSM を書かない頃)の CFN Exports(`kagerou-preview-base:*`)には
  fallback して検出する。同じ project に両方あれば **SSM が勝つ**。
- キーの追加は互換変更(このパス配下に将来 `sashiki_host` 等を足しうる)。
  既存キーの意味変更・削除はしない。
- ベースは `kagerou:managed` タグを持たない = **`list` / `reap` の対象外**
  (環境と違い TTL で消えない。所有はリポジトリのコラボレータ全員で、
  更新はテンプレート/モジュールの再適用)。

## 10. peer 連動(複数アプリの環境を名前で組む)

複数アプリ(= 複数の kagerou プロジェクト)が連動する系(例: A が SNS に publish、
B の SQS が受ける)では、**環境名が合成キー**になる。kagerou.yaml で相手を宣言する:

```yaml
peer:
  project: pub-demo   # 相手の kagerou:project
  fallback: main      # 同名 env が居ないとき繋ぐ env 名(省略時 main)
```

`up --name pr-42` のたびに kagerou が解決する:

1. 相手プロジェクトに **ready な同名 env(pr-42)** が居ればそれ
2. 居なければ **fallback の env(main)**
3. どちらも居なければ fallback 名のまま進め、URL 無しで**警告**する
   (相手が後から立つ運用を止めない)

解決結果はテンプレートが宣言していれば届く(URL と同じ「任意の口」方式):

| パラメータ | 中身 |
|---|---|
| `EnvPeerEnv` | 解決した相手の env 名(例 `pr-42` / `main`)。Topic 名等の規約参照に使う |
| `EnvPeerUrl` | その env の URL(相手の `kagerou:url` タグ / KagerouUrl Output 由来)。無ければ渡らない |

- 相手の実在確認は kagerou 自身のタグ走査(`kagerou:project` / `kagerou:name`)。
  SNS 等のリソース種別には依存しない — テンプレート側が `EnvPeerEnv` から
  規約名(例 `pub-demo-${EnvPeerEnv}-events`)を組む。
- ready でない相手(creating / failed)には繋がない。
- これにより「相手不在の Subscription が 4 分半 rollback して理由も出ない」
  (#99 の実測)が、up 前の即決 + 明示の警告に変わる。
