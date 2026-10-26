// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"slices"
	"sort"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// TENANT-PINNED READS (issue #130).
//
// A tenant's per-service configuration (the generated tenant_config.TenantConfig,
// a (spec.per_service_config) message whose fields are each service's
// (spec.config_slice)) can be held by an exported (spec.service_state) root as a
// collection keyed by tenant, e.g. kernel.ConfigState.tenant_configs:
//
//	message TenantConfigEntry {
//	  string                     tenant_name = 1 [(spec.indexed) = true];
//	  tenant_config.TenantConfig config      = 2;
//	}
//	message ConfigState {
//	  option (spec.service_state) = {label: ["kernel"], export: true};
//	  repeated TenantConfigEntry tenant_configs = 2;
//	}
//
// Every service that owns a slice of that TenantConfig is then derived a
// read-only subscription to that ONE collection (InjectTenantConfigSubscriptions)
// and a field-scoped, tenant-pinned read grant on it. The grant is enforced by
// the SERVING side, never by the subscriber:
//
//   - a caller whose namespace is exactly /tenants/<t> sees only the entry whose
//     indexed key is <t>; a Get/ContainsKey of any other key is PERMISSION_DENIED,
//     and GetAll/KeySet/Size/Watch/Query/WatchQuery carry only its own entry;
//   - a caller whose namespace is /system or below sees every tenant's entry;
//   - a caller in any other namespace gets PERMISSION_DENIED on every pinned RPC;
//   - every entry delivered to a pinned caller is PROJECTED: only the key field
//     and, inside the TenantConfig, the caller's own slice field survive. Other
//     services' slices (an IdP client secret, say) never leave the server.
//
// The grant covers only the collection's read RPCs. It confers nothing on the
// root's other boundaries and no write, so the subscriber is NOT added to the
// root's service-level auth_services or to its SecurityPolicy.RO.
//
// The same rule serves the other holder of a per-service config, a (spec.grpc_config)
// Config (issue #135): when the owner's Config carries one
// map<string, TenantConfig> field (cross-test/proto-tenantslice: tenancy's
// tenants), the injected <Owner>ConfigState service's GetConfig and WatchConfig
// are the pinned reads. A pinned caller receives the Config reduced to that map,
// the map reduced to the admitted tenants' entries, each entry projected to the
// caller's slice; the Config's other fields are the owner's and never leave it.
//
// Owner and declared importers are untouched: the owner holds RW, and a service
// that declares a (spec.state_import)/(spec.state_imports) of the root keeps the
// whole-root read it asked for and is not pinned.

// Tenant-pinned namespaces. A tenant-owned instance's token namespace is
// exactly TenantNamespacePrefix + <tenant>; a shared platform service's is
// SystemNamespace or below it (a location-bearing /system/<location>).
const (
	TenantNamespacePrefix = "/tenants/"
	SystemNamespace       = "/system"
)

// TenantPinnedSlice is one subscriber of a tenant-pinned collection: the
// caller's location-agnostic service path, and the TenantConfig field holding
// its slice (by construction the same name — a slice field is named after the
// (spec.config_slice).service_name that owns it).
type TenantPinnedSlice struct {
	Caller     string
	SliceField string
}

// TenantPinnedBoundary is one collection of an exported state root that holds a
// per-service config, and the callers pinned on it.
type TenantPinnedBoundary struct {
	Package     string // owning proto package, e.g. "kernel"
	StateRoot   string // (spec.service_state) name, e.g. "ConfigState"
	ProtoFile   string // flat-tree relative path of the root's proto
	Field       string // the collection field, e.g. "tenant_configs"
	KeyField    string // the item's (spec.indexed) string field, e.g. "tenant_name"
	ConfigField string // the item's per_service_config field, e.g. "config"
	// MapField is set for a grpc_config Config instead of a collection: Field is
	// then the state root's singleton holding the Config (e.g. "config"),
	// MapField the Config's map<string, per_service_config> field (e.g.
	// "tenants"), and KeyField and ConfigField are empty.
	MapField string
	// ConfigType is the Config message's name, set with MapField.
	ConfigType string
	Slices     []TenantPinnedSlice
}

// IsConfigMap reports whether the boundary is a grpc_config Config's map rather
// than an exported collection.
func (b TenantPinnedBoundary) IsConfigMap() bool { return b.MapField != "" }

// StateService is the generated state service hosting the boundary.
func (b TenantPinnedBoundary) StateService() string { return StateServiceName(b.StateRoot) }

// BoundaryPascal is the boundary's name in its generated RPCs, the same
// spelling AppendStateServiceToProtos uses (GetAll<BoundaryPascal>, ...).
func (b TenantPinnedBoundary) BoundaryPascal() string { return strutil.ToCamelCase(b.Field) }

