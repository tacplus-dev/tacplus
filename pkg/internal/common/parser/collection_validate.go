package parser

import (
	"fmt"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// ValidateMethodCollections enforces the method_collection rules at generation
// time (see docs/API.md "Generation errors"). It accumulates ALL violations
// into one error to minimize fix-and-rebuild cycles, mirroring the graph/
// timeseries validators.
//
// index maps fully-qualified message names ("pkg.Name") to definitions for
// resolving the response collection's element type and computing item bytes
// (build via collection.BuildMessageIndex over the parsed files).
func ValidateMethodCollections(dp *DescriptorParser, index map[string]*model.Message) error {
	return validateMethodCollections(dp, index, nil)
}

// ValidateMethodCollectionsForPackages enforces method_collection rules for the
// requested proto packages. This lets per-service builds validate the generated
// surface they own without making unrelated packages in a monorepo block codegen.
// Passing no packages preserves the global validation behavior.
func ValidateMethodCollectionsForPackages(dp *DescriptorParser, index map[string]*model.Message, packages ...string) error {
	if len(packages) == 0 {
		return ValidateMethodCollections(dp, index)
	}
	allowed := make(map[string]bool, len(packages))
	for _, pkg := range packages {
		pkg = strings.TrimSpace(pkg)
		if pkg != "" {
			allowed[pkg] = true
		}
	}
	if len(allowed) == 0 {
		return ValidateMethodCollections(dp, index)
	}
	return validateMethodCollections(dp, index, allowed)
}

func validateMethodCollections(dp *DescriptorParser, index map[string]*model.Message, allowedPackages map[string]bool) error {
	if dp == nil {
		return nil
	}
	var errs []string
	for _, fd := range dp.GetAllFiles() {
		if len(allowedPackages) > 0 && !allowedPackages[fd.Package] {
			continue
		}
		for si := range fd.Services {
			svc := &fd.Services[si]
			for mi := range svc.Methods {
				m := &svc.Methods[mi]
				if m.Collection == nil {
					validateMissingCollectionAnnotation(svc, m, index, &errs)
					continue
				}
				loc := fmt.Sprintf("%s.%s", svc.Name, m.Name)
				validateOneCollection(loc, svc, m, index, &errs)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("method_collection annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func validateMissingCollectionAnnotation(svc *ServiceDescriptor, m *MethodDescriptor, index map[string]*model.Message, errs *[]string) {
	if svc == nil || m == nil || m.ClientStreaming || m.ServerStreaming {
		return
	}
	if !svc.IsS2S && !svc.IsC2S && !svc.IsC2D {
		return
	}
	outMsg := lookupMessage(m.OutputType, index)
	// PAGE-shaped but undeclared: a request carrying page_size/page_token or a
	// response carrying next_page_token is a paginated read that MUST declare
	// (spec.method_collection) = {mode: COLLECTION_MODE_PAGE, ...}. Without it the
	// page fields are silently inert and the method generates as an unbounded
	// unary read — the envelope validator only runs once PAGE is declared.
	if pageShaped(lookupMessage(m.InputType, index), outMsg) {
		*errs = append(*errs, fmt.Sprintf("%s.%s: request/response carry PAGE envelope fields (page_size/page_token/next_page_token) but the method does not declare (spec.method_collection) = {mode: COLLECTION_MODE_PAGE, ...}; declare it or remove the page fields",
			svc.Name, m.Name))
		return
	}
	if outMsg == nil {
		return
	}
	repeated := repeatedFields(outMsg)
	if len(repeated) != 1 || meaningfulFieldCount(outMsg) != 1 {
		return
	}
	*errs = append(*errs, fmt.Sprintf("%s.%s: unary response %q has a single repeated result field %q; Tier-1 collection reads must declare method_collection so tacplus can derive bounds, transport tier, flow control, and client/handler shape",
		svc.Name, m.Name, m.OutputType, repeated[0].Name))
}

// pageShaped reports whether a method's request or response carries the PAGE
// envelope's marker fields (page_size/page_token on the request, next_page_token
// on the response) — paginated intent that must be declared via
// COLLECTION_MODE_PAGE rather than left inert on an unbounded unary read.
func pageShaped(inMsg, outMsg *model.Message) bool {
	if inMsg != nil {
		if fieldByName(inMsg, "page_size") != nil || fieldByName(inMsg, "page_token") != nil {
			return true
		}
	}
	if outMsg != nil && fieldByName(outMsg, "next_page_token") != nil {
		return true
	}
	return false
}

func validateOneCollection(loc string, svc *ServiceDescriptor, m *MethodDescriptor, index map[string]*model.Message, errs *[]string) {
	c := m.Collection
	add := func(format string, a ...interface{}) {
		*errs = append(*errs, loc+": "+fmt.Sprintf(format, a...))
	}
	streaming := m.ServerStreaming
	outputPkg := pkgOf(m.OutputType)

	// --- mode + cross-field rules ---
	switch c.Mode {
	case "SNAPSHOT":
		if c.MaxItems == 0 {
			add("SNAPSHOT method_collection requires a positive max_items (an unbounded snapshot is a contradiction; use WATCH)")
		}
		if c.ReplayDepth != 0 {
			add("replay_depth is only valid on WATCH, not SNAPSHOT")
		}
	case "WATCH":
		if c.MaxItems != 0 {
			add("max_items is only valid on SNAPSHOT, not WATCH")
		}
		if !streaming {
			add("WATCH must be declared server-streaming: rpc %s(...) returns (stream Item)", m.Name)
		}
	case "PAGE":
		if c.MaxItems != 0 {
			add("max_items is only valid on SNAPSHOT, not PAGE (PAGE bounds each page with page_size)")
		}
		if c.ReplayDepth != 0 {
			add("replay_depth is only valid on WATCH, not PAGE")
		}
		if streaming {
			add("PAGE must be a unary RPC returning the page envelope, not server-streaming")
		}
		if c.DefaultPageSize == 0 {
			add("PAGE method_collection requires a positive default_page_size")
		}
		validatePageEnvelope(m.InputType, m.OutputType, index, add)
	default:
		add("method_collection requires mode SNAPSHOT, WATCH, or PAGE")
		return // cannot validate further without a mode
	}

	if len(c.OrderBy) == 0 {
		add("method_collection requires order_by (a stable total order is required for pagination/deltas)")
	}

	// --- resolve the item and its max byte size (hybrid transport) ---
	// SNAPSHOT streaming: the streamed output message IS the item.
	// WATCH streaming: the streamed output message is an explicit watch envelope;
	// snapshot_item is the item. See docs/API.md "Generated API contract".
	// Unary (Tier-1): the item is the single repeated field's element type.
	var itemBytes int64 = -1   // -1 = unresolved
	var itemMsg *model.Message // message whose fields order_by may reference
	outMsg := lookupMessage(m.OutputType, index)
	if c.Mode == "WATCH" {
		if outMsg == nil {
			add("cannot resolve WATCH envelope type %q", m.OutputType)
		} else {
			itemField := validateWatchEnvelope(outMsg, m.OutputType, add)
			if itemField != nil {
				if itemField.IsMessage {
					itemMsg = resolveFieldMessageInPackage(itemField, outputPkg, index)
					if itemMsg != nil {
						itemBytes = collection.MaxMessageBytes(itemMsg, fieldMessagePackage(itemField, outputPkg), index)
					} else {
						itemBytes = collection.ItemBytes(itemField, index)
					}
				} else {
					itemBytes = collection.ItemBytes(itemField, index)
				}
			}
		}
		validateWatchRequest(m.InputType, index, add)
	} else if streaming {
		if outMsg == nil {
			add("cannot resolve streamed item type %q", m.OutputType)
		} else {
			itemBytes = collection.MaxMessageBytes(outMsg, pkgOf(m.OutputType), index)
			itemMsg = outMsg
		}
	} else {
		if outMsg == nil {
			add("cannot resolve response message %q to locate the collection field", m.OutputType)
		} else {
			repeated := repeatedFields(outMsg)
			switch len(repeated) {
			case 0:
				add("response %q has no repeated field; a unary collection read must return exactly one repeated field", m.OutputType)
			case 1:
				if repeated[0].IsMessage {
					itemMsg = resolveFieldMessageInPackage(repeated[0], outputPkg, index)
					if itemMsg != nil {
						itemBytes = collection.MaxMessageBytes(itemMsg, fieldMessagePackage(repeated[0], outputPkg), index)
					} else {
						itemBytes = collection.ItemBytes(repeated[0], index)
					}
				} else {
					itemBytes = collection.ItemBytes(repeated[0], index)
				}
			default:
				names := make([]string, len(repeated))
				for i, f := range repeated {
					names[i] = f.Name
				}
				add("response %q has multiple repeated fields (%s); the collection item is ambiguous — exactly one is required", m.OutputType, strings.Join(names, ", "))
			}
		}
	}

	validateOrderByFields(c.OrderBy, itemMsg, m.OutputType, add)

	// --- trust-scaled ceilings ---
	effAuthDisabled := m.AuthDisabled || svc.AuthDisabled
	trust := collection.TrustOf(svc.IsC2S, svc.IsS2S, svc.IsC2D, effAuthDisabled)
	ceil := collection.CeilingsFor(trust)

	if c.Mode == "WATCH" && !ceil.AllowWatch {
		add("WATCH is not permitted at this caller-trust level (%s); an unauthenticated/anonymous caller cannot open a long-lived watch", trustName(trust))
	}

	if c.Mode == "SNAPSHOT" && c.MaxItems > 0 {
		if c.MaxItems > ceil.MaxItems {
			add("max_items=%d exceeds the ceiling for this caller-trust level (%s, max %d)", c.MaxItems, trustName(trust), ceil.MaxItems)
		}
		if ceil.InlineOnly && streaming {
			add("an anonymous (auth_disabled) collection read must be a single inline Tier-1 unary response, not server-streaming; require authentication or lower max_items")
		}
		if itemBytes >= 0 {
			tier := collection.TierFor(c.MaxItems, itemBytes)
			// tier <-> declaration consistency (hybrid transport).
			if !streaming && tier != collection.TierInline {
				add("max_items=%d x item size routes to Tier %d, which exceeds a single inline message; declare this method server-streaming: returns (stream Item)", c.MaxItems, int(tier))
			}
			if streaming && tier == collection.TierInline {
				add("max_items=%d x item size fits inline (Tier 1); declare this method unary returning a collection rather than server-streaming", c.MaxItems)
			}
			if tier == collection.TierCursor && !ceil.AllowCursor {
				add("max_items=%d x item size requires a Tier-3 cursor stream, which is not permitted at this caller-trust level (%s)", c.MaxItems, trustName(trust))
			}
		}
	}
}

func validateWatchRequest(inputType string, index map[string]*model.Message, add func(string, ...interface{})) {
	inMsg := lookupMessage(inputType, index)
	if inMsg == nil {
		add("cannot resolve WATCH request type %q", inputType)
		return
	}
	if fieldByName(inMsg, "from_revision") == nil {
		add("WATCH request %q must declare explicit int64 from_revision; tacplus does not inject it", inputType)
	}
}

func validateWatchEnvelope(outMsg *model.Message, outputType string, add func(string, ...interface{})) *model.Field {
	snapshotItem := fieldByName(outMsg, "snapshot_item")
	snapshotDone := fieldByName(outMsg, "snapshot_done")
	delta := fieldByName(outMsg, "delta")
	if snapshotItem == nil || snapshotDone == nil || delta == nil {
		add("WATCH return type %q must be a conformant envelope with snapshot_item, snapshot_done, and delta fields", outputType)
		return nil
	}
	if !snapshotItem.IsOneOf || !snapshotDone.IsOneOf || !delta.IsOneOf ||
		snapshotItem.OneOfName == "" ||
		snapshotItem.OneOfName != snapshotDone.OneOfName ||
		snapshotItem.OneOfName != delta.OneOfName {
		add("WATCH return type %q must put snapshot_item, snapshot_done, and delta in the same oneof", outputType)
	}
	if !snapshotItem.IsMessage {
		add("WATCH return type %q snapshot_item must be a message item", outputType)
		return nil
	}
	if strings.ToLower(snapshotDone.Type) != "int64" && strings.ToLower(snapshotDone.ProtoType) != "int64" {
		add("WATCH return type %q snapshot_done must be int64", outputType)
	}
	if !delta.IsMessage {
		add("WATCH return type %q delta must be a message", outputType)
	}
	return snapshotItem
}

// validatePageEnvelope checks the PAGE convention: the request carries an int32
// page_size and a string page_token; the response carries exactly one repeated
// items field plus a string next_page_token and an optional int64 total_count.
// Field names are the recognized convention (mirrors the WATCH envelope).
func validatePageEnvelope(inputType, outputType string, index map[string]*model.Message, add func(string, ...interface{})) {
	isType := func(f *model.Field, want ...string) bool {
		t, pt := strings.ToLower(f.Type), strings.ToLower(f.ProtoType)
		for _, w := range want {
			if t == w || pt == w {
				return true
			}
		}
		return false
	}
	if inMsg := lookupMessage(inputType, index); inMsg != nil {
		if ps := fieldByName(inMsg, "page_size"); ps == nil {
			add("PAGE request %q must declare a page_size field (int32)", inputType)
		} else if !isType(ps, "int32", "uint32") {
			add("PAGE request page_size must be int32/uint32, got %q", ps.Type)
		}
		if pt := fieldByName(inMsg, "page_token"); pt == nil {
			add("PAGE request %q must declare a page_token field (string)", inputType)
		} else if !isType(pt, "string") {
			add("PAGE request page_token must be string, got %q", pt.Type)
		}
	}
	outMsg := lookupMessage(outputType, index)
	if outMsg == nil {
		add("cannot resolve PAGE response type %q", outputType)
		return
	}
	if reps := repeatedFields(outMsg); len(reps) != 1 {
		add("PAGE response %q must have exactly one repeated items field (found %d)", outputType, len(reps))
	}
	if npt := fieldByName(outMsg, "next_page_token"); npt == nil {
		add("PAGE response %q must declare a next_page_token field (string)", outputType)
	} else if !isType(npt, "string") {
		add("PAGE response next_page_token must be string, got %q", npt.Type)
	}
	if tc := fieldByName(outMsg, "total_count"); tc != nil && !isType(tc, "int64") {
		add("PAGE response total_count must be int64 when present, got %q", tc.Type)
	}
}

func fieldByName(m *model.Message, name string) *model.Field {
	if m == nil {
		return nil
	}
	for i := range m.Fields {
		if m.Fields[i].Name == name {
			return &m.Fields[i]
		}
	}
	return nil
}

// repeatedFields returns the message's repeated (non-map) fields. Maps are
// excluded — the collection convention is a repeated field, not a map.
func repeatedFields(m *model.Message) []*model.Field {
	var out []*model.Field
	for i := range m.Fields {
		f := &m.Fields[i]
		if f.IsRepeated && !f.IsMap {
			out = append(out, f)
		}
	}
	return out
}

func meaningfulFieldCount(m *model.Message) int {
	if m == nil {
		return 0
	}
	n := 0
	for i := range m.Fields {
		switch m.Fields[i].Name {
		case "trace_id", "noop":
			continue
		default:
			n++
		}
	}
	return n
}

// validateOrderByFields checks each order_by entry names a field on the item
// message, when that message is resolvable. A scalar item (itemMsg == nil) has
// no sub-fields, so order_by refers to the value itself — nothing to resolve.
func validateOrderByFields(orderBy []string, itemMsg *model.Message, itemTypeRef string, add func(string, ...interface{})) {
	if itemMsg == nil {
		return
	}
	have := map[string]bool{}
	for i := range itemMsg.Fields {
		have[itemMsg.Fields[i].Name] = true
	}
	for _, ob := range orderBy {
		name := strings.TrimSpace(ob)
		if name == "" {
			continue
		}
		if !have[name] {
			add("order_by field %q does not exist on the collection item type %q", name, itemTypeRef)
		}
	}
}

// pkgOf returns the package portion of a fully-qualified type reference
// ("pkg.Name" -> "pkg"); empty if there is no dot.
func pkgOf(typeRef string) string {
	if i := strings.LastIndex(typeRef, "."); i >= 0 {
		return typeRef[:i]
	}
	return ""
}

func resolveFieldMessage(f *model.Field, index map[string]*model.Message) *model.Message {
	return resolveFieldMessageInPackage(f, "", index)
}

func fieldMessagePackage(f *model.Field, parentPkg string) string {
	if f.TypePackage != "" {
		return f.TypePackage
	}
	return parentPkg
}

func resolveFieldMessageInPackage(f *model.Field, parentPkg string, index map[string]*model.Message) *model.Message {
	if f.TypePackage != "" {
		if m, ok := index[f.TypePackage+"."+f.Type]; ok {
			return m
		}
	}
	if parentPkg != "" {
		if m, ok := index[parentPkg+"."+f.Type]; ok {
			return m
		}
	}
	if m, ok := index[f.Type]; ok {
		return m
	}
	return nil
}

// lookupMessage resolves an output-type reference (FQN "pkg.Name" or bare
// "Name") against the message index.
func lookupMessage(typeRef string, index map[string]*model.Message) *model.Message {
	if index == nil || typeRef == "" {
		return nil
	}
	if m, ok := index[typeRef]; ok {
		return m
	}
	if i := strings.LastIndex(typeRef, "."); i >= 0 {
		if m, ok := index[typeRef[i+1:]]; ok {
			return m
		}
	}
	return nil
}

func trustName(t collection.TrustTier) string {
	switch t {
	case collection.TrustAnon:
		return "anonymous/auth_disabled C2S"
	case collection.TrustC2SAuth:
		return "authenticated C2S"
	case collection.TrustS2S:
		return "S2S (internal)"
	case collection.TrustC2D:
		return "C2D (local)"
	default:
		return "unknown"
	}
}
