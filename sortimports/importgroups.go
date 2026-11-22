package sortimports

import (
	"sort"
	"strings"
)

// importLine is one import spec: its source text (alias and trailing comment
// included) and its import path as the parser read it.
type importLine struct {
	src  string
	path string
}

// importGroups holds import lines split into three groups: stdlib, local
// (non-stdlib whose first path element has no dot — module names like
// "g" or "kvdbs/redis"), and others (domain-rooted paths like "github.com/...").
type importGroups struct {
	stdlib []importLine
	local  []importLine
	others []importLine
}

// add appends an import line to the appropriate group.
func (g *importGroups) add(srcLine string, importPath string) {
	l := importLine{src: srcLine, path: importPath}
	switch {
	case isStdlib(importPath):
		g.stdlib = append(g.stdlib, l)
	case !strings.Contains(strings.SplitN(importPath, "/", 2)[0], "."):
		g.local = append(g.local, l)
	default:
		g.others = append(g.others, l)
	}
}

// groups returns the three groups in output order.
func (g *importGroups) groups() [][]importLine {
	return [][]importLine{g.stdlib, g.local, g.others}
}

// total returns the total number of imports across all groups.
func (g *importGroups) total() int {
	n := 0
	for _, grp := range g.groups() {
		n += len(grp)
	}
	return n
}

// sort sorts each group alphabetically by import path in place.
func (g *importGroups) sort() {
	for _, grp := range g.groups() {
		sortByImportPath(grp)
	}
}

// sortByImportPath orders lines by import path; two imports of the same path
// (under different names) are ordered by their text, so the result depends
// only on the set of imports, never on the order they came in.
func sortByImportPath(lines []importLine) {
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].path != lines[j].path {
			return lines[i].path < lines[j].path
		}
		return lines[i].src < lines[j].src
	})
}

// formatLines returns the sorted import block as source lines.
// Single import → []string{"import \"foo\""}.
// Multiple → []string{"import (", "\t...", ")"} with a blank line between groups.
// openTail and closeTail are the comments carried after the "(" and the ")";
// with either, the block form is kept even for a single import.
func (g *importGroups) formatLines(openTail, closeTail string) []string {
	g.sort()

	if g.total() == 1 && openTail == "" && closeTail == "" {
		for _, grp := range g.groups() {
			if len(grp) == 1 {
				return []string{"import " + grp[0].src}
			}
		}
	}

	out := []string{"import (" + spaced(openTail)}
	first := true
	for _, grp := range g.groups() {
		if len(grp) == 0 {
			continue
		}
		if !first {
			out = append(out, "")
		}
		first = false
		for _, s := range grp {
			out = append(out, "\t"+s.src)
		}
	}
	out = append(out, ")"+spaced(closeTail))
	return out
}

// spaced returns s preceded by one space, or "" for an empty s.
func spaced(s string) string {
	if s == "" {
		return ""
	}
	return " " + s
}