// Callers is every pinned caller's service path, sorted.
func (b TenantPinnedBoundary) Callers() []string {
	out := make([]string, 0, len(b.Slices))
	for _, s := range b.Slices {
		out = append(out, s.Caller)
	}
	return out
}

// TenantPinnedRPCKind names which read a pinned RPC is, so each language's
// wrapper renders the matching filter.
type TenantPinnedRPCKind string

const (
	TenantPinGet         TenantPinnedRPCKind = "get"          // <B>GetRequest → <B>ValueResponse
	TenantPinContainsKey TenantPinnedRPCKind = "contains_key" // <B>GetRequest → <B>ValueResponse
	TenantPinGetAll      TenantPinnedRPCKind = "get_all"      // → <B>EntriesResponse
	TenantPinKeySet      TenantPinnedRPCKind = "key_set"      // → <B>KeysResponse
	TenantPinSize        TenantPinnedRPCKind = "size"         // → <B>SizeResponse
	TenantPinWatch       TenantPinnedRPCKind = "watch"        // → stream <B>EntriesResponse
	TenantPinQuery       TenantPinnedRPCKind = "query"        // → <B>QueryResponse
	TenantPinWatchQuery  TenantPinnedRPCKind = "watch_query"  // → stream <B>QueryResponse

	// A grpc_config Config's singleton (TenantPinnedBoundary.IsConfigMap).
	TenantPinGetConfig   TenantPinnedRPCKind = "get_config"   // TraceRequest → <Config>
	TenantPinWatchConfig TenantPinnedRPCKind = "watch_config" // TraceRequest → stream <Config>
)

// TenantPinnedRPC is one read RPC of a pinned boundary.
type TenantPinnedRPC struct {
	Name     string
	Kind     TenantPinnedRPCKind
	Boundary TenantPinnedBoundary
}

// ReadRPCs is every read RPC AppendStateServiceToProtos emits for a plain
// collection boundary (for a Config singleton, its Get and Watch), in emission
// order. These, and only these, carry the pinned callers; every mutator keeps
// the service-level caller list.
func (b TenantPinnedBoundary) ReadRPCs() []TenantPinnedRPC {
	p := b.BoundaryPascal()
	if b.IsConfigMap() {
		return []TenantPinnedRPC{
			{Name: "Get" + p, Kind: TenantPinGetConfig, Boundary: b},
			{Name: "Watch" + p, Kind: TenantPinWatchConfig, Boundary: b},
		}
	}
	return []TenantPinnedRPC{
		{Name: "Get" + p, Kind: TenantPinGet, Boundary: b},
		{Name: "ContainsKey" + p, Kind: TenantPinContainsKey, Boundary: b},
		{Name: "GetAll" + p, Kind: TenantPinGetAll, Boundary: b},
		{Name: "KeySet" + p, Kind: TenantPinKeySet, Boundary: b},
		{Name: "Size" + p, Kind: TenantPinSize, Boundary: b},
		{Name: "Watch" + p, Kind: TenantPinWatch, Boundary: b},
		{Name: "Query" + p, Kind: TenantPinQuery, Boundary: b},
		{Name: "WatchQuery" + p, Kind: TenantPinWatchQuery, Boundary: b},
	}
}

// TenantPinnedRPCIndex maps "<package>.<StateService>/<Method>" to the pinned
// read it is, for every pinned boundary in the descriptor set. The wrapper
// emitters look methods up here; a method absent from it is not pinned.
func TenantPinnedRPCIndex(allFiles map[string]*parser.FileDescriptor) (map[string]TenantPinnedRPC, error) {
	bs, err := ResolveTenantPinnedBoundaries(allFiles)
	if err != nil {
		return nil, err
	}
	out := make(map[string]TenantPinnedRPC)
	for _, b := range bs {
		for _, rpc := range b.ReadRPCs() {
			out[b.Package+"."+b.StateService()+"/"+rpc.Name] = rpc
		}
	}
	return out, nil
}

