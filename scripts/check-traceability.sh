#!/usr/bin/env bash
# Fails if docs/traceability.md names a test that does not exist, so the matrix
# cannot silently drift from the code. Run by `make check-docs` and by CI.
set -euo pipefail
cd "$(dirname "$0")/.."

missing=0
for name in $(grep -o '`Test[A-Za-z0-9_]*`' docs/traceability.md | tr -d '`' | sort -u); do
  if ! grep -rqE "^func $name\(" --include='*_test.go' .; then
    echo "docs/traceability.md refers to a test that does not exist: $name" >&2
    missing=1
  fi
done
exit $missing
