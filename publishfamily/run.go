package publishfamily

import (
	"path/filepath"
	"strings"
)

// gitRun runs a git command whose output is not needed.
func gitRun(repo string, args ...string) error {
	_, err := git(repo, args...)
	return err
}

// Run executes the release: commit #0 (pre-existing non-module dirt), then the
// waves, then the closing sweep. Within a wave each module runs the step
// sequence tidy -> commit -> tag -> push -> nudge -> wait FORWARD from its
// prechecked entry point; commits happen only here, and each module's files
// are committed exactly once, by its own wave, post-tidy, in final form.
//
// A returned error stops the run where it stood. That is safe by construction:
// the precheck ladder re-enters every module at its first undone step, so a
// re-run continues rather than repeats.
func Run(cfg Config, p Plan) error {
	pending := false
	for _, st := range p.states {
		if st.entry != entryDone {
			pending = true
		}
	}
	dirty, err := gitDirty(cfg.Repo)
	if err != nil {
		return err
	}
	fencedDirty := 0
	for _, f := range dirty {
		if cfg.underFamilyRoot(f) {
			fencedDirty++
		}
	}
	if !pending && len(p.rootDirt) == 0 && fencedDirty == 0 {
		stderr("family %s fully published and family root clean — nothing to do\n", p.version)
		return nil
	}

	// commit #0: non-module dirt (final at sync time; no wave owns it)
	if len(p.rootDirt) > 0 {
		for _, f := range p.rootDirt {
			if err := gitRun(cfg.Repo, "add", "--", f); err != nil {
				return err
			}
		}
		if gitStagedAny(cfg.Repo) {
			if err := gitRun(cfg.Repo, "commit", "-m", cfg.CommitMsg); err != nil {
				return err
			}
			stderr("commit #0: %d non-module path(s)\n", len(p.rootDirt))
		}
	}

	for i, wave := range p.waves {
		var work []*modState // this wave's modules that enter anywhere before DONE
		for _, dir := range wave {
			if st := p.states[dir]; st.entry != entryDone {
				work = append(work, st)
			}
		}
		if len(work) == 0 {
			stderr("wave %d: all DONE — skip\n", i+1)
			continue
		}
		var names []string
		for _, st := range work {
			names = append(names, cfg.modLabel(st.dir)+" ["+st.entry.String()+"]")
		}
		stderr("wave %d: %s\n", i+1, strings.Join(names, ", "))

		// step: tidy + build (entryFresh only) — go.mods take final form here
		for _, st := range work {
			if st.entry < entryFresh {
				continue
			}
			abs := filepath.Join(cfg.Repo, filepath.FromSlash(st.dir))
			stderr("  tidy  %s\n", cfg.modLabel(st.dir))
			if err := goModuleAlone(abs, "mod", "tidy"); err != nil {
				return err
			}
			stderr("  build %s\n", cfg.modLabel(st.dir))
			if err := buildModuleAlone(abs); err != nil {
				return err
			}
			if err := gitRun(cfg.Repo, "add", "--", cfg.modPathspec(st.dir)); err != nil {
				return err
			}
		}
		// step: one commit for the wave's freshly-finalized modules
		if gitStagedAny(cfg.Repo) {
			if err := gitRun(cfg.Repo, "commit", "-m", cfg.CommitMsg); err != nil {
				return err
			}
			stderr("  commit\n")
		}
		// step: tag (entryTag and deeper)
		for _, st := range work {
			if st.entry >= entryTag {
				if err := gitRun(cfg.Repo, "tag", st.tag); err != nil {
					return err
				}
				stderr("  tag   %s\n", st.tag)
			}
		}
		// step: one push — the wave's commit plus every tag not yet on the remote
		pushArgs := []string{"push", "origin", "HEAD"}
		for _, st := range work {
			if st.entry >= entryPush {
				pushArgs = append(pushArgs, st.tag)
			}
		}
		if err := gitRun(cfg.Repo, pushArgs...); err != nil {
			return err
		}
		stderr("  push  (%d tag(s))\n", len(pushArgs)-3)

		// step: nudge + wait until every module of the wave is landed
		var modpaths []string
		for _, st := range work {
			modpaths = append(modpaths, cfg.modPath(st.dir))
		}
		stderr("  awaiting proxy...\n")
		if err := awaitLanded(modpaths, p.version); err != nil {
			return err
		}
		for _, st := range work {
			st.entry = entryDone
		}
	}

	// closing sweep: dirt created during the run — committed untagged so the
	// remote equals the working tree (clone-as-is), FENCED to the family root
	// (other families' dirt is reported, never staged). Loud when it touches a
	// module already tagged this run: main is running ahead of that tag.
	leftover, err := gitDirty(cfg.Repo)
	if err != nil {
		return err
	}
	if len(leftover) > 0 {
		var fenced []string
		for _, f := range leftover {
			if !cfg.underFamilyRoot(f) {
				stderr("NOTE: outside familyroot, left untouched: %s\n", f)
				continue
			}
			if owner, owned := cfg.ownedBy(f); owned {
				stderr("WARNING: %s modified after %s was tagged — committing untagged (main runs ahead of the tag)\n",
					cfg.modLabel(owner), p.states[owner].tag)
			}
			fenced = append(fenced, f)
		}
		if len(fenced) > 0 {
			if err := gitRun(cfg.Repo, "add", "-A", "--", cfg.famRootPathspec()); err != nil {
				return err
			}
			if gitStagedAny(cfg.Repo) {
				if err := gitRun(cfg.Repo, "commit", "-m", cfg.CommitMsg); err != nil {
					return err
				}
				if err := gitRun(cfg.Repo, "push", "origin", "HEAD"); err != nil {
					return err
				}
				stderr("closing sweep: %d leftover path(s) committed and pushed, no tags\n", len(fenced))
			}
		}
	}

	// "remote == tree" has TWO failure modes and the dirt sweep covers only
	// one: unpushed commits are the other. A housekeeping-only run commits #0
	// with every wave skipped (no wave push), and a crash between a wave's
	// commit and its push strands the branch — push whenever local is ahead.
	// (Tags re-derive via the precheck ladder; the branch recovers here.)
	if gitAheadOfUpstream(cfg.Repo) {
		if err := gitRun(cfg.Repo, "push", "origin", "HEAD"); err != nil {
			return err
		}
		stderr("push: local branch was ahead of origin — synced\n")
	}

	stderr("family %s published: %d wave(s) landed\n", p.version, len(p.waves))
	return nil
}
