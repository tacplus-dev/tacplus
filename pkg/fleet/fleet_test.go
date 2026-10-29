// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/buildinfo"
	pkgproto "github.com/tacplus-dev/tacplus/pkg/proto"
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
	// Service repos are fixture git repos, cloned into the cache under HOME.
	home, err := os.MkdirTemp("", "tacplus-fleet-home-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	// Every release has an image unless a test says otherwise.
	releaseImage = fakeImage
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// fakeImage is the image a fixture release published: named for its repo,
// its digest derived from the repo and version.
func fakeImage(repo, version string) (string, error) {
	sum := sha256.Sum256([]byte(repo + "@" + version))
	return "ghcr.io/test/" + strings.TrimSuffix(path.Base(repo), "-repo") + "@sha256:" + hex.EncodeToString(sum[:]), nil
}

// gitIn runs git in dir.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// release commits dir's tree (making it a git repo on first use) and tags it
// with each tag in turn, one commit per tag, so every tag has its own commit.
// It returns the repo's file:// URL, which the fleet resolves like any remote.
func release(t *testing.T, dir string, tags ...string) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		gitIn(t, dir, "init", "-q", "-b", "main")
	}
	for _, tag := range tags {
		write(t, filepath.Join(dir, "VERSION"), tag+"\n")
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "-q", "-m", tag)
		gitIn(t, dir, "tag", tag)
	}
	return "file://" + dir
}

// commitOfTag is the commit a fixture repo's tag points at.
func commitOfTag(t *testing.T, url, tag string) string {
	t.Helper()
	return gitIn(t, strings.TrimPrefix(url, "file://"), "rev-parse", tag+"^{commit}")
}

// releasedService is serviceRepoID, released at tags.
func releasedService(t *testing.T, root, name string, id int, registryRef string, depends []string, tags ...string) string {
	t.Helper()
	return release(t, serviceRepoID(t, root, name, id, registryRef, depends), tags...)
}

func TestBuildResolvesChecksMergesAndLocks(t *testing.T) {
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	alpha := releasedService(t, root, "alpha", 101, reg, []string{"beta@1.0"}, "v0.1.0")
	beta := releasedService(t, root, "beta", 102, reg, nil, "v1.2.0", "v1.2.1", "v1.3.0")
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: alpha, id: 101, contract: "+alpha+"}\n    - {name: beta, id: 102, contract: "+beta+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: alpha, version: \"0.1\", repo: "+alpha+"}\n  - {name: beta, version: \"1.2\", repo: "+beta+"}\n")

	res, err := Build(fl, filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lock.Services) != 2 || res.Lock.Services[0].Name != "alpha" || res.Lock.Services[0].ID != 101 || res.Lock.Services[1].ID != 102 {
		t.Fatalf("lock services = %+v", res.Lock.Services)
	}
	if b := res.Lock.Services[1]; b.Version != "1.2.1" || b.Commit != commitOfTag(t, beta, "v1.2.1") {
		t.Fatalf("beta must lock its line's highest patch, 1.2.1 at its commit: %+v", b)
	}
	if want, _ := fakeImage(beta, "1.2.1"); res.Lock.Services[1].Image != want {
		t.Fatalf("beta's image = %q, want the release's %q", res.Lock.Services[1].Image, want)
	}
	if res.Lock.Services[0].Depends[0] != "beta@1.0" || res.Lock.Services[0].Contract != alpha {
		t.Fatalf("lock entry = %+v", res.Lock.Services[0])
	}
	if len(res.Lock.Services[0].InputsDigest) != 64 || res.Lock.Services[0].InputsDigest == res.Lock.Services[1].InputsDigest {
		t.Fatalf("inputs digests = %q, %q", res.Lock.Services[0].InputsDigest, res.Lock.Services[1].InputsDigest)
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
	alpha := releasedService(t, root, "alpha", 101, reg, []string{"beta"}, "v1.0.0")
	rogue := releasedService(t, root, "beta", 7, reg, nil, "v1.0.0")
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: alpha, id: 101, contract: "+alpha+"}\n    - {name: beta, id: 102, contract: "+rogue+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: alpha, version: \"1.0\", repo: "+alpha+"}\n")
	_, err := Build(fl, filepath.Join(root, "out"))
	if err == nil || !strings.Contains(err.Error(), `depends on "beta", which the fleet does not run`) {
		t.Fatalf("missing dependency err = %v", err)
	}

	// A contract that renumbered itself is refused.
	fl2 := fleetRepo(t, filepath.Join(root, "two"), reg, "  - {name: beta, version: \"1.0\", repo: "+rogue+"}\n")
	_, err = Build(fl2, filepath.Join(root, "out2"))
	if err == nil || !strings.Contains(err.Error(), "declares id 7 but the registry says 102") {
		t.Fatalf("wrong id err = %v", err)
	}
}

