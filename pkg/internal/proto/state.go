// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/eventbus"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// findMessageClosingBrace finds the position of the closing "}" for a
// top-level message declaration. Returns the byte offset of the "}" or -1
// if not found. Uses brace-depth tracking instead of regex.
func findMessageClosingBrace(text, messageName string) int {
	return findDeclClosingBrace(text, "message", messageName)
}

// findDeclClosingBrace finds the position of the closing "}" for a top-level
// `<keyword> <name> { ... }` declaration ("message" or "service"). Returns the
// byte offset of the "}" or -1 if not found.
func findDeclClosingBrace(text, keyword, name string) int {
	target := keyword + " " + name
	searchFrom := 0
	for {
		idx := strings.Index(text[searchFrom:], target)
		if idx < 0 {
			return -1
		}
		idx += searchFrom

		// Verify this is a whole-word match (not a prefix of another message name).
		// The character after the name must be whitespace or '{'.
		afterName := idx + len(target)
		if afterName < len(text) {
			ch := text[afterName]
			if ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' && ch != '{' {
				searchFrom = afterName
				continue
			}
		}

		// Find the opening brace
		braceStart := strings.Index(text[afterName:], "{")
		if braceStart < 0 {
			return -1
		}
		braceStart += afterName

		return findMatchingCloseBrace(text, braceStart)
	}
}

// findMatchingCloseBrace returns the byte offset of the "}" that closes the
// "{" at openBrace, tracking brace depth so nested braces in the body (e.g. a
// multi-line option literal) don't end the walk early. Returns -1 if
// unbalanced.
func findMatchingCloseBrace(text string, openBrace int) int {
	depth := 1
	for i := openBrace + 1; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// AppendStateServiceToProtos appends strongly-typed state service definitions
// directly to each service's proto file in tempProtoDir. For each state root,
// it appends:
//   - A WatchService with a bidirectional Watch RPC
//   - A typed Commit message (writable boundaries + Lock)
//   - A typed Update message (all boundaries including read-only)
//
// Since the definitions are appended to the same proto file (same package),
// local types are referenced without a package prefix.
func AppendStateServiceToProtos(tempProtoDir, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	generated := make(map[string]bool)
	eventSurfaces := collectStateEventSurfaces(dp.GetAllFiles())

	// Who may call each generated state service: the owning package plus every
	// package that declares a state_import of the root. Indexed once over the
	// whole descriptor set, since importers live in other services' protos.
	importersByRoot := stateImportersByRoot(dp.GetAllFiles())

	// State services whose root declares disable_auth_message: no token
	// validation at all, so no caller list — the auth post-condition skips them.
	// Keyed by "<relPath>/<ServiceName>" because a service name alone is not
	// unique across packages (e.g. every service's ServiceConfigState root).
	authExempt := make(map[string]bool)

	// Group state roots by file so we can emit shared messages once per file.
	type fileEntry struct {
		fileDesc *parser.FileDescriptor
		relPath  string
		roots    []stateRootEntry
	}
	fileOrder := make([]string, 0) // processing order; see sorted walk below
	fileMap := make(map[string]*fileEntry)

	// GetAllFiles is a MAP, and processing order is externally observable: the
	// pkgEmitted bookkeeping below emits each package's shared messages
	// (MapEmpty, CollectionQuery, ...) into whichever of that package's files is
	// processed FIRST. Under raw map order two identical builds put those
	// messages in different proto files — and therefore in different generated
	// type modules. Walk sorted so "first in the package" is a fixed file.
	allPaths := make([]string, 0, len(dp.GetAllFiles()))
	for relPath := range dp.GetAllFiles() {
		allPaths = append(allPaths, relPath)
	}
	sort.Strings(allPaths)

	for _, relPath := range allPaths {
		fileDesc := dp.GetAllFiles()[relPath]
		// Skip infrastructure protos except the manifest-declared runtime config
		// state proto, which contains ServiceConfigState.
		if IsInfraProtoFile(relPath, fileDesc) && !IsRuntimeConfigStateProto(relPath) {
			continue
		}

		for i := range fileDesc.Messages {
			ann, ok := fileDesc.Messages[i].Annotations["service_state"]
			if !ok || ann.StringValue == "" {
				continue
			}
			stateName := ann.StringValue
			key := fileDesc.Package + "." + stateName
			if generated[key] {
				continue
			}
			generated[key] = true

			var readsState []string
			for _, svc := range fileDesc.Services {
				if len(svc.ReadsState) > 0 {
					readsState = svc.ReadsState
					break
				}
			}

			fe, ok := fileMap[relPath]
			if !ok {
				fe = &fileEntry{fileDesc: fileDesc, relPath: relPath}
				fileMap[relPath] = fe
				fileOrder = append(fileOrder, relPath)
			}
			var authServices []string
			if _, authDisabled := fileDesc.Messages[i].Annotations["disable_auth_message"]; authDisabled {
				authExempt[filepath.ToSlash(relPath)+"/"+StateServiceName(stateName)] = true
			} else {
				authServices = stateServiceAuthServices(fileDesc.Package, importersByRoot[key])
			}

			fe.roots = append(fe.roots, stateRootEntry{
				stateRoot:    &fileDesc.Messages[i],
				stateName:    stateName,
				readsState:   readsState,
				authServices: authServices,
			})
		}
	}

	// Process each file: build all services, then emit shared messages once.
	// Track per-package which shared messages have been written so that two
	// files in the same package (e.g. services/ and vservices/ both using
	// package gitlabconnector) don't each emit MapEmpty, causing a duplicate.
	pkgEmitted := make(map[string]*sharedMessageNeeds)
	for _, relPath := range fileOrder {
		fe := fileMap[relPath]
		if _, ok := pkgEmitted[fe.fileDesc.Package]; !ok {
			pkgEmitted[fe.fileDesc.Package] = &sharedMessageNeeds{}
		}
		if err := appendStateServicesForFile(tempProtoDir, fe.fileDesc, fe.roots, fe.relPath, pkgEmitted[fe.fileDesc.Package], eventSurfaces); err != nil {
			return fmt.Errorf("failed to append state services for %s: %w", relPath, err)
		}
	}

	// Post-condition, checked on what was actually written: a state service
	// emitted without a resolvable caller list fails closed at runtime on every
	// S2S call with no other build-time signal, so generation stops here instead.
	if err := ValidateGeneratedStateServiceAuth(tempProtoDir, authExempt); err != nil {
		return err
	}

	return nil
}

// stateImportersByRoot maps "package.StateName" to the packages that declare a
// read-only import of that root, from either the field-level state_import
// projections or the legacy file-level state_imports option. Those importers,
// plus the owning package itself, are exactly the callers the root's generated
// state service must authorize — see stateServiceAuthServices.
func stateImportersByRoot(allFiles map[string]*parser.FileDescriptor) map[string][]string {
	byRoot := make(map[string]map[string]bool)
	add := func(root, pkg string) {
		if root == "" || pkg == "" {
			return
		}
		if byRoot[root] == nil {
			byRoot[root] = make(map[string]bool)
		}
		byRoot[root][pkg] = true
	}
	for _, fileDesc := range allFiles {
		for _, imp := range fileDesc.FieldStateImports {
			add(imp.Root, fileDesc.Package)
		}
		for _, root := range fileDesc.FileStateImports {
			add(root, fileDesc.Package)
		}
	}
	out := make(map[string][]string, len(byRoot))
	for root, pkgs := range byRoot {
		list := make([]string, 0, len(pkgs))
		for pkg := range pkgs {
			list = append(list, pkg)
		}
		sort.Strings(list)
		out[root] = list
	}
	return out
}

// stateServiceAuthServices resolves the auth_services list for a state root's
// generated service. A state service is reachable by its owning service (which
// holds RW on its own state) and by every service that declares a state_import
// of it; nothing else has a generated client for it, so nothing else is
// authorized. Importers are RO by construction — ResolveStatesFromProto records
// them as AccessRO and the state-client generator emits accessors only for RO
// states, so a service-level list grants them no usable mutator path.
//
// The list is never empty: the owner is always present. That is the point —
// an empty auth_services on an S2S service means every service caller is
// denied at runtime with no build-time signal, which is exactly the failure
// ValidateGeneratedStateServiceAuth exists to make impossible.
func stateServiceAuthServices(ownerPkg string, importers []string) []string {
	auth := []string{ownerPkg}
	for _, pkg := range importers {
		if pkg != ownerPkg {
			auth = append(auth, pkg)
		}
	}
	return auth
}

// formatAuthServicesOption renders the service-level spec.service_s2s option
// carrying the resolved caller list. Service-level auth_services is the
// declared home for tacplus-generated service protos (see ServiceS2SAnnotation
// in sys/spec.proto); the per-language service wrappers fan it out to a
// per-RPC SetMethodAuth registration for every method on the service.
func formatAuthServicesOption(auth []string) string {
	quoted := make([]string, 0, len(auth))
	for _, a := range auth {
		quoted = append(quoted, fmt.Sprintf("%q", a))
	}
	return fmt.Sprintf("\toption (spec.service_s2s) = {auth_services: [%s]};\n", strings.Join(quoted, ", "))
}

// formatEventBusS2SMethodOption renders the per-RPC spec.rpc option carrying the
// caller list the event/record message declared in (spec.record).s2s /
// (spec.event).s2s. Without it a topic's RPCs fall back to the state service's
// service-level list, which for a synthesized <Event>Bus wrapper is resolved from
// the owning PACKAGE (stateServiceAuthServices) — silently narrowing an
// author-declared ["*"] to ["<package>"]. Per-RPC because that is the scope the
// declaration has (one topic, not the whole state root), and because the registry
// and every service wrapper already resolve a method-level auth_services as an
// override of the service-level default.
func formatEventBusS2SMethodOption(auth *model.EventBusMethodS2S) string {
	if auth == nil || len(auth.AuthServices) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(auth.AuthServices))
	for _, a := range auth.AuthServices {
		quoted = append(quoted, fmt.Sprintf("%q", a))
	}
	return fmt.Sprintf("\t\toption (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: [%s]};\n",
		strings.Join(quoted, ", "))
}

