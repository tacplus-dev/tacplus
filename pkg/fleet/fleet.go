// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package fleet is the fleet build: a deployment repo's fleet.yaml names the
// registries a fleet trusts, the tacplus floor, and every service the fleet
// runs with the repo that implements it. The build resolves each implementation
// at its version, checks every service against the registries (same id, name
// declared) and against each other (every declared dependency is in the fleet,
// at a version that satisfies the dependent's floor), merges the protos into one
// tree, runs the checks that only make sense for a whole fleet, and writes a
// commit-level lock so the same fleet builds identically later.
//
//	registries:
//	  - https://github.com/tacplus-dev/registry.git
//	tacplus: "2.2"
//	services:
//	  - name: kernel
//	    version: "0.1"              # MAJOR.MINOR -> latest patch; omit for latest
//	    repo: https://github.com/tacplus-dev/kernel.git
//	  - name: doppelganger
//	    repo: https://github.com/tacplus-dev/doppelganger.git
//
// The registry decides a service's name and id and says where its contract
// lives; the manifest decides which implementation runs, always as an explicit
// repo, so a fleet may run its own kernel under the same name and id. The
// deployment repo's own dependencies.yaml supplies the language and tool pins
// for everything generated from the merged tree; the services' own pins are
// their own builds' business.
package fleet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/tacplus-dev/tacplus/pkg/buildinfo"
	"github.com/tacplus-dev/tacplus/pkg/gittag"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/protosrc"
	"github.com/tacplus-dev/tacplus/pkg/registry"
	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

// ManifestFile is the manifest's name in a deployment repo.
const ManifestFile = "fleet.yaml"

// LockFile is written beside the manifest after a successful build.
const LockFile = "fleet.lock"

// Manifest is fleet.yaml.
type Manifest struct {
	Registries []string      `yaml:"registries"`
	Tacplus    string        `yaml:"tacplus,omitempty"`
	Services   []ServiceSpec `yaml:"services"`
}

// ServiceSpec is one fleet service: its registry name, the MAJOR.MINOR it runs
// (latest patch; empty = latest), and the repo implementing it.
type ServiceSpec struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version,omitempty"`
	Repo    string `yaml:"repo"`
}

// Lock is fleet.lock: what the build resolved, commit by commit.
type Lock struct {
	Tacplus    string          `yaml:"tacplus"`
	Registries []LockedSource  `yaml:"registries"`
	Services   []LockedService `yaml:"services"`
	Libraries  []LockedLibrary `yaml:"libraries,omitempty"` // contract-only vservices pulled in by reference
}

// LockedSource is a resolved repo: where it came from and the exact commit.
type LockedSource struct {
	Repo   string `yaml:"repo"`
	Commit string `yaml:"commit"`
}

// LockedService is one resolved service.
type LockedService struct {
	Name           string   `yaml:"name"`
	ID             int32    `yaml:"id"`
	Version        string   `yaml:"version,omitempty"`
	Repo           string   `yaml:"repo"`
	Commit         string   `yaml:"commit"`
	Contract       string   `yaml:"contract,omitempty"`
	ContractCommit string   `yaml:"contract_commit,omitempty"`
	Depends        []string `yaml:"depends,omitempty"`
	// InputsDigest is a sha256 over the descriptors of everything this
	// service generates from (its scope: own contract, depends closure, sys
	// protos; the whole tree for grpc-servers/grpc-clients: all). The image
	// build keys on it: same digest and same commit, same image.
	InputsDigest string `yaml:"inputs_digest,omitempty"`
}

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	versionRe = regexp.MustCompile(`^(\d+)\.(\d+)$`)
)

