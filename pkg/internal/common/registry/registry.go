// Package registry generates registry.json, registry.schema.json, and AGENT.md
// from parsed proto files. This is shared across all language generators.
package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"

	"gitlab.com/secondstarcavalier/schematic/tacplus/docs"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/eventbus"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/llmdesc"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/verbs"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
)

// Config holds the parameters needed for registry generation.
type Config struct {
	OutputDir   string // directory to write registry.json, registry.schema.json, AGENT.md
	VersionTag  string // tacplus version (e.g. "1.1.25")
	InputDir    string // proto source directory (for git sha)
	Language    string // target language ("go", "rust", ...) for the per-language grants helper
	PackageBase string // language package/module base for the grants helper (Go module path, Java package, Rust crate, Python/TS package)
}

// treeNode is an internal node in the package hierarchy.
type treeNode struct {
	name       string
	fullPath   string
	isLeaf     bool
	children   map[string]*treeNode
	childOrder []string
}

// Generate produces registry.json, registry.schema.json, and AGENT.md in cfg.OutputDir.
func Generate(cfg Config, allParsedFiles []model.ProtoFile, dp *parser.DescriptorParser) error {
	if dp == nil {
		return fmt.Errorf("registry descriptor parser is required")
	}

	// Group proto files by subdir.
	type subdirEntry struct {
		pf  model.ProtoFile
		fds []*parser.FileDescriptor
	}
	subdirMap := map[string]*subdirEntry{}
	leafDirs := map[string]bool{}

	for i := range allParsedFiles {
		pf := allParsedFiles[i]
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		subdir := resolveSubdir(pf)
		leafDirs[subdir] = true

		fd, err := dp.GetFileDescriptor(pf.RelPath)
		if err != nil {
			return fmt.Errorf("registry descriptor lookup for %s: %w", pf.RelPath, err)
		}
		if existing, ok := subdirMap[subdir]; ok {
			existing.pf.Messages = append(existing.pf.Messages, pf.Messages...)
			existing.pf.Enums = append(existing.pf.Enums, pf.Enums...)
			existing.fds = append(existing.fds, fd)
		} else {
			entry := &subdirEntry{pf: pf, fds: []*parser.FileDescriptor{fd}}
			subdirMap[subdir] = entry
		}
	}

	// Build tree from leaf dirs.
	root := &treeNode{name: "types", fullPath: "", children: map[string]*treeNode{}}
	for subdir := range leafDirs {
		parts := strings.Split(filepath.ToSlash(subdir), "/")
		cur := root
		for i, part := range parts {
			if _, ok := cur.children[part]; !ok {
				partPath := strings.Join(parts[:i+1], "/")
				cur.children[part] = &treeNode{
					name:     part,
					fullPath: partPath,
					children: map[string]*treeNode{},
				}
				cur.childOrder = append(cur.childOrder, part)
			}
			cur = cur.children[part]
		}
		cur.isLeaf = true
	}

	// Sort for deterministic output.
	var sortChildren func(node *treeNode)
	sortChildren = func(node *treeNode) {
		sort.Strings(node.childOrder)
		for _, child := range node.children {
			sortChildren(child)
		}
	}
	sortChildren(root)

	// Collect ALL file descriptors for cross-package subscription resolution.
	var allFds []*parser.FileDescriptor
	for _, entry := range subdirMap {
		allFds = append(allFds, entry.fds...)
	}

	// Collect registry types keyed by package.
	allRegistryTypes := llmdesc.CollectRegistryTypes(allParsedFiles)
	typesByPkg := make(map[string][]llmdesc.RegistryTypeInfo)
	for _, t := range allRegistryTypes {
		typesByPkg[t.Package] = append(typesByPkg[t.Package], t)
	}

	// Build PackageNode tree.
	var buildNode func(node *treeNode) (*spec.PackageNode, error)
	buildNode = func(node *treeNode) (*spec.PackageNode, error) {
		pn := &spec.PackageNode{
			Name:     node.name,
			FullPath: node.fullPath,
		}
		if node.isLeaf {
			if entry, ok := subdirMap[node.fullPath]; ok {
				protoPackages := declaredProtoPackages(entry.fds)
				pn.ProtoPackages = protoPackages
				pn.Vservices = declaredVServices(entry.fds)
				pn.ClientDependencies = declaredClientDependencies(entry.fds)
				eventPublishes, err := CollectEventPublishProtos(entry.fds, allFds)
				if err != nil {
					return nil, fmt.Errorf("collecting event_publish metadata for %s: %w", node.fullPath, err)
				}
				pn.EventPublishes = eventPublishes
				eventWatches, err := CollectEventWatchProtos(entry.fds, allFds)
				if err != nil {
					return nil, fmt.Errorf("collecting event_watch metadata for %s: %w", node.fullPath, err)
				}
				pn.EventWatches = eventWatches
				for _, protoPkg := range protoPackages {
					for _, t := range typesByPkg[protoPkg] {
						pn.Types = append(pn.Types, BuildTypeInfoProto(t))
					}
				}
				rpcs, err := CollectRpcProtos(entry.fds)
				if err != nil {
					return nil, fmt.Errorf("collecting rpc metadata for %s: %w", node.fullPath, err)
				}
				pn.Rpcs = rpcs
				pn.Boundaries = CollectBoundaryProtos(entry.fds, pn.Rpcs)
				subs, subsErr := CollectSubscriptionProtos(entry.fds, allFds)
				if subsErr != nil {
					return nil, fmt.Errorf("collecting subscriptions for %s: %w", node.fullPath, subsErr)
				}
				pn.Subscriptions = subs

				// Collect file-level constants.
				for _, fd := range entry.fds {
					for _, c := range fd.Constants {
						if pn.Constants == nil {
							pn.Constants = make(map[string]string)
						}
						pn.Constants[c.Name] = strings.Trim(c.Value, `"`)
					}
				}

				for _, fd := range entry.fds {
					for _, enum := range fd.Enums {
						ei := &spec.EnumInfo{Name: enum.Name, Package: fd.Package, Description: enum.Comment}
						for _, v := range enum.Values {
							ei.Values = append(ei.Values, &spec.EnumValueInfo{
								Name:        v.Name,
								Number:      v.Number,
								Description: v.Comment,
							})
						}
						pn.Enums = append(pn.Enums, ei)
					}
				}

				// Build ServiceInfo from descriptor identities. A leaf may hold
				// multiple declared packages, so package+service is the key.
				if len(pn.Rpcs) > 0 {
					seenServices := make(map[string]bool)
					for _, fd := range entry.fds {
						for _, svc := range fd.Services {
							serviceRpcs := rpcInfosForService(pn.Rpcs, fd.Package, svc.Name)
							if len(serviceRpcs) == 0 {
								continue
							}
							serviceKey := fd.Package + "." + svc.Name
							if seenServices[serviceKey] {
								continue
							}
							seenServices[serviceKey] = true
							si := &spec.ServiceInfo{
								Name:        svc.Name,
								Description: svc.Comment,
								Package:     fd.Package,
								GrpcService: serviceKey,
								Rpcs:        serviceRpcs,
								HostLabel:   svc.HostLabel,
							}
							if svc.EventLabel != "" {
								si.EventLabel = svc.EventLabel
							}
							if svc.IsSMB {
								si.Smb = true
								smbRoot := svc.SmbRoot
								si.SmbRoot = &smbRoot
							}
							if svc.IsSMBAuthority {
								si.SmbAuthority = true
							}
							for _, bi := range pn.Boundaries {
								for _, rpcName := range bi.Rpcs {
									for _, svcRpc := range si.Rpcs {
										if svcRpc.Rpc == rpcName {
											si.Boundaries = append(si.Boundaries, bi.Boundary)
											goto nextBoundary
										}
									}
								}
							nextBoundary:
							}
							pn.Services = append(pn.Services, si)
						}
					}
				}
			}
		}
		for _, childName := range node.childOrder {
			child := node.children[childName]
			childNode, err := buildNode(child)
			if err != nil {
				return nil, err
			}
			pn.Children = append(pn.Children, childNode)
		}
		return pn, nil
	}

	rootNode, err := buildNode(root)
	if err != nil {
		return err
	}

	// Build metadata. registry.json is a deterministic function of the proto
	// graph, service identity, language, and package name — it must NOT embed
	// non-semantic build provenance (a wall-clock timestamp or the source git
	// SHA), which would make two builds of the same inputs differ. Only the
	// requested version tag (a controlled semantic input) is recorded.
	rootNode.Build = &spec.BuildInfo{
		TacplusVersion: cfg.VersionTag,
	}

	rootNode.Schema = "This document is a recursive PackageNode tree. Its structure is defined by the types under children[sys].children[tacplus]: PackageNode, TypeInfo, FieldInfo, RpcInfo, ServiceInfo, EntitlementGrantInfo, BoundaryInfo, EnumValueInfo. Navigate children by name to explore the tree."
	eventTopicGrantMethods, err := collectEventTopicEntitlementGrantMethods(allFds)
	if err != nil {
		return fmt.Errorf("collecting EventBus entitlement grants: %w", err)
	}
	rootNode.EntitlementGrants = collectEntitlementGrants(rootNode, eventTopicGrantMethods...)
	if err := validateEntitlementGrantTokenCollisions(rootNode.EntitlementGrants); err != nil {
		return fmt.Errorf("entitlement grant catalog: %w", err)
	}

	// Emit the per-language entitlement-grant helper alongside registry.json.
	if err := emitGrantsHelper(cfg, rootNode.EntitlementGrants); err != nil {
		return fmt.Errorf("failed to emit entitlement grants helper: %w", err)
	}

	// Render AGENT.md. A render (template execution) failure is a codegen bug,
	// not a user-input condition — AGENT.md is a primary generated artifact
	// (alongside registry.json/registry.schema.json), so fail fast rather than
	// silently omit it (a gray failure that ships an incomplete contract).
	agentMD, agentErr := renderAgentMD(rootNode)
	if agentErr != nil {
		return fmt.Errorf("failed to render AGENT.md template: %w", agentErr)
	}

	// Embed documentation files.
	rootNode.Docs = make(map[string]string)
	if agentMD != "" {
		rootNode.Docs["AGENT.md"] = agentMD
	}
	fs.WalkDir(docs.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".tmpl") {
			return nil
		}
		content, readErr := fs.ReadFile(docs.FS, path)
		if readErr != nil {
			return nil
		}
		rootNode.Docs[path] = string(content)
		return nil
	})

	// Marshal to JSON.
	marshaler := protojson.MarshalOptions{
		Multiline:     true,
		Indent:        "  ",
		UseProtoNames: true,
	}
	jsonBytes, err := marshaler.Marshal(rootNode)
	if err != nil {
		return fmt.Errorf("failed to marshal registry JSON: %w", err)
	}

	jsonStr := string(jsonBytes)
	if strings.HasPrefix(jsonStr, "{\n") {
		jsonStr = "{\n  \"$schema\": \"registry.schema.json\",\n" + jsonStr[2:]
	}

	outputPath := filepath.Join(cfg.OutputDir, "registry.json")
	if err := os.WriteFile(outputPath, []byte(jsonStr), 0644); err != nil {
		return fmt.Errorf("failed to write registry.json: %w", err)
	}
	zap.L().Info("Generated registry.json", zap.String("path", outputPath))

	if agentMD != "" {
		agentPath := filepath.Join(cfg.OutputDir, "AGENT.md")
		if err := os.WriteFile(agentPath, []byte(agentMD), 0644); err != nil {
			return fmt.Errorf("failed to write AGENT.md: %w", err)
		}
		zap.L().Info("Generated AGENT.md", zap.String("path", agentPath))
	}

	if err := generateRegistrySchema(cfg.OutputDir, rootNode); err != nil {
		return fmt.Errorf("failed to generate registry.schema.json: %w", err)
	}

	return nil
}

