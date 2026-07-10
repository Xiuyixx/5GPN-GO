//go:build !embed

// Package web owns the embedded panel bundle.
package web

import "io/fs"

// FS is nil in non-embed builds; the daemon falls back to serving from disk.
var FS fs.FS
