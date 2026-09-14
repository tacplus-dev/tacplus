// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/template"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	"go.uber.org/zap"
)

// The GENERATED handler behind the client-facing record read plane.
//
// The author writes nothing for <Svc>Records. Its two RPCs per record have one
// correct body, that body is the whole of gates 2 and 3, and leaving it to
// hand-written code would put the tenant pin and the row scope on the far side
// of a review rather than on the far side of a generator.
//
// WHAT THE BODY DOES, IN ORDER — and the order is the design:
//
//  1. Resolve the caller's namespace claim. Absent is UNAUTHENTICATED, not an
//     empty filter. Same expression the (spec.store_namespace) egress filter
//     compares against, so the two can never disagree.
//  2. Resolve the caller's subject, the same way the (spec.store_principal)
//     egress filter does, for the same reason.
//  3. Copy the client's <T>ReadFilter into the plane's <T>Filter and OVERWRITE
//     the principal and namespace criteria with the two values above. The client
//     cannot observe the overwrite, because it has no field to put either in.
//  4. Ask the store, through the already-generated typed reader — which is the
//     single largest piece of reuse here: the Create/List/Delete handle
//     lifecycle, released on both the success and the failure path, is not
//     rewritten for the client-facing plane.
//
// The response then passes back out through the generated wrapper's egress
// filters, which drop a foreign principal's row and ABORT on a foreign tenant's.
// That abort is the backstop behind step 3, not the client's gate: because the
// request carries no namespace field, no client input can reach it.

// recordC2SMethod is one record type's pair of generated RPCs.
type recordC2SMethod struct {
	RecordName    string // "ScottyActivity"
	RecordFQN     string // "scotty.ScottyActivity"
	GetFn         string // "get_scotty_activity"
	ListFn        string // "get_scotty_activity_page"
	GetReq        string // "GetScottyActivityRequest"
	GetResp       string // "GetScottyActivityResponse"
	ListReq       string // "GetScottyActivityPageRequest"
	ListResp      string // "GetScottyActivityPageResponse"
	FilterType    string // plane filter, "ScottyActivityFilter"
	ReaderFn      string // "scotty_activity_record_reader"
	KeyField      string // "activity_id"
	KeyIsString   bool
	PrincipalPin  string // <T>Filter field pinned to the caller's subject
	NamespacePin  string // <T>Filter field pinned to the caller's namespace
	Criteria      []recordC2SCriterion
	GetMethodName string // "GetScottyActivity", for status text
	ListMethodNam string // "GetScottyActivityPage", for status text
}

// recordC2SCriterion is one criterion copied straight through from the client's
// filter to the plane's, unchanged. Both are `optional`, so both are Option<T>
// on the Rust side and the copy is a move.
type recordC2SCriterion struct {
	Field string
}

// recordC2SFile is the template data for s2sm/record_c2s.rs.
type recordC2SFile struct {
	StructName  string // "ScottyRecordsGeneratedHandler"
	TraitPath   string // module path of the generated handler trait
	TraitName   string // "ScottyRecordsHandler"
	TypesPath   string // reader package's generated types, for the request/response types
	QueryPath   string // the plane's generated types, for <T>Filter
	Methods     []recordC2SMethod
	ServiceName string // "ScottyRecords"
	SelfPackage string
}