func formatEventBusC2SMethodOption(auth *model.EventBusMethodC2S) string {
	if auth == nil || (!auth.AuthDisabled && !auth.AuthAnyUser && auth.TargetNamespaceField == "") {
		return ""
	}
	parts := []string{
		"principal: RPC_PRINCIPAL_USER",
		"exposure: RPC_EXPOSURE_EXTERNAL",
	}
	var flags []string
	if auth.AuthDisabled {
		flags = append(flags, "RPC_FLAG_PUBLIC_NO_AUTH")
	}
	if auth.AuthAnyUser {
		flags = append(flags, "RPC_FLAG_AUTH_ANY_USER")
	}
	if len(flags) > 0 {
		parts = append(parts, "flags: ["+strings.Join(flags, ", ")+"]")
	}
	if auth.TargetNamespaceField != "" {
		parts = append(parts, fmt.Sprintf("target_namespace_field: %q", auth.TargetNamespaceField))
	}
	return "\t\toption (spec.rpc) = {" + strings.Join(parts, ", ") + "};\n"
}

// sharedMessageNeeds tracks which file-scoped shared messages are required
// across all state roots in a single proto file.
type sharedMessageNeeds struct {
	mapEmpty              bool
	queueReceipt          bool
	eventBusRecordMeta    bool
	leaderElection        bool
	collectionQuery       bool
	emptyImport           bool
	traceImport           bool
	timestampImport       bool
	eventBusSupportImport bool
}

type stateEventSurface struct {
	c2s          *model.EventBusMethodC2S
	s2s          *model.EventBusMethodS2S
	s2sSubscribe *model.EventBusMethodS2S
	declared     bool
}

func collectStateEventSurfaces(allFiles map[string]*parser.FileDescriptor) map[string]stateEventSurface {
	surfaces := make(map[string]stateEventSurface)
	for _, fd := range allFiles {
		for i := range fd.Messages {
			msg := &fd.Messages[i]
			ann, ok := msg.Annotations["event_surface"]
			if !ok || len(ann.EventPublishEntries) == 0 {
				continue
			}
			entry := ann.EventPublishEntries[0]
			surfaces[fd.Package+"."+msg.Name] = stateEventSurface{
				c2s:          entry.MethodC2S,
				s2s:          entry.MethodS2S,
				s2sSubscribe: entry.MethodS2SSubscribe,
				declared:     true,
			}
		}
	}
	return surfaces
}

// stateServiceResult holds the output of building one state root's service definition.
type stateServiceResult struct {
	serviceProto   string // the service definition + per-collection messages
	needs          sharedMessageNeeds
	stateName      string
	singletons     int
	collections    int
	queues         int
	priorityQueues int
	topics         int
	views          int
	hasLeader      bool
	readOnly       int
}

func stateServiceTraceMessage(name string, fields ...string) string {
	return stateServiceMessage(name, true, fields...)
}

func stateServiceMessage(name string, includeTraceFields bool, fields ...string) string {
	if !includeTraceFields {
		var buf strings.Builder
		fmt.Fprintf(&buf, "message %s {", name)
		for _, field := range fields {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			fmt.Fprintf(&buf, " %s", field)
		}
		buf.WriteString(" }\n")
		return buf.String()
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "message %s {\n", name)
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		fmt.Fprintf(&buf, "  %s\n", field)
	}
	if includeTraceFields {
		buf.WriteString(TraceFieldsProto)
	}
	buf.WriteString("}\n")
	return buf.String()
}

