package publishfamily

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

// entry is where a module ENTERS its pub cycle, found by the reverse-order
// precheck: probe from the deepest published state backward; the first "yes"
// wins. Each state is exactly one crash window of the step sequence
// tidy -> commit -> tag -> push -> nudge -> wait.
//
//	landed on proxy?           yes -> entryDone  (nothing to do)
//	  (asked ONLY when the remote tag exists — no remote tag means the proxy
//	   CANNOT know the version, and probing it anyway plants a negative-cached
//	   404 that outlives the honest nudges after the push)
//	tag on remote?             yes -> entryAwait (nudge + wait)
//	tag local only?            yes -> entryPush  (push tag, then await)
//	module dir clean?          yes -> entryTag   (commit exists; tag onward)
//	otherwise                       -> entryFresh (full cycle from tidy)
type entry int

const (
	entryDone entry = iota
	entryAwait
	entryPush
	entryTag
	entryFresh
)

func (e entry) String() string {
	return [...]string{"DONE", "await", "push tag", "tag", "fresh"}[e]
}

// modState is one module's precheck verdict.
type modState struct {
	dir   string
	tag   string // <dir>/<version>
	entry entry
}

// Plan is the settled preview of the whole run, derived from the conf + the
// repo + (for pushed tags only) the proxy — never from tool-local state.
type Plan struct {
	version       string
	versionSource string // how version was settled: inferred, or given by -version
	waves         [][]string
	states        map[string]*modState
	rootDirt      []string // dirty paths in the family root owned by no module: commit #0 material
	outsideDirt   []string // dirty paths outside the family root: reported, never staged
}