// ReadManifest reads and validates fleet.yaml in dir.
func ReadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("fleet: %w", err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("fleet: parsing %s: %w", ManifestFile, err)
	}
	if len(m.Registries) == 0 {
		return nil, fmt.Errorf("fleet: %s names no registries", ManifestFile)
	}
	if len(m.Services) == 0 {
		return nil, fmt.Errorf("fleet: %s lists no services", ManifestFile)
	}
	seen := map[string]bool{}
	for i, s := range m.Services {
		if !nameRe.MatchString(s.Name) {
			return nil, fmt.Errorf("fleet: services[%d]: %q is not a registry name", i, s.Name)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("fleet: %s lists %q twice", ManifestFile, s.Name)
		}
		seen[s.Name] = true
		if s.Repo == "" {
			return nil, fmt.Errorf("fleet: %s: service %q names no repo; every service states the implementation it runs, there is no default", ManifestFile, s.Name)
		}
		if s.Version != "" && !versionRe.MatchString(s.Version) {
			return nil, fmt.Errorf("fleet: %s: service %q version %q must be MAJOR.MINOR (the latest patch runs; never a patch pin)", ManifestFile, s.Name, s.Version)
		}
	}
	if err := tacplusyaml.CheckFloor(m.Tacplus, buildinfo.Version); err != nil {
		return nil, fmt.Errorf("fleet: %w", err)
	}
	return &m, nil
}

// Result is what Build produced.
type Result struct {
	Lock     *Lock
	ProtoDir string // the merged, validated proto tree
}