// buildStateService builds the proto text for a single state root's state service.
// It does NOT read/write files or emit shared messages — the caller handles that.
func buildStateService(
	fileDesc *parser.FileDescriptor,
	stateRoot *parser.MessageDescriptor,
	stateName string,
	readsState []string,
	authServices []string,
	eventSurfaces map[string]stateEventSurface,
) stateServiceResult {
	pkg := fileDesc.Package

	type boundaryField struct {
		fieldName       string
		typeName        string
		goName          string
		isPrivate       bool
		isRepeated      bool
		isQueue         bool
		isPriorityQueue bool
		priorityField   string
		isTopic         bool
		isLeader        bool
		isCommand       bool
		isView          bool
		isInstanceKeyed bool
		viewSources     []string
		refreshOnMiss   string
		subscribeRecord string
		publishC2S      *model.EventBusMethodC2S
		publishS2S      *model.EventBusMethodS2S
		subscribeS2S    *model.EventBusMethodS2S
		hasEventSurface bool
	}

	var singletons, collections, queues, priorityQueues, topics, commands, views []boundaryField
	var readOnlySingletons []boundaryField
	for _, field := range stateRoot.Fields {
		if field.TypeKind != "message" {
			continue
		}
		// Skip injected trace fields (200-203) — they are not state boundaries.
		if field.Number >= 200 && field.Number <= 203 {
			continue
		}
		// Skip every map field on a state root. ValidateStateConsistency rejects
		// them loudly before generation reaches here (a boundary must be a named
		// message type), so this is defence in depth against a validation-ordering
		// change — matching the boundary collector, which also skips them
		// (boundaries.go). It must skip MESSAGE-valued maps too: falling through
		// would classify the field as a boundary and synthesize a service over the
		// internal map-entry type, which protoc then rejects ("map_entry should not
		// be set explicitly") — a generated-proto break, not a skipped boundary.
		if field.IsMap {
			continue
		}

		// State import: read-only projection of a boundary owned by another
		// service. Emit only Get/Watch and skip mutable boundary classification.
		if importAnn, hasImport := field.Annotations["state_import"]; hasImport && importAnn.StringValue != "" {
			typeName := field.Type
			if strings.HasPrefix(typeName, pkg+".") {
				typeName = strings.TrimPrefix(typeName, pkg+".")
			}
			readOnlySingletons = append(readOnlySingletons, boundaryField{
				fieldName: field.Name,
				typeName:  typeName,
				goName:    strutil.ToCamelCase(field.Name),
			})
			continue
		}

		// View: read-only union of multiple source collections (skip normal classification)
		if vsAnn, hasVS := field.Annotations["view_sources"]; hasVS && len(vsAnn.StringSliceValue) > 0 {
			typeName := field.Type
			if strings.HasPrefix(typeName, pkg+".") {
				typeName = strings.TrimPrefix(typeName, pkg+".")
			}
			views = append(views, boundaryField{
				fieldName:   field.Name,
				typeName:    typeName,
				goName:      strutil.ToCamelCase(field.Name),
				isRepeated:  field.IsRepeated,
				isView:      true,
				viewSources: vsAnn.StringSliceValue,
			})
			continue
		}

		// Command: explicit command RPC (skip normal boundary classification)
		if cmdAnn, hasCmd := field.Annotations["command"]; hasCmd && cmdAnn.BoolValue {
			_, isPrivate := field.Annotations["private"]
			typeName := field.Type
			if strings.HasPrefix(typeName, pkg+".") {
				typeName = strings.TrimPrefix(typeName, pkg+".")
			}
			commands = append(commands, boundaryField{
				fieldName: field.Name,
				typeName:  typeName,
				goName:    strutil.ToCamelCase(field.Name),
				isPrivate: isPrivate,
				isCommand: true,
			})
			continue
		}

		queueAnn, hasQueue := field.Annotations["queue"]
		pqAnn, hasPQ := field.Annotations["priority_queue"]
		topicAnn, hasTopic := field.Annotations["topic"]
		leaderAnn, hasLeader := field.Annotations["leader"]
		ikAnn, hasIK := field.Annotations["instance_keyed"]
		_, isPrivate := field.Annotations["private"]

		typeName := field.Type
		if strings.HasPrefix(typeName, pkg+".") {
			typeName = strings.TrimPrefix(typeName, pkg+".")
		}

		isLeader := hasLeader && leaderAnn.BoolValue

		var refreshOnMiss string
		if romAnn, hasROM := field.Annotations["refresh_on_miss"]; hasROM {
			refreshOnMiss = romAnn.StringValue
		}

		isInstanceKeyed := hasIK && ikAnn.BoolValue

		bf := boundaryField{
			fieldName:       field.Name,
			typeName:        typeName,
			goName:          strutil.ToCamelCase(field.Name),
			isPrivate:       isPrivate,
			isRepeated:      field.IsRepeated,
			isLeader:        isLeader,
			isInstanceKeyed: isInstanceKeyed,
			refreshOnMiss:   refreshOnMiss,
		}
		if surface, ok := eventSurfaces[field.Type]; ok {
			bf.publishC2S = surface.c2s
			bf.publishS2S = surface.s2s
			bf.subscribeS2S = surface.s2sSubscribe
			bf.hasEventSurface = surface.declared
		}

		switch {
		case hasQueue && queueAnn.BoolValue:
			bf.isQueue = true
			queues = append(queues, bf)
		case hasPQ && pqAnn.StringValue != "":
			bf.isPriorityQueue = true
			bf.priorityField = pqAnn.StringValue
			priorityQueues = append(priorityQueues, bf)
		case hasTopic && topicAnn.BoolValue:
			bf.isTopic = true
			bf.subscribeRecord = TopicSubscribeRecordName(stateName, bf.goName)
			topics = append(topics, bf)
		case field.IsRepeated:
			collections = append(collections, bf)
		default:
			singletons = append(singletons, bf)
		}
	}

	for _, readRef := range readsState {
		parts := strings.Split(readRef, ".")
		if len(parts) != 2 {
			zap.L().Warn("Invalid reads_state reference", zap.String("ref", readRef))
			continue
		}
		readOnlySingletons = append(readOnlySingletons, boundaryField{
			fieldName: strutil.ToSnakeCase(parts[1]),
			typeName:  readRef,
			goName:    parts[1],
		})
	}

	var buf strings.Builder
	eventC2S := false
	hasDeclaredEventSurface := false
	for _, topic := range topics {
		if topic.isPrivate {
			continue
		}
		hasDeclaredEventSurface = hasDeclaredEventSurface || topic.hasEventSurface
		eventC2S = eventC2S || topic.publishC2S != nil
	}
	if !hasDeclaredEventSurface && len(topics) > 0 {
		if export, ok := stateRoot.Annotations["service_state_export"]; ok && export.BoolValue {
			eventC2S = true
		}
	}

	buf.WriteString("\n// ============================================================================\n")
	buf.WriteString("// Auto-generated State Service (tacplus)\n")
	buf.WriteString("// ============================================================================\n")
	// The sentinel carries its KIND. A generated STATE service means the process
	// must reach its state host and therefore needs an S2S identity — unlike a
	// generated store service, which a c2d daemon may never touch. Readers that
	// cannot tell them apart forced a minter on builds that need none.
	buf.WriteString("// tacplus:generated-service " + parser.SynthesizedKindState + "\n")

	fmt.Fprintf(&buf, "service %s {\n", StateServiceName(stateName))

	// Record/event topics with a declared C2S producer surface are served by
	// EventBus under this per-topic service name. Put that real descriptor on the
	// ordinary C2S path; all other state services remain S2S.
	if eventC2S {
		buf.WriteString("\toption (spec.service_c2s) = {auth_entitled_users: true};\n")
	} else {
		// The caller list is resolved from the root's owner and its declared
		// state_import-ers; an empty spec.service_s2s here would leave every RPC
		// with no auth_services policy, which the authorizers reject at runtime
		// with no build-time signal. disable_auth_message roots skip token
		// validation entirely and therefore carry no caller list.
		if len(authServices) > 0 {
			buf.WriteString(formatAuthServicesOption(authServices))
		} else {
			fmt.Fprintf(&buf, "\toption (spec.service_s2s) = {};\n")
		}
	}

	// Propagate service_event annotation to generated service options.
	if ann, ok := stateRoot.Annotations["service_event_label"]; ok && ann.StringValue != "" {
		fmt.Fprintf(&buf, "\toption (spec.event_label) = \"%s\";\n", ann.StringValue)
	}

	for _, b := range singletons {
		if b.isPrivate {
			continue
		}
		fmt.Fprintf(&buf, "\t// Singleton: %s\n", b.goName)
		fmt.Fprintf(&buf, "\trpc Get%s(trace.TraceRequest) returns (%s);\n", b.goName, b.typeName)
		fmt.Fprintf(&buf, "\trpc Set%s(%s) returns (%s);\n", b.goName, b.typeName, b.typeName)
		fmt.Fprintf(&buf, "\trpc Watch%s(trace.TraceRequest) returns (stream %s);\n", b.goName, b.typeName)
	}
	for _, b := range readOnlySingletons {
		fmt.Fprintf(&buf, "\t// Read-only singleton: %s\n", b.goName)
		fmt.Fprintf(&buf, "\trpc Get%s(trace.TraceRequest) returns (%s);\n", b.goName, b.typeName)
		fmt.Fprintf(&buf, "\trpc Watch%s(trace.TraceRequest) returns (stream %s);\n", b.goName, b.typeName)
	}

	for _, b := range collections {
		if b.isPrivate {
			continue
		}
		if b.isInstanceKeyed {
			fmt.Fprintf(&buf, "\t// Collection (instance-keyed): %s\n", b.goName)
			// Publisher: simplified Set — no key param, server builds key from auth context
			fmt.Fprintf(&buf, "\trpc Set%s(%s) returns (MapEmpty);\n", b.goName, b.typeName)
			// Consumer: standard read RPCs
			fmt.Fprintf(&buf, "\trpc Get%s(%sGetRequest) returns (%sValueResponse);\n", b.goName, b.goName, b.goName)
			fmt.Fprintf(&buf, "\trpc GetAll%s(%sGetAllRequest) returns (%sEntriesResponse);\n", b.goName, b.goName, b.goName)
			fmt.Fprintf(&buf, "\trpc ContainsKey%s(%sGetRequest) returns (%sValueResponse);\n", b.goName, b.goName, b.goName)
			fmt.Fprintf(&buf, "\trpc KeySet%s(%sKeysRequest) returns (%sKeysResponse);\n", b.goName, b.goName, b.goName)
			fmt.Fprintf(&buf, "\trpc Size%s(%sSizeRequest) returns (%sSizeResponse);\n", b.goName, b.goName, b.goName)
			fmt.Fprintf(&buf, "\trpc Watch%s(trace.TraceRequest) returns (stream %sEntriesResponse);\n", b.goName, b.goName)
			fmt.Fprintf(&buf, "\trpc Query%s(%sQueryRequest) returns (%sQueryResponse);\n", b.goName, b.goName, b.goName)
			fmt.Fprintf(&buf, "\trpc WatchQuery%s(%sQueryRequest) returns (stream %sQueryResponse);\n", b.goName, b.goName, b.goName)
			continue
		}
		fmt.Fprintf(&buf, "\t// Collection: %s\n", b.goName)
		fmt.Fprintf(&buf, "\trpc Get%s(%sGetRequest) returns (%sValueResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc Put%s(%sPutRequest) returns (%sValueResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc Set%s(%sPutRequest) returns (MapEmpty);\n", b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc Remove%s(%sGetRequest) returns (%sValueResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc Delete%s(%sGetRequest) returns (MapEmpty);\n", b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc ContainsKey%s(%sGetRequest) returns (%sValueResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc GetAll%s(%sGetAllRequest) returns (%sEntriesResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc KeySet%s(%sKeysRequest) returns (%sKeysResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc Size%s(%sSizeRequest) returns (%sSizeResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc Clear%s(%sClearRequest) returns (MapEmpty);\n", b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc PutIfAbsent%s(%sPutRequest) returns (%sValueResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc Watch%s(trace.TraceRequest) returns (stream %sEntriesResponse);\n", b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc Query%s(%sQueryRequest) returns (%sQueryResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc WatchQuery%s(%sQueryRequest) returns (stream %sQueryResponse);\n", b.goName, b.goName, b.goName)
	}

	for _, b := range queues {
		if b.isPrivate {
			continue
		}
		fmt.Fprintf(&buf, "\t// Queue: %s\n", b.goName)
		fmt.Fprintf(&buf, "\trpc OfferTo%s(%s) returns (QueueReceipt);\n", b.goName, b.typeName)
		fmt.Fprintf(&buf, "\trpc ConsumeFrom%s(trace.TraceRequest) returns (stream %s);\n", b.goName, b.typeName)
		fmt.Fprintf(&buf, "\trpc Peek%s(trace.TraceRequest) returns (%s);\n", b.goName, b.typeName)
		fmt.Fprintf(&buf, "\trpc Size%s(trace.TraceRequest) returns (QueueSizeResponse);\n", b.goName)
	}

	for _, b := range priorityQueues {
		if b.isPrivate {
			continue
		}
		fmt.Fprintf(&buf, "\t// PriorityQueue: %s (priority field: %s)\n", b.goName, b.priorityField)
		fmt.Fprintf(&buf, "\trpc OfferTo%s(%s) returns (QueueReceipt);\n", b.goName, b.typeName)
		fmt.Fprintf(&buf, "\trpc ConsumeFrom%s(trace.TraceRequest) returns (stream %s);\n", b.goName, b.typeName)
		fmt.Fprintf(&buf, "\trpc Peek%s(trace.TraceRequest) returns (%s);\n", b.goName, b.typeName)
		fmt.Fprintf(&buf, "\trpc Size%s(trace.TraceRequest) returns (QueueSizeResponse);\n", b.goName)
	}

	for _, b := range topics {
		if b.isPrivate {
			continue
		}
		// A record/event c2s declaration is a producer surface only. When this
		// state service is the ordinary C2S client descriptor, omit internal-only
		// topics and do not turn the broker's S2S Subscribe method into a C2S RPC.
		if eventC2S && b.hasEventSurface && b.publishC2S == nil {
			continue
		}
		fmt.Fprintf(&buf, "\t// Topic: %s\n", b.goName)
		methodOption := formatEventBusC2SMethodOption(b.publishC2S)
		if methodOption == "" {
			methodOption = formatEventBusS2SMethodOption(b.publishS2S)
		}
		if methodOption == "" {
			fmt.Fprintf(&buf, "\trpc PublishTo%s(stream %s) returns (stream eventbus_support.PublishResponse);\n", b.goName, b.typeName)
		} else {
			fmt.Fprintf(&buf, "\trpc PublishTo%s(stream %s) returns (stream eventbus_support.PublishResponse) {\n%s\t}\n", b.goName, b.typeName, methodOption)
		}
		// Subscribe carries the CONSUMER declaration and never the producer one:
		// (spec.record).s2s / (spec.event).s2s is documented as the publish surface,
		// so propagating it here would invent a policy the author never wrote.
		// (spec.record).s2s_subscribe / (spec.event).s2s_subscribe is the consumer
		// half, and it emits at the same per-RPC level for the same reason — one
		// topic, not the whole state root. Undeclared keeps the service-level
		// default, which is what every topic had before the field existed.
		if !eventC2S || !b.hasEventSurface {
			subscribeOption := formatEventBusS2SMethodOption(b.subscribeS2S)
			if subscribeOption == "" {
				fmt.Fprintf(&buf, "\trpc SubscribeTo%s(stream eventbus_support.SubscribeRequest) returns (stream %s);\n", b.goName, b.subscribeRecord)
			} else {
				fmt.Fprintf(&buf, "\trpc SubscribeTo%s(stream eventbus_support.SubscribeRequest) returns (stream %s) {\n%s\t}\n", b.goName, b.subscribeRecord, subscribeOption)
			}
		}
	}

	hasLeaderBoundary := false
	allGraphBoundaries := append(append(queues, priorityQueues...), topics...)
	for _, b := range allGraphBoundaries {
		if b.isPrivate || !b.isLeader {
			continue
		}
		hasLeaderBoundary = true
		fmt.Fprintf(&buf, "\t// Leader Election: %s\n", b.goName)
		fmt.Fprintf(&buf, "\trpc Elect%sLeader(LeaderElectRequest) returns (LeaderRecord);\n", b.goName)
		fmt.Fprintf(&buf, "\trpc Resign%sLeader(LeaderResignRequest) returns (LeaderRecord);\n", b.goName)
		fmt.Fprintf(&buf, "\trpc GetLeader%s(trace.TraceRequest) returns (LeaderRecord);\n", b.goName)
		fmt.Fprintf(&buf, "\trpc WatchLeader%s(trace.TraceRequest) returns (stream LeaderRecord);\n", b.goName)
		fmt.Fprintf(&buf, "\trpc RenewLease%s(stream LeaderLeaseRenewal) returns (stream LeaderRecord);\n", b.goName)
	}

	for _, b := range singletons {
		if b.isPrivate || b.refreshOnMiss == "" {
			continue
		}
		fmt.Fprintf(&buf, "\t// Lookup (refresh-on-miss): %s\n", b.goName)
		fmt.Fprintf(&buf, "\trpc Lookup%s(%s) returns (%s);\n", b.goName, b.refreshOnMiss, b.typeName)
	}
	for _, b := range collections {
		if b.isPrivate || b.refreshOnMiss == "" {
			continue
		}
		fmt.Fprintf(&buf, "\t// Lookup (refresh-on-miss): %s\n", b.goName)
		fmt.Fprintf(&buf, "\trpc Lookup%s(%s) returns (%sValueResponse);\n", b.goName, b.refreshOnMiss, b.goName)
	}

	for _, b := range views {
		fmt.Fprintf(&buf, "\t// View (read-only): %s [sources: %s]\n", b.goName, strings.Join(b.viewSources, ", "))
		fmt.Fprintf(&buf, "\trpc Get%s(%sGetRequest) returns (%sValueResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc ContainsKey%s(%sGetRequest) returns (%sValueResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc GetAll%s(%sGetAllRequest) returns (%sEntriesResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc KeySet%s(%sKeysRequest) returns (%sKeysResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc Size%s(%sSizeRequest) returns (%sSizeResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc Watch%s(trace.TraceRequest) returns (stream %sEntriesResponse);\n", b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc Query%s(%sQueryRequest) returns (%sQueryResponse);\n", b.goName, b.goName, b.goName)
		fmt.Fprintf(&buf, "\trpc WatchQuery%s(%sQueryRequest) returns (stream %sQueryResponse);\n", b.goName, b.goName, b.goName)
	}

	for _, b := range commands {
		if b.isPrivate {
			continue
		}
		fmt.Fprintf(&buf, "\t// Command: %s\n", b.goName)
		fmt.Fprintf(&buf, "\trpc %s(%s) returns (google.protobuf.Empty);\n", b.goName, b.typeName)
	}

	buf.WriteString("}\n\n")

	// Per-topic Subscribe records. The wire shape intentionally mirrors the
	// EventBus typed Subscribe contract: field 1 carries record metadata and
	// field 2 carries the typed event payload. The broker may use its own
	// RecordMeta type internally; protobuf field numbers keep the wire stable.
	for _, b := range topics {
		if b.isPrivate {
			continue
		}
		buf.WriteString(stateServiceMessage(
			b.subscribeRecord,
			false,
			"EventBusRecordMeta meta = 1;",
			fmt.Sprintf("%s payload = 2;", b.typeName),
		))
		buf.WriteString("\n")
	}

	// Per-collection typed request/response messages
	for _, b := range collections {
		if b.isPrivate {
			continue
		}
		buf.WriteString(stateServiceTraceMessage(b.goName+"GetRequest", "string key = 1;"))
		if !b.isInstanceKeyed {
			buf.WriteString(stateServiceTraceMessage(
				b.goName+"PutRequest",
				"string key = 1;",
				fmt.Sprintf("%s value = 2;", b.typeName),
				"int64 lock_counter = 3;",
			))
		}
		buf.WriteString(stateServiceTraceMessage(b.goName + "GetAllRequest"))
		buf.WriteString(stateServiceTraceMessage(b.goName + "KeysRequest"))
		buf.WriteString(stateServiceTraceMessage(b.goName + "SizeRequest"))
		if !b.isInstanceKeyed {
			buf.WriteString(stateServiceTraceMessage(b.goName + "ClearRequest"))
		}
		buf.WriteString(stateServiceTraceMessage(
			b.goName+"ValueResponse",
			fmt.Sprintf("%s value = 1;", b.typeName),
			"bool found = 2;",
		))
		buf.WriteString(stateServiceTraceMessage(
			b.goName+"EntriesResponse",
			fmt.Sprintf("map<string, %s> entries = 1;", b.typeName),
		))
		buf.WriteString(stateServiceTraceMessage(b.goName+"KeysResponse", "repeated string keys = 1;"))
		buf.WriteString(stateServiceTraceMessage(b.goName+"SizeResponse", "int32 size = 1;"))
		buf.WriteString(stateServiceTraceMessage(b.goName+"QueryRequest", "CollectionQuery query = 1;"))
		buf.WriteString(stateServiceTraceMessage(
			b.goName+"QueryResponse",
			fmt.Sprintf("repeated %s results = 1;", b.typeName),
			"int32 total_count = 2;",
		))
		buf.WriteString("\n")
	}

	// Per-view typed request/response messages (same shape as collection but no Put/Clear)
	for _, b := range views {
		buf.WriteString(stateServiceTraceMessage(b.goName+"GetRequest", "string key = 1;"))
		buf.WriteString(stateServiceTraceMessage(b.goName + "GetAllRequest"))
		buf.WriteString(stateServiceTraceMessage(b.goName + "KeysRequest"))
		buf.WriteString(stateServiceTraceMessage(b.goName + "SizeRequest"))
		buf.WriteString(stateServiceTraceMessage(
			b.goName+"ValueResponse",
			fmt.Sprintf("%s value = 1;", b.typeName),
			"bool found = 2;",
		))
		buf.WriteString(stateServiceTraceMessage(
			b.goName+"EntriesResponse",
			fmt.Sprintf("map<string, %s> entries = 1;", b.typeName),
		))
		buf.WriteString(stateServiceTraceMessage(b.goName+"KeysResponse", "repeated string keys = 1;"))
		buf.WriteString(stateServiceTraceMessage(b.goName+"SizeResponse", "int32 size = 1;"))
		buf.WriteString(stateServiceTraceMessage(b.goName+"QueryRequest", "CollectionQuery query = 1;"))
		buf.WriteString(stateServiceTraceMessage(
			b.goName+"QueryResponse",
			fmt.Sprintf("repeated %s results = 1;", b.typeName),
			"int32 total_count = 2;",
		))
		buf.WriteString("\n")
	}

	// Determine shared message needs from boundary types
	hasNonPrivateCollections := false
	for _, b := range collections {
		if !b.isPrivate {
			hasNonPrivateCollections = true
			break
		}
	}
	hasNonPrivateTopics := false
	for _, b := range topics {
		if !b.isPrivate {
			hasNonPrivateTopics = true
			break
		}
	}

	needs := sharedMessageNeeds{
		mapEmpty:              true, // always needed (singletons use it too via collection RPCs in same file)
		queueReceipt:          len(queues) > 0 || len(priorityQueues) > 0,
		eventBusRecordMeta:    hasNonPrivateTopics,
		leaderElection:        hasLeaderBoundary,
		collectionQuery:       hasNonPrivateCollections || len(views) > 0,
		emptyImport:           len(commands) > 0,
		traceImport:           len(singletons) > 0 || len(readOnlySingletons) > 0 || len(collections) > 0 || len(queues) > 0 || len(priorityQueues) > 0 || hasLeaderBoundary || len(views) > 0,
		timestampImport:       hasNonPrivateTopics,
		eventBusSupportImport: hasNonPrivateTopics,
	}

	return stateServiceResult{
		serviceProto:   buf.String(),
		needs:          needs,
		stateName:      stateName,
		singletons:     len(singletons),
		collections:    len(collections),
		queues:         len(queues),
		priorityQueues: len(priorityQueues),
		topics:         len(topics),
		views:          len(views),
		hasLeader:      hasLeaderBoundary,
		readOnly:       len(readOnlySingletons),
	}
}

// TopicSubscribeRecordName returns the generated state-service Subscribe envelope
// type for a topic boundary.
func TopicSubscribeRecordName(stateName, fieldName string) string {
	return strutil.ToPascalCase(stateName) + fieldName + "SubscribeRecord"
}

// appendStateServicesForFile processes all state roots in a single proto file,
// emitting shared messages exactly once regardless of how many roots exist.
// stateRootEntry pairs a state root message with its name and reads_state references.
type stateRootEntry struct {
	stateRoot  *parser.MessageDescriptor
	stateName  string
	readsState []string
	// authServices is the resolved S2S caller list for this root's generated
	// service (owner + declared state_import-ers). Empty only when the root is
	// disable_auth_message, where token validation is skipped entirely.
	authServices []string
}

func appendStateServicesForFile(
	tempProtoDir string,
	fileDesc *parser.FileDescriptor,
	roots []stateRootEntry,
	relPath string,
	pkgEmitted *sharedMessageNeeds,
	eventSurfaces map[string]stateEventSurface,
) error {
	// Build all service definitions and merge their shared message needs.
	var results []stateServiceResult
	var merged sharedMessageNeeds
	for _, root := range roots {
		r := buildStateService(fileDesc, root.stateRoot, root.stateName, root.readsState, root.authServices, eventSurfaces)
		results = append(results, r)
		merged.mapEmpty = merged.mapEmpty || r.needs.mapEmpty
		merged.queueReceipt = merged.queueReceipt || r.needs.queueReceipt
		merged.eventBusRecordMeta = merged.eventBusRecordMeta || r.needs.eventBusRecordMeta
		merged.leaderElection = merged.leaderElection || r.needs.leaderElection
		merged.collectionQuery = merged.collectionQuery || r.needs.collectionQuery
		merged.emptyImport = merged.emptyImport || r.needs.emptyImport
		merged.traceImport = merged.traceImport || r.needs.traceImport
		merged.timestampImport = merged.timestampImport || r.needs.timestampImport
		merged.eventBusSupportImport = merged.eventBusSupportImport || r.needs.eventBusSupportImport
	}

	// Read the proto file
	protoPath := filepath.Join(tempProtoDir, relPath)
	content, err := os.ReadFile(protoPath)
	if err != nil {
		return fmt.Errorf("failed to read proto file %s: %w", relPath, err)
	}
	text := string(content)

	// Ensure google/protobuf/empty.proto is imported (raw proto text manipulation)
	if merged.emptyImport {
		if !strings.Contains(text, `"google/protobuf/empty.proto"`) {
			importLine := `import "google/protobuf/empty.proto";` + "\n"
			if idx := strings.LastIndex(text, "import "); idx >= 0 {
				endOfLine := strings.Index(text[idx:], "\n")
				if endOfLine >= 0 {
					insertAt := idx + endOfLine + 1
					text = text[:insertAt] + importLine + text[insertAt:]
				}
			} else {
				if idx := strings.Index(text, ";\n"); idx >= 0 {
					text = text[:idx+2] + "\n" + importLine + text[idx+2:]
				}
			}
		}
	}

	// Ensure sys/trace.proto is imported for TraceRequest in Watch/Get RPCs.
	if merged.traceImport {
		if !strings.Contains(text, `"sys/trace.proto"`) {
			text = addImport(text, traceImport)
		}
	}
	if merged.timestampImport {
		if !strings.Contains(text, `"google/protobuf/timestamp.proto"`) {
			text = addImport(text, `import "google/protobuf/timestamp.proto";`)
		}
	}
	if merged.eventBusSupportImport {
		if !strings.Contains(text, `"`+EventBusSupportProtoPath+`"`) {
			text = addImport(text, `import "`+EventBusSupportProtoPath+`";`)
		}
	}

	// CollectionQuery types — emitted once per package
	if merged.collectionQuery && !pkgEmitted.collectionQuery {
		pkgEmitted.collectionQuery = true
		var cqBuf bytes.Buffer
		cqBuf.WriteString("\n// Collection query types (auto-injected)\n")
		cqBuf.WriteString("enum ComparisonOp {\n")
		cqBuf.WriteString("  COMPARISON_OP_UNSPECIFIED = 0;\n")
		cqBuf.WriteString("  COMPARISON_OP_EQ = 1;\n")
		cqBuf.WriteString("  COMPARISON_OP_NE = 2;\n")
		cqBuf.WriteString("  COMPARISON_OP_GT = 3;\n")
		cqBuf.WriteString("  COMPARISON_OP_GE = 4;\n")
		cqBuf.WriteString("  COMPARISON_OP_LT = 5;\n")
		cqBuf.WriteString("  COMPARISON_OP_LE = 6;\n")
		cqBuf.WriteString("  COMPARISON_OP_IN = 7;\n")
		cqBuf.WriteString("  COMPARISON_OP_NOT_IN = 8;\n")
		cqBuf.WriteString("  COMPARISON_OP_LIKE = 9;\n")
		cqBuf.WriteString("  COMPARISON_OP_IS_NULL = 10;\n")
		cqBuf.WriteString("  COMPARISON_OP_IS_NOT_NULL = 11;\n")
		cqBuf.WriteString("}\n")
		cqBuf.WriteString("enum SortDirection {\n")
		cqBuf.WriteString("  SORT_DIRECTION_UNSPECIFIED = 0;\n")
		cqBuf.WriteString("  SORT_DIRECTION_ASC = 1;\n")
		cqBuf.WriteString("  SORT_DIRECTION_DESC = 2;\n")
		cqBuf.WriteString("}\n")
		cqBuf.WriteString("enum LogicalOp {\n")
		cqBuf.WriteString("  LOGICAL_OP_UNSPECIFIED = 0;\n")
		cqBuf.WriteString("  LOGICAL_OP_AND = 1;\n")
		cqBuf.WriteString("  LOGICAL_OP_OR = 2;\n")
		cqBuf.WriteString("}\n")
		cqBuf.WriteString(stateServiceTraceMessage(
			"QueryValue",
			"oneof value { string string_value = 1; int64 int_value = 2; double double_value = 3; bool bool_value = 4; }",
		))
		cqBuf.WriteString(stateServiceTraceMessage(
			"Predicate",
			"string field_name = 1;",
			"ComparisonOp op = 2;",
			"QueryValue value = 3;",
			"repeated QueryValue values = 4;",
		))
		cqBuf.WriteString(stateServiceTraceMessage(
			"PredicateGroup",
			"LogicalOp op = 1;",
			"repeated Predicate predicates = 2;",
			"repeated PredicateGroup groups = 3;",
		))
		cqBuf.WriteString(stateServiceTraceMessage(
			"SortField",
			"string field_name = 1;",
			"SortDirection direction = 2;",
		))
		cqBuf.WriteString(stateServiceTraceMessage(
			"CollectionQuery",
			"PredicateGroup where = 1;",
			"repeated SortField order_by = 2;",
			"int32 limit = 3;",
			"int32 offset = 4;",
		))
		cqBuf.WriteString("\n")
		text += cqBuf.String()
	}

	// Append all service definitions
	for _, r := range results {
		text += r.serviceProto
	}

	// Shared messages — emitted once per package across all files.
	// pkgEmitted tracks what has already been written for this package so that
	// two files in the same package (e.g. services/ and vservices/ both using
	// package gitlabconnector) don't each emit MapEmpty, causing a duplicate.
	var shared strings.Builder
	if merged.mapEmpty && !pkgEmitted.mapEmpty {
		shared.WriteString(stateServiceTraceMessage("MapEmpty"))
		shared.WriteString("\n")
		pkgEmitted.mapEmpty = true
	}
	if merged.queueReceipt && !pkgEmitted.queueReceipt {
		shared.WriteString(stateServiceTraceMessage("QueueReceipt", "bool accepted = 1;", "int32 queue_size = 2;"))
		shared.WriteString(stateServiceTraceMessage("QueueSizeResponse", "int32 size = 1;"))
		shared.WriteString("\n")
		pkgEmitted.queueReceipt = true
	}
	if merged.eventBusRecordMeta && !pkgEmitted.eventBusRecordMeta {
		shared.WriteString(stateServiceMessage(
			"EventBusRecordMeta",
			false,
			"bytes key = 1;",
			"string topic = 2;",
			"int32 partition = 3;",
			"int64 offset = 4;",
			"google.protobuf.Timestamp timestamp = 5;",
		))
		shared.WriteString("\n")
		pkgEmitted.eventBusRecordMeta = true
	}
	if merged.leaderElection && !pkgEmitted.leaderElection {
		shared.WriteString(stateServiceTraceMessage(
			"LeaderRecord",
			"string leader_id = 1;",
			"int64 fence_token = 2;",
			"int64 lease_expiry_ms = 3;",
			"int64 term = 4;",
		))
		shared.WriteString(stateServiceTraceMessage("LeaderElectRequest", "string candidate_id = 1;"))
		shared.WriteString(stateServiceTraceMessage(
			"LeaderResignRequest",
			"string leader_id = 1;",
			"int64 fence_token = 2;",
		))
		shared.WriteString(stateServiceTraceMessage(
			"LeaderLeaseRenewal",
			"string leader_id = 1;",
			"int64 fence_token = 2;",
		))
		shared.WriteString("\n")
		pkgEmitted.leaderElection = true
	}
	text += shared.String()

	if err := os.WriteFile(protoPath, []byte(text), 0644); err != nil {
		return fmt.Errorf("failed to write proto file %s: %w", relPath, err)
	}

	for _, r := range results {
		zap.L().Info("Appended state service to proto",
			zap.String("state", r.stateName),
			zap.String("file", relPath),
			zap.Int("singletons", r.singletons),
			zap.Int("collections", r.collections),
			zap.Int("queues", r.queues),
			zap.Int("priorityQueues", r.priorityQueues),
			zap.Int("topics", r.topics),
			zap.Int("views", r.views),
			zap.Bool("hasLeaderBoundary", r.hasLeader),
			zap.Int("readOnly", r.readOnly))
	}

	return nil
}

type stateImportRootRef struct {
	relPath  string
	msg      *parser.MessageDescriptor
	exported bool
}

func indexStateImportRoots(allFiles map[string]*parser.FileDescriptor) map[string]stateImportRootRef {
	roots := make(map[string]stateImportRootRef)
	for relPath, fileDesc := range allFiles {
		for i := range fileDesc.Messages {
			msg := &fileDesc.Messages[i]
			ann, ok := msg.Annotations["service_state"]
			if !ok || ann.StringValue == "" {
				continue
			}
			_, exported := msg.Annotations["service_state_export"]
			if expAnn, ok := msg.Annotations["service_state_export"]; ok {
				exported = expAnn.BoolValue
			}
			roots[fileDesc.Package+"."+ann.StringValue] = stateImportRootRef{
				relPath:  relPath,
				msg:      msg,
				exported: exported,
			}
		}
	}
	return roots
}

func splitStateImportSource(source string) (root string, field string) {
	root = parser.StateImportRoot(source)
	idx := strings.LastIndex(source, ".")
	if root == "" || idx <= 0 || idx == len(source)-1 {
		return "", ""
	}
	return root, source[idx+1:]
}

func findStateImportSourceField(msg *parser.MessageDescriptor, fieldName string) *parser.FieldDescriptor {
	for i := range msg.Fields {
		if msg.Fields[i].Name == fieldName {
			return &msg.Fields[i]
		}
	}
	return nil
}

// stateBoundaryFieldAnnotations are the field-level annotations that only make
// sense on a message-typed state-root boundary field. The boundary collector
// skips every non-message field, so any of these on a scalar state-root field is
// silently dropped and generates no state surface — ValidateStateConsistency
// rejects that loudly (NEW-23 / H-3). Single source of truth for the set: each
// annotation's own spec.proto doc requires a message-typed field (queue/topic/
// priority_queue carry message items; command's field type is the request
// message; view_member/instance_keyed key a message collection; epoch_expiration/
// indexed/persistence mark a message boundary or a field inside a collection item;
// leader enables election on a queue/topic/pq boundary; collected_health/
// eviction_max_size/refresh_on_miss configure or extend a named boundary field).
//
// Note: sync_backup_count, async_backup_count, eviction_policy,
// eviction_max_size_policy, eviction_ttl_seconds, and eviction_max_idle_seconds
// are boundary annotations in spec.proto that no generator implements. They do
// not belong here — they are rejected outright, on a scalar root or anywhere
// else, by the parser.UnimplementedStatedbAnnotations loop above (GEN-ANN-1).
var stateBoundaryFieldAnnotations = []string{
	"epoch_expiration", "indexed", "persistence",
	"queue", "priority_queue", "topic", "command",
	"view_sources", "view_member", "instance_keyed",
	"leader", "collected_health", "eviction_max_size", "refresh_on_miss",
}

// ValidateStateConsistency checks boundary-shape rules on service_state messages:
// view_sources/view_member shape, state_import projections, instance_keyed, and
// disable_auth_message placement. (The per-field consistency model was removed; every
// message-typed boundary is convergent, so no consistency annotation is validated.)
// Returns an error listing violations.
func ValidateStateConsistency(inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	allFiles := dp.GetAllFiles()
	stateImportRoots := indexStateImportRoots(allFiles)

	var errors []string
	for relPath, fileDesc := range allFiles {
		for _, msg := range fileDesc.Messages {
			ann, hasState := msg.Annotations["service_state"]

			// Statedb tuning annotations spec.proto declares but no generator
			// consumes: refuse rather than let a durability or retention intent
			// generate nothing at all (GEN-ANN-1). Checked on every message, not
			// just state roots — off a state root they are doubly inert.
			for _, unimplemented := range parser.UnimplementedStatedbAnnotations() {
				if _, has := msg.Annotations[unimplemented]; has {
					errors = append(errors, fmt.Sprintf(
						"%s: message %s has (spec.%s), which tacplus declares but does not implement — "+
							"no generator reads it, so it would silently generate nothing; remove it",
						relPath, msg.Name, unimplemented))
				}
				for _, field := range msg.Fields {
					if _, has := field.Annotations[unimplemented]; has {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has (spec.%s), which tacplus declares but does not implement — "+
								"no generator reads it, so the boundary would silently get no %s behavior; "+
								"remove it (implemented today: (spec.eviction_max_size) for a size bound, "+
								"(spec.epoch_expiration) on a collection item for per-entry expiry)",
							relPath, msg.Name, field.Name, unimplemented, unimplemented))
					}
				}
			}

			// Rule 0b: disable_auth_message requires service_state
			_, hasStateDisableAuth := msg.Annotations["disable_auth_message"]
			if hasStateDisableAuth && (!hasState || ann.StringValue == "") {
				errors = append(errors, fmt.Sprintf(
					"%s: message %s has (spec.disable_auth_message) but no (spec.service_state) — "+
						"disable_auth_message is only allowed on state root messages",
					relPath, msg.Name))
			}

			// A malformed (spec.subscribe) source ref must fail loudly: the boundary
			// collector silently drops unparseable refs (if err != nil { continue }),
			// so the subscription would vanish with no diagnostic (MS-3).
			if subAnn, hasSub := msg.Annotations["subscribe"]; hasSub {
				for _, entry := range subAnn.SubscriptionEntries {
					if _, err := eventbus.ParseTopicRef(entry.Source); err != nil {
						errors = append(errors, fmt.Sprintf(
							"%s: message %s has a malformed (spec.subscribe) source %q: %v",
							relPath, msg.Name, entry.Source, err))
					}
				}
			}

			if !hasState || ann.StringValue == "" {
				continue
			}

			// Kernel holds no durable user data (decision 2026-09-25): it is
			// in-memory coordination plus its own identity snapshot, nothing else.
			// A boundary that needs to survive a cold start is a stratum record, so
			// (spec.persistence) on a state root routed to kernel is refused below —
			// unless the root is itself kernel's identity snapshot, the one thing
			// the decision says kernel does keep durably (signing keys, tenant
			// tree, grants, links, principals), marked explicitly via
			// (spec.service_state).identity_snapshot so the carve-out is
			// greppable rather than inferred from message name or content.
			isKernelLabelled := false
			if labelAnn, hasLabel := msg.Annotations["service_state_label"]; hasLabel {
				for _, label := range labelAnn.StringSliceValue {
					// "kernel" is a well-known label, not a proto-declared role: see
					// docs/USER.md "Well-Known Names" and https://github.com/tacplus-dev/tacplus/issues/1.
					if label == "kernel" {
						isKernelLabelled = true
						break
					}
				}
			}
			_, isIdentitySnapshot := msg.Annotations["service_state_identity_snapshot"]

			for _, field := range msg.Fields {
				// A map field cannot be a state-root boundary — boundaries are named
				// message types. Reject it loudly (every language) rather than silently
				// skipping it (as the Go client did) so the boundary model has one answer.
				if field.IsMap {
					errors = append(errors, fmt.Sprintf(
						"%s: field %s.%s is a map — a state-root boundary must be a named "+
							"message type; wrap the map in a message and use that field as the boundary",
						relPath, msg.Name, field.Name))
					continue
				}

				// A non-message field on a state root is not a boundary — the boundary
				// collector skips every non-message field, so any boundary/storage
				// annotation on it generates NO state surface at all. Reject it loudly
				// rather than emit nothing (NEW-23 / H-3). Per-entry expiry, for
				// example, belongs on an int64 field INSIDE the collection item message,
				// where epoch_expiration is valid (collections only).
				if field.TypeKind != "message" {
					for _, boundaryAnn := range stateBoundaryFieldAnnotations {
						if _, has := field.Annotations[boundaryAnn]; has {
							errors = append(errors, fmt.Sprintf(
								"%s: field %s.%s carries (spec.%s) but is a scalar on state root %s — "+
									"boundary/storage annotations require a message-typed boundary field",
								relPath, msg.Name, field.Name, boundaryAnn, msg.Name))
						}
					}
				}

				importAnn, hasStateImport := field.Annotations["state_import"]
				_, hasQueue := field.Annotations["queue"]
				_, hasPQ := field.Annotations["priority_queue"]
				_, hasTopic := field.Annotations["topic"]
				cmdAnn, hasCommand := field.Annotations["command"]
				isCommand := hasCommand && cmdAnn.BoolValue

				// View sources check
				vsAnn, hasViewSources := field.Annotations["view_sources"]
				isViewSources := hasViewSources && len(vsAnn.StringSliceValue) > 0

				// Every message-typed field of a state root is a convergent boundary; no
				// per-field boundary-model annotation is needed.

				// Rule 7: view_sources fields must be repeated
				if isViewSources && !field.IsRepeated {
					errors = append(errors, fmt.Sprintf(
						"%s: field %s.%s has (spec.view_sources) but is not repeated — "+
							"views must be repeated fields",
						relPath, msg.Name, field.Name))
				}

				// Rule 8: view_sources must reference at least 2 source fields
				if isViewSources && len(vsAnn.StringSliceValue) < 2 {
					errors = append(errors, fmt.Sprintf(
						"%s: field %s.%s has (spec.view_sources) with fewer than 2 sources — "+
							"views must reference at least 2 source collections",
						relPath, msg.Name, field.Name))
				}

				// view_member validation
				vmAnn, hasViewMember := field.Annotations["view_member"]
				isViewMember := hasViewMember && vmAnn.StringValue != ""

				if hasStateImport && importAnn.StringValue != "" {
					if field.TypeKind != "message" || field.IsMap {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has (spec.state_import) but is not a singular message field",
							relPath, msg.Name, field.Name))
					}
					if field.IsRepeated {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has (spec.state_import) on a repeated field — "+
								"state_import currently supports singular message projections only",
							relPath, msg.Name, field.Name))
					}
					if hasQueue || hasPQ || hasTopic || isCommand || isViewSources || isViewMember {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s combines (spec.state_import) with another boundary annotation — "+
								"state_import is a read-only projection and must stand alone",
							relPath, msg.Name, field.Name))
					}

					root, sourceFieldName := splitStateImportSource(importAnn.StringValue)
					if root == "" || sourceFieldName == "" {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has invalid (spec.state_import) %q; expected package.StateRoot.field",
							relPath, msg.Name, field.Name, importAnn.StringValue))
					} else if sourceRoot, ok := stateImportRoots[root]; !ok {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s references unknown state_import root %q",
							relPath, msg.Name, field.Name, root))
					} else if !sourceRoot.exported {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s references non-exported state_import root %q declared in %s",
							relPath, msg.Name, field.Name, root, sourceRoot.relPath))
					} else if sourceField := findStateImportSourceField(sourceRoot.msg, sourceFieldName); sourceField == nil {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s references missing source field %q on state_import root %q",
							relPath, msg.Name, field.Name, sourceFieldName, root))
					} else {
						if sourceField.TypeKind != "message" || sourceField.IsMap || sourceField.IsRepeated {
							errors = append(errors, fmt.Sprintf(
								"%s: field %s.%s references source field %s.%s, but state_import currently supports singular message projections only",
								relPath, msg.Name, field.Name, sourceRoot.msg.Name, sourceField.Name))
						}
						if sourceField.Type != field.Type {
							errors = append(errors, fmt.Sprintf(
								"%s: field %s.%s type %q does not match state_import source %s.%s type %q",
								relPath, msg.Name, field.Name, field.Type, sourceRoot.msg.Name, sourceField.Name, sourceField.Type))
						}
					}
				}

				if isViewMember && !field.IsRepeated {
					errors = append(errors, fmt.Sprintf(
						"%s: field %s.%s has (spec.view_member) but is not repeated — "+
							"view_member fields must be repeated collections",
						relPath, msg.Name, field.Name))
				}

				if isViewMember && isViewSources {
					errors = append(errors, fmt.Sprintf(
						"%s: field %s.%s has both (spec.view_member) and (spec.view_sources) — "+
							"a field cannot be both a view member and a view",
						relPath, msg.Name, field.Name))
				}

				if isViewMember && isCommand {
					errors = append(errors, fmt.Sprintf(
						"%s: field %s.%s has both (spec.view_member) and (spec.command) — "+
							"a field cannot be both a view member and a command",
						relPath, msg.Name, field.Name))
				}

				if isViewMember && (hasQueue || hasPQ || hasTopic) {
					errors = append(errors, fmt.Sprintf(
						"%s: field %s.%s has (spec.view_member) with queue/topic/priority_queue — "+
							"view_member is only valid on collection boundaries",
						relPath, msg.Name, field.Name))
				}

				// Rule 9: instance_keyed validation
				ikValidAnn, hasInstanceKeyed := field.Annotations["instance_keyed"]
				isInstanceKeyed := hasInstanceKeyed && ikValidAnn.BoolValue
				if isInstanceKeyed {
					if !field.IsRepeated {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has (spec.instance_keyed) but is not repeated",
							relPath, msg.Name, field.Name))
					}
					if hasQueue || hasPQ || hasTopic {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has (spec.instance_keyed) with queue/topic/priority_queue",
							relPath, msg.Name, field.Name))
					}
					if isViewSources {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has (spec.instance_keyed) with view_sources",
							relPath, msg.Name, field.Name))
					}
					if isCommand {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has (spec.instance_keyed) with command",
							relPath, msg.Name, field.Name))
					}
				}

				// Rule 3: persistence cannot be on queues/topics/priority_queues
				_, hasPersistence := field.Annotations["persistence"]
				if hasPersistence {
					if hasQueue || hasPQ || hasTopic {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has (spec.persistence) on a queue/topic/priority_queue — "+
								"persistence cannot be set on queues, topics, or priority queues",
							relPath, msg.Name, field.Name))
					}
					// Rule 3b: persistence cannot be on a kernel-labelled state root,
					// except kernel's own identity snapshot (identity_snapshot: true).
					if isKernelLabelled && !isIdentitySnapshot {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has (spec.persistence) on a kernel-labelled state root — "+
								"durable data belongs in stratum records",
							relPath, msg.Name, field.Name))
					}
				}

				// Rule 4: refresh_on_miss cannot be on queues/topics/priority_queues
				romAnn, hasROM := field.Annotations["refresh_on_miss"]
				if hasROM && romAnn.StringValue != "" {
					if hasQueue || hasPQ || hasTopic {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has (spec.refresh_on_miss) on a queue/topic/priority_queue — "+
								"refresh_on_miss cannot be set on queues, topics, or priority queues",
							relPath, msg.Name, field.Name))
					}
				}

				// Rule 10: boundary_callers only valid on queue, topic, priority_queue, or command fields
				bcAnn, hasBoundaryCallers := field.Annotations["boundary_callers"]
				if hasBoundaryCallers && len(bcAnn.StringSliceValue) > 0 {
					isQueueOrTopic := hasQueue || hasPQ || hasTopic
					if !isQueueOrTopic && !isCommand {
						errors = append(errors, fmt.Sprintf(
							"%s: field %s.%s has (spec.boundary_callers) but is not a queue, topic, priority_queue, or command — "+
								"boundary_callers is only valid on queue, topic, and command boundaries",
							relPath, msg.Name, field.Name))
					}
				}
			}
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("state consistency validation failed:\n  %s", strings.Join(errors, "\n  "))
	}
	return nil
}

