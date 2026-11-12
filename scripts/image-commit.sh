#!/bin/sh
# Stamp the commit for an image build.
# Usage: image-commit.sh <context-dir> <output-file>
# LOOPFORGE_COMMIT wins. Otherwise resolve .git/HEAD in the context.
# Objects are not required; a missing or unreadable git dir writes nothing.
set -eu
ctx=${1:-.}
out=${2:?}
commit=${LOOPFORGE_COMMIT:-}
if [ -z "$commit" ] && [ -f "$ctx/.git/HEAD" ]; then
  head=$(tr -d ' \t\r\n' < "$ctx/.git/HEAD")
  case "$head" in
    ref:refs/*)
      ref=${head#ref:}
      case "$ref" in
        *..*) ref= ;;
      esac
      if [ -n "$ref" ] && [ -f "$ctx/.git/$ref" ]; then
        commit=$(tr -d ' \t\r\n' < "$ctx/.git/$ref")
      elif [ -n "$ref" ] && [ -f "$ctx/.git/packed-refs" ]; then
        commit=$(awk -v r="$ref" '$1 !~ /^[#^]/ && $2 == r { print $1; exit }' "$ctx/.git/packed-refs")
      fi
      ;;
    *)
      case "$head" in
        *[!0-9a-fA-F]*) ;;
        *) commit=$head ;;
      esac
      ;;
  esac
fi
case "$commit" in
  *[!0-9a-fA-F]*) commit= ;;
esac
if [ "${#commit}" -lt 7 ] || [ "${#commit}" -gt 40 ]; then
  commit=
fi
printf '%s' "$commit" > "$out"
