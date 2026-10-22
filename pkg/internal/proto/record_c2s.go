// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"go.uber.org/zap"
)

// The CLIENT-FACING record read plane.
//
// One synthesized sibling service per reading service, emitted into that
// service's OWN proto from (spec.record_read).c2s:
//
//	// tacplus:generated-service recordc2s
//	service <Svc>Records {
//	  option (spec.service_c2s) = {auth_entitled_users: true};
//	  rpc Get<T>(Get<T>Request)         returns (Get<T>Response);
//	  rpc Get<T>Page(Get<T>PageRequest) returns (Get<T>PageResponse);
//	}
//
// WHY IT IS NOT THE RECORD PLANE'S c2s HALF. The trait's RecordQuery lives on
// the record STORE and is entitled against the trait, so one audience covers
// every record type in the fleet and a grant widened for one widens over data
// whose owner was never consulted. This surface lives on the service that chose
// to disclose the rows, so its grants land in that service's own catalog
// (SCOTTY_LIST_SCOTTYACTIVITY, and the package's _READ_ANY/_PAGE_ANY wildcards)
// by generation, with nothing to write. That is the whole of gate 1.
//
// A SIBLING SERVICE, not RPCs appended to the reader's own. A service declares
// exactly one caller surface (parser.ValidateServiceSurfaces) but a package may
// host several, so a separate service means a generated RPC can never collide
// with a hand-written one and the `tacplus:generated-service` sentinel covers a
// whole surface rather than individual methods. Exactly how the record plane
// already emits two siblings from one trait.
//
// TWO RPCS, AND THAT IS THE POINT. No Create/Update/Delete<T>Query: the query
// handle's lifecycle is a store-internal protocol, and handing it to a client
// gives a mobile app a server-side resource it can leak and a predicate seam
// only a wrapper pin keeps honest. The generated handler drives that lifecycle
// internally through the typed s2s reader, which already releases the handle on
// both the success and the failure path. No Watch<T> and no Get<T>Count in v1 —
// see the design note; both are decisions, not omissions.
//
// WHY THE REQUEST CARRIES ITS OWN FILTER TYPE rather than the plane's <T>Filter:
//
//   - <T>Filter lives in the TRAIT's proto, which already imports the record's
//     proto. A reader importing it back would be an import cycle.
//   - <T>Filter carries the principal and namespace criteria, and the whole
//     point of this surface is that a client cannot express either. A generated
//     request message with no such field beats a stamp-or-refuse check: an
//     absent field cannot be forged, mistyped, or reasoned about wrongly.
//
// So <T>ReadFilter is <T>Filter MINUS those two criteria, in the reader's own
// package, and the generated handler is what turns one into the other.

// recordC2SRead is one record type a service asked for a client-facing read of.
type recordC2SRead struct {
	recordFQN  string // "scotty.ScottyActivity"
	recordName string // "ScottyActivity"
	recordPath string // proto file declaring the record
	key        string // record key field name
	keyType    string // proto scalar type of the key
	scope      string // C2SScope value name
	exposure   string // RpcExposure value name
	// criteria are the criteria a CLIENT may supply: the record's declared
	// (spec.query_field)s plus the record key, minus the principal and namespace
	// fields, which the server pins and the client cannot name.
	criteria []recordFilter
}

