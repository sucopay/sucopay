#!/usr/bin/env bash
# Fails when the module requires something directly that this project has not
# decided to depend on. Run from the repository root. Needs the Go toolchain.
#
# Every dependency is a liability: a supply chain to watch, a licence to
# check, and code that runs where payments are handled. Three are worth it and
# a fourth is a decision somebody has to make on purpose, which is what this
# turns into a failing build rather than a diff nobody reads.
set -euo pipefail

allowed="github.com/goccy/go-yaml
github.com/jackc/pgx/v5
golang.org/x/crypto"

# The main module's own requirements, without the ones pulled in behind them.
required="$(go list -m -f '{{if not .Indirect}}{{.Path}}{{end}}' all |
  grep -v '^github.com/sucopay/sucopay$' | sort)"

if [ "$required" != "$(printf '%s' "$allowed" | sort)" ]; then
  echo "::error::go.mod requires directly:"
  printf '%s\n' "$required" | sed 's/^/  /'
  echo "::error::and this project has decided on:"
  printf '%s\n' "$allowed" | sort | sed 's/^/  /'
  echo "::error::Adding one is a decision. Open an issue first, as CONTRIBUTING says."
  exit 1
fi

echo "ok: the module requires only what this project decided to depend on"
