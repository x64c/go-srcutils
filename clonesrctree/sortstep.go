package clonesrctree

import (
	"fmt"

	"github.com/x64c/go-srcutils/sortimports"
)

// initSortImports loads the stdlib list once at startup (runs `go list std`,
// inheriting the process env so GOEXPERIMENT is respected). Fails fast.
func initSortImports() error {
	if err := sortimports.LoadStdlib(); err != nil {
		return fmt.Errorf("sort-imports: load stdlib: %v", err)
	}
	return nil
}

// sortStep fixes import order across the target (real runs only) and returns a
// one-line status for the summary.
func sortStep(to string, dry bool) (string, error) {
	if dry {
		return "would run", nil
	}
	if err := sortimports.ProcSrcTree(to); err != nil {
		return "", fmt.Errorf("sort-imports: %v", err)
	}
	return "ok", nil
}
