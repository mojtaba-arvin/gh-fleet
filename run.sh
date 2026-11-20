#!/bin/sh
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ ! -x "$here/gh-fleet" ]; then
  (cd "$here" && go build -o gh-fleet .)
fi
exec "$here/gh-fleet" "$@"
