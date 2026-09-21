package main

import (
	"fmt"
	"os"
)

func stderr(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format, args...)
}

// fail reports and exits. Every failure is exit 1: the command distinguishes
// success from failure, nothing finer.
func fail(format string, args ...any) {
	stderr("error: "+format+"\n", args...)
	os.Exit(1)
}
