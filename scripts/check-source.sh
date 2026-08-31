#!/usr/bin/env bash
# Fails when a tracked file holds a character that does not show up in it.
# Run from the repository root.
#
# The code refuses these at runtime in configuration documents, in reports and
# in payment metadata, because a terminal acts on them rather than showing
# them. The same property makes them a hazard in a source file: nobody reviews
# what they cannot see, and tools that split lines split on some of them.
#
# The set is the one internal/invisible defines, less the tab and the newline
# that every source file is made of. A test there checks the two agree.
set -euo pipefail

# The pattern is written as escapes for the reason it exists.
invisible='[\x{0000}-\x{0008}\x{000b}-\x{000d}\x{000e}-\x{001f}\x{007f}-\x{009f}\x{00a0}\x{200b}-\x{200f}\x{2028}\x{2029}\x{202a}-\x{202e}\x{2060}\x{2066}-\x{2069}\x{feff}]'

fail=0
report() {
  echo "::error::$1:$2 holds a character that does not show up. Write it as an escape."
  fail=1
}

# -z and -d '' because git prints a name holding anything but ASCII as a quoted
# C string, which would name a file that does not exist and be skipped.
# -a because a match inside what grep decides is binary is otherwise announced
# on a line this loop never sees.
while IFS= read -r -d '' file; do
  [ -f "$file" ] || continue
  matches="$(grep -anP "$invisible" "$file" | tr -d '\0')" && rc=0 || rc=$?
  case "$rc" in
    0) while IFS= read -r match; do report "$file" "${match%%:*}"; done <<< "$matches" ;;
    1) ;;
    # Anything else means grep did not read the file. \x{} above the first 256
    # needs a UTF-8 locale, and a `|| true` here would report every file as
    # clean without having looked at one.
    *) echo "::error::grep could not read $file (exit $rc). Check the locale." ; fail=1 ;;
  esac
# --others so that a file not yet added is checked too: the point is to catch
# this before it is committed, not after.
done < <(git ls-files -z --cached --others --exclude-standard)

if [ "$fail" -eq 0 ]; then
  echo "ok: every character in the source is one a reader can see"
fi
exit $fail
