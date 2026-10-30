#!/usr/bin/env bash
# Downloads the live OpenAPI spec and regenerates the SDK's generated code. Needs curl and node 20+.
#   scripts/regen.sh                       (live spec)
#   PITCHMENTOR_OPENAPI_URL=... scripts/regen.sh
set -euo pipefail
cd "$(dirname "$0")/.."
url="${PITCHMENTOR_OPENAPI_URL:-https://api.pitchmentor.net/v1/openapi.json}"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
curl -fsSL --retry 3 "$url" -o "$tmp"
node -e 'const s = JSON.parse(require("fs").readFileSync(process.argv[1], "utf8")); if (!s.openapi || !s.paths) throw new Error("not an OpenAPI document")' "$tmp"
# pretty-print so diffs in pull requests stay readable
node -e 'const fs = require("fs"); fs.writeFileSync("openapi.json", JSON.stringify(JSON.parse(fs.readFileSync(process.argv[1], "utf8")), null, 2) + "\n")' "$tmp"
node codegen/generate.mjs
