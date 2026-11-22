package sortimports

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// parseResult holds the result of parsing a Go source for imports.
type parseResult struct {
	startLine int          // first line of first import (import keyword), -1 if none
	endLine   int          // line after last import block (exclusive)
	groups    importGroups // all imports merged
	openTail  string       // comment after the "(" of the first import block, or ""
	closeTail string       // comment after the ")" of the last import block, or ""
	identity  []string     // package name, then every import as name+" "+path, sorted
}

// hasImports returns true if any import declarations were found.
func (r *parseResult) hasImports() bool {
	return r.startLine >= 0
}

// parseSrcImports finds the import declarations of a Go source with Go's own
// parser (package clause and imports only), records the line range from the
// first declaration to the end of the last, and collects every import spec
// into a single importGroups: its source text from the spec to the end of its
// line (alias and trailing comment included), classified and sorted by its
// import path as the parser read it ("…" and `…` alike).
//
// The lines of that range are later replaced as a whole, so everything on them
// must be accounted for. The parser accounts for the tokens; this function
// checks the rest: each "import", "(", ")" and spec sits on a line of its own,
// with nothing before it and nothing after it but comments that end on that
// line. A layout that breaks this (several things sharing a line, a comment
// running on to the next line) is an error: the file is refused, never
// rewritten with something dropped.
//
// Dropped by design, as before: blank lines and comment-only lines inside the
// range. Kept: the comment after the "(" of the first block and after the ")"
// of the last.
//
// Positions are physical. A //line directive ahead of the imports (generated
// code carries them) shifts reported positions, and must not shift these.
func parseSrcImports(src []byte, lines []string) (parseResult, error) {
	r := parseResult{startLine: -1, endLine: -1}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ImportsOnly)
	if err != nil {
		return r, fmt.Errorf("parse imports: %w", err)
	}
	r.identity = importIdentity(f)
	at := func(p token.Pos) token.Position { return fset.PositionFor(p, false) }
	// before/after return the text of a line left of a position / from it on.
	before := func(p token.Position) string { return lines[p.Line-1][:p.Column-1] }
	after := func(p token.Position) string { return lines[p.Line-1][p.Column-1:] }

	sawBlock := false
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		kw := at(gd.Pos())
		if strings.TrimSpace(before(kw)) != "" {
			return r, fmt.Errorf("line %d: \"import\" does not start its line (gofmt the file first)", kw.Line)
		}
		if r.startLine < 0 {
			r.startLine = kw.Line - 1
		}
		afterKw := after(kw)[len("import"):] // what follows the keyword on its line

		if !gd.Lparen.IsValid() {
			// import "path"  |  import name "path"
			spec := gd.Specs[0].(*ast.ImportSpec)
			sp := at(spec.Pos())
			if sp.Line != kw.Line || strings.TrimSpace(before(sp)[kw.Column-1+len("import"):]) != "" {
				return r, fmt.Errorf("line %d: \"import\" and import %s are not side by side on one line (gofmt the file first)", kw.Line, spec.Path.Value)
			}
			if err := r.addSpec(spec, at, after); err != nil {
				return r, err
			}
			r.endLine = kw.Line
			continue
		}

		// import ( ... )
		lp, rp := at(gd.Lparen), at(gd.Rparen)
		if lp.Line != kw.Line || strings.TrimSpace(afterKw[:lp.Column-kw.Column-len("import")]) != "" {
			return r, fmt.Errorf("line %d: \"import\" and \"(\" are not side by side on one line (gofmt the file first)", kw.Line)
		}
		r.endLine = rp.Line
		if len(gd.Specs) == 0 {
			// import (): nothing to carry, but code after it on its line would be lost
			if _, err := lineTail(after(rp)[1:]); err != nil {
				return r, fmt.Errorf("line %d: \")\" %w", rp.Line, err)
			}
			continue
		}
		open, err := lineTail(after(lp)[1:])
		if err != nil {
			return r, fmt.Errorf("line %d: \"(\" %w", lp.Line, err)
		}
		if strings.TrimSpace(before(rp)) != "" {
			return r, fmt.Errorf("line %d: \")\" does not start its line (gofmt the file first)", rp.Line)
		}
		closing, err := lineTail(after(rp)[1:])
		if err != nil {
			return r, fmt.Errorf("line %d: \")\" %w", rp.Line, err)
		}
		if !sawBlock {
			sawBlock = true
			r.openTail = open
		}
		r.closeTail = closing

		for _, s := range gd.Specs {
			spec := s.(*ast.ImportSpec)
			if strings.TrimSpace(before(at(spec.Pos()))) != "" {
				return r, fmt.Errorf("line %d: import %s does not start its line (gofmt the file first)", at(spec.Pos()).Line, spec.Path.Value)
			}
			if err := r.addSpec(spec, at, after); err != nil {
				return r, err
			}
		}
	}
	return r, nil
}

// addSpec collects one import spec: the text from the spec to the end of its
// line, under its unquoted path. What follows the spec on its line must be
// comments that end there.
func (r *parseResult) addSpec(spec *ast.ImportSpec, at func(token.Pos) token.Position, after func(token.Position) string) error {
	path, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		return fmt.Errorf("import %s: %w", spec.Path.Value, err)
	}
	sp, ep := at(spec.Pos()), at(spec.End())
	if ep.Line != sp.Line {
		return fmt.Errorf("line %d: import %s spans lines (gofmt the file first)", sp.Line, spec.Path.Value)
	}
	if _, err := lineTail(after(ep)); err != nil {
		return fmt.Errorf("line %d: import %s %w", sp.Line, spec.Path.Value, err)
	}
	r.groups.add(strings.TrimSpace(after(sp)), path)
	return nil
}

// lineTail checks the text that follows a token up to the end of its line and
// returns it, trimmed. It may hold comments only, and they must end on this
// line: whole lines are moved, so code sharing the line, or a comment running
// on to the next line, cannot be carried with them.
func lineTail(rest string) (string, error) {
	rest = strings.TrimSpace(rest)
	for s := rest; s != ""; {
		switch {
		case strings.HasPrefix(s, "//"):
			return rest, nil
		case strings.HasPrefix(s, "/*"):
			end := strings.Index(s[2:], "*/")
			if end < 0 {
				return "", errors.New("is followed by a comment that runs on to the next line")
			}
			s = strings.TrimSpace(s[2+end+2:])
		default:
			return "", errors.New("is followed by code on the same line (gofmt the file first)")
		}
	}
	return rest, nil
}

// importIdentity is what sorting must never change: the package name and the
// set of imports (name and path of each), in a canonical order. ProcSrcFile
// compares it before and after as its last line of defense: whatever the
// layout, a result that would not import exactly the same packages is refused.
func importIdentity(f *ast.File) []string {
	id := make([]string, 0, len(f.Imports))
	for _, spec := range f.Imports {
		name := ""
		if spec.Name != nil {
			name = spec.Name.Name
		}
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			path = spec.Path.Value
		}
		id = append(id, name+" "+path)
	}
	sort.Strings(id)
	return append([]string{"package " + f.Name.Name}, id...)
}

// sourceIdentity returns the importIdentity of a Go source.
func sourceIdentity(src []byte) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	return importIdentity(f), nil
}
