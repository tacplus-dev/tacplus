// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// TENANT-PINNED READS, Go serving side (issue #130). The rule is stated once,
// in proto.ResolveTenantPinnedBoundaries; this file renders it into the wrapper
// of a state service the Go build hosts:
//
//   - goTenantPinPre    — after the caller is bound: resolve the pin (a non-pinned
//     caller resolves to nil and is served exactly as before), refuse a pinned
//     caller in a namespace that is neither /system nor /tenants/<t>, and refuse
//     a Get/ContainsKey of another tenant's key;
//   - goTenantPinPost   — after the handler returns: drop other tenants' entries
//     and project the survivors to the caller's slice;
//   - goTenantPinStream — the same filter on every frame of a Watch/WatchQuery.
//
// The helpers live in a file of their own beside the wrapper
// (<wrapper>_tenant_pin.go), with the wrapper's server name as prefix so two
// pinned state services in one package cannot collide.

// goTenantPinShapeError refuses a pinned RPC whose generated shape is not the
// one this file renders a filter for. Unreachable for the state-service RPCs
// AppendStateServiceToProtos emits; it exists so a change there fails
// generation instead of serving the RPC unfiltered.
func goTenantPinShapeError(method *parser.MethodInfo, pin proto.TenantPinnedRPC) error {
	stream := pin.Kind == proto.TenantPinWatch || pin.Kind == proto.TenantPinWatchQuery || pin.Kind == proto.TenantPinWatchConfig
	if method.ClientStreaming || method.ServerStreaming != stream || method.IsWatchCollection() || method.IsPageCollection() {
		return fmt.Errorf("%s is a tenant-pinned %s read, but its generated shape is not the one the Go wrapper filters; refusing to serve it unfiltered", method.Name, pin.Kind)
	}
	return nil
}

// tenantPin looks a method up in the pinned-read index.
func (es *egressSanitizer) tenantPin(servicePackage, serviceName, method string) (proto.TenantPinnedRPC, bool) {
	if es == nil {
		return proto.TenantPinnedRPC{}, false
	}
	pin, ok := es.tenantPins[servicePackage+"."+serviceName+"/"+method]
	return pin, ok
}

// goTenantPinResolver is the per-boundary resolver's name.
func goTenantPinResolver(pin proto.TenantPinnedRPC) string {
	return "resolve{{.ServerName}}TenantPin" + pin.Boundary.BoundaryPascal()
}

// goTenantPinPre renders the resolve and the key gate, spliced after the caller
// is bound. failRet is "return nil," (unary) or "return" (stream).
func goTenantPinPre(pin proto.TenantPinnedRPC, failRet string) string {
	out := fmt.Sprintf(`
	// Tenant-pinned read (%[1]s): a pinned caller is served its own tenant's
	// entry only, projected to its own config slice.
	__pin, err := %[2]s(caller)
	if err != nil {
		%[3]s err
	}
`, pin.Boundary.Field, goTenantPinResolver(pin), failRet)
	if pin.Kind == proto.TenantPinGet || pin.Kind == proto.TenantPinContainsKey {
		out += fmt.Sprintf(`	if !__pin.admits(req.GetKey()) {
		%s status.Error(codes.PermissionDenied, "%s: the key is another tenant's entry")
	}
`, failRet, pin.Name)
	}
	return out
}

