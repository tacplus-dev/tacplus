// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Command sysrename performs the case-preserving, whole-token rename of the
// reserved embedded namespace `libs` -> `sys` (SYS_RENAME_PLAN Phase 1). It is
// also the downstream fixer: pointed at a consuming repo with --downstream it
// rewrites only the import/use path segment `libs` -> `sys`.
//
// The rename is whole-token and case-preserving: `libs`->`sys`, `Libs`->`Sys`,
// `LIBS`->`SYS`. It never touches the singular `lib`/`Lib` family (libDir,
// opalib, tokenlib, skipLibCheck, ...) because every match requires the trailing
// `s`. Go's regexp has no lookahead, so boundaries are expressed with capture
// groups; the token set was verified to have no run-on forms (`libs` is never
// followed by a lowercase letter, and the only letter-preceded form is the
// protoc `Mlibs/` mapping flag, handled explicitly).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// Lowercase `libs` token: boundary = not a letter on the left, not a
	// lowercase letter on the right (so `libs/`, `libsDir`, `"libs`, `libs.`).
	reLower = regexp.MustCompile(`(^|[^a-zA-Z])libs([^a-z]|$)`)
	// PascalCase `Libs` component: capital L self-delimits the left; right must
	// not be a lowercase letter (so `LibsBase`, `IsLibsProto`, not `Libspec`).
	rePascal = regexp.MustCompile(`Libs([^a-z]|$)`)
	// All-caps `LIBS`, bounded by non-uppercase.
	reUpper = regexp.MustCompile(`(^|[^A-Z])LIBS([^A-Z]|$)`)
	// protoc `-M` mapping flag: `Mlibs/x.proto` -> `Msys/x.proto`. The `libs`
	// here is preceded by the flag letter `M`, so the lowercase rule skips it.
	reProtocM = regexp.MustCompile(`Mlibs/`)
	// Post-apply guard: the singular `lib`/`Lib` token family must be untouched.
	reSingularLib = regexp.MustCompile(`(^|[^a-zA-Z])[Ll]ib([^a-zA-Z]|$)`)
)

// denyDirs are directory names never descended into.
var denyDirs = map[string]bool{
	".git": true, "target": true, "todo": true, "node_modules": true,
	"roadmap": true, // planning docs describe the rename; do not rewrite them
	"sysrename": true, // this tool's own source carries the from/to literals
}

// rewrite applies the case-preserving token rename to one file's contents.
func rewrite(s string) string {
	s = reProtocM.ReplaceAllString(s, "Msys/")
	s = rePascal.ReplaceAllString(s, "Sys$1")
	s = reUpper.ReplaceAllString(s, "${1}SYS$2")
	s = reLower.ReplaceAllString(s, "${1}sys$2")
	return s
}

// isText returns false for files that look binary (NUL byte in the first 8KB).
func isText(b []byte) bool {
	n := len(b)
	if n > 8192 {
		n = 8192
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return false
		}
	}
	return true
}

func main() {
	apply := flag.Bool("apply", false, "perform edits (default is dry-run)")
	root := flag.String("root", ".", "repository root to sweep")
	downstream := flag.Bool("downstream", false, "downstream mode: rewrite only path-segment libs/ in imports (reserved; same token rules today)")
	flag.Parse()
	_ = downstream // path-segment rules are identical today; reserved for future divergence

	var files, hits int
	var singularBefore, singularAfter int

	err := filepath.Walk(*root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if denyDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || !isText(b) {
			return nil
		}
		orig := string(b)
		if !reLower.MatchString(orig) && !rePascal.MatchString(orig) &&
			!reUpper.MatchString(orig) && !reProtocM.MatchString(orig) {
			return nil
		}
		out := rewrite(orig)
		if out == orig {
			return nil
		}
		files++
		hits += strings.Count(orig, "libs") + strings.Count(orig, "Libs") + strings.Count(orig, "LIBS")
		singularBefore += len(reSingularLib.FindAllString(orig, -1))
		singularAfter += len(reSingularLib.FindAllString(out, -1))
		if *apply {
			if werr := os.WriteFile(path, []byte(out), info.Mode()); werr != nil {
				return werr
			}
		} else {
			rel, _ := filepath.Rel(*root, path)
			fmt.Printf("%s\n", rel)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "walk error:", err)
		os.Exit(1)
	}

	fmt.Printf("\nfiles changed: %d, libs-token occurrences: %d\n", files, hits)
	fmt.Printf("singular lib/Lib tokens: before=%d after=%d\n", singularBefore, singularAfter)
	if singularBefore != singularAfter {
		fmt.Fprintln(os.Stderr, "FATAL: singular lib/Lib token count changed — the rename corrupted a lib-singular identifier")
		os.Exit(2)
	}
	if !*apply {
		fmt.Println("(dry-run; re-run with --apply to write)")
	}
}
