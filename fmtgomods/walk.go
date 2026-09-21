package fmtgomods

import (
	"io/fs"
	"path/filepath"
)

// Tree formats every go.mod under root and returns the paths it changed, in
// walk order. Directories a module never owns are skipped: vendor, testdata,
// and any dot directory.
func Tree(root string) ([]string, error) {
	var changed []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "go.mod" {
			return nil
		}
		did, ferr := File(path)
		if ferr != nil {
			return ferr
		}
		if did {
			changed = append(changed, path)
		}
		return nil
	})
	return changed, err
}

func skipDir(name string) bool {
	if name == "vendor" || name == "testdata" {
		return true
	}
	return len(name) > 1 && name[0] == '.'
}
