package fmtgomods

import (
	"fmt"
	"os"

	"golang.org/x/mod/modfile"
)

// File rewrites one go.mod into the canonical layout and reports whether the
// file changed. A file already in that layout is left untouched, so a second
// run over the same file changes nothing.
func File(path string) (bool, error) {
	before, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	f, err := modfile.Parse(path, before, nil)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}

	// Requires: the toolchain's own layout — one group for directs, one for
	// indirects, parentheses only where a group holds more than one.
	f.SetRequireSeparateIndirect(f.Require)
	regroupReplaces(f.Syntax)
	f.SortBlocks()
	f.Cleanup()

	after, err := f.Format()
	if err != nil {
		return false, fmt.Errorf("format %s: %w", path, err)
	}
	if string(after) == string(before) {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, after, info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}
