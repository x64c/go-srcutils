package main

import (
	"flag"
	"os"

	"github.com/x64c/go-srcutils/publishfamily"
	"github.com/x64c/go-srcutils/toolver"
)

func main() {
	flag.Usage = usage
	conf := flag.String("conf", "", "JSON config file (required)")
	assertVersion := flag.String("version", "", "assert the inferred family version (optional)")
	dry := flag.Bool("dry", false, "preflight + plan only; no tidy, no git writes, no pushes")
	version := flag.Bool("v", false, "print version and exit")
	flag.Parse()

	if *version {
		toolver.Print()
		return
	}
	if *conf == "" || flag.NArg() > 0 {
		usage()
		os.Exit(1)
	}

	cfg, err := publishfamily.LoadConfig(*conf)
	if err != nil {
		fail("%v", err)
	}
	for k, v := range cfg.GoEnv {
		if err := os.Setenv(k, v); err != nil {
			fail("setenv %s: %v", k, err)
		}
	}

	p, err := publishfamily.Preflight(cfg, *assertVersion)
	if err != nil {
		fail("%v", err)
	}
	p.Print(cfg)
	if *dry {
		stderr("\n=== DRY RUN (nothing changed) ===\n")
		return
	}
	if err := publishfamily.Run(cfg, p); err != nil {
		fail("%v", err)
	}
}
