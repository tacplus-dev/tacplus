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
		if tacplusyaml.Exists(implDir) {
			cfg, err := tacplusyaml.Read(implDir)
			if err != nil {
				return nil, fmt.Errorf("fleet: service %q: %w", spec.Name, err)
			}
			depends = cfg.Depends
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

		r := resolved{spec: spec, id: id, protoDir: staged, depends: depends}
		services = append(services, r)
		byName[spec.Name] = &services[len(services)-1]
		lock.Services = append(lock.Services, LockedService{
			Name: spec.Name, ID: id, Version: spec.Version, Repo: spec.Repo,
			Commit: commitOf(implDir), Contract: contract, ContractCommit: commitOf(contractRepoDir), Depends: depends,
		})
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

	// 4. Merge: each service's contract files and lock; the fleet's own
	// dependencies.yaml supplies the pins.
	var dirs []string
	for _, r := range services {
		dirs = append(dirs, r.protoDir)
	}
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

	// 5. Whole-fleet checks over the merged tree.
	if err := validateFleet(mergedDir); err != nil {
		return nil, err
	}

	// 6. The lock.
	data, err := yaml.Marshal(lock)
	if err != nil {
		return nil, err
	}
	header := "# Written by tacplus fleet build. The exact commits this fleet was resolved to; regenerate, do not edit.\n"
	if err := os.WriteFile(filepath.Join(manifestDir, LockFile), append([]byte(header), data...), 0o644); err != nil {
		return nil, fmt.Errorf("fleet: writing %s: %w", LockFile, err)
	}
	zap.L().Info("fleet resolved", zap.Int("services", len(services)), zap.String("proto", mergedDir), zap.String("lock", filepath.Join(manifestDir, LockFile)))
	return &Result{Lock: lock, ProtoDir: mergedDir}, nil
}

// validateFleet runs the checks that only a whole fleet can answer: every
// routing label served by a loaded service, every C2S host label served, one
// SMB authority if any SMB member, exactly one minter, a complete and
// collision-free registry.
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
// from tacplus and are skipped), plus the repo's registry.yaml. A proto repo
// shared by many services therefore contributes only this service's files.
func extractContract(protoDir, name string, isService bool) (string, func(), error) {
	root, rootCleanup, err := registry.PrepareDescriptorRoot(protoDir)
	if err != nil {
		return "", nil, fmt.Errorf("preparing descriptors: %w", err)
	}
	defer rootCleanup()
	dp, err := parser.NewDescriptorParser(root)
	if err != nil {
		return "", nil, fmt.Errorf("parsing protos: %w", err)
	}
	all := dp.GetAllFiles()
	var owner string
	for path, fd := range all {
		if fd == nil {
			continue
		}
		if (isService && fd.ServiceName == name) || (!isService && fd.VServiceName == name) {
			owner = path
			break
		}
	}
	if owner == "" {
		return "", nil, fmt.Errorf("no proto declares %q (file-level (spec.service) / (spec.vservice) identity); the implementation must implement the registered contract", name)
	}
	want := map[string]bool{}
	var walk func(path string)
	walk = func(path string) {
		if want[path] || isTacplusProto(path) {
			return
		}
		fd := all[path]
		if fd == nil {
			return
		}
		want[path] = true
		for _, imp := range fd.Imports {
			walk(imp)
		}
	}
	walk(owner)

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
	if _, err := os.Stat(filepath.Join(protoDir, "registry.yaml")); err == nil {
		if err := copyOne("registry.yaml"); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return staged, cleanup, nil
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
