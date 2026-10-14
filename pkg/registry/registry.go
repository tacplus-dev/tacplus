// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// Package registry provides stable ID assignment for services and vservices.
// Both share the same ID space (1-99) since they are assigned as field numbers
// in the same TokenEntitlements proto message. Field 100 is reserved for azp.
package registry

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	pkgproto "github.com/tacplus-dev/tacplus/pkg/proto"
)

// ServiceEntry is a single service entry in the registry.
type ServiceEntry struct {
	Name              string `yaml:"name"`
	ID                int32  `yaml:"id"`
	TenantSliceField  int32  `yaml:"tenant_slice_field,omitempty"`
	ProjectSliceField int32  `yaml:"project_slice_field,omitempty"`
}

// VServiceEntry is a single vservice entry in the registry.
type VServiceEntry struct {
	Name    string `yaml:"name"`
	FieldID int32  `yaml:"field_id"`
}

// Registry maps service and vservice names to stable numeric IDs.
// Both share the same ID space (1-99). Field 100 is reserved for azp.
// Loaded from registry.yaml in the proto source directory.
type Registry struct {
	// IDMin is the lowest ID this registry may auto-assign.
	//
	// Service IDs share one 1-99 space across every proto repo that is merged
	// into a build, but each repo allocates from its OWN registry. Two repos
	// therefore both pick "max local ID + 1" and collide: that is how the app's
	// echo service was handed 47, the same ID platform had already given
	// operator. The multi-source merge hard-errors on the collision, so nothing
	// ships broken — but the app cannot generate at all until a human picks a
	// free number by hand.
	//
	// Giving each repo a floor removes the collision by construction and needs
	// no cross-repo discovery: platform allocates from 1, an app from (say) 60.
	// The merge's hard error remains the backstop for a misconfigured floor.
	IDMin     int32            `yaml:"id_min,omitempty"`
	Services  []*ServiceEntry  `yaml:"services"`
	VServices []*VServiceEntry `yaml:"vservices"`
	inputDir  string
	dirty     bool
	byName    map[string]*ServiceEntry
	vsByName  map[string]*VServiceEntry
}

// Load loads registry.yaml from inputDir.
// If inputDir is empty or registry.yaml does not exist, an empty registry is returned.
func Load(inputDir string) (*Registry, error) {
	r := &Registry{
		inputDir: inputDir,
		byName:   make(map[string]*ServiceEntry),
		vsByName: make(map[string]*VServiceEntry),
	}
	if inputDir == "" {
		return r, nil
	}
	path := filepath.Join(inputDir, "registry.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		zap.L().Info("registry.yaml not found, using empty registry", zap.String("path", path))
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading registry.yaml: %w", err)
	}
	if err := yaml.Unmarshal(data, r); err != nil {
		return nil, fmt.Errorf("parsing registry.yaml: %w", err)
	}
	// Duplicate names fail closed: the map keeps only the last entry while the
	// slices keep both, so lookups and iteration would disagree about what the
	// registry says.
	for _, e := range r.Services {
		if _, dup := r.byName[e.Name]; dup {
			return nil, fmt.Errorf("parsing registry.yaml: duplicate service name %q", e.Name)
		}
		r.byName[e.Name] = e
	}
	for _, e := range r.VServices {
		if _, dup := r.vsByName[e.Name]; dup {
			return nil, fmt.Errorf("parsing registry.yaml: duplicate vservice name %q", e.Name)
		}
		r.vsByName[e.Name] = e
	}
	zap.L().Info("loaded registry",
		zap.String("path", path),
		zap.Int("services", len(r.Services)),
		zap.Int("vservices", len(r.VServices)))
	return r, nil
}

// ServiceID returns the stable numeric ID for serviceName.
// If the service is not in the registry, the next sequential ID is auto-assigned.
func (r *Registry) ServiceID(name string) (int32, error) {
	if e, ok := r.byName[name]; ok {
		return e.ID, nil
	}
	if r.inputDir == "" {
		return 0, fmt.Errorf("service %q not in registry and no registry file available", name)
	}
	next := r.nextID()
	if next >= 100 {
		return 0, fmt.Errorf("registry ID overflow: all IDs 1-99 are taken (100 reserved for azp)")
	}
	zap.L().Warn("service not in registry, auto-assigning ID",
		zap.String("name", name), zap.Int32("id", next))
	e := &ServiceEntry{Name: name, ID: next}
	r.Services = append(r.Services, e)
	r.byName[name] = e
	r.dirty = true
	return next, nil
}

