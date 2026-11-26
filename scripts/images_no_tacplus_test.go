// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type imagesWorkflow struct {
	Jobs map[string]struct {
		If    string `yaml:"if"`
		Steps []struct {
			Run  string            `yaml:"run"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// ghcr.io/tacplus-dev/tacplus/tacplus is deprecated (#193, docs/VERSIONING.md):
// its tags stay as they are and it is never pushed again, so a patch on this
// line pushes base and build only (#932). A release still gives its build
// image the version policy's tags.
func TestImagesPushNoTacplusImage(t *testing.T) {
	for _, f := range []string{"../images/tacplus.Dockerfile", "../Dockerfile"} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s exists: it defines the deprecated tacplus image", f)
		}
	}
	data, err := os.ReadFile("../.github/workflows/images.yml")
	if err != nil {
		t.Fatal(err)
	}
	var wf imagesWorkflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatal(err)
	}
	var releaseTags bool
	for name, job := range wf.Jobs {
		for _, s := range job.Steps {
			for k, v := range s.With {
				if strings.Contains(v, "tacplus.Dockerfile") || strings.Contains(v, "}}/tacplus,") || strings.Contains(v, "}}/tacplus:") {
					t.Errorf("job %s: %s: %q builds or pushes the tacplus image", name, k, v)
				}
			}
			if strings.Contains(s.Run, "$NS/tacplus") {
				t.Errorf("job %s: a step tags or runs the tacplus image:\n%s", name, s.Run)
			}
			if strings.Contains(job.If, "github.ref_type == 'tag'") && strings.Contains(s.Run, "scripts/images-tags.sh") && strings.Contains(s.Run, `"$NS/build:$t"`) {
				releaseTags = true
			}
		}
	}
	if !releaseTags {
		t.Error("no job gives a release's build image the tags of scripts/images-tags.sh")
	}
}
