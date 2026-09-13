#!/usr/bin/env bash
# 複数サービス構成に固有の検証。run.sh(up → 共通検証 → down)から
# up の後に呼ばれる。
#
#   ./e2e/aws/multi/verify.sh <スタック名> <環境URL>
#
# 共通の run.sh が見るのは「環境 URL が 200 / タグ / down」まで。
# ここで見るのは **サービスが 3 つあるとき** にしか壊れないもの:
#   - A → B/C の依存が CFN 参照で結線されているか
#   - 3 サービス全部が SPA のオリジンを CORS で許しているか
#   - 環境 URL の 200 が「全サービス ready」を意味しないこと(既知の穴の記録)
set -euo pipefail

STACK=${1:?usage: verify.sh <stack> <url>}
URL=${2:?usage: verify.sh <stack> <url>}

fail() { echo "MULTI VERIFY FAILED: $*" >&2; exit 1; }
log() { echo; echo "=== $* ==="; }

out() { # Output を引く
  aws cloudformation describe-stacks --stack-name "$STACK" \
    --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue" --output text
}

A=$(out AUrl); B=$(out BUrl); C=$(out CUrl)
[ -n "$A" ] && [ -n "$B" ] && [ -n "$C" ] || fail "A/B/C の Output が揃っていない: a=$A b=$B c=$C"
echo "  a=$A"
echo "  b=$B"
echo "  c=$C"

log "config.json が 3 本とも実 URL を指す"
cfg=$(curl -sSf "$URL/config.json") || fail "config.json が取れない"
echo "  $cfg"
for pair in "a=$A" "b=$B" "c=$C"; do
  k=${pair%%=*}; v=${pair#*=}
  got=$(python3 -c "import json,sys;print(json.load(sys.stdin)[\"$k\"])" <<<"$cfg")
  [ "$got" = "$v" ] || fail "config.json の $k=$got が Output($v)と違う"
done

log "A → B/C の依存が結線されている"
# A の応答に downstream の実結果が入る。ここが空や error なら、
# B_URL / C_URL の CFN 参照が壊れている(作るまで分からない値の受け渡し)。
abody=$(curl -sSf "$A") || fail "A に到達できない"
echo "  $abody"
python3 - "$abody" <<'PY'
import json, sys
d = json.loads(sys.argv[1])
ds = {x["name"]: x for x in d.get("downstream", [])}
for name in ("b", "c"):
    x = ds.get(name)
    assert x, f"downstream に {name} が無い: {d}"
    assert x.get("ok"), f"A から {name} に届いていない: {x}"
    assert x["body"]["service"] == name, f"{name} の応答が別サービス: {x}"
print("  A は B と C の両方に届いている")
PY

log "3 サービス全部が SPA のオリジンを CORS で許す"
# サービスを増やすと「1 つだけ許可を忘れる」が起きやすい。全数を回す。
# `*` は合格にしない。何も設定しなくても通ってしまい検証にならない上、
# credentials とも併用できない。**SPA のオリジンそのもの**を要求する。
for pair in "a=$A" "b=$B" "c=$C"; do
  k=${pair%%=*}; v=${pair#*=}
  allow=$(curl -sSf -D - -o /dev/null -H "Origin: $URL" "$v" | tr -d '\r' \
    | awk -F': ' 'tolower($1)=="access-control-allow-origin"{print $2}')
  [ "$allow" = "$URL" ] || fail "$k の CORS 許可元が SPA のオリジンでない: '$allow' (want '$URL')"
done
echo "  a / b / c すべて SPA のオリジンを許可"

log "環境名が 3 サービスに届いている"
for pair in "a=$A" "b=$B" "c=$C"; do
  k=${pair%%=*}; v=${pair#*=}
  env=$(curl -sSf "$v" | python3 -c 'import json,sys;print(json.load(sys.stdin)["env"])')
  [ -n "$env" ] || fail "$k に KAGEROU_ENV が届いていない"
done
echo "  OK"

# 既知の穴の記録: kagerou の ready は環境 URL 1 本しか見ない。
# ここまでの検証を CI 側でやっているのは、その穴を埋めるため。
# readiness が複数点を見られるようになったら、この節ごと kagerou.yaml に移す。
log "MULTI VERIFY PASSED"
