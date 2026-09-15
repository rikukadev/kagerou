#!/usr/bin/env bash
# squash タイトルが、中の変更より「見えない」type になっていないかを見る。
#
#   .github/scripts/check-pr-title.sh "<PR タイトル>" <コミット件名を改行区切りで stdin>
#
# squash マージではこのリポジトリの設定上 **PR タイトルがそのまま変更履歴に出る**。
# release-please は test / ci / chore を履歴から除外するので、fix を含む PR を
# test: で出すと、利用者に効く修正がリリースノートから丸ごと消える。
#
# 3 回やって 3 回ともリリース後に手で追記した(#157 / #174 / #200)。
# CONTRIBUTING に書いただけでは止まらなかったので、機械で止める。
set -euo pipefail

title=${1:?usage: check-pr-title.sh "<title>" < commit-subjects}

# 変更履歴に出る type(release-please-config.json の changelog-sections と揃える)
visible='feat|fix|perf|refactor|docs|revert'
hidden='test|ci|chore|build|style'

type_of() { # "fix(scope)!: subject" -> "fix"
  printf '%s' "$1" | sed -nE 's/^([a-z]+)(\([^)]*\))?!?:.*/\1/p'
}

t=$(type_of "$title")
if [ -z "$t" ]; then
  echo "::error::PR タイトルが Conventional Commits の形になっていない: '$title'"
  exit 1
fi
# タイトルが既に見える type なら、中身が何でも履歴には出る
if printf '%s' "$t" | grep -qE "^($visible)$"; then
  exit 0
fi
if ! printf '%s' "$t" | grep -qE "^($hidden)$"; then
  echo "::error::PR タイトルの type '$t' を知らない: '$title'"
  exit 1
fi

# タイトルが隠れる type のとき、中に見える type が混ざっていたら止める
buried=()
while IFS= read -r line; do
  [ -n "$line" ] || continue
  c=$(type_of "$line")
  if printf '%s' "$c" | grep -qE "^($visible)$"; then
    buried+=("$line")
  fi
done

if [ ${#buried[@]} -eq 0 ]; then
  exit 0
fi

{
  echo "::error::PR タイトルが '$t:' ですが、中に変更履歴に出る変更が含まれています。"
  echo "squash タイトルがそのまま変更履歴になるため、このままだと次の変更が"
  echo "リリースノートから消えます:"
  printf '  %s\n' "${buried[@]}"
  echo
  echo "影響の大きいほうに合わせてタイトルを付け直してください(CONTRIBUTING.md)。"
  echo "判断に迷ったら『リリースノートだけを読む人がそれを知らなくて困るか』で決める。"
} >&2
exit 1
