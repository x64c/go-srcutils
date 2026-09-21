package clonesrctree

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// goworkPlan captures the workspace shape of the run, settled before the
// pipeline touches anything. Two interweaved concepts, kept apart:
// go.work GENERATION is clone content (driven by the source's shape, the
// keeps, and the conf's buildtest question); the workspace BUILDTEST is a
// question asked afterwards (build.go).
//
// A target workspace is a function of the CLONE GEOMETRY, not of the source's
// own layout: which modules exist is the importmap's answer, where they land
// is the tree map's, and the version bridges are the conf's. So a source
// go.work is not required to produce one — asking the workspace question is
// enough.
//
// Source go.work count:
//
//	0    — nothing to carry. A kept target-only go.work survives, an unkept
//	       one is stale — UNLESS buildtest is "workspace", in which case the
//	       target workspace is generated at the target root from the geometry.
//	1    — the workspace lives at srcRel; the target counterpart lives at the
//	       corresponding location tgtRel. Kept there → hand-maintained, no
//	       generation (a kept go.work anywhere ELSE = mismatch, hard error).
//	       Not kept → generated at tgtRel, carrying the source's go line and
//	       any replaces that do not target family modules.
//	many — the tree is mid-surgery (e.g. an incomplete merge): cloning only,
//	       verbatim; ANY buildtest = error — no verdict on it is trustworthy.
type goworkPlan struct {
	srcRels  []string // every go.work in the source tree, source-relative
	tgtRel   string   // single-workspace case: target-relative location
	generate bool     // single case, not kept: we own the target go.work
	kept     bool     // single case, kept at tgtRel: hand-maintained
}

func (p goworkPlan) count() int { return len(p.srcRels) }

// planGoWork discovers source go.work files, resolves the target location and
// the keep interplay, and runs the fail-fast validations (kept-path mismatch;
// buildtest askability lives in build.go against this plan).
func planGoWork(cfg Config, keeps, skips patternSet, tm treeMap) (goworkPlan, error) {
	var plan goworkPlan
	_ = filepath.WalkDir(cfg.From, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := relOf(cfg.From, p)
		if d.IsDir() {
			if rel != "." && skips.protects(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == "go.work" {
			plan.srcRels = append(plan.srcRels, rel)
		}
		return nil
	})
	sort.Strings(plan.srcRels)

	if plan.count() > 1 {
		return plan, nil // mid-surgery: cloning only, no plan to make
	}

	if plan.count() == 0 {
		// No source workspace to correspond to. The target one is still fully
		// determined by the clone geometry, so generate it at the target root
		// when the conf asks the workspace question and there is an importmap
		// to read the geometry from.
		if cfg.BuildTest == "workspace" && cfg.ImportMap != nil {
			plan.tgtRel = "go.work"
			plan.kept = keeps.protects(plan.tgtRel)
			plan.generate = !plan.kept
		}
		return plan, nil
	}

	srcRel := plan.srcRels[0]
	// Corresponding location: mapped when the go.work sits inside a module
	// subtree, else the same relative path (dirs outside modules keep their
	// names — only module dirs are renamed by the geometry).
	if t, ok := tm.srcToTarget(srcRel); ok {
		plan.tgtRel = t
	} else {
		plan.tgtRel = srcRel
	}
	plan.kept = keeps.protects(plan.tgtRel)
	plan.generate = !plan.kept && cfg.ImportMap != nil

	// A kept go.work anywhere other than the corresponding location breaks
	// the source↔target workspace correspondence: refuse loudly. (Unkept
	// strays are ordinary stale files; delete-stale handles them.)
	_ = filepath.WalkDir(cfg.To, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := relOf(cfg.To, p)
		if d.IsDir() {
			return nil
		}
		if d.Name() == "go.work" && rel != plan.tgtRel && keeps.protects(rel) {
			return fmt.Errorf("go.work mismatch: source workspace is %s (target %s), but target keeps a go.work at %s",
				srcRel, plan.tgtRel, rel)
		}
		return nil
	})
	return plan, nil
}

