#!/usr/bin/env bash
# Fails if internal planning material has been committed to this repository.
# A backstop for CI, not a substitute for keeping such material out of the tree.
# Run from the repository root.
set -euo pipefail

fail=0
note() { echo "::error::$*"; fail=1; }

# Directory names used for internal planning material.
for d in private strategy research biz decisions notes; do
  if [ -d "$d" ]; then
    note "'$d/' must not exist in this repository."
  fi
done

# Files marked as internal. The marker is assembled at runtime so this script
# does not match itself.
pub="PUBLISH"
marker="$(printf 'public \xe3\x81\xab\xe3\x81\xaf\xe5\x87\xba\xe3\x81\x95\xe3\x81\xaa\xe3\x81\x84')"
while IFS= read -r f; do
  note "$f is marked as internal but is committed here."
done < <(grep -rl --exclude-dir=.git --exclude=check-public-only.sh -e "$marker" -e "DO NOT ${pub}" . 2>/dev/null || true)

# References to documents that live only in the private repository. They read
# as a citation and resolve to nothing for anyone outside it, so whatever the
# reference was carrying has to be said here instead.
#
# Case-insensitive, and repo rather than repository, because a citation written
# in prose does not have to match the spelling of a link. Loose enough to catch
# a sentence merely holding the words rather than a link; a false positive here
# costs a rewording, and a false negative ships the citation.
while IFS= read -r -d '' f; do
  [ "$f" = "scripts/check-public-only.sh" ] && continue
  if grep -inE '\[?ADR [0-9]|decisions/adr/|sucopay-strategy|open.?questions|strategy repo|private repo' "$f" >/dev/null 2>&1; then
    note "$f cites a document only the private repository has."
  fi
done < <(git ls-files -z --cached --others --exclude-standard)

if [ "$fail" -eq 0 ]; then
  echo "ok: no internal material found"
fi
exit $fail
