// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

// repoBlobURL is where a link to a repository file that is not published on
// the site points.
const (
	repoBlobURL = "https://github.com/tacplus-dev/tacplus/blob/main/"
	repoTreeURL = "https://github.com/tacplus-dev/tacplus/tree/main/"
)

// publishedDocs is the set of repository documents that appear on the site,
// keyed by repository path, valued by page path under the docs directory.
// Internal working documents (DEVELOPMENT, KNOWN_TEST_GAPS, the analyses,
// ...) are deliberately absent; links to them from published documents go to
// GitHub. site/mkdocs.yml's nav names these page paths.
var publishedDocs = []struct{ Src, Dst string }{
	{"docs/THEORY.md", "guide/theory.md"},
	{"docs/USER.md", "guide/user-guide.md"},
	{"docs/API.md", "guide/generated-api.md"},
	{"docs/VERSIONING.md", "project/versioning.md"},
	{"CONTRIBUTING.md", "project/contributing.md"},
	{"SECURITY.md", "project/security.md"},
	{"CODE_OF_CONDUCT.md", "project/code-of-conduct.md"},
}

// generatedDirs are the docs subdirectories sitegen owns outright.
var generatedDirs = []string{"guide", "project", "reference"}

// stageDocs stages the published documents of the repository repo into the
// docs directory docs.
func stageDocs(repo, docs *os.Root) error {
	pages := map[string]string{}
	for _, d := range publishedDocs {
		pages[d.Src] = d.Dst
	}
	for _, d := range publishedDocs {
		src, err := repo.ReadFile(d.Src)
		if err != nil {
			return err
		}
		staged, err := rewriteLinks(string(src), d.Src, d.Dst, pages, existsIn(repo))
		if err != nil {
			return fmt.Errorf("%s: %w", d.Src, err)
		}
		if err := writeFile(docs, d.Dst, []byte(staged)); err != nil {
			return err
		}
	}
	return nil
}

var (
	// [text](target) and [text](target "title"); also matches images.
	inlineLink = regexp.MustCompile(`\]\(([^)\s]+)((?:\s+"[^"]*")?)\)`)
	// [label]: target
	refDef = regexp.MustCompile(`^(\s{0,3}\[[^\]]+\]:\s*)(\S+)(.*)$`)
)

// rewriteLinks rewrites every relative link in a Markdown document staged from
// repository path srcPath to page path dstPage. Links inside fenced code blocks
// and code spans are left alone. exists reports whether a repository path
// exists and whether it is a directory. The error joins every broken link.
func rewriteLinks(doc, srcPath, dstPage string, pages map[string]string, exists func(string) (bool, bool)) (string, error) {
	var errs []error
	fix := func(target string) string {
		out, err := resolveLink(target, srcPath, dstPage, pages, exists)
		if err != nil {
			errs = append(errs, err)
		}
		return out
	}
	lines := strings.Split(doc, "\n")
	fence := ""
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			continue
		}
		if m := refDef.FindStringSubmatch(line); m != nil {
			lines[i] = m[1] + fix(m[2]) + m[3]
			continue
		}
		lines[i] = mapOutsideCodeSpans(line, func(s string) string {
			return inlineLink.ReplaceAllStringFunc(s, func(link string) string {
				m := inlineLink.FindStringSubmatch(link)
				return "](" + fix(m[1]) + m[2] + ")"
			})
		})
	}
	return strings.Join(lines, "\n"), errors.Join(errs...)
}

// existsIn reports whether a repository path exists under repo and whether it
// is a directory, for rewriteLinks. A path that leaves repo, through ".." or a
// symbolic link, does not exist.
func existsIn(repo *os.Root) func(string) (bool, bool) {
	return func(p string) (bool, bool) {
		fi, err := repo.Stat(p)
		if err != nil {
			return false, false
		}
		return true, fi.IsDir()
	}
}

// checkLinks reports every relative link of the Markdown documents files
// (repository paths under repo) to a file that does not exist or that leaves
// the repository, as staging a published document does (`just docs`).
func checkLinks(repo *os.Root, files []string) error {
	exists := existsIn(repo)
	var errs []error
	for _, f := range files {
		doc, err := repo.ReadFile(f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := rewriteLinks(string(doc), f, f, nil, exists); err != nil {
			for _, e := range err.(interface{ Unwrap() []error }).Unwrap() {
				errs = append(errs, fmt.Errorf("%s: %w", f, e))
			}
		}
	}
	return errors.Join(errs...)
}

// mapOutsideCodeSpans applies f to the parts of line that are not inside
// backtick code spans. A line with an unbalanced backtick is treated as having
// no code spans.
func mapOutsideCodeSpans(line string, f func(string) string) string {
	parts := strings.Split(line, "`")
	if len(parts)%2 == 0 {
		return f(line)
	}
	for i := 0; i < len(parts); i += 2 {
		parts[i] = f(parts[i])
	}
	return strings.Join(parts, "`")
}

func resolveLink(target, srcPath, dstPage string, pages map[string]string, exists func(string) (bool, bool)) (string, error) {
	if target == "" || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "/") ||
		strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
		return target, nil
	}
	file, frag, _ := strings.Cut(target, "#")
	if frag != "" {
		frag = "#" + frag
	}
	repoPath := path.Clean(path.Join(path.Dir(srcPath), file))
	if strings.HasPrefix(repoPath, "../") {
		return target, fmt.Errorf("link %q leaves the repository", target)
	}
	if page, ok := pages[repoPath]; ok {
		return relPage(dstPage, page) + frag, nil
	}
	ok, isDir := exists(repoPath)
	switch {
	case !ok:
		return target, fmt.Errorf("link %q: %s does not exist", target, repoPath)
	case isDir:
		return repoTreeURL + repoPath + frag, nil
	default:
		return repoBlobURL + repoPath + frag, nil
	}
}

// relPage is the relative link from page from to page to, both paths under the
// docs directory.
func relPage(from, to string) string {
	fromDir := strings.Split(path.Dir(from), "/")
	if fromDir[0] == "." {
		fromDir = nil
	}
	toParts := strings.Split(to, "/")
	i := 0
	for i < len(fromDir) && i < len(toParts)-1 && fromDir[i] == toParts[i] {
		i++
	}
	rel := strings.Repeat("../", len(fromDir)-i) + strings.Join(toParts[i:], "/")
	return rel
}