type registryGrantMethod struct {
	meta  verbs.MethodMeta
	token string
}

func collectEntitlementGrants(root *spec.PackageNode, additionalMethods ...registryGrantMethod) []*spec.EntitlementGrantInfo {
	methods := append([]registryGrantMethod{}, additionalMethods...)
	var walk func(node *spec.PackageNode)
	walk = func(node *spec.PackageNode) {
		for _, svc := range node.GetServices() {
			pkg := svc.GetPackage()
			for _, rpc := range svc.GetRpcs() {
				if rpc.GetAuthModel() != "c2s" || rpc.GetAuthDisabled() || rpc.GetAuthAnyUser() {
					continue
				}
				scopes := make([]verbs.Scope, 0, len(rpc.GetEntitlementScopes()))
				for _, scope := range rpc.GetEntitlementScopes() {
					if scope != "" {
						scopes = append(scopes, verbs.Scope(scope))
					}
				}
				m := registryGrantMethod{
					meta: verbs.MethodMeta{
						Package:    pkg,
						Service:    svc.GetName(),
						RPC:        rpc.GetRpc(),
						Scopes:     scopes,
						Capability: verbs.Scope(rpc.GetCapabilityScope()),
					},
				}
				m.token = verbs.CanonicalGrantToken(verbs.MethodGrant(m.meta))
				methods = append(methods, m)
			}
		}
		for _, child := range node.GetChildren() {
			walk(child)
		}
	}
	walk(root)

	sort.Slice(methods, func(i, j int) bool { return methods[i].token < methods[j].token })

	var out []*spec.EntitlementGrantInfo
	byPkg := make(map[string][]registryGrantMethod)
	for _, m := range methods {
		out = append(out, &spec.EntitlementGrantInfo{
			Token:       m.token,
			Package:     m.meta.Package,
			ServiceName: m.meta.Service,
			Rpc:         m.meta.RPC,
		})
		byPkg[m.meta.Package] = append(byPkg[m.meta.Package], m)
	}

	var pkgs []string
	for pkg := range byPkg {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		pkgMethods := byPkg[pkg]
		metas := make([]verbs.MethodMeta, 0, len(pkgMethods))
		for _, m := range pkgMethods {
			metas = append(metas, m.meta)
		}
		for _, grant := range verbs.BuildPackageGrants(metas) {
			if grant.IsExact() {
				continue
			}
			out = append(out, &spec.EntitlementGrantInfo{
				Token:           verbs.CanonicalGrantToken(grant),
				Package:         pkg,
				CapabilityScope: string(grant.Capability),
				ExpandsTo:       exactGrantTokensFromGrants(verbs.ExpandGrant(grant, metas)),
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].GetToken() < out[j].GetToken() })
	return out
}

func collectEventTopicEntitlementGrantMethods(fds []*parser.FileDescriptor) ([]registryGrantMethod, error) {
	registry, err := eventbus.BuildTopicRegistry(fds)
	if err != nil {
		return nil, err
	}

	var methods []registryGrantMethod
	for _, topic := range registry.Topics() {
		if !topic.Export {
			continue
		}
		packages := []string{topic.EventBusLabel}
		if topic.Ref.Package != topic.EventBusLabel {
			packages = append(packages, topic.Ref.Package)
		}
		// The grant must describe the SAME service+rpc the caller actually
		// invokes, because MethodGrant is documented as the only grant shape a
		// runtime authorizer matches. The wire moved to per-topic, so these move
		// with it; leaving them on the flat names would describe the
		// authorization identity of these calls in a naming scheme that no
		// longer exists on the wire. It happens to be inert today only because
		// the platform's topics declare auth_services ["*"], which never
		// consults an exact grant — the first topic to restrict callers would
		// have found the mismatch the hard way.
		perTopicService := eventbus.PerTopicServiceName(topic.Ref)
		for _, pkg := range packages {
			methods = append(methods,
				eventTopicGrantMethod(pkg, perTopicService, eventbus.PerTopicPublishRPC(topic.Ref), []verbs.Scope{verbs.ScopeAny, verbs.ScopeWrite}, verbs.ScopeWrite),
				eventTopicGrantMethod(pkg, perTopicService, eventbus.PerTopicSubscribeRPC(topic.Ref), []verbs.Scope{verbs.ScopeAny, verbs.ScopeWatch}, verbs.ScopeWatch),
			)
		}
	}
	sort.Slice(methods, func(i, j int) bool { return methods[i].token < methods[j].token })
	return methods, nil
}

func eventTopicGrantMethod(pkg, service, rpc string, scopes []verbs.Scope, capability verbs.Scope) registryGrantMethod {
	method := registryGrantMethod{
		meta: verbs.MethodMeta{
			Package:    pkg,
			Service:    service,
			RPC:        rpc,
			Scopes:     scopes,
			Capability: capability,
		},
	}
	method.token = verbs.CanonicalGrantToken(verbs.MethodGrant(method.meta))
	return method
}

func validateEntitlementGrantTokenCollisions(grants []*spec.EntitlementGrantInfo) error {
	seen := make(map[string]*spec.EntitlementGrantInfo, len(grants))
	for _, grant := range grants {
		token := grant.GetToken()
		if token == "" {
			return fmt.Errorf("empty entitlement grant token for %s", describeEntitlementGrant(grant))
		}
		if prev, ok := seen[token]; ok {
			return fmt.Errorf("entitlement grant token collision %q between %s and %s",
				token, describeEntitlementGrant(prev), describeEntitlementGrant(grant))
		}
		seen[token] = grant
	}
	return nil
}

func describeEntitlementGrant(grant *spec.EntitlementGrantInfo) string {
	if grant == nil {
		return "<nil grant>"
	}
	if grant.GetRpc() != "" {
		return fmt.Sprintf("exact %s.%s.%s", grant.GetPackage(), grant.GetServiceName(), grant.GetRpc())
	}
	if grant.GetCapabilityScope() != "" {
		return fmt.Sprintf("wildcard %s.%s", grant.GetPackage(), grant.GetCapabilityScope())
	}
	return fmt.Sprintf("grant package=%q service=%q rpc=%q capability=%q",
		grant.GetPackage(), grant.GetServiceName(), grant.GetRpc(), grant.GetCapabilityScope())
}

func exactGrantTokensFromGrants(grants []verbs.Grant) []string {
	seen := make(map[string]bool, len(grants))
	var out []string
	for _, grant := range grants {
		token := verbs.CanonicalGrantToken(grant)
		if !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
	}
	sort.Strings(out)
	return out
}

func declaredProtoPackages(fds []*parser.FileDescriptor) []string {
	seen := make(map[string]bool)
	var out []string
	for _, fd := range fds {
		pkg := strings.TrimSpace(fd.Package)
		if pkg == "" || seen[pkg] {
			continue
		}
		seen[pkg] = true
		out = append(out, pkg)
	}
	sort.Strings(out)
	return out
}

func declaredVServices(fds []*parser.FileDescriptor) []*spec.VServiceInfo {
	var out []*spec.VServiceInfo
	for _, fd := range fds {
		if fd.VServiceName == "" {
			continue
		}
		out = append(out, &spec.VServiceInfo{
			Name:    fd.VServiceName,
			Id:      fd.VServiceID,
			Package: fd.Package,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetPackage() != out[j].GetPackage() {
			return out[i].GetPackage() < out[j].GetPackage()
		}
		if out[i].GetName() != out[j].GetName() {
			return out[i].GetName() < out[j].GetName()
		}
		return out[i].GetId() < out[j].GetId()
	})
	return out
}

func declaredClientDependencies(fds []*parser.FileDescriptor) []*spec.ClientDependencyInfo {
	var out []*spec.ClientDependencyInfo
	for _, fd := range fds {
		if fd.AllGrpcS2SClients {
			out = append(out, &spec.ClientDependencyInfo{
				SourcePackage: fd.Package,
				SurfaceKind:   "s2s",
				Service:       "*",
				Wildcard:      true,
			})
		}
		out = append(out, clientDependencyInfos(fd.Package, "s2s", fd.GrpcS2SClients)...)
		out = append(out, clientDependencyInfos(fd.Package, "c2s", fd.GrpcC2SClients)...)
		out = append(out, clientDependencyInfos(fd.Package, "w2s", fd.GrpcW2SClients)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GetSourcePackage() != out[j].GetSourcePackage() {
			return out[i].GetSourcePackage() < out[j].GetSourcePackage()
		}
		if out[i].GetSurfaceKind() != out[j].GetSurfaceKind() {
			return out[i].GetSurfaceKind() < out[j].GetSurfaceKind()
		}
		if out[i].GetService() != out[j].GetService() {
			return out[i].GetService() < out[j].GetService()
		}
		return !out[i].GetWildcard() && out[j].GetWildcard()
	})
	return out
}

func clientDependencyInfos(sourcePackage, surfaceKind string, refs []string) []*spec.ClientDependencyInfo {
	out := make([]*spec.ClientDependencyInfo, 0, len(refs))
	for _, ref := range refs {
		targetPackage, targetService := splitServiceRef(ref)
		out = append(out, &spec.ClientDependencyInfo{
			SourcePackage: sourcePackage,
			SurfaceKind:   surfaceKind,
			Service:       ref,
			TargetPackage: targetPackage,
			TargetService: targetService,
		})
	}
	return out
}

func splitServiceRef(ref string) (targetPackage, targetService string) {
	pkg, svc, ok := strings.Cut(ref, ".")
	if !ok {
		return "", ref
	}
	return pkg, svc
}

func rpcInfosForService(rpcs []*spec.RpcInfo, protoPackage, serviceName string) []*spec.RpcInfo {
	prefix := "/" + protoPackage + "." + serviceName + "/"
	var out []*spec.RpcInfo
	for _, rpc := range rpcs {
		if strings.HasPrefix(rpc.GetGrpcPath(), prefix) {
			out = append(out, rpc)
		}
	}
	return out
}

// resolveSubdir determines the package subdirectory for a proto file.
func resolveSubdir(pf model.ProtoFile) string {
	parentDir := filepath.Dir(pf.RelPath)
	if serviceName, _ := proto.ServiceProtoParts(pf.RelPath); serviceName != "" {
		return filepath.Join(parentDir, serviceName)
	}
	if filepath.Base(parentDir) == pf.Package {
		return parentDir
	}
	return filepath.Join(parentDir, pf.Package)
}

// BuildTypeInfoProto converts an llmdesc.RegistryTypeInfo to a proto TypeInfo.
func BuildTypeInfoProto(t llmdesc.RegistryTypeInfo) *spec.TypeInfo {
	ti := &spec.TypeInfo{
		Fqn:               t.FQN,
		Name:              t.Descriptor.Name,
		Package:           t.Descriptor.Package,
		Description:       t.Descriptor.Description,
		SchemaJson:        t.SchemaJSON,
		RecordKey:         t.RecordKey,
		RecordOwner:       t.RecordOwner,
		RecordRetention:   retentionPolicyProto(t.RecordRetention),
		ServiceStore:      t.ServiceStore,
		ServiceStoreName:  t.ServiceStoreName,
		ServiceStoreLabel: t.ServiceStoreLabels,

		DataEntitlementDefault: entitlementModeToProto(t.Descriptor.DataEntitlementDefault),
	}
	// Transformations — the registry carries store-neutral semantic intent
	// (dedup/cardinality_hint). A downstream materializer renders the concrete
	// backend schema (engine/merge). Where the annotation omits dedup,
	// defaultTransformationDedup preserves the legacy default losslessly.
	// Retention is NOT here: it is delivery-side state, carried on record_retention.
	for _, tp := range t.Transformations {
		tpInfo := &spec.TransformationInfo{
			Name:  tp.Name,
			When:  tp.When,
			Dedup: defaultTransformationDedup(tp),
		}
		for _, f := range tp.Fields {
			tpInfo.Field = append(tpInfo.Field, &spec.TransformationFieldInfo{
				Source:          f.Source,
				As:              f.As,
				OrderKey:        f.OrderKey,
				PartitionKey:    f.PartitionKey,
				CardinalityHint: spec.CardinalityHint(f.CardinalityHint),
			})
		}
		ti.Transformations = append(ti.Transformations, tpInfo)
	}
	for _, field := range t.Descriptor.Fields {
		ti.Fields = append(ti.Fields, buildFieldInfoProto(field))
	}

	// Timeseries table metadata for messages with (spec.timeseries_type). The
	// registry carries only store-neutral identity (table name + source); a
	// downstream materializer renders the concrete backend schema.
	if t.TimeseriesType {
		tableName := t.TimeseriesTableName
		if tableName == "" {
			// Acronym-aware snake_case via strutil (CON-3): a type like HTTPMetric
			// yields http_metric, matching every other snake_case name tacplus emits.
			// (Before CON-3 the local naive converter produced h_t_t_p_metric.)
			tableName = strutil.ToSnakeCase(t.Descriptor.Name)
		}
		ti.TimeseriesTableName = tableName
		ti.TimeseriesTableSource = t.TimeseriesTableSource
		// Table dedup is store-neutral intent declared on (spec.timeseries_type).
		// Unset stays nil (append-only). Retention is never here — it is
		// delivery-side state on record_retention.
		ti.TimeseriesDedup = dedupPolicyProto(t.TimeseriesDedup)
	}

	return ti
}

// retentionPolicyProto converts a model retention entry into its registry proto
// form, returning nil when retention is unset (indefinite).
func retentionPolicyProto(r *model.RetentionPolicyEntry) *spec.RetentionPolicyInfo {
	if r == nil {
		return nil
	}
	return &spec.RetentionPolicyInfo{
		TimestampField:  r.TimestampField,
		DurationSeconds: r.DurationSeconds,
	}
}

// defaultTransformationDedup resolves the store-neutral dedup policy for a
// transformation, preserving the legacy default losslessly. A derived row type is
// materialized from a source delivered at least once, so when the annotation
// omits dedup we default to keeping the latest row per key by timestamp_ms — the
// store-neutral "replace by latest timestamp" dedup default.
// The key fields are the transformation's sort-key (order_key) fields, in order.
func defaultTransformationDedup(tp model.TransformationEntry) *spec.DedupPolicyInfo {
	if tp.Dedup != nil {
		return dedupPolicyProto(tp.Dedup)
	}
	return &spec.DedupPolicyInfo{
		Strategy:     spec.DedupStrategy_DEDUP_LATEST_BY_VERSION,
		KeyFields:    transformationOrderKeyFields(tp),
		VersionField: "timestamp_ms",
	}
}

// dedupPolicyProto converts a model dedup entry into its registry proto form,
// returning nil when dedup is unset (append-only). Unlike a transformation, a
// standalone (spec.timeseries_type) table gets NO implicit latest-by-version
// default — an unset dedup stays append-only.
func dedupPolicyProto(d *model.DedupPolicyEntry) *spec.DedupPolicyInfo {
	if d == nil {
		return nil
	}
	return &spec.DedupPolicyInfo{
		Strategy:     spec.DedupStrategy(d.Strategy),
		KeyFields:    append([]string(nil), d.KeyFields...),
		VersionField: d.VersionField,
	}
}

// transformationOrderKeyFields returns the transformation's sort-key column names
// ordered by their order_key position — the logical row identity used as the
// default dedup key. The column name is the field's `as` override or the last
// segment of its source path.
func transformationOrderKeyFields(tp model.TransformationEntry) []string {
	type oc struct {
		name string
		pos  int32
	}
	var cols []oc
	for _, f := range tp.Fields {
		if f.OrderKey <= 0 {
			continue
		}
		name := f.As
		if name == "" {
			if idx := strings.LastIndex(f.Source, "."); idx >= 0 {
				name = f.Source[idx+1:]
			} else {
				name = f.Source
			}
		}
		cols = append(cols, oc{name: name, pos: f.OrderKey})
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].pos < cols[j].pos })
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
	}
	return names
}

