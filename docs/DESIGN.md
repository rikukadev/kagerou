# kagerou 設計ドラフト v0

> 議論用の叩き台。sashiki の SPEC v2 のような確定仕様ではなく、
> 「決めたこと」「選択肢と推し」「未決」を分けて書く。
> これが固まるまでコードは書かない。

## 1. 位置づけとスコープ

**kagerou は AWS 上に「現れて、揺らめいて、消える」一時環境を作る道具**。
第一のユースケースは GitHub PR ごとのプレビュー環境だが、コアは PR を知らない。
コアが知っているのは「名前つきの環境と、その寿命」だけである。

sashiki との関係 — **sashiki はオプショナルな隣人であり、依存ではない**:

- kagerou は単体で完結する。DB を持たないアプリ、RDS の共有 dev DB、
  SQLite 同梱アプリ、どれでも環境は作れる
- sashiki を併用すると「PR を開くと本物データ入りのフルスタック環境が生える」に
  格上げされる。が、それは数ある `--env` の注入元の一つにすぎない
- kagerou のコードは sashiki を import しない。名前を知っているのはドキュメントとデモだけ

原型は sashiki-todo-demo の `preview.yml`(約 260 行の workflow)。あの YAML に
書き散らかされているものを、再利用可能な形に抽出するのが kagerou である。

### 非スコープ(v0.x では やらない)

- AWS 以外のクラウド
- 本番環境の管理(kagerou が作るものはすべて「消えてよい」環境)
- アプリのビルド(ビルドは CI の仕事。kagerou は成果物を受け取って配置する)
- DB のブランチ(sashiki の仕事。kagerou は接続情報を受け渡すだけ)

## 2. 用語とドメインモデル

| 用語 | 意味 |
|---|---|
| Environment | 名前を持つ一時環境。`name`(例: `pr-42`)、`driver`、`ttl`、`url`、`state` を持つ |
| Driver | 環境の実体を作る戦略。provision / update / destroy / url の 4 操作を実装する |
| Adapter | 外部イベントを環境のライフサイクルに変換する層。第一弾は GitHub PR |
| Publisher | 環境の URL・接続情報を人間に届ける層。第一弾は PR コメント(冪等更新) |
| Reaper | 寿命(TTL)切れ・孤児環境を回収する常設の掃除役 |

デモの「3 モード」は Driver の 3 実装として一般化する:

| デモのモード | kagerou Driver(仮名) | 特徴 |
|---|---|---|
| app(PR ごとに SAM スタック) | `stack` | 分離が完全。作成に数分 |
| app-shared(常設 Lambda + サブドメイン) | `shared` | 環境作成は URL 発行だけ。数秒 |
| app-local(runner ホストの systemd) | `local` | AWS 不要。デモ・開発用 |
| (デモ外)静的配信で完結するもの | `static` | ビルド成果物を S3+CloudFront に配置して URL を配るだけ |

driver 選びの軸は「フロントエンドかどうか」**ではなく**、
**「リクエスト処理に compute が要るか」**である:

1. **compute が要る** — API サーバーはもちろん、**SSR フロントエンド**
   (Next.js SSR / Remix / Astro server)もこちら。サーバーとして動くので
   実行時 env(API の URL、シークレット等)を持つ。`stack` / `shared` の世界
2. **静的配信で完結する** — SSG や CSR の SPA。env はビルド時に焼き込まれる
   (Vite の `VITE_*` 等)ので実行時注入の概念がない。`static` の世界
   (Vercel / Amplify の PR プレビューと同型)。環境 = S3 プレフィックスなので
   作成は秒・コストほぼゼロ

SSR の本番構成は compute + 静的アセット + CDN の複合(open-next 型)になりがち
だが、**プレビュー環境では「全部 Lambda で受ける」に倒す**。LWA(Lambda Web
Adapter)で SSR サーバーをそのまま包めば静的アセットも Lambda が返し、環境 =
スタック 1 個の単純さが保てる(sashiki-todo-demo がまさにこの形)。性能は本番構成に
劣るが確認用途には無関係。つまりハイブリッドは driver ではなく **stack テンプレート
の書き方の問題**に倒し、kagerou はテンプレートの中身を知らないという境界を守る。

