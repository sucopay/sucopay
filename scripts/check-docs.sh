#!/usr/bin/env bash
# Fails when the documentation names something the code does not have.
# Run from the repository root. Needs the Go toolchain.
set -euo pipefail

fail=0
note() { echo "::error::$*"; fail=1; }

# Every relative link resolves. A moved or deleted file leaves the reference
# behind and nothing else reports it.
while IFS= read -r doc; do
  while IFS= read -r target; do
    [ -z "$target" ] && continue
    case "$target" in http*|mailto:*) continue ;; esac
    resolved="$(dirname "$doc")/${target%%#*}"
    [ -e "$resolved" ] || note "$doc links to $target, which does not exist"
  done < <(grep -oE '\]\([^)]+\)' "$doc" | sed 's/^](//; s/)$//')
done < <(find . -name '*.md' -not -path './.git/*')

# The commands each README names are the commands suco dispatches. The usage
# text and the dispatch already read one list in Go; a README is the third
# place a command name can be written.
#
# This is the line the check reads. Rewording it in a README is a change to
# this script as well, so a README that no longer carries it is a failure
# rather than a silent pass.
list='^- \[[ x]\] CLI: '

suco="$(mktemp)"
trap 'rm -f "$suco"' EXIT
go build -o "$suco" ./cmd/suco

# help is a command but not a feature, so the READMEs leave it out.
dispatched="$("$suco" help | sed -n 's/^  suco \([a-z][a-z]*\).*/\1/p' | sed '/^help$/d' | sort | tr '\n' ' ')"

for readme in README.md README.ja.md; do
  line="$(grep -m1 -E "$list" "$readme" || true)"
  if [ -z "$line" ]; then
    note "$readme has no line matching '$list'; restore it or update scripts/check-docs.sh"
    continue
  fi
  documented="$(printf '%s' "$line" | grep -oE '`[a-z]+`' | tr -d '`' | sort | tr '\n' ' ' || true)"
  [ "$documented" = "$dispatched" ] ||
    note "$readme lists commands [$documented] but suco dispatches [$dispatched]"
done

if [ "$fail" -eq 0 ]; then
  echo "ok: the documentation matches the code"
fi
exit $fail
