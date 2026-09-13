#!/usr/bin/env bash
# 実 AWS で環境を 1 個作って、確かめて、消す。
#
#   ./e2e/aws/run.sh <kagerou バイナリ> <kagerou.yaml> <環境名>
#
# moto では見えないものを見るための E2E。実際にこれで踏んだ種類:
#   - IAM の過不足(スタック作成前に呼ばれる API、Transform 自体の ARN、タグ伝播)
#   - 中身の入った S3 バケットがあると DeleteStack が失敗する
#   - post_up → readiness の順序(配置前に 200 を待つと永遠に待つ)
#
# **失敗しても必ず片付ける。** 落ちたまま環境が残ると次の実行が名前で衝突し、
# 課金も続く。呼び出し側の workflow も if: always() で down を回すが、
# ここでも trap を張って二重に担保する。
set -euo pipefail

KAGEROU=${1:?usage: run.sh <kagerou> <config> <name>}
CONFIG=${2:?usage: run.sh <kagerou> <config> <name>}
NAME=${3:?usage: run.sh <kagerou> <config> <name>}

fail() { echo "E2E FAILED: $*" >&2; exit 1; }
log()  { echo; echo "=== $* ==="; }

# 中身の入ったバケットを空にするのは **kagerou.yaml の pre_down の仕事**。
# ここでは何もしない。down がそれで通ること自体が検証項目なので、CI 側で
# 先回りして空にすると pre_down が動かなくても気づけなくなる。
#
# 例外は trap の側(下)。そこは「E2E が途中で落ちた後始末」なので、
# pre_down が動かない状態でも確実に消せる必要がある。
force_empty_buckets() {
  local stack="$1" b
  for b in $(aws cloudformation describe-stack-resources --stack-name "$stack" \
      --query "StackResources[?ResourceType=='AWS::S3::Bucket'].PhysicalResourceId" \
      --output text 2>/dev/null || true); do
    [ -n "$b" ] && [ "$b" != "None" ] || continue
    echo "  emptying s3://$b"
    aws s3 rm "s3://$b" --recursive >/dev/null 2>&1 || true
  done
}

STACK=""
cleanup() {
  local rc=$?
  log "cleanup (exit=$rc)"
  [ -n "$STACK" ] && force_empty_buckets "$STACK"
  "$KAGEROU" down --name "$NAME" --config "$CONFIG" || echo "  down に失敗(手で確認すること)" >&2
  exit "$rc"
}
trap cleanup EXIT

log "up: $NAME"
up_json=$("$KAGEROU" up --name "$NAME" --config "$CONFIG" --output json) \
  || fail "up が失敗した"
echo "$up_json"

read -r state url stack <<<"$(python3 -c '
import json, sys
d = json.load(sys.stdin)
print(d.get("state", ""), d.get("url", ""), d.get("stack", {}).get("name", ""))' <<<"$up_json")"
STACK="$stack"

[ "$state" = "ready" ] || fail "state=$state (ready であるべき)"
[ -n "$url" ] && [ "$url" != "None" ] || fail "環境 URL が空(KagerouUrl Output か url_template を確認)"
[ -n "$STACK" ] || fail "スタック名が返っていない"
echo "  state=$state url=$url stack=$STACK"

# readiness_path を設定してあるので、ready なら既に 200 が返っているはず。
# ここで落ちるなら「ready と言っているのに使えない」ことになる。
log "環境 URL が応答する"
code=$(curl -sS -o /dev/null -w '%{http_code}' "$url" || true)
[ "$code" = "200" ] || fail "URL が $code を返した(ready なら 200 のはず): $url"
echo "  200 OK"

# --env が Env<Key> パラメータ経由でアプリの環境変数まで届いているか。
# 契約(CONTRACT §4)の端から端までを 1 回で見る。
log "--env がアプリまで届いている"
api=$(aws cloudformation describe-stacks --stack-name "$STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='ApiUrl'].OutputValue" --output text 2>/dev/null || true)
[ -n "$api" ] && [ "$api" != "None" ] || api="$url"
body=$(curl -sS "$api" || fail "API に到達できない: $api")
echo "  $body"
read -r got_env got_greeting <<<"$(python3 -c '
import json, sys
d = json.load(sys.stdin)
print(d.get("env", ""), d.get("greeting", ""))' <<<"$body")"
[ "$got_env" = "$NAME" ] || fail "env=$got_env (want $NAME)"
[ -n "$got_greeting" ] || fail "--env の GREETING が届いていない"

# タグ(CONTRACT §1)。reap / list はこれで環境を見分けるので、
# 欠けていると「消せない環境」が生まれる。
log "契約のタグが付いている"
tags=$(aws cloudformation describe-stacks --stack-name "$STACK" \
  --query 'Stacks[0].Tags' --output json)
for k in kagerou:managed kagerou:name kagerou:project kagerou:expires-at; do
  grep -q "\"$k\"" <<<"$tags" || fail "タグ $k が無い: $tags"
done
echo "  managed / name / project / expires-at がある"

# 自分のプロジェクトの一覧に出ること。
log "list に出る"
"$KAGEROU" list --config "$CONFIG" --output json | grep -q "\"$NAME\"" \
  || fail "list に $NAME が出てこない"
echo "  OK"

# 構成固有の検証(あれば)。共通の run.sh は「1 個の compute」を前提に
# 書けることしか見ないので、複数サービスの結線などはフィクスチャ側に置く。
EXTRA="$(dirname "$CONFIG")/verify.sh"
if [ -x "$EXTRA" ]; then
  log "構成固有の検証: $EXTRA"
  "$EXTRA" "$STACK" "$url" || fail "構成固有の検証が失敗した"
fi

# ここから先は cleanup(trap)が down を回す。その結果まで確かめたいので、
# trap を外して自分で down し、残骸ゼロを見る。
trap - EXIT
log "down(バケットを空にするのは pre_down の仕事)"
"$KAGEROU" down --name "$NAME" --config "$CONFIG" || fail "down が失敗した"

log "残骸が無い"
if aws cloudformation describe-stacks --stack-name "$STACK" >/dev/null 2>&1; then
  status=$(aws cloudformation describe-stacks --stack-name "$STACK" \
    --query 'Stacks[0].StackStatus' --output text)
  fail "スタックが残っている: $STACK ($status)"
fi
echo "  スタックは消えている"

# down は冪等であること(close の再送や reap との競合で 2 回走り得る)。
"$KAGEROU" down --name "$NAME" --config "$CONFIG" || fail "2 回目の down が失敗した(冪等でない)"
echo "  2 回目の down も成功(冪等)"

log "E2E PASSED: $NAME"
