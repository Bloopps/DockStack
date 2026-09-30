#!/usr/bin/env bash
# Régénère internal/theme/ghostty-themes.txt, le catalogue de thèmes embarqué,
# depuis le dossier ghostty/ du dépôt iTerm2-Color-Schemes : la source des
# thèmes livrés avec Ghostty. Un seul fichier plutôt qu'un fichier par thème :
# go:embed refuse certains noms (apostrophes).
set -euo pipefail

repo=https://github.com/mbadolato/iTerm2-Color-Schemes.git
root=$(cd "$(dirname "$0")/.." && pwd)
out=$root/internal/theme/ghostty-themes.txt
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

git clone -q --depth 1 --filter=blob:none --sparse "$repo" "$tmp/src"
git -C "$tmp/src" sparse-checkout set ghostty
rev=$(git -C "$tmp/src" rev-parse --short HEAD)

{
	echo "# Thèmes Ghostty, générés par scripts/update-themes.sh — ne pas modifier."
	echo "# Source : $repo (dossier ghostty/, commit $rev)"
	echo "# Licence MIT, voir LICENSE-iTerm2-Color-Schemes."
	LC_ALL=C ls "$tmp/src/ghostty" | while IFS= read -r name; do
		echo
		echo "[$name]"
		# Seules les clés utiles à dockstack sont gardées.
		grep -E '^(palette|background|foreground|selection-background|selection-foreground) *=' \
			"$tmp/src/ghostty/$name"
	done
} >"$out"
cp "$tmp/src/LICENSE" "$root/internal/theme/LICENSE-iTerm2-Color-Schemes"
echo "$(grep -c '^\[' "$out") thèmes écrits dans $out"
