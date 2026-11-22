# sort-imports

Merge all Go import declarations into a single block with two groups (stdlib, others) separated by a blank line, each group alphabetically sorted. Single import uses no parentheses.

## Install

```bash
go install github.com/x64c/go-srcutils/cmd/sort-imports@latest
```

## Usage

```bash
# Single file
sort-imports path/to/file.go

# Entire source tree (concurrent)
sort-imports path/to/src/
```

Prints each modified file path to stdout. Files already sorted are skipped.

## Stdlib detection

Uses `go list std` at runtime, inheriting your environment. For experimental packages (e.g. `simd`), set `GOEXPERIMENT`:

```bash
GOEXPERIMENT=simd sort-imports ./src
```

## Precautions

- A file that imports `"C"` (cgo) is left exactly as it is. There, the comment above
  `import "C"` is C code, and it only counts while that import stands on its own, so
  merging the imports into one block would break the file.
- Standalone comment lines between import declarations are removed.
- Inline comments (e.g. `"foo" // bar`) are preserved, and so is a comment after the `(` of
  the first import block and after the `)` of the last.
- Imports are found with Go's own parser and moved as whole lines, so the import section
  must be laid out the way `gofmt` leaves it: `import`, `(`, `)` and every import on a line
  of their own. A file laid out otherwise (two of them sharing a line, code on an import's
  line, a comment after an import that runs on to the next line), or one whose package
  clause or imports do not parse, is reported and left untouched (exit status 1). It is
  never rewritten with something dropped.
