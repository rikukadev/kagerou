# kagerou

Ephemeral environments on AWS.

Named after 陽炎 — a heat shimmer — and 蜉蝣, the mayfly that lives for a day.
Environments that appear, flicker, and are gone.

> Status: alpha. The core works — `up` / `down` / `url` / `list` / `reap` on the
> `stack` driver (one CloudFormation stack per environment), plus a composite
> action for CI. Not yet proven on a long-running real deployment.
> The first adapter is GitHub pull requests; the core is not PR-specific.

Sibling project: [sashiki](https://github.com/rikukadev/sashiki) — disposable database branches.

## 使ってみる

```bash
# CI から(composite action)
- uses: rikukadev/kagerou/action@v1
  with:
    name: pr-${{ github.event.pull_request.number }}
    env: |
      DB_HOST=10.0.1.5
      DB_USER=dev@pr-42

# 手元から
kagerou up --name pr-42
kagerou url --name pr-42
kagerou down --name pr-42
```

必要なのは `kagerou.yaml` と、[CONTRACT](docs/CONTRACT.md) に沿った
CloudFormation テンプレート(`Env<Key>` パラメータと `KagerouUrl` Output)の 2 つ。

動く例:

| リポジトリ | 構成 |
|---|---|
| [kagerou-ssr-demo](https://github.com/rikukadev/kagerou-ssr-demo) | React Router(SSR)を 1 Lambda で受ける最小構成 |
| [kagerou-3tier-demo](https://github.com/rikukadev/kagerou-3tier-demo) | React SPA / Go API / RDB を別オリジンで分離した構成 |
