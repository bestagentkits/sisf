// Package templates holds the files a factory install is generated from:
// the Compose project of Docker mode and the service-manager units of
// Standalone mode. They are Go text/template files; internal/install fills
// them in and checks the result.
package templates

import "embed"

// FS holds every template, addressed as compose/compose.yaml and
// standalone/<name>.
//
//go:embed compose/compose.yaml standalone/*
var FS embed.FS
