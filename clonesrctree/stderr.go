package clonesrctree

import (
	"fmt"
	"os"
)

// stderr writes the run's progress commentary. Failures are returned, never
// printed here: what to do about one is the caller's decision.
func stderr(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format, args...)
}
