#!/usr/bin/env bash
# Decide whether the nightly run of .github/workflows/images.yml rebuilds a
# release line's images (#966).
#
# Usage:  scripts/images-line-stale.sh <release/X.Y>
#
# Run from the repository checkout, with origin/release/X.Y fetched, NS the
# images' namespace (ghcr.io/tacplus-dev/tacplus), gh able to list
# GITHUB_REPOSITORY's runs and docker able to pull NS/build:X.Y. Prints one
# line: "rebuild: <why>" or "current: <why>".
#
# A line is rebuilt when something its images are made of is newer than its
# last successful line rebuild: images/ on the branch, an upstream image its
# Dockerfiles start from (their FROM, with the ARG defaults), or a Debian
# package (`apt-get -s upgrade` in NS/build:X.Y). Otherwise the rebuild would
# publish the same package set again, and a dated tag would name nothing new.
# The dated tag build:<yyyymmdd>-<sha7> is immutable, so a line whose tag for
# today and its branch's head exists is current whatever else changed.
set -euo pipefail

ref="$1"
line="${ref#release/}"
head="$(git rev-parse "origin/$ref")"
image="$NS/build:$line"

stamp="$NS/build:$(date -u +%Y%m%d)-${head::7}"
if docker manifest inspect "$stamp" >/dev/null 2>&1; then
  echo "current: $stamp is already published"
  exit 0
fi
if ! docker manifest inspect "$image" >/dev/null 2>&1; then
  echo "rebuild: $image does not exist"
  exit 0
fi

last="$(gh run list --repo "$GITHUB_REPOSITORY" --workflow images.yml --branch "$ref" --status success --limit 1 \
  --json createdAt,headSha --jq '.[] | "\(.createdAt) \(.headSha)"')"
if [[ -z $last ]]; then
  echo "rebuild: no line rebuild of $ref has succeeded yet"
  exit 0
fi
read -r at sha <<<"$last"
since="$(jq -rn --arg c "$at" '$c | sub("\\.[0-9]+"; "") | fromdateiso8601')"

if ! git cat-file -e "$sha^{commit}" 2>/dev/null || ! git diff --quiet "$sha" "origin/$ref" -- images/; then
  echo "rebuild: images/ changed on $ref since its rebuild of $at (${sha::7})"
  exit 0
fi

# The upstream images of the line's Dockerfiles: each FROM's image with the
# global ARG defaults (those before the first FROM) substituted, less the
# stages it names and the line's own images (NS/...).
upstream() {
  awk -v ns="$NS/" '
    function subst(s,   name) {
      while (match(s, /\$\{[A-Za-z_][A-Za-z0-9_]*\}|\$[A-Za-z_][A-Za-z0-9_]*/)) {
        name = substr(s, RSTART, RLENGTH); gsub(/[${}]/, "", name)
        if (!(name in arg)) return ""
        s = substr(s, 1, RSTART - 1) arg[name] substr(s, RSTART + RLENGTH)
      }
      return s
    }
    toupper($1) == "ARG" && !from && index($2, "=") {
      split($2, kv, "="); arg[kv[1]] = substr($2, length(kv[1]) + 2); next
    }
    toupper($1) == "FROM" {
      from = 1; i = 2
      while ($i ~ /^--/) i++
      ref = subst($i)
      if (toupper($(i + 1)) == "AS") stage[$(i + 2)] = 1
      if (ref != "" && ref != "scratch" && !(ref in stage) && index(ref, ns) != 1) print ref
    }'
}
refs="$(for f in $(git ls-tree --name-only "origin/$ref" images/ | grep '\.Dockerfile$'); do
  git show "origin/$ref:$f" | upstream
done | sort -u)"
for r in $refs; do
  created="$(docker buildx imagetools inspect "$r" --format '{{json .Image}}' |
    jq -r 'if type == "object" and (.["linux/amd64"] | type) == "object" then .["linux/amd64"].created // "" else .created // "" end')"
  if [[ -z $created ]] || (( $(jq -rn --arg c "$created" '$c | sub("\\.[0-9]+"; "") | fromdateiso8601') > since )); then
    echo "rebuild: $r was updated ${created:-at an unknown time} after the line's rebuild of $at"
    exit 0
  fi
done

apt="$(docker run --rm --pull always --user 0 --entrypoint sh "$image" -c 'apt-get update -qq >/dev/null && apt-get -s upgrade')"
docker rmi "$image" >/dev/null 2>&1 || true
pkgs="$(awk '$1 == "Inst" { printf "%s%s", sep, $2; sep = " " }' <<<"$apt")"
if [[ -n $pkgs ]]; then
  echo "rebuild: Debian packages to upgrade in $image: $pkgs"
  exit 0
fi
echo "current: nothing new since the line's rebuild of $at (${sha::7})"
