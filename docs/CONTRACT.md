# kagerou 公開契約 v0.1

> 外部(Backstage プラグイン、CI、他ツール)から見た kagerou の約束事。
> 設計の背景は docs/DESIGN.md。

## この文書の読み方

見出しに **[Stable]** と **[Reference]** が付いている。**約束しているのは Stable
だけ**で、そこに依存するコードは v0.x の間、壊れないまま動き続ける。

| | 意味 | 変更のしかた |
|---|---|---|
| **[Stable]** | 外部が依存してよい。データの形と名前 | **追加のみ**(タグ / フィールド / キーを足す)。既存の意味とキー名は変えない |
| **[Reference]** | 現状の実装と既定値。**依存しないこと** | 予告なく変わる。変わったら CHANGELOG に書く |

この区別が要る理由は単純で、**全部に互換を約束すると直せなくなる**から。
生成される CloudFormation の中身、IAM の権限セット、リスナールールの採番、
認証の実装は、AWS 側の都合やバグ修正で動く。実際 v0.12 までに
リスナー優先度の式も Cloud Map のレコード型も IAM の権限も変えている。
それらを「契約」に含めていたら、直すたびに破壊的変更になっていた。

一方、**タグ・環境名・Environment JSON・`Env<Key>` 規約・`serve` の
エンドポイント・hooks の環境変数・SSM のキー**は、外から読み書きされる
データそのものなので、こちらは動かさない。

| 節 | 区分 |
|---|---|
| §1 タグスキーマ | Stable |
| §2 環境名の制約 | Stable |
| §3 Environment JSON | Stable |
| §4 env の届け方(`Env<Key>` 規約) | Stable |
| §4.5 driver: static のプレフィックス配置 | Stable |
| §5 URL の発行(`kagerou:url` / `url_template`) | Stable |
| §6 読み取り口(`kagerou serve`) | Stable |
| §7 Hooks に渡る環境変数 | Stable |
| §8 IAM(生成される権限の中身) | Reference |
| §9 SSM のキー契約(パスと意味) | Stable |
| §9 の各節(入口 / 認証 / MemorySize / 優先度 / routing) | Reference |
| §10 peer 連動 | Reference |

## 1. タグスキーマ [Stable]

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

## 2. 環境名の制約 [Stable]

```
^[a-z]([a-z0-9-]*[a-z0-9])?$   かつ 63 文字以下
```

小文字英字始まり・小文字英数字とハイフン・末尾ハイフン禁止。Route53 の
サブドメインラベル(63 文字)と S3 プレフィックスにそのまま使えるよう最初から狭くする。

## 3. Environment JSON [Stable]

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

## 4. env の届け方(stack driver)[Stable]

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

## 4.5 driver: static [Stable]

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

## 5. URL の発行 [Stable]

環境 URL の解決順:

1. **`kagerou:url` タグ** — kagerou.yaml の `url_template`(例
   `"https://{name}.preview.example.com"`)で**作成前に確定**した URL。
   テンプレートが `EnvKagerouUrl` パラメータを宣言していれば同じ値が届くので、
   CORS 許可元・OAuth コールバック等を循環参照なしに書ける
2. CFN Outputs のキー `KagerouUrl`
3. CFN Outputs のキー `PreviewUrl`

