// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	commonspiffe "github.com/tacplus-dev/tacplus/pkg/internal/common/spiffe"
)

// The product layer (roadmap/PRODUCTS_LAYER.md, #475; decision
// products-layer-2026-11-08): a fleet's product code runs at
// /products/<name>, between /system and the tenants, and its S2S edges are
// fixed at build time. This file is the one place the compiler decides a
// service's layer and whether an edge between two layers is allowed, so the
// build, the fleet build and the client selection cannot disagree.

// ProductLayerRule names the decision every refusal here cites.
const ProductLayerRule = "products-layer-2026-11-08"

// productNameRe is a product name's shape, the generated sys/spiffe's: the
// operator's namespace for it is tac-<deployment>-<name>.
var productNameRe = regexp.MustCompile(commonspiffe.ProductNamePattern)

// tidShapeRe is a tid's shape. A tenant's namespace is tac-<deployment>-<tid>,
// so a product named like a tid would collide with a tenant's.
var tidShapeRe = regexp.MustCompile(commonspiffe.TidShapePattern)

// tenantNamingFields are the request field names that name a tenant. On a
// product service each must be bound to the caller's token (caller_tid): a
// product reaches a tenant's data only as that tenant's custodian.
var tenantNamingFields = map[string]bool{"tenant": true, "tenant_id": true, "tid": true}

// literalServiceRe is an auth_services entry that is a plain service name, not
// a pattern: only those are resolved to a layer here. A pattern is checked at
// run time by the door, like any caller.
var literalServiceRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ServiceLayer is the layer a service runs in: a tenant's (Tenant), a fleet
// product's (Product non-empty), or the platform's, /system (neither).
type ServiceLayer struct {
	Tenant  bool
	Product string
}

func (l ServiceLayer) String() string {
	switch {
	case l.Tenant:
		return "tenant-owned"
	case l.Product != "":
		return fmt.Sprintf("product %q", l.Product)
	default:
		return "/system"
	}
}

// ValidateProductName checks a product name: its shape, not a tid's shape, not
// reserved.
func ValidateProductName(name string) error {
	switch {
	case !productNameRe.MatchString(name):
		return fmt.Errorf("product %q is not a product name, [a-z][a-z0-9-]{0,30}[a-z0-9]", name)
	case spiffeReservedTags[name] || name == commonspiffe.ReservedProductName:
		return fmt.Errorf("product %q is reserved (a SPIFFE tag such as system, tenants or products, or tacplus)", name)
	case tidShapeRe.MatchString(name):
		return fmt.Errorf("product %q has a tid's shape (8 of [0-9a-z]); its namespace tac-<deployment>-%s would collide with a tenant's", name, name)
	}
	return nil
}

// S2SEdgeRefusal says why a service in layer caller may not call one in layer
// target, or "" when it may. The rules are about service principals; user
// traffic (C2S) is not an edge.
func S2SEdgeRefusal(caller, target ServiceLayer) string {
	switch {
	case caller.Tenant:
		return "" // tenant -> anything; tenant -> another tenant is refused at the door (#258)
	case caller.Product != "" && target.Tenant:
		return "a product never calls a tenant"
	case caller.Product != "" && target.Product != "" && target.Product != caller.Product:
		return "a product never calls another product"
	case caller.Product == "" && target.Product != "":
		return "the platform never calls a product"
	}
	return ""
}

// layerIndex resolves service labels and client references to layers.
type layerIndex struct {
	files  map[string]*FileDescriptor
	owned  map[string]bool
	labels map[string]ServiceLayer // (spec.service) name and package -> layer
}

func newLayerIndex(dp *DescriptorParser) *layerIndex {
	ix := &layerIndex{files: dp.GetAllFiles(), owned: TenantOwnedServices(dp), labels: map[string]ServiceLayer{}}
	for _, fd := range ix.files {
		if fd.ServiceName == "" {
			continue
		}
		l := ServiceLayer{Tenant: fd.ServiceTenantOwned, Product: fd.ServiceProduct}
		ix.labels[fd.ServiceName] = l
		ix.labels[fd.Package] = l
	}
	return ix
}

