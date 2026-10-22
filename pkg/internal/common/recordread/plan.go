// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package recordread resolves a service's (spec.record_read) declarations into
// the language-neutral facts every typed record reader is generated from: the
// record, the plane it is read over, which half of the plane, and the key.
//
// Everything except the record NAME is derived: the plane's host, its S2S and
// C2S services, the key field and its type all come off the (spec.trait) and
// the (spec.record), so a reader's proto names one thing and cannot disagree
// with the surface it calls. The per-language generators map these facts to
// their own type and accessor names and nothing else.
package recordread

import (
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// Reader is one (spec.record_read) declaration, resolved.
type Reader struct {
	RecordFQN     string // e.g. "messaging.Message"
	RecordName    string // e.g. "Message"
	RecordPackage string // proto package of the record, e.g. "messaging"
	RecordRelPath string // proto file of the record, e.g. "services/messaging.proto"
	RecordFile    *parser.FileDescriptor

	TraitPackage string // proto package of the plane's trait, e.g. "query"
	TraitRelPath string // proto file of the trait, e.g. "vservices/query.proto"
	Host         string // routing label of the plane's host, e.g. "stratum"

	// PlaneService is the plane service this reader dials: the trait's S2S
	// service for an ordinary record, its C2S service for a (spec.record).private
	// record. record_map.go emits NO rpcs for a private type on the s2s half (a
	// peer calls that surface with its own identity, so the owner pin has nothing
	// to compare a row against) and keeps its full lifecycle on the c2s half,
	// which carries the delegated user token the pin needs.
	PlaneService string

	KeyField     string // record key field name, e.g. "delivery_id"
	KeyProtoType string // proto scalar type of the key, e.g. "string"; "" when not a point-read key
	// HasKeyFilter reports whether the key is usable as a filter criterion: a
	// singular scalar. A key that is not is not a point-read key, and readers
	// omit get() rather than emitting a method that cannot compile.
	HasKeyFilter bool
	// HasDeletedFlag reports whether the record declares (spec.deleted_flag),
	// so reads can opt into flagged rows. Open<T>QueryRequest carries
	// include_deleted for every record, but only such a reader exposes it.
	HasDeletedFlag bool
}

// Collect resolves selfPackage's (spec.record_read) declarations. It returns
// nil when the service declares no reads, or when the plane or a record cannot
// be resolved: ValidateRecordReadAnnotations has already refused those builds
// with a message naming the problem, and emitting nothing avoids replacing it
// with a worse one from a template.
func Collect(dp *parser.DescriptorParser, selfPackage string) []Reader {
	all := dp.GetAllFiles()
	// GetAllFiles is a map, a package may carry option (spec.service) in more
	// than one file, and RecordReads is file-scoped: iterate in path order so
	// the same build always emits the same reader set.
	paths := make([]string, 0, len(all))
	for rp := range all {
		paths = append(paths, rp)
	}
	sort.Strings(paths)

	var self *parser.FileDescriptor
	for _, rp := range paths {
		fd := all[rp]
		if fd.ServiceName != "" && fd.Package == selfPackage {
			self = fd
			break
		}
	}
	if self == nil || len(self.RecordReads) == 0 {
		return nil
	}

	// The plane: the trait whose host declares (spec.record_store).
	recordStores := make(map[string]bool)
	for _, fd := range all {
		if fd.RecordStore && fd.ServiceName != "" {
			recordStores[fd.ServiceName] = true
		}
	}
	var trait *parser.FileDescriptor
	var traitPath string
	for _, rp := range paths {
		fd := all[rp]
		if fd.Trait == nil || fd.Trait.Host == "" || fd.Trait.S2SService == "" {
			continue
		}
		if recordStores[fd.Trait.Host] {
			trait, traitPath = fd, rp
			break
		}
	}
	if trait == nil {
		return nil
	}

	var readers []Reader
	for _, rr := range self.RecordReads {
		rec, recPath, recFd := FindRecord(dp, rr.Record)
		if rec == nil {
			continue
		}
		key := model.DefaultRecordKeyField
		if k, ok := rec.Annotations["record_key"]; ok && k.StringValue != "" {
			key = k.StringValue
		}
		keyType, hasKey := ScalarKeyType(rec, key)
		_, hasDeletedFlag := parser.DeletedFlagField(rec)
		planeService := trait.Trait.S2SService
		if rec.Annotations["record_private"].BoolValue {
			planeService = trait.Trait.C2SService
		}
		readers = append(readers, Reader{
			RecordFQN:      rr.Record,
			RecordName:     rec.Name,
			RecordPackage:  recFd.Package,
			RecordRelPath:  recPath,
			RecordFile:     recFd,
			TraitPackage:   trait.Package,
			TraitRelPath:   traitPath,
			Host:           trait.Trait.Host,
			PlaneService:   planeService,
			KeyField:       key,
			KeyProtoType:   keyType,
			HasKeyFilter:   hasKey,
			HasDeletedFlag: hasDeletedFlag,
		})
	}
	sort.Slice(readers, func(i, j int) bool { return readers[i].RecordFQN < readers[j].RecordFQN })
	return readers
}

// FindRecord locates a (spec.record) message by "package.Message". It returns
// nil when the message does not exist or is not a record.
func FindRecord(dp *parser.DescriptorParser, fqn string) (*parser.MessageDescriptor, string, *parser.FileDescriptor) {
	idx := strings.LastIndex(fqn, ".")
	if idx <= 0 {
		return nil, "", nil
	}
	pkg, name := fqn[:idx], fqn[idx+1:]
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for rp := range all {
		paths = append(paths, rp)
	}
	sort.Strings(paths)
	for _, rp := range paths {
		fd := all[rp]
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

// ScalarKeyType returns the proto type of a record's key field and whether the
// key is usable as a point-read criterion: a singular scalar of one of the
// types the plane's <T>Filter can carry.
func ScalarKeyType(msg *parser.MessageDescriptor, key string) (string, bool) {
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if f.Name != key {
			continue
		}
		if f.IsRepeated || f.IsMap || f.TypeKind != "scalar" {
			return "", false
		}
		switch f.Type {
		case "string", "int64", "sint64", "sfixed64", "int32", "sint32", "sfixed32",
			"uint64", "fixed64", "uint32", "fixed32", "bool":
			return f.Type, true
		}
		return "", false
	}
	return "", false
}

// C2SMethod is one record type's pair of generated client-facing RPCs on the
// synthesized <Svc>Records service: Get<T> (point read by key) and
// Get<T>Page (paged read of the caller's own rows).
type C2SMethod struct {
	RecordName   string // "ScottyActivity"
	RecordFQN    string // "scotty.ScottyActivity"
	KeyField     string // proto name of the record key, e.g. "activity_id"
	KeyProtoType string // proto scalar type of the key
	PrincipalPin string // <T>Filter field pinned to the caller's subject
	NamespacePin string // <T>Filter field pinned to the caller's namespace
	// Criteria are the <T>Filter fields the client may supply, copied straight
	// through from its <T>ReadFilter: every EQ/RANGE (spec.query_field) except
	// the two pinned fields (a RANGE field contributes <f>_from and <f>_to),
	// plus the record key. Recomputed from the same annotations the injector
	// used rather than plumbed across generators, so a criterion cannot appear
	// on one side and not the other.
	Criteria []string
}

// C2SPlane is the generated handler's plan for the client-facing record read
// plane: the synthesized service and one method pair per (spec.record_read).c2s.
type C2SPlane struct {
	ServiceName  string // "ScottyRecords"
	SelfPackage  string
	SelfRelPath  string
	SelfFile     *parser.FileDescriptor
	TraitPackage string
	TraitRelPath string
	Methods      []C2SMethod
}

// CollectC2SPlane resolves selfPackage's (spec.record_read).c2s declarations,
// or returns nil when it declares none, which is every service that predates
// the field. Unresolvable declarations were refused by
// ValidateRecordReadAnnotations already and are skipped here.
func CollectC2SPlane(dp *parser.DescriptorParser, selfPackage string) *C2SPlane {
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for rp := range all {
		paths = append(paths, rp)
	}
	sort.Strings(paths)

	var self *parser.FileDescriptor
	var selfPath string
	for _, rp := range paths {
		fd := all[rp]
		if fd.ServiceName != "" && fd.Package == selfPackage {
			self, selfPath = fd, rp
			break
		}
	}
	if self == nil {
		return nil
	}
	var decls []parser.RecordRead
	for _, rr := range self.RecordReads {
		if rr.C2S != nil {
			decls = append(decls, rr)
		}
	}
	if len(decls) == 0 {
		return nil
	}
	sort.Slice(decls, func(i, j int) bool { return decls[i].Record < decls[j].Record })

	recordStores := make(map[string]bool)
	for _, fd := range all {
		if fd.RecordStore && fd.ServiceName != "" {
			recordStores[fd.ServiceName] = true
		}
	}
	var trait *parser.FileDescriptor
	var traitPath string
	for _, rp := range paths {
		fd := all[rp]
		if fd.Trait == nil || fd.Trait.Host == "" || fd.Trait.S2SService == "" {
			continue
		}
		if recordStores[fd.Trait.Host] {
			trait, traitPath = fd, rp
			break
		}
	}
	if trait == nil {
		return nil
	}

	plane := &C2SPlane{
		ServiceName:  strutil.ToPascalCase(self.ServiceName) + "Records",
		SelfPackage:  selfPackage,
		SelfRelPath:  selfPath,
		SelfFile:     self,
		TraitPackage: trait.Package,
		TraitRelPath: traitPath,
	}
	for _, decl := range decls {
		rec, _, _ := FindRecord(dp, decl.Record)
		if rec == nil {
			continue
		}
		key := model.DefaultRecordKeyField
		if k, ok := rec.Annotations["record_key"]; ok && k.StringValue != "" {
			key = k.StringValue
		}
		keyType, _ := ScalarKeyType(rec, key)
		principal, namespace := "", ""
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
		var criteria []string
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
				criteria = append(criteria, f.Name+"_from", f.Name+"_to")
				continue
			}
			criteria = append(criteria, f.Name)
		}
		if !haveKey && key != principal && key != namespace {
			criteria = append(criteria, key)
		}
		plane.Methods = append(plane.Methods, C2SMethod{
			RecordName:   rec.Name,
			RecordFQN:    decl.Record,
			KeyField:     key,
			KeyProtoType: keyType,
			PrincipalPin: principal,
			NamespacePin: namespace,
			Criteria:     criteria,
		})
	}
	if len(plane.Methods) == 0 {
		return nil
	}
	return plane
}
