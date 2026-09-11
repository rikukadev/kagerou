# kagerou 設計ドラフト v0

> 議論用の叩き台。sashiki の SPEC v2 のような確定仕様ではなく、
> 「決めたこと」「選択肢と推し」「未決」を分けて書く。
> これが固まるまでコードは書かない。

## 1. 位置づけとスコープ

**kagerou は AWS 上に「現れて、揺らめいて、消える」一時環境を作る道具**。
第一のユースケースは GitHub PR ごとのプレビュー環境だが、コアは PR を知らない。
コアが知っているのは「名前つきの環境と、その寿命」だけである。

sashiki との関係:

- sashiki = データの株(DB ブランチ)を秒で用意する
- kagerou = その上で動くアプリ環境(compute + URL)を用意し、寿命が来たら消す
- 両者は独立に使える。組み合わせると「PR を開くと本物データ入りのフルスタック環境が生える」

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

## 5. sashiki 連携

kagerou は DB を作らない。連携は環境変数の受け渡しに限定する:

```
kagerou up --name pr-42 \
  --env DB_HOST=... --env DB_USER=dev@pr-42 ...   # sashiki action の outputs をそのまま渡す
```

将来 `--sashiki` フラグで「同名の DB ブランチを作って結線」まで面倒を見る案は
あるが、v0.x では**やらない**(結合を疎に保つ。デモの workflow で十分示せる)。

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

`shared` / `local` driver、Route53 サブドメイン管理は v0.2 以降。

## 8. 未決事項(実装前に決める)

- [ ] タグスキーマ(`kagerou:*`)の確定。CFN スタック以外のリソース(Route53 等)への付け方
- [ ] `stack` driver の入力: SAM テンプレートを user 持ちにするか、kagerou がテンプレートを生成するか
- [ ] TTL 延長のポリシー: PR 更新(synchronize)ごとに touch でよいか
- [ ] `list` / reaper の走査コスト(タグ検索は Resource Groups Tagging API で足りるか)
- [ ] URL の発行方法: CFN Output 前提でよいか(shared driver を見据えると抽象化が要る)
- [ ] リポジトリ構成(sashiki の 25 章に倣うか)と CI の初期セット