// ServiceLayers is every loaded service's layer, by its (spec.service) name
// and its package.
func ServiceLayers(dp *DescriptorParser) map[string]ServiceLayer {
	if dp == nil {
		return map[string]ServiceLayer{}
	}
	return newLayerIndex(dp).labels
}

// fileLayer is the layer of the service a file belongs to.
func (ix *layerIndex) fileLayer(fd *FileDescriptor) ServiceLayer {
	if fd.ServiceName != "" {
		return ServiceLayer{Tenant: fd.ServiceTenantOwned, Product: fd.ServiceProduct}
	}
	return ix.labels[fd.Package]
}

// serviceLabel is the name a file's service is known by.
func serviceLabel(fd *FileDescriptor) string {
	if fd.ServiceName != "" {
		return fd.ServiceName
	}
	return fd.Package
}

// refLayer resolves a "package.Service" client reference to the layer of the
// service that serves it: a surface hosted by label (a vservice, a trait
// plane) is served by its host. ok is false when the reference's package is
// not loaded in this run: the fleet build, which loads it, decides then.
func (ix *layerIndex) refLayer(ref string) (label string, layer ServiceLayer, ok bool) {
	dot := strings.LastIndex(ref, ".")
	if dot < 0 {
		return "", ServiceLayer{}, false
	}
	pkg, name := ref[:dot], ref[dot+1:]
	for _, fd := range ix.files {
		if fd.Package != pkg {
			continue
		}
		if t := fd.Trait; t != nil && t.Host != "" && (name == t.S2SService || name == t.C2SService) {
			if l, known := ix.labels[t.Host]; known {
				return t.Host, l, true
			}
		}
		for i := range fd.Services {
			svc := &fd.Services[i]
			if svc.Name != name {
				continue
			}
			if IsTenantRouted(fd, svc, ix.owned) {
				host := serviceLabel(fd)
				if svc.HostLabel != "" {
					host = svc.HostLabel
				}
				return host, ServiceLayer{Tenant: true}, true
			}
			if svc.HostLabel != "" {
				l, known := ix.labels[svc.HostLabel]
				return svc.HostLabel, l, known
			}
			return serviceLabel(fd), ix.fileLayer(fd), true
		}
	}
	l, known := ix.labels[pkg]
	return pkg, l, known
}

// recordPlaneHost is the host of the record query plane every
// (spec.record_read) resolves to: the trait whose host is a record store.
func (ix *layerIndex) recordPlaneHost() string {
	stores := map[string]bool{}
	for _, fd := range ix.files {
		if fd.RecordStore && fd.ServiceName != "" {
			stores[fd.ServiceName] = true
		}
	}
	var hosts []string
	for _, fd := range ix.files {
		if t := fd.Trait; t != nil && t.Host != "" && t.S2SService != "" && stores[t.Host] {
			hosts = append(hosts, t.Host)
		}
	}
	sort.Strings(hosts)
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0]
}

// recordPlaneRef reports whether a "package.Service" client reference is a
// record query plane's S2S half (reads, watches and the record write surface):
// the S2S service of a trait hosted on a record store, or, once injected, a
// synthesized record map service.
func (ix *layerIndex) recordPlaneRef(ref string) bool {
	dot := strings.LastIndex(ref, ".")
	if dot < 0 {
		return false
	}
	pkg, name := ref[:dot], ref[dot+1:]
	for _, fd := range ix.files {
		if fd.Package != pkg {
			continue
		}
		if t := fd.Trait; t != nil && t.Host != "" && name == t.S2SService && ix.recordStore(t.Host) {
			return true
		}
		for i := range fd.Services {
			if svc := &fd.Services[i]; svc.Name == name && !svc.IsC2S && svc.SynthesizedKind == SynthesizedKindRecordMap {
				return true
			}
		}
	}
	return false
}

