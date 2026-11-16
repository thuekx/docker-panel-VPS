// Package webassets embeds templates, static files, locale files and the Nostek Migrate plugin into the binary.
package webassets

import "embed"

//go:embed templates/*.html static locales/*.json plugin/*.php
var FS embed.FS
