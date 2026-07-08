// Package web holds the embedded frontend assets.
package web

import "embed"

//go:embed index.html app.html app.js style.css
var Files embed.FS
