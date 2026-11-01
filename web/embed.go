// Package web embeds the built Svelte SPA (web/dist).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built SPA rooted at web/dist (index.html at the top level).
// Before the first `npm run build` it contains only .gitkeep.
var Dist = func() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // "dist" is a constant, embedded directory
	}
	return sub
}()
