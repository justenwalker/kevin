#!/usr/bin/env bash
# Flags likely comment-quality violations in lines an edit just added to Go
# files, using $CLAUDE_FILE_PATHS from a PostToolUse Edit|Write hook. Cheap,
# high-confidence, regex-only checks - see docs/GO_CONVENTIONS.md GO-005/006
# and AGENTS.md "Writing Style" for the rules this enforces. Nuanced
# what-vs-why judgment is left to the go-reviewer subagent.
set -euo pipefail

found=0

for f in ${CLAUDE_FILE_PATHS:-}; do
  case "$f" in
    *.go) ;;
    *) continue ;;
  esac
  case "$f" in
    *_templ.go|*/pb/*|*.pb.go) continue ;;
  esac
  [ -f "$f" ] || continue

  dir=$(dirname "$f")
  base=$(basename "$f")
  if git -C "$dir" ls-files --error-unmatch "$base" >/dev/null 2>&1; then
    diff_out=$(git -C "$dir" diff --unified=0 HEAD -- "$base" 2>/dev/null || true)
  else
    diff_out=$(git -C "$dir" diff --unified=0 --no-index -- /dev/null "$base" 2>/dev/null || true)
  fi
  [ -z "$diff_out" ] && continue

  while IFS= read -r line; do
    case "$line" in
      +++*|---*) continue ;;
      +*) ;;
      *) continue ;;
    esac
    content=${line#+}
    trimmed=$(printf '%s' "$content" | sed -E 's/^[[:space:]]*//')
    case "$trimmed" in
      //*) ;;
      *) continue ;;
    esac

    if printf '%s' "$trimmed" | grep -qF '—'; then
      echo "$f: em dash in comment (banned everywhere): $trimmed"
      found=1
    fi
    if printf '%s' "$trimmed" | grep -qiE '\b(used to|previously|before the fix|before this (fix|change)|no longer (does|happens)|now correctly|now properly)\b'; then
      echo "$f: comment narrates history instead of current behavior (git tracks history): $trimmed"
      found=1
    fi
    if printf '%s' "$trimmed" | grep -qiE '\b(comprehensive|robust|seamlessly|leverage[sd]?)\b'; then
      echo "$f: AI-flavored filler word in comment, use plain language: $trimmed"
      found=1
    fi
  done <<EOF
$diff_out
EOF
done

if [ "$found" -eq 1 ]; then
  echo "Review flagged comments above against AGENTS.md 'Writing Style' and docs/GO_CONVENTIONS.md before finishing." >&2
  exit 2
fi
exit 0
