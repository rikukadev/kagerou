#!/usr/bin/env bash
# 共有 ALB 入口に固有の検証。run.sh(up → 共通検証 → down)から up の後に呼ばれる。
#
#   ./e2e/aws/alb/verify.sh <スタック名> <環境URL>
#
# 共通の run.sh が見るのは「環境 URL が 200 / タグ / down」まで。
# ここで見るのは **入口が共有 ALB のとき** にしか壊れないもの:
#   - {{resolve:ssm}} がベースの値に解決されたこと(デプロイロールの
#     ssm:GetParameters が無いと、そもそもここまで来ない)
#   - リスナールールが **共有リスナーの上** に載っていること
#   - Lambda ターゲットが登録され、応答が中身まで届いていること
set -euo pipefail

STACK=${1:?usage: verify.sh <stack> <url>}
URL=${2:?usage: verify.sh <stack> <url>}

fail() { echo "ALB VERIFY FAILED: $*" >&2; exit 1; }
log() { echo; echo "=== $* ==="; }

out() {
  aws cloudformation describe-stacks --stack-name "$STACK" \
    --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue" --output text
}

ssm() {
  aws ssm get-parameter --name "$1" --query 'Parameter.Value' --output text
}

RULE_ARN=$(out ListenerRuleArn)
TG_ARN=$(out TargetGroupArn)
[ -n "$RULE_ARN" ] && [ "$RULE_ARN" != "None" ] || fail "ListenerRuleArn の Output が無い"
[ -n "$TG_ARN" ] && [ "$TG_ARN" != "None" ] || fail "TargetGroupArn の Output が無い"

log "ルールが共有リスナーの上に載っている"
# ここが本題。テンプレートは ListenerArn を {{resolve:ssm}} でしか知らないので、
# 一致するということは動的参照がデプロイロールの資格情報で解決されたということ。
# ssm:GetParameters が抜けると CFN はここまで来ずに落ちる(#170 で落ちていた)。
BASE_LISTENER=$(ssm /kagerou/base/kagerou-e2e-alb/alb_listener_arn)
RULE_LISTENER=$(aws elbv2 describe-rules --rule-arns "$RULE_ARN" \
  --query 'Rules[0].RuleArn' --output text)
# describe-rules は ListenerArn を返さないので、リスナー側から引いて突き合わせる
aws elbv2 describe-rules --listener-arn "$BASE_LISTENER" \
  --query 'Rules[].RuleArn' --output text | tr '\t' '\n' | grep -qx "$RULE_ARN" \
  || fail "ルール $RULE_LISTENER が共有リスナー($BASE_LISTENER)に無い"

log "URL がベースの DNS 名から組まれている"
BASE_DNS=$(ssm /kagerou/base/kagerou-e2e-alb/alb_dns_name)
case "$URL" in
  *"$BASE_DNS"*) ;;
  *) fail "環境 URL($URL)がベースの DNS($BASE_DNS)を指していない" ;;
esac

log "Lambda ターゲットが登録されている"
TT=$(aws elbv2 describe-target-groups --target-group-arns "$TG_ARN" \
  --query 'TargetGroups[0].TargetType' --output text)
[ "$TT" = "lambda" ] || fail "TargetType=$TT(lambda のはず)"
TARGET=$(aws elbv2 describe-target-health --target-group-arn "$TG_ARN" \
  --query 'TargetHealthDescriptions[0].Target.Id' --output text)
case "$TARGET" in
  arn:aws:lambda:*) ;;
  *) fail "ターゲットが Lambda ではない: $TARGET" ;;
esac

log "応答を返しているのが **この環境の Lambda** である"
# 共通検証(run.sh)も env / greeting を見るが、あちらは「届いたか」。
# ここで見たいのは「共有リスナーの既定応答ではなく、自分のルールが横取りした
# 先から返っている」こと。ベースの fixed-response を 200 に変えられても、
# JSON にならないのでここで落ちる
BODY=$(curl -fsS --max-time 20 "$URL")
GOT=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("env",""))' <<<"$BODY" 2>/dev/null || true)
[ -n "$GOT" ] || fail "応答が JSON ではない(共有リスナーの既定応答かもしれない): $BODY"
[ "$GOT" = "$(basename "$URL")" ] || fail "別の環境が応答している: env=$GOT url=$URL"

echo
echo "ALB VERIFY OK"