func buildFieldInfoProto(field llmdesc.FieldDescriptor) *spec.FieldInfo {
	fi := &spec.FieldInfo{
		Name:                      field.Name,
		Type:                      fieldTypeKindProto(field.Type),
		Description:               field.Description,
		Required:                  field.Required,
		Repeated:                  field.Repeated,
		Oneof:                     field.OneOf,
		OneofName:                 field.OneOfName,
		StoreKey:                  field.StoreKey,
		QuorumWrite:               field.QuorumWrite,
		MapKeyType:                field.MapKey,
		MapValueType:              field.MapValue,
		ProjectionSource:          field.ProjectionSource,
		TimeseriesOrderKey:        int32(field.TimeseriesOrderKey),
		TimeseriesPartitionKey:    field.TimeseriesPartitionKey,
		TimeseriesCardinalityHint: spec.CardinalityHint(field.TimeseriesCardinality),
		DataEntitlementMode:       entitlementModeToProto(field.DataEntitlementMode),
		FilterMode:                filterModeToProto(field.FilterMode),
	}
	if defaultJSON := marshalDefault(field.Default); defaultJSON != "" {
		fi.DefaultJson = defaultJSON
	}
	if len(field.Constraints) > 0 {
		fi.Constraints = field.Constraints
	}
	for _, constraint := range field.ConstraintInfos {
		fi.ConstraintInfos = append(fi.ConstraintInfos, buildFieldConstraintInfoProto(constraint))
	}
	if field.Message != nil {
		fi.Message = BuildTypeInfoProto(llmdesc.RegistryTypeInfo{
			Descriptor: *field.Message,
		})
	}
	for _, ev := range field.EnumValues {
		fi.EnumValues = append(fi.EnumValues, &spec.EnumValueInfo{
			Name:        ev.Name,
			Number:      int32(ev.Number),
			Description: ev.Description,
		})
	}
	return fi
}