// Build runs the fleet build for the deployment repo in manifestDir, writing
// the merged proto tree under outDir/proto and fleet.lock beside the manifest.
func Build(manifestDir, outDir string) (*Result, error) {
	m, err := ReadManifest(manifestDir)
	if err != nil {
		return nil, err
	}
	pins := filepath.Join(manifestDir, "dependencies.yaml")
	if _, err := os.Stat(pins); err != nil {
		return nil, fmt.Errorf("fleet: %s/dependencies.yaml is required: it holds the language and tool pins for everything generated from the fleet's merged protos", manifestDir)
	}

	var cleanups []func()
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()

	// 1. Registries: unioned, disjoint.
	lock := &Lock{Tacplus: m.Tacplus}
	union, regCleanup, err := registry.LoadAuthorities(m.Registries, func(ref string) (string, func(), error) {
		res, err := protosrc.Resolve(ref, "latest")
		if err != nil {
			return "", nil, err
		}
		lock.Registries = append(lock.Registries, LockedSource{Repo: ref, Commit: commitOf(res.Dir)})
		return res.Dir, res.Cleanup, nil
	})
	if err != nil {
		return nil, fmt.Errorf("fleet: %w", err)
	}
	cleanups = append(cleanups, regCleanup)

	// 2. Each service: registered; its implementation resolved (for the commit
	// and its depends); its CONTRACT gathered from the registry's home for it
	// (the proto that declares it plus everything that proto imports, so a
	// shared proto repo contributes only this service's files); the contract's
	// lock carries the registry's id and the proto declares the name.
	type resolved struct {
		spec     ServiceSpec
		id       int32
		protoDir string // staged: this service's contract files + the contract repo's registry.yaml
		depends  []string
		whole    bool // grpc-servers/grpc-clients: all — generates from the whole tree
	}
	var services []resolved
	byName := map[string]*resolved{}
	for _, spec := range m.Services {
		id, isService, ok := idOf(union, spec.Name)
		if !ok {
			return nil, fmt.Errorf("fleet: service %q is not in any registry the fleet names (%s); register it first", spec.Name, strings.Join(m.Registries, ", "))
		}
		contract, _ := union.ContractOf(spec.Name)
		if contract == "" {
			return nil, fmt.Errorf("fleet: service %q is registered without a contract; the registry must say where its proto lives", spec.Name)
		}
		tag := "latest"
		if spec.Version != "" {
			tag = spec.Version + ".latest"
		}

		implDir, _, implCleanup, err := resolveImplementation(spec.Repo, tag)
		if err != nil {
			return nil, fmt.Errorf("fleet: service %q: implementation %s: %w", spec.Name, spec.Repo, err)
		}
		cleanups = append(cleanups, implCleanup)
		var depends []string
		whole := false
		if tacplusyaml.Exists(implDir) {
			cfg, err := tacplusyaml.Read(implDir)
			if err != nil {
				return nil, fmt.Errorf("fleet: service %q: %w", spec.Name, err)
			}
			depends = cfg.Depends
			whole = cfg.WholeFleet()
		}

		// The contract rides the implementation's version when they are the
		// same repo; a shared contract repo (one proto repo for many services)
		// is taken at its head.
		contractTag := "latest"
		if sameRepo(contract, spec.Repo) {
			contractTag = tag
		}
		contractRepoDir, contractProtoDir, contractCleanup, err := resolveImplementation(contract, contractTag)
		if err != nil {
			return nil, fmt.Errorf("fleet: service %q: contract %s: %w", spec.Name, contract, err)
		}
		cleanups = append(cleanups, contractCleanup)

		staged, stageCleanup, err := extractContract(contractProtoDir, spec.Name, isService)
		if err != nil {
			return nil, fmt.Errorf("fleet: service %q: contract %s: %w", spec.Name, contract, err)
		}
		cleanups = append(cleanups, stageCleanup)

		// The contract's lock must carry the registry's id: a fleet may run
		// its own kernel, but never under a different number.
		svcLock, err := registry.Load(contractProtoDir)
		if err != nil {
			return nil, fmt.Errorf("fleet: service %q: reading the contract's registry.yaml: %w", spec.Name, err)
		}
		var lockID int32
		var inLock bool
		if isService {
			lockID, inLock = svcLock.ServiceIDIfPresent(spec.Name)
		} else {
			lockID, inLock = svcLock.VServiceFieldIDIfPresent(spec.Name)
		}
		if !inLock {
			return nil, fmt.Errorf("fleet: service %q: %s has no entry for it in registry.yaml; run tacplus registry there", spec.Name, contract)
		}
		if lockID != id {
			return nil, fmt.Errorf("fleet: service %q: %s declares id %d but the registry says %d; an implementation keeps the registry's id", spec.Name, contract, lockID, id)
		}

		r := resolved{spec: spec, id: id, protoDir: staged, depends: depends, whole: whole}
		services = append(services, r)
		byName[spec.Name] = &services[len(services)-1]
		lock.Services = append(lock.Services, LockedService{
			Name: spec.Name, ID: id, Version: spec.Version, Repo: spec.Repo,
			Commit: commitOf(implDir), Contract: contract, ContractCommit: commitOf(contractRepoDir), Depends: depends,
		})
	}

	// 2b. Contract-only references. A listed service's contract may name a
	// package no listed contract declares: a vservice such as the trace
	// topics gateway publishes to. If a registry knows that name as a
	// vservice, its contract is fetched and extracted too (a library, not a
	// service: nothing runs it). A referenced package that is a registered
	// SERVICE the fleet does not run is an error: the manifest is complete.
	var stagedDirs []string
	for _, r := range services {
		stagedDirs = append(stagedDirs, r.protoDir)
	}
	libraries, libCleanups, err := resolveReferencedContracts(stagedDirs, m, union, lock)
	cleanups = append(cleanups, libCleanups...)
	if err != nil {
		return nil, err
	}

	// 3. Closure: every declared dependency is in the fleet, at a version that
	// satisfies the dependent's floor. The manifest is the complete list; this
	// is where the kernel build "follows the dependency".
	var errs []string
	for _, r := range services {
		for _, d := range r.depends {
			depName, floor := tacplusyaml.SplitDepend(d)
			dep, ok := byName[depName]
			if !ok {
				errs = append(errs, fmt.Sprintf("%q depends on %q, which the fleet does not run; add it to %s", r.spec.Name, depName, ManifestFile))
				continue
			}
			if floor != "" && dep.spec.Version != "" && !satisfies(dep.spec.Version, floor) {
				errs = append(errs, fmt.Sprintf("%q needs %q at %s or a later minor; the fleet runs %s", r.spec.Name, depName, floor, dep.spec.Version))
			}
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return nil, fmt.Errorf("fleet: dependencies not satisfied:\n  %s", strings.Join(errs, "\n  "))
	}

	// 4. Merge: each service's contract files, the referenced libraries, and
	// the fleet's own dependencies.yaml for the pins.
	var dirs []string
	for _, r := range services {
		dirs = append(dirs, r.protoDir)
	}
	dirs = append(dirs, libraries...)
	pinsDir, err := os.MkdirTemp("", "tacplus-fleet-pins-*")
	if err != nil {
		return nil, err
	}
	cleanups = append(cleanups, func() { os.RemoveAll(pinsDir) })
	pinData, err := os.ReadFile(pins)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(pinsDir, "dependencies.yaml"), pinData, 0o644); err != nil {
		return nil, err
	}
	dirs = append(dirs, pinsDir)
	mergedDir := filepath.Join(outDir, "proto")
	if err := os.RemoveAll(mergedDir); err != nil {
		return nil, err
	}
	if err := protosrc.MergeSources(dirs, mergedDir); err != nil {
		return nil, fmt.Errorf("fleet: merging protos: %w", err)
	}
	// 4b. Caller lists the fleet owns: a service_s2s declaring
	// auth_services_from_fleet gets every fleet service that declares a client
	// of it. Only this build knows who is in the fleet.
	if err := stampFleetAuth(mergedDir); err != nil {
		return nil, err
	}
	if err := writeFleetLock(mergedDir, m.Registries, union); err != nil {
		return nil, err
	}

	// 5. Whole-fleet checks over the merged tree.
	if err := validateFleet(mergedDir); err != nil {
		return nil, err
	}

	// 5b. Each service's scope and inputs digest. The scope is what its
	// tacplus.yaml declares (own contract + depends, or the whole tree); a
	// service reaching an undeclared contract fails the build here, naming
	// the declaration to add.
	if err := writeLock(lock, filepath.Join(mergedDir, LockFile)); err != nil {
		return nil, err
	}
	scoper, err := NewScoper(mergedDir)
	if err != nil {
		return nil, fmt.Errorf("fleet: %w", err)
	}
	for i, r := range services {
		sc, err := scoper.Scope(r.spec.Name, r.depends, r.whole)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		digest, err := InputsDigest(mergedDir, sc)
		if err != nil {
			return nil, fmt.Errorf("fleet: service %q: %w", r.spec.Name, err)
		}
		lock.Services[i].InputsDigest = digest
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return nil, fmt.Errorf("fleet: scopes not declared:\n  %s", strings.Join(errs, "\n  "))
	}

	// 6. The lock, beside the manifest and in the merged tree (the compiler
	// reads each service's depends from it when it scopes a generation).
	for _, p := range []string{filepath.Join(manifestDir, LockFile), filepath.Join(mergedDir, LockFile)} {
		if err := writeLock(lock, p); err != nil {
			return nil, err
		}
	}
	zap.L().Info("fleet resolved", zap.Int("services", len(services)), zap.String("proto", mergedDir), zap.String("lock", filepath.Join(manifestDir, LockFile)))
	return &Result{Lock: lock, ProtoDir: mergedDir}, nil
}