// ServiceIDIfPresent returns the registered ID for name and true if it exists.
// Unlike ServiceID it never auto-assigns a new ID, so callers can probe
// registry membership without mutating the registry.
func (r *Registry) ServiceIDIfPresent(name string) (int32, bool) {
	if e, ok := r.byName[name]; ok {
		return e.ID, true
	}
	return 0, false
}

// VServiceFieldID returns the stable TokenEntitlements field number for a vservice.
// If the vservice is not in the registry, the next sequential ID is auto-assigned.
func (r *Registry) VServiceFieldID(name string) (int32, error) {
	if e, ok := r.vsByName[name]; ok {
		return e.FieldID, nil
	}
	if r.inputDir == "" {
		return 0, fmt.Errorf("vservice %q not in registry and no registry file available", name)
	}
	next := r.nextID()
	if next >= 100 {
		return 0, fmt.Errorf("registry ID overflow: all IDs 1-99 are taken (100 reserved for azp)")
	}
	e := &VServiceEntry{Name: name, FieldID: next}
	r.VServices = append(r.VServices, e)
	r.vsByName[name] = e
	r.dirty = true
	return next, nil
}

// AssignTenantSliceField ensures serviceName has a tenant_slice_field.
// ServiceID must have been called for this name first.
func (r *Registry) AssignTenantSliceField(name string) (int32, error) {
	e, ok := r.byName[name]
	if !ok {
		return 0, fmt.Errorf("service %q not in registry — call ServiceID first", name)
	}
	if e.TenantSliceField != 0 {
		return e.TenantSliceField, nil
	}
	e.TenantSliceField = r.nextTenantSliceField()
	r.dirty = true
	return e.TenantSliceField, nil
}

