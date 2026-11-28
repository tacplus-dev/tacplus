// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflowStep struct {
	ID  string            `yaml:"id"`
	Run string            `yaml:"run"`
	Env map[string]string `yaml:"env"`
}

type workflow struct {
	Jobs map[string]struct {
		Steps []workflowStep `yaml:"steps"`
	} `yaml:"jobs"`
}

func readWorkflow(t *testing.T, path string) workflow {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return w
}

// An expansion of data a user controls inside a run: script is pasted into
// the script's text before bash reads it: a workflow_dispatch input of
// `"; curl … | sh; "` would run (#584). Such data goes through the step's env.
var untrustedExpansion = regexp.MustCompile(`\$\{\{[^}]*\b(github\.event\.|github\.head_ref|inputs\.)`)

func TestWorkflowsRunNoUntrustedExpansion(t *testing.T) {
	files, err := filepath.Glob("../.github/workflows/*.yml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflows: %v", err)
	}
	for _, f := range files {
		for job, j := range readWorkflow(t, f).Jobs {
			for i, s := range j.Steps {
				for _, line := range strings.Split(s.Run, "\n") {
					if untrustedExpansion.MatchString(line) {
						t.Errorf("%s: job %s step %d: untrusted expansion in run: %s", filepath.Base(f), job, i, strings.TrimSpace(line))
					}
				}
			}
		}
	}
}

// The plan step hands images-plan.sh the event, the ref type, the rebuild
// input and the ref name, unchanged and in that order, whatever the input
// holds (#584): a dry run of the step's call against a stub that echoes them.
func TestImagesPlanStepArguments(t *testing.T) {
	var step *workflowStep
	for _, s := range readWorkflow(t, "../.github/workflows/images.yml").Jobs["plan"].Steps {
		if s.ID == "plan" {
			step = &s
		}
	}
	if step == nil {
		t.Fatal("images.yml: no plan step in job plan")
	}
	var call string
	for _, line := range strings.Split(step.Run, "\n") {
		if strings.Contains(line, "scripts/images-plan.sh") {
			call = strings.TrimSpace(line)
		}
	}
	if call == "" {
		t.Fatal("images.yml: the plan step does not call scripts/images-plan.sh")
	}
	dir := t.TempDir()
	stub := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\"\n"
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "images-plan.sh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	// What Actions puts in the step's env: ${{ }} values become plain strings.
	resolve := regexp.MustCompile(`^\$\{\{\s*(.*?)\s*\}\}$`)
	for _, tc := range []struct {
		name, event, refType, rebuild, refName string
	}{
		{"push to main", "push", "branch", "", "main"},
		{"release tag", "push", "tag", "", "v2.6.2"},
		{"schedule", "schedule", "branch", "", "main"},
		{"dispatch build", "workflow_dispatch", "branch", "build", "main"},
		{"dispatch base", "workflow_dispatch", "branch", "base", "main"},
		{"dispatch on a line", "workflow_dispatch", "branch", "build", "release/2.6"},
		{"hostile input", "workflow_dispatch", "branch", `"; echo injected; "$(id)`, "main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := map[string]string{
				"github.event_name":           tc.event,
				"github.event.inputs.rebuild": tc.rebuild,
				"inputs.rebuild":              tc.rebuild,
			}
			env := append(os.Environ(), "GITHUB_REF_TYPE="+tc.refType, "GITHUB_REF_NAME="+tc.refName)
			for k, v := range step.Env {
				if m := resolve.FindStringSubmatch(v); m != nil {
					got, ok := ctx[m[1]]
					if !ok {
						continue
					}
					v = got
				}
				env = append(env, k+"="+v)
			}
			// Actions' own substitution of any ${{ }} left in the script.
			script := regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`).ReplaceAllStringFunc(call, func(e string) string {
				return ctx[resolve.FindStringSubmatch(e)[1]]
			})
			cmd := exec.Command("bash", "-c", "set -euo pipefail; "+script+"; printf '%s' \"$plan\"")
			cmd.Dir = dir
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v\n%s", script, err, out)
			}
			want := strings.Join([]string{tc.event, tc.refType, tc.rebuild, tc.refName}, "\n")
			if string(out) != want {
				t.Errorf("images-plan.sh got\n%q\nwant\n%q", out, want)
			}
		})
	}
}