// LockedLibrary is a contract-only vservice the fleet pulled in by reference.
type LockedLibrary struct {
	Name           string `yaml:"name"`
	ID             int32  `yaml:"id"`
	Contract       string `yaml:"contract"`
	ContractCommit string `yaml:"contract_commit"`
	ReferencedBy   string `yaml:"referenced_by"`
}

// resolveReferencedContracts scans the staged contract dirs for option-level
// package references to packages declared in none of them, resolves each
// through the registries' union (a vservice: fetch and extract its contract as
// a library; a service: it must be in the manifest), and repeats until the set
// is closed. It returns the library dirs to merge.
func resolveReferencedContracts(stagedDirs []string, m *Manifest, union *registry.Registry, lock *Lock) ([]string, []func(), error) {
	var libDirs []string
	var cleanups []func()
	listed := map[string]bool{}
	for _, s := range m.Services {
		listed[s.Name] = true
	}
	all := append([]string{}, stagedDirs...)
	for round := 0; round < 16; round++ {
		declared := map[string]bool{}
		refs := map[string]string{} // package -> first referencing file
		for _, dir := range all {
			files, err := protoFiles(dir)
			if err != nil {
				return nil, cleanups, err
			}
			for _, rel := range files {
				data, err := os.ReadFile(filepath.Join(dir, rel))
				if err != nil {
					return nil, cleanups, err
				}
				src := stripComments(string(data))
				if pm := packageRe.FindStringSubmatch(src); pm != nil {
					declared[pm[1]] = true
				}
				for _, mm := range optionRefRe.FindAllStringSubmatch(src, -1) {
					if _, seen := refs[mm[1]]; !seen {
						refs[mm[1]] = rel
					}
				}
			}
		}
		var missing []string
		for pkg := range refs {
			if !declared[pkg] && !isSysPackage(pkg) {
				missing = append(missing, pkg)
			}
		}
		if len(missing) == 0 {
			return libDirs, cleanups, nil
		}
		sort.Strings(missing)
		for _, pkg := range missing {
			if _, ok := union.ServiceEntry(pkg); ok {
				if !listed[pkg] {
					return nil, cleanups, fmt.Errorf("fleet: %s references service %q, which the fleet does not run; add it to %s", refs[pkg], pkg, ManifestFile)
				}
				return nil, cleanups, fmt.Errorf("fleet: %s references %q but the listed service's contract declares no package %q", refs[pkg], pkg, pkg)
			}
			vs, ok := union.VServiceEntry(pkg)
			if !ok {
				return nil, cleanups, fmt.Errorf("fleet: %s references package %q, which no listed contract declares and no registry the fleet names knows", refs[pkg], pkg)
			}
			if vs.Contract == "" {
				return nil, cleanups, fmt.Errorf("fleet: vservice %q (referenced by %s) is registered without a contract", pkg, refs[pkg])
			}
			repoDir, protoDir, cleanup, err := resolveImplementation(vs.Contract, "latest")
			if err != nil {
				return nil, cleanups, fmt.Errorf("fleet: library %q: contract %s: %w", pkg, vs.Contract, err)
			}
			cleanups = append(cleanups, cleanup)
			staged, stageCleanup, err := extractContract(protoDir, pkg, false)
			if err != nil {
				return nil, cleanups, fmt.Errorf("fleet: library %q: contract %s: %w", pkg, vs.Contract, err)
			}
			cleanups = append(cleanups, stageCleanup)
			libDirs = append(libDirs, staged)
			all = append(all, staged)
			lock.Libraries = append(lock.Libraries, LockedLibrary{Name: pkg, ID: vs.FieldID, Contract: vs.Contract, ContractCommit: commitOf(repoDir), ReferencedBy: refs[pkg]})
			zap.L().Info("fleet: pulled a referenced contract", zap.String("vservice", pkg), zap.String("referenced_by", refs[pkg]), zap.String("contract", vs.Contract))
		}
	}
	return nil, cleanups, fmt.Errorf("fleet: contract references did not close after 16 rounds")
}

