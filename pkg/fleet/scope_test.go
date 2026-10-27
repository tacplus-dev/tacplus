// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scopeTree is a merged fleet tree: alpha imports beta, beta imports gamma,
// delta stands alone, and the vservice q is hosted by delta.
func scopeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	svc := func(name, extra, body string) {
		write(t, filepath.Join(root, "services", name+".proto"), `syntax = "proto3";
package `+name+`;
import "sys/spec.proto";
`+extra+`
option (spec.service) = {name: "`+name+`"};
message PingRequest { string m = 1; `+body+` }
service `+strings.ToUpper(name[:1])+name[1:]+` {
  option (spec.service_s2s) = {auth_services: ["*"]};
  rpc Ping(PingRequest) returns (PingRequest);
}
`)
	}
	svc("alpha", `import "services/beta.proto";`, "beta.PingRequest b = 2;")
	svc("beta", `import "services/gamma.proto";`, "gamma.PingRequest g = 2;")
	// gamma names delta by registry name in an option, without importing it.
	svc("gamma", `option (spec.opa_import) = {source: "q.Q", service: "delta"};`, "")
	svc("delta", "", "")
	// epsilon is named by nobody's proto; beta declares it in tacplus.yaml,
	// which the fleet build leaves in the tree as fleet.lock.
	svc("epsilon", "", "")
	write(t, filepath.Join(root, LockFile), "services:\n    - {name: beta, depends: [epsilon]}\n")
	write(t, filepath.Join(root, "vservices", "q.proto"), `syntax = "proto3";
package q;
import "sys/spec.proto";
option (spec.vservice) = {name: "q"};
option (spec.trait) = {name: "query", host: "delta"};
message Q { string m = 1; }
`)
	write(t, filepath.Join(root, "dependencies.yaml"), "tools:\n    go: \"1.25\"\n")
	write(t, filepath.Join(root, "registry.yaml"), "registries: []\nservices:\n    - {name: alpha, id: 1}\n    - {name: beta, id: 2}\n    - {name: gamma, id: 3}\n    - {name: delta, id: 4}\n")
	return root
}

func TestScopeFollowsDeclaredDependsAndTheirReach(t *testing.T) {
	root := scopeTree(t)
	sc, err := NewScoper(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sc.Scope("alpha", []string{"beta@1.0"}, false)
	if err != nil {
		t.Fatal(err)
	}
	// gamma names delta in an option; delta hosts q. All of it rides along
	// through beta, and none of it is alpha's to declare.
	if strings.Join(got.Files, " ") != "services/alpha.proto services/beta.proto services/delta.proto services/epsilon.proto services/gamma.proto vservices/q.proto" {
		t.Fatalf("alpha's scope = %v", got.Files)
	}
	// delta hosts the vservice q: q names delta, delta never names q.
	if got, _ := sc.Scope("delta", nil, false); strings.Join(got.Files, " ") != "services/delta.proto vservices/q.proto" {
		t.Fatalf("delta's scope = %v", got.Files)
	}
	// ...and so does anything that depends on delta.
	if got, _ := sc.Scope("gamma", []string{"delta"}, false); strings.Join(got.Files, " ") != "services/delta.proto services/gamma.proto vservices/q.proto" {
		t.Fatalf("gamma's scope = %v", got.Files)
	}
	// Naming delta in an option is a direct use: gamma must declare it.
	if _, err := sc.Scope("gamma", nil, false); err == nil || !strings.Contains(err.Error(), "types of delta") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := sc.Scope("delta", nil, true); len(got.Files) != 6 {
		t.Fatalf("whole-fleet scope = %v", got.Files)
	}
}

func TestScopeLeavesATreeOfOnlyOwnProtosAlone(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "services", "solo.proto"), `syntax = "proto3";
package solo;
import "sys/spec.proto";
option (spec.service) = {name: "solo"};
message PingRequest { string m = 1; }
service Solo {
  option (spec.service_s2s) = {auth_services: ["*"]};
  rpc Ping(PingRequest) returns (PingRequest);
}
`)
	write(t, filepath.Join(root, "common", "extra.proto"), "syntax = \"proto3\";\npackage extra;\nmessage E {}\n")
	sc, err := NewScoper(root)
	if err != nil {
		t.Fatal(err)
	}
	// No other service in the tree: nothing to scope.
	got, err := sc.Scope("solo", nil, false)
	if err != nil || len(got.Files) != 2 {
		t.Fatalf("files=%v err=%v", got, err)
	}
	// Another project against the same tree does see solo as another service.
	if _, err := sc.Scope("unrelated", nil, false); err == nil {
		t.Fatal("a project the tree does not declare must declare what it uses")
	}
}