// preflight reads the world and validates it against the conf's instruction.
//
// The family version is INFERRED: after a family sync every consumer go.mod
// carries the same "hoping require" for its family deps (a version that may
// not be published yet — that's the point). All hoping requires must be
// unanimous; -version (assertVersion) asserts it — and is the only source when a
// family has no internal requires to infer from (a single-module family).
// The conf's explicit dependency lists must exactly match each go.mod's
// actual family requires — instruction and reality agree, or nothing runs.
// Preflight reads the world and validates it against the conf's instruction,
// returning the settled Plan or the first disagreement it finds.
func Preflight(cfg Config, assertVersion string) (Plan, error) {
	p := Plan{states: map[string]*modState{}}

	version := ""
	versionFrom := ""
	for dir := range cfg.Modules {
		gomodPath := filepath.Join(cfg.Repo, filepath.FromSlash(dir), "go.mod")
		data, err := os.ReadFile(gomodPath)
		if err != nil {
			return Plan{}, fmt.Errorf("read %s: %v", gomodPath, err)
		}
		mf, err := modfile.Parse(gomodPath, data, nil)
		if err != nil {
			return Plan{}, fmt.Errorf("parse %s: %v", gomodPath, err)
		}
		if mf.Module == nil || mf.Module.Mod.Path != cfg.modPath(dir) {
			return Plan{}, fmt.Errorf("module %s: go.mod declares %q, conf implies %q",
				dir, mf.Module.Mod.Path, cfg.modPath(dir))
		}
		famReqs := map[string]string{} // dep dir -> required version
		for _, r := range mf.Require {
			d, ok := cfg.famDir(r.Mod.Path)
			if !ok {
				continue
			}
			famReqs[d] = r.Mod.Version
		}
		// instruction vs reality, both directions
		for _, d := range cfg.Modules[dir] {
			v, ok := famReqs[d]
			if !ok {
				return Plan{}, fmt.Errorf("module %s: conf declares dep %q but go.mod has no such family require", dir, d)
			}
			if version == "" {
				version, versionFrom = v, dir+" -> "+d
			} else if v != version {
				return Plan{}, fmt.Errorf("family not coherently bumped: %s requires %s@%s, but %s says %s — resync",
					dir, d, v, versionFrom, version)
			}
			delete(famReqs, d)
		}
		if len(famReqs) > 0 {
			var extra []string
			for d := range famReqs {
				extra = append(extra, d)
			}
			sort.Strings(extra)
			return Plan{}, fmt.Errorf("module %s: go.mod requires family module(s) the conf does not declare: %s",
				dir, strings.Join(extra, ", "))
		}
	}
	versionSource := "inferred from hoping requires"
	if version == "" {
		if assertVersion == "" {
			return Plan{}, fmt.Errorf("no family-internal requires exist — version cannot be inferred; pass -version")
		}
		// a single-module family has no family-internal require to read a
		// version from, so -version is the only source there is
		version, versionSource = assertVersion, "given by -version"
	}
	if !semver.IsValid(version) {
		return Plan{}, fmt.Errorf("inferred version %q is not valid semver", version)
	}
	if assertVersion != "" && assertVersion != version {
		return Plan{}, fmt.Errorf("-version %s does not match the version the repo is bumped to (%s)", assertVersion, version)
	}
	p.version = version
	p.versionSource = versionSource
	waves, err := cfg.waves()
	if err != nil {
		return Plan{}, err
	}
	p.waves = waves

	// dirty inventory, partitioned by ownership; outside the family root =
	// another family's business — never staged, but never silent either
	dirtyByDir := map[string]bool{}
	dirty, err := gitDirty(cfg.Repo)
	if err != nil {
		return Plan{}, err
	}
	for _, rel := range dirty {
		switch owner, owned := cfg.ownedBy(rel); {
		case owned:
			dirtyByDir[owner] = true
		case cfg.underFamilyRoot(rel):
			p.rootDirt = append(p.rootDirt, rel)
		default:
			p.outsideDirt = append(p.outsideDirt, rel)
		}
	}
	sort.Strings(p.rootDirt)
	sort.Strings(p.outsideDirt)

	// reverse-order precheck per module (see the entry ladder above)
	remoteTags, err := gitRemoteTags(cfg.Repo)
	if err != nil {
		return Plan{}, err
	}
	for dir := range cfg.Modules {
		// a module at the family root carries a bare tag; one in a
		// subdirectory carries that path, as Go requires of both
		tag := version
		if dir != "" {
			tag = dir + "/" + version
		}
		st := &modState{dir: dir, tag: tag}
		switch {
		case remoteTags[st.tag]:
			landed, lerr := proxyLanded(cfg.modPath(dir), version)
			if lerr != nil {
				return Plan{}, lerr
			}
			if landed {
				st.entry = entryDone
				if dirtyByDir[dir] {
					return Plan{}, fmt.Errorf("module %s: %s is LANDED on the proxy but the dir has uncommitted changes — a landed version is immutable; resolve by hand",
						dir, st.tag)
				}
			} else {
				st.entry = entryAwait
			}
		case tagLocal(cfg.Repo, st.tag, &err):
			if err != nil {
				return Plan{}, err
			}
			st.entry = entryPush
		case !dirtyByDir[dir]:
			st.entry = entryTag
		default:
			st.entry = entryFresh
		}
		p.states[dir] = st
	}
	return p, nil
}

// tagLocal answers the switch's question while keeping the error reachable:
// a case expression has nowhere to return one.
func tagLocal(repo, tag string, out *error) bool {
	exists, err := gitTagExists(repo, tag)
	if err != nil {
		*out = err
		return true // take this branch so the caller sees the error immediately
	}
	return exists
}

func (p Plan) Print(cfg Config) {
	stderr("family version: %s (%s)\n", p.version, p.versionSource)
	stderr("waves (each module enters at its prechecked step):\n")
	for i, wave := range p.waves {
		stderr("  %d:", i+1)
		for _, dir := range wave {
			stderr("  %s [%s]", cfg.modLabel(dir), p.states[dir].entry)
		}
		stderr("\n")
	}
	if len(p.rootDirt) > 0 {
		stderr("commit #0 (non-module dirt):\n")
		for _, f := range p.rootDirt {
			stderr("  %s\n", f)
		}
	} else {
		stderr("commit #0: nothing (no non-module dirt)\n")
	}
	if len(p.outsideDirt) > 0 {
		stderr("NOTE: %d dirty path(s) outside familyroot left untouched:\n", len(p.outsideDirt))
		for _, f := range p.outsideDirt {
			stderr("  %s\n", f)
		}
	}
}