// TenantSlices returns entries with tenant_slice_field set, sorted by field number.
func (r *Registry) TenantSlices() []*ServiceEntry {
	var out []*ServiceEntry
	for _, e := range r.Services {
		if e.TenantSliceField != 0 {
			out = append(out, e)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].TenantSliceField < out[j-1].TenantSliceField; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// AssignProjectSliceField ensures serviceName has a project_slice_field.
// ServiceID must have been called for this name first.
func (r *Registry) AssignProjectSliceField(name string) (int32, error) {
	e, ok := r.byName[name]
	if !ok {
		return 0, fmt.Errorf("service %q not in registry — call ServiceID first", name)
	}
	if e.ProjectSliceField != 0 {
		return e.ProjectSliceField, nil
	}
	e.ProjectSliceField = r.nextProjectSliceField()
	r.dirty = true
	return e.ProjectSliceField, nil
}

// ProjectSlices returns entries with project_slice_field set, sorted by field number.
func (r *Registry) ProjectSlices() []*ServiceEntry {
	var out []*ServiceEntry
	for _, e := range r.Services {
		if e.ProjectSliceField != 0 {
			out = append(out, e)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ProjectSliceField < out[j-1].ProjectSliceField; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Save writes the registry back to registry.yaml if it was modified.
func (r *Registry) Save() error {
	if !r.dirty || r.inputDir == "" {
		return nil
	}
	// IDMin is omitempty because zero is exactly how Load spells "no floor":
	// a registry that never had one must not gain `id_min: 0` on save.
	type registryFile struct {
		IDMin     int32            `yaml:"id_min,omitempty"`
		Services  []*ServiceEntry  `yaml:"services"`
		VServices []*VServiceEntry `yaml:"vservices,omitempty"`
	}
	data, err := yaml.Marshal(&registryFile{IDMin: r.IDMin, Services: r.Services, VServices: r.VServices})
	if err != nil {
		return fmt.Errorf("marshaling registry.yaml: %w", err)
	}
	path := filepath.Join(r.inputDir, "registry.yaml")
	return os.WriteFile(path, append([]byte("# Managed by tacplus — do not edit IDs manually.\n"), data...), 0644)
}

// nextID returns max(existing)+1, at or above the registry's floor. Registry
// IDs are TokenEntitlements field numbers, so gap-filling on every allocation
// would risk reusing a retired ID's number for a different service. Only when
// max+1 overflows the 1-99 space do we fall back to scanning for the lowest
// free ID — a single manually assigned entry sitting at 99 must not falsely
// report the whole space exhausted with 98 IDs still free.
func (r *Registry) nextID() int32 {
	// An empty (or low) registry in a repo with a floor starts AT the floor,
	// which is the whole point: the app's first service must not land on 1.
	floor := r.IDMin
	if floor < 1 {
		floor = 1
	}
	used := make(map[int32]bool, len(r.Services)+len(r.VServices))
	max := floor - 1
	for _, e := range r.Services {
		used[e.ID] = true
		if e.ID > max {
			max = e.ID
		}
	}
	for _, e := range r.VServices {
		used[e.FieldID] = true
		if e.FieldID > max {
			max = e.FieldID
		}
	}
	if max+1 <= 99 {
		return max + 1
	}
	for id := floor; id <= 99; id++ {
		if !used[id] {
			return id
		}
	}
	// Field 100 is reserved for azp; callers turn this into the overflow error.
	return 100
}

func (r *Registry) nextTenantSliceField() int32 {
	var max int32
	for _, e := range r.Services {
		if e.TenantSliceField > max {
			max = e.TenantSliceField
		}
	}
	return max + 1
}

func (r *Registry) nextProjectSliceField() int32 {
	var max int32
	for _, e := range r.Services {
		if e.ProjectSliceField > max {
			max = e.ProjectSliceField
		}
	}
	return max + 1
}

// Validate checks that registry.yaml contains entries for all services and
// vservices declared in the proto directory. Returns an error listing any
// missing entries. Does not modify the registry.
func Validate(protoDir string) error {
	reg, err := Load(protoDir)
	if err != nil {
		return err
	}

	var missing []string

	serviceNames, vserviceNames, err := ScanRegistryNames(protoDir)
	if err != nil {
		return fmt.Errorf("scanning proto declarations: %w", err)
	}
	for _, name := range serviceNames {
		if _, ok := reg.byName[name]; !ok {
			missing = append(missing, fmt.Sprintf("service %q (add to registry.yaml services section)", name))
		}
	}
	for _, name := range vserviceNames {
		if _, ok := reg.vsByName[name]; !ok {
			missing = append(missing, fmt.Sprintf("vservice %q (add to registry.yaml vservices section)", name))
		}
	}

	// Services and vservices share one ID space, so every ID must be unique
	// across BOTH kinds and be a legal TokenEntitlements field number: 1-99,
	// with 100 reserved for azp.
	idOwner := make(map[int32]string)
	checkID := func(kind, name string, id int32) {
		who := fmt.Sprintf("%s %q", kind, name)
		if id < 1 || id > 99 {
			missing = append(missing, fmt.Sprintf("illegal ID: %s has ID %d, outside the usable range 1-99 (100 is reserved for azp)", who, id))
			return
		}
		if existing, ok := idOwner[id]; ok {
			missing = append(missing, fmt.Sprintf("ID collision: %s has ID %d already used by %s", who, id, existing))
			return
		}
		idOwner[id] = who
	}
	for _, e := range reg.Services {
		checkID("service", e.Name, e.ID)
	}
	for _, e := range reg.VServices {
		checkID("vservice", e.Name, e.FieldID)
	}

	if len(missing) > 0 {
		return fmt.Errorf("registry.yaml is incomplete:\n  %s\n\nRun `tacplus registry --proto-dir .` to fix, or add entries manually with the next available ID", strings.Join(missing, "\n  "))
	}
	return nil
}

// ScanRegistryNames returns the file-level (spec.service).name and
// (spec.vservice).name declarations anywhere under protoDir, using the same
// descriptor model the generator uses. Scanning only services/ and vservices/
// missed declarations the generator itself finds and consumes — a service
// declared under any other package directory was never assigned an ID nor
// reported missing.
//
// The parse is scoped to the tree's own files (imports are still resolved) so
// it works pre-generation, when generated/*.proto has not been written yet.
func ScanRegistryNames(protoDir string) (services []string, vservices []string, err error) {
	if _, err := os.Stat(protoDir); os.IsNotExist(err) {
		return nil, nil, nil
	}
	var protoFiles []string
	if err := filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}
		relPath, err := filepath.Rel(protoDir, path)
		if err != nil {
			return err
		}
		protoFiles = append(protoFiles, filepath.ToSlash(relPath))
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if len(protoFiles) == 0 {
		return nil, nil, nil
	}
	sort.Strings(protoFiles)
	descriptorRoot, cleanup, err := prepareVServiceDescriptorRoot(protoDir)
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()

	p, err := parser.NewDescriptorParserForFiles(descriptorRoot, protoFiles)
	if err != nil {
		return nil, nil, err
	}
	seenService := make(map[string]bool)
	seenVService := make(map[string]bool)
	for _, path := range protoFiles {
		fd, err := p.GetFileDescriptor(path)
		if err != nil {
			return nil, nil, err
		}
		if fd.ServiceName != "" && !seenService[fd.ServiceName] {
			seenService[fd.ServiceName] = true
			services = append(services, fd.ServiceName)
		}
		if fd.VServiceName != "" && !seenVService[fd.VServiceName] {
			seenVService[fd.VServiceName] = true
			vservices = append(vservices, fd.VServiceName)
		}
	}
	sort.Strings(services)
	sort.Strings(vservices)
	return services, vservices, nil
}

func prepareVServiceDescriptorRoot(protoDir string) (string, func(), error) {
	tempDir, err := os.MkdirTemp("", "tacplus-vservice-registry-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("creating vservice descriptor root: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}
	if err := copyProtoTree(protoDir, tempDir); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := copyEmbeddedProtoFS(pkgproto.SysProtos, "sys", tempDir, "sys"); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := writeDescriptorGeneratedStubs(tempDir); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tempDir, cleanup, nil
}

func writeDescriptorGeneratedStubs(root string) error {
	stubs := map[string]string{
		"services.proto": "// CODE GENERATED by tacplus — DO NOT EDIT\n" +
			"// Minimal stub for descriptor-only scans.\n\n" +
			"syntax = \"proto3\";\n\npackage services;\n\n" +
			"enum ServiceName {\n  SERVICE_NAME_UNSPECIFIED = 0;\n  SERVICE_NAME_ANY = 99;\n}\n",
		"accounting_method.proto": "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
			"syntax = \"proto3\";\n\npackage accounting_method;\n\n" +
			"message AccountingMethod {}\n",
		"entitlements.proto": "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
			"syntax = \"proto3\";\n\npackage entitlements;\n\n" +
			"message TokenEntitlements {\n  repeated string grants = 1;\n  repeated string azp = 100;\n}\n",
		"services_config.proto": "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
			"syntax = \"proto3\";\n\npackage services_config;\n\n" +
			"message Services {}\n",
		"tenant_config.proto": "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
			"syntax = \"proto3\";\n\npackage tenant_config;\n\n" +
			"message TenantConfig {}\n",
		"project_config.proto": "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
			"syntax = \"proto3\";\n\npackage project_config;\n\n" +
			"message ProjectConfig {}\n",
	}
	generatedDir := filepath.Join(root, "generated")
	if err := os.MkdirAll(generatedDir, 0755); err != nil {
		return fmt.Errorf("creating generated descriptor stubs dir: %w", err)
	}
	for name, content := range stubs {
		path := filepath.Join(generatedDir, name)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("checking generated/%s descriptor stub: %w", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Errorf("writing generated/%s descriptor stub: %w", name, err)
		}
	}
	return nil
}

func copyProtoTree(srcRoot, dstRoot string) error {
	return filepath.Walk(srcRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".proto" {
			return err
		}
		relPath, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dstRoot, relPath)
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dstPath, content, 0644)
	})
}

func copyEmbeddedProtoFS(protoFS fs.FS, embedDir, targetDir, targetSubdir string) error {
	entries, err := fs.ReadDir(protoFS, embedDir)
	if err != nil {
		return err
	}
	outDir := filepath.Join(targetDir, targetSubdir)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".proto") {
			continue
		}
		targetPath := filepath.Join(outDir, entry.Name())
		if _, err := os.Stat(targetPath); err == nil {
			continue
		}
		content, err := fs.ReadFile(protoFS, filepath.Join(embedDir, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(targetPath, content, 0644); err != nil {
			return err
		}
	}
	return nil
}
