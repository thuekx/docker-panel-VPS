// Package webassets embeds templates, static files and locale files into the binary.
package webassets

import "embed"

//go:embed templates/*.html static locales/*.json
var FS embed.FS
