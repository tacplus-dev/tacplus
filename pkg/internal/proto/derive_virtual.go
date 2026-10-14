// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"go.uber.org/zap"
)

// DeriveVirtualServiceClients inspects the proto annotations for the service
// identified by selfPackage and returns GrpcEntry values for virtual service
// clients that should be auto-registered.
//
// For each annotation on the service's proto file:
//   - opa_import  → OpaEval client (source file determined by scanning for spec.opa_eval)
//
// The TargetService and ProtoPackage fields are populated so the generator
// can resolve the correct package name and token audience.
//
// Returns nil if the service has no virtual import annotations.
func DeriveVirtualServiceClients(inputDir, selfPackage string) ([]tactype.GrpcEntry, error) {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser for virtual client derivation: %w", err)
	}

	// Find the service's own proto file. Nothing in the schema limits a package
	// to ONE file carrying option (spec.service) — parseServiceIdentity is
	// per-file and defaults the name to the package — and the fields read off
	// serviceDesc below (Imports, OpaImportTargets, RecordReads) are file-scoped,
	// so a raw map walk could derive a different client set on each build. Sorted
	// path order, matching findAnnotatedSourceFile and the trait scan below.
	var serviceDesc *parser.FileDescriptor
	selfPaths := make([]string, 0, len(dp.GetAllFiles()))
	for rp := range dp.GetAllFiles() {
		selfPaths = append(selfPaths, rp)
	}
	sort.Strings(selfPaths)
	for _, rp := range selfPaths {
		fd := dp.GetAllFiles()[rp]
		if fd.ServiceName != "" && fd.Package == selfPackage {
			serviceDesc = fd
			break
		}
	}
	if serviceDesc == nil {
		return nil, nil
	}

	var entries []tactype.GrpcEntry

	// OPA imports → OpaEval client.
	// Skip if this service IS the OPA server (imports sys/opalib.proto).
	isOpaServer := false
	for _, imp := range serviceDesc.Imports {
		if imp == "sys/opalib.proto" {
			isOpaServer = true
			break
		}
	}
	if !isOpaServer {
		for _, imp := range serviceDesc.OpaImportTargets {
			if imp.Service == "" {
				continue
			}
			// Use imp.Source (e.g. "backendtoken.BackendTokenEvalInput") to select
			// the specific vservice proto whose OpaEval the consumer needs.
			// This is essential when multiple vservice files have opa_eval annotations.
			protoFile, pkg, err := findAnnotatedSourceFileByPkg(dp, "opa_eval", imp.Source)
			if err != nil {
				return nil, err
			}
			entries = append(entries, tactype.GrpcEntry{
				ProtoFile:     protoFile,
				ServiceName:   "OpaEval",
				TargetService: imp.Service,
				ProtoPackage:  pkg,
			})
			zap.L().Info("derived virtual service client from opa_import",
				zap.String("service", selfPackage),
				zap.String("virtual", "OpaEval"),
				zap.String("target", imp.Service),
				zap.String("source", imp.Source),
				zap.String("sourceFile", protoFile))

		}
	}

	// (spec.record_read) -> a client for the record query plane's S2S half.
	//
	// The reader declares which RECORDS it reads; it never names the plane, the
	// store, or the transport. Those are consequences of the read, derived here
	// from the (spec.trait) that hosts the plane, so a reader's proto cannot
	// disagree with the plane it actually calls. Restating the dependency by hand
	// is exactly the two-independently-maintained-surfaces shape BIG GOAL 7's
	// eventbus finding was.
	//
	// Derived rather than injected into the proto TEXT, because client inference
	// runs BEFORE the plane's services are materialized
	// (AppendRecordMapServiceToProtos appends them later). service.go's resolver
	// already understands that a client may reference a trait's declared service
	// name before it exists; this produces the entry that resolver expects.
	//
	// One entry regardless of how many records are read: they are all RPCs on the
	// same service. Nothing is emitted when the reader declares no record_read,
	// so this is a no-op for every service in the fleet that predates the field.
	if len(serviceDesc.RecordReads) > 0 {
		var all []*parser.FileDescriptor
		for _, fd := range dp.GetAllFiles() {
			all = append(all, fd)
		}
		traitFd := FindRecordPlaneTrait(all)
		// No plane to name. ValidateRecordReadAnnotations has already refused this
		// build with a message that says so; returning an error here too would
		// only replace that message with a worse one.
		if traitFd != nil {
			traitPath := traitFd.Path
			entries = append(entries, tactype.GrpcEntry{
				ProtoFile:    traitPath,
				ServiceName:  traitFd.Trait.S2SService,
				ProtoPackage: traitFd.Package,
				// The plane's proto package ("query") is NOT a service: the RPCs are
				// served by the trait's host ("stratum"). TargetService is what every
				// language's client generator and audience mapper reads to route and
				// to mint against the PHYSICAL host rather than the path package —
				// the same field the opa/graph/timeseries virtual clients above set.
				// Without it Go and Java mint aud=query for /query.RecordMap/... and
				// the token authority refuses the exchange, since no delegate is
				// authorized for a service named "query".
				TargetService: traitFd.Trait.Host,
			})
			zap.L().Info("derived record query plane client from record_read",
				zap.String("service", selfPackage),
				zap.String("plane", traitFd.Trait.S2SService),
				zap.String("host", traitFd.Trait.Host),
				zap.Strings("records", recordReadNames(serviceDesc.RecordReads)))
			// A (spec.record).private record is read over the plane's C2S half
			// instead: record_map.go emits no rpcs for a private type on the s2s
			// half at all, because a peer calls that surface with its own service
			// identity and the owner pin would have nothing to compare a row
			// against. So a reader of a private record needs the SECOND client,
			// alongside the first — a service reading both kinds dials both.
			//
			// Derived only when such a record is actually read: this is a whole
			// extra generated client, and a reader of ordinary records alone must
			// keep emitting exactly what it emitted before.
			//
			// ValidateRecordReadAnnotations refuses a private record read whose
			// plane declares no c2s_service, so the name is non-empty here.
			if readsPrivateRecord(dp, serviceDesc.RecordReads) && traitFd.Trait.C2SService != "" {
				entries = append(entries, tactype.GrpcEntry{
					ProtoFile:     traitPath,
					ServiceName:   traitFd.Trait.C2SService,
					ProtoPackage:  traitFd.Package,
					TargetService: traitFd.Trait.Host,
				})
				zap.L().Info("derived record query plane C2S client from a private record_read",
					zap.String("service", selfPackage),
					zap.String("plane", traitFd.Trait.C2SService),
					zap.String("host", traitFd.Trait.Host))
			}
		}
	}

	return entries, nil
}