// AppendRecordC2SServiceToProtos emits the client-facing record read plane into
// the proto of every service that declares (spec.record_read).c2s.
//
// Runs on EVERY service's codegen, like the other injectors, so the synthesized
// service is visible in every proto view — a peer declaring a c2s client against
// it has to be able to see it. The injection does not depend on who is building,
// so selfPackage is unused.
//
// Emits nothing when no service declares the c2s block, which is every service
// in the fleet that predates it.
func AppendRecordC2SServiceToProtos(tempProtoDir, inputDir, selfPackage string) error {
	_ = inputDir
	_ = selfPackage
	dp, err := parser.NewDescriptorParser(tempProtoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for record c2s injection: %w", err)
	}

	// Index every record once: the reader names a record by FQN and everything
	// else — key, criteria, principal and namespace fields — is read off the
	// record itself, never restated by the reader.
	type recordInfo struct {
		msg      *parser.MessageDescriptor
		relPath  string
		pkg      string
		key      string
		keyType  string
		criteria []recordFilter
	}
	records := make(map[string]*recordInfo)
	for relPath, fd := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
				continue
			}
			key := defaultRecordKeyField
			if k, ok := msg.Annotations["record_key"]; ok && k.StringValue != "" {
				key = k.StringValue
			}
			info := &recordInfo{msg: msg, relPath: relPath, pkg: fd.Package, key: key}
			// The two SERVER-PINNED fields, excluded from the client's filter — not
			// validated on it, not stamped into it: absent from the generated
			// message entirely, so there is nothing to forge.
			//
			// The principal is resolved exactly as the egress row filter and
			// recordPrincipalCriterion resolve it, most specific first —
			// (spec.store_principal) on a field, else (spec.record).owner. Only the
			// field that actually WINS is excluded: on a record carrying
			// store_principal, the declared `owner` is some other field (on
			// ScottyActivity it is the publishing service's spiffe_id) and stays a
			// perfectly ordinary criterion a client may narrow by.
			principalField := ""
			namespaceField := ""
			for fi := range msg.Fields {
				f := &msg.Fields[fi]
				if principalField == "" && f.Annotations["store_principal"].BoolValue {
					principalField = f.Name
				}
				if namespaceField == "" && f.Annotations["store_namespace"].BoolValue {
					namespaceField = f.Name
				}
			}
			if principalField == "" {
				principalField = msg.Annotations["record_owner"].StringValue
			}
			pinned := map[string]bool{}
			if principalField != "" {
				pinned[principalField] = true
			}
			if namespaceField != "" {
				pinned[namespaceField] = true
			}
			for fi := range msg.Fields {
				f := &msg.Fields[fi]
				if f.Name == key {
					info.keyType = f.Type
				}
				if pinned[f.Name] {
					continue
				}
				qf, ok := f.Annotations["query_field"]
				if !ok {
					continue
				}
				info.criteria = append(info.criteria, recordFilter{
					name:      f.Name,
					protoType: f.Type,
					rangeOp:   qf.StringValue == "range",
				})
			}
			// Point read by key, folded in exactly as the s2s filter folds it —
			// and skipped when the author already declared the key queryable, so
			// the criterion is never emitted twice.
			info.criteria = appendKeyFilterCriterion(info.criteria, key, msg)
			records[fd.Package+"."+msg.Name] = info
		}
	}

	// Every reader that asked for the surface, in deterministic file order.
	var relPaths []string
	for relPath := range dp.GetAllFiles() {
		relPaths = append(relPaths, relPath)
	}
	sort.Strings(relPaths)

	for _, relPath := range relPaths {
		fd := dp.GetAllFiles()[relPath]
		if fd.ServiceName == "" {
			continue
		}
		var reads []recordC2SRead
		for _, decl := range fd.RecordReads {
			if decl.C2S == nil {
				continue
			}
			info, ok := records[decl.Record]
			if !ok {
				// ValidateRecordReadAnnotations has already refused this build with
				// a message naming the unresolvable record; a second one here would
				// only replace it with a worse one.
				continue
			}
			idx := strings.LastIndex(decl.Record, ".")
			reads = append(reads, recordC2SRead{
				recordFQN:  decl.Record,
				recordName: decl.Record[idx+1:],
				recordPath: info.relPath,
				key:        info.key,
				keyType:    info.keyType,
				scope:      decl.C2S.Scope,
				exposure:   decl.C2S.Exposure,
				criteria:   info.criteria,
			})
		}
		if len(reads) == 0 {
			continue
		}
		sort.Slice(reads, func(i, j int) bool { return reads[i].recordFQN < reads[j].recordFQN })
		if err := appendRecordC2SPlane(tempProtoDir, relPath, fd, reads); err != nil {
			return err
		}
	}
	return nil
}

