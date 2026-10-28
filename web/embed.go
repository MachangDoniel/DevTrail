package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html
var files embed.FS

// GetFS returns the filesystem containing the embedded web dashboard assets.
func GetFS() (fs.FS, error) {
	return fs.Sub(files, ".")
}
