// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
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
		var traitFd *parser.FileDescriptor
		var traitPath string
		recordStores := make(map[string]bool)
		for _, fd := range dp.GetAllFiles() {
			if fd.RecordStore && fd.ServiceName != "" {
				recordStores[fd.ServiceName] = true
			}
		}
		var paths []string
		for rp := range dp.GetAllFiles() {
			paths = append(paths, rp)
		}
		sort.Strings(paths)
		for _, rp := range paths {
			fd := dp.GetAllFiles()[rp]
			if fd.Trait == nil || fd.Trait.Host == "" || fd.Trait.S2SService == "" {
				continue
			}
			if !recordStores[fd.Trait.Host] {
				continue
			}
			traitFd = fd
			traitPath = rp
			break
		}
		// No plane to name. ValidateRecordReadAnnotations has already refused this
		// build with a message that says so; returning an error here too would
		// only replace that message with a worse one.
		if traitFd != nil {
			entries = append(entries, tactype.GrpcEntry{
				ProtoFile:    traitPath,
				ServiceName:  traitFd.Trait.S2SService,
				ProtoPackage: traitFd.Package,
			})
			zap.L().Info("derived record query plane client from record_read",
				zap.String("service", selfPackage),
				zap.String("plane", traitFd.Trait.S2SService),
				zap.String("host", traitFd.Trait.Host),
				zap.Strings("records", recordReadNames(serviceDesc.RecordReads)))
		}
	}

	return entries, nil
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

// recordReadNames renders a service's (spec.record_read) declarations as the
// record names alone, for the log line above.
func recordReadNames(reads []parser.RecordRead) []string {
	out := make([]string, 0, len(reads))
	for _, r := range reads {
		out = append(out, r.Record)
	}
	return out
}