// FindRecordPlaneTrait returns the (spec.trait) whose host is a (spec.record_store)
// — the record query plane every (spec.record_read) in a proto set resolves to.
// Returns nil when the set declares no such trait.
//
// Shared with the registry emitter so the derived client and the registry node's
// client_dependencies entry for a record reader name the same plane, package and
// host from ONE lookup. Two independently-maintained resolutions of "which plane
// does a record read call" is exactly the shape that let the registry omit the
// dependency the chassis was already dialing.
//
// Sorted by proto path so a map-ordered caller cannot pick a different trait
// between runs.
func FindRecordPlaneTrait(fds []*parser.FileDescriptor) *parser.FileDescriptor {
	recordStores := make(map[string]bool)
	for _, fd := range fds {
		if fd.RecordStore && fd.ServiceName != "" {
			recordStores[fd.ServiceName] = true
		}
	}
	sorted := make([]*parser.FileDescriptor, len(fds))
	copy(sorted, fds)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	for _, fd := range sorted {
		if fd.Trait == nil || fd.Trait.Host == "" || fd.Trait.S2SService == "" {
			continue
		}
		if !recordStores[fd.Trait.Host] {
			continue
		}
		return fd
	}
	return nil
}

// findAnnotatedSourceFile returns the first non-infra proto file that contains
// a message with the given annotation, along with the file's proto package.
// Returns ("", "") if not found.
func findAnnotatedSourceFile(dp *parser.DescriptorParser, annotation string) (relPath, pkg string) {
	var paths []string
	pkgMap := make(map[string]string)
	for rp, fd := range dp.GetAllFiles() {
		if IsInfraProto(rp) {
			continue
		}
		for _, msg := range fd.Messages {
			if _, ok := msg.Annotations[annotation]; ok {
				paths = append(paths, rp)
				pkgMap[rp] = fd.Package
				break
			}
		}
	}
	if len(paths) == 0 {
		return "", ""
	}
	// Return the first alphabetically for determinism.
	first := paths[0]
	for _, p := range paths[1:] {
		if p < first {
			first = p
		}
	}
	return first, pkgMap[first]
}

