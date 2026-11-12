// Package ui embeds the built interface (a single self-contained HTML file).
// Rebuild it with:  cd ui && npm install && npm run build
package ui

import (
	_ "embed"
	"strings"
)

//go:embed dist/index.html
var page string

const bootMarker = "/*__PILOT_BOOT__*/"

// HTML returns the interface. boot is a JavaScript snippet that runs before
// the app starts (the HTTP server uses it to hand over its access token).
func HTML(boot string) string {
	return strings.Replace(page, bootMarker, boot, 1)
}
