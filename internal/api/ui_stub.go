//go:build !ui

// Default build (no `ui` tag): no embedded web assets. The binary runs the API
// without serving a UI — useful for dev (Vite serves the UI on :5173 and proxies
// /api here) and for building without Node. Build with `-tags ui` to embed the
// built UI. See web/README.md.
package api

import "io/fs"

// uiFS returns nil — no embedded UI in this build.
func uiFS() fs.FS { return nil }