// entitlementModeToProto maps a data-entitlement mode string (as stamped by the
// descriptor parser's redactModeString) to its spec.RedactMode. An empty or
// unrecognized string maps to REDACT_MODE_UNSPECIFIED (not annotated).
func entitlementModeToProto(mode string) spec.RedactMode {
	switch mode {
	case "drop":
		return spec.RedactMode_REDACT_MODE_DROP
	case "pseudonymize":
		return spec.RedactMode_REDACT_MODE_PSEUDONYMIZE
	case "may_redact":
		return spec.RedactMode_REDACT_MODE_MAY_REDACT
	case "may_anonymize":
		return spec.RedactMode_REDACT_MODE_MAY_ANONYMIZE
	default:
		return spec.RedactMode_REDACT_MODE_UNSPECIFIED
	}
}

// filterModeToProto maps a (spec.query_field) op string (as stamped by the
// descriptor parser: "eq"/"range") to its spec.QueryOp. An empty or unrecognized
// string maps to QUERY_OP_UNSPECIFIED — the field carries no (spec.query_field)
// and is not filterable, so it has no criterion on the generated <T>Filter.
func filterModeToProto(mode string) spec.QueryOp {
	switch mode {
	case "eq":
		return spec.QueryOp_QUERY_OP_EQ
	case "range":
		return spec.QueryOp_QUERY_OP_RANGE
	default:
		return spec.QueryOp_QUERY_OP_UNSPECIFIED
	}
}

