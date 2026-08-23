// Package version holds kevin's version string, embedded from a file the
// release process writes and commits before tagging. internal/cmd and
// internal/relay both read it - internal/relay can't import internal/cmd
// without an import cycle (internal/cmd imports internal/engine, which
// imports internal/relay) - so it lives here instead, as the one source
// both derive from.
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var versionFile string

// String is kevin's version.
var String = strings.TrimSpace(versionFile)
