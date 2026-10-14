// Command chaos runs the Profile A model-stress / resilience / interop harness
// against a deployment profile and prints the report. Exit code is non-zero if
// any oracle is RED or an unexpected failure occurred.
//
// Usage:
//
//	go run ./cross-test/chaos/cmd/chaos                       # baked-in Profile A defaults
//	go run ./cross-test/chaos/cmd/chaos -profile path.yaml    # a profile file
//	go run ./cross-test/chaos/cmd/chaos -inject-coll1-bug     # demo: harness goes RED
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/tacplus-dev/tacplus/cross-test/chaos/orchestrator"
	"github.com/tacplus-dev/tacplus/cross-test/chaos/profile"
)

func main() {
	profilePath := flag.String("profile", "", "path to a profile YAML (default: baked-in Profile A)")
	injectBug := flag.Bool("inject-coll1-bug", false, "re-introduce the COLL-1 dropped-delta bug (demonstrates the WATCH oracle going RED)")
	seed := flag.Int64("seed", 0, "override the profile seed (0 = use profile's)")
	flag.Parse()

	var p *profile.Profile
	if *profilePath != "" {
		loaded, err := profile.Load(*profilePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
		p = loaded
	} else {
		p = profile.DefaultProfileA()
	}
	if *injectBug {
		p.InjectColl1Bug = true
	}
	if *seed != 0 {
		p.Seed = *seed
	}

	mesh, err := orchestrator.Deploy(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	rep := mesh.Run()
	fmt.Print(rep.String())
	if !rep.Pass() {
		os.Exit(1)
	}
}
