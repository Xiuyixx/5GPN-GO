//go:build embed

// Package web owns the embedded panel bundle.
//
// The `embed` build tag flips FS from an empty stub to the real go:embed
// filesystem sourced from web/dist. Keeping this in a separate package makes
// the embed directive resolvable from the module root (../../web/dist works
// from here but not from cmd/5gpn/).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var raw embed.FS

// FS is the panel bundle rooted at the dist directory.
var FS fs.FS = mustSub(raw)

func mustSub(f embed.FS) fs.FS {
	sub, err := fs.Sub(f, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
