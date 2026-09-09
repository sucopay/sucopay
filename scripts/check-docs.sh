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

# The commands each roadmap names are the commands suco dispatches. The usage
# text and the dispatch already read one list in Go; a roadmap is the third
# place a command name can be written.
#
# This is the line the check reads. Rewording it in a roadmap is a change to
# this script as well, so a roadmap that no longer carries it is a failure
# rather than a silent pass.
list='^- \[[ x]\] CLI: '

suco="$(mktemp)"
go build -o "$suco" ./cmd/suco

# help is a command but not a feature, so the READMEs leave it out. The first
# word after suco is what a README names; a group of commands has one line
# per command and one word in a README.
dispatched="$("$suco" help | sed -n 's/^  suco \([a-z][a-z]*\).*/\1/p' | sed '/^help$/d' | sort -u | tr '\n' ' ')"

for roadmap in ROADMAP.md ROADMAP.ja.md; do
  line="$(grep -m1 -E "$list" "$roadmap" || true)"
  if [ -z "$line" ]; then
    note "$roadmap has no line matching '$list'; restore it or update scripts/check-docs.sh"
    continue
  fi
  documented="$(printf '%s' "$line" | grep -oE '`[a-z]+`' | tr -d '`' | sort | tr '\n' ' ' || true)"
  [ "$documented" = "$dispatched" ] ||
    note "$roadmap lists commands [$documented] but suco dispatches [$dispatched]"
done

# The suco.yaml each README shows is one suco accepts. A reader copies it, so a
# key renamed in the code and not there sends them to a refusal on their first
# run.
#
# doctor is what reads it. Its exit status is about what it reached, which
# needs a database and a provider; that a document resolved at all is the
# report it prints before going looking, so the report is what this reads.
work="$(mktemp -d)"
trap 'rm -f "$suco"; rm -rf "$work"' EXIT
cat >"$work/head.yaml" <<'YAML'
listen:
  port: 7826
credentials:
  key: ${SUCO_CREDENTIALS_KEY}
  key_id: "0123456789abcdef"
database:
  managed: false
  url: ${SUCO_DATABASE_URL}
YAML

for readme in README.md README.ja.md; do
  # The one fenced yaml block each README carries, which is the example.
  awk '/^```yaml$/{inside=1; next} /^```$/{inside=0} inside' "$readme" >"$work/example.yaml"
  if [ ! -s "$work/example.yaml" ]; then
    note "$readme has no yaml example for scripts/check-docs.sh to check"
    continue
  fi
  cat "$work/head.yaml" "$work/example.yaml" >"$work/suco.yaml"
  report="$(cd "$work" && SUCO_CONFIG=suco.yaml \
    SUCO_CREDENTIALS_KEY=0000000000000000000000000000000000000000000000000000000000000000 \
    SUCO_DATABASE_URL=postgres://nobody@127.0.0.1:1/nothing \
    SUCO_POLYGON_RPC_URL=http://127.0.0.1:1/rpc \
    "$suco" doctor 2>&1 || true)"
  case "$report" in
    *assets.jpyc.reference*) ;;
    *) note "$readme shows a suco.yaml that suco does not accept:"$'\n'"$report" ;;
  esac
done

if [ "$fail" -eq 0 ]; then
  echo "ok: the documentation matches the code"
fi
exit $fail