// appendRecordC2SPlane writes one reader's surface into its own proto file.
func appendRecordC2SPlane(tempProtoDir, relPath string, fd *parser.FileDescriptor, reads []recordC2SRead) error {
	filePath := filepath.Join(tempProtoDir, relPath)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read proto file %s for the record c2s plane: %w", relPath, err)
	}

	// The record may live in another proto. Import it when it does; the record's
	// own file never imports the reader's, so this cannot cycle.
	var imports []string
	seen := map[string]bool{relPath: true}
	for _, r := range reads {
		if seen[r.recordPath] {
			continue
		}
		seen[r.recordPath] = true
		imports = append(imports, fmt.Sprintf("import %q;", r.recordPath))
	}
	if len(imports) > 0 {
		content = injectImports(content, imports)
	}

	svc := strutil.ToPascalCase(fd.ServiceName) + "Records"

	var sb strings.Builder
	sb.WriteString("\n// Auto-generated client-facing record read plane from (spec.record_read).c2s (tacplus)\n")

	for _, r := range reads {
		t := r.recordName
		fq := r.recordFQN

		// <T>ReadFilter — what a CLIENT may narrow by. The principal and namespace
		// criteria are absent by construction: they are pinned server-side before
		// the store is asked, and a field that does not exist cannot be forged.
		sb.WriteString(fmt.Sprintf("\nmessage %sReadFilter {\n", t))
		n := 1
		for _, f := range r.criteria {
			if f.rangeOp {
				sb.WriteString(fmt.Sprintf("  optional %s %s_from = %d;\n", f.protoType, f.name, n))
				n++
				sb.WriteString(fmt.Sprintf("  optional %s %s_to = %d;\n", f.protoType, f.name, n))
				n++
			} else {
				sb.WriteString(fmt.Sprintf("  optional %s %s = %d;\n", f.protoType, f.name, n))
				n++
			}
		}
		sb.WriteString("}\n")

		// Get<T> — a point read by the record's key, and nothing else. An absent
		// row is an ordinary answer (an empty `items`) rather than NOT_FOUND:
		// confirming that a key exists in another tenant would itself be a
		// disclosure, so a miss and a cross-tenant hit have to be indistinguishable.
		//
		// `repeated items`, holding at most one row, rather than `optional item`.
		// The (spec.store_principal) and (spec.store_namespace) egress filters —
		// the backstop behind the server's own pin, and the abort that makes a
		// cross-tenant leak impossible rather than unlikely — are spliced onto
		// REPEATED record-typed response fields (rust/types/namespace_gen.go's
		// namespaceListFields, and the Java twin). A singular field would carry
		// neither, which would leave the one RPC where a point read of another
		// tenant's row is expressible as the only unguarded path on the surface.
		// Matching the shape the backstop already covers is much cheaper than
		// teaching every egress seam a second field shape, and it keeps both RPCs
		// on this surface identical from the filter's point of view.
		sb.WriteString(fmt.Sprintf(`
message Get%[1]sRequest {
  %[2]s %[3]s = 1;
}

message Get%[1]sResponse {
  repeated %[4]s items = 1;
}

message Get%[1]sPageRequest {
  %[1]sReadFilter filter = 1;
  int32 page_size = 2;
  string page_token = 3;
}

message Get%[1]sPageResponse {
  repeated %[4]s items = 1;
  string next_page_token = 2;
  int64 total_count = 3;
}
`, t, r.keyType, r.key, fq))
	}

	// THE PAGED READ IS Get<T>Page, NOT List<T>, AND IT DECLARES NO
	// (spec.method_collection). Both follow from one fact about the Rust wrapper,
	// and neither is a naming preference.
	//
	// The wrapper's (spec.store_principal) and (spec.store_namespace) egress
	// filters — the row scope, and the tenant backstop that makes a cross-tenant
	// leak impossible rather than unlikely — were originally spliced onto the
	// UNARY, NON-COLLECTION seam only; servicewrapper_gen.go's PAGE branch now
	// generates the same filters (fixed 2026-09-02), but this surface still
	// avoids COLLECTION_MODE_PAGE for the independent reason below (PAGE is the
	// wrong MODEL for this read), so the RPC shape here is unchanged. Kept as
	// history: a PAGE List<T> reaching this generator with no egress filter at
	// all used to be possible for a Go target with no equivalent refusal —
	// verify that gap is still open (or already closed) before relying on this
	// paragraph as current fact.
	//
	// PAGE is also the wrong MODEL for this read, independent of the filters. A
	// COLLECTION_MODE_PAGE handler is a KEYSET producer: the wrapper owns the
	// page-token codec and hands the handler a decoded cursor. This surface pages
	// through the record store, whose page_token is an opaque server-side value
	// the store issues and the store interprets. Smuggling one through a keyset
	// cursor to satisfy the annotation would be exactly the kind of shim that
	// makes a generated guarantee stop being one.
	//
	// So the paging is explicit in the message — page_size and page_token in,
	// next_page_token and total_count out, the store applying the plane's declared
	// default when page_size is 0 — and the RPC carries no collection annotation.
	// That in turn forces the name: verbs.classify refuses `List` without
	// COLLECTION_MODE_SNAPSHOT or COLLECTION_MODE_PAGE, and SNAPSHOT would be a
	// false statement (it means the whole bounded set inline, with no paging at
	// all). `Get<T>Page` classifies as GET/READ, says what the call does, and puts
	// the grant in the reader's own catalog beside Get<T> — which is all gate 1
	// ever needed. The surface the design asked for is unchanged; only the
	// spelling is.
	sb.WriteString("\n// tacplus:generated-service " + parser.SynthesizedKindRecordC2S + "\nservice " + svc + " {\n")
	sb.WriteString("  option (spec.service_c2s) = {auth_entitled_users: true};\n")
	for _, r := range reads {
		sb.WriteString(fmt.Sprintf(`
  rpc Get%[1]s(Get%[1]sRequest) returns (Get%[1]sResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: %[2]s};
  }
  rpc Get%[1]sPage(Get%[1]sPageRequest) returns (Get%[1]sPageResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: %[2]s};
  }
`, r.recordName, r.exposure))
	}
	sb.WriteString("}\n")

	var buf strings.Builder
	buf.Write(content)
	buf.WriteString(sb.String())
	if err := os.WriteFile(filePath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write proto file %s: %w", relPath, err)
	}

	names := make([]string, 0, len(reads))
	for _, r := range reads {
		names = append(names, r.recordFQN)
	}
	zap.L().Info("appended client-facing record read plane",
		zap.String("file", relPath),
		zap.String("service", svc),
		zap.Strings("records", names))
	return nil
}