`url_template` の URL に実際にトラフィックを到達させる仕組み(共有 CloudFront +
サブドメイン等)は利用者側のインフラの責務(kagerou#32 で共通化を検討中)。

展開できるプレースホルダは `{name}` / `{project}` / **`{base_domain}`**。
`{base_domain}` はベースが SSM に書いたドメイン(§9)に up 時に解決される:

```yaml
url_template: "https://{name}.{base_domain}"
```

ドメインを kagerou.yaml に書き写すと、ベース側を変えたときに黙って古くなる
(`url_template` の値はそのまま `kagerou:url` タグになり、Output と突き合わされない
= **どこにも到達しない URL のまま緑になる**)。`{base_domain}` はその二重管理を消す。
**引けなければ up は失敗する** — 空文字で `https://pr-42..` を作らない。
`{name}` / `{project}` と同じく `env` の値と hooks 本文でも使える。

## 6. 読み取り口(`kagerou serve`)[Stable]

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

### 置き方(認証)

**この API は認証を持たない。** 一覧には環境名・URL に加えて `source`
(例 `github_pr://org/repo/42`)が入るので、素で公開すると**社内の PR 番号と
プレビュー URL が外から読める**。プレビュー本体を `auth:` で守っていても、
その一覧が素通しでは意味が薄い。

置き方は次のどちらかにする:

- **function URL を `AuthType: AWS_IAM`** にして、ポータル側が SigV4 で署名して叩く
  (Backstage のバックエンドから呼ぶならこれが素直)
- **VPC 内からのみ到達させる**(社内ネットワーク / VPC エンドポイント経由)

どちらも取れない場合に何が出るかは把握しておくこと: 環境名、URL、TTL、
`source`(リポジトリ名と PR 番号)、`owner`(IAM プリンシパル)。
**`AuthType: NONE` で置くのは、これらが公開されてよいときだけ。**

## 7. Hooks に渡る環境変数 [Stable]

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

## 8. IAM(デプロイロール / trust / boundary / execution)[Reference]

> **[Reference]** 生成される権限の集合は、AWS 側の都合と実運用で見つかる
> 不足で動く(実際 v0.12 までに何度も足している)。**生成された JSON を
> 写して固定するのではなく、`kagerou iam-policy` を都度実行すること**。

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

導出に使うテンプレートは `template` → **設定ファイルの隣の `template.yaml`** →
カレント直下の `template.yaml` の順に探す。1 つも見つからなければ生成は
`--with-*` だけに落ちるので、**`--check` は比較せずエラーにする**(基準そのものが
壊れた状態の drift 判定は意味を持たないため)。生成のみのときは警告に留めるが、
「フラグに落ちた」ではなく「このポリシーでは足りない」と伝える。

保証する性質(外部が依存してよい):

- **デプロイロールは OIDC の `sts:AssumeRoleWithWebIdentity` 前提**で、`sub` を
  `repo:<owner>/<name>:pull_request`(preview)と `repo:<owner>/<name>:ref:refs/heads/<branch>`
  (schedule の reap)だけに固定する。**fork の PR は sub が一致せず assume できない**
  (GitHub も fork PR の workflow に OIDC トークンを既定で渡さない)。`aud` は `sts.amazonaws.com` に固定。
  **immutable subject**(新しい org の既定)のリポジトリではトークンの `sub` が
  `repo:<owner>@<ownerID>/<name>@<repoID>:<context>` の形で来るため、同じ 2 つの
  context について **その形式も併記**する(context は増やさない)。数値 id は
  `--owner-id` / `--repo-id`、省略時は `gh` で引く。**引けなければ警告する** —
  古典形式だけの trust は `StringEquals` が一生一致せず、症状は
  `Not authorized to perform sts:AssumeRoleWithWebIdentity` だけで原因が遠い。
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

**これらは Reference** — 個々の Allow / Deny は足し引きされる。保つのは
上記の**性質**(昇格させない、名前空間の外を触らせない、子ロールに同じ天井)で、
アクションの集合そのものではない。`policy` はワイルドカードが残る箇所があるため、
アタッチ前にレビューすること(コマンドが stderr で注意する)。

## 9. preview base のデータ契約(SSM)[Stable]

ベース(共有 CloudFront / 証明書 / DNS / バケット。§5・DESIGN §10)と環境の境界は
**SSM Parameter Store のキー契約**。ベースは作成時に次を書き、`kagerou init` は
これを読んで既存ベースを検出する(**us-east-1**。ベース自体と同じ置き場所):

```
/kagerou/base/<project>/domain         # 例 todo.example.com
/kagerou/base/<project>/bucket         # 成果物バケット名
/kagerou/base/<project>/distribution   # CloudFront distribution id
/kagerou/base/<project>/routing        # directory | spa(拡張子の無いパスの解決)

/kagerou/base/_shared/…                # 1 ドメインを複数アプリで共有するベース

# ALB ベース(deploy/alb-base.yaml。入口 = alb のとき)。**per-app**。
# CloudFront ベースと違い、ALB と同じ **アプリのリージョン** に書く
/kagerou/base/<project>/alb_listener_arn
/kagerou/base/<project>/alb_cluster
/kagerou/base/<project>/alb_vpc_id
/kagerou/base/<project>/alb_subnets              # タスクを置くサブネット(カンマ区切り)
/kagerou/base/<project>/alb_task_security_group
/kagerou/base/<project>/alb_assign_public_ip     # ENABLED | DISABLED

# API Gateway ベース(deploy/apigw-base.yaml。compute: ecs × 入口 = apigateway)。
# ALB と同じコンテナを固定費ゼロで受けるための VPC Link / Cloud Map。
# 置き方は ALB ベースと同じ(per-app・アプリのリージョン)で、接頭辞だけ違う
/kagerou/base/<project>/apigw_vpc_link_id
/kagerou/base/<project>/apigw_namespace_id       # Cloud Map。環境ごとにサービスを足す
/kagerou/base/<project>/apigw_hosted_zone_id     # 環境ごとに A レコードを足すため
/kagerou/base/<project>/apigw_cluster
/kagerou/base/<project>/apigw_vpc_id
/kagerou/base/<project>/apigw_subnets
/kagerou/base/<project>/apigw_task_security_group
/kagerou/base/<project>/apigw_assign_public_ip   # ENABLED | DISABLED

# **regional** な ACM 証明書(*.<domain>)。ALB 専用ではないので alb_ を付けない。
# API Gateway のカスタムドメイン・AppSync もこれを使う。1 つの project では
# ALB ベースと API Gateway ベースは排他なので、書き手はそのどちらか片方
/kagerou/base/<project>/regional_certificate_arn
```

**証明書はリージョンで用途が分かれる。** CloudFront は us-east-1 の証明書しか
受けず、API Gateway のカスタムドメイン(regional)・ALB・AppSync は逆に
us-east-1 のものを受けない。preview base が作るのは前者、ALB / API Gateway
ベースが作るのは後者で、どちらも同じ `*.<domain>` を覆う(ワイルドカードは
1 ラベル分)。**regional な入口が要るとき、2 枚目を立てる必要はない**(#133)。

**`*_subnets` だけは動的参照で読めない**。動的参照は「文字列を書く場所」でしか
展開されず、`Fn::Split` の中では素の文字列のまま渡る(cfn-lint E1018)。そのため
環境テンプレートは `BaseSubnets` というリスト型の SSM パラメータで受け取る:

```yaml
  BaseSubnets:
    Type: AWS::SSM::Parameter::Value<List<AWS::EC2::Subnet::Id>>
    Default: /kagerou/base/<project>/alb_subnets   # apigateway 入口なら apigw_subnets
```

`Env*` ではないので kagerou は値を渡さず、`Default` の SSM パスから解決される。

`kagerou capacity` はこの `alb_listener_arn` を読み、**あと何面置けるか**を出す。
上限は Service Quotas ではなく `elbv2 DescribeAccountLimits` から取る(引き上げ済みの
実効値が返り、quota コードを覚えなくてよい)。読み取りだけで、届かなければ
判定不能として続行する — ゲートにはしない。

**ALB も per-app が既定**(CloudFront ベースと同じ)。チームごとに使うので 1 本では
足りなくなるうえ、リスナールール(既定 100)と証明書(25)の上限もある。
1 本を全アプリで共有したい場合は、alb-base の `Project` に `_shared-alb` を渡して
共有名前空間に書く(オプトイン。DESIGN §11.3 と同じ考え方)。

`domain` は入口によらず「このアプリのプレビュードメイン」の意味で共通。
ALB 固有の値だけ `alb_` を接頭辞にしてあるので、1 つのアプリが static(CloudFront)と
ecs/lambda(ALB)を併用しても衝突しない。

`compute: ecs` と、独自ドメインで配る `compute: lambda`(入口 = ALB)の
環境テンプレートは、`alb_` のキーを CloudFormation の動的参照
(`{{resolve:ssm:…}}`)で読む — パラメータの手渡しは要らない。

テンプレートの外(Go 側で URL を組み立てる `url_template` など)からは動的参照が
使えないので、`{base_domain}`(§5)が `domain` を up 時に引く。

**同じキーが 2 つのリージョンにありうる**のが要点。`domain` は入口によらず
同じ意味だが、置き場所はベースの種類で変わる(CloudFront ベース = us-east-1、
ALB ベース = アプリのリージョン)。解決側は入口を知らないので両方を見る:

1. `/kagerou/base/<project>/domain`(`region` → us-east-1 の順)
2. `/kagerou/base/_shared/domain`(同上)
3. `/kagerou/base/_shared-alb/domain`(`region`。ALB を 1 本に共有する運用)

どれも無ければ up は**失敗する**(探したキーを region つきで表示する)。
`kagerou init` のベース検出も同じ理由で 2 リージョンを走査する。
`{base_domain}` を使う設定では `kagerou iam-policy` が `ssm:GetParameter`
(`/kagerou/base/*`)を自動で足す — フラグにすると付け忘れて 403 になるため。

### 入口(entrypoint)[Reference]

> ここから先(入口 / 認証 / MemorySize / 優先度 / routing)は、**kagerou が今
> 何を作るか**の説明であって、外部が依存してよい形ではない。生成物の中身は
> バグ修正と AWS の仕様変更で動く。

環境の公開経路は **compute と entrypoint の組み合わせ**で決まる。
**既定は独自ドメイン**(`alb`)で、`apigateway` の中身は compute で変わる:

| compute × 入口 | 使うとき | 環境が作るもの |
|---|---|---|
| `lambda` × `alb`(既定) | 独自ドメインで配る | リスナールール + ターゲットグループ + invoke 権限。どれも無料 |
| `lambda` × `apigateway` | ドメインが取れないときのフォールバック | HTTP API(生の `execute-api` URL) |
| `ecs` × `alb` | 常駐プロセス。応答時間の上限が要らない | リスナールール + ターゲットグループ + ECS サービス |
| `ecs` × `apigateway` | 同じコンテナを**固定費ゼロ**で動かす | HTTP API + Cloud Map サービス + ECS サービス + カスタムドメイン |
| CloudFront(preview base) | `driver: static` | S3 プレフィックスのみ |

`ecs` × `apigateway` は ALB を持たないので月 $18 の固定費が消える代わりに、
リクエストが 30 秒で切れる。タスクの IP は起動のたびに変わるので、ターゲット
グループではなく **Cloud Map に SRV で登録**して VPC Link から名前で引く
(A レコードでは VPC Link 統合が繋がらない)。

`alb` では ALB 本体(固定費)は共有ベースの持ち物で、環境が足すのはホストヘッダの
ルール 1 本とターゲットグループだけ。lambda ターゲットは VPC の外のままなので、
アイドル $0 は変わらない(TTL も 72h のまま)。

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

### 認証(`auth:`)[Reference]

`kagerou.yaml` に `auth:` を書くと、プレビューがログインの後ろに入る。

```yaml
auth:
  provider: google-oidc
  domain: example.com   # 任意。通す組織のドメイン
  secret_arn: arn:aws:secretsmanager:ap-northeast-1:123456789012:secret:kagerou/shop-AbCdEf
```

**client_secret はここに書けない。** このファイルはコミットされるので、
置き場は Secrets Manager 固定にしてある(`{"client_id": …, "client_secret": …}`)。
`auth.client_secret` のようなキーは `kagerou validate` と設定の読み込みが
理由つきで弾く。

置き場を Secrets Manager にしたのは、SSM の `ssm-secure` 動的参照が
**11 のリソース型にしか対応しておらず、ELBv2 を含まない**ため。
`secretsmanager` は「すべてのリソースプロパティで使える」と明記がある。

入口ごとに実装が変わる:

| 入口 | 認証の担い手 | アプリ側のコード |
|---|---|---|
| `alb` | ALB の `authenticate-oidc` | 0 行(ただし下の検証は必須) |
| 静的配信 | CloudFront + Lambda@Edge | 0 行(関数が肩代わり) |

**`domain` を書いても、それだけでは組織外を締め出せない。** ALB には `hd` を
認可リクエストの追加パラメータとして渡すが、`hd` は**ヒントであって強制ではない**
(ログイン画面の既定が変わるだけで、他ドメインのアカウントでも認証は通る)。
最終的な関門はアプリ側で、ALB が付ける `x-amzn-oidc-data`(ALB が署名した JWT)の
`hd` / `email` クレームを検証すること。生成されるテンプレートは、その手順と
検証に使う環境変数 `KAGEROU_AUTH_DOMAIN` を書き出す。

デプロイロールには `secretsmanager:GetSecretValue` が要る(CloudFormation が
動的参照を**デプロイロールの資格情報で**解決するため)。`kagerou iam-policy` が
テンプレートから導いて、読むシークレットの ARN に絞って出す。

### 認証つきのベース(edge)[Reference]

`kagerou init --auth` は preview base の代わりに **edge base**
(`deploy/edge-base.yaml` + `deploy/edge-auth/index.mjs`)を生成する。
ALB の `authenticate-oidc` は S3 を守れないので、静的配信に認証をかけられるのは
CloudFront の手前(Lambda@Edge)だけ。

書く SSM キーは preview base と**同じ**(`/kagerou/base/<project>/…`)。
環境側から見た契約は変わらず、入口に認証が挟まるだけになる。

認証の設定は別 prefix に置き、**機密はテンプレートに載せない**。Lambda@Edge は
環境変数を持てないので、関数が cold start 時にここを読む(**us-east-1**):

```
/kagerou/edge-auth/<domain>/issuer          # 例 https://accounts.google.com
/kagerou/edge-auth/<domain>/client_id
/kagerou/edge-auth/<domain>/client_secret   # SecureString。人が置く
/kagerou/edge-auth/<domain>/session_secret  # SecureString。セッション Cookie の署名鍵
/kagerou/edge-auth/<domain>/allowed_domain  # 任意。未設定なら IdP で認証できる全員が入れる
/kagerou/edge-auth/<domain>/mode            # テンプレートが書く
/kagerou/edge-auth/<domain>/routing         # テンプレートが書く
```

`<project>` ではなく `<domain>` で引くのは、関数が Host ヘッダからしか自分の
素性を知れないため(Lambda@Edge に環境変数もタグも渡らない)。

IdP に登録する redirect_uri は **1 本だけ**:
`https://auth.<domain>/_kagerou/auth/callback`。環境のホスト名は毎回変わり、
ワイルドカードの redirect_uri は登録できないので、受け口を 1 か所に寄せて
元のホストと URI は署名付きの `state` で持ち回る。セッション Cookie は
`Domain=.<domain>` で発行するので、全環境で共有される
(= 環境ごとにログインし直さない)。

### Lambda の MemorySize / Timeout [Reference]

`kagerou init --memory 1024 --timeout 120` で生成物に焼き込む。検出はしない
(適正なメモリはリポジトリのファイルからは分からない)。既定は 512MB。

`template.yaml` は **force でも上書きしない**ので、生成後に直接編集してもよい。
フラグはあくまで「最初の 1 回を本番に合わせる」ためのもの。

**Timeout の既定は入口で変わる。**

| 入口 | 既定 | 上限 |
|---|---|---|
| 共有 ALB | 60 秒 | 実質なし(ALB のアイドルタイムアウトまで) |
| HTTP API(lambda / ecs どちらも) | 30 秒 | **応答 30 秒**。超えても Lambda は動き続けるが、呼び出し側には 504 が返る |

HTTP API 入口で 30 秒を超える `--timeout` を渡すと `init` が警告する。設定自体は
書き出すが、**効かない設定**であることを黙って隠さない。

### リスナールールの優先度 [Reference]

共有 ALB 入口では、環境が足すリスナールールに **1〜50000 の一意な優先度**が要る。
**kagerou が `up` のたびに空きを確保する**(#189)。テンプレートが優先度の
パラメータを宣言していて、その値が `--env` でも `--param` でも来ていないとき、
共有リスナーの既存ルールを読んで空きを取り、パラメータとして渡す。

| テンプレートの宣言 | env キー |
|---|---|
| `EnvRulePriority` | `RULE_PRIORITY`(単一サービス) |
| `EnvRulePriority<Service>` | `RULE_PRIORITY_<SERVICE>`(サービスごと) |

**明示が常に勝つ。** 値を渡せばそれが使われるので、PR 番号を渡す既存の運用も
そのまま動く。リスナーが分からない(SSM に `alb_listener_arn` が無い)ときは
確保せず、テンプレートの `Default` に任せる。

決め方には 2 つの性質がある。

- **同じ環境は同じあたりを掴む。** 開始位置を環境名から決めるので、続けて `up` しても
  優先度が動かない(差分の無い更新になる)。既存スタックの更新では確保し直さず、
  今の値をそのまま使う(`UsePreviousValue`)
- **別の環境とは散る。** 同時に走る `up` が同じ空きを掴みにくい。それでも衝突したら
  ALB が `PriorityInUse` を返すので、**失敗として観測できる**。kagerou は残骸を消して
  取り直し、1 回だけ作り直す

以前は PR 番号を優先度に写していた(#187)。番号から導く限り「5000 番台で上限を
超える」「連結で桁を食うのでサービスは 10 個まで」「番号が 4999 離れると衝突する」
が消えなかった。優先度に要るのは **そのリスナー上で、そのとき** 一意であることだけで、
未来永劫の一意性ではない。

### routing [Reference]

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

## 10. peer 連動(複数アプリの環境を名前で組む)[Reference]

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