// isSysPackage reports whether a referenced package is one tacplus supplies.
func isSysPackage(pkg string) bool {
	switch pkg {
	case "spec", "tokenrequest", "x509", "healthreport", "minter", "redolog", "servicehealth", "services", "entitlements", "accounting_method", "services_config", "tenant_config", "project_config", "mcp", "query", "logger", "host", "key", "jwk", "jwkview", "publicjwksview", "smbcert", "tlscert", "grpcconfig", "opalib", "accounting", "trace", "tags", "token", "tokencache", "ip", "http", "httpclient", "filesystem", "diskstore", "cloudidentity", "cloudvendor", "credpair", "alpn", "entitlement", "entitlementview", "errors", "outputformat", "tacplus", "eventbus_support":
		return true
	}
	return false
}

// writeFleetLock writes the merged tree's registry.yaml: a lock naming the
// fleet's registries, with an entry for every service and vservice the merged
// protos declare, each at the id the registries' union gives it. A declared
// name no registry knows is an error: the fleet runs only registered things.
func writeFleetLock(mergedDir string, registries []string, union *registry.Registry) error {
	svcNames, vsNames, err := registry.ScanRegistryNames(mergedDir)
	if err != nil {
		return fmt.Errorf("fleet: scanning the merged protos: %w", err)
	}
	lock := &registry.Registry{Registries: registries}
	var missing []string
	for _, n := range svcNames {
		e, ok := union.ServiceEntry(n)
		if !ok {
			missing = append(missing, "service "+n)
			continue
		}
		c := *e
		lock.Services = append(lock.Services, &c)
	}
	for _, n := range vsNames {
		e, ok := union.VServiceEntry(n)
		if !ok {
			missing = append(missing, "vservice "+n)
			continue
		}
		c := *e
		lock.VServices = append(lock.VServices, &c)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("fleet: the merged protos declare names no registry the fleet names knows:\n  %s", strings.Join(missing, "\n  "))
	}
	return lock.SaveTo(mergedDir)
}

