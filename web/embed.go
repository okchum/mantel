// Package web embeds the dashboard page so the server ships as one binary.
package web

import "embed"

//go:embed index.html app.css app.js js fonts
var FS embed.FS
