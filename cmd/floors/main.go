// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Command floors checks every version floor tacplus hands its consumers
// against OSV (tacplus#949): each floor a dependencies.yaml inventory under
// -root declares (Go, Java, Rust, TypeScript, Python), and each floor a
// generated manifest under -generated declares (Cargo.toml, package.json,
// pyproject.toml, a C# .csproj, a Swift Package.swift). A lockfile resolves
// the newest compatible release, so a scanner of a lock passes while a
// consumer who holds a package at the floor gets the vulnerable version; this
// asks OSV about the floor itself and fails on an advisory it admits.
//
// OSV is queried at run time (api.osv.dev's batch API), and the output's
// "Checked against" says when. An advisory id (or alias) that
// security-exceptions.yaml accepts, unexpired, is reported and does not fail.
//
// Run by scripts/floors.sh (`just floors`), which generates the models first.
//
//	go run ./cmd/floors -root . -generated DIR
package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

const defaultOSV = "https://api.osv.dev"

func main() {
	root := flag.String("root", ".", "the checkout: its dependencies.yaml inventories and security-exceptions.yaml")
	generated := flag.String("generated", "", "the generated models whose manifests' floors are checked")
	osv := flag.String("osv", defaultOSV, "the OSV API")
	flag.Parse()

	floors, err := collect(*root, *generated)
	if err != nil {
		fmt.Fprintf(os.Stderr, "floors: %v\n", err)
		os.Exit(2)
	}
	accepted, err := readExceptions(filepath.Join(*root, "security-exceptions.yaml"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "floors: %v\n", err)
		os.Exit(2)
	}
	failing, err := check(os.Stdout, *osv, floors, accepted, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "floors: %v\n", err)
		os.Exit(2)
	}
	if failing > 0 {
		os.Exit(1)
	}
}

// floor is one version a consumer may hold a package at, and where it is
// declared.
type floor struct {
	Ecosystem, Name, Version string
	Sources                  []string
}

type floorSet map[string]*floor

func (s floorSet) add(ecosystem, name, spec, source string) {
	for _, v := range floorOf(ecosystem, spec) {
		key := ecosystem + "\x00" + name + "\x00" + v
		f, ok := s[key]
		if !ok {
			f = &floor{Ecosystem: ecosystem, Name: name, Version: v}
			s[key] = f
		}
		if len(f.Sources) == 0 || f.Sources[len(f.Sources)-1] != source {
			f.Sources = append(f.Sources, source)
		}
	}
}

var (
	versionNumber = regexp.MustCompile(`^v?(\d+(?:\.\d+)*)(.*)$`)
	// A lower bound in a requirement: >=1.2, ^1.2, ~1.2, ~=1.2, ==1.2, =1.2 or 1.2.
	lowerBound = regexp.MustCompile(`^(?:>=|\^|~=|~|==|=)?\s*v?(\d[0-9A-Za-z.+-]*)$`)
)

// floorOf is the lowest version a requirement admits, per alternative ("a ||
// b" admits both floors); none for a requirement with no lower bound (a path,
// "*", "<2").
func floorOf(ecosystem, spec string) []string {
	spec = strings.TrimSpace(spec)
	if ecosystem == "NuGet" && (strings.HasPrefix(spec, "[") || strings.HasPrefix(spec, "(")) {
		// A NuGet range: [lower, upper).
		spec = strings.TrimLeft(strings.SplitN(spec, ",", 2)[0], "[(")
	}
	var floors []string
	for _, alt := range strings.Split(spec, "||") {
		// The first comparator of the alternative that bounds it below.
		for _, part := range strings.Split(alt, ",") {
			part = strings.Join(strings.Fields(part), "")
			m := lowerBound.FindStringSubmatch(part)
			if m == nil {
				continue
			}
			v := strings.TrimSuffix(m[1], "+incompatible")
			if ecosystem == "crates.io" {
				v = padSemver(v)
			}
			if ecosystem == "npm" {
				v = padSemver(strings.TrimSuffix(strings.TrimSuffix(v, ".x"), ".x"))
			}
			floors = append(floors, v)
			break
		}
	}
	return floors
}

// padSemver completes a Cargo or npm partial version: "0.14" is 0.14.0.
func padSemver(v string) string {
	m := versionNumber.FindStringSubmatch(v)
	if m == nil {
		return v
	}
	for strings.Count(m[1], ".") < 2 {
		m[1] += ".0"
	}
	return m[1] + m[2]
}

