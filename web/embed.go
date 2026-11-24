// Package web holds the browser UI. Build ezp.wasm first (see Makefile).
package web

import "embed"

//go:embed index.html app.js wasm_exec.js ezp.wasm fonts
var Files embed.FS