// ValidateC2STopicActorIdentity checks that every C2S event topic's message
// type carries a server-authoritative SPIFFE identity field. C2S events are
// defined by spec.service_event with export: true.
//
// A topic type satisfies the rule if it has at least one string field annotated
// with either:
//   - (spec.identity_spiffe_subject): the acting principal's SUBJECT SPIFFE ID,
//     which EventBus stamps from the caller's verified token at publish time
//     (e.g. spiffe://<root>/tenants/<tenant>/users/<sub>). This is the case for
//     sensor topics where the authenticated caller IS the actor.
//   - (spec.identity_spiffe_resource): a RESOURCE SPIFFE ID that tacplus
//     constructs from sibling fields. This is the case for service-ingested
//     topics (e.g. gitlab-connector) where the publisher is not the actor and
//     the owner is a tenant/resource identity rather than a caller subject.
//
// Human-readable claims (e.g. actor_email) are optional event-time display
// metadata and are not required by this rule.
//
// This function also enforces the actor-provenance rules for the OPTIONAL
// (spec.identity_spiffe_actor) field (the delegated-mint actor, distinct
// from the required subject/resource identity above): at most one such
// field per topic type, and it must never also be (spec.store_principal),
// so the actor can never appear in a store's read filter.
func ValidateC2STopicActorIdentity(inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Build a lookup: type FQN → MessageDescriptor
	msgByFQN := make(map[string]*parser.MessageDescriptor)
	for _, fileDesc := range dp.GetAllFiles() {
		for i := range fileDesc.Messages {
			msg := &fileDesc.Messages[i]
			fqn := fileDesc.Package + "." + msg.Name
			msgByFQN[fqn] = msg
		}
	}

	var errors []string
	for relPath, fileDesc := range dp.GetAllFiles() {
		for _, msg := range fileDesc.Messages {
			// Only check service_event messages with export: true.
			if _, isEvent := msg.Annotations["service_event_label"]; !isEvent {
				continue
			}
			exportAnn, hasExport := msg.Annotations["service_state_export"]
			if !hasExport || !exportAnn.BoolValue {
				continue
			}

			for _, field := range msg.Fields {
				if !field.Annotations["topic"].BoolValue {
					continue
				}
				if field.TypeKind != "message" {
					continue
				}

				// Look up the topic's message type.
				typeDef, ok := msgByFQN[field.Type]
				if !ok {
					errors = append(errors, fmt.Sprintf(
						"%s: C2S topic %s.%s references type %s which was not found",
						relPath, msg.Name, field.Name, field.Type))
					continue
				}

				// Require a string field carrying a SPIFFE identity — either a
				// stampable subject SPIFFE or a constructed resource SPIFFE.
				// Also enforce the actor-provenance rule: at most one
				// (spec.identity_spiffe_actor) field per topic type (it's
				// optional, unlike the required-exactly-one subject/resource
				// identity above), and no field may be annotated both
				// (spec.identity_spiffe_actor) and (spec.store_principal) —
				// the actor must never appear in a store's read filter.
				hasSpiffeIdentity := false
				actorFieldCount := 0
				for _, f := range typeDef.Fields {
					if f.TypeKind != "scalar" || f.Type != "string" {
						continue
					}
					if f.Annotations["identity_spiffe_subject"].BoolValue {
						hasSpiffeIdentity = true
					}
					if _, hasResource := f.Annotations["identity_spiffe_resource"]; hasResource {
						hasSpiffeIdentity = true
					}
					if f.Annotations["identity_spiffe_actor"].BoolValue {
						actorFieldCount++
						if f.Annotations["store_principal"].BoolValue {
							errors = append(errors, fmt.Sprintf(
								"%s: C2S topic %s.%s has type %s field %s is annotated both "+
									"(spec.identity_spiffe_actor) and (spec.store_principal) — "+
									"the actor must never appear in a store's read filter",
								relPath, msg.Name, field.Name, field.Type, f.Name))
						}
					}
				}
				if !hasSpiffeIdentity {
					errors = append(errors, fmt.Sprintf(
						"%s: C2S topic %s.%s has type %s which is missing a required "+
							"SPIFFE identity field — every C2S event type must have a string "+
							"field annotated (spec.identity_spiffe_subject) or "+
							"(spec.identity_spiffe_resource) for server-authoritative identity",
						relPath, msg.Name, field.Name, field.Type))
				}
				if actorFieldCount > 1 {
					errors = append(errors, fmt.Sprintf(
						"%s: C2S topic %s.%s has type %s with %d fields annotated "+
							"(spec.identity_spiffe_actor) — at most one is allowed per topic type",
						relPath, msg.Name, field.Name, field.Type, actorFieldCount))
				}
			}
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("C2S topic actor identity validation failed:\n  %s", strings.Join(errors, "\n  "))
	}
	return nil
}

// hasServiceState returns true if any message in the file descriptor
// has an explicit (spec.service_state) annotation (not synthesized from service_event).
func hasServiceState(fileDesc *parser.FileDescriptor) bool {
	for _, msg := range fileDesc.Messages {
		if _, ok := msg.Annotations["service_state"]; ok {
			// Skip state synthesized from service_event — those are EventBus topic
			// declarations, not real state roots that would cause import conflicts.
			if _, isEvent := msg.Annotations["service_event_label"]; isEvent {
				continue
			}
			return true
		}
	}
	return false
}

// ValidateConsistencyImports checks that no proto file with (spec.service_state)
// imports another proto file that also has (spec.service_state).
// This ensures state boundaries are self-contained — a service's state cannot depend on
// another service's state.
func ValidateConsistencyImports(inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	allFiles := dp.GetAllFiles()

	// Build set of files that have state state
	stateFiles := make(map[string]bool)
	for relPath, fileDesc := range allFiles {
		if hasServiceState(fileDesc) {
			stateFiles[relPath] = true
		}
	}

	var errors []string
	for relPath, fileDesc := range allFiles {
		if !stateFiles[relPath] {
			continue
		}
		for _, imp := range fileDesc.Imports {
			if stateFiles[imp] {
				errors = append(errors, fmt.Sprintf(
					"%s imports %s — a proto file with (spec.service_state) "+
						"cannot import another proto file with (spec.service_state)",
					relPath, imp))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("consistency import validation failed:\n  %s", strings.Join(errors, "\n  "))
	}
	return nil
}

// StoreServiceName returns the gRPC service name for a service_store message.
// CredPair → CredPairStoreService
func StoreServiceName(msgName string) string {
	return msgName + "StoreService"
}

// AppendStoreServiceToProtos appends strongly-typed store service definitions
// to each proto file that contains service_store messages. For each store message,
// it appends:
//   - A gRPC service with Put/Get/Delete/List RPCs
//   - Wrapper request/response messages
//
// Store services are simpler than state services: no Watch, no fan-out,
// no boundary decomposition. The entire message is the value, keyed by
// the field annotated with store_key.
func AppendStoreServiceToProtos(tempProtoDir, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for store services: %w", err)
	}

	generated := make(map[string]bool)

	for relPath, fileDesc := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}

		var storeProtos []string
		needsEmpty := false

		for i := range fileDesc.Messages {
			ann, ok := fileDesc.Messages[i].Annotations["service_store"]
			if !ok || ann.StringValue == "" {
				continue
			}

			storeName := ann.StringValue
			key := fileDesc.Package + "." + storeName
			if generated[key] {
				continue
			}
			generated[key] = true

			// Find the store_key field.
			var keyFieldName string
			for _, field := range fileDesc.Messages[i].Fields {
				if skAnn, hasSK := field.Annotations["store_key"]; hasSK && skAnn.BoolValue {
					keyFieldName = field.Name
					break
				}
			}
			if keyFieldName == "" {
				zap.L().Debug("service_store has no store_key field; keyed store-service generation not required",
					zap.String("message", storeName), zap.String("file", relPath))
				continue
			}

			labels := fileDesc.Messages[i].Annotations["service_store_label"].StringSliceValue

			proto := buildStoreService(fileDesc.Package, storeName, keyFieldName, labels)
			storeProtos = append(storeProtos, proto)
			needsEmpty = true

			zap.L().Info("generated store service",
				zap.String("message", storeName),
				zap.String("key", keyFieldName),
				zap.Strings("labels", labels),
				zap.String("file", relPath))
		}

		if len(storeProtos) == 0 {
			continue
		}

		// Read the existing proto file and append store service definitions.
		filePath := filepath.Join(tempProtoDir, relPath)
		content, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read proto file %s: %w", relPath, err)
		}

		var buf bytes.Buffer
		buf.Write(content)

		// Add google.protobuf.Empty import if not already present.
		if needsEmpty && !bytes.Contains(content, []byte("google/protobuf/empty.proto")) {
			// Insert import at the top of the file (after syntax/package lines).
			// Actually, for appended services, google.protobuf.Empty is already
			// imported by the state service generation. If not, we add it here.
			buf.Reset()
			lines := strings.Split(string(content), "\n")
			importInserted := false
			for _, line := range lines {
				buf.WriteString(line)
				buf.WriteString("\n")
				if !importInserted && strings.HasPrefix(strings.TrimSpace(line), "import ") {
					buf.WriteString("import \"google/protobuf/empty.proto\";\n")
					importInserted = true
				}
			}
			if !importInserted {
				// Fallback: append import at end before the store service
				buf.WriteString("\nimport \"google/protobuf/empty.proto\";\n")
			}
		}

		for _, sp := range storeProtos {
			buf.WriteString(sp)
		}

		if err := os.WriteFile(filePath, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("failed to write proto file %s: %w", relPath, err)
		}
	}

	// Same post-condition state services carry: a store service emitted with an
	// empty caller list builds and ships, then denies every S2S call at runtime
	// with no other build-time signal.
	return ValidateGeneratedStoreServiceAuth(tempProtoDir)
}

