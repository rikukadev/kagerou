#!/usr/bin/env bash
# HTTP API + VPC Link → Cloud Map → ECS Fargate に固有の検証。
# run.sh(up → 共通検証 → down)から up の後に呼ばれる。
#
#   ./e2e/aws/apigw/verify.sh <スタック名> <環境URL>
#
# 共通の run.sh が見るのは「環境 URL が 200 / タグ / down」まで。
# ここで見るのは **compute が Lambda でないとき** にしか壊れないもの:
#   - Cloud Map の DNS レコードが SRV であること(A だと 503 になる)
#   - タスクがポート付きで登録されていること(SRV に要る)
#   - 200 の中身が「このタスクが返した」ものであること(env が中まで届く)
set -euo pipefail

STACK=${1:?usage: verify.sh <stack> <url>}
URL=${2:?usage: verify.sh <stack> <url>}

fail() { echo "APIGW VERIFY FAILED: $*" >&2; exit 1; }
log() { echo; echo "=== $* ==="; }

out() {
  aws cloudformation describe-stacks --stack-name "$STACK" \
    --query "Stacks[0].Outputs[?OutputKey=='$1'].OutputValue" --output text
}

SVC_ID=$(out DiscoveryServiceId)
[ -n "$SVC_ID" ] || fail "DiscoveryServiceId の Output が無い"

log "Cloud Map のレコードが SRV"
# A レコードでも CFN は成功し、cfn-lint も通る。**繋がらないのは実行時だけ**。
# ここを固定しておかないと、誰かが「A のほうが素直では」と直したときに
# 503 の原因に辿り着けない
rec=$(aws servicediscovery get-service --id "$SVC_ID" \
  --query 'Service.DnsConfig.DnsRecords[0].Type' --output text)
[ "$rec" = "SRV" ] || fail "DNS レコードが $rec(VPC Link 統合は SRV を要求する)"
echo "  SRV"

log "タスクがポート付きで登録されている"
# SRV にはポートが要る。ECS の ServiceRegistries に ContainerName/ContainerPort が
# 無いと、ここが空になって登録自体が成立しない
port=$(aws servicediscovery list-instances --service-id "$SVC_ID" \
  --query 'Instances[0].Attributes.AWS_INSTANCE_PORT' --output text)
[ "$port" = "8080" ] || fail "登録ポートが '$port'(ServiceRegistries の ContainerPort を確認)"
ip=$(aws servicediscovery list-instances --service-id "$SVC_ID" \
  --query 'Instances[0].Attributes.AWS_INSTANCE_IPV4' --output text)
[ -n "$ip" ] && [ "$ip" != "None" ] || fail "IP が登録されていない"
echo "  $ip:$port"

log "VPC Link 越しの応答がこのタスクのもの"
body=$(curl -sSf "$URL/") || fail "環境 URL に到達できない"
echo "  $body"
python3 - "$body" "$STACK" <<'PY'
import json, sys
d = json.loads(sys.argv[1])
assert d.get("service") == "apigw-ecs", f"別のものが応答している: {d}"
# env が **コンテナの中まで** 届いていることを応答で確かめる。
# HTTP が 200 なだけでは、環境変数の配線が壊れていても気づけない
assert d.get("env"), f"KAGEROU_ENV がタスクに届いていない: {d}"
assert d["env"] in sys.argv[2], f"別環境の応答: {d['env']} not in {sys.argv[2]}"
assert d.get("greeting"), f"env の追加値が届いていない: {d}"
print(f"  env={d['env']} greeting={d['greeting']}")
PY

log "APIGW VERIFY PASSED"