func TestScopeRefusesAnUndeclaredDirectUse(t *testing.T) {
	root := scopeTree(t)
	sc, err := NewScoper(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sc.Scope("alpha", nil, false)
	if err == nil || !strings.Contains(err.Error(), `"alpha" uses the types of beta`) {
		t.Fatalf("err = %v", err)
	}
	if _, err := sc.Scope("nobody", nil, false); err == nil || !strings.Contains(err.Error(), `declares "nobody"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestInputsDigestFollowsDescriptorsNotText(t *testing.T) {
	root := scopeTree(t)
	digest := func(self string, depends []string) string {
		t.Helper()
		sc, err := NewScoper(root)
		if err != nil {
			t.Fatal(err)
		}
		s, err := sc.Scope(self, depends, false)
		if err != nil {
			t.Fatal(err)
		}
		d, err := InputsDigest(root, s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	alpha0, delta0 := digest("alpha", []string{"beta"}), digest("delta", nil)
	if len(alpha0) != 64 || alpha0 == delta0 {
		t.Fatalf("digests alpha=%s delta=%s", alpha0, delta0)
	}
	// A comment in gamma: nothing alpha generates from changed.
	gamma := filepath.Join(root, "services", "gamma.proto")
	write(t, gamma, "// a comment\n"+readFile(t, gamma))
	if d := digest("alpha", []string{"beta"}); d != alpha0 {
		t.Fatal("a comment moved alpha's digest")
	}
	// A field in gamma reaches alpha through beta; delta never sees gamma.
	write(t, gamma, strings.Replace(readFile(t, gamma), "string m = 1; ", "string m = 1; int32 n = 7; ", 1))
	if d := digest("alpha", []string{"beta"}); d == alpha0 {
		t.Fatal("a new field in gamma left alpha's digest alone")
	}
	if d := digest("delta", nil); d != delta0 {
		t.Fatal("gamma's field moved delta's digest")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The runner names the project by its proto package (tacplus.yaml pkg's last
// segment), which need not be its registry name: example/ declares
// package notesapi with (spec.service) = {name: "notes"}.
func TestScopeNamesTheProjectByItsPackage(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "notes", "notes.proto"), `syntax = "proto3";
package notesapi;
import "sys/spec.proto";
option (spec.service) = {name: "notes"};
message PingRequest { string m = 1; }
service Notes {
  option (spec.service_s2s) = {auth_services: ["*"]};
  rpc Ping(PingRequest) returns (PingRequest);
}
`)
	write(t, filepath.Join(root, "client", "notesclient.proto"), `syntax = "proto3";
package notesclient;
import "sys/spec.proto";
option (spec.service) = {name: "notesclient"};
message M { string m = 1; }
`)
	write(t, filepath.Join(root, "registry.yaml"), "services:\n    - {name: notes, id: 1}\n    - {name: notesclient, id: 3}\n")
	sc, err := NewScoper(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sc.Scope("notesapi", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Files, " ") != "notes/notes.proto" {
		t.Fatalf("notesapi's scope = %v", got.Files)
	}
}