### 2.5 AWS のカテゴリで見た組み合わせ

環境の構成要素を AWS の分類そのままに 2 軸で見ると、責務の境界が一番はっきりする:

| 軸 | 一時化の手段(例) | 誰の責務か |
|---|---|---|
| **Compute / 配信** | Lambda+LWA(`stack`)、常設 Lambda+振り分け(`shared`)、S3+CF(`static`)、将来 ECS Fargate | **kagerou の driver** |
| **データ** | sashiki ブランチ(ZFS/FSx)、共有 RDS、Aurora fast clone、DynamoDB のテーブル/プレフィックス分離 | **backing service**(kagerou は env で繋ぐだけ) |

- **driver はデータ軸を知らず、backing service は compute 軸を知らない**。
  この直交性のおかげで「Lambda × sashiki」「Fargate × Aurora clone」…と
  組み合わせが増えても、kagerou 側の実装は driver の数だけで済む
- データ軸の一時化はサービスごとに流儀が違う(sashiki はブランチ、Aurora は
  fast clone、DynamoDB はテーブル分け)。そこに kagerou は踏み込まない —
  名前(`{name}`)と TTL の規約を提供して繋ぐだけ

## 3. アーキテクチャの選択肢

### 案 A: ステートレス CLI + AWS を真実の源にする(推し)

`kagerou` は CLI。CI(GitHub Actions)から呼ばれ、その場で AWS を操作して終了する。
**専用の状態ストアを持たない**。環境の一覧・寿命は AWS リソースのタグ
(`kagerou:name`, `kagerou:expires-at` 等)と CloudFormation スタックから復元する。

- Reaper も CLI(`kagerou reap`)。EventBridge Scheduler + CodeBuild/Lambda、
  または GitHub Actions の cron で定期実行する
- 長所: 常駐プロセスなし。片付け忘れがタグから機械的に見つかる。導入が
  「workflow に 2 ステップ足す」で済む
- 短所: 同時実行制御が CFN 頼み。ローカル driver だけ毛色が違う(タグがない)

### 案 B: デーモン型(sashiki 相似形)

`kagerould` が REST API を持ち、Action は薄いクライアント。sashiki と完全に対称。

- 長所: 冪等 API・キュー・ロックを自前で持てる。local driver が自然
- 短所: 常駐先が必要。「環境は AWS にあるのに状態は別の場所」という二重管理

### 案 C: GitHub App + Lambda コントローラ

webhook 駆動で runner すら不要。pre-alpha には運用が重すぎるので v0.x では見送り。

**推しは A**。理由: kagerou の管理対象(CFN スタック・Route53 レコード・Lambda)は
sashiki の管理対象(ZFS・mysqld プロセス)と違って**それ自体が API で列挙できる**ので、
状態の二重持ちは負債にしかならない。local driver は A の例外として最小限に留める
(デモ用と割り切る)。B への移行は「A の CLI をそのままデーモンで包む」形で後からできる。

## 4. 環境のライフサイクルと TTL

kagerou の署名的な性質: **すべての環境は生まれた瞬間から死ぬ予定がある**。

- `kagerou up --name pr-42 --ttl 72h` — TTL は必須(デフォルト 72h)。
  「無期限」を選ぶには明示的な `--ttl none` が要る
- `up` は冪等。既存なら update(TTL は延長される = touch)
- `kagerou down --name pr-42` — 冪等。存在しなければ成功
- Reaper は 2 種類の死を扱う:
  1. **TTL 切れ** — `expires-at` を過ぎた環境を destroy
  2. **孤児** — close イベントの取りこぼし(workflow 失敗・webhook 欠落)で
     残った環境。PR が closed なのに環境が居る、を adapter に問い合わせて検出

デモで実際に起きた問題への回答でもある: close イベントが失われると環境が
リークする。イベント駆動だけに頼らず、**必ず Reaper が最後の網になる**。

## 5. 外部リソースの繋ぎ方(sashiki 連携もここに含まれる)