// goTenantPinPost renders the response filter, spliced after the handler.
func goTenantPinPost(pin proto.TenantPinnedRPC) string {
	p := pin.Boundary.BoundaryPascal()
	switch pin.Kind {
	case proto.TenantPinGet, proto.TenantPinContainsKey:
		return fmt.Sprintf("\tresp = __pin.filter%sValue(resp)\n", p)
	case proto.TenantPinGetAll:
		return fmt.Sprintf("\tresp = __pin.filter%sEntries(resp)\n", p)
	case proto.TenantPinKeySet:
		return fmt.Sprintf("\tresp = __pin.filter%sKeys(resp)\n", p)
	case proto.TenantPinQuery:
		return fmt.Sprintf("\tresp = __pin.filter%sQuery(resp)\n", p)
	case proto.TenantPinGetConfig:
		return fmt.Sprintf("\tresp = __pin.filter%sMap(resp)\n", p)
	case proto.TenantPinSize:
		// The handler's size counts every tenant. A pinned caller is told how
		// many entries it can see, counted over the keys it is admitted to.
		return fmt.Sprintf(`	if __pin != nil {
		keys, err := p.handler.KeySet%[1]s(sanitizeContext(ctx), caller, &types.%[1]sKeysRequest{})
		if err != nil {
			return nil, err
		}
		resp = &types.%[1]sSizeResponse{Size: int32(len(__pin.filter%[1]sKeys(keys).GetKeys()))}
	}
`, p)
	}
	return ""
}

// goTenantPinStream wraps a server stream argument in the per-frame filter.
func goTenantPinStream(pin proto.TenantPinnedRPC, goOutput, streamArg string) string {
	filter := "Entries"
	switch pin.Kind {
	case proto.TenantPinWatchQuery:
		filter = "Query"
	case proto.TenantPinWatchConfig:
		filter = "Map"
	}
	return fmt.Sprintf("{{.ServerName}}TenantPinServerStream[%s]{ServerStreamingServer: %s, filter: __pin.filter%s%s}",
		goOutput, streamArg, pin.Boundary.BoundaryPascal(), filter)
}

