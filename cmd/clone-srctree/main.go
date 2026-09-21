package main

import (
	"flag"
	"os"

	"github.com/x64c/go-srcutils/clonesrctree"
	"github.com/x64c/go-srcutils/toolver"
)

func main() {
	flag.Usage = usage
	from := flag.String("from", "", "source tree")
	to := flag.String("to", "", "target tree")
	buildtest := flag.String("buildtest", "", "test-compile mode: module|workspace (absent = skip)")
	conf := flag.String("conf", "", "JSON config file")
	dry := flag.Bool("dry", false, "plan only")
	version := flag.Bool("v", false, "print version and exit")
	verbose := flag.Bool("vv", false, "verbose")
	var keep, skip stringList
	flag.Var(&keep, "keep", "target-side protection (repeatable)")
	flag.Var(&skip, "skip", "source-side exclusion (repeatable)")
	flag.Parse()

	if *version {
		toolver.Print()
		return
	}

	seen := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { seen[f.Name] = true })

	cfg := clonesrctree.Config{}
	if *conf != "" {
		if err := clonesrctree.LoadConf(*conf, &cfg); err != nil {
			fail("conf: %v", err)
		}
	}
	if seen["from"] {
		cfg.From = *from
	}
	if seen["to"] {
		cfg.To = *to
	}
	if seen["keep"] {
		cfg.Keep = keep
	}
	if seen["skip"] {
		cfg.Skip = skip
	}
	if seen["buildtest"] {
		cfg.BuildTest = *buildtest
	}
	cfg.Dry = *dry
	cfg.Verbose = *verbose

	if cfg.From == "" || cfg.To == "" {
		usage()
		os.Exit(1)
	}
	if err := clonesrctree.Run(&cfg); err != nil {
		fail("%v", err)
	}
}
