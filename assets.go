// Package doupro exposes the embedded web assets (web/templates,
// web/static). go:embed patterns cannot climb out of their package
// directory with "..", so the module root — the only package directory
// that is an ancestor of web/ — is where these embeds have to live; see
// docs/architecture.md for the repo layout this is keeping intact.
// internal/web receives these as fs.FS parameters rather than embedding
// them itself.
package doupro

import "embed"

//go:embed web/templates/*.html
var TemplatesFS embed.FS

//go:embed web/static
var StaticFS embed.FS

// ManualsFS embeds the end-user docs rendered by the in-app Manual
// section (internal/web) — see manuals/README.md.
//
//go:embed manuals/*.md
var ManualsFS embed.FS
