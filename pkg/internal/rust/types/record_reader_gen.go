// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	"go.uber.org/zap"
)

// recordReaderEntry is one (spec.record_read) declaration rendered for the
// service_manager template: the typed reader this service gets over ONE record
// type's stored rows.
//
// The reader exists because the plane it wraps is a three-call lifecycle
// (Create<T>Query -> List<T> -> Delete<T>Query) and the two questions a reader
// actually asks are "give me this row" and "give me the rows matching this".
// Leaving the lifecycle to hand-written code would mean every consumer
// re-deriving it, and each one owning the same failure mode: a Create whose
// handle is never Deleted when the List between them fails leaks a server-side
// query per failed read. The generated reader closes the handle on both paths.
type recordReaderEntry struct {
	FnName         string // SM method, e.g. "message_record_reader"
	StructName     string // reader type, e.g. "MessageRecordReader"
	RecordFQN      string // e.g. "messaging.Message"
	RecordName     string // e.g. "Message"
	RecordPath     string // Rust path to the record type
	QueryPath      string // Rust path to the plane's generated query module
	HostAccessor   string // GrpcClients accessor for the plane host, e.g. "stratum"
	ClientFn       string // client accessor on the host, e.g. "record_map_client"
	KeyField       string // record key field name, e.g. "delivery_id"
	KeyRustType    string // Rust type of the key field, e.g. "String"
	KeyIsString    bool
	CreateFn       string // e.g. "create_message_query"
	ListFn         string // e.g. "list_message_page"
	DeleteFn       string // e.g. "delete_message_query"
	FilterType     string // e.g. "MessageFilter"
	OpenReqType    string // e.g. "OpenMessageQueryRequest"
	PullReqType    string // e.g. "PullMessageRequest"
	HandleType     string // "QueryHandle"
	HasKeyFilter   bool   // whether the key reached <T>Filter (point read is possible)
	HasDeletedFlag bool   // whether the record declares (spec.deleted_flag), so reads can opt into flagged rows
	ClientModPath  string // module path under internal/grpc/clients, e.g. "vservices::query::record_map"
	ClientStruct   string // e.g. "RecordMapGrpcClient"
}

// recordReaderFile is the template data for record_readers.rs: the readers plus
// the one plane client they all borrow.
type recordReaderFile struct {
	RecordReaders []recordReaderEntry
	QueryPathRoot string // Rust path to the plane's generated query types
	ClientModPath string // module path under internal/grpc/clients, e.g. "vservices::query::record_map"
	ClientStruct  string // e.g. "RecordMapGrpcClient"
	HandleType    string // "QueryHandle"
}

// generateRecordReaders writes s2sm/record_readers.rs when this service declares
// any (spec.record_read). No declaration, no file — the mechanism is additive and
// every service in the fleet predates it.
func generateRecordReaders(config *tactype.Config, entries []recordReaderEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tmplContent, err := loadTemplate("record_readers.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load record_readers template: %w", err)
	}
	tmpl, err := template.New("recordreaders").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse record_readers template: %w", err)
	}
	data := recordReaderFile{
		RecordReaders: entries,
		QueryPathRoot: entries[0].QueryPath,
		ClientModPath: entries[0].ClientModPath,
		ClientStruct:  entries[0].ClientStruct,
		HandleType:    entries[0].HandleType,
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute record_readers template: %w", err)
	}
	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create record_readers dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "record_readers.rs")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write record_readers.rs: %w", err)
	}
	zap.L().Info("Generated Rust record readers",
		zap.String("path", outputPath), zap.Int("records", len(entries)))
	return nil
}

