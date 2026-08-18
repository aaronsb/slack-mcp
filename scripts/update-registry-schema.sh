#!/usr/bin/env bash
# Refresh the vendored MCP Registry schema from the URL server.json declares.
#
# Vendored rather than fetched at test time so the check runs offline and
# deterministically, and so a schema change arrives as a reviewable diff instead
# of a build that starts failing on its own.
set -euo pipefail
cd "$(dirname "$0")/.."

url=$(jq -r '."$schema"' server.json)
echo "Fetching $url"
curl -fsSL "$url" -o docs/schemas/mcp-server.schema.json
echo "Updated docs/schemas/mcp-server.schema.json ($(wc -c < docs/schemas/mcp-server.schema.json) bytes)"
