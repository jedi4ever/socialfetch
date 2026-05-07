// Package web ships the embedded static assets (HTML / JS / CSS)
// the `social-ui serve` HTTP handler serves. //go:embed bundles
// every file under static/ into the binary so a single `go build`
// produces a runnable artifact — operators don't need to manage
// a separate web/ directory at install time.
package web

import "embed"

// Files is the embedded static asset tree. Mounted at /static/
// by cmd_serve.go (with index.html served by the / root handler).
//
//go:embed static
var Files embed.FS
