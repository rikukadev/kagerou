#!/usr/bin/env bash
# Release の tar.gz を落として checksums.txt と突き合わせ、bin に置く。
#
#   install.sh <version> <arch> <bin-dir>
#
# **checksum を必ず検証する**(#239)。生成 workflow は AWS の OIDC 資格情報を
# 設定した**後**にこの Action を走らせるので、ここで落としたバイナリは AWS 資格情報を
# 持つプロセスとして起動する。配信経路を取られたら終わりになる。
# npm installer(npm/install.js)は前から検証していて、Action 経路だけ抜けていた。
#
# composite action の run: に直接書かず切り出しているのは、**不一致のときに
# 本当に止まるかをテストしたい**ため。KAGEROU_RELEASE_BASE を差し替えれば
# ローカルの偽 release で回せる。
set -euo pipefail

VERSION=${1:?usage: install.sh <version> <arch> <bin-dir>}
ARCH=${2:?usage: install.sh <version> <arch> <bin-dir>}
BIN=${3:?usage: install.sh <version> <arch> <bin-dir>}

# テストで差し替える。既定は GitHub Release。
BASE=${KAGEROU_RELEASE_BASE:-https://github.com/rikukadev/kagerou/releases/download}
LATEST_URL=${KAGEROU_LATEST_URL:-https://github.com/rikukadev/kagerou/releases/latest}

ver="$VERSION"
if [ "$ver" = "latest" ]; then
  # latest のリダイレクト先から実際の版を取る(資産名に版が入るため)
  ver=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$LATEST_URL" | sed 's#.*/tag/##')
  if [ -z "$ver" ]; then
    echo "could not resolve the latest release; set version explicitly or use ref" >&2
    exit 1
  fi
fi
case "$ver" in
  v*) ;;
  *) echo "could not resolve the latest release (no releases yet?): $ver" >&2
     echo "set version explicitly or use ref (go install)" >&2
     exit 1 ;;
esac

asset="kagerou_${ver#v}_linux_${ARCH}.tar.gz"
echo "installing kagerou ${ver} (${asset})"

# リリース直後はタグだけ先にでき、アセットは goreleaser が後から上げる。
# version: latest はタグを見るので、**アセットがまだ無い版に解決する**(#260)。
# リリースごとに 1〜2 分の窓が開き、その間に走った利用者は 404 で落ちていた。
# 待てば解決するので有限回リトライする。使い切ったら普通に失敗する。
RETRIES=${KAGEROU_FETCH_RETRIES:-8}
DELAY=${KAGEROU_FETCH_DELAY:-15}

fetch() {
  url=$1
  out=$2
  i=1
  while :; do
    if curl -fsSL "$url" -o "$out"; then
      return 0
    fi
    if [ "$i" -ge "$RETRIES" ]; then
      # 何回試したかを出す。黙って時間だけ延びると、ネットワークの問題と
      # 「アセットがまだ無い」を区別できない
      echo "取得できません($i 回試行): $url" >&2
      echo "リリース直後ならアセットの生成待ちかもしれません。少し待って再実行してください" >&2
      return 1
    fi
    echo "取得に失敗($i/$RETRIES)。${DELAY}s 待って再試行: $url" >&2
    sleep "$DELAY"
    i=$((i + 1))
  done
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fetch "${BASE}/${ver}/${asset}" "$tmp/$asset"
fetch "${BASE}/${ver}/checksums.txt" "$tmp/checksums.txt"

want=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")
# 項目が無いときも **fail closed**。空の want で比較すると「一致しなかった」ではなく
# 「検証していない」状態で通ってしまう。
if [ -z "$want" ]; then
  echo "checksums.txt に $asset がありません(検証できないので中止します)" >&2
  exit 1
fi
got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
if [ "$got" != "$want" ]; then
  echo "checksum が一致しません: $got != $want ($asset)" >&2
  exit 1
fi

tar -xzf "$tmp/$asset" -C "$tmp"
mkdir -p "$BIN"
install -m 755 "$tmp/kagerou" "$BIN/kagerou"
