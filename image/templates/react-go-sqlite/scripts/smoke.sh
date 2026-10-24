#!/usr/bin/env bash
# Run against an already supervised foreground preview; never starts a daemon.
set -euo pipefail
BASE_URL="${BASE_URL:-http://127.0.0.1:3000}"
curl -fsS "$BASE_URL/" >/dev/null
curl -fsS "$BASE_URL/api/v1/health" | grep -q '"ok":true'
curl -fsS "$BASE_URL/api/v1/items" | grep -q '"items"'
title="smoke-$(date +%s)"
created="$(curl -fsS -H 'Content-Type: application/json' -d "{\"title\":\"$title\"}" "$BASE_URL/api/v1/items")"
id="$(printf '%s' "$created" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')"
test -n "$id"
curl -fsS -X DELETE "$BASE_URL/api/v1/items/$id" -o /dev/null
printf 'core API smoke passed\n'