ルールは 1 行で言える: **kagerou は DB やキューを作らない。接続情報を環境変数で
環境に配るだけ。** コードに sashiki 専用の処理は存在しないし、`--sashiki` のような
固有サービス名のフラグも作らない。

ただし作者の主用途は sashiki 併用なので、**併用時の体験は一級市民**として設計する。
それを可能にするのが「接続情報は名前から導出できる」という sashiki 側の性質:

- sashiki のプロキシは固定エンドポイント(`:3306`)+ **username routing**
  (`dev@pr-42` で接続するとブランチ pr-42 に振り分け)を持つ
- つまり host / port は固定値、user は `dev@{name}`。**動的な outputs の受け渡しは
  そもそも不要**で、必要なのは名前の規約だけ

```yaml
# kagerou.yaml — sashiki 併用の推奨構成。sashiki は設定の文字列にしか現れない
env:
  DB_HOST: db.preview.internal   # sashiki プロキシの固定エンドポイント
  DB_PORT: "3306"
  DB_USER: dev@{name}            # {name} は環境名で展開
hooks:
  pre_up: sashiki create {name}  # 汎用フック。冪等なので毎回呼んでよい
```

```bash
kagerou up --name pr-42    # DB ブランチ + アプリ環境がこれ 1 コマンドで生える
```

**片付けはオーケストレーションしない**。sashiki のブランチは sashiki 自身の TTL、
kagerou の環境は kagerou の TTL で、それぞれ勝手に蒸発する(両者の TTL を揃える
のが規約)。`post_down` フックを書けば即時削除もできるが、必須ではない —
reaper の取りこぼし回収も各自の TTL が担うので、連携コードはゼロで済む。

kagerou から見れば、sashiki のブランチも、共有の RDS も、SQLite 同梱も、
すべて「env の出どころが違うだけ」で同じもの。hooks も `pre_up` に何を書くかが
違うだけの汎用機構である(この整理は 12-Factor の backing services そのまま)。

検討済みの代替案: sashiki 側の **lazy create**(プロキシが `dev@pr-42` の初回接続で
ブランチを自動作成)。最も魔法的だが、FSx バックエンドは create に 60〜80 秒かかり
初回接続がタイムアウトする・タイポで資源が生える、の 2 点から v0.x では採らない。
ローカル/EBS 限定のオプトイン機能として sashiki 側の issue 候補に留める。

## 5.5 CLI 素描(案 A 前提)

リポジトリに `kagerou.yaml` を 1 つ置き、CI からの呼び出しは可変部分だけにする
(samconfig.toml と同じ発想):

```yaml
# kagerou.yaml
project: todo                  # kagerou:project タグ(Backstage 連携の紐付けキー)
driver: stack
template: template.yaml        # stack driver: packaged 済み SAM/CFN テンプレート
region: ap-northeast-1
name_prefix: todo-             # スタック名は todo-pr-42 になる
ttl: 72h                       # デフォルト寿命
tags:
  team: rikuka                 # 全リソースに付く追加タグ
env:                           # 全環境共通の env。値の {name} は環境名で展開
  DB_USER: dev@{name}
hooks:                         # ライフサイクルフック(冪等前提)
  pre_up: sashiki create {name}
```

```bash
# 作成/更新(冪等。既存なら update + TTL 延長)
kagerou up --name pr-42 \
  --env DB_HOST=10.0.1.5 --env DB_USER=dev@pr-42 \   # アプリに届ける環境変数
  --param SubnetIds=subnet-xxx \                      # テンプレートのパラメータ
  --wait --output json                                # {"name":"pr-42","url":"https://..."}

kagerou down --name pr-42          # 冪等。無ければ成功
kagerou list --output json         # タグから復元した一覧(name, url, expires-at)
kagerou url --name pr-42           # URL だけ欲しい時(コメント投稿用)
kagerou reap --dry-run             # TTL 切れ・孤児の検出と削除
```

- `--env`(アプリの語彙)と `--param`(インフラの語彙 = CFN parameter-overrides)を
  分ける。sashiki 等の outputs は `--env` に流す
- `list` / `reap` は状態ストアなしで動く: `kagerou:name` / `kagerou:expires-at`
  タグを Resource Groups Tagging API で走査して復元する(案 A の肝)

