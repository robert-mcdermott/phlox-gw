package phloxgw

import "embed"

// Frontend contains the built dashboard served by the Go binary.
//
//go:embed frontend/dist/*
var Frontend embed.FS
