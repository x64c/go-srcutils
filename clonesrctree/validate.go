package clonesrctree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// validate checks the resolved config and canonicalizes -from/-to.
func validate(cfg *Config) error {
	if cfg.From == "" || cfg.To == "" {
		return fmt.Errorf("-from and -to are required")
	}

	fa, err := filepath.Abs(cfg.From)
	if err != nil {
		return fmt.Errorf("-from: %v", err)
	}
	ta, err := filepath.Abs(cfg.To)
	if err != nil {
		return fmt.Errorf("-to: %v", err)
	}
	cfg.From, cfg.To = fa, ta

	if err := mustDir(fa, "-from"); err != nil {
		return err
	}
	if err := mustDir(ta, "-to"); err != nil {
		return err
	}

	if within(ta, fa) || within(fa, ta) {
		return fmt.Errorf("-from and -to must not be nested within each other")
	}

	for _, e := range cfg.Exclude {
		p := filepath.Join(fa, filepath.FromSlash(strings.Trim(e, "/")))
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			return fmt.Errorf("exclude %q: no such directory under from", e)
		}
	}
	return nil
}

func mustDir(path, label string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s %s: %v", label, path, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("%s %s is not a directory", label, path)
	}
	return nil
}

// within reports whether a is inside or equal to b.
func within(a, b string) bool {
	rel, err := filepath.Rel(b, a)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
