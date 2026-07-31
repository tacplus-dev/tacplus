// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// restRoute is one (HTTP method, REST path) pair a gateway RPC is reachable on.
type restRoute struct {
	httpMethod string
	path       string
}

// restRoutesForMethod returns every (HTTP method, path) route a method exposes:
// the convention path and the optional rest_path alias, across all HTTP methods.
// Non-gateway methods (no HttpPath) expose no REST routes.
func restRoutesForMethod(m *MethodDescriptor) []restRoute {
	if m == nil || m.HttpPath == "" {
		return nil
	}
	methods := m.HttpMethods
	if len(methods) == 0 {
		methods = []string{m.HttpMethod}
	}
	var out []restRoute
	for _, hm := range methods {
		out = append(out, restRoute{hm, m.HttpPath})
		if m.HttpAliasPath != "" {
			out = append(out, restRoute{hm, m.HttpAliasPath})
		}
	}
	return out
}

// ValidateRestPathCollisions fails generation when two distinct gateway RPCs map
// to the same (HTTP method, REST path) route. REST routing must be as
// collision-free as the gRPC + entitlement-grant identity. The convention path
// now carries the service segment ({root}/{package}/{service}/{method}), but
// rest_path aliases, event-tag routes, and future schemes can still collide, so
// this is the generation-time backstop — mirroring
// validateEntitlementGrantTokenCollisions for the authorization identity.
func ValidateRestPathCollisions(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	seen := make(map[string]string) // "METHOD path" -> "package.Service.RPC"
	var errs []string
	for _, fd := range dp.GetAllFiles() {
		for si := range fd.Services {
			svc := &fd.Services[si]
			for mi := range svc.Methods {
				m := &svc.Methods[mi]
				desc := fmt.Sprintf("%s.%s.%s", fd.Package, svc.Name, m.Name)
				for _, r := range restRoutesForMethod(m) {
					key := r.httpMethod + " " + r.path
					if prev, ok := seen[key]; ok && prev != desc {
						errs = append(errs, fmt.Sprintf(
							"REST route %s %s collides between %s and %s",
							r.httpMethod, r.path, prev, desc))
						continue
					}
					seen[key] = desc
				}
			}
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf(
			"REST path collisions (each route must be unique per HTTP method; "+
				"the convention path is {root}/{package}/{service}/{method}):\n  %s",
			strings.Join(errs, "\n  "))
	}
	return nil
}