func TestBuildRefusesVersionBelowFloor(t *testing.T) {
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	alpha := releasedService(t, root, "alpha", 101, reg, []string{"beta@2.0"}, "v1.0.0")
	beta := releasedService(t, root, "beta", 102, reg, nil, "v1.9.0", "v2.0.0")
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: alpha, id: 101, contract: "+alpha+"}\n    - {name: beta, id: 102, contract: "+beta+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: alpha, version: \"1.0\", repo: "+alpha+"}\n  - {name: beta, version: \"1.9\", repo: "+beta+"}\n")
	_, err := Build(fl, filepath.Join(root, "out"))
	if err == nil || !strings.Contains(err.Error(), "needs \"beta\" at 2.0") {
		t.Fatalf("floor err = %v", err)
	}
}

func TestReadManifestRequiresRepoAndVersion(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ service, want string }{
		{`{name: alpha, version: "0.1"}`, "no default"},
		// No implicit main: every service names the line it runs.
		{`{name: alpha, repo: x}`, `names no version`},
		// The lock records the patch; the manifest states the line.
		{`{name: alpha, repo: x, version: "1.2.3"}`, "never a patch pin"},
		{`{name: alpha, repo: x, version: "latest"}`, "must be MAJOR.MINOR"},
		{`{name: alpha, repo: x, version: "1"}`, "must be MAJOR.MINOR"},
	} {
		write(t, filepath.Join(dir, ManifestFile), "registries: [r]\nservices:\n  - "+tc.service+"\n")
		if _, err := ReadManifest(dir); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.service, err, tc.want)
		}
	}
	// A line, and an explicit pre-release, are both accepted.
	write(t, filepath.Join(dir, ManifestFile), "registries: [r]\nservices:\n  - {name: alpha, repo: x, version: \"0.4\"}\n  - {name: beta, repo: y, version: \"0.4.3-rc.1\"}\n")
	if _, err := ReadManifest(dir); err != nil {
		t.Fatal(err)
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
	implURL := release(t, impl, "v0.3.0")
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: gamma, id: 5, contract: "+shared+"}\n    - {name: delta, id: 6, contract: "+shared+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: gamma, version: \"0.3\", repo: "+implURL+"}\n")
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

// A service proto with no (spec.service) option is identified by its package,
// as the generator identifies it, when it declares a service or a grpc_config.
func TestDeclaresIdentityWithoutServiceOption(t *testing.T) {
	src := "syntax = \"proto3\";\npackage operator;\nimport \"sys/spec.proto\";\nmessage Config { option (spec.grpc_config) = {service_name: \"operator\"}; }\n"
	if !declaresIdentity(src, "operator", true) {
		t.Fatal("a grpc_config-declaring file is the service named by its package")
	}
	if declaresIdentity("syntax = \"proto3\";\npackage shapes;\nmessage Box {}\n", "shapes", true) {
		t.Fatal("a plain message file is not a service")
	}
}

// A contract may import another service's proto (operator composes other
// services' config). The import is not in its repo; that service's contract
// supplies it in the merge, and extraction must not fail on it.
func TestExtractContractToleratesCrossContractImports(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "proto")
	write(t, filepath.Join(repo, "services", "operator.proto"), `syntax = "proto3";
package operator;
import "sys/spec.proto";
import "services/oauth.proto";
option (spec.service) = {name: "operator"};
message Config { option (spec.grpc_config) = {service_name: "operator"}; oauth.Config oauth = 1; }
`)
	staged, cleanup, err := extractContract(repo, "operator", true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(staged, "services", "operator.proto")); err != nil {
		t.Fatal("own file missing")
	}
	if _, err := os.Stat(filepath.Join(staged, "services", "oauth.proto")); !os.IsNotExist(err) {
		t.Fatal("a file this repo does not hold cannot be staged from it")
	}
}