// writeLock writes the lock with its header.
func writeLock(lock *Lock, path string) error {
	data, err := yaml.Marshal(lock)
	if err != nil {
		return err
	}
	header := "# Written by tacplus fleet build. The exact commits this fleet was resolved to; regenerate, do not edit.\n"
	if err := os.WriteFile(path, append([]byte(header), data...), 0o644); err != nil {
		return fmt.Errorf("fleet: writing %s: %w", path, err)
	}
	return nil
}

// validateFleet runs the checks that only a whole fleet can answer: every
// routing label served by a loaded service, every C2S host label served, one
// SMB authority if any SMB member, at most one approval broker, a route field
// on every rpc a tenant-owned service hosts, every host_tenant_owned surface's
// host tenant-owned, exactly one minter, a complete and collision-free
// registry.
func validateFleet(mergedDir string) error {
	if err := registry.Validate(mergedDir); err != nil {
		return fmt.Errorf("fleet: %w", err)
	}
	root, cleanup, err := registry.PrepareDescriptorRoot(mergedDir)
	if err != nil {
		return fmt.Errorf("fleet: preparing descriptors: %w", err)
	}
	defer cleanup()
	dp, err := parser.NewDescriptorParser(root)
	if err != nil {
		return fmt.Errorf("fleet: parsing merged protos: %w", err)
	}
	for _, check := range []func(*parser.DescriptorParser) error{
		parser.ValidateRoutingLabels,
		parser.ValidateC2SHostLabels,
		parser.ValidateSmbAuthority,
		parser.ValidateApprovalBroker,
		parser.ValidateHostTenantOwned,
		parser.ValidateTenantRouting,
	} {
		if err := check(dp); err != nil {
			return fmt.Errorf("fleet: %w", err)
		}
	}
	if err := parser.ValidateTokenMinters(root, true); err != nil {
		return fmt.Errorf("fleet: %w", err)
	}
	return nil
}

// resolveImplementation fetches a service's repo at tag and returns the repo
// dir, its proto dir (proto/ by convention when present), and a cleanup.
func resolveImplementation(repo, tag string) (repoDir, protoDir string, cleanup func(), err error) {
	resolveTag := tag
	if protosrc.IsGitSource(repo) {
		resolveTag = gittag.ToMinorLatest(tag)
	}
	res, err := protosrc.Resolve(repo, resolveTag)
	if err != nil {
		return "", "", nil, err
	}
	protoDir = res.Dir
	if _, err := os.Stat(filepath.Join(res.Dir, "proto", "registry.yaml")); err == nil {
		protoDir = filepath.Join(res.Dir, "proto")
	}
	return res.Dir, protoDir, res.Cleanup, nil
}