// goworkOwned reports whether the plan owns the target-relative path rel as
// workspace machinery: the generated go.work and its go.work.sum (which
// follows its sibling go.work's fate — never copied, never deleted
// independently, refreshed by workspace-mode builds).
func (p goworkPlan) goworkOwned(rel string) bool {
	if !p.generate && !p.kept {
		return false
	}
	dir := path.Dir(p.tgtRel)
	return rel == p.tgtRel || rel == path.Join(dir, "go.work.sum")
}

// generateGoWork builds the target go.work from the source one: use dirs are
// mapped through the tree geometry, entries whose module falls outside the
// clone domain (excluded) are dropped, replaces targeting mapped modules are
// dropped, the go line is carried. Returns the content and a dry-run diff tag.
//
// Family confs (importprefix + version) additionally get a version-bridge
// replace per CONSUMED workspace module — one whose path some generated
// go.mod pins — a bare directive for one, a parenthesized block for more
// (single-imports rule):
//
//	replace <module> <version> => ./<dir>
//
// A `use` directive supplies a module's CONTENT but does not satisfy the
// version REQUIREMENT another workspace module's go.mod declares — on a
// family version bump those pins point at a not-yet-published tag, and the
// module graph fails to load for the whole workspace. The bridge redirects
// the pinned version to the local dir, so a workspace buildtest can verify
// the family BEFORE its tags exist. Inert once the tags are published.
// Unconsumed modules get NO bridge: no requirement edge exists for one to
// redirect, and the dead directive draws "unused replace" warnings in IDEs
// (whose quick-fix removal would then fight the generator forever).
func generateGoWork(cfg Config, tm treeMap, plan goworkPlan, srcMods []string, keeps patternSet, consumed map[string]bool) ([]byte, string, error) {
	// The source workspace, when there is one, contributes its go line and any
	// replaces that do not target family modules. It does NOT decide membership:
	// every cloned module joins the workspace, because that is what the clone
	// produced and what the buildtest must cover.
	var wf *modfile.WorkFile
	if plan.count() == 1 {
		srcPath := filepath.Join(cfg.From, filepath.FromSlash(plan.srcRels[0]))
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %v", srcPath, err)
		}
		wf, err = modfile.ParseWork(srcPath, data, nil)
		if err != nil {
			return nil, "", fmt.Errorf("parse %s: %v", srcPath, err)
		}
	}

	tgtDir := path.Dir(plan.tgtRel)
	var uses []string
	var bridges []string // family mode: "<module> <version> => ./<dir>" per workspace module
	for _, srcModRel := range srcMods {
		tgtModRel, ok := tm.srcToTarget(srcModRel)
		if !ok || isKeptPath(keeps, path.Join(tgtModRel, "go.mod")) {
			continue // excluded / outside the clone domain, or hand-maintained
		}
		rel, rerr := filepath.Rel(tgtDir, tgtModRel)
		if rerr != nil {
			return nil, "", fmt.Errorf("gowork use %s: %v", tgtModRel, rerr)
		}
		uses = append(uses, "./"+filepath.ToSlash(rel))
		if cfg.ImportPrefix != "" && cfg.Version != "" && consumed[cfg.ImportPrefix+tgtModRel] {
			bridges = append(bridges, cfg.ImportPrefix+tgtModRel+" "+cfg.Version+" => ./"+filepath.ToSlash(rel))
		}
	}
	sort.Strings(uses)
	sort.Strings(bridges)

	toSet := map[string]bool{}
	for _, e := range cfg.ImportMap {
		toSet[e.To] = true
	}

	var b bytes.Buffer
	switch {
	case wf != nil && wf.Go != nil:
		b.WriteString("go " + wf.Go.Version + "\n")
	default:
		// No source workspace to carry a go line: a workspace must be at least
		// as new as every module it uses, so take the highest of theirs.
		if v := maxModuleGoVersion(cfg, srcMods); v != "" {
			b.WriteString("go " + v + "\n")
		}
	}
	if len(uses) > 0 {
		b.WriteString("\nuse (\n")
		for _, u := range uses {
			b.WriteString("\t" + u + "\n")
		}
		b.WriteString(")\n")
	}
	writeReplaces(&b, bridges)
	var carried []string // source replaces that survive: not targeting family modules
	for _, rp := range workReplaces(wf) {
		if _, isKey := cfg.ImportMap[rp.Old.Path]; isKey || toSet[rp.Old.Path] {
			continue // family replace: dropped, same rule as go.mod generation
		}
		line := rp.Old.Path
		if rp.Old.Version != "" {
			line += " " + rp.Old.Version
		}
		line += " => " + rp.New.Path
		if rp.New.Version != "" {
			line += " " + rp.New.Version
		}
		carried = append(carried, line)
	}
	writeReplaces(&b, carried)

	content := b.Bytes()
	existing, rerr := os.ReadFile(filepath.Join(cfg.To, filepath.FromSlash(plan.tgtRel)))
	diff := "new"
	if rerr == nil {
		diff = "differs"
		if bytes.Equal(existing, content) {
			diff = "identical"
		}
	}
	return content, diff, nil
}