// recordStore reports whether label is a loaded (spec.record_store).
func (ix *layerIndex) recordStore(label string) bool {
	for _, fd := range ix.files {
		if fd.RecordStore && fd.ServiceName == label {
			return true
		}
	}
	return false
}

// ValidateProductLayer enforces the product layer at generation time:
// (spec.service).product's name and exclusivity, no product in a sys contract,
// every S2S edge a loaded service declares against the edge table, no config
// slice owned by a product, and
// (spec.caller_tid)'s shape and its use on a product service. The fleet build
// runs it over the merged protos, where every edge's far end is loaded.
func ValidateProductLayer(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	ix := newLayerIndex(dp)
	paths := make([]string, 0, len(ix.files))
	for path := range ix.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var errs []string
	edge := func(path, callerLabel string, caller ServiceLayer, targetLabel string, target ServiceLayer, via string) {
		if why := S2SEdgeRefusal(caller, target); why != "" {
			errs = append(errs, fmt.Sprintf("%s: %q (%s) -> %q (%s), by %s: %s (%s)",
				path, callerLabel, caller, targetLabel, target, via, why, ProductLayerRule))
		}
	}
	planeHost := ix.recordPlaneHost()
	// The record plane is hosted at /system, but its rows are tenants' and the
	// platform's: a product reaches them by no edge (its server refuses a
	// product's caller too).
	planeEdge := func(path, callerLabel string, caller ServiceLayer, via string) {
		if caller.Product != "" {
			errs = append(errs, fmt.Sprintf("%s: %q (%s) -> %q (the record plane), by %s: a product never reads or writes the record plane, whose rows are tenants' and the platform's (%s)",
				path, callerLabel, caller, planeHost, via, ProductLayerRule))
		}
	}

	for _, path := range paths {
		fd := ix.files[path]
		if p := fd.ServiceProduct; p != "" {
			if err := ValidateProductName(p); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %s: (spec.service).product: %v", path, fd.Package, err))
			}
			if fd.ServiceTenantOwned || fd.ServicePinnedTenant != "" {
				errs = append(errs, fmt.Sprintf("%s: %s: (spec.service).product %q with tenant_owned or pinned_tenant; a product service runs once per fleet, never per tenant",
					path, fd.Package, p))
			}
			if strings.HasPrefix(path, "sys/") {
				errs = append(errs, fmt.Sprintf("%s: a sys contract declares (spec.service).product %q; a tacplus release ships no product code",
					path, p))
			}
		}

		// Decision 4 (2026-11-09): a product owns no slice of the tenant (or a
		// tenant's project) config in this release.
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			owner := msg.Annotations["config_slice_service"].StringValue
			if l, ok := ix.labels[owner]; ok && owner != "" && l.Product != "" {
				errs = append(errs, fmt.Sprintf("%s: %s.%s: (spec.config_slice) owned by %q (%s): a product owns no slice of the tenant config; its per-tenant settings go through (spec.caller_tid) rpcs (%s)",
					path, fd.Package, msg.Name, owner, l, ProductLayerRule))
			}
		}

		caller, callerLabel := ix.fileLayer(fd), serviceLabel(fd)
		for _, ref := range fd.GrpcS2SClients {
			if ix.recordPlaneRef(ref) {
				planeEdge(path, callerLabel, caller, "grpc_s2s_client "+ref)
			}
			if label, target, ok := ix.refLayer(ref); ok {
				edge(path, callerLabel, caller, label, target, "grpc_s2s_client "+ref)
			}
		}
		for _, root := range fd.StateImports {
			if dot := strings.LastIndex(root, "."); dot > 0 {
				if target, ok := ix.labels[root[:dot]]; ok {
					edge(path, callerLabel, caller, root[:dot], target, "state_import "+root)
				}
			}
		}
		if len(fd.RecordReads) > 0 && planeHost != "" {
			planeEdge(path, callerLabel, caller, "record_read "+fd.RecordReads[0].Record)
			edge(path, callerLabel, caller, planeHost, ix.labels[planeHost], "record_read "+fd.RecordReads[0].Record)
		}
		for si := range fd.Services {
			svc := &fd.Services[si]
			if !svc.IsS2S {
				continue
			}
			targetLabel, target := callerLabel, caller
			if svc.HostLabel != "" {
				l, known := ix.labels[svc.HostLabel]
				if !known {
					continue
				}
				targetLabel, target = svc.HostLabel, l
			}
			if IsTenantRouted(fd, svc, ix.owned) {
				target = ServiceLayer{Tenant: true}
			}
			lists := [][]string{svc.AuthServices}
			for mi := range svc.Methods {
				lists = append(lists, svc.Methods[mi].AuthServices)
			}
			seen := map[string]bool{}
			for _, list := range lists {
				for _, who := range list {
					if seen[who] || !literalServiceRe.MatchString(who) {
						continue
					}
					seen[who] = true
					if l, ok := ix.labels[who]; ok {
						edge(path, who, l, targetLabel, target, "auth_services of "+svc.Name)
					}
				}
			}
		}
	}
	errs = append(errs, validateCallerTid(ix, paths)...)
	if len(errs) > 0 {
		return fmt.Errorf("product layer errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// validateCallerTid checks (spec.caller_tid): one plain string per request,
// only on a message an rpc takes as its request, and, on a product service,
// on every request field that names a tenant.
func validateCallerTid(ix *layerIndex, paths []string) []string {
	requests := map[string]bool{}          // full name of every rpc request loaded
	productRequests := map[string]string{} // full name -> product, for requests of product services
	for _, path := range paths {
		fd := ix.files[path]
		for si := range fd.Services {
			for _, m := range fd.Services[si].Methods {
				requests[m.InputType] = true
				if fd.ServiceProduct != "" {
					productRequests[m.InputType] = fd.ServiceProduct
				}
			}
		}
	}
	var errs []string
	for _, path := range paths {
		fd := ix.files[path]
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			full := fd.Package + "." + msg.Name
			bound := 0
			for fi := range msg.Fields {
				f := &msg.Fields[fi]
				where := fmt.Sprintf("%s: %s.%s", path, msg.Name, f.Name)
				if !f.Annotations["caller_tid"].BoolValue {
					if product, ok := productRequests[full]; ok && tenantNamingFields[f.Name] {
						errs = append(errs, fmt.Sprintf("%s names a tenant on a request of product %q without (spec.caller_tid); a product keeps a tenant's data only keyed by the caller's token's tid, never the request's (%s)",
							where, product, ProductLayerRule))
					}
					continue
				}
				bound++
				switch {
				case f.IsRepeated || f.IsMap:
					errs = append(errs, where+": (spec.caller_tid) on a repeated or map field; a call is for exactly one tenant")
				case f.Type != "string":
					errs = append(errs, fmt.Sprintf("%s: (spec.caller_tid) on a %s field; a tid is a string", where, f.Type))
				case f.IsOptional:
					errs = append(errs, where+": (spec.caller_tid) on a proto3 optional field; the binding is a plain string, filled when empty")
				}
				// The request is validated before the binding fills it, so a
				// field that must be non-empty refuses the empty value the
				// binding exists to fill.
				for _, a := range []string{"assert_required", "assert_min_size"} {
					if _, ok := f.Annotations[a]; ok {
						errs = append(errs, fmt.Sprintf("%s: (spec.caller_tid) with (spec.%s); the field is filled from the token when empty, so the client may leave it so", where, a))
					}
				}
				if !requests[full] {
					errs = append(errs, where+": (spec.caller_tid) on a message no rpc takes as its request; the binding is the wrapper's, on a request")
				}
			}
			if bound > 1 {
				errs = append(errs, fmt.Sprintf("%s: %s declares (spec.caller_tid) on more than one field; one field carries the caller's tid", path, msg.Name))
			}
		}
	}
	return errs
}

// SelfServiceProduct is the (spec.service).product of the service
// selfPackage names, or "" when it declares none (a platform or tenant-owned
// service) or nothing is loaded. The generated authorizer bakes it in, and an
// instance whose own identity is in another layer does not start.
func SelfServiceProduct(dp *DescriptorParser, selfPackage string) string {
	if dp == nil || selfPackage == "" {
		return ""
	}
	for _, fd := range dp.GetAllFiles() {
		if fd.Package == selfPackage && fd.ServiceName != "" {
			return fd.ServiceProduct
		}
	}
	return ""
}

// ClientTargetLayer is the layer a generated S2S client for ref
// ("package.Service") checks its caller against before every call
// (serviceconnection.CheckTargetLayer and its Java and Rust twins): "tenant"
// for a tenant-owned or tenant-routed target, "product:<name>" for a
// product's, and "" for a /system target, which every layer may call, or one
// not loaded. A surface hosted by label is its host's.
func ClientTargetLayer(dp *DescriptorParser, ref string) string {
	if dp == nil {
		return ""
	}
	_, l, ok := newLayerIndex(dp).refLayer(ref)
	switch {
	case !ok:
		return ""
	case l.Tenant:
		return "tenant"
	case l.Product != "":
		return "product:" + l.Product
	}
	return ""
}

// SelfServiceProductIn is SelfServiceProduct over the protos under
// annotatedProtoDir, "" when there is none.
func SelfServiceProductIn(annotatedProtoDir, selfPackage string) (string, error) {
	if annotatedProtoDir == "" || selfPackage == "" {
		return "", nil
	}
	dp, err := NewDescriptorParser(annotatedProtoDir)
	if err != nil {
		return "", fmt.Errorf("create descriptor parser for the service's product: %w", err)
	}
	return SelfServiceProduct(dp, selfPackage), nil
}

// productLayerServerLanguages are the languages that generate servers (each
// has a generated door, #258), true for those whose server also has the
// product layer's doors: the product door, the S2S client's layer check and
// the (spec.caller_tid) binding (Go, Java, Rust; roadmap/PRODUCTS_LAYER.md
// section 3).
var productLayerServerLanguages = map[string]bool{"go": true, "java": true, "rust": true, "python": false, "typescript": false}

// productTenantDeletionLanguages are the languages whose server generates a
// product custodian's OnTenantDeleted and the runner that calls it
// (SelfProductCustodian; sys/tenantdeletion.proto).
var productTenantDeletionLanguages = map[string]bool{"go": true, "java": true, "rust": true}

// ValidateProductLayerHosting refuses to generate a server the product layer's
// doors do not cover yet, which would fail open: in Python and TypeScript, the
// server of a product service (it would run a product's code behind no product
// door) or of one binding (spec.caller_tid) (it would serve the field the
// client wrote); in a language without its generated OnTenantDeleted, a
// product service binding (spec.caller_tid), whose custodian rows must go
// with their tenant. Clients of a product service generate as before: an edge
// into a product is the door's to check, and a caller_tid field is the
// server's to bind.
func ValidateProductLayerHosting(dp *DescriptorParser, selfPackage, language string) error {
	doors, server := productLayerServerLanguages[language]
	deletion := productTenantDeletionLanguages[language]
	if dp == nil || !server {
		return nil
	}
	var errs []string
	for path, fd := range dp.GetAllFiles() {
		if selfPackage != "" && fd.Package != selfPackage {
			continue
		}
		if fd.ServiceProduct != "" && !doors {
			errs = append(errs, fmt.Sprintf("%s: %s: (spec.service).product %q: no %s product door", path, fd.Package, fd.ServiceProduct, language))
		}
		for mi := range fd.Messages {
			for fi := range fd.Messages[mi].Fields {
				if !fd.Messages[mi].Fields[fi].Annotations["caller_tid"].BoolValue {
					continue
				}
				switch {
				case fd.ServiceProduct != "" && !deletion:
					errs = append(errs, fmt.Sprintf("%s: %s.%s (spec.caller_tid) on product %q: a product's custodian rows go with their tenant, and OnTenantDeleted is not generated in %s yet",
						path, fd.Messages[mi].Name, fd.Messages[mi].Fields[fi].Name, fd.ServiceProduct, language))
				case !doors:
					errs = append(errs, fmt.Sprintf("%s: %s.%s (spec.caller_tid): no %s binding", path, fd.Messages[mi].Name, fd.Messages[mi].Fields[fi].Name, language))
				}
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("the product layer's generated doors are not all in this release yet (#475), so a %s server of it would fail open; not generated:\n  %s",
		language, strings.Join(errs, "\n  "))
}

// SelfProductCustodian reports whether selfPackage is a product service that
// binds (spec.caller_tid): a custodian of tenants' data, which deletes a
// tenant's rows when the tenant is deleted. Its server generates the
// handler's OnTenantDeleted and a runner that calls it for each tid in the
// fleet's sys/tenantdeletion.proto root, which it reads.
func SelfProductCustodian(dp *DescriptorParser, selfPackage string) bool {
	if dp == nil || selfPackage == "" {
		return false
	}
	for _, pkg := range ProductCustodians(dp.GetAllFiles()) {
		if pkg == selfPackage {
			return true
		}
	}
	return false
}

// ProductCustodians is every package of files that is a product service
// binding (spec.caller_tid), sorted: the readers of the fleet's tenant
// deletions, whom the state host's generated state service authorizes.
func ProductCustodians(files map[string]*FileDescriptor) []string {
	product := map[string]bool{}
	for _, fd := range files {
		if fd.ServiceProduct != "" {
			product[fd.Package] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, fd := range files {
		if !product[fd.Package] || seen[fd.Package] {
			continue
		}
		for mi := range fd.Messages {
			for fi := range fd.Messages[mi].Fields {
				if fd.Messages[mi].Fields[fi].Annotations["caller_tid"].BoolValue && !seen[fd.Package] {
					seen[fd.Package] = true
					out = append(out, fd.Package)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// RefusedS2SClient is an S2S service a declaring service may not call: Ref is
// "package.Service", ProtoFile the proto that declares it (or the trait's that
// hosts it), Why the rule.
type RefusedS2SClient struct {
	Ref, ProtoFile, Service, Why string
}

// RefusedS2SClients is every loaded S2S service the service selfPackage names
// may not call (design section 1, point 3): by the edge table, and, for a
// product, a record plane's S2S half. all_grpc_s2s_clients generates a client
// for none of them, so it never generates one for a refused edge. Sorted by
// Ref; nil when selfPackage names no loaded service.
func RefusedS2SClients(dp *DescriptorParser, selfPackage string) []RefusedS2SClient {
	if dp == nil || selfPackage == "" {
		return nil
	}
	ix := newLayerIndex(dp)
	var self *FileDescriptor
	for _, fd := range ix.files {
		if fd.Package == selfPackage && fd.ServiceName != "" {
			self = fd
		}
	}
	if self == nil {
		return nil
	}
	caller := ix.fileLayer(self)
	seen := map[string]bool{}
	var out []RefusedS2SClient
	consider := func(path, pkg, name string) {
		ref := pkg + "." + name
		if seen[ref] {
			return
		}
		seen[ref] = true
		why := ""
		if _, target, ok := ix.refLayer(ref); ok {
			why = S2SEdgeRefusal(caller, target)
		}
		if why == "" && caller.Product != "" && ix.recordPlaneRef(ref) {
			why = "a product never reads or writes the record plane"
		}
		if why != "" {
			out = append(out, RefusedS2SClient{Ref: ref, ProtoFile: path, Service: name, Why: why + " (" + ProductLayerRule + ")"})
		}
	}
	for path, fd := range ix.files {
		for i := range fd.Services {
			if fd.Services[i].IsS2S {
				consider(path, fd.Package, fd.Services[i].Name)
			}
		}
		if t := fd.Trait; t != nil && t.S2SService != "" {
			consider(path, fd.Package, t.S2SService)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}