// collectRecordC2SPlane resolves this service's (spec.record_read).c2s
// declarations into the generated handler's template data, or nil when it
// declares none — which is every service that predates the field.
func collectRecordC2SPlane(config *tactype.Config, protoDir string) (*recordC2SFile, error) {
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to parse protos for the client-facing record read plane: %w", err)
	}

	// Sorted for the same reason the trait scan below is: GetAllFiles is a map,
	// a package may carry option (spec.service) in more than one file, and
	// RecordReads is file-scoped — so the raw-map "first match" could emit a
	// different C2S plane on each build.
	var self *parser.FileDescriptor
	var selfPath string
	selfPaths := make([]string, 0, len(dp.GetAllFiles()))
	for relPath := range dp.GetAllFiles() {
		selfPaths = append(selfPaths, relPath)
	}
	sort.Strings(selfPaths)
	for _, relPath := range selfPaths {
		fd := dp.GetAllFiles()[relPath]
		if fd.ServiceName != "" && fd.Package == config.SelfPackage {
			self = fd
			selfPath = relPath
			break
		}
	}
	if self == nil {
		return nil, nil
	}
	var decls []parser.RecordRead
	for _, rr := range self.RecordReads {
		if rr.C2S != nil {
			decls = append(decls, rr)
		}
	}
	if len(decls) == 0 {
		return nil, nil
	}
	sort.Slice(decls, func(i, j int) bool { return decls[i].Record < decls[j].Record })

	// The plane's own types live in the trait's proto — resolved from the
	// declaration exactly as collectRecordReaders resolves it, never from a
	// constant.
	recordStores := make(map[string]bool)
	for _, fd := range dp.GetAllFiles() {
		if fd.RecordStore && fd.ServiceName != "" {
			recordStores[fd.ServiceName] = true
		}
	}
	var traitFd *parser.FileDescriptor
	var traitPath string
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
		if recordStores[fd.Trait.Host] {
			traitFd = fd
			traitPath = rp
			break
		}
	}
	if traitFd == nil {
		// Refused by ValidateRecordReadAnnotations already; emitting nothing here
		// keeps that message rather than replacing it with a template failure.
		return nil, nil
	}

	svcName := strutil.ToPascalCase(self.ServiceName) + "Records"
	parentMod := rustParentMod(firstPathSegment(selfPath))
	wrapperMod := strutil.StripPackagePrefix(strutil.ToSnakeCase(svcName), self.Package)

	out := &recordC2SFile{
		StructName:  svcName + "GeneratedHandler",
		TraitName:   svcName + "Handler",
		TraitPath:   fmt.Sprintf("super::internal::grpc::servers::%s::%s::%s", parentMod, self.Package, wrapperMod),
		TypesPath:   rustModulePath(selfPath, self.Package),
		QueryPath:   rustModulePath(traitPath, traitFd.Package),
		ServiceName: svcName,
		SelfPackage: config.SelfPackage,
	}

	for _, decl := range decls {
		rec, _, _ := findRecordByFQN(dp, decl.Record)
		if rec == nil {
			continue
		}
		key := model.DefaultRecordKeyField
		if k, ok := rec.Annotations["record_key"]; ok && k.StringValue != "" {
			key = k.StringValue
		}
		_, keyIsString, _ := recordKeyRustType(rec, key)

		principal := ""
		namespace := ""
		for i := range rec.Fields {
			f := &rec.Fields[i]
			if principal == "" && f.Annotations["store_principal"].BoolValue {
				principal = f.Name
			}
			if namespace == "" && f.Annotations["store_namespace"].BoolValue {
				namespace = f.Name
			}
		}
		if principal == "" {
			principal = rec.Annotations["record_owner"].StringValue
		}

		// The criteria the client may supply are exactly the ones the injector put
		// on <T>ReadFilter: every EQ/RANGE (spec.query_field) except the two pinned
		// fields, plus the record key. Recomputed here from the same annotations
		// rather than plumbed across the two generators, so a criterion cannot
		// appear on one side and not the other.
		var criteria []recordC2SCriterion
		haveKey := false
		for i := range rec.Fields {
			f := &rec.Fields[i]
			if f.Name == principal || f.Name == namespace {
				continue
			}
			qf, ok := f.Annotations["query_field"]
			if !ok {
				continue
			}
			if f.Name == key {
				haveKey = true
			}
			if qf.StringValue == "range" {
				criteria = append(criteria,
					recordC2SCriterion{Field: prostSnakeName(f.Name + "_from")},
					recordC2SCriterion{Field: prostSnakeName(f.Name + "_to")})
				continue
			}
			criteria = append(criteria, recordC2SCriterion{Field: prostSnakeName(f.Name)})
		}
		if !haveKey && key != principal && key != namespace {
			criteria = append(criteria, recordC2SCriterion{Field: prostSnakeName(key)})
		}

		snake := strutil.ToSnakeCase(rec.Name)
		out.Methods = append(out.Methods, recordC2SMethod{
			RecordName:    rec.Name,
			RecordFQN:     decl.Record,
			GetFn:         "get_" + snake,
			ListFn:        "get_" + snake + "_page",
			GetReq:        "Get" + rec.Name + "Request",
			GetResp:       "Get" + rec.Name + "Response",
			ListReq:       "Get" + rec.Name + "PageRequest",
			ListResp:      "Get" + rec.Name + "PageResponse",
			FilterType:    rec.Name + "Filter",
			ReaderFn:      snake + "_record_reader",
			KeyField:      prostSnakeName(key),
			KeyIsString:   keyIsString,
			PrincipalPin:  prostSnakeName(principal),
			NamespacePin:  prostSnakeName(namespace),
			Criteria:      criteria,
			GetMethodName: "Get" + rec.Name,
			ListMethodNam: "Get" + rec.Name + "Page",
		})
	}
	if len(out.Methods) == 0 {
		return nil, nil
	}
	return out, nil
}

// generateRecordC2SPlane writes s2sm/record_c2s.rs when this service declares
// any (spec.record_read).c2s. No declaration, no file.
func generateRecordC2SPlane(config *tactype.Config, data *recordC2SFile) error {
	if data == nil {
		return nil
	}
	tmplContent, err := loadTemplate("record_c2s.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load record_c2s template: %w", err)
	}
	tmpl, err := template.New("recordc2s").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse record_c2s template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute record_c2s template: %w", err)
	}
	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create record_c2s dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "record_c2s.rs")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write record_c2s.rs: %w", err)
	}
	zap.L().Info("Generated the client-facing record read plane handler",
		zap.String("path", outputPath), zap.Int("records", len(data.Methods)))
	return nil
}

// recordC2SServiceName is the synthesized service <Svc>Records for a package, or
// "" when the service declares no client-facing read. The service_manager and
// handler templates use it to tell the ONE service whose implementation is
// generated apart from every service whose implementation the author supplies.
func recordC2SServiceName(data *recordC2SFile) string {
	if data == nil {
		return ""
	}
	return data.ServiceName
}
