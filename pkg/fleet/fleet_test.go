// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/buildinfo"
)

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fleetRepo(t *testing.T, root, registryRef string, services string) string {
	t.Helper()
	dir := filepath.Join(root, "fleet")
	write(t, filepath.Join(dir, ManifestFile), "registries:\n  - "+registryRef+"\nservices:\n"+services)
	write(t, filepath.Join(dir, "dependencies.yaml"), "tools:\n    go: \"1.25\"\n")
	return dir
}

func TestMain(m *testing.M) {
	// The floor check compares against the running version; tests run unstamped.
	buildinfo.Version = "v9.9.9"
	os.Exit(m.Run())
}

func TestBuildResolvesChecksMergesAndLocks(t *testing.T) {
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	alpha := serviceRepoID(t, root, "alpha", 101, reg, []string{"beta@1.0"})
	beta := serviceRepoID(t, root, "beta", 102, reg, nil)
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: alpha, id: 101, contract: "+alpha+"}\n    - {name: beta, id: 102, contract: "+beta+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: alpha, repo: "+alpha+"}\n  - {name: beta, version: \"1.2\", repo: "+beta+"}\n")

	res, err := Build(fl, filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lock.Services) != 2 || res.Lock.Services[0].Name != "alpha" || res.Lock.Services[0].ID != 101 || res.Lock.Services[1].ID != 102 {
		t.Fatalf("lock services = %+v", res.Lock.Services)
	}
	if res.Lock.Services[0].Depends[0] != "beta@1.0" || res.Lock.Services[0].Contract != alpha {
		t.Fatalf("lock entry = %+v", res.Lock.Services[0])
	}
	for _, p := range []string{"services/alpha.proto", "services/beta.proto", "registry.yaml", "dependencies.yaml"} {
		if _, err := os.Stat(filepath.Join(res.ProtoDir, p)); err != nil {
			t.Errorf("merged tree lacks %s: %v", p, err)
		}
	}
	pins, _ := os.ReadFile(filepath.Join(res.ProtoDir, "dependencies.yaml"))
	if !strings.Contains(string(pins), "1.25") || strings.Contains(string(pins), "1.99") {
		t.Fatalf("merged pins must be the fleet's, not a service's: %s", pins)
	}
	if _, err := os.Stat(filepath.Join(fl, LockFile)); err != nil {
		t.Fatalf("fleet.lock not written: %v", err)
	}
}

func TestBuildRefusesMissingDependencyAndWrongID(t *testing.T) {
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	alpha := serviceRepoID(t, root, "alpha", 101, reg, []string{"beta"})
	rogue := serviceRepoID(t, root, "beta", 7, reg, nil)
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: alpha, id: 101, contract: "+alpha+"}\n    - {name: beta, id: 102, contract: "+rogue+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: alpha, repo: "+alpha+"}\n")
	_, err := Build(fl, filepath.Join(root, "out"))
	if err == nil || !strings.Contains(err.Error(), `depends on "beta", which the fleet does not run`) {
		t.Fatalf("missing dependency err = %v", err)
	}

	// A contract that renumbered itself is refused.
	fl2 := fleetRepo(t, filepath.Join(root, "two"), reg, "  - {name: beta, repo: "+rogue+"}\n")
	_, err = Build(fl2, filepath.Join(root, "out2"))
	if err == nil || !strings.Contains(err.Error(), "declares id 7 but the registry says 102") {
		t.Fatalf("wrong id err = %v", err)
	}
}

func TestBuildRefusesVersionBelowFloor(t *testing.T) {
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	alpha := serviceRepoID(t, root, "alpha", 101, reg, []string{"beta@2.0"})
	beta := serviceRepoID(t, root, "beta", 102, reg, nil)
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: alpha, id: 101, contract: "+alpha+"}\n    - {name: beta, id: 102, contract: "+beta+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: alpha, repo: "+alpha+"}\n  - {name: beta, version: \"1.9\", repo: "+beta+"}\n")
	_, err := Build(fl, filepath.Join(root, "out"))
	if err == nil || !strings.Contains(err.Error(), "needs \"beta\" at 2.0") {
		t.Fatalf("floor err = %v", err)
	}
}

func TestReadManifestRequiresRepoAndMinorVersion(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ManifestFile), "registries: [r]\nservices:\n  - {name: alpha}\n")
	if _, err := ReadManifest(dir); err == nil || !strings.Contains(err.Error(), "no default") {
		t.Fatalf("repo-less service err = %v", err)
	}
	write(t, filepath.Join(dir, ManifestFile), "registries: [r]\nservices:\n  - {name: alpha, repo: x, version: \"1.2.3\"}\n")
	if _, err := ReadManifest(dir); err == nil || !strings.Contains(err.Error(), "never a patch pin") {
		t.Fatalf("patch pin err = %v", err)
	}
}

// serviceRepoID is serviceRepo with a plain integer id.
func serviceRepoID(t *testing.T, root, name string, id int, registryRef string, depends []string) string {
	t.Helper()
	dir := filepath.Join(root, name+"-repo")
	dep := ""
	if len(depends) > 0 {
		dep = "depends:\n"
		for _, d := range depends {
			dep += "  - " + d + "\n"
		}
	}
	write(t, filepath.Join(dir, "tacplus.yaml"), "language: go\nsrc: proto\noutput-dir: model\n"+dep)
	write(t, filepath.Join(dir, "proto", "dependencies.yaml"), "tools:\n    go: \"1.99\"\n")
	write(t, filepath.Join(dir, "proto", "registry.yaml"), "registries: ["+registryRef+"]\nservices:\n    - {name: "+name+", id: "+itoaInt(id)+"}\n")
	write(t, filepath.Join(dir, "proto", "services", name+".proto"), `syntax = "proto3";
package `+name+`;
import "sys/spec.proto";
option (spec.service) = {name: "`+name+`"};
message PingRequest { string m = 1; }
service `+strings.ToUpper(name[:1])+name[1:]+` {
  option (spec.service_s2s) = {auth_services: ["*"]};
  rpc Ping(PingRequest) returns (PingRequest);
}
`)
	return dir
}

