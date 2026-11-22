package clonesrctree

import (
	"bytes"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// rwPair is one import-path mapping: an import of the module path label, or of
// a package under label/, is rewritten onto to. Pairs are matched against the
// import paths of parsed import declarations, never against raw file text: a
// string literal, struct tag or comment that merely reads like a module path
// is not an import and must not change.
type rwPair struct {
	label string // importmap key: the source module path (also used in messages)
	to    string // the mapped module path
}

// importPairs derives the import rewrites from the importmap, one per key,
// longest key first so a longer key is never shadowed by a shorter prefix.
func importPairs(im map[string]importEntry) []rwPair {
	keys := make([]string, 0, len(im))
	for k := range im {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	pairs := make([]rwPair, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, rwPair{label: k, to: im[k].To})
	}
	return pairs
}

// rewriteTarget rewrites the mapped import paths of every non-protected target
// .go file. Only import declarations change; every other byte is left as is.
func rewriteTarget(to string, keeps patternSet, pairs []rwPair, res *result, verbose bool) error {
	if len(pairs) == 0 {
		return nil
	}
	err := filepath.WalkDir(to, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := relOf(to, p)
		if rel == "." {
			return nil
		}
		if keeps.protects(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out, err := rewriteImports(data, pairs)
		if err != nil {
			return fmt.Errorf("%s: %v", rel, err)
		}
		if bytes.Equal(out, data) {
			return nil
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(p, out, info.Mode().Perm()); err != nil {
			return err
		}
		res.rewritten = append(res.rewritten, rel)
		if verbose {
			stderr("  rewrite %s\n", rel)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("rewrite: %v", err)
	}
	sort.Strings(res.rewritten)
	return nil
}

// planRewrites reports which files a real run would rewrite, scanning source
// (the post-copy content) rather than the untouched target. Reported paths are
// target-relative (where the rewrite would land). A source file whose imports
// cannot be parsed is an error, named by its source-relative path; a real run
// asks this first, as its preflight, so that error surfaces before anything is
// touched rather than halfway through the rewrite.
func planRewrites(from string, keeps, skips patternSet, tm treeMap, pairs []rwPair, res *result) error {
	if len(pairs) == 0 {
		return nil
	}
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := relOf(from, p)
		if rel == "." {
			return nil
		}
		if skips.protects(rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		trel, inDomain := tm.srcToTarget(rel)
		if d.IsDir() {
			if !inDomain {
				if tm.domainAncestor(rel) {
					return nil
				}
				return fs.SkipDir
			}
			if keeps.protects(trel) {
				return fs.SkipDir
			}
			return nil
		}
		if !inDomain || keeps.protects(trel) || !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		hits, err := importHits(data, pairs)
		if err != nil {
			return fmt.Errorf("%s: %v", rel, err)
		}
		if len(hits) > 0 {
			res.rewritten = append(res.rewritten, trel)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("plan rewrites: %v", err)
	}
	sort.Strings(res.rewritten)
	return nil
}

// verifyResidue scans the import declarations of all target .go files (rewrite
// excludes protected paths; verification deliberately does not) for any import
// path still under a mapped key.
func verifyResidue(to string, pairs []rwPair) ([]string, error) {
	var hits []string
	if len(pairs) == 0 {
		return nil, nil
	}
	err := filepath.WalkDir(to, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel := relOf(to, p)
		lines, err := importHits(data, pairs)
		if err != nil {
			return fmt.Errorf("%s: %v", rel, err)
		}
		src := strings.Split(string(data), "\n")
		for _, n := range lines {
			hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, n, strings.TrimSpace(src[n-1])))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("verify: %v", err)
	}
	sort.Strings(hits)
	return hits, nil
}

// importLit is one import path literal in a source file.
type importLit struct {
	start, end int    // byte range of the literal, quotes included
	line       int    // physical line it is on, 1-based
	path       string // the import path it spells
}

// importLits locates every import path literal in src by parsing its package
// clause and import declarations with Go's own parser. In Go source a module
// path is import-path text in exactly one place, the import declaration, so
// this is the only place the rewrite ever looks.
//
// Positions are physical: a //line directive ahead of the imports (generated
// code carries them) shifts reported positions, and must not shift these.
func importLits(src []byte) ([]importLit, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("parse imports: %v", err)
	}
	lits := make([]importLit, 0, len(f.Imports))
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value) // "…" and `…` alike
		if err != nil {
			return nil, fmt.Errorf("import %s: %v", spec.Path.Value, err)
		}
		start := fset.PositionFor(spec.Path.Pos(), false)
		end := fset.PositionFor(spec.Path.End(), false)
		lits = append(lits, importLit{start: start.Offset, end: end.Offset, line: start.Line, path: path})
	}
	return lits, nil
}

// mappedPath returns path rewritten onto the first (longest) key it falls
// under: the key itself, or a package under key/.
func mappedPath(path string, pairs []rwPair) (string, bool) {
	for _, pr := range pairs {
		if path == pr.label || strings.HasPrefix(path, pr.label+"/") {
			return pr.to + path[len(pr.label):], true
		}
	}
	return "", false
}

// rewriteImports returns src with every mapped import path rewritten and every
// other byte untouched. A rewritten path is written as an interpreted string
// ("…"), the form gofmt gives every import path.
func rewriteImports(src []byte, pairs []rwPair) ([]byte, error) {
	lits, err := importLits(src)
	if err != nil {
		return nil, err
	}
	out := src
	for i := len(lits) - 1; i >= 0; i-- { // back to front: earlier offsets stay valid
		l := lits[i]
		np, ok := mappedPath(l.path, pairs)
		if !ok {
			continue
		}
		out = append(append(append([]byte{}, out[:l.start]...), strconv.Quote(np)...), out[l.end:]...)
	}
	return out, nil
}

// importHits returns the lines of the import paths in src that fall under a
// mapped key.
func importHits(src []byte, pairs []rwPair) ([]int, error) {
	lits, err := importLits(src)
	if err != nil {
		return nil, err
	}
	var lines []int
	for _, l := range lits {
		if _, ok := mappedPath(l.path, pairs); ok {
			lines = append(lines, l.line)
		}
	}
	return lines, nil
}
