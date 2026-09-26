#!/usr/bin/env bash
# E2E のデプロイロールに、リポジトリのポリシーを適用する。
#
#   ./scripts/apply-e2e-policy.sh [--dry-run]
#
# **なぜこれが要るか。** iam-policy-drift が比べているのは
# 「e2e/aws/ci-policy.json ↔ 生成器」で、**実際に attach されているロール**は
# 誰も見ていなかった。生成器を変えて JSON を更新すると CI は全部緑になるが、
# 適用を忘れると次の e2e-aws-* が 6 本まとめて AccessDenied で落ちる。
# 実際に 2 回踏んだ(tag:GetResources / cloudformation:DescribeStacks)うえ、
# 気づかないまま 2 件のずれが溜まっていた(#243)。
#
# 適用するもの:
#   kge2e-deploy    e2e/aws/ci-policy.json        生成器と一致させる本体
#   kge2e-selfcheck e2e/aws/selfcheck-policy.json CI が実ロールを読むための権限
#
# selfcheck を別ポリシーにしているのは、比較対象の kge2e-deploy を
# 生成器の出力と **1 バイトも違わない** 状態に保つため。同じ文書に混ぜると
# drift チェックが over-permission として落ちる。
set -euo pipefail

ROLE=${ROLE:-kagerou-e2e-github-actions}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
DRY=false
[ "${1:-}" = "--dry-run" ] && DRY=true

# IAM の policyDocument は印字可能な ASCII しか受け付けない。生成物には
# 日本語コメントが入らない作りだが、手で足したときに気づけるよう確かめる。
check_ascii() {
  if LC_ALL=C grep -q '[^ -~]' "$1"; then
    echo "$1 に非 ASCII 文字がある(IAM が ValidationError で落ちる)" >&2
    exit 1
  fi
}

# 適用前に **内容で** 差分を出す。Sid の集合だけ比べると、同名 statement の
# 中身が違うのを見落とす(実際に見落として reap を落とした)。
diff_live() {
  local name=$1 file=$2 live
  live=$(mktemp)
  if ! aws iam get-role-policy --role-name "$ROLE" --policy-name "$name" \
      --query PolicyDocument --output json > "$live" 2>/dev/null; then
    echo "  $name: まだ存在しない(新規作成)"
    rm -f "$live"
    return 0
  fi
  LIVE="$live" FILE="$file" NAME="$name" python3 - <<'PY'
import json, os

def by_sid(doc):
    return {s.get("Sid", "?"): {k: v for k, v in s.items() if k != "Sid"} for s in doc["Statement"]}

live = by_sid(json.load(open(os.environ["LIVE"])))
want = by_sid(json.load(open(os.environ["FILE"])))
name = os.environ["NAME"]

only_live = sorted(set(live) - set(want))
only_want = sorted(set(want) - set(live))
changed = sorted(k for k in set(live) & set(want)
                 if json.dumps(live[k], sort_keys=True) != json.dumps(want[k], sort_keys=True))

if not (only_live or only_want or changed):
    print(f"  {name}: 一致(適用不要)")
else:
    print(f"  {name}:")
    for sid in only_want:
        print(f"    + {sid}")
    for sid in only_live:
        print(f"    - {sid}")
    for sid in changed:
        la = set(live[sid].get("Action") or [])
        wa = set(want[sid].get("Action") or [])
        if isinstance(live[sid].get("Action"), str):
            la = {live[sid]["Action"]}
        if isinstance(want[sid].get("Action"), str):
            wa = {want[sid]["Action"]}
        print(f"    ~ {sid}")
        for a in sorted(wa - la):
            print(f"        + {a}")
        for a in sorted(la - wa):
            print(f"        - {a}")
        if la == wa:
            print("        (Action は同じ。Resource か Condition が違う)")
PY
  rm -f "$live"
}

apply_one() {
  local name=$1 file=$2
  check_ascii "$file"
  diff_live "$name" "$file"
  if [ "$DRY" = true ]; then
    return 0
  fi
  aws iam put-role-policy --role-name "$ROLE" --policy-name "$name" \
    --policy-document "file://$file"
  echo "  $name: 適用した"
}

echo "role: $ROLE"
apply_one kge2e-deploy "$ROOT/e2e/aws/ci-policy.json"
apply_one kge2e-selfcheck "$ROOT/e2e/aws/selfcheck-policy.json"

if [ "$DRY" = true ]; then
  echo "--dry-run なので適用していない"
fi