// extractContract stages, from a contract repo's proto tree, exactly the
// files that are name's contract: the proto whose file-level identity is name
// and, transitively, every proto it imports (sys/, google/ and generated/ come
// from tacplus and are skipped). A proto repo shared by many services therefore
// contributes only this service's files. Its registry.yaml is not copied: the
// merged tree gets one lock synthesized from the registries' union.
//
// Identity and imports are read from the proto text, not from a descriptor
// parse: a shared repo may hold protos this generator cannot parse (another
// service's, needing a newer sys proto), and that must not block a service
// whose own closure is fine. The merged tree is parsed and checked afterwards.
func extractContract(protoDir, name string, isService bool) (string, func(), error) {
	files, err := protoFiles(protoDir)
	if err != nil {
		return "", nil, err
	}
	owner := ""
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(protoDir, rel))
		if err != nil {
			return "", nil, err
		}
		if declaresIdentity(string(data), name, isService) {
			if owner != "" {
				return "", nil, fmt.Errorf("both %s and %s declare %q", owner, rel, name)
			}
			owner = rel
		}
	}
	if owner == "" {
		return "", nil, fmt.Errorf("no proto declares %q (file-level (spec.service) / (spec.vservice) identity); the implementation must implement the registered contract", name)
	}
	want := map[string]bool{}
	var walk func(rel string) error
	walk = func(rel string) error {
		if want[rel] || isTacplusProto(rel) {
			return nil
		}
		data, err := os.ReadFile(filepath.Join(protoDir, rel))
		if err != nil {
			// Not in this repo: another service's proto (operator imports
			// oauth's and kmaker's to compose their config). That service's
			// own contract supplies it in the merge, and the parse of the
			// merged tree fails if nothing does.
			zap.L().Info("fleet: contract imports a file outside its repo; expecting another contract to supply it",
				zap.String("contract", owner), zap.String("import", rel))
			return nil
		}
		want[rel] = true
		for _, imp := range importsOf(string(data)) {
			if err := walk(imp); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(owner); err != nil {
		return "", nil, err
	}
	// A contract may name another file's package inside an option without
	// importing it: (spec.opa_import).source, a state_import, a client. When
	// that package is declared in this same repo it is part of the contract
	// too (an OPA-eval input, say), so follow it; references to packages
	// declared elsewhere are other services, which the fleet lists.
	byPackage := map[string]string{}
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(protoDir, rel))
		if err != nil {
			return "", nil, err
		}
		if pm := packageRe.FindStringSubmatch(stripComments(string(data))); pm != nil {
			byPackage[pm[1]] = rel
		}
	}
	// A contract also owns the files in its repo that are HOSTED by it: a
	// trait whose host is this service (the record store's query plane), or
	// a surface whose label is. They name the service; the service does not
	// name them, so the import walk above cannot reach them.
	hostedRe := hostedByRe(name)
	for _, rel := range files {
		if want[rel] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(protoDir, rel))
		if err != nil {
			return "", nil, err
		}
		if hostedRe.MatchString(stripComments(string(data))) {
			if err := walk(rel); err != nil {
				return "", nil, err
			}
		}
	}
	for {
		added := false
		for rel := range want {
			data, _ := os.ReadFile(filepath.Join(protoDir, rel))
			for _, m := range optionRefRe.FindAllStringSubmatch(stripComments(string(data)), -1) {
				if target, ok := byPackage[m[1]]; ok && !want[target] {
					if err := walk(target); err != nil {
						return "", nil, err
					}
					added = true
				}
			}
		}
		if !added {
			break
		}
	}

	staged, err := os.MkdirTemp("", "tacplus-fleet-contract-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(staged) }
	copyOne := func(rel string) error {
		data, err := os.ReadFile(filepath.Join(protoDir, rel))
		if err != nil {
			return err
		}
		dst := filepath.Join(staged, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	}
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := copyOne(p); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("copying %s: %w", p, err)
		}
	}
	return staged, cleanup, nil
}

