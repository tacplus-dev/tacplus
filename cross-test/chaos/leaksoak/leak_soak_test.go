//go:build soak

// Copyright (c) 2026 Second Star Cavalier LLC

// Package leaksoak closes docs/KNOWN_TEST_GAPS.md PC-L-9: the chaos LeakOracle
// was only ever fed hand-built counter trends, so an SM that leaked on teardown
// would not have been caught by anything. This harness generates a real Go
// model, runs a real generated ServiceManager through repeated create/teardown
// cycles in its own process, and feeds the resulting per-cycle
// node.Resources trend into the existing, unchanged oracle.LeakOracle.
//
// It lives in its own package rather than in cross-test/chaos/oracle or
// cross-test/chaos/node so that generation/exec machinery stays out of those
// pure packages; the driver it runs lives under testdata/ (ignored by the go
// tool) exactly like cross-test/cert-stream/testdata.
//
// Excluded from `go test ./...` (the `soak` build tag) because it generates
// and builds a full driver binary per run — too slow for the mandatory
// `just test` gate. Run via `just leak-soak`.
package leaksoak

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/cross-test/chaos/node"
	"gitlab.com/secondstarcavalier/schematic/tacplus/cross-test/chaos/oracle"
)

// The generated module's identity. tacplus derives the "self" service from the
// last segment of the module base path, so pkg must end in the service name
// declared by the fixture protos (svc/certstream) — otherwise the service
// runtime, and with it chassis/servicemanager, is not emitted at all.
const (
	generatedPkg    = "com.example.certstream"
	generatedModule = "example.com/certstream/tacplus/model"
)

type scrape struct {
	Goroutines int64 `json:"goroutines"`
	OpenFDs    int64 `json:"open_fds"`
}

type driverResult struct {
	Baseline     scrape   `json:"baseline"`
	Peak         scrape   `json:"peak"`
	Cycles       []scrape `json:"cycles"`
	FDsSupported bool     `json:"fds_supported"`
}

// TestGeneratedServiceManagerDoesNotLeakAcrossSoakCycles is the PC-L-9 harness:
// real generated ServiceManager, real runtime.NumGoroutine()/FD scrapes, fed to
// the real LeakOracle.
func TestGeneratedServiceManagerDoesNotLeakAcrossSoakCycles(t *testing.T) {
	root := repoRoot(t)
	work := t.TempDir()

	out := generateModel(t, root, work)
	installDriver(t, root, out)

	res := runDriver(t, out)

	if len(res.Cycles) == 0 {
		t.Fatal("driver reported no soak cycles")
	}
	// Guard against a harness that measures nothing: if the live ServiceManager
	// never held more goroutines than the post-teardown baseline, the cycles
	// exercised no lifecycle and a flat trend would be meaningless.
	if res.Peak.Goroutines <= res.Baseline.Goroutines {
		t.Fatalf("live ServiceManager held %d goroutines vs baseline %d: the cycle exercised nothing",
			res.Peak.Goroutines, res.Baseline.Goroutines)
	}
	if !res.FDsSupported {
		t.Logf("open-FD scraping unavailable on %s (no /proc/self/fd); only the goroutine leg of the leak oracle is exercised here", runtime.GOOS)
	}
	t.Logf("baseline=%+v peak=%+v cycles=%+v fds=%v", res.Baseline, res.Peak, res.Cycles, res.FDsSupported)

	perCycle := make([]node.Resources, 0, len(res.Cycles))
	for _, c := range res.Cycles {
		perCycle = append(perCycle, node.Resources{Goroutines: c.Goroutines, OpenFDs: c.OpenFDs})
	}
	verdict := oracle.LeakOracle{
		Baseline: node.Resources{Goroutines: res.Baseline.Goroutines, OpenFDs: res.Baseline.OpenFDs},
	}.Evaluate(perCycle)
	if !verdict.Pass {
		t.Fatalf("%s: generated ServiceManager leaks across create/teardown cycles:\n%s",
			verdict.Name, strings.Join(verdict.Violations, "\n"))
	}
}

// generateModel builds the tacplus binary and generates the Go model for the
// cert-stream fixture protos, returning the generated model directory.
func generateModel(t *testing.T, root, work string) string {
	t.Helper()

	bin := os.Getenv("TACPLUS_BIN")
	if bin == "" {
		// Not under work: the generated output dir is itself named "tacplus".
		bin = filepath.Join(t.TempDir(), "tacplus")
		run(t, root, "go", "build", "-o", bin, ".")
	}

	protoSrc := filepath.Join(root, "cross-test", "cert-stream", "proto")
	cfg := "language: \"go\"\npkg: \"" + generatedPkg + "\"\nsrc: \"" + protoSrc + "\"\noutput-dir: \"tacplus\"\n"
	if err := os.WriteFile(filepath.Join(work, "tacplus.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write tacplus.yaml: %v", err)
	}
	run(t, work, bin, "build", "--config", "tacplus.yaml", "--src", protoSrc)

	out := filepath.Join(work, "tacplus", "model")
	if _, err := os.Stat(filepath.Join(out, "chassis", "servicemanager", "servicemanager.go")); err != nil {
		t.Fatalf("generated model has no chassis/servicemanager: %v", err)
	}
	return out
}

// installDriver drops the leak-soak driver and the module's go.mod into the
// generated model and resolves its dependencies.
func installDriver(t *testing.T, root, out string) {
	t.Helper()

	testdata := filepath.Join(root, "cross-test", "chaos", "leaksoak", "testdata")
	if err := os.MkdirAll(filepath.Join(out, "leaksoak"), 0o755); err != nil {
		t.Fatalf("mkdir leaksoak: %v", err)
	}
	copyFile(t, filepath.Join(testdata, "sm_leak_driver.go"), filepath.Join(out, "leaksoak", "main.go"))
	copyFile(t, filepath.Join(testdata, "go.mod"), filepath.Join(out, "go.mod"))

	run(t, out, "go", "mod", "tidy")
}

func runDriver(t *testing.T, out string) driverResult {
	t.Helper()

	stdout := run(t, out, "go", "run", "./leaksoak")
	var res driverResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &res); err != nil {
		t.Fatalf("parse driver output %q: %v", stdout, err)
	}
	return res
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s (in %s): %v\n%s", name, strings.Join(args, " "), dir, err, stderr.String())
	}
	return string(stdout)
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
