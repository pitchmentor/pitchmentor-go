#!/usr/bin/env bash
# Fails unless the Version constant in client.go equals $1 (e.g. 0.1.0 or v0.1.0).
set -euo pipefail
cd "$(dirname "$0")/.."
want="${1#v}"
[[ "$want" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.+].*)?$ ]] || { echo "usage: $0 <version>" >&2; exit 2; }
have="$(sed -nE 's/^[[:space:]]*Version[[:space:]]+= "(.*)"/\1/p' client.go)"
[[ "$have" == "$want" ]] || { echo "version mismatch: client.go Version is '$have', expected '$want'" >&2; exit 1; }
