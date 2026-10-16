#!/usr/bin/env bash
# Calcule la prochaine version à publier à partir des commits (Conventional
# Commits) depuis la dernière version stable.
#
# Usage : scripts/next-version.sh <stable|beta>
# Sortie : lignes KEY=VALUE (compatibles $GITHUB_OUTPUT)
#   tag=vX.Y.Z[-beta.N]   vide s'il n'y a rien à publier
#   previous=<tag>        point de départ des notes de version
#
# Règles : `feat:` → mineure, `fix:` → correctif, `type!:` ou « BREAKING CHANGE »
# → majeure (mineure tant que la version est en 0.x). Les autres types (docs,
# test, ci, chore…) ne déclenchent aucune version. La beta vX.Y.Z-beta.N
# prépare la prochaine stable vX.Y.Z ; N s'incrémente à chaque publication.
set -euo pipefail

channel="${1:-}"
case "$channel" in stable | beta) ;; *)
  echo "usage: $0 <stable|beta>" >&2
  exit 2
  ;;
esac

stable_re='^v[0-9]+\.[0-9]+\.[0-9]+$'
last_stable=$(git tag --merged HEAD --list 'v*' --sort=-v:refname | grep -E "$stable_re" | head -n1 || true)
base=${last_stable:-v0.0.0}
range=${last_stable:+$last_stable..}HEAD

bump=""
subjects=$(git log --format=%s "$range")
bodies=$(git log --format=%b "$range")
if grep -Eq '^[a-z]+(\([^)]*\))?!:' <<<"$subjects" || grep -q '^BREAKING[ -]CHANGE' <<<"$bodies"; then
  bump=major
elif grep -Eq '^feat(\([^)]*\))?:' <<<"$subjects"; then
  bump=minor
elif grep -Eq '^fix(\([^)]*\))?:' <<<"$subjects"; then
  bump=patch
fi

if [ -z "$bump" ]; then
  echo "tag="
  exit 0
fi

IFS=. read -r major minor patch <<<"${base#v}"
if [ "$bump" = major ] && [ "$major" -eq 0 ]; then bump=minor; fi
case "$bump" in
  major) major=$((major + 1)); minor=0; patch=0 ;;
  minor) minor=$((minor + 1)); patch=0 ;;
  patch) patch=$((patch + 1)) ;;
esac
next="v$major.$minor.$patch"

# Déjà publié sur ce commit : rien à refaire.
if git tag --points-at HEAD --list 'v*' | grep -q .; then
  echo "tag="
  exit 0
fi

if [ "$channel" = stable ]; then
  if git rev-parse -q --verify "refs/tags/$next" >/dev/null; then
    echo "tag="
    exit 0
  fi
  echo "tag=$next"
  echo "previous=${last_stable}"
  exit 0
fi

n=$(git tag --list "$next-beta.*" | sed -E 's/.*-beta\.//' | sort -n | tail -n1)
tag="$next-beta.$(( ${n:-0} + 1 ))"
previous=$(git tag --merged HEAD --list 'v*' --sort=-v:refname | head -n1 || true)
echo "tag=$tag"
echo "previous=${previous:-$last_stable}"