// collectRecordReaders resolves this service's (spec.record_read) declarations
// into template entries.
//
// Everything except the record NAME is derived: the plane's host, its S2S
// service, the client accessor, the filter type and the key field all come off
// the (spec.trait) and the (spec.record), so a reader's proto names one thing and
// cannot disagree with the surface it calls. Returns nil when the service
// declares no reads, which is every service in the fleet that predates the field.
func collectRecordReaders(config *tactype.Config, protoDir string) ([]recordReaderEntry, error) {
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to parse protos for record reader generation: %w", err)
	}

	// Sorted for the same reason the trait scan below is: GetAllFiles is a map,
	// a package may carry option (spec.service) in more than one file, and
	// RecordReads is file-scoped — so the raw-map "first match" could emit a
	// different reader set on each build.
	var self *parser.FileDescriptor
	selfPaths := make([]string, 0, len(dp.GetAllFiles()))
	for relPath := range dp.GetAllFiles() {
		selfPaths = append(selfPaths, relPath)
	}
	sort.Strings(selfPaths)
	for _, relPath := range selfPaths {
		fd := dp.GetAllFiles()[relPath]
		if fd.ServiceName != "" && fd.Package == config.SelfPackage {
			self = fd
			break
		}
	}
	if self == nil || len(self.RecordReads) == 0 {
		return nil, nil
	}

	// The plane: the trait whose host declares (spec.record_store). Resolved the
	// same way collectRecordQueryPlanes does, from the declaration rather than
	// from any constant.
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
		// ValidateRecordReadAnnotations has already refused this build with a
		// message naming the missing plane; emitting nothing here avoids replacing
		// that message with a worse one from a template.
		return nil, nil
	}

	queryPath := rustModulePath(traitPath, traitFd.Package)
	hostAccessor := strutil.ToSnakeCase(strings.ReplaceAll(traitFd.Trait.Host, "-", "_"))
	clientFn := strutil.ToSnakeCase(traitFd.Trait.S2SService) + "_client"
	// The generated client module mirrors the trait proto's own location:
	// internal/grpc/clients/<parent>/<pkg>/<service_snake>.rs
	clientModParts := []string{}
	if mod := rustParentMod(firstPathSegment(traitPath)); mod != "" {
		clientModParts = append(clientModParts, mod)
	}
	clientModParts = append(clientModParts, traitFd.Package, strutil.ToSnakeCase(traitFd.Trait.S2SService))
	clientModPath := strings.Join(clientModParts, "::")

	var entries []recordReaderEntry
	for _, rr := range self.RecordReads {
		fqn := rr.Record
		rec, recPath, recFd := findRecordByFQN(dp, fqn)
		if rec == nil {
			// Refused by ValidateRecordReadAnnotations; see above.
			continue
		}
		key := model.DefaultRecordKeyField
		if k, ok := rec.Annotations["record_key"]; ok && k.StringValue != "" {
			key = k.StringValue
		}
		keyRust, isString, hasKey := recordKeyRustType(rec, key)
		// Open<T>QueryRequest carries include_deleted for every record, but only a
		// record with a (spec.deleted_flag) field has rows the flag can include, so
		// only such a reader exposes the parameter.
		_, hasDeletedFlag := parser.DeletedFlagField(rec)
		name := rec.Name
		snake := strutil.ToSnakeCase(name)
		entries = append(entries, recordReaderEntry{
			FnName:         snake + "_record_reader",
			StructName:     name + "RecordReader",
			RecordFQN:      fqn,
			RecordName:     name,
			RecordPath:     rustModulePath(recPath, recFd.Package) + "::" + name,
			QueryPath:      queryPath,
			HostAccessor:   hostAccessor,
			ClientFn:       clientFn,
			KeyField:       key,
			KeyRustType:    keyRust,
			KeyIsString:    isString,
			CreateFn:       "create_" + snake + "_query",
			ListFn:         "list_" + snake + "_page",
			DeleteFn:       "delete_" + snake + "_query",
			FilterType:     name + "Filter",
			OpenReqType:    "Open" + name + "QueryRequest",
			PullReqType:    "Pull" + name + "Request",
			HandleType:     "QueryHandle",
			HasKeyFilter:   hasKey,
			HasDeletedFlag: hasDeletedFlag,
			ClientModPath:  clientModPath,
			ClientStruct:   traitFd.Trait.S2SService + "GrpcClient",
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RecordFQN < entries[j].RecordFQN })
	return entries, nil
}

// findRecordByFQN locates a (spec.record) message by "package.Message".
func findRecordByFQN(dp *parser.DescriptorParser, fqn string) (*parser.MessageDescriptor, string, *parser.FileDescriptor) {
	idx := strings.LastIndex(fqn, ".")
	if idx <= 0 {
		return nil, "", nil
	}
	pkg, name := fqn[:idx], fqn[idx+1:]
	var relPaths []string
	for rp := range dp.GetAllFiles() {
		relPaths = append(relPaths, rp)
	}
	sort.Strings(relPaths)
	for _, rp := range relPaths {
		fd := dp.GetAllFiles()[rp]
		if fd.Package != pkg {
			continue
		}
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if msg.Name != name {
				continue
			}
			if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
				return nil, "", nil
			}
			return msg, rp, fd
		}
	}
	return nil, "", nil
}

// recordKeyRustType returns the Rust type of a record's key field and whether it
// is a string, plus whether the key is usable as a filter criterion at all.
//
// The key reaches <T>Filter only when the record declares (spec.record).s2s_read
// — that is what folds it in (record_map.go's appendKeyFilterCriterion) — and
// only when it is scalar. A key that is not is not a point-read key, and the
// generated reader omits get() rather than emitting a method that cannot compile.
func recordKeyRustType(msg *parser.MessageDescriptor, key string) (rustType string, isString bool, ok bool) {
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if f.Name != key {
			continue
		}
		if f.IsRepeated || f.IsMap || f.TypeKind != "scalar" {
			return "", false, false
		}
		switch f.Type {
		case "string":
			return "String", true, true
		case "int64", "sint64", "sfixed64":
			return "i64", false, true
		case "int32", "sint32", "sfixed32":
			return "i32", false, true
		case "uint64", "fixed64":
			return "u64", false, true
		case "uint32", "fixed32":
			return "u32", false, true
		case "bool":
			return "bool", false, true
		default:
			return "", false, false
		}
	}
	return "", false, false
}

// rustModulePath renders the crate::types::<parent>::<pkg> module path for a
// proto file, matching the convention rustEventTypePath uses.
func rustModulePath(relPath, pkg string) string {
	parts := []string{"crate::types"}
	if mod := rustParentMod(firstPathSegment(relPath)); mod != "" {
		parts = append(parts, mod)
	}
	parts = append(parts, pkg)
	return strings.Join(parts, "::")
}
