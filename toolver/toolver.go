// Package toolver reports a tool's version from the build info embedded by the
// Go toolchain — the same truth `go version -m <binary>` reads. Installed
// binaries report their exact module tag; source builds report (devel).
//
// The name comes from the main PACKAGE path and the version from the module,
// because several commands share one module: taking both from the module would
// give every tool the module's own name.
package toolver

import (
	"fmt"
	"path"
	"runtime"
	"runtime/debug"
)

func String() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown (no build info)"
	}
	return fmt.Sprintf("%s %s (%s, %s)",
		path.Base(bi.Path), bi.Main.Version, bi.Path, runtime.Version())
}

func Print() {
	fmt.Println(String())
}
