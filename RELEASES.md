# Release notes

## v0.8.0 (2026-11-22)

Two bug fixes, in `clone-srctree` and `sort-imports`. Both bugs were also in v0.6.0 and v0.7.0.

Both tools used to treat Go source as plain text. They now read import declarations with Go's own parser (`go/parser`). As a consequence, some inputs that used to be mishandled without a word now stop the tool with an error; see "Behavior changes" under each fix.

### clone-srctree: the import rewrite changed text that was not an import

**The bug.** For every `importmap` key `K`, the rewrite replaced the bytes `"K"` and `"K/` everywhere in each target `.go` file. Any string literal, struct tag or comment that read like a module path was rewritten along with the imports. Nothing reported it: the exit status was 0, the residue check passed (nothing of the old text was left), and the build test passed whenever the changed strings still compiled.

Example, with the key `base` mapped to `example.com/p/base`:

```go
import "base/b"            // rewritten, as intended
const Name = "base"        // also rewritten: "example.com/p/base"
F string `json:"base"`     // also rewritten: `json:"example.com/p/base"`
```

**The fix.** The rewrite, the dry-run plan (`-dry`) and the residue check now look at import declarations only. Every other byte of a file is left as it is.

**Behavior changes**

- **Only imports are rewritten.** A module path mentioned in a comment or a string is no longer touched. If you relied on that side effect (for example, an import path shown in a doc comment), it now stays as written in the source.
- **A `.go` file must parse up to the end of its imports.** One that doesn't is an error. A real run checks every source file before it deletes or copies anything, and stops with `... (nothing was changed)`. A dry run reports the same error. Syntax errors after the imports are not seen by this check.
- **An import path written as a raw string** (`` `K/pkg` ``) is now rewritten too. It is written back in double quotes, the form `gofmt` uses.
- **`-dry` counts differently.** `REWRITES: N pair(s)` counts one per `importmap` key (it used to count two), and a file is listed only if one of its imports matches.

### sort-imports: imports it could not read were deleted, and some valid files crashed it

**The bug.** `sort-imports` scanned lines. It recognized an import only by a double-quoted string on the line, a block only by a line that is exactly `import (`, and a block's end only by a line that is exactly `)`. It then replaced the whole import section with what it had recognized.

| Input | v0.7.0 |
|---|---|
| an import path in backquotes (`` y `strings` ``) | the import line was **deleted**, exit status 0 |
| `) // comment` closing a block | **panic** |
| `import ( // comment` opening a block | the block was left unsorted, without a message |
| code sharing a line with an import (`import "fmt"; var X = 1`) | the file was **corrupted** |
| a comment after an import that runs on to the next line | the file was **corrupted** |
| an unterminated `import (` | **panic** |

`clone-srctree` runs the same code as its step 6, so a clone could lose an import or crash partway through.

**The fix.** Imports are found with Go's parser. Before anything is written, the tool checks every line it is about to replace, then re-parses its own result and requires the same package name and exactly the same set of imports as before. A file it cannot sort safely is reported and left untouched.

**Behavior changes**

- **The import section must be laid out the way `gofmt` leaves it:** `import`, `(`, `)` and each import on a line of their own. A comment may follow them on the same line if it ends there. A file laid out otherwise is an error (exit status 1) that names the line, and the file is not changed. This includes valid Go that v0.7.0 silently left alone, such as `import ("os"; "fmt")`. Run `gofmt` on the file first.
- **A file whose package clause or imports do not parse** is an error. v0.7.0 skipped it, rewrote it or crashed, depending on the file.
- **A comment after the `(` of the first block and after the `)` of the last is kept.** Blocks with such comments are now sorted.
- **Imports in backquotes are kept** as written, and sorted by their path.
- **Files with CRLF line endings are now sorted**, and stay CRLF. v0.7.0 never recognized their import blocks.
- **The same path imported under several names** (`"fmt"`, `_ "fmt"`, `z "fmt"`) comes out in a fixed order.
- Through `clone-srctree`, any of the errors above stops the run at step 6 with the file's name.

Unchanged: comment-only lines between import declarations, and inside an import block, are still removed.

### Known issue: sort-imports and cgo

`sort-imports` must not be run on files that import `"C"`. It merges `import "C"` into the import block, which detaches the C code in the comment above it, and the file no longer builds. If that comment sits between two imports, it is removed from the file. This is not new in v0.8.0 (v0.6.0 and v0.7.0 do the same), and it is not fixed in this release.

### Install

```bash
go install github.com/x64c/go-srcutils/cmd/clone-srctree@v0.8.0
go install github.com/x64c/go-srcutils/cmd/sort-imports@v0.8.0
go install github.com/x64c/go-srcutils/cmd/publish-family@v0.8.0
go install github.com/x64c/go-srcutils/cmd/fmtgomods@v0.8.0
```

`publish-family` and `fmtgomods` have no changes in this release.
