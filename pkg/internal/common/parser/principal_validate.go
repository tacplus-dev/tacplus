// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidatePrincipalAnnotations enforces the shape the principal and namespace
// annotations need to be generatable: the wrapper compares the field against a
// caller string (the subject, or the namespace claim), so the field must be a
// singular string. Generation FAILS on violation rather than emitting code that
// cannot compile.
func ValidatePrincipalAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	// Deterministic file order: the errors below are aggregated into one
	// message, and a map walk would reorder it run to run.
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	fds := make([]*FileDescriptor, 0, len(all))
	for _, path := range paths {
		fds = append(fds, all[path])
	}

	// Index every message by fully-qualified name so an rpc's request type can
	// be resolved back to the fields it declares.
	msgs := map[string]*MessageDescriptor{}
	for _, fd := range fds {
		for mi := range fd.Messages {
			msgs[fd.Package+"."+fd.Messages[mi].Name] = &fd.Messages[mi]
		}
	}

	var errs []string
	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			// At most one principal field per message: the wrapper binds THE
			// principal of the type, so two candidates have no defined meaning.
			seen := map[string]string{}
			for fi := range msg.Fields {
				field := &msg.Fields[fi]
				for _, name := range []string{"principal", "store_principal", "namespace", "store_namespace"} {
					if _, ok := field.Annotations[name]; !ok {
						continue
					}
					if field.Type != "string" || field.IsMap || field.IsRepeated {
						errs = append(errs, fmt.Sprintf("%s.%s.%s: (spec.%s) requires a singular string field; got %s",
							fd.Package, msg.Name, field.Name, name, field.Type))
					}
					if prev, dup := seen[name]; dup {
						errs = append(errs, fmt.Sprintf("%s.%s: (spec.%s) is declared on both %s and %s; exactly one field per message may carry it",
							fd.Package, msg.Name, name, prev, field.Name))
						continue
					}
					seen[name] = field.Name
				}
			}
		}
	}

	// An rpc that can never carry a verified caller must not demand one of
	// every request.
	//
	// auth_disabled and service_c2d are the two shapes where nothing runs an
	// authorizer over the request's credentials — a c2d service has no
	// authorizer at all, its boundary being the unix socket's file permissions
	// — so the generated wrapper has no verified subject or namespace claim to
	// stamp with. The wrapper would then refuse every call with UNAUTHENTICATED
	// and the rpc would be uncallable by construction. That is a contradiction
	// in the proto, not a runtime condition, so it fails HERE, at
	// `tacplus build`, for the same reason MCP-5 (mcp_validate.go) refuses an
	// MCP tool on a c2d service: the shape cannot be generated honestly, and a
	// generation error is the only place an author sees it.
	//
	// Language-agnostic by construction: the predicate is the proto's, so the
	// Rust, Go and Java wrappers are all covered by this one check.
	for _, fd := range fds {
		for si := range fd.Services {
			svc := &fd.Services[si]
			for mi := range svc.Methods {
				m := &svc.Methods[mi]
				if !m.AuthDisabled && !svc.AuthDisabled && !svc.IsC2D {
					continue
				}
				pkg := m.InputTypePackage
				if pkg == "" {
					pkg = fd.Package
				}
				name := m.InputType
				if i := strings.LastIndex(name, "."); i >= 0 {
					name = name[i+1:]
				}
				msg, ok := msgs[pkg+"."+name]
				if !ok {
					continue
				}
				var declared []string
				for _, marker := range []string{"principal", "namespace"} {
					for fi := range msg.Fields {
						if _, ok := msg.Fields[fi].Annotations[marker]; ok {
							declared = append(declared, fmt.Sprintf("(spec.%s) on %s.%s", marker, name, msg.Fields[fi].Name))
							break
						}
					}
				}
				if len(declared) == 0 {
					continue
				}
				why := "auth_disabled"
				if svc.IsC2D {
					why = "service_c2d"
				}
				errs = append(errs, fmt.Sprintf(
					"%s.%s/%s: its request declares %s, but the rpc is %s; nothing verifies a caller on that path, "+
						"so the wrapper has no subject or namespace claim to bind the field to and every call would be "+
						"refused UNAUTHENTICATED. Drop the marker, or host the rpc on an authenticated (service_c2s/"+
						"service_s2s) surface",
					fd.Package, svc.Name, m.Name, strings.Join(declared, " and "), why))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("principal annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
