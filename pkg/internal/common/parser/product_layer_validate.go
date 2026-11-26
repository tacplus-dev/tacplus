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

// ValidateProductLayer enforces the product layer at generation time:
// (spec.service).product's name and exclusivity, no product in a sys contract,
// every S2S edge a loaded service declares against the edge table, and
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

		caller, callerLabel := ix.fileLayer(fd), serviceLabel(fd)
		for _, ref := range fd.GrpcS2SClients {
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
// has a generated door, #258).
var productLayerServerLanguages = map[string]bool{"go": true, "java": true, "rust": true, "python": true, "typescript": true}

// ValidateProductLayerHosting refuses to generate the server of a product
// service, or of one binding (spec.caller_tid), until the generated doors
// exist: the product door, the S2S client check and the caller_tid binding
// are step 2 of roadmap/PRODUCTS_LAYER.md's order. A server generated before
// them would run a product's code as a /system service and serve a
// caller_tid field the client wrote, which is failing open. Clients of a
// product service generate as before: an edge into a product is the door's to
// check, and a caller_tid field is the server's to bind.
func ValidateProductLayerHosting(dp *DescriptorParser, selfPackage, language string) error {
	if dp == nil || !productLayerServerLanguages[language] {
		return nil
	}
	var errs []string
	for path, fd := range dp.GetAllFiles() {
		if selfPackage != "" && fd.Package != selfPackage {
			continue
		}
		if fd.ServiceProduct != "" {
			errs = append(errs, fmt.Sprintf("%s: %s: (spec.service).product %q", path, fd.Package, fd.ServiceProduct))
		}
		for mi := range fd.Messages {
			for fi := range fd.Messages[mi].Fields {
				if fd.Messages[mi].Fields[fi].Annotations["caller_tid"].BoolValue {
					errs = append(errs, fmt.Sprintf("%s: %s.%s (spec.caller_tid)", path, fd.Messages[mi].Name, fd.Messages[mi].Fields[fi].Name))
				}
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("the product layer's generated doors are not in this release yet (#475), so a %s server of it would fail open; not generated:\n  %s",
		language, strings.Join(errs, "\n  "))
}
