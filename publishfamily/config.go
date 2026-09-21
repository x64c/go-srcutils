package publishfamily

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Config declares STRUCTURE only. Every per-run fact (the family version, the
// waves' readiness, resume state) is read from the world: the repo's go.mods,
// its tags, and the module proxy.
type Config struct {
	Repo       string              `json:"repo"`       // umbrella git repo root (workspace root); REQUIRED, absolute
	FamilyRoot *string             `json:"familyroot"` // REQUIRED-explicit family root path within the repo ("" = repo root); every commit the run makes is fenced to it — other families' files in the same repo are never staged
	ModPrefix  string              `json:"modprefix"`  // module path prefix, must end "/"; <ModPrefix><dir> = module path = tag prefix
	CommitMsg  string              `json:"commitmsg"`  // REQUIRED-explicit commit message for every commit the run makes
	GoEnv      map[string]string   `json:"goenv"`      // toolchain env applied to every go subprocess (e.g. GOEXPERIMENT)
	Modules    map[string][]string `json:"modules"`    // dir -> EXPLICIT family-internal deps (dirs); the instruction, cross-validated against go.mods

	famRoot string // validated *FamilyRoot
}

// LoadConfig reads and validates the conf. Every problem it finds is a
// returned error: what to do about one is the caller's decision.
func LoadConfig(confPath string) (Config, error) {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return Config{}, fmt.Errorf("read conf: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg, json.RejectUnknownMembers(true)); err != nil {
		return Config{}, fmt.Errorf("parse conf: %v", err)
	}
	if cfg.Repo == "" || !filepath.IsAbs(cfg.Repo) {
		return Config{}, fmt.Errorf("conf: repo must be an absolute path")
	}
	if fi, err := os.Stat(filepath.Join(cfg.Repo, ".git")); err != nil || !fi.IsDir() {
		return Config{}, fmt.Errorf("conf: repo %s is not a git root (no .git directory)", cfg.Repo)
	}
	if !strings.HasSuffix(cfg.ModPrefix, "/") {
		return Config{}, fmt.Errorf("conf: modprefix must end with \"/\": %q", cfg.ModPrefix)
	}
	if cfg.CommitMsg == "" {
		return Config{}, fmt.Errorf("conf: commitmsg must be set explicitly")
	}
	if len(cfg.Modules) == 0 {
		return Config{}, fmt.Errorf("conf: modules must not be empty")
	}
	if cfg.FamilyRoot == nil {
		return Config{}, fmt.Errorf("conf: familyroot must be set explicitly (\"\" = repo root)")
	}
	cfg.famRoot = *cfg.FamilyRoot
	if cfg.famRoot != "" {
		if cfg.famRoot != path.Clean(cfg.famRoot) || strings.HasPrefix(cfg.famRoot, "/") || strings.HasPrefix(cfg.famRoot, "..") {
			return Config{}, fmt.Errorf("conf: familyroot must be a clean repo-relative path: %q", cfg.famRoot)
		}
		if fi, err := os.Stat(filepath.Join(cfg.Repo, filepath.FromSlash(cfg.famRoot))); err != nil || !fi.IsDir() {
			return Config{}, fmt.Errorf("conf: familyroot %q is not a directory under repo", cfg.famRoot)
		}
	}
	for dir, deps := range cfg.Modules {
		if fi, err := os.Stat(filepath.Join(cfg.Repo, filepath.FromSlash(dir), "go.mod")); err != nil || fi.IsDir() {
			return Config{}, fmt.Errorf("conf: module %q has no go.mod under repo", dir)
		}
		if !cfg.underFamilyRoot(dir) {
			return Config{}, fmt.Errorf("conf: module %q lies outside familyroot %q", dir, cfg.famRoot)
		}
		for _, d := range deps {
			if _, ok := cfg.Modules[d]; !ok {
				return Config{}, fmt.Errorf("conf: module %q depends on %q, which is not a modules key", dir, d)
			}
		}
	}
	return cfg, nil
}

// underFamilyRoot reports whether a repo-relative slash path lies inside the
// family root — the boundary every commit this tool makes is fenced to.
func (cfg Config) underFamilyRoot(rel string) bool {
	if cfg.famRoot == "" {
		return true
	}
	return rel == cfg.famRoot || strings.HasPrefix(rel, cfg.famRoot+"/")
}

// famRootPathspec is the git pathspec covering exactly the family root.
func (cfg Config) famRootPathspec() string {
	if cfg.famRoot == "" {
		return "."
	}
	return cfg.famRoot
}

// modPathspec is the git pathspec covering exactly one module's directory. A
// module at the family root has no dir to name, and git refuses an empty
// pathspec, so it addresses the repo root instead — which is that module's
// whole tree anyway.
func (cfg Config) modPathspec(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}

// waves stages the EXPLICIT dependency graph: wave N = every module whose
// declared deps all sit in earlier waves. A leftover means a cycle — hard error.
func (cfg Config) waves() ([][]string, error) {
	placed := map[string]bool{}
	var out [][]string
	for len(placed) < len(cfg.Modules) {
		var wave []string
		for dir, deps := range cfg.Modules {
			if placed[dir] {
				continue
			}
			ready := true
			for _, d := range deps {
				if !placed[d] {
					ready = false
					break
				}
			}
			if ready {
				wave = append(wave, dir)
			}
		}
		if len(wave) == 0 {
			var stuck []string
			for dir := range cfg.Modules {
				if !placed[dir] {
					stuck = append(stuck, dir)
				}
			}
			sort.Strings(stuck)
			return nil, fmt.Errorf("conf: dependency cycle among: %s", strings.Join(stuck, ", "))
		}
		sort.Strings(wave)
		for _, dir := range wave {
			placed[dir] = true
		}
		out = append(out, wave)
	}
	return out, nil
}

// modPath returns the module path for a conf module dir. A module AT the family
// root has no dir of its own, so its path is the prefix without the separator —
// the same rule Go applies to a module at a repository's root.
func (cfg Config) modPath(dir string) string {
	if dir == "" {
		return strings.TrimSuffix(cfg.ModPrefix, "/")
	}
	return cfg.ModPrefix + dir
}

// ownedBy returns the conf module dir owning a repo-relative path, and whether
// any module owns it. Longest match wins so nested module layouts partition
// correctly; a module AT the family root has the empty dir, owns everything
// under that root, and therefore loses to any module deeper in the tree.
//
// Ownership is reported separately from the dir because the root module's dir
// is itself "" — the value that would otherwise mean "owned by nobody".
func (cfg Config) ownedBy(rel string) (string, bool) {
	best, found := "", false
	for dir := range cfg.Modules {
		match := false
		if dir == "" {
			match = cfg.underFamilyRoot(rel)
		} else {
			match = rel == dir || strings.HasPrefix(rel, dir+"/")
		}
		if match && (!found || len(dir) > len(best)) {
			best, found = dir, true
		}
	}
	return best, found
}

// modLabel names a module for display. A module at the family root has no dir
// to show, so it is named by the last element of its module path.
func (cfg Config) modLabel(dir string) string {
	if dir == "" {
		return path.Base(cfg.modPath(dir))
	}
	return dir
}

func fmtDeps(deps []string) string {
	if len(deps) == 0 {
		return "(none)"
	}
	s := append([]string(nil), deps...)
	sort.Strings(s)
	return strings.Join(s, ", ")
}
