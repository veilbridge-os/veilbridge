//go:build ui

// This file is compiled with `-tags ui`, after `npm run build` has populated
// internal/api/dist. CI runs the web build then `go build -tags ui` so the
// production binary ships the UI. See cmd/veilbridged and web/README.md.
package api

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// uiFS returns the built web assets rooted at dist/, or nil if unavailable.
func uiFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil
	}
	return sub
}
