#!/usr/bin/env bash
# install.sh の checksum 検証をローカルの偽 release で確かめる(#239)。
#
# 確かめたいのは 1 点: **一致しないものを絶対に install しないこと**。
# ここが崩れると、配信経路を取られたときに AWS 資格情報を持つプロセスとして
# 任意のバイナリが走る(生成 workflow は OIDC 設定の後に Action を呼ぶ)。
#
# curl は file:// を読めるので、KAGEROU_RELEASE_BASE をローカルに向ける。
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
INSTALL="$ROOT/action/install.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail() { echo "ACTION INSTALL TEST FAILED: $*" >&2; exit 1; }

VER=v9.9.9
ASSET="kagerou_9.9.9_linux_amd64.tar.gz"
REL="$WORK/release/$VER"
mkdir -p "$REL"

# 偽の kagerou を tar.gz に詰める
printf '#!/bin/sh\necho "kagerou 9.9.9+fake"\n' > "$WORK/kagerou"
chmod +x "$WORK/kagerou"
tar -czf "$REL/$ASSET" -C "$WORK" kagerou

sum=$(sha256sum "$REL/$ASSET" | awk '{print $1}')
export KAGEROU_RELEASE_BASE="file://$WORK/release"

echo "=== 一致していれば install する ==="
printf '%s  %s\n' "$sum" "$ASSET" > "$REL/checksums.txt"
bin="$WORK/bin-ok"
"$INSTALL" "$VER" amd64 "$bin" >/dev/null || fail "正しい checksum で install できない"
[ -x "$bin/kagerou" ] || fail "バイナリが置かれていない"
"$bin/kagerou" | grep -q "9.9.9+fake" || fail "置かれたバイナリが動かない"
echo "  OK"

echo "=== 不一致なら install しない ==="
printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$ASSET" > "$REL/checksums.txt"
bin="$WORK/bin-bad"
if out=$("$INSTALL" "$VER" amd64 "$bin" 2>&1); then
  fail "checksum 不一致で install が成功した"
fi
grep -q "checksum が一致しません" <<<"$out" || fail "理由が分からない出力: $out"
[ ! -e "$bin/kagerou" ] || fail "不一致なのにバイナリが置かれた"
echo "  OK"

echo "=== checksums.txt に項目が無ければ install しない(fail closed) ==="
printf '%s  %s\n' "$sum" "kagerou_9.9.9_linux_arm64.tar.gz" > "$REL/checksums.txt"
bin="$WORK/bin-missing"
if out=$("$INSTALL" "$VER" amd64 "$bin" 2>&1); then
  fail "項目が無いのに install が成功した(検証していない状態で通っている)"
fi
grep -q "checksums.txt に $ASSET がありません" <<<"$out" || fail "理由が分からない出力: $out"
[ ! -e "$bin/kagerou" ] || fail "項目が無いのにバイナリが置かれた"
echo "  OK"

echo "=== checksums.txt が空でも install しない ==="
: > "$REL/checksums.txt"
bin="$WORK/bin-empty"
if "$INSTALL" "$VER" amd64 "$bin" >/dev/null 2>&1; then
  fail "空の checksums.txt で install が成功した"
fi
[ ! -e "$bin/kagerou" ] || fail "空なのにバイナリが置かれた"
echo "  OK"

echo "=== アセットが遅れて現れても成功する(リリース直後、#260) ==="
REL2="$WORK/release2/$VER"
mkdir -p "$REL2"
cp "$REL/$ASSET" "$WORK/pending.tar.gz" 2>/dev/null || true
tar -czf "$WORK/pending.tar.gz" -C "$WORK" kagerou
sum2=$(sha256sum "$WORK/pending.tar.gz" | awk '{print $1}')
printf '%s  %s\n' "$sum2" "$ASSET" > "$REL2/checksums.txt"
# tar.gz は少し遅れて現れる(goreleaser のアップロード待ちを模す)
( sleep 2; cp "$WORK/pending.tar.gz" "$REL2/$ASSET" ) &
bin="$WORK/bin-late"
KAGEROU_RELEASE_BASE="file://$WORK/release2" KAGEROU_FETCH_RETRIES=5 KAGEROU_FETCH_DELAY=1 \
  "$INSTALL" "$VER" amd64 "$bin" >/dev/null 2>&1 || fail "遅れて現れたアセットで install できない"
wait
[ -x "$bin/kagerou" ] || fail "遅れて現れたのにバイナリが置かれていない"
echo "  OK"

echo "=== 無いものは有限回で失敗し、試行回数を出す ==="
bin="$WORK/bin-404"
if out=$(KAGEROU_RELEASE_BASE="file://$WORK/nothing" KAGEROU_FETCH_RETRIES=3 KAGEROU_FETCH_DELAY=0 \
    "$INSTALL" "$VER" amd64 "$bin" 2>&1); then
  fail "存在しないアセットで install が成功した"
fi
grep -q "3 回試行" <<<"$out" || fail "試行回数が出ていない: $out"
[ ! -e "$bin/kagerou" ] || fail "失敗したのにバイナリが置かれた"
echo "  OK"

echo "ACTION INSTALL TEST PASSED"