// buildStoreService builds the proto text for a single store service. labels are
// the store's declared routing labels. (spec.event_label) is a singular option
// and cannot carry a set, so it is emitted only for a single-label store; a store
// declaring several routes through TypeInfo.service_store_label in registry.json,
// which carries all of them.
//
// ownerPkg is the package that declares the store message. It is the store
// service's caller list: an empty auth_services fails closed on every S2S call
// at runtime (see ValidateGeneratedStoreServiceAuth).
func buildStoreService(ownerPkg, msgName, keyFieldName string, labels []string) string {
	svcName := StoreServiceName(msgName)
	goKeyName := strutil.ToCamelCase(keyFieldName)

	var buf strings.Builder

	buf.WriteString("\n// ============================================================================\n")
	buf.WriteString("// Auto-generated Store Service (tacplus)\n")
	buf.WriteString("// ============================================================================\n")
	buf.WriteString("// tacplus:generated-service " + parser.SynthesizedKindStore + "\n")

	fmt.Fprintf(&buf, "service %s {\n", svcName)
	// All store services are S2S — accessible only by authorized services.
	buf.WriteString(formatAuthServicesOption([]string{ownerPkg}))
	if len(labels) == 1 {
		fmt.Fprintf(&buf, "\toption (spec.event_label) = \"%s\";\n", labels[0])
	}
	fmt.Fprintf(&buf, "\trpc Put(%s) returns (google.protobuf.Empty);\n", msgName)
	fmt.Fprintf(&buf, "\trpc Get(%sStoreKeyRequest) returns (%s);\n", msgName, msgName)
	fmt.Fprintf(&buf, "\trpc Delete(%sStoreKeyRequest) returns (google.protobuf.Empty);\n", msgName)
	fmt.Fprintf(&buf, "\trpc List(google.protobuf.Empty) returns (%sStoreListResponse);\n", msgName)
	buf.WriteString("}\n\n")

	fmt.Fprintf(&buf, "message %sStoreKeyRequest {\n", msgName)
	fmt.Fprintf(&buf, "\tstring %s = 1;\n", keyFieldName)
	buf.WriteString("}\n\n")

	fmt.Fprintf(&buf, "message %sStoreListResponse {\n", msgName)
	fmt.Fprintf(&buf, "\trepeated %s items = 1;\n", msgName)
	buf.WriteString("}\n\n")

	_ = goKeyName // used later for Go client generation

	return buf.String()
}
