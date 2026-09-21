package clonesrctree

import (
	"fmt"
	"os"
	"strings"
)

// Run validates cfg, applies the family's toolchain env, and executes the full
// sync pipeline. cfg is canonicalized in place (-from/-to resolve to absolute
// paths), which is why it is taken by pointer.
func Run(cfg *Config) error {
	if err := validate(cfg); err != nil {
		return err
	}
	// goenv: the cloned family's toolchain requirements, applied to this
	// process so every go subprocess (tidy, buildtest, go list) inherits them.
	// Set before initSortImports — its `go list std` must see them too.
	for k, v := range cfg.GoEnv {
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("goenv %s: %v", k, err)
		}
	}
	if err := initSortImports(); err != nil {
		return err
	}
	return sync(*cfg)
}

// sync executes the pipeline against an already-validated config.
func sync(cfg Config) error {
	keeps := patternSet{cfg.keepPatterns()}
	skips := patternSet{cfg.skipPatterns()}
	pairs := importPairs(cfg.ImportMap)
	genActive := cfg.ImportMap != nil

	// 0. geometry: discover source modules, resolve the source→target tree map
	// (identity in pair mode; importmap-value dirs in prefixed mode)
	var srcMods []string
	if genActive {
		srcMods = discoverSourceModules(cfg.From, skips)
	}
	tm, err := buildTreeMap(cfg, srcMods)
	if err != nil {
		return err
	}
	gwPlan, err := planGoWork(cfg, keeps, skips, tm)
	if err != nil {
		return err
	}

	var res result

	// 1. delete stale, then prune emptied dirs
	if err := deleteStale(cfg.From, cfg.To, keeps, tm, gwPlan, &res, cfg.Dry, cfg.Verbose); err != nil {
		return err
	}
	if err := removeEmptyDirs(cfg.From, cfg.To, keeps, tm, &res, cfg.Dry, cfg.Verbose); err != nil {
		return err
	}

	// 2. copy (raw go.mod/go.sum excluded when generation applies; ditto
	// go.work/go.work.sum when the workspace plan owns them)
	if err := copyTree(cfg.From, cfg.To, keeps, skips, genActive, tm, gwPlan, &res, cfg.Dry, cfg.Verbose); err != nil {
		return err
	}

	// 3. rewrite (dry scans source, i.e. post-copy content)
	if cfg.Dry {
		err = planRewrites(cfg.From, keeps, skips, tm, pairs, &res)
	} else {
		err = rewriteTarget(cfg.To, keeps, pairs, &res, cfg.Verbose)
	}
	if err != nil {
		return err
	}

	// 4. verify (real runs only)
	if !cfg.Dry {
		hits, verr := verifyResidue(cfg.To, pairs)
		if verr != nil {
			return verr
		}
		if len(hits) > 0 {
			stderr("VERIFY FAILED: rewrite residue in %d location(s):\n", len(hits))
			for _, h := range hits {
				stderr("  %s\n", h)
			}
			return fmt.Errorf("aborting: import-path rewrite left residue")
		}
	}

	// 5. generate go.mod(s) + resolve pins; tidy only on module-alone runs
	// (tidy needs published pins — see generateStep); plan only (dry)
	if err := generateStep(cfg, keeps, tm, srcMods, &res, cfg.Dry); err != nil {
		return err
	}

	// 5b. go.work: generated when the source is a workspace and the target's
	// isn't kept — clone content, independent of any buildtest. Bridges are
	// emitted only for CONSUMED modules, read off the pins step 5 resolved.
	consumed := map[string]bool{}
	for _, g := range res.gomods {
		for _, pin := range g.pins {
			if i := strings.IndexByte(pin, ' '); i > 0 {
				consumed[pin[:i]] = true
			}
		}
	}
	goworkStatus, err := goworkStep(cfg, tm, gwPlan, srcMods, keeps, consumed, cfg.Dry)
	if err != nil {
		return err
	}

	// 6. sort-imports, 7. build (always after the WHOLE clone)
	siStatus, err := sortStep(cfg.To, cfg.Dry)
	if err != nil {
		return err
	}
	buildStatus, err := buildTestStep(cfg, tm, srcMods, keeps, gwPlan, cfg.Dry)
	if err != nil {
		return err
	}

	if cfg.Dry {
		printDry(&res, len(pairs), genActive, cfg.Verbose, goworkStatus, siStatus, buildStatus)
	} else {
		printSummary(&res, goworkStatus, siStatus, buildStatus)
	}
	return nil
}

func printList(header string, items []string) {
	fmt.Printf("\n%s (%d):\n", header, len(items))
	for _, s := range items {
		fmt.Printf("  %s\n", s)
	}
}

func gomodLine(g gomodResult) string {
	pins := "none"
	if len(g.pins) > 0 {
		pins = strings.Join(g.pins, ", ")
	}
	return fmt.Sprintf("%s: module %s, pins: %s, replaces: +%d/-%d",
		g.dir, g.module, pins, g.addReplace, g.dropReplace)
}

func printDry(res *result, nPairs int, genActive, verbose bool, goworkStatus, siStatus, buildStatus string) {
	fmt.Println("=== DRY RUN (nothing changed) ===")

	fmt.Printf("\nDELETIONS (%d files, %d dirs):\n", len(res.deletedFiles), len(res.deletedDirs))
	for _, f := range res.deletedFiles {
		fmt.Printf("  - %s\n", f)
	}
	for _, d := range res.deletedDirs {
		fmt.Printf("  - %s/  (empty dir)\n", d)
	}

	printList("NEW FILES", res.newFiles)
	printList("OVERWRITES", res.overwritten)

	fmt.Printf("\nREWRITES: %d pair(s), %d file(s) would change:\n", nPairs, len(res.rewritten))
	for _, f := range res.rewritten {
		fmt.Printf("  %s\n", f)
	}

	if !genActive {
		fmt.Printf("\nGO.MOD: no importmap (go.mod/go.sum copied verbatim)\n")
	} else {
		fmt.Printf("\nGO.MOD GENERATION (%d module(s)):\n", len(res.gomods))
		for _, g := range res.gomods {
			fmt.Printf("  %s  [%s]\n", gomodLine(g), g.diff)
			if verbose && g.content != nil {
				fmt.Printf("  --- generated go.mod (%s) ---\n%s  --- end ---\n", g.dir, g.content)
			}
		}
	}

	fmt.Printf("\ngowork:       %s\n", goworkStatus)
	fmt.Printf("sort-imports: %s\n", siStatus)
	fmt.Printf("buildtest:    %s\n", buildStatus)
}

func printSummary(res *result, goworkStatus, siStatus, buildStatus string) {
	fmt.Println(strings.Repeat("=", 12) + " summary " + strings.Repeat("=", 12))
	fmt.Printf("deleted:      %d files, %d dirs\n", len(res.deletedFiles), len(res.deletedDirs))
	fmt.Printf("copied (new): %d\n", len(res.newFiles))
	fmt.Printf("overwritten:  %d\n", len(res.overwritten))
	fmt.Printf("rewritten:    %d\n", len(res.rewritten))
	for _, g := range res.gomods {
		fmt.Printf("gomod %s\n", gomodLine(g))
	}
	fmt.Printf("gowork:       %s\n", goworkStatus)
	fmt.Printf("sort-imports: %s\n", siStatus)
	fmt.Printf("buildtest:    %s\n", buildStatus)
}