// findAnnotatedSourceFileByPkg selects the proto file that has the given annotation
// AND belongs to the package extracted from sourceFilter (e.g. "backendtoken" from
// "backendtoken.BackendTokenEvalInput"). When sourceFilter is empty or no match is
// found, falls back to findAnnotatedSourceFile (alphabetically first).
//
// This is used so that opa_import {source: "pkg.Msg"} consumers get the client
// for the correct vservice package rather than whichever comes first alphabetically.
func findAnnotatedSourceFileByPkg(dp *parser.DescriptorParser, annotation, sourceFilter string) (relPath, pkg string, err error) {
	if sourceFilter == "" {
		rp, p := findAnnotatedSourceFile(dp, annotation)
		return rp, p, nil
	}

	// Extract the package prefix from "package.MessageName".
	pkgFilter := sourceFilter
	if dotIdx := strings.Index(sourceFilter, "."); dotIdx > 0 {
		pkgFilter = sourceFilter[:dotIdx]
	}

	var paths []string
	pkgMap := make(map[string]string)
	for rp, fd := range dp.GetAllFiles() {
		if IsInfraProto(rp) || fd.Package != pkgFilter {
			continue
		}
		for _, msg := range fd.Messages {
			if _, ok := msg.Annotations[annotation]; ok {
				paths = append(paths, rp)
				pkgMap[rp] = fd.Package
				break
			}
		}
	}
	if len(paths) == 0 {
		// The consumer explicitly named a source package. Falling back to the
		// alphabetically-first annotated proto in ANY package would silently wire
		// the client to the WRONG source (a different service's OpaEval). Fail loud
		// so the typo / missing annotation is fixed, not papered over (MS-class
		// fail-open + nondeterministic-source bug).
		return "", "", fmt.Errorf(
			"opa_import source %q names package %q, but no proto in that package declares a message annotated (spec.%s); "+
				"fix the source reference or add the annotation", sourceFilter, pkgFilter, annotation)
	}
	// Return the first alphabetically for determinism.
	first := paths[0]
	for _, p := range paths[1:] {
		if p < first {
			first = p
		}
	}
	return first, pkgMap[first], nil
}

// FindAnnotatedSourceFileFromDir is the exported version of findAnnotatedSourceFile.
// It creates a descriptor parser from inputDir and returns the first source proto
// containing a message with the given annotation, plus the proto package name.
func FindAnnotatedSourceFileFromDir(inputDir, annotation string) (relPath, pkg string) {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return "", ""
	}
	return findAnnotatedSourceFile(dp, annotation)
}

// readsPrivateRecord reports whether any of a service's (spec.record_read)
// declarations names a (spec.record).private record — the one record shape whose
// data path is the query plane's C2S half rather than its s2s sibling.
//
// A record the build cannot resolve is not private here: ValidateRecordReadAnnotations
// has already refused the build with a message naming it, and guessing would only
// derive a client for a plane half nothing reads.
func readsPrivateRecord(dp *parser.DescriptorParser, reads []parser.RecordRead) bool {
	for _, r := range reads {
		idx := strings.LastIndex(r.Record, ".")
		if idx <= 0 {
			continue
		}
		pkg, name := r.Record[:idx], r.Record[idx+1:]
		for _, fd := range dp.GetAllFiles() {
			if fd.Package != pkg {
				continue
			}
			for mi := range fd.Messages {
				msg := &fd.Messages[mi]
				if msg.Name == name && msg.Annotations["record_private"].BoolValue {
					return true
				}
			}
		}
	}
	return false
}

// recordReadNames renders a service's (spec.record_read) declarations as the
// record names alone, for the log line above.
func recordReadNames(reads []parser.RecordRead) []string {
	out := make([]string, 0, len(reads))
	for _, r := range reads {
		out = append(out, r.Record)
	}
	return out
}