func itoaInt(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

// A shared contract repo (one proto repo declaring many services, the platform
// shape) contributes only the named service's files and what they import; the
// implementation repo need not carry a proto at all.
func TestBuildExtractsAContractFromASharedProtoRepo(t *testing.T) {
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	shared := filepath.Join(root, "platform-proto")
	write(t, filepath.Join(shared, "proto", "dependencies.yaml"), "{}\n")
	write(t, filepath.Join(shared, "proto", "registry.yaml"), "services:\n    - {name: gamma, id: 5}\n    - {name: delta, id: 6}\n")
	write(t, filepath.Join(shared, "proto", "common", "shapes.proto"), "syntax = \"proto3\";\npackage shapes;\nmessage Box { int32 w = 1; }\n")
	for _, n := range []string{"gamma", "delta"} {
		write(t, filepath.Join(shared, "proto", "services", n+".proto"), `syntax = "proto3";
package `+n+`;
import "sys/spec.proto";
import "common/shapes.proto";
option (spec.service) = {name: "`+n+`"};
message `+strings.ToUpper(n[:1])+n[1:]+`Request { shapes.Box box = 1; }
service `+strings.ToUpper(n[:1])+n[1:]+` {
  option (spec.service_s2s) = {auth_services: ["*"]};
  rpc Do(`+strings.ToUpper(n[:1])+n[1:]+`Request) returns (`+strings.ToUpper(n[:1])+n[1:]+`Request);
}
`)
	}
	impl := filepath.Join(root, "gamma-impl")
	write(t, filepath.Join(impl, "tacplus.yaml"), "language: go\nsrc: "+filepath.Join(shared, "proto")+"\noutput-dir: model\n")
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: gamma, id: 5, contract: "+shared+"}\n    - {name: delta, id: 6, contract: "+shared+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: gamma, repo: "+impl+"}\n")
	res, err := Build(fl, filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"services/gamma.proto", "common/shapes.proto", "registry.yaml"} {
		if _, err := os.Stat(filepath.Join(res.ProtoDir, p)); err != nil {
			t.Errorf("merged tree lacks %s", p)
		}
	}
	if _, err := os.Stat(filepath.Join(res.ProtoDir, "services", "delta.proto")); !os.IsNotExist(err) {
		t.Error("a service the fleet does not run must not be dragged in from the shared contract repo")
	}
	if res.Lock.Services[0].ContractCommit == "" {
		t.Error("lock must record the contract's commit")
	}
}

// A contract may reference a sibling file's package only inside an option
// (an OPA-eval input source); that file is part of the contract and comes along.
func TestExtractContractFollowsOptionReferences(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "proto")
	write(t, filepath.Join(shared, "vservices", "inputs.proto"), "syntax = \"proto3\";\npackage evalinputs;\nimport \"sys/spec.proto\";\nmessage EvalInput { option (spec.opa_eval) = {}; string who = 1; }\n")
	write(t, filepath.Join(shared, "vservices", "unrelated.proto"), "syntax = \"proto3\";\npackage unrelated;\nmessage X {}\n")
	write(t, filepath.Join(shared, "services", "host.proto"), `syntax = "proto3";
package host;
import "sys/spec.proto";
option (spec.service) = {name: "host"};
option (spec.opa_import) = {source: "evalinputs.EvalInput", service: "opa"};
message Req {}
service Host { option (spec.service_s2s) = {}; rpc Do(Req) returns (Req); }
`)
	staged, cleanup, err := extractContract(shared, "host", true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(staged, "vservices", "inputs.proto")); err != nil {
		t.Fatal("the option-referenced file must be part of the contract")
	}
	if _, err := os.Stat(filepath.Join(staged, "vservices", "unrelated.proto")); !os.IsNotExist(err) {
		t.Fatal("an unreferenced sibling must not come along")
	}
}

// A trait hosted by the service (the record store's query plane) names the
// service as host without the service naming it; it is part of the contract.
func TestExtractContractIncludesTraitsHostedByTheService(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "proto")
	write(t, filepath.Join(shared, "services", "store.proto"), `syntax = "proto3";
package store;
import "sys/spec.proto";
option (spec.service) = {name: "store"};
message Req {}
service Store { option (spec.service_s2s) = {}; rpc Do(Req) returns (Req); }
`)
	write(t, filepath.Join(shared, "vservices", "query.proto"), "syntax = \"proto3\";\npackage query;\nimport \"sys/spec.proto\";\noption (spec.trait) = {name: \"query\" host: \"store\" s2s_service: \"RecordMap\"};\n")
	write(t, filepath.Join(shared, "vservices", "other.proto"), "syntax = \"proto3\";\npackage other;\nimport \"sys/spec.proto\";\noption (spec.trait) = {name: \"other\" host: \"elsewhere\" s2s_service: \"X\"};\n")
	staged, cleanup, err := extractContract(shared, "store", true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(staged, "vservices", "query.proto")); err != nil {
		t.Fatal("a trait hosted by the service must be part of its contract")
	}
	if _, err := os.Stat(filepath.Join(staged, "vservices", "other.proto")); !os.IsNotExist(err) {
		t.Fatal("a trait hosted elsewhere must not come along")
	}
}
