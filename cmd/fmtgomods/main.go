package main

import (
	"fmt"
	"os"

	"github.com/x64c/go-srcutils/fmtgomods"
	"github.com/x64c/go-srcutils/toolver"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-v" {
		toolver.Print()
		return
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	target := os.Args[1]
	info, err := os.Stat(target)
	if err != nil {
		fail("%v", err)
	}
	if info.IsDir() {
		changed, err := fmtgomods.Tree(target)
		for _, p := range changed { // report what was done even if the walk then failed
			fmt.Println(p)
		}
		if err != nil {
			fail("%v", err)
		}
		return
	}
	changed, err := fmtgomods.File(target)
	if err != nil {
		fail("%v", err)
	}
	if changed {
		fmt.Println(target)
	}
}

func usage() {
	stderr("usage: %s <go.mod|directory>   (-v: version)\n\n", os.Args[0])
	stderr("Formats go.mod files into one layout: a direct require group, an indirect\n")
	stderr("require group, then replace. Each group is sorted, and takes parentheses\n")
	stderr("only when it holds more than one item.\n\n")
	stderr("Requires follow the toolchain's own tidy layout, so `go mod tidy` leaves the\n")
	stderr("result alone. Replace is the group no generator maintains — a published\n")
	stderr("go.mod carries none — so only a source tree has that layout to get wrong.\n\n")
	stderr("Prints each file it changes; silence means every file was already correct.\n")
}
