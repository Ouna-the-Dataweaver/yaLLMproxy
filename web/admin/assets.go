// Package adminui exposes the production admin console assets to the Go server.
package adminui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

var assets, _ = fs.Sub(embedded, "dist")

// Assets returns the built admin console rooted at its dist directory.
func Assets() fs.FS {
	return assets
}