// protoFiles lists every .proto under dir, relative, sorted, skipping hidden
// dirs and tacplus-supplied trees.
func protoFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			if rel != "." && (d.Name()[0] == '.' || isTacplusProto(rel+"/")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".proto") {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

var (
	importRe       = regexp.MustCompile(`(?m)^\s*import\s+(?:public\s+|weak\s+)?"([^"]+)"\s*;`)
	serviceRe      = regexp.MustCompile(`(?s)option\s*\(\s*spec\.service\s*\)\s*=\s*\{([^}]*)\}`)
	vserviceRe     = regexp.MustCompile(`(?s)option\s*\(\s*spec\.vservice\s*\)\s*=\s*\{([^}]*)\}`)
	nameRe2        = regexp.MustCompile(`\bname\s*:\s*"([^"]+)"`)
	packageRe      = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][\w.]*)\s*;`)
	serviceBlockRe = regexp.MustCompile(`(?m)^\s*service\s+[A-Za-z_][A-Za-z0-9_]*\s*\{`)
	grpcConfigRe   = regexp.MustCompile(`\(\s*spec\.grpc_config\s*\)`)
	// optionRefRe matches a "package.Name" string value inside a spec option,
	// e.g. source: "userentitlements.EntitlementEvalInput".
	optionRefRe = regexp.MustCompile(`[a-z_]+\s*:\s*"([a-z_][a-z0-9_]*)\.[A-Z][A-Za-z0-9_.]*"`)
)

// importsOf returns a proto file's import paths, in order.
func importsOf(src string) []string {
	var out []string
	for _, m := range importRe.FindAllStringSubmatch(stripComments(src), -1) {
		out = append(out, m[1])
	}
	return out
}

// declaresIdentity reports whether src carries the file-level identity name:
// (spec.service) for a service (its explicit name, or the package when no name
// is given), (spec.vservice) for a vservice. A service file with no
// (spec.service) option at all is identified by its package, the way the
// generator does, provided it declares a gRPC service or a grpc_config.
func declaresIdentity(src, name string, isService bool) bool {
	src = stripComments(src)
	re := vserviceRe
	if isService {
		re = serviceRe
	}
	m := re.FindStringSubmatch(src)
	if m != nil {
		if n := nameRe2.FindStringSubmatch(m[1]); n != nil {
			return n[1] == name
		}
		if !isService {
			return false
		}
	} else if !isService || !(serviceBlockRe.MatchString(src) || grpcConfigRe.MatchString(src)) {
		return false
	}
	if pm := packageRe.FindStringSubmatch(src); pm != nil {
		return pm[1] == name
	}
	return false
}

// stripComments removes // and /* */ comments so a commented-out option or
// import is not read as a live one.
func stripComments(src string) string {
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")
	return regexp.MustCompile(`(?m)//[^\n]*`).ReplaceAllString(src, "")
}

// isTacplusProto reports whether a proto path is supplied by tacplus itself
// (embedded sys protos, well-known google types, generated stubs).
func isTacplusProto(path string) bool {
	p := filepath.ToSlash(path)
	return strings.HasPrefix(p, "sys/") || strings.HasPrefix(p, "google/") || strings.HasPrefix(p, "generated/")
}

// sameRepo reports whether two repo references name the same repository,
// ignoring scheme, host form and a trailing .git.
func sameRepo(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSuffix(strings.TrimSpace(s), ".git")
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimPrefix(s, "ssh://")
		s = strings.TrimPrefix(s, "git@")
		s = strings.Replace(s, ":", "/", 1)
		return strings.TrimSuffix(s, "/")
	}
	return norm(a) == norm(b)
}

func idOf(u *registry.Registry, name string) (int32, bool, bool) {
	if id, ok := u.ServiceIDIfPresent(name); ok {
		return id, true, true
	}
	if id, ok := u.VServiceFieldIDIfPresent(name); ok {
		return id, false, true
	}
	return 0, false, false
}

func contractOf(u *registry.Registry, name string) string {
	c, _ := u.ContractOf(name)
	return c
}

// satisfies reports whether running version MAJOR.MINOR meets floor MAJOR.MINOR.
func satisfies(running, floor string) bool {
	r := versionRe.FindStringSubmatch(running)
	f := versionRe.FindStringSubmatch(floor)
	if r == nil || f == nil {
		return false
	}
	rMaj, _ := strconv.Atoi(r[1])
	rMin, _ := strconv.Atoi(r[2])
	fMaj, _ := strconv.Atoi(f[1])
	fMin, _ := strconv.Atoi(f[2])
	return rMaj == fMaj && rMin >= fMin
}

// commitOf is the checked-out commit of a git dir, or "local" for a plain dir.
func commitOf(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "local"
	}
	return strings.TrimSpace(string(out))
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