func buildFieldConstraintInfoProto(constraint llmdesc.ConstraintDescriptor) *spec.FieldConstraintInfo {
	info := &spec.FieldConstraintInfo{
		Rule:        constraint.Rule,
		Target:      constraint.Target,
		ValueKind:   constraint.ValueKind,
		StringValue: constraint.StringValue,
		EnumValue:   constraint.EnumValue,
		IntValues:   append([]int64(nil), constraint.IntValues...),
		FloatValues: append([]float64(nil), constraint.FloatValues...),
		Operator:    constraint.Operator,
		Defaulted:   constraint.Defaulted,
		Unit:        constraint.Unit,
		Source:      constraint.Source,
	}
	if constraint.BoolValue != nil {
		value := *constraint.BoolValue
		info.BoolValue = &value
	}
	if constraint.IntValue != nil {
		value := *constraint.IntValue
		info.IntValue = &value
	}
	if constraint.FloatValue != nil {
		value := *constraint.FloatValue
		info.FloatValue = &value
	}
	return info
}

func fieldTypeKindProto(fieldType string) spec.TypeKind {
	switch {
	case strings.HasPrefix(fieldType, "message"):
		return spec.TypeKind_TYPE_KIND_MESSAGE
	case fieldType == "map":
		return spec.TypeKind_TYPE_KIND_MAP
	case fieldType == "enum":
		return spec.TypeKind_TYPE_KIND_ENUM
	case fieldType == "string":
		return spec.TypeKind_TYPE_KIND_STRING
	case fieldType == "bool":
		return spec.TypeKind_TYPE_KIND_BOOL
	case fieldType == "bytes":
		return spec.TypeKind_TYPE_KIND_BYTES
	case fieldType == "int32":
		return spec.TypeKind_TYPE_KIND_INT32
	case fieldType == "int64":
		return spec.TypeKind_TYPE_KIND_INT64
	case fieldType == "uint32":
		return spec.TypeKind_TYPE_KIND_UINT32
	case fieldType == "uint64":
		return spec.TypeKind_TYPE_KIND_UINT64
	case fieldType == "sint32":
		return spec.TypeKind_TYPE_KIND_SINT32
	case fieldType == "sint64":
		return spec.TypeKind_TYPE_KIND_SINT64
	case fieldType == "fixed32":
		return spec.TypeKind_TYPE_KIND_FIXED32
	case fieldType == "fixed64":
		return spec.TypeKind_TYPE_KIND_FIXED64
	case fieldType == "sfixed32":
		return spec.TypeKind_TYPE_KIND_SFIXED32
	case fieldType == "sfixed64":
		return spec.TypeKind_TYPE_KIND_SFIXED64
	case fieldType == "float":
		return spec.TypeKind_TYPE_KIND_FLOAT
	case fieldType == "double":
		return spec.TypeKind_TYPE_KIND_DOUBLE
	default:
		return spec.TypeKind_TYPE_KIND_UNSPECIFIED
	}
}

// RpcCategoryFromVerb classifies an RPC method name by its verb prefix.
func RpcCategoryFromVerb(methodName string) spec.RpcCategory {
	accessorPrefixes := []string{
		"Get", "GetAll", "Watch", "WatchQuery", "Query",
		"KeySet", "Size", "ContainsKey", "Contains",
		"List", "Peek", "Subscribe", "Connections",
	}
	mutatorPrefixes := []string{
		"Set", "Put", "PutIfAbsent", "Delete", "Remove",
		"Clear", "Publish", "Offer", "Reload",
	}
	executorPrefixes := []string{
		"Assist", "Authenticate", "Authorize", "Callback",
		"Consume", "Elect", "Evaluate", "Exchange",
		"Logout", "Refresh", "Renew", "Resign", "Simulate",
	}

	for _, p := range accessorPrefixes {
		if strings.HasPrefix(methodName, p) {
			return spec.RpcCategory_RPC_CATEGORY_ACCESSOR
		}
	}
	for _, p := range mutatorPrefixes {
		if strings.HasPrefix(methodName, p) {
			return spec.RpcCategory_RPC_CATEGORY_MUTATOR
		}
	}
	for _, p := range executorPrefixes {
		if strings.HasPrefix(methodName, p) {
			return spec.RpcCategory_RPC_CATEGORY_EXECUTOR
		}
	}
	return spec.RpcCategory_RPC_CATEGORY_UNSPECIFIED
}