// writeGoTenantPinFile writes the pinned-read helpers of one hosted state
// service. No-op when none of its RPCs is pinned.
func writeGoTenantPinFile(outputPath, pkgName, serverName, typesModulePath, serviceManagerImportPath string, pins []proto.TenantPinnedRPC) error {
	if len(pins) == 0 {
		return nil
	}
	boundaries := make(map[string]proto.TenantPinnedBoundary)
	for _, p := range pins {
		boundaries[p.Boundary.Field] = p.Boundary
	}
	fields := make([]string, 0, len(boundaries))
	for f := range boundaries {
		fields = append(fields, f)
	}
	sort.Strings(fields)

	var b bytes.Buffer
	b.WriteString(header.Copyright(consts.LangGo))
	fmt.Fprintf(&b, `
// Generated by service generator
//
// CODE GENERATED - DO NOT EDIT

package %[1]s

import (
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	servicemanager "%[3]s"
	types "%[2]s"
)

// %[4]sTenantPin is how one pinned caller reads a tenant-pinned collection
// (spec.per_service_config held by an exported state root): every tenant's
// entry when its namespace is /system or below, only the entry keyed by <t>
// when it is exactly /tenants/<t>, and in both cases each entry projected to
// the key and the caller's own config slice. A nil pin is a caller that is
// not pinned, and every method below is the identity for it.
type %[4]sTenantPin struct {
	all         bool
	tenant      string
	keyField    protoreflect.Name
	configField protoreflect.Name
	mapField    protoreflect.Name // set for a grpc_config Config's map (#135)
	sliceField  protoreflect.Name
}

// resolve%[4]sTenantPin pins caller when its service path is one of slices'
// keys; nil, nil when it is not. Fail closed: no verified service identity,
// or a pinned caller in any other namespace, is PermissionDenied.
func resolve%[4]sTenantPin(caller *servicemanager.Caller, slices map[string]string, keyField, configField string) (*%[4]sTenantPin, error) {
	if caller == nil || caller.Token() == nil {
		return nil, status.Error(codes.PermissionDenied, "a tenant-pinned read needs a verified caller")
	}
	path, err := caller.Token().ServicePath()
	if err != nil {
		return nil, status.Errorf(codes.PermissionDenied, "a tenant-pinned read needs the caller's service identity: %%v", err)
	}
	slice, pinned := slices[path]
	if !pinned {
		return nil, nil
	}
	pin := &%[4]sTenantPin{
		keyField:    protoreflect.Name(keyField),
		configField: protoreflect.Name(configField),
		sliceField:  protoreflect.Name(slice),
	}
	ns := caller.Token().GetNamespace()
	if ns == %[5]q || strings.HasPrefix(ns, %[5]q+"/") {
		pin.all = true
		return pin, nil
	}
	if t, ok := strings.CutPrefix(ns, %[6]q); ok && t != "" && !strings.Contains(t, "/") {
		pin.tenant = t
		return pin, nil
	}
	return nil, status.Errorf(codes.PermissionDenied, "a tenant-pinned read is served to %[5]s and %[6]s<t> callers only, not namespace %%q", ns)
}

// admits reports whether the entry keyed by key is the pinned caller's.
func (p *%[4]sTenantPin) admits(key string) bool {
	return p == nil || p.all || key == p.tenant
}

// keyOf is an entry's indexed key.
func (p *%[4]sTenantPin) keyOf(m proto.Message) string {
	r := m.ProtoReflect()
	return r.Get(r.Descriptor().Fields().ByName(p.keyField)).String()
}

// keepOnly clears every field of r but keep, unknown fields included.
func %[7]sTenantPinKeepOnly(r protoreflect.Message, keep ...protoreflect.Name) {
	var drop []protoreflect.FieldDescriptor
	r.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		for _, k := range keep {
			if fd.Name() == k {
				return true
			}
		}
		drop = append(drop, fd)
		return true
	})
	for _, fd := range drop {
		r.Clear(fd)
	}
	r.SetUnknown(nil)
}

// %[7]sTenantPinProject returns a copy of entry holding only its key and, inside
// its config, the pinned caller's own slice.
func %[7]sTenantPinProject[T proto.Message](p *%[4]sTenantPin, entry T) T {
	if p == nil || !entry.ProtoReflect().IsValid() {
		return entry
	}
	out := proto.Clone(entry).(T)
	r := out.ProtoReflect()
	%[7]sTenantPinKeepOnly(r, p.keyField, p.configField)
	if cfd := r.Descriptor().Fields().ByName(p.configField); cfd != nil && r.Has(cfd) {
		%[7]sTenantPinKeepOnly(r.Mutable(cfd).Message(), p.sliceField)
	}
	return out
}

// %[7]sTenantPinMap keeps the admitted entries of a keyed map, projected.
func %[7]sTenantPinMap[V proto.Message](p *%[4]sTenantPin, in map[string]V) map[string]V {
	kept := make(map[string]V, len(in))
	for k, v := range in {
		if p.admits(k) {
			kept[k] = %[7]sTenantPinProject(p, v)
		}
	}
	return kept
}

// %[7]sTenantPinList keeps the admitted entries of a list, projected.
func %[7]sTenantPinList[V proto.Message](p *%[4]sTenantPin, in []V) []V {
	kept := make([]V, 0, len(in))
	for _, v := range in {
		if p.admits(p.keyOf(v)) {
			kept = append(kept, %[7]sTenantPinProject(p, v))
		}
	}
	return kept
}

// %[4]sTenantPinServerStream filters every frame a pinned Watch sends.
type %[4]sTenantPinServerStream[T any] struct {
	grpc.ServerStreamingServer[T]
	filter func(*T) *T
}

func (s %[4]sTenantPinServerStream[T]) Send(msg *T) error {
	return s.ServerStreamingServer.Send(s.filter(msg))
}
`, pkgName, typesModulePath, serviceManagerImportPath, serverName, proto.SystemNamespace, proto.TenantNamespacePrefix, lowerFirst(serverName))

	for _, f := range fields {
		bd := boundaries[f]
		p := bd.BoundaryPascal()
		var slices []string
		for _, s := range bd.Slices {
			slices = append(slices, fmt.Sprintf("%q: %q", s.Caller, s.SliceField))
		}
		if bd.IsConfigMap() {
			fmt.Fprintf(&b, `
// resolve%[1]sTenantPin%[2]s pins the callers holding a tenant-pinned read of
// %[3]s.%[4]s (the %[5]s map of its Config), each to its own config slice.
func resolve%[1]sTenantPin%[2]s(caller *servicemanager.Caller) (*%[1]sTenantPin, error) {
	pin, err := resolve%[1]sTenantPin(caller, map[string]string{%[6]s}, "", "")
	if pin != nil {
		pin.mapField = protoreflect.Name(%[5]q)
	}
	return pin, err
}

// filter%[2]sMap returns the Config reduced to its tenant map, the map reduced
// to the admitted tenants' entries, each projected to the caller's slice. The
// Config's other fields are the owner's own and never leave it.
func (p *%[1]sTenantPin) filter%[2]sMap(r *types.%[7]s) *types.%[7]s {
	if p == nil || r == nil {
		return r
	}
	out := proto.Clone(r).(*types.%[7]s)
	m := out.ProtoReflect()
	%[8]sTenantPinKeepOnly(m, p.mapField)
	if fd := m.Descriptor().Fields().ByName(p.mapField); fd != nil && m.Has(fd) {
		entries := m.Mutable(fd).Map()
		var drop []protoreflect.MapKey
		entries.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			if !p.admits(k.String()) {
				drop = append(drop, k)
			} else {
				%[8]sTenantPinKeepOnly(v.Message(), p.sliceField)
			}
			return true
		})
		for _, k := range drop {
			entries.Clear(k)
		}
	}
	return out
}
`, serverName, p, bd.StateRoot, bd.Field, bd.MapField, strings.Join(slices, ", "), bd.ConfigType, lowerFirst(serverName))
			continue
		}
		fmt.Fprintf(&b, `
// resolve%[1]sTenantPin%[2]s pins the callers holding a tenant-pinned read of
// %[3]s.%[4]s, each to its own config slice.
func resolve%[1]sTenantPin%[2]s(caller *servicemanager.Caller) (*%[1]sTenantPin, error) {
	return resolve%[1]sTenantPin(caller, map[string]string{%[5]s}, %[6]q, %[7]q)
}

func (p *%[1]sTenantPin) filter%[2]sValue(r *types.%[2]sValueResponse) *types.%[2]sValueResponse {
	if p == nil || r == nil {
		return r
	}
	out := proto.Clone(r).(*types.%[2]sValueResponse)
	out.Value = %[8]sTenantPinProject(p, out.Value)
	return out
}

func (p *%[1]sTenantPin) filter%[2]sEntries(r *types.%[2]sEntriesResponse) *types.%[2]sEntriesResponse {
	if p == nil || r == nil {
		return r
	}
	out := proto.Clone(r).(*types.%[2]sEntriesResponse)
	out.Entries = %[8]sTenantPinMap(p, out.Entries)
	return out
}

func (p *%[1]sTenantPin) filter%[2]sKeys(r *types.%[2]sKeysResponse) *types.%[2]sKeysResponse {
	if p == nil || r == nil {
		return r
	}
	out := proto.Clone(r).(*types.%[2]sKeysResponse)
	kept := make([]string, 0, len(out.Keys))
	for _, k := range out.Keys {
		if p.admits(k) {
			kept = append(kept, k)
		}
	}
	out.Keys = kept
	return out
}

func (p *%[1]sTenantPin) filter%[2]sQuery(r *types.%[2]sQueryResponse) *types.%[2]sQueryResponse {
	if p == nil || r == nil {
		return r
	}
	out := proto.Clone(r).(*types.%[2]sQueryResponse)
	out.Results = %[8]sTenantPinList(p, out.Results)
	out.TotalCount = int32(len(out.Results))
	return out
}
`, serverName, p, bd.StateRoot, bd.Field, strings.Join(slices, ", "), bd.KeyField, bd.ConfigField, lowerFirst(serverName))
	}

	formatted, err := format.Source(b.Bytes())
	if err != nil {
		return fmt.Errorf("format %s: %w", outputPath, err)
	}
	return os.WriteFile(outputPath, formatted, 0644)
}

// goTenantPinFilePath is the helper file beside a wrapper at wrapperPath.
func goTenantPinFilePath(wrapperPath string) string {
	return strings.TrimSuffix(wrapperPath, filepath.Ext(wrapperPath)) + "_tenant_pin.go"
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
