package webassets

import "embed"

// Static contains the offline configuration console bundled into the binary.
//
//go:embed static/*
var Static embed.FS