var pythonName = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[[^\]]*\])?\s*(.*)$`)

// pythonRequirement splits a PEP 508 requirement into its name and version
// specifier, without extras or markers.
func pythonRequirement(req string) (name, spec string) {
	req, _, _ = strings.Cut(req, ";")
	m := pythonName.FindStringSubmatch(req)
	if m == nil {
		return "", ""
	}
	return m[1], strings.TrimSpace(m[2])
}

// inventoryEcosystems maps a dependencies.yaml language to its OSV
// ecosystem. Swift and C# pins are checked where the generated manifest
// writes them.
var inventoryEcosystems = map[string]string{
	"go":         "Go",
	"java":       "Maven",
	"rust":       "crates.io",
	"typescript": "npm",
	"python":     "PyPI",
}

// collect reads every inventory under root (but target/, .git and
// node_modules) and every manifest under generated.
func collect(root, generated string) ([]floor, error) {
	set := floorSet{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "target", ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "dependencies.yaml" {
			return nil
		}
		return addInventory(set, path, relTo(root, path))
	})
	if err != nil {
		return nil, err
	}
	if generated != "" {
		err := filepath.WalkDir(generated, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", ".build", "obj", "target":
					return filepath.SkipDir
				}
				return nil
			}
			source := relTo(generated, path)
			switch {
			case d.Name() == "Cargo.toml":
				return addCargo(set, path, source)
			case d.Name() == "package.json":
				return addPackageJSON(set, path, source)
			case d.Name() == "pyproject.toml":
				return addPyproject(set, path, source)
			case strings.HasSuffix(d.Name(), ".csproj"):
				return addCsproj(set, path, source)
			case d.Name() == "Package.swift":
				return addPackageSwift(set, path, source)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	floors := make([]floor, 0, len(set))
	for _, f := range set {
		floors = append(floors, *f)
	}
	sort.Slice(floors, func(i, j int) bool {
		a, b := floors[i], floors[j]
		if a.Ecosystem != b.Ecosystem {
			return a.Ecosystem < b.Ecosystem
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Version < b.Version
	})
	return floors, nil
}

func relTo(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}

func addInventory(set floorSet, path, source string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var inv struct {
		Lang map[string]struct {
			Dependencies map[string]string `yaml:"dependencies"`
		} `yaml:"lang"`
	}
	if err := yaml.Unmarshal(data, &inv); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	for lang, deps := range inv.Lang {
		ecosystem, ok := inventoryEcosystems[lang]
		if !ok {
			continue
		}
		for name, spec := range deps.Dependencies {
			set.add(ecosystem, name, spec, source)
		}
	}
	return nil
}

// addCargo reads a Cargo.toml's dependency tables, the workspace's and each
// target's too; a path, git or { workspace = true } dependency declares no
// floor of its own.
func addCargo(set floorSet, path, source string) error {
	var manifest map[string]any
	if _, err := toml.DecodeFile(path, &manifest); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	addTables := func(tables map[string]any) {
		for _, key := range []string{"dependencies", "dev-dependencies", "build-dependencies"} {
			deps, _ := tables[key].(map[string]any)
			for name, dep := range deps {
				switch d := dep.(type) {
				case string:
					set.add("crates.io", name, d, source)
				case map[string]any:
					if v, ok := d["version"].(string); ok {
						if pkg, ok := d["package"].(string); ok {
							name = pkg
						}
						set.add("crates.io", name, v, source)
					}
				}
			}
		}
	}
	addTables(manifest)
	if ws, ok := manifest["workspace"].(map[string]any); ok {
		addTables(ws)
	}
	if targets, ok := manifest["target"].(map[string]any); ok {
		for _, t := range targets {
			if tables, ok := t.(map[string]any); ok {
				addTables(tables)
			}
		}
	}
	return nil
}

func addPackageJSON(set floorSet, path, source string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	for _, key := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		raw, ok := manifest[key]
		if !ok {
			continue
		}
		var deps map[string]string
		if err := json.Unmarshal(raw, &deps); err != nil {
			return fmt.Errorf("%s: %s: %w", source, key, err)
		}
		for name, spec := range deps {
			set.add("npm", name, spec, source)
		}
	}
	return nil
}

func addPyproject(set floorSet, path, source string) error {
	var manifest struct {
		BuildSystem struct {
			Requires []string `toml:"requires"`
		} `toml:"build-system"`
		Project struct {
			Dependencies         []string            `toml:"dependencies"`
			OptionalDependencies map[string][]string `toml:"optional-dependencies"`
		} `toml:"project"`
	}
	if _, err := toml.DecodeFile(path, &manifest); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	reqs := append(append([]string{}, manifest.BuildSystem.Requires...), manifest.Project.Dependencies...)
	for _, extra := range manifest.Project.OptionalDependencies {
		reqs = append(reqs, extra...)
	}
	for _, req := range reqs {
		if name, spec := pythonRequirement(req); name != "" {
			set.add("PyPI", name, spec, source)
		}
	}
	return nil
}

func addCsproj(set floorSet, path, source string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var project struct {
		ItemGroups []struct {
			References []struct {
				Include string `xml:"Include,attr"`
				Version string `xml:"Version,attr"`
			} `xml:"PackageReference"`
		} `xml:"ItemGroup"`
	}
	if err := xml.Unmarshal(data, &project); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	for _, group := range project.ItemGroups {
		for _, ref := range group.References {
			set.add("NuGet", ref.Include, ref.Version, source)
		}
	}
	return nil
}

// .package(url: "https://github.com/apple/swift-protobuf.git", from: "1.38.0"),
// and exact:, .upToNextMajor(from:) and .upToNextMinor(from:).
var swiftPackage = regexp.MustCompile(`\.package\(\s*url:\s*"([^"]+)"\s*,\s*(?:from:|exact:|\.upToNext(?:Major|Minor)\(\s*from:)\s*"([^"]+)"`)

// addPackageSwift reads a Package.swift's dependencies: OSV's SwiftURL
// ecosystem names a package by its URL without the scheme and ".git".
func addPackageSwift(set floorSet, path, source string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, m := range swiftPackage.FindAllStringSubmatch(string(data), -1) {
		name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(m[1], "https://"), "http://"), ".git")
		set.add("SwiftURL", name, m[2], source)
	}
	return nil
}

// exception is an entry of security-exceptions.yaml.
type exception struct {
	ID      string
	Reason  string
	Expires string
}

// readExceptions reads security-exceptions.yaml as the build's scanner does
// (looper-scan read_exceptions): line by line, not as YAML, since a value
// runs to the end of its line, colons and all. An entry starts at "  - key:
// value", and "    key: value" lines continue it.
func readExceptions(path string) ([]exception, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []exception
	set := func(e *exception, kv string) {
		key, value, _ := strings.Cut(kv, ":")
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		switch key {
		case "id":
			e.ID = value
		case "reason":
			e.Reason = value
		case "expires":
			e.Expires = value
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "  - "):
			list = append(list, exception{})
			set(&list[len(list)-1], strings.TrimPrefix(line, "  - "))
		case strings.HasPrefix(line, "    ") && len(list) > 0 && !strings.HasPrefix(strings.TrimSpace(line), "#"):
			set(&list[len(list)-1], strings.TrimSpace(line))
		}
	}
	return list, nil
}

type osvPackage struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

type osvQuery struct {
	Package osvPackage `json:"package"`
	Version string     `json:"version"`
}

type osvBatchRequest struct {
	Queries []osvQuery `json:"queries"`
}

type osvVulnRef struct {
	ID string `json:"id"`
}

type osvBatchResult struct {
	Vulns []osvVulnRef `json:"vulns,omitempty"`
}

type osvBatchResponse struct {
	Results []osvBatchResult `json:"results"`
}

type osvVuln struct {
	ID        string        `json:"id"`
	Summary   string        `json:"summary,omitempty"`
	Aliases   []string      `json:"aliases,omitempty"`
	Withdrawn string        `json:"withdrawn,omitempty"`
	Affected  []osvAffected `json:"affected,omitempty"`
}

type osvAffected struct {
	DatabaseSpecific struct {
		// RustSec's informational advisories: "unmaintained", "unsound",
		// "notice".
		Informational string `json:"informational,omitempty"`
	} `json:"database_specific"`
}

// informational is the kind of a RustSec informational advisory, or "": a
// crate's maintenance, not a flaw a release fixes, which no floor moves past.
// It warns, as cargo audit's does.
func (v osvVuln) informational() string {
	for _, a := range v.Affected {
		if a.DatabaseSpecific.Informational != "" {
			return a.DatabaseSpecific.Informational
		}
	}
	return ""
}

// osvBatch is the most queries OSV's querybatch takes at once.
const osvBatch = 1000

var (
	client = &http.Client{Timeout: 2 * time.Minute}
	// retryDelay is the first wait before asking OSV again; each retry waits
	// longer.
	retryDelay = time.Second
)

func osvDo(req *http.Request, into any) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * retryDelay)
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return err
				}
				req.Body = body
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("%s %s: %s: %s", req.Method, req.URL, resp.Status, bytes.TrimSpace(body))
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return lastErr
			}
			continue
		}
		return json.Unmarshal(body, into)
	}
	return lastErr
}

// check asks OSV about every floor, prints what it admits, and returns how
// many floors admit an advisory no exception accepts. An unanswered query is
// an error: a floor nobody checked is no pass.
func check(w io.Writer, osv string, floors []floor, accepted []exception, now time.Time) (int, error) {
	queriedAt := now.UTC().Format(time.RFC3339)
	ids := make([][]string, len(floors))
	for start := 0; start < len(floors); start += osvBatch {
		end := min(start+osvBatch, len(floors))
		var batch osvBatchRequest
		for _, f := range floors[start:end] {
			batch.Queries = append(batch.Queries, osvQuery{Package: osvPackage{Name: f.Name, Ecosystem: f.Ecosystem}, Version: f.Version})
		}
		body, err := json.Marshal(batch)
		if err != nil {
			return 0, err
		}
		req, err := http.NewRequest(http.MethodPost, osv+"/v1/querybatch", bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		var resp osvBatchResponse
		if err := osvDo(req, &resp); err != nil {
			return 0, err
		}
		if len(resp.Results) != end-start {
			return 0, fmt.Errorf("OSV answered %d of %d queries", len(resp.Results), end-start)
		}
		for i, res := range resp.Results {
			for _, v := range res.Vulns {
				ids[start+i] = append(ids[start+i], v.ID)
			}
		}
	}

	records := map[string]osvVuln{}
	record := func(id string) (osvVuln, error) {
		if r, ok := records[id]; ok {
			return r, nil
		}
		req, err := http.NewRequest(http.MethodGet, osv+"/v1/vulns/"+id, nil)
		if err != nil {
			return osvVuln{}, err
		}
		var r osvVuln
		if err := osvDo(req, &r); err != nil {
			return osvVuln{}, err
		}
		records[id] = r
		return r, nil
	}
	acceptedBy := func(v osvVuln) *exception {
		for i, e := range accepted {
			expires, err := time.Parse("2006-01-02", e.Expires)
			if err != nil || now.After(expires.Add(24*time.Hour)) {
				continue
			}
			if e.ID == v.ID {
				return &accepted[i]
			}
			for _, alias := range v.Aliases {
				if e.ID == alias {
					return &accepted[i]
				}
			}
		}
		return nil
	}

	failing := 0
	for i, f := range floors {
		var fails, oks, warns []string
		for _, id := range ids[i] {
			v, err := record(id)
			if err != nil {
				return 0, err
			}
			if v.Withdrawn != "" {
				continue
			}
			line := v.ID
			if len(v.Aliases) > 0 {
				line += " (" + strings.Join(v.Aliases, ", ") + ")"
			}
			if v.Summary != "" {
				line += " " + v.Summary
			}
			if e := acceptedBy(v); e != nil {
				oks = append(oks, line+" [accepted until "+e.Expires+"]")
				continue
			}
			if kind := v.informational(); kind != "" {
				warns = append(warns, line+" ["+kind+"]")
				continue
			}
			fails = append(fails, line)
		}
		where := strings.Join(f.Sources, ", ")
		if len(f.Sources) > 3 {
			where = fmt.Sprintf("%s and %d more", strings.Join(f.Sources[:3], ", "), len(f.Sources)-3)
		}
		for _, line := range oks {
			fmt.Fprintf(w, "accepted %s %s@%s: %s\n    declared in %s\n", f.Ecosystem, f.Name, f.Version, line, where)
		}
		for _, line := range warns {
			fmt.Fprintf(w, "warning %s %s@%s: %s\n    declared in %s\n", f.Ecosystem, f.Name, f.Version, line, where)
		}
		if len(fails) > 0 {
			failing++
			fmt.Fprintf(w, "FAIL %s %s@%s: %s\n    declared in %s\n", f.Ecosystem, f.Name, f.Version, strings.Join(fails, "; "), where)
		}
	}

	byEcosystem := map[string]int{}
	for _, f := range floors {
		byEcosystem[f.Ecosystem]++
	}
	var counts []string
	for e, n := range byEcosystem {
		counts = append(counts, fmt.Sprintf("%s %d", e, n))
	}
	sort.Strings(counts)
	fmt.Fprintf(w, "\n%d floors checked (%s): %d admit an advisory no exception accepts\n", len(floors), strings.Join(counts, ", "), failing)
	fmt.Fprintf(w, "\n### Checked against\n- osv: db %s, queried_at %s\n", osv, queriedAt)
	return failing, nil
}