// workReplaces returns wf's replaces, or none when there is no source
// workspace to carry any.
func workReplaces(wf *modfile.WorkFile) []*modfile.Replace {
	if wf == nil {
		return nil
	}
	return wf.Replace
}

// maxModuleGoVersion returns the highest go directive among the source
// modules — the lowest language version a workspace over them can declare.
// Empty when no module declares one.
func maxModuleGoVersion(cfg Config, srcMods []string) string {
	var best string
	for _, relDir := range srcMods {
		p := filepath.Join(cfg.From, filepath.FromSlash(relDir), "go.mod")
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		mf, err := modfile.Parse(p, data, nil)
		if err != nil || mf.Go == nil {
			continue
		}
		if best == "" || goVersionLess(best, mf.Go.Version) {
			best = mf.Go.Version
		}
	}
	return best
}

// goVersionLess compares dotted go versions ("1.26", "1.26.5") numerically,
// segment by segment — string order would rank "1.9" above "1.26".
func goVersionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// writeReplaces emits replace directives: a single one gets the bare directive
// form, two or more get a parenthesized block — same rule sort-imports applies
// to import declarations.
func writeReplaces(b *bytes.Buffer, lines []string) {
	switch len(lines) {
	case 0:
	case 1:
		b.WriteString("\nreplace " + lines[0] + "\n")
	default:
		b.WriteString("\nreplace (\n")
		for _, l := range lines {
			b.WriteString("\t" + l + "\n")
		}
		b.WriteString(")\n")
	}
}

// goworkStep runs go.work generation (real) or planning (dry) and returns a
// one-line status for the summary.
func goworkStep(cfg Config, tm treeMap, plan goworkPlan, srcMods []string, keeps patternSet, consumed map[string]bool, dry bool) (string, error) {
	switch {
	case plan.count() > 1:
		return "verbatim mirror only (source has " + strconv.Itoa(plan.count()) + " go.work files)", nil
	case plan.kept:
		return plan.tgtRel + " kept (hand-maintained)", nil
	case plan.generate:
		// fall through to generation below
	case plan.count() == 0:
		return "none (no source go.work; buildtest is not workspace)", nil
	default:
		return plan.tgtRel + " copied verbatim (no importmap)", nil
	}
	content, diff, err := generateGoWork(cfg, tm, plan, srcMods, keeps, consumed)
	if err != nil {
		return "", err
	}
	if dry {
		return plan.tgtRel + " would generate  [" + diff + "]", nil
	}
	out := filepath.Join(cfg.To, filepath.FromSlash(plan.tgtRel))
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", fmt.Errorf("mkdir for %s: %v", plan.tgtRel, err)
	}
	if err := os.WriteFile(out, content, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %v", plan.tgtRel, err)
	}
	return plan.tgtRel + " generated  [" + diff + "]", nil
}
