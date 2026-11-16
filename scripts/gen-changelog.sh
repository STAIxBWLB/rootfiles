#!/usr/bin/env bash
# Regenerate CHANGELOG.md from docs/releases/v*.md, newest first.
set -euo pipefail
cd "$(dirname "$0")/.."
{
  echo "# Changelog"
  echo
  echo "Generated from docs/releases/ by scripts/gen-changelog.sh; edit the release notes, not this file."
  for f in $(ls docs/releases/v*.md | sort -rV); do
    echo
    echo "## $(basename "$f" .md)"
    echo
    cat "$f"
  done
} > CHANGELOG.md
