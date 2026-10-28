// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// RouteFieldDescriptor is a method's resolved (spec.rpc).route_field: the
// request field whose value, a tid, names the tenant whose instance receives
// the call. Resolved once against the request descriptor so every router (the
// gateway's REST and gRPC route tables, every generated client) reads the same
// field the same way.
type RouteFieldDescriptor struct {
	Field    string // top-level request field name, as declared
	JSONName string // the field's proto3 JSON name
	Number   int32  // the field's wire number, for a router that reads raw frames
}

// routeFieldDescriptor resolves (spec.rpc).route_field on method's request.
// The router reads ONE plain string: a repeated, map, optional or non-string
// field would make "the target tenant" a question with more than one answer.
func routeFieldDescriptor(method protoreflect.MethodDescriptor, name string) (*RouteFieldDescriptor, error) {
	input := method.Input()
	fd := input.Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return nil, fmt.Errorf("%s declares (spec.rpc).route_field %q, but its request %s has no top-level field of that name; the router reads the target tenant off the request",
			method.FullName(), name, input.FullName())
	}
	if fd.IsList() || fd.IsMap() {
		return nil, fmt.Errorf("%s declares (spec.rpc).route_field on %s.%s, which is repeated or a map; a call routes to exactly one tenant",
			method.FullName(), input.FullName(), name)
	}
	if fd.Kind() != protoreflect.StringKind {
		return nil, fmt.Errorf("%s declares (spec.rpc).route_field on %s.%s, which is a %s field; the target tenant id is a string",
			method.FullName(), input.FullName(), name, fd.Kind())
	}
	if fd.HasPresence() {
		return nil, fmt.Errorf("%s declares (spec.rpc).route_field on %s.%s, which is proto3 optional; the route field is a plain string, and an empty one is refused at run time",
			method.FullName(), input.FullName(), name)
	}
	return &RouteFieldDescriptor{Field: name, JSONName: fd.JSONName(), Number: int32(fd.Number())}, nil
}

// TenantOwnedServices is the set of service names (the (spec.service) name and
// the package) whose file declares (spec.service).tenant_owned.
func TenantOwnedServices(dp *DescriptorParser) map[string]bool {
	owned := make(map[string]bool)
	if dp == nil {
		return owned
	}
	for _, fd := range dp.GetAllFiles() {
		if !fd.ServiceTenantOwned {
			continue
		}
		owned[fd.Package] = true
		if fd.ServiceName != "" {
			owned[fd.ServiceName] = true
		}
	}
	return owned
}

// IsTenantRouted reports whether the rpcs of svc, declared in fd, are routed
// by tenant: a user-declared s2s or c2s service of a tenant-owned file, or a
// surface hosted (service_s2s/service_c2s label) by a tenant-owned service.
// C2D services dial a local daemon and synthesized planes carry their own
// tenant pin, so neither is ever addressed by tenant.
func IsTenantRouted(fd *FileDescriptor, svc *ServiceDescriptor, tenantOwned map[string]bool) bool {
	if svc.Synthesized || !(svc.IsS2S || svc.IsC2S) {
		return false
	}
	return fd.ServiceTenantOwned || (svc.HostLabel != "" && tenantOwned[svc.HostLabel])
}

// ValidateTenantRouting enforces (spec.rpc).route_field and
// RPC_FLAG_ANY_TENANT. Every rpc of a tenant-routed service must declare its
// route field: tacplus refuses to generate one that does not, rather than let
// a router fall back on the caller's token. Every other rpc must declare
// neither: a platform service is never addressed by tenant, and a declaration
// every router ignores is refused instead of silently doing nothing.
func ValidateTenantRouting(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	tenantOwned := TenantOwnedServices(dp)
	var errs []string
	for _, fd := range dp.GetAllFiles() {
		for si := range fd.Services {
			svc := &fd.Services[si]
			routed := IsTenantRouted(fd, svc, tenantOwned)
			for mi := range svc.Methods {
				m := &svc.Methods[mi]
				switch {
				case routed && m.RouteField == nil:
					errs = append(errs, fmt.Sprintf("%s.%s: an rpc of a tenant_owned service must declare (spec.rpc).route_field, the request field carrying the target tenant id; the call is never routed by the caller's token",
						svc.Name, m.Name))
				case !routed && m.RouteField != nil:
					errs = append(errs, fmt.Sprintf("%s.%s: (spec.rpc).route_field %q on an rpc of a service that is not tenant_owned; a platform service is never addressed by tenant",
						svc.Name, m.Name, m.RouteField.Field))
				}
				if !routed && m.AnyTenant {
					errs = append(errs, fmt.Sprintf("%s.%s: RPC_FLAG_ANY_TENANT on an rpc of a service that is not tenant_owned; only a tenant-owned instance has a tenant door to open",
						svc.Name, m.Name))
				}
			}
		}
	}
	if len(errs) > 0 {
		// GetAllFiles is a map: sort so the message is identical run-to-run.
		sort.Strings(errs)
		return fmt.Errorf("tenant routing errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
