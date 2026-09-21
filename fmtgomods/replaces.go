package fmtgomods

import (
	"sort"

	"golang.org/x/mod/modfile"
)

// regroupReplaces collects every replace directive — bare lines and block
// entries alike — and re-emits them as one group at the end of the file: a
// bare line when there is exactly one, a sorted parenthesised block when there
// are more. A line's comments travel with it.
//
// No generator maintains this group. A published go.mod carries no replace at
// all (the clone drops them), so a hand-written source tree is the only place
// the layout can drift.
func regroupReplaces(syn *modfile.FileSyntax) {
	var lines []*modfile.Line
	var kept []modfile.Expr
	for _, stmt := range syn.Stmt {
		switch x := stmt.(type) {
		case *modfile.Line:
			if isReplace(x.Token) {
				l := *x
				l.Token = append([]string(nil), x.Token[1:]...) // a block entry carries no verb
				lines = append(lines, &l)
				continue
			}
		case *modfile.LineBlock:
			if isReplace(x.Token) {
				for _, bl := range x.Line {
					l := *bl
					l.Token = append([]string(nil), bl.Token...)
					lines = append(lines, &l)
				}
				continue
			}
		}
		kept = append(kept, stmt)
	}
	switch len(lines) {
	case 0:
		syn.Stmt = kept
	case 1:
		one := lines[0]
		one.Token = append([]string{"replace"}, one.Token...) // a lone directive carries the verb
		syn.Stmt = append(kept, one)
	default:
		sortByOldPath(lines)
		syn.Stmt = append(kept, &modfile.LineBlock{Token: []string{"replace"}, Line: lines})
	}
}

func isReplace(token []string) bool {
	return len(token) > 0 && token[0] == "replace"
}

// sortByOldPath orders replace entries by the module being replaced, which is
// the first token of every form the directive takes.
func sortByOldPath(lines []*modfile.Line) {
	sort.SliceStable(lines, func(i, j int) bool {
		if len(lines[i].Token) == 0 || len(lines[j].Token) == 0 {
			return len(lines[i].Token) < len(lines[j].Token)
		}
		return lines[i].Token[0] < lines[j].Token[0]
	})
}
