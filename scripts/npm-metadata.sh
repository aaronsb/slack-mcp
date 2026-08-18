#!/usr/bin/env bash
# Writes the metadata and README that every platform package publishes with.
#
# The six platform packages are public on npmjs.com. Left as they were, they
# render with no README, no homepage and no bug link — which is what an
# abandoned package looks like. Deliberately no `keywords`: these should not
# compete with the wrapper in npm search, because installing one directly does
# nothing useful.
set -euo pipefail
cd "$(dirname "$0")/.."

for dir in npm/slack-mcp-server-*/; do
  platform=$(basename "$dir" | sed 's/^slack-mcp-server-//')

  jq --arg desc "Platform binary for @aaronsb/slack-mcp ($platform). Installed automatically as an optional dependency; not useful on its own." \
     '. + {
        description: $desc,
        files: ["bin"],
        author: {name: "Aaron Bockelie", url: "https://github.com/aaronsb"},
        homepage: "https://github.com/aaronsb/slack-mcp#readme",
        bugs: {url: "https://github.com/aaronsb/slack-mcp/issues"},
        license: "MIT"
      }' "$dir/package.json" > "$dir/package.json.tmp"
  mv "$dir/package.json.tmp" "$dir/package.json"

  cat > "$dir/README.md" <<EOF
# @aaronsb/slack-mcp-$platform

The \`slack-mcp\` binary compiled for **$platform**.

You almost certainly do not want to install this directly. Install the wrapper
instead — it detects your platform and npm pulls in only the matching binary:

\`\`\`bash
npx @aaronsb/slack-mcp
\`\`\`

Source, documentation and issues:
<https://github.com/aaronsb/slack-mcp>
EOF
done

echo "Wrote metadata and README into $(ls -d npm/slack-mcp-server-*/ | wc -l) platform packages"
