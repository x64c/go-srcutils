package sortimports

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProcSrcFile reads a Go source file, sorts its imports,
// and atomically replaces the file only if imports changed.
// Returns true if the file was modified.
//
// A file it cannot sort safely is an error and is left untouched: one whose
// package clause or imports do not parse, or whose import layout cannot be
// carried line by line (see parseSrcImports). A cgo file (one that imports
// "C") is never sorted and is not an error (see importsC).
func ProcSrcFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	r, err := parseSrcImports(data, lines)
	if err != nil {
		return false, err
	}
	if !r.hasImports() || r.groups.total() == 0 {
		return false, nil // no imports, or only an empty import (): nothing to sort
	}

	formatted := r.groups.formatLines(r.openTail, r.closeTail)
	if strings.HasSuffix(lines[r.startLine], "\r") {
		// CRLF file: lines were split on "\n", so each still ends in "\r"
		for i := range formatted {
			formatted[i] += "\r"
		}
	}

	// Check if imports are already sorted
	oldImportLines := lines[r.startLine:r.endLine]
	if linesEqual(oldImportLines, formatted) {
		return false, nil
	}

	// The new content: the import range replaced, every other line as it was.
	newLines := make([]string, 0, len(lines)-len(oldImportLines)+len(formatted))
	newLines = append(newLines, lines[:r.startLine]...)
	newLines = append(newLines, formatted...)
	newLines = append(newLines, lines[r.endLine:]...)
	out := strings.Join(newLines, "\n")

	// Last line of defense. Sorting moves import lines; it never adds, removes
	// or changes an import. So the result must declare the same package and
	// import exactly the same packages under the same names. If it would not,
	// whatever the reason, nothing is written.
	got, err := sourceIdentity([]byte(out))
	if err != nil {
		return false, fmt.Errorf("refusing to write: the sorted file would not parse (%v); file left unchanged", err)
	}
	if !linesEqual(got, r.identity) {
		return false, fmt.Errorf("refusing to write: the sorted file would not import the same packages; file left unchanged")
	}

	// Write to temp file in the same directory (for atomic rename)
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sort-imports-*.go")
	if err != nil {
		return false, fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		// Clean up temp file on failure
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmp.WriteString(out); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temp: %w", err)
	}

	// Preserve original file permissions
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("stat: %w", err)
	}
	if err := os.Chmod(tmpPath, info.Mode()); err != nil {
		return false, fmt.Errorf("chmod: %w", err)
	}

	// Atomic replace
	if err := os.Rename(tmpPath, path); err != nil {
		return false, fmt.Errorf("rename: %w", err)
	}

	return true, nil
}

// linesEqual compares two string slices.
func linesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
