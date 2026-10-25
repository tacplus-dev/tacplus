// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"gopkg.in/yaml.v3"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/registry"
	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

// Scope is what one project generates from inside a fleet's merged proto
// tree: its own contract, the contracts it names in tacplus.yaml `depends`
// (and whatever those reach), and the files they import or name in options.
// A project with grpc-servers/grpc-clients: all has the whole tree.
//
// The same scope answers two questions with one declaration: which protos the
// compiler feeds a service's generation, and which descriptors decide whether
// its image must be rebuilt (InputsDigest). A dependency's patch that touches
// no descriptor in the scope changes neither.
type Scope struct {
	Files []string // proto paths relative to the tree root, sorted
}

// Scoper reads a merged tree once (identities, packages, text) and answers
// Scope for each project built against it.
type Scoper struct {
	root        string
	files       []string
	texts       map[string]string
	byPackage   map[string]string
	fileService map[string]string   // contract file -> service name it declares
	ownerOf     map[string]string   // service or vservice name -> its contract file
	dependsOf   map[string][]string // service -> its declared depends, from the tree's fleet.lock
}

// NewScoper indexes the merged tree at root. Reading is text-based like
// extractContract: imports, option references ("pkg.Type" strings) and files
// hosted by a service.
func NewScoper(root string) (*Scoper, error) {
	files, err := protoFiles(root)
	if err != nil {
		return nil, err
	}
	svcNames, vsNames, err := registry.ScanRegistryNames(root)
	if err != nil {
		return nil, fmt.Errorf("scope: scanning %s: %w", root, err)
	}
	sc := &Scoper{root: root, files: files, texts: map[string]string{}, byPackage: map[string]string{}, fileService: map[string]string{}, ownerOf: map[string]string{}, dependsOf: map[string][]string{}}
	// The fleet build leaves its lock in the merged tree: each service's
	// declared depends, so a dependency's declaration carries (coop declares
	// stratum for its records; tenantecho, which depends on coop, need not).
	if data, err := os.ReadFile(filepath.Join(root, LockFile)); err == nil {
		var lock Lock
		if err := yaml.Unmarshal(data, &lock); err != nil {
			return nil, fmt.Errorf("scope: reading %s in %s: %w", LockFile, root, err)
		}
		for _, svc := range lock.Services {
			sc.dependsOf[svc.Name] = svc.Depends
		}
	}
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return nil, err
		}
		src := stripComments(string(data))
		sc.texts[rel] = src
		if pm := packageRe.FindStringSubmatch(src); pm != nil {
			sc.byPackage[pm[1]] = rel
		}
		for _, n := range svcNames {
			if declaresIdentity(src, n, true) {
				sc.fileService[rel] = n
				sc.ownerOf[n] = rel
			}
		}
		for _, n := range vsNames {
			if declaresIdentity(src, n, false) {
				sc.ownerOf[n] = rel
			}
		}
	}
	return sc, nil
}

