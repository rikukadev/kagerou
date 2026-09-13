# kagerou

PR や E2E で使い捨てるプレビュー環境を、**今あるコンテナ定義から**作る CLI。

このパッケージは [GitHub Releases](https://github.com/rikukadev/kagerou/releases) の
バイナリを取ってくる薄い wrapper です。中身は Go 製の単一バイナリで、
チェックサム(`checksums.txt`)を検証してから展開します。

## まず診断だけ

Go を入れていなくても、その場で試せます。**ファイルを 1 つも書かず、AWS も呼びません。**

```console
$ npx kagerou diagnose

kagerou diagnose  acme/shop

detected
  framework    next
  services     1
  port         3000

recommended
❯ lambda      単一コンテナ。アイドル $0 で、環境も 1 スタックで済む
  apigateway  複数サービスではないので ECS を持ち出す必要がない
  alb         同上。固定費もかかる

no files were written and no AWS calls were made.
```

`--auth` を付けると「社内限定にする」前提で判定し直します。

## セットアップ

```console
$ npx kagerou init
```

## 他の入れ方

```console
$ go install github.com/rikukadev/kagerou/cmd/kagerou@latest
```

対応は macOS / Linux の amd64・arm64。詳細は
[リポジトリの README](https://github.com/rikukadev/kagerou#readme) を参照してください。

MIT License
