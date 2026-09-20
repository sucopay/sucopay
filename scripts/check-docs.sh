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
YAML

for readme in README.md README.ja.md; do
  # The one fenced yaml block each README carries, which is the example.
  awk '/^```yaml$/{inside=1; next} /^```$/{inside=0} inside' "$readme" >"$work/example.yaml"
  if [ ! -s "$work/example.yaml" ]; then
    note "$readme has no yaml example for scripts/check-docs.sh to check"
    continue
  fi
  cat "$work/head.yaml" "$work/example.yaml" >"$work/suco.yaml"
  # A README that shows the database settings carries them itself. One that
  # does not gets the pair serve needs.
  if ! grep -q '^database:' "$work/suco.yaml"; then
    printf 'database:\n  managed: false\n  url: ${SUCO_DATABASE_URL}\n' >>"$work/suco.yaml"
  fi
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

# The Japanese docs hold to the glossary in WRITING-STYLE.ja.md. Prose only:
# fenced code is blanked and code spans are replaced by a marker, since both
# carry the words as code has them. Lines are kept in place, so that a report
# names the line of the file.
#
# Documents not yet proofread to these rules are skipped here, and leave this
# list as each is proofread.
unwritten=""
for doc in $unwritten; do
  [ -e "$doc" ] || note "$doc is listed as not rewritten yet, and does not exist"
done
prose() {
  awk '
    function run(line,   t, c, n) {
      t = line; sub(/^[ \t]+/, "", t); c = substr(t, 1, 1)
      if (c != "`" && c != "~") return 0
      n = 0; while (substr(t, n + 1, 1) == c) n++
      if (n < 3) return 0
      opener = c; width = n; return 1
    }
    !inside { if (run($0)) { inside = 1; print ""; next } print; next }
    inside {
      t = $0; sub(/^[ \t]+/, "", t); sub(/[ \t]+$/, "", t)
      if (substr(t, 1, 1) == opener && length(t) >= width && t !~ /[^`~]/) inside = 0
      print ""
    }' "$1" | sed 's/`[^`]*`/X/g'
}
# The character classes below are Unicode ones, which grep reads as such only
# under a UTF-8 locale. A locale that is not one leaves grep -P failing, which
# would pass every document, so a known violation is tried first.
export LC_ALL=C.UTF-8
if ! printf 'サーバが\n' | grep -qP 'サーバ(?!ー)'; then
  note "grep cannot read UTF-8 under this locale, so the documents were not checked"
fi
forbidden='印字(?!可能)|訊|配備|宛先|経路|要求|応答|契約|配送|要り|要る|断(り|る|っ|れ)|名指|押さえ|落ち(た|る|て|れ)|控え|素の|周(?=[がをはにのとでも、。]|$)|[0-9] ?周|(サーバ|ユーザ|インタフェース|アダプタ|エクスプローラ|プロバイダ)(?!ー)|(?<![A-Za-z/._])(Payment|Refund)s?(?![A-Za-z_])|(?<!API )(?<!RPC )(?<!Webhook )(?<![A-Za-z])エンドポイント'
# A sentence that opens with a demonstrative points at the sentence before it,
# and a thing named by a verb and もの has no name. Both are ruled out by the
# sentence rules in WRITING-STYLE.ja.md; the lead's このページは is the one
# demonstrative allowed.
unnamed='(^|。|\| )(それ(?!ぞれ)|その|これ|この(?!ページ)|そこ|あれ|あの)|(る|た|ない)もの'
for doc in docs/*.ja.md README.ja.md CONTRIBUTING.ja.md ROADMAP.ja.md SECURITY.ja.md; do
  [ -f "$doc" ] || { note "$doc is not a file"; continue; }
  case " $unwritten " in *" $doc "*) continue ;; esac
  while IFS= read -r line; do
    note "$doc uses a word WRITING-STYLE.ja.md rules out: $line"
  done < <(prose "$doc" | grep -nP "$forbidden")
  while IFS= read -r line; do
    note "$doc opens a sentence with a demonstrative, or names a thing by a verb and もの: $line"
  done < <(prose "$doc" | grep -nP "$unnamed")
done

# A half-width letter or digit and a full-width character have a space between
# them, in every document of either language. A code span reads as half-width,
# which is what the marker stands for.
for doc in docs/*.md README.md README.ja.md CONTRIBUTING.md CONTRIBUTING.ja.md ROADMAP.md ROADMAP.ja.md SECURITY.md SECURITY.ja.md WRITING-STYLE.ja.md; do
  [ -f "$doc" ] || { note "$doc is not a file"; continue; }
  while IFS= read -r line; do
    note "$doc has no space between a half-width and a full-width character: $line"
  done < <(prose "$doc" | grep -nP '[A-Za-z0-9][\x{3041}-\x{3096}\x{30A1}-\x{30FA}\x{30FC}\x{4E00}-\x{9FFF}]|[\x{3041}-\x{3096}\x{30A1}-\x{30FA}\x{30FC}\x{4E00}-\x{9FFF}][A-Za-z0-9]')
done

if [ "$fail" -eq 0 ]; then
  echo "ok: the documentation matches the code"
fi
exit $fail
