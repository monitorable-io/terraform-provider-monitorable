#!/usr/bin/env sh
# Copies the REST v1 contract from the sibling backend checkout
# (../monitorable-backend). The committed openapi.json was taken from
# monitorable-backend main 87ca91a (unchanged since 6acd31f). Re-run this
# after the API changes and commit the result; internal/client/contract_test.go
# then shows which client calls no longer match.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
src="${1:-$root/../monitorable-backend/internal/publicapi/openapi/openapi.json}"
if [ ! -f "$src" ]; then
  echo "sync-openapi: $src not found (expected a sibling checkout ../monitorable-backend)" >&2
  exit 1
fi
cp "$src" "$root/openapi.json"
echo "sync-openapi: copied $src"
