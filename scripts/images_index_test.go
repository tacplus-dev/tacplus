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
)

// checkIndex runs images-check-index.sh against a fake docker whose
// `buildx imagetools inspect <ref> --format '{{json .Image}}'` prints image.
func checkIndex(t *testing.T, image string, since string) (string, error) {
	t.Helper()
	script, err := filepath.Abs("images-check-index.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "image.json"), []byte(image), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := "#!/usr/bin/env bash\n" +
		`[[ "$*" == "buildx imagetools inspect ghcr.io/x/build:20261108-abc1234 --format {{json .Image}}" ]] || { echo "unexpected: docker $*" >&2; exit 2; }` + "\n" +
		`exec cat "` + filepath.Join(dir, "image.json") + `"` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "ghcr.io/x/build:20261108-abc1234", since)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The run started 2026-11-08T00:04:00Z (1794096240).
const runStart = "1794096240"

func TestImagesCheckIndex(t *testing.T) {
	cfg := func(created string) string { return `{"created":"` + created + `","architecture":"x"}` }
	for _, tc := range []struct {
		name, image string
		refused     string // empty: accepted
	}{
		{"both platforms built by this run",
			`{"linux/amd64":` + cfg("2026-11-08T00:15:35.123456789Z") + `,"linux/arm64":` + cfg("2026-11-08T00:43:15Z") + `}`, ""},
		// #369: v2.6.2's arm64 leg was all registry-cache hits and re-pushed
		// v2.6.1's image, its config created three days before the run.
		{"arm64 is an older run's image",
			`{"linux/amd64":` + cfg("2026-11-08T00:15:35Z") + `,"linux/arm64":` + cfg("2026-11-05T20:28:07Z") + `}`,
			"linux/arm64 was created 2026-11-05T20:28:07Z, before this run started"},
		{"arm64 missing",
			`{"linux/amd64":` + cfg("2026-11-08T00:15:35Z") + `}`, "has no linux/arm64 image"},
		{"amd64 missing",
			`{"linux/arm64":` + cfg("2026-11-08T00:15:35Z") + `}`, "has no linux/amd64 image"},
		{"a single-platform image, not an index",
			cfg("2026-11-08T00:15:35Z"), "has no linux/amd64 image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := checkIndex(t, tc.image, runStart)
			if tc.refused == "" {
				if err != nil {
					t.Fatalf("refused: %v\n%s", err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted; want refused with %q\n%s", tc.refused, out)
			}
			if !strings.Contains(out, tc.refused) {
				t.Fatalf("refused with\n%s\nwant %q", out, tc.refused)
			}
		})
	}
}

// The OS upgrade in each image is a layer the registry cache would otherwise
// hand back unchanged as long as the image it starts from has not moved (#369):
// it must depend on APT_REFRESH, and images.yml must pass a value unique to
// the run, so every run rebuilds it with the current Debian packages.
func TestImagesAptUpgradeRebuildsEachRun(t *testing.T) {
	for _, f := range []string{"../images/base.Dockerfile", "../images/build.Dockerfile"} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := strings.ReplaceAll(string(data), "\\\n", " ")
		lines := strings.Split(src, "\n")
		var stage bool // APT_REFRESH declared in the current stage
		var upgrades int
		for _, l := range lines {
			switch {
			case strings.HasPrefix(l, "FROM "):
				stage = false
			case l == "ARG APT_REFRESH" || strings.HasPrefix(l, "ARG APT_REFRESH="):
				stage = true
			case strings.HasPrefix(l, "RUN ") && strings.Contains(l, "apt-get upgrade"):
				upgrades++
				if !stage || !strings.Contains(l, "${APT_REFRESH}") {
					t.Errorf("%s: an apt-get upgrade that does not depend on APT_REFRESH:\n%s", f, l)
				}
			}
		}
		if upgrades == 0 {
			t.Errorf("%s: no apt-get upgrade", f)
		}
	}
	wf, err := os.ReadFile("../.github/workflows/images.yml")
	if err != nil {
		t.Fatal(err)
	}
	pass := regexp.MustCompile(`APT_REFRESH=\$\{\{ github\.run_id \}\}-\$\{\{ github\.run_attempt \}\}`)
	if n := len(pass.FindAll(wf, -1)); n != 2 {
		t.Errorf("images.yml passes APT_REFRESH=<run id>-<attempt> to %d builds, want 2 (base and build)", n)
	}
	check := regexp.MustCompile(`scripts/images-check-index\.sh"? "\$NS/(base|build):`)
	if n := len(check.FindAll(wf, -1)); n != 2 {
		t.Errorf("images.yml checks %d pushed indexes with images-check-index.sh, want 2 (base and build)", n)
	}
}
