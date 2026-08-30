#!/usr/bin/env bash
# Fails when the documentation names something the code does not have.
# Run from the repository root.
set -euo pipefail

fail=0
note() { echo "::error::$*"; fail=1; }

# Every relative link in a Markdown file resolves. A file moved or removed
# leaves the reference behind, and reading a document is the only way that
# shows up otherwise.
while IFS= read -r doc; do
  while IFS= read -r target; do
    [ -z "$target" ] && continue
    case "$target" in http*|mailto:*) continue ;; esac
    resolved="$(dirname "$doc")/${target%%#*}"
    [ -e "$resolved" ] || note "$doc links to $target, which does not exist"
  done < <(grep -oE '\]\([^)]+\)' "$doc" | sed 's/^](//; s/)$//')
done < <(find . -name '*.md' -not -path './.git/*')

# The commands the README names are the commands suco dispatches. The usage
# text and the dispatch already read one list in Go; the README is the third
# place a command name can be written.
suco="$(mktemp)"
trap 'rm -f "$suco"' EXIT
go build -o "$suco" ./cmd/suco
dispatched="$("$suco" help | sed -n 's/^  suco \([a-z]*\).*/\1/p' | sort | tr '\n' ' ')"
for readme in README.md README.ja.md; do
  documented="$(sed -n 's/.*CLI[:.] *//p' "$readme" | grep -oE '`[a-z]+`' | tr -d '`' | sort | tr '\n' ' ')"
  [ -z "$documented" ] && continue
  # help is a command but not a feature, so the README omits it.
  expected="$(echo "$dispatched" | tr ' ' '\n' | grep -v '^help$' | grep -v '^$' | sort | tr '\n' ' ')"
  if [ "$documented" != "$expected" ]; then
    note "$readme lists commands [$documented] but suco dispatches [$expected]"
  fi
done

if [ "$fail" -eq 0 ]; then
  echo "ok: the documentation matches the code"
fi
exit $fail