// ResolveTenantPinnedBoundaries finds every collection of an exported
// (spec.service_state) root whose item holds a (spec.per_service_config)
// message, and pins on it each service owning a field of that message — except
// the root's owner and the services that declare a whole-root state import.
// Sorted by package, root, field; slices by caller.
//
// A shape that holds a per-service config but cannot be pinned is an error, not
// a skip: skipping would silently leave the slice owners with no subscription
// (the bug #130 reported), and pinning it wrongly would leak a tenant's config.
func ResolveTenantPinnedBoundaries(allFiles map[string]*parser.FileDescriptor) ([]TenantPinnedBoundary, error) {
	msgs := make(map[string]*parser.MessageDescriptor) // "pkg.Name" → message
	perServiceConfig := make(map[string]bool)
	for _, fd := range allFiles {
		for i := range fd.Messages {
			msg := &fd.Messages[i]
			key := fd.Package + "." + msg.Name
			msgs[key] = msg
			if _, ok := msg.Annotations["per_service_config"]; ok {
				perServiceConfig[key] = true
			}
		}
	}
	if len(perServiceConfig) == 0 {
		return nil, nil
	}
	importers := stateImportersByRoot(allFiles)

	paths := make([]string, 0, len(allFiles))
	for p := range allFiles {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var out []TenantPinnedBoundary
	for _, relPath := range paths {
		fd := allFiles[relPath]
		if IsInfraProto(relPath) {
			continue
		}
		for i := range fd.Messages {
			root := &fd.Messages[i]
			ann, ok := root.Annotations["service_state"]
			if !ok || ann.StringValue == "" {
				continue
			}
			rootKey := fd.Package + "." + ann.StringValue
			declared := make(map[string]bool)
			for _, pkg := range importers[rootKey] {
				declared[pkg] = true
			}
			if exp, ok := root.Annotations["service_state_export"]; !ok || !exp.BoolValue {
				b, err := configMapBoundary(fd, relPath, root, ann.StringValue, msgs, perServiceConfig, declared)
				if err != nil {
					return nil, err
				}
				if b != nil {
					out = append(out, *b)
				}
				continue
			}
			for _, field := range root.Fields {
				if !tenantPinCandidate(field) {
					continue
				}
				item, ok := msgs[field.Type]
				if !ok {
					continue
				}
				var cfgFields []parser.FieldDescriptor
				for _, f := range item.Fields {
					if f.TypeKind == "message" && !f.IsMap && perServiceConfig[f.Type] {
						cfgFields = append(cfgFields, f)
					}
				}
				if len(cfgFields) == 0 {
					continue
				}
				where := fmt.Sprintf("%s: %s.%s.%s", relPath, fd.Package, root.Name, field.Name)
				if _, disabled := root.Annotations["disable_auth_message"]; disabled {
					return nil, fmt.Errorf("%s holds a (spec.per_service_config) message, but the root declares (spec.disable_auth_message): a tenant-pinned read needs the caller's verified namespace, and this root verifies none", where)
				}
				if len(cfgFields) > 1 || cfgFields[0].IsRepeated {
					return nil, fmt.Errorf("%s: item %s must hold exactly one singular (spec.per_service_config) field to be served per tenant", where, field.Type)
				}
				keyField := ""
				for _, f := range item.Fields {
					if a, ok := f.Annotations["indexed"]; ok && a.BoolValue {
						keyField = f.Name
						if f.TypeKind != "scalar" || f.Type != "string" || f.IsRepeated {
							return nil, fmt.Errorf("%s: the (spec.indexed) key %s.%s must be a singular string — it is compared to the tenant of the caller's /tenants/<t> namespace", where, field.Type, f.Name)
						}
					}
				}
				if keyField == "" {
					return nil, fmt.Errorf("%s: item %s holds a (spec.per_service_config) message but has no (spec.indexed) string key naming the tenant", where, field.Type)
				}
				b := TenantPinnedBoundary{
					Package:     fd.Package,
					StateRoot:   ann.StringValue,
					ProtoFile:   relPath,
					Field:       field.Name,
					KeyField:    keyField,
					ConfigField: cfgFields[0].Name,
				}
				for _, slice := range msgs[cfgFields[0].Type].Fields {
					if slice.Name == fd.Package || declared[slice.Name] {
						continue
					}
					b.Slices = append(b.Slices, TenantPinnedSlice{Caller: slice.Name, SliceField: slice.Name})
				}
				if len(b.Slices) == 0 {
					continue
				}
				sort.Slice(b.Slices, func(i, j int) bool { return b.Slices[i].Caller < b.Slices[j].Caller })
				out = append(out, b)
			}
		}
	}
	return out, nil
}

// configMapBoundary resolves the pinned read of a grpc_config Config's
// map<string, per_service_config> (issue #135) held by the injected
// <Owner>ConfigState root. nil, nil when the root is not such a holder, or
// every slice owner is already served another way. The shapes it cannot
// filter per tenant are errors, never skipped: a skipped Config leaves the
// slice owners subscribed to a service that refuses them, and granting one
// unfiltered would show every tenant's entry.
func configMapBoundary(fd *parser.FileDescriptor, relPath string, root *parser.MessageDescriptor, stateRoot string,
	msgs map[string]*parser.MessageDescriptor, perServiceConfig, declared map[string]bool) (*TenantPinnedBoundary, error) {
	for _, field := range root.Fields {
		if field.TypeKind != "message" || field.IsRepeated || field.IsMap {
			continue
		}
		cfg, ok := msgs[field.Type]
		if !ok {
			continue
		}
		if _, isConfig := cfg.Annotations["grpc_config"]; !isConfig {
			continue
		}
		where := fmt.Sprintf("%s: %s.%s.%s", relPath, fd.Package, root.Name, field.Name)
		var maps []parser.FieldDescriptor
		for _, f := range cfg.Fields {
			switch {
			case f.IsMap && perServiceConfig[f.MapValueType]:
				maps = append(maps, f)
			case f.TypeKind == "message" && !f.IsMap:
				// One level of wrapper message, the depth the subscription
				// derivation looks through.
				if inner, ok := msgs[f.Type]; ok {
					for _, g := range inner.Fields {
						if g.IsMap && perServiceConfig[g.MapValueType] {
							return nil, fmt.Errorf("%s: %s holds a (spec.per_service_config) map inside %s; the server filters a Config's own map<string, per_service_config> field only, so move it onto %s", where, cfg.Name, f.Type, cfg.Name)
						}
					}
				}
			}
		}
		if len(maps) == 0 {
			continue
		}
		if _, disabled := root.Annotations["disable_auth_message"]; disabled {
			return nil, fmt.Errorf("%s holds a (spec.per_service_config) map, but the root declares (spec.disable_auth_message): a tenant-pinned read needs the caller's verified namespace, and this root verifies none", where)
		}
		if len(maps) > 1 {
			return nil, fmt.Errorf("%s: %s must hold exactly one map<string, per_service_config> field to be served per tenant, found %d", where, cfg.Name, len(maps))
		}
		if maps[0].MapKeyType != "string" {
			return nil, fmt.Errorf("%s: the key of %s.%s must be a string — it is compared to the tenant of the caller's /tenants/<t> namespace", where, cfg.Name, maps[0].Name)
		}
		b := TenantPinnedBoundary{
			Package:    fd.Package,
			StateRoot:  stateRoot,
			ProtoFile:  relPath,
			Field:      field.Name,
			MapField:   maps[0].Name,
			ConfigType: cfg.Name,
		}
		for _, slice := range msgs[maps[0].MapValueType].Fields {
			if slice.Name == fd.Package || declared[slice.Name] {
				continue
			}
			b.Slices = append(b.Slices, TenantPinnedSlice{Caller: slice.Name, SliceField: slice.Name})
		}
		if len(b.Slices) == 0 {
			return nil, nil
		}
		sort.Slice(b.Slices, func(i, j int) bool { return b.Slices[i].Caller < b.Slices[j].Caller })
		return &b, nil
	}
	return nil, nil
}

// tenantPinCandidate reports whether a state-root field is a plain collection
// boundary — the shape whose read RPCs ReadRPCs names. Queues, topics, views,
// commands, imports, instance-keyed and private fields are other surfaces.
func tenantPinCandidate(field parser.FieldDescriptor) bool {
	if field.TypeKind != "message" || !field.IsRepeated || field.IsMap {
		return false
	}
	if field.Number >= 200 && field.Number <= 203 {
		return false // injected trace fields
	}
	if _, private := field.Annotations["private"]; private {
		return false
	}
	for _, other := range []string{"queue", "priority_queue", "topic", "command", "view_sources", "state_import", "instance_keyed"} {
		if a, ok := field.Annotations[other]; ok && (a.BoolValue || a.StringValue != "" || len(a.StringSliceValue) > 0) {
			return false
		}
	}
	return true
}

// NarrowStateRoot returns the state root a generated client of it is built
// from: stateRoot itself, or — for a subscription narrowed to some boundaries
// (tactype.StateEntry.Boundaries, a tenant-pinned subscription) — a copy that
// keeps only those fields. Every full-service language's state-client generator
// calls it, so a narrowed client has no accessor and opens no Watch for a
// boundary its grant does not cover.
func NarrowStateRoot(config *tactype.Config, protoFile string, stateRoot *parser.MessageDescriptor) *parser.MessageDescriptor {
	ann := stateRoot.Annotations["service_state"]
	keep := config.BoundariesForState(protoFile, ann.StringValue)
	if len(keep) == 0 {
		return stateRoot
	}
	narrowed := *stateRoot
	narrowed.Fields = nil
	for _, f := range stateRoot.Fields {
		if slices.Contains(keep, f.Name) {
			narrowed.Fields = append(narrowed.Fields, f)
		}
	}
	return &narrowed
}