// CollectRpcProtos collects RpcInfo proto objects from parsed file descriptors.
func CollectRpcProtos(fds []*parser.FileDescriptor) ([]*spec.RpcInfo, error) {
	var rpcs []*spec.RpcInfo
	for _, fd := range fds {
		for _, svc := range fd.Services {
			for _, m := range svc.Methods {
				// Determine auth model from service annotation.
				authModel := ""
				if svc.IsS2S {
					authModel = "s2s"
				} else if svc.IsC2S {
					authModel = "c2s"
				} else if svc.IsC2D {
					authModel = "c2d"
				}
				// Resolve auth_services: method-level override takes precedence.
				authServices := svc.AuthServices
				if len(m.AuthServices) > 0 {
					authServices = m.AuthServices
				}
				// Method-level auth overrides take precedence over service-level.
				authDisabled := m.AuthDisabled || svc.AuthDisabled
				authAnyUser := m.AuthAnyUser || svc.AuthAnyUser
				subjectNamespaceSource, targetNamespaceSource, targetNamespace := namespaceAuthMetadata(authModel, authDisabled, authAnyUser, m.TargetNamespaceField)
				grpcPath := "/" + fd.Package + "." + svc.Name + "/" + m.Name
				rpc := &spec.RpcInfo{
					ServiceName:            svc.Name,
					Rpc:                    m.Name,
					Description:            m.Comment,
					InputType:              m.InputType,
					OutputType:             m.OutputType,
					ClientStreaming:        m.ClientStreaming,
					ServerStreaming:        m.ServerStreaming,
					AuthDisabled:           authDisabled,
					AuthServices:           authServices,
					AuthEntitledUsers:      svc.AuthEntitledUsers,
					AuthAnyUser:            authAnyUser,
					AuthModel:              authModel,
					TargetNamespaceField:   m.TargetNamespaceField,
					SubjectNamespaceSource: subjectNamespaceSource,
					TargetNamespaceSource:  targetNamespaceSource,
					TargetNamespace:        targetNamespace,
					Category:               RpcCategoryFromVerb(m.Name),
					GrpcPath:               grpcPath,
					HttpPath:               m.HttpPath,
					HttpMethods:            m.HttpMethods,
					HttpAliasPath:          m.HttpAliasPath,
					Surfaces:               rpcSurfaceInfos(svc, m, grpcPath, authServices, authDisabled, authAnyUser),
				}
				// method_collection (collection-returning read) — surface the
				// declared shape so service-local generators see it.
				if m.Collection != nil {
					rpc.CollectionMode = m.Collection.Mode
					rpc.CollectionMaxItems = m.Collection.MaxItems
					rpc.CollectionOrderBy = m.Collection.OrderBy
					rpc.CollectionReplayDepth = m.Collection.ReplayDepth
					rpc.CollectionDefaultPageSize = m.Collection.DefaultPageSize
				}
				class, err := classifyRpcDescriptorMethod(m)
				if err != nil {
					rpc.FoldedToVerb = foldedToVerb(m.Name)
					if authModel == "c2s" {
						return nil, fmt.Errorf("%s.%s/%s: %w", fd.Package, svc.Name, m.Name, err)
					}
					rpc.VerbStatus = verbStatusForClassificationError(authModel)
					rpcs = append(rpcs, rpc)
					continue
				}
				rpc.VerbStatus = "classified"
				rpc.RpcVerb = class.Verb
				rpc.RpcNoun = class.Noun
				rpc.MethodPattern = string(class.Pattern)
				rpc.AccessKind = string(class.Access)
				rpc.VerbClass = string(class.Class)
				rpc.RetryPolicy = string(class.RetryPolicy(m.ClientStreaming, m.ServerStreaming))
				rpc.CapabilityScope = string(class.CapabilityScope())
				rpc.HandlerShape = handlerShape(m)
				for _, scope := range class.ScopesAdmitting() {
					rpc.EntitlementScopes = append(rpc.EntitlementScopes, string(scope))
				}
				if def, ok := verbs.Lookup(class.Verb); ok {
					rpc.VerbDefinition = def.Definition
					rpc.VerbBoundary = def.Boundary
					rpc.AuditMeaning = def.Audit
					rpc.RejectedVerbs = verbs.RejectedSynonyms(class.Verb)
					rpc.AllowedPatterns = patternStrings(def.AllowedPatterns())
				}
				rpcs = append(rpcs, rpc)
			}
		}
	}
	return rpcs, nil
}

func namespaceAuthMetadata(authModel string, authDisabled, authAnyUser bool, targetNamespaceField string) (subjectSource, targetSource, fixedTarget string) {
	if authModel == "" || authModel == "c2d" || authDisabled {
		return "", "", ""
	}
	subjectSource = "token.namespace"
	target := strings.TrimSpace(targetNamespaceField)
	if target == "" {
		if authAnyUser {
			return subjectSource, "literal", "/"
		}
		return subjectSource, "", ""
	}
	if authAnyUser {
		return subjectSource, "literal", target
	}
	return subjectSource, "request.field", ""
}

func rpcSurfaceInfos(svc parser.ServiceDescriptor, m parser.MethodDescriptor, grpcPath string, authServices []string, authDisabled, authAnyUser bool) []*spec.RpcSurfaceInfo {
	var surfaces []*spec.RpcSurfaceInfo
	add := func(surfaceKind, transport, authModel string, includeHTTP bool) {
		subjectNamespaceSource, targetNamespaceSource, targetNamespace := namespaceAuthMetadata(authModel, authDisabled, authAnyUser, m.TargetNamespaceField)
		surface := &spec.RpcSurfaceInfo{
			SurfaceKind:            surfaceKind,
			Transport:              transport,
			AuthModel:              authModel,
			AuthDisabled:           authDisabled,
			AuthServices:           append([]string(nil), authServices...),
			AuthEntitledUsers:      svc.AuthEntitledUsers,
			AuthAnyUser:            authAnyUser,
			TargetNamespaceField:   m.TargetNamespaceField,
			SubjectNamespaceSource: subjectNamespaceSource,
			TargetNamespaceSource:  targetNamespaceSource,
			TargetNamespace:        targetNamespace,
			GrpcPath:               grpcPath,
		}
		if includeHTTP {
			surface.HttpPath = m.HttpPath
			surface.HttpMethods = append([]string(nil), m.HttpMethods...)
			surface.HttpAliasPath = m.HttpAliasPath
		}
		surfaces = append(surfaces, surface)
	}
	if svc.IsS2S {
		add("s2s", "grpc", "s2s", false)
	}
	if svc.IsC2S {
		add("c2s", "grpc", "c2s", true)
		if m.HttpPath != "" {
			add("w2s", "grpc_over_rest", "w2s", true)
		}
	}
	if svc.IsC2D {
		add("c2d", "unix", "c2d", false)
	}
	return surfaces
}

func classifyRpcDescriptorMethod(m parser.MethodDescriptor) (verbs.Classification, error) {
	mode := ""
	if m.Collection != nil {
		mode = m.Collection.Mode
	}
	return verbs.Classify(verbs.MethodShape{
		Name:            m.Name,
		ClientStreaming: m.ClientStreaming,
		ServerStreaming: m.ServerStreaming,
		CollectionMode:  mode,
	})
}

func verbStatusForClassificationError(authModel string) string {
	if authModel == "" {
		return "exempt"
	}
	return "unclassified"
}

func foldedToVerb(methodName string) string {
	verb, ok := verbs.LeadingVerb(methodName)
	if !ok {
		return ""
	}
	canonical, ok := verbs.FoldOf(verb)
	if !ok {
		return ""
	}
	return canonical
}