// A service_s2s declaring auth_services_from_fleet gets its caller list from the
// fleet: every service that declares a client of it, and nobody else.
func TestBuildStampsFleetCallerList(t *testing.T) {
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	alpha := serviceRepoID(t, root, "alpha", 101, reg, []string{"beta"})
	beta := serviceRepoID(t, root, "beta", 102, reg, nil)
	gamma := serviceRepoID(t, root, "gamma", 103, reg, nil)
	write(t, filepath.Join(alpha, "proto", "services", "alpha.proto"), `syntax = "proto3";
package alpha;
import "sys/spec.proto";
option (spec.service) = {name: "alpha"};
option (spec.grpc_s2s_client) = {service: "beta.Beta"};
message PingRequest { string m = 1; }
service Alpha {
  option (spec.service_s2s) = {auth_services: ["*"]};
  rpc Ping(PingRequest) returns (PingRequest);
}
`)
	write(t, filepath.Join(beta, "proto", "services", "beta.proto"), `syntax = "proto3";
package beta;
import "sys/spec.proto";
option (spec.service) = {name: "beta"};
message PingRequest { string m = 1; }
service Beta {
  option (spec.service_s2s) = {auth_services_from_fleet: true};
  rpc Ping(PingRequest) returns (PingRequest);
}
`)
	alphaURL, betaURL, gammaURL := release(t, alpha, "v1.0.0"), release(t, beta, "v1.0.0"), release(t, gamma, "v1.0.0")
	write(t, filepath.Join(reg, "registry.yaml"), "scope: global\nservices:\n    - {name: alpha, id: 101, contract: "+alphaURL+"}\n    - {name: beta, id: 102, contract: "+betaURL+"}\n    - {name: gamma, id: 103, contract: "+gammaURL+"}\n")
	fl := fleetRepo(t, root, reg, "  - {name: alpha, version: \"1.0\", repo: "+alphaURL+"}\n  - {name: beta, version: \"1.0\", repo: "+betaURL+"}\n  - {name: gamma, version: \"1.0\", repo: "+gammaURL+"}\n")

	res, err := Build(fl, filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	stamped, _ := os.ReadFile(filepath.Join(res.ProtoDir, "services", "beta.proto"))
	if !strings.Contains(string(stamped), `auth_services: ["alpha"]`) || strings.Contains(string(stamped), "auth_services_from_fleet") {
		t.Fatalf("beta's caller list must be exactly its declared fleet callers:\n%s", stamped)
	}
	if strings.Contains(string(stamped), "gamma") {
		t.Fatalf("gamma declares no client of beta and must not be admitted:\n%s", stamped)
	}
	// The contract itself is untouched.
	src, _ := os.ReadFile(filepath.Join(beta, "proto", "services", "beta.proto"))
	if !strings.Contains(string(src), "auth_services_from_fleet: true") {
		t.Fatal("the contract repo must keep declaring auth_services_from_fleet")
	}
}

// Every package tacplus embeds under sys/ is one a contract may reference
// without the fleet pulling a contract for it: isSysPackage must know them
// all, or a service calling a sys contract (redo.Redo, say) fails the fleet
// build as referencing a package nobody declares.
func TestIsSysPackage_KnowsEveryEmbeddedSysPackage(t *testing.T) {
	entries, err := pkgproto.SysProtos.ReadDir("sys")
	if err != nil {
		t.Fatalf("reading embedded sys protos: %v", err)
	}
	pkgRe := regexp.MustCompile(`(?m)^package\s+([A-Za-z0-9_.]+)\s*;`)
	for _, e := range entries {
		data, err := pkgproto.SysProtos.ReadFile("sys/" + e.Name())
		if err != nil {
			t.Fatalf("reading sys/%s: %v", e.Name(), err)
		}
		m := pkgRe.FindSubmatch(data)
		if m == nil {
			t.Fatalf("sys/%s declares no package", e.Name())
		}
		if !isSysPackage(string(m[1])) {
			t.Errorf("sys/%s declares package %q, which isSysPackage does not know", e.Name(), m[1])
		}
	}
}

// The fleet is where a contract's host_tenant_owned meets its real host: the
// sys redo contract (redolog.Redo) says the service at label "redo" is
// tenant-owned, and a fleet whose redo is a shared service is refused instead
// of routing every call to per-tenant instances that do not exist.
func TestValidateFleetChecksHostTenantOwnedAgainstTheHost(t *testing.T) {
	for _, tc := range []struct {
		option string
		ok     bool
	}{
		{`{name: "redo"}`, false},
		{`{name: "redo", tenant_owned: true, pinned_tenant: "restore"}`, true},
	} {
		merged := t.TempDir()
		write(t, filepath.Join(merged, "registry.yaml"), "services:\n    - name: redo\n      id: 1\n")
		write(t, filepath.Join(merged, "services", "redo.proto"), "syntax = \"proto3\";\n\npackage redo;\n\nimport \"sys/spec.proto\";\n\noption (spec.service) = "+tc.option+";\n")
		err := validateFleet(merged)
		if tc.ok && err != nil {
			t.Fatalf("%s: validateFleet: %v", tc.option, err)
		}
		if !tc.ok && (err == nil || !strings.Contains(err.Error(), "host_tenant_owned") || !strings.Contains(err.Error(), "redolog.Redo")) {
			t.Fatalf("%s: validateFleet err = %v, want the host_tenant_owned refusal for redolog.Redo", tc.option, err)
		}
	}
}