// Scope computes the scope of project `self` (a registry service name) with
// the given tacplus.yaml depends; wholeFleet is grpc-servers/grpc-clients:
// all. A project the tree does not declare (a client) is scoped by its
// depends alone.
//
// Declaration is enforced at the project's own boundary: a file self owns
// may only reach another service's contract when that service is in
// depends. What a dependency reaches in turn is the dependency's declaration,
// not self's (letsencrypt → kernel → opa needs only kernel in letsencrypt's
// depends).
func (sc *Scoper) Scope(self string, depends []string, wholeFleet bool) (*Scope, error) {
	if wholeFleet || !sc.holdsOthers(self) {
		return &Scope{Files: append([]string(nil), sc.files...)}, nil
	}
	selfFile, hasSelf := sc.ownerOf[self]
	if !hasSelf && len(depends) == 0 {
		return nil, fmt.Errorf("scope: no proto in %s declares %q and tacplus.yaml names no depends; a project generating against a fleet tree declares its own contract, the services it uses, or grpc-servers/grpc-clients: all", sc.root, self)
	}
	declared := map[string]bool{self: true}
	for _, d := range depends {
		name, _ := tacplusyaml.SplitDepend(d)
		declared[name] = true
	}

	want := map[string]bool{}
	undeclared := map[string]bool{}
	entered := map[string]bool{}
	var walk func(rel, owner string)
	walk = func(rel, owner string) {
		if isTacplusProto(rel) {
			return
		}
		if s := sc.fileService[rel]; s != "" && s != owner {
			if owner == self && !declared[s] {
				undeclared[s] = true
			}
			owner = s
		}
		if want[rel] {
			return
		}
		if s := sc.fileService[rel]; s != "" && !entered[s] {
			// First file of service s: what s declares it depends on is
			// part of s's contract as generated.
			entered[s] = true
			defer func() {
				for _, d := range sc.dependsOf[s] {
					name, _ := tacplusyaml.SplitDepend(d)
					if f, ok := sc.ownerOf[name]; ok {
						walk(f, name)
					}
				}
			}()
		}
		src, ok := sc.texts[rel]
		if !ok {
			// A merged fleet tree is complete; the parse that follows names
			// the missing file.
			return
		}
		want[rel] = true
		for _, imp := range importsOf(src) {
			walk(imp, owner)
		}
		for _, m := range optionRefRe.FindAllStringSubmatch(src, -1) {
			if target, ok := sc.byPackage[m[1]]; ok {
				walk(target, owner)
			}
		}
		// An option may name a service by registry name: (spec.opa_import)
		// {service: "opa"}.
		for _, m := range serviceRefRe.FindAllStringSubmatch(src, -1) {
			if target, ok := sc.ownerOf[m[1]]; ok {
				walk(target, owner)
			}
		}
	}
	if hasSelf {
		walk(selfFile, self)
	}
	for name := range declared {
		if name == self {
			continue
		}
		f, ok := sc.ownerOf[name]
		if !ok {
			return nil, fmt.Errorf("scope: %q depends on %q, which no proto in the tree declares", self, name)
		}
		walk(f, name)
	}
	// Files hosted by a service in the scope (a trait or surface whose
	// host/label is that service: stratum's record query plane) name the
	// service; the service does not name them. They belong to every service
	// reached, not only self, until no service is added.
	seenHost := map[string]bool{}
	for {
		added := false
		for _, rel := range sortedKeys(want) {
			host := sc.fileService[rel]
			if host == "" || seenHost[host] {
				continue
			}
			seenHost[host] = true
			hosted := hostedByRe(host)
			for _, f := range sc.files {
				if !want[f] && hosted.MatchString(sc.texts[f]) {
					walk(f, host)
					added = true
				}
			}
		}
		if !added {
			break
		}
	}
	if len(undeclared) > 0 {
		names := make([]string, 0, len(undeclared))
		for n := range undeclared {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("%q uses the types of %s without declaring it; add it to depends in tacplus.yaml", self, strings.Join(names, ", "))
	}
	out := make([]string, 0, len(want))
	for rel := range want {
		out = append(out, rel)
	}
	sort.Strings(out)
	return &Scope{Files: out}, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// holdsOthers reports whether the tree declares a service other than self:
// a project's own proto dir holds only its own, and there is nothing to
// scope; a fleet's merged tree holds every service.
func (sc *Scoper) holdsOthers(self string) bool {
	for _, n := range sc.fileService {
		if n != self {
			return true
		}
	}
	return false
}

// ProtoFiles lists every proto under a tree, relative and sorted, the way
// the scope sees it.
func ProtoFiles(root string) ([]string, error) { return protoFiles(root) }

// Stage copies the scope's files, with the tree's dependencies.yaml and
// registry.yaml, into dest: a proto root holding exactly what the project
// generates from.
func Stage(root string, sc *Scope, dest string) error {
	copyOne := func(rel string) error {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		dst := filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	}
	for _, rel := range sc.Files {
		if err := copyOne(rel); err != nil {
			return fmt.Errorf("staging %s: %w", rel, err)
		}
	}
	for _, rel := range []string{"dependencies.yaml", "registry.yaml"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			if err := copyOne(rel); err != nil {
				return fmt.Errorf("staging %s: %w", rel, err)
			}
		}
	}
	return nil
}

// InputsDigest is a sha256 over the descriptors of everything in the scope,
// the sys protos and well-known types they import included: the shape the
// generated code is a function of. Source info (comments, positions) is
// stripped first, so a comment edit or reformat leaves the digest alone and a
// field, message, RPC or option change moves it.
func InputsDigest(root string, sc *Scope) (string, error) {
	staged, err := os.MkdirTemp("", "tacplus-scope-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)
	if err := Stage(root, sc, staged); err != nil {
		return "", err
	}
	descRoot, cleanup, err := registry.PrepareDescriptorRoot(staged)
	if err != nil {
		return "", err
	}
	defer cleanup()
	dp, err := parser.NewDescriptorParser(descRoot)
	if err != nil {
		return "", fmt.Errorf("inputs digest: %w", err)
	}
	raw := dp.RawFileDescriptors()
	names := make([]string, 0, len(raw))
	for n := range raw {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fd := proto.Clone(raw[n]).(*descriptorpb.FileDescriptorProto)
		fd.SourceCodeInfo = nil
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(fd)
		if err != nil {
			return "", err
		}
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// serviceRefRe matches a service: option value naming a service by registry
// name (not a "pkg.Type" string, which optionRefRe reads).
var serviceRefRe = regexp.MustCompile(`\bservice\s*:\s*"([a-z][a-z0-9_-]*)"`)

// hostedByRe matches a host: or label: option naming the service.
func hostedByRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?:host|label)\s*:\s*"` + regexp.QuoteMeta(name) + `"`)
}