func handlerShape(m parser.MethodDescriptor) string {
	switch {
	case m.ClientStreaming && m.ServerStreaming:
		return "BIDI"
	case m.ClientStreaming:
		return "CLIENT_STREAM"
	case m.ServerStreaming:
		return "SERVER_STREAM"
	default:
		return "UNARY"
	}
}

func patternStrings(patterns []verbs.Pattern) []string {
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		out = append(out, string(pattern))
	}
	return out
}

// CollectBoundaryProtos collects BoundaryInfo proto objects from parsed file descriptors.
func CollectBoundaryProtos(fds []*parser.FileDescriptor, rpcs []*spec.RpcInfo) []*spec.BoundaryInfo {
	allBoundaries := boundaries.CollectAllBoundaries(fds)
	var result []*spec.BoundaryInfo
	for _, bd := range allBoundaries {
		bi := &spec.BoundaryInfo{
			StateRoot:      bd.StateRoot,
			Boundary:       bd.FieldName,
			TypeFqn:        bd.TypeFQN,
			Collection:     bd.Collection,
			IsConfig:       bd.IsConfig,
			Label:          bd.Labels,
			GraphType:      bd.GraphType,
			AutoTlsKeyType: bd.AutoTlsKeyType,
		}
		bi.Persistence = bd.Persistence
		bi.CollectedHealth = bd.CollectedHealth
		bi.BoundaryCallers = bd.BoundaryCallers
		bi.Rpcs = bd.Rpcs
		result = append(result, bi)
	}
	return result
}

// CollectSubscriptionProtos collects SubscriptionInfo proto objects from parsed file descriptors.
// It resolves source_type_fqn by looking up the referenced topic field in all known file descriptors.
func CollectSubscriptionProtos(fds []*parser.FileDescriptor, allFds []*parser.FileDescriptor) ([]*spec.SubscriptionInfo, error) {
	allSubs, err := boundaries.CollectAllSubscriptions(fds)
	if err != nil {
		return nil, err
	}
	if len(allSubs) == 0 {
		return nil, nil
	}

	// Build a lookup: "package.StateRoot" -> message descriptor
	stateRootMsgs := make(map[string]*parser.MessageDescriptor)
	for _, fd := range allFds {
		for i := range fd.Messages {
			msg := &fd.Messages[i]
			if ann, ok := msg.Annotations["service_state"]; ok {
				key := fd.Package + "." + ann.StringValue
				stateRootMsgs[key] = msg
			}
		}
	}

	var result []*spec.SubscriptionInfo
	for _, sd := range allSubs {
		si := &spec.SubscriptionInfo{
			Source:          sd.Source,
			SourcePackage:   sd.SourcePackage,
			SourceStateRoot: sd.SourceStateRoot,
			SourceField:     sd.SourceField,
		}
		// Resolve source_type_fqn by finding the field on the source state root
		key := sd.SourcePackage + "." + sd.SourceStateRoot
		if msg, ok := stateRootMsgs[key]; ok {
			for _, field := range msg.Fields {
				if field.Name == sd.SourceField {
					si.SourceTypeFqn = field.Type
					break
				}
			}
		}
		result = append(result, si)
	}
	return result, nil
}