## 6. セキュリティ(デモから引き継ぐ制約)

- fork PR では動かさない(`head.repo == repository` の判定は adapter の責務)
- 実行者の許可リスト(デモの `PREVIEW_ALLOWED_ACTORS` 相当)
- AWS 権限は OIDC の AssumeRole 前提。長期キーは扱わない
- 環境ごとの分離は driver 依存(`stack` は完全分離、`shared` は同居)であることを
  ドキュメントに明記する

## 7. MVP(v0.1)の範囲

1. CLI: `up` / `down` / `list` / `reap`(Go、sashiki と同じツールチェーン)
2. Driver: `stack`(SAM/CFN テンプレートを受け取ってスタック管理)のみ
3. Adapter: GitHub Actions 用の composite action(`rikukadev/kagerou/action`)
4. Publisher: PR コメント冪等更新(デモの marker 方式をそのまま昇華)
5. Reaper: `kagerou reap` + サンプル cron workflow

`static`(静的配信専用)/ `shared` / `local` driver、Route53 サブドメイン管理は
v0.2 以降。優先順は需要次第だが、`static` は実装が最も薄く恩恵が広いので
v0.2 の筆頭候補。SSR フロントは v0.1 の `stack` + LWA テンプレートで既に賄える。

## 8. 未決事項の解消状況

設計レビュー(2026-09-12、Backstage 連携観点)で大半を解消。外部が依存してよい
契約は **docs/CONTRACT.md** に分離した(タグスキーマ / Environment JSON /
名前制約 / env の届け方 / URL 規約)。

