#!/usr/bin/env bash
# 非同期ワーカー構成に固有の検証(#119)。run.sh から up の後に呼ばれる。
#
#   ./e2e/aws/worker/verify.sh <スタック名> <環境URL>
#
# readiness は API の 200 しか見ない。ここで見るのは **worker が本当に
# 動いているか** — ジョブを入れて、処理結果が出るまでを外から確かめる。
set -euo pipefail

STACK=${1:?usage: verify.sh <stack> <url>}
URL=${2:?usage: verify.sh <stack> <url>}

fail() { echo "WORKER VERIFY FAILED: $*" >&2; exit 1; }
log() { echo; echo "=== $* ==="; }

out() {
  aws cloudformation describe-stacks --stack-name "$STACK" \
    --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue" --output text
}

log "キュー / テーブルが環境スコープになっている"
# 名前がスタック由来でなければ、環境間でジョブが混ざる(pr-41 のジョブを
# pr-42 の worker が食う)。分離の根拠を名前で確かめる。
Q=$(out QueueName); T=$(out TableName)
case "$Q" in "$STACK"-*) : ;; *) fail "キュー名がスタックスコープでない: $Q" ;; esac
case "$T" in "$STACK"-*) : ;; *) fail "テーブル名がスタックスコープでない: $T" ;; esac
echo "  queue=$Q table=$T"

log "ジョブを入れて、worker が処理するまでを見る"
resp=$(curl -sSf -X POST "$URL/jobs" -H 'content-type: application/json' \
  -d '{"input":"kagerou"}') || fail "ジョブを投入できない"
id=$(python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])' <<<"$resp")
echo "  queued: id=$id"

# ポーリング。cold start + SQS 配信で数秒かかる。60 秒待って出なければ、
# worker が壊れている(readiness はこれを検出できない — それがこの検証の意味)。
state=""
for _ in $(seq 1 30); do
  body=$(curl -sSf "$URL/jobs/$id") || fail "結果の取得に失敗"
  state=$(python3 -c 'import json,sys;print(json.load(sys.stdin)["state"])' <<<"$body")
  [ "$state" = "done" ] && break
  sleep 2
done
[ "$state" = "done" ] || fail "60 秒待っても処理されない(worker が動いていない。readiness では見えない故障)"
echo "  done: $body"

result=$(python3 -c 'import json,sys;print(json.load(sys.stdin)["result"])' <<<"$body")
[ "$result" = "KAGEROU" ] || fail "処理結果が違う: $result (want KAGEROU)"

# URL を持たない compute に KAGEROU_ENV が届いたか。HTTP が無いので
# 「応答から読む」が使えず、処理結果に書き込ませて確かめるしかない。
wenv=$(python3 -c 'import json,sys;print(json.load(sys.stdin)["env"])' <<<"$body")
[ "$wenv" = "${STACK#kge2e-}" ] || fail "worker の KAGEROU_ENV がずれている: $wenv"
echo "  worker は正しい環境名で動いている: $wenv"

log "DLQ が空(黙って捨てられたジョブが無い)"
dlq=$(out DeadLetterQueueUrl)
n=$(aws sqs get-queue-attributes --queue-url "$dlq" \
  --attribute-names ApproximateNumberOfMessages \
  --query 'Attributes.ApproximateNumberOfMessages' --output text)
[ "$n" = "0" ] || fail "DLQ に $n 件落ちている"
echo "  0 件"

log "WORKER VERIFY PASSED"