// CollectEventPublishProtos collects validated event_publish bindings from parsed file descriptors.
func CollectEventPublishProtos(fds []*parser.FileDescriptor, allFds []*parser.FileDescriptor) ([]*spec.EventPublishInfo, error) {
	if !hasEventBindingAnnotation(fds, "event_publish") {
		return nil, nil
	}
	registry, err := eventbus.BuildTopicRegistry(allFds)
	if err != nil {
		return nil, err
	}
	var result []*spec.EventPublishInfo
	for _, fd := range fds {
		for i := range fd.Messages {
			msg := &fd.Messages[i]
			ann, ok := msg.Annotations["event_publish"]
			if !ok {
				continue
			}
			for _, entry := range ann.EventPublishEntries {
				binding, err := registry.ValidateBinding(eventbus.BindingPublish, fd.Package, msg.Name, entry)
				if err != nil {
					return nil, fmt.Errorf("%s.%s: %w", fd.Package, msg.Name, err)
				}
				authModel, authServices, authDisabled, authAnyUser, targetNamespaceField := eventBindingAuth(binding)
				result = append(result, &spec.EventPublishInfo{
					ActorPackage:         binding.ActorPackage,
					ActorMessage:         binding.ActorMessage,
					TopicRef:             binding.Topic.Ref.Raw,
					EventTypeFqn:         binding.Topic.EventTypeFQN,
					BrokerLabel:          binding.Topic.EventBusLabel,
					RpcStem:              binding.Topic.RPCStem,
					AuthModel:            authModel,
					AuthServices:         authServices,
					AuthDisabled:         authDisabled,
					AuthAnyUser:          authAnyUser,
					TargetNamespaceField: targetNamespaceField,
				})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return eventPublishSortKey(result[i]) < eventPublishSortKey(result[j]) })
	return result, nil
}

// CollectEventWatchProtos collects validated event_watch bindings from parsed file descriptors.
func CollectEventWatchProtos(fds []*parser.FileDescriptor, allFds []*parser.FileDescriptor) ([]*spec.EventWatchInfo, error) {
	if !hasEventBindingAnnotation(fds, "event_watch") {
		return nil, nil
	}
	registry, err := eventbus.BuildTopicRegistry(allFds)
	if err != nil {
		return nil, err
	}
	var result []*spec.EventWatchInfo
	for _, fd := range fds {
		for i := range fd.Messages {
			msg := &fd.Messages[i]
			ann, ok := msg.Annotations["event_watch"]
			if !ok {
				continue
			}
			for _, entry := range ann.EventWatchEntries {
				binding, err := registry.ValidateBinding(eventbus.BindingWatch, fd.Package, msg.Name, entry)
				if err != nil {
					return nil, fmt.Errorf("%s.%s: %w", fd.Package, msg.Name, err)
				}
				authModel, authServices, authDisabled, authAnyUser, targetNamespaceField := eventBindingAuth(binding)
				result = append(result, &spec.EventWatchInfo{
					ActorPackage:         binding.ActorPackage,
					ActorMessage:         binding.ActorMessage,
					TopicRef:             binding.Topic.Ref.Raw,
					EventTypeFqn:         binding.Topic.EventTypeFQN,
					BrokerLabel:          binding.Topic.EventBusLabel,
					RpcStem:              binding.Topic.RPCStem,
					AuthModel:            authModel,
					AuthServices:         authServices,
					AuthDisabled:         authDisabled,
					AuthAnyUser:          authAnyUser,
					TargetNamespaceField: targetNamespaceField,
				})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return eventWatchSortKey(result[i]) < eventWatchSortKey(result[j]) })
	return result, nil
}

func hasEventBindingAnnotation(fds []*parser.FileDescriptor, annotation string) bool {
	for _, fd := range fds {
		for i := range fd.Messages {
			if _, ok := fd.Messages[i].Annotations[annotation]; ok {
				return true
			}
		}
	}
	return false
}

func eventBindingAuth(binding eventbus.Binding) (authModel string, authServices []string, authDisabled, authAnyUser bool, targetNamespaceField string) {
	if binding.MethodS2S != nil {
		return "s2s", append([]string(nil), binding.MethodS2S.AuthServices...), binding.MethodS2S.AuthDisabled, false, ""
	}
	if binding.MethodC2S != nil {
		return "c2s", nil, binding.MethodC2S.AuthDisabled, binding.MethodC2S.AuthAnyUser, binding.MethodC2S.TargetNamespaceField
	}
	return "", nil, false, false, ""
}

func eventPublishSortKey(info *spec.EventPublishInfo) string {
	return info.GetActorPackage() + "\x00" + info.GetActorMessage() + "\x00" + info.GetTopicRef()
}

func eventWatchSortKey(info *spec.EventWatchInfo) string {
	return info.GetActorPackage() + "\x00" + info.GetActorMessage() + "\x00" + info.GetTopicRef()
}

func marshalDefault(value any) string {
	if value == nil {
		return ""
	}
	jsonBytes, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(jsonBytes)
}

type registryProtoMessageDoc struct {
	Name   string
	Fields []registryProtoFieldDoc
}

type registryProtoFieldDoc struct {
	Name string
	Type string
}

func registryProtoDocs() []registryProtoMessageDoc {
	messages := spec.File_sys_tacplus_proto.Messages()
	docs := make([]registryProtoMessageDoc, 0, messages.Len())
	for i := 0; i < messages.Len(); i++ {
		message := messages.Get(i)
		fields := message.Fields()
		msgDoc := registryProtoMessageDoc{
			Name:   string(message.Name()),
			Fields: make([]registryProtoFieldDoc, 0, fields.Len()),
		}
		for j := 0; j < fields.Len(); j++ {
			field := fields.Get(j)
			msgDoc.Fields = append(msgDoc.Fields, registryProtoFieldDoc{
				Name: string(field.Name()),
				Type: registryProtoFieldType(field),
			})
		}
		docs = append(docs, msgDoc)
	}
	return docs
}

func registryProtoFieldType(field protoreflect.FieldDescriptor) string {
	if field.IsMap() {
		return fmt.Sprintf("map<%s, %s>", field.MapKey().Kind(), registryProtoScalarType(field.MapValue()))
	}
	if field.IsList() {
		return "repeated " + registryProtoScalarType(field)
	}
	return registryProtoScalarType(field)
}

func registryProtoScalarType(field protoreflect.FieldDescriptor) string {
	switch field.Kind() {
	case protoreflect.EnumKind:
		return string(field.Enum().FullName())
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return string(field.Message().FullName())
	default:
		return field.Kind().String()
	}
}

func renderAgentMD(root *spec.PackageNode) (string, error) {
	tmplBytes, err := fs.ReadFile(docs.FS, "AGENT.md.tmpl")
	if err != nil {
		return "", fmt.Errorf("failed to read AGENT.md.tmpl: %w", err)
	}

	tmpl, err := template.New("AGENT.md").Parse(string(tmplBytes))
	if err != nil {
		return "", fmt.Errorf("failed to parse AGENT.md template: %w", err)
	}

	var countTypes, countRpcs, countBoundaries, countPackages int
	svcSet := map[string]bool{}
	var pkgPaths []string

	var walk func(node *spec.PackageNode)
	walk = func(node *spec.PackageNode) {
		countTypes += len(node.Types)
		countRpcs += len(node.Rpcs)
		countBoundaries += len(node.Boundaries)
		for _, r := range node.Rpcs {
			if r.ServiceName != "" {
				svcSet[r.ServiceName] = true
			}
		}
		if len(node.Children) == 0 && node.FullPath != "" {
			countPackages++
			pkgPaths = append(pkgPaths, node.FullPath)
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)

	services := make([]string, 0, len(svcSet))
	for s := range svcSet {
		services = append(services, s)
	}
	sort.Strings(services)
	sort.Strings(pkgPaths)

	data := struct {
		TypeCount     int
		RpcCount      int
		BoundaryCount int
		ServiceCount  int
		PackageCount  int
		Services      []string
		Packages      []string
		RegistryTypes []registryProtoMessageDoc
	}{
		TypeCount:     countTypes,
		RpcCount:      countRpcs,
		BoundaryCount: countBoundaries,
		ServiceCount:  len(services),
		PackageCount:  countPackages,
		Services:      services,
		Packages:      pkgPaths,
		RegistryTypes: registryProtoDocs(),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute AGENT.md template: %w", err)
	}
	return buf.String(), nil
}

func generatedRegistrySchema() map[string]any {
	return map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         "registry.schema.json",
		"title":       "tacplus Registry",
		"description": "Schema for the tacplus registry.json, generated from sys/tacplus.proto descriptors.",
		"$ref":        "#/$defs/PackageNode",
		"$defs":       registryProtoSchemaDefs(),
	}
}

func registryProtoSchemaDefs() map[string]any {
	messages := spec.File_sys_tacplus_proto.Messages()
	defs := make(map[string]any, messages.Len())
	for i := 0; i < messages.Len(); i++ {
		message := messages.Get(i)
		defs[string(message.Name())] = registryProtoMessageSchema(message)
	}
	return defs
}

func registryProtoMessageSchema(message protoreflect.MessageDescriptor) map[string]any {
	fields := message.Fields()
	properties := make(map[string]any, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		properties[string(field.Name())] = registryProtoFieldSchema(field)
	}
	return map[string]any{
		"type":        "object",
		"description": string(message.FullName()),
		"properties":  properties,
	}
}

func registryProtoFieldSchema(field protoreflect.FieldDescriptor) map[string]any {
	if field.IsMap() {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": registryProtoSingularFieldSchema(field.MapValue()),
		}
	}
	if field.IsList() {
		return map[string]any{
			"type":  "array",
			"items": registryProtoSingularFieldSchema(field),
		}
	}
	return registryProtoSingularFieldSchema(field)
}

func registryProtoSingularFieldSchema(field protoreflect.FieldDescriptor) map[string]any {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.StringKind, protoreflect.BytesKind:
		return map[string]any{"type": "string"}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return map[string]any{"type": "integer"}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return map[string]any{
			"oneOf": []any{
				map[string]any{"type": "integer"},
				map[string]any{"type": "string", "pattern": "^-?\\d+$"},
			},
		}
	case protoreflect.EnumKind:
		values := field.Enum().Values()
		enumValues := make([]string, 0, values.Len())
		for i := 0; i < values.Len(); i++ {
			enumValues = append(enumValues, string(values.Get(i).Name()))
		}
		return map[string]any{"type": "string", "enum": enumValues}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		if field.Message().ParentFile().Path() == spec.File_sys_tacplus_proto.Path() {
			return map[string]any{"$ref": "#/$defs/" + string(field.Message().Name())}
		}
		return map[string]any{"type": "object"}
	default:
		return map[string]any{"type": "string"}
	}
}

func writeRegistrySchema(outputDir string, schema map[string]any) error {
	jsonBytes, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal schema: %w", err)
	}
	schemaPath := filepath.Join(outputDir, "registry.schema.json")
	if err := os.WriteFile(schemaPath, jsonBytes, 0644); err != nil {
		return fmt.Errorf("failed to write schema: %w", err)
	}
	zap.L().Info("Generated registry.schema.json", zap.String("path", schemaPath))
	return nil
}

func generateRegistrySchema(outputDir string, _root *spec.PackageNode) error {
	return writeRegistrySchema(outputDir, generatedRegistrySchema())
}
