// Package webassets embeds templates, static files, locale files, the Nostek Migrate plugin and a copy of the guides
// and the CHANGELOG (HELP-01, sync-docs.sh) into the binary.
package webassets

import "embed"

//go:embed templates/*.html static locales/*.json plugin/*.php docs/CHANGELOG.md docs/guides/*.md
var FS embed.FS