- [x] タグスキーマ → CONTRACT §1 で凍結。`kagerou:managed / project / driver / source / version` を追加。スタック外リソースを持つ driver は個別に付ける責務
- [x] `--env` の届け方 → テンプレートが宣言する **`Env<Key>` パラメータへ流す**(CONTRACT §4)。宣言が受け取り口なので「テンプレートの中身を知らない」境界を守れる。(b) デプロイ後注入案は Lambda 限定のため撤回
- [x] テンプレートは user 持ち(packaged 済み)。kagerou は生成しない
- [x] TTL 延長 → up ごとに touch。ただし**初回作成から 30 日を上限**にして無限延長を防ぐ(実装は #6)
- [x] 走査コスト → `kagerou:managed=true` で絞れば Resource Groups Tagging API で十分(数百スタックでも 1〜2 ページ)
- [x] hooks → `pre_up` 失敗は up を止める / `post_down` 失敗は記録して続行 / **reap も post_down を呼ぶ**(呼ばないと sashiki 側に孤児が残る経路になる)(実装は #7)
- [x] URL → CFN Output 規約 `KagerouUrl` > `PreviewUrl`(CONTRACT §5)
- [x] リポジトリ構成 / CI → #11 で確定(sashiki と同じツールチェーン)
- [x] 環境名 → 小文字限定・63 文字・末尾ハイフン禁止に**最初から狭める**(Route53 ラベル / S3 プレフィックスを後で使うため。広げるのは安全、狭めるのは破壊)
- [x] 同名 up の同時実行 → `*_IN_PROGRESS` なら安定を待ってから自分の変更を重ねる
- [ ] reap の孤児検出: **既定は TTL のみ**。adapter への問い合わせは `--check-source github` のオプトイン(Reaper を Lambda 化しても GitHub トークンが要らないままにする)
- [ ] `kagerou serve`(読み取り専用 HTTP、Lambda function URL)— Backstage プラグインの読み取り口。書き込みは workflow_dispatch 経由(CONTRACT §6)。v0.2

## 9. テスト戦略

3 層に分ける([rikuka.dev のエミュレータ比較](https://rikuka.dev/blog/aws-local-emulator-comparison)の
結論に従う。LocalStack 無料版は 2026-03 に廃止済みのため候補にしない):

1. **ユニット** — config / 名前検証 / TTL 解釈など純 Go の層。`make test`
2. **AWS 結合(ローカル/CI)** — stack driver・タグ走査・reap を **moto server**
   (無料、`docker run motoserver/moto`)に対して検証。kagerou 側は AWS SDK の
   `AWS_ENDPOINT_URL` を尊重するだけで、エミュレータ専用コードは書かない。
   `make test-aws` + CI ジョブ(#13)
3. **実 AWS e2e** — moto がカバーしない領域(SAM 変換・実 URL 疎通)。
   常設 CI にはせず手動 or nightly。sashiki-todo-demo の kagerou 化(#9)が兼ねる

デプロイ系の機能は「実装と同じ PR に moto 結合テストが付いてくる」を規約にする。

## 10. 共有配信基盤と static driver(#32 / #10)

> 状態: レビュー待ちの設計案(2026-09-12)。実装前にここで固める。

### 10.1 まず切り分け: #32 は driver 追加なしで解ける

#32 の主訴は「3 層デモの SPA が http(S3 website)のまま」で、これは
**配信基盤の不在**の問題。static driver(#10)が無くても、共有 CloudFront が
あれば現行の stack driver + post_up(base バケットへ sync)で https 化できる。

- **#32 = 共有配信基盤(preview base)を出す** — 先にやる
- **#10 = static driver** — 「フロントのみ」構成向けの省コスト経路。base の上に載る

### 10.2 preview base: 1 回だけ作る共有スタック

環境ごとに CloudFront を作ると 5〜10 分かかり ephemeral に合わない。
**ディストリビューション・証明書・DNS は共有し、環境は S3 プレフィックスだけ**にする。

構成(kagerou が `deploy/preview-base.yaml` として同梱、`kagerou init` の
setup から案内):

- S3 バケット(非公開、OAC 経由のみ)
- CloudFront: 代替ドメイン `*.preview.example.com`、ACM 証明書
- CloudFront Function(viewer-request): Host の最初のラベルを origin path に写す
  (`pr-42.preview.example.com` → `/pr-42/…`。sashiki-demo の fwd-host と同型)
- Route53: `*.preview.example.com` → CloudFront の alias
- **リージョンは us-east-1 固定**(CloudFront 用 ACM の制約。クロスリージョン
  参照を CFN で頑張るより、base スタックごと us-east-1 に置く方が単純)

前提: Route53 のホストゾーンは利用者が持っている(ドメイン所有は kagerou の
外)。base の Outputs(バケット名・ドメイン)を kagerou.yaml に写して使う。

### 10.3 3 層構成の最終形(#32 の解)

SPA の配置先を S3 website から base バケットのプレフィックスに変えるだけ:

```yaml
url_template: "https://{name}.preview.example.com"
hooks:
  post_up: aws s3 sync web/dist "s3://$PREVIEW_BASE_BUCKET/{name}/" --delete
```

https になり、Cookie の Secure も使え、URL に PR 番号が出る。driver は stack のまま。

### 10.4 static driver(#10): フロントのみ構成の省コスト経路

「compute が要らない」アプリ(SSG / CSR)向け。**不変条件「環境 = CFN スタック
1 個」は static でも維持する**:

- up = メタデータ専用の極小スタック作成(タグの担い手。実リソースほぼ無し、数秒)
  + `static.dist` を `s3://<base バケット>/{name}/` へ sync
- down = メタスタック削除 + プレフィックスのオブジェクト削除
- これにより list / reap / Environment JSON / iam-policy が**全 driver 共通のまま**

```yaml
driver: static
static:
  dist: dist
  bucket: <preview-base の Output>
url_template: "https://{name}.preview.example.com"   # static では必須
```

### 10.5 未決(実装前に決める)

- [ ] キャッシュ戦略: 環境更新のたびに CloudFront invalidation を打つか、
      キャッシュ TTL を短くして不要に倒すか(アセットがハッシュ名なら後者で足りるはず)
- [ ] SPA fallback(404 → index.html)を CF Function でやるか custom error response か
      (custom error はディストリビューション全体に効くので、プレフィックス単位なら Function 側)
- [ ] base スタックの管理コマンド(`kagerou base up` を作るか、テンプレート同梱 +
      README 手順に留めるか)
- [ ] iam-policy への base 用モジュール(--with-preview-base)
