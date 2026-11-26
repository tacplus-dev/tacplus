// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The per-language gate sets (tacplus#976): gates.yaml runs a language's set,
// not the full gates, for a change under pkg/internal/<lang>/ or
// pkg/templates/<lang>/, and scripts/gate-sets.sh names each set's legs. These
// tests hold the map to what such a path can break, and fail closed.

var gateLanguages = []string{"go", "java", "rust", "typescript", "python", "csharp", "swift"}

type gateLeg struct{ gate, leg, languages string }

func gateSets(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("bash", append([]string{"gate-sets.sh"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("gate-sets.sh %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func gateTable(t *testing.T) []gateLeg {
	t.Helper()
	var rows []gateLeg
	for _, line := range strings.Split(strings.TrimSpace(gateSets(t, "table")), "\n") {
		f := strings.SplitN(line, " ", 3)
		if len(f) != 3 {
			t.Fatalf("gate-sets.sh table: a row is not gate, leg and languages: %q", line)
		}
		rows = append(rows, gateLeg{f[0], f[1], f[2]})
	}
	return rows
}

func fullGates(t *testing.T, recipes map[string]string) []string {
	t.Helper()
	m := gateList.FindStringSubmatch(recipes["gates"])
	if m == nil {
		t.Fatalf("the gates recipe does not run scripts/gates.sh with its gate list:\n%s", recipes["gates"])
	}
	return strings.Fields(m[1])
}

// Every gate of `just gates` is in the table, by the legs it runs, and
// nothing else is: a gate added to the full gates fails here until each
// language it generates is named.
func TestGateSetsTableCoversTheFullGates(t *testing.T) {
	recipes := justRecipes(t, "../justfile")
	gates := fullGates(t, recipes)
	rows := gateTable(t)
	legsOf := map[string][]string{}
	var order []string
	for _, r := range rows {
		if !slices.Contains(gates, r.gate) {
			t.Errorf("gate-sets.sh names %s (leg %s), which `just gates` does not run", r.gate, r.leg)
			continue
		}
		if _, ok := recipes[r.leg]; !ok {
			t.Errorf("gate-sets.sh names the leg %s, which is not a recipe", r.leg)
		}
		if len(order) == 0 || order[len(order)-1] != r.gate {
			order = append(order, r.gate)
		}
		legsOf[r.gate] = append(legsOf[r.gate], r.leg)
		for _, l := range strings.Fields(r.languages) {
			if l != "*" && !slices.Contains(gateLanguages, l) {
				t.Errorf("leg %s: %q is not a language (%v) or *", r.leg, l, gateLanguages)
			}
		}
		// A leg named for a language generates it.
		for _, l := range gateLanguages {
			if strings.HasSuffix(r.leg, "-"+l) && !strings.Contains(" "+r.languages+" ", " "+l+" ") {
				t.Errorf("leg %s does not name %s among its languages (%s)", r.leg, l, r.languages)
			}
		}
	}
	if !slices.Equal(order, gates) {
		t.Errorf("gate-sets.sh's gates, in order, are\n  %v\n`just gates` runs\n  %v", order, gates)
	}
	for _, g := range gates {
		header := strings.Fields(strings.SplitN(recipes[g], "\n", 2)[0])
		body := strings.TrimSpace(strings.SplitN(recipes[g], "\n", 2)[1])
		switch {
		case body == "" && len(header) > 0:
			// A gate that names its legs: scripts/gates.sh runs those.
			if !slices.Equal(legsOf[g], header) {
				t.Errorf("gate %s runs the legs %v; gate-sets.sh names %v", g, header, legsOf[g])
			}
		case slices.Equal(legsOf[g], []string{g}):
		default:
			// A gate that runs every language itself: its legs are its
			// --language recipes, every one of them.
			var want []string
			for name, b := range recipes {
				if strings.HasPrefix(name, g+"-") && strings.Contains(b, "--language "+strings.TrimPrefix(name, g+"-")) {
					want = append(want, name)
				}
			}
			got := slices.Clone(legsOf[g])
			slices.Sort(want)
			slices.Sort(got)
			if len(want) == 0 || !slices.Equal(got, want) {
				t.Errorf("gate %s: its --language recipes are %v; gate-sets.sh names %v", g, want, got)
			}
		}
	}
}

// Each language's set is the legs that generate it, with those every change
// runs, and `just gates-lang` runs them through scripts/gates.sh.
func TestGateSetsLegsOfALanguage(t *testing.T) {
	rows := gateTable(t)
	for _, l := range gateLanguages {
		var want []string
		for _, r := range rows {
			if r.languages == "*" || strings.Contains(" "+r.languages+" ", " "+l+" ") {
				want = append(want, r.leg)
			}
		}
		got := strings.Fields(gateSets(t, "legs", l))
		if !slices.Equal(got, want) {
			t.Errorf("legs %s: %v, want %v", l, got, want)
		}
		for _, must := range []string{"soundness", "test", "floors"} {
			if !slices.Contains(got, must) {
				t.Errorf("legs %s lacks %s", l, must)
			}
		}
	}
	if out, err := exec.Command("bash", "gate-sets.sh", "legs", "cobol").CombinedOutput(); err == nil {
		t.Errorf("legs of a language with no set succeeded:\n%s", out)
	}
	recipes := justRecipes(t, "../justfile")
	if !strings.Contains(recipes["gates-lang"], "scripts/gates.sh $(bash scripts/gate-sets.sh legs") {
		t.Errorf("gates-lang does not run scripts/gates.sh on gate-sets.sh's legs:\n%s", recipes["gates-lang"])
	}
}

var minutesLine = regexp.MustCompile(`(?m)^minutes: ([0-9]+) \((.*)\)$`)

func auditPaths(t *testing.T, paths ...string) (sets, why string, minutes int, sum string, out string) {
	t.Helper()
	out = gateSets(t, append([]string{"audit", "--paths"}, paths...)...)
	m := regexp.MustCompile(`(?m)^gates: ([a-z+-]+) \((.*)\)$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("audit %v printed no gates: line:\n%s", paths, out)
	}
	mm := minutesLine.FindStringSubmatch(out)
	if mm == nil {
		t.Fatalf("audit %v printed no minutes: line:\n%s", paths, out)
	}
	minutes, _ = strconv.Atoi(mm[1])
	return m[1], m[2], minutes, mm[2], out
}

// A change under a language's directory runs that language's set and not the
// others', and the audit prints the set's legs and its minutes.
func TestGateSetsAChangeToOneLanguageRunsItsSet(t *testing.T) {
	for _, l := range gateLanguages {
		for _, p := range []string{"pkg/internal/" + l + "/types/generator.go", "pkg/templates/" + l + "/chassis/x.tmpl"} {
			sets, why, minutes, sum, out := auditPaths(t, p)
			if sets != l {
				t.Errorf("%s runs %s (%s), want %s alone", p, sets, why, l)
				continue
			}
			if !strings.Contains(out, "  "+l+" runs: "+strings.Join(strings.Fields(gateSets(t, "legs", l)), " ")+"\n") {
				t.Errorf("audit of %s does not print %s's legs:\n%s", p, l, out)
			}
			if minutes <= 0 || sum != l+" "+strconv.Itoa(minutes) {
				t.Errorf("audit of %s: minutes %d (%s), want %s's own", p, minutes, sum, l)
			}
		}
	}
	sets, _, minutes, sum, _ := auditPaths(t, "pkg/internal/go/x.go", "pkg/templates/rust/y.tmpl")
	_, _, goMin, _, _ := auditPaths(t, "pkg/internal/go/x.go")
	_, _, rustMin, _, _ := auditPaths(t, "pkg/internal/rust/x.go")
	if sets != "go+rust" || minutes != goMin+rustMin || sum != "go "+strconv.Itoa(goMin)+" + rust "+strconv.Itoa(rustMin) {
		t.Errorf("a change to go and rust runs %s in %d minutes (%s), want go+rust in %d", sets, minutes, sum, goMin+rustMin)
	}
}

// Fail closed: a path no set names, the gate map and the justfile run full.
func TestGateSetsFailClosed(t *testing.T) {
	for _, c := range []struct{ paths []string }{
		{[]string{"pkg/builder/builder.go"}},
		{[]string{"pkg/internal/common/parser/parser.go"}},
		{[]string{"pkg/internal/proto/spec/spec.go"}},
		{[]string{"pkg/templates/embed.go"}},
		{[]string{"pkg/templates/go.mod"}},
		{[]string{"pkg/internal/kotlin/types/generator.go"}},
		{[]string{"scripts/gate-sets.sh"}},
		{[]string{"cross-test/rotation/run.sh"}},
		{[]string{"gates.yaml"}},
		{[]string{"justfile"}},
		{[]string{"pkg/internal/go/x.go", "pkg/builder/builder.go"}},
		{[]string{"pkg/internal/rust/x.go", "gates.yaml"}},
		{[]string{"pkg/internal/java/x.go", "justfile"}},
	} {
		sets, why, _, _, out := auditPaths(t, c.paths...)
		if sets != "full" || !strings.Contains(out, "  full runs: just gates\n") {
			t.Errorf("%v runs %s (%s), want full:\n%s", c.paths, sets, why, out)
		}
	}
	if _, why, _, _, _ := auditPaths(t, "pkg/internal/go/x.go", "gates.yaml"); why != "gates.yaml changed" {
		t.Errorf("a change to gates.yaml runs full for %q, want \"gates.yaml changed\"", why)
	}
	if _, why, _, _, _ := auditPaths(t, "scripts/new.sh"); why != "scripts/new.sh matches no gate set" {
		t.Errorf("an unmapped path runs full for %q", why)
	}
	if _, why, _, _, _ := auditPaths(t, "justfile"); why != "justfile is mapped to full (/justfile)" {
		t.Errorf("the justfile runs full for %q", why)
	}
}

// The VM spec (tacplus#968) is read only outside the repository: a change to
// it, as the recap's drift fixes, runs the docs set, not full.
func TestGateSetsLooperYamlRunsDocs(t *testing.T) {
	if sets, why, _, _, out := auditPaths(t, "looper.yaml"); sets != "docs" {
		t.Errorf("looper.yaml runs %s (%s), want docs:\n%s", sets, why, out)
	}
}

// What the sets rest on: a language's packages change only what tacplus
// generates for it. They are imported by pkg/builder alone, import no other
// language's, register nothing at init and read only their own templates; and
// every language tacplus generates has a set. A change that breaks one of
// these makes a language's path able to break another's legs: the map must be
// derived again before this passes.
func TestGateSetsLanguagesAreLeaves(t *testing.T) {
	langPkg := regexp.MustCompile(`"github\.com/tacplus-dev/tacplus/pkg/internal/(` + strings.Join(gateLanguages, "|") + `)(/[^"]*)?"`)
	tmplDir := regexp.MustCompile(`"(` + strings.Join(gateLanguages, "|") + `)/[a-z_]+[/"]`)
	langOf := func(path string) string {
		for _, l := range gateLanguages {
			if strings.HasPrefix(path, "pkg/internal/"+l+"/") {
				return l
			}
		}
		return ""
	}
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("..", path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "target" || rel == ".git" || strings.HasPrefix(rel, "pkg/templates/") || strings.HasPrefix(rel, "site/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(data)
		own := langOf(rel)
		for _, m := range langPkg.FindAllStringSubmatch(src, -1) {
			if m[1] != own && !strings.HasPrefix(rel, "pkg/builder/") {
				t.Errorf("%s imports pkg/internal/%s%s: only pkg/builder may (gates.yaml's %s set covers only %s's output)", rel, m[1], m[2], m[1], m[1])
			}
		}
		if own == "" {
			return nil
		}
		if regexp.MustCompile(`(?m)^func init\(\)`).MatchString(src) {
			t.Errorf("%s has an init(): a language's package must register nothing another language's generation reads", rel)
		}
		for _, m := range tmplDir.FindAllStringSubmatch(src, -1) {
			if m[1] != own && !(m[1] == "go" && regexp.MustCompile(`"go/(ast|build|format|parser|printer|scanner|token|types|constant|doc)"`).MatchString(m[0]+`"`)) {
				t.Errorf("%s reads %s's templates (%s)", rel, m[1], m[0])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("../pkg/templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() && !slices.Contains(gateLanguages, e.Name()) {
			t.Errorf("pkg/templates/%s is a language with no gate set: add it to gates.yaml and scripts/gate-sets.sh", e.Name())
		}
	}
	for _, l := range gateLanguages {
		if _, err := os.Stat("../pkg/internal/" + l); err != nil {
			t.Errorf("gates.yaml's %s set maps pkg/internal/%s, which is gone: %v", l, l, err)
		}
	}
}
