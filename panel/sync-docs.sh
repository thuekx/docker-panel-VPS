#!/bin/sh
# Copies the guides and the CHANGELOG into the panel so the Help pages (HELP-01) can show them; the panel image is
# built from this folder only. TestDocsCopy fails when the copy is behind. Run from anywhere: sh panel/sync-docs.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
rm -rf "$here/web/docs"
mkdir -p "$here/web/docs/guides"
cp "$here/../docs/guides/"*.md "$here/web/docs/guides/"
cp "$here/../CHANGELOG.md" "$here/web/docs/CHANGELOG.md"
