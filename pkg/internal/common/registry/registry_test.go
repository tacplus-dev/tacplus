package registry

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/llmdesc"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/verbs"
	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestCollectRpcProtosIncludesClassification(t *testing.T) {
	fds := []*parser.FileDescriptor{
		{
			Package: "contract",
			Services: []parser.ServiceDescriptor{
				{
					Name:  "ContractApi",
					IsC2S: true,
					Methods: []parser.MethodDescriptor{
						{
							Name:       "ListEventsPaged",
							InputType:  "ContractPageRequest",
							OutputType: "ContractPageResponse",
							Collection: &parser.CollectionDescriptor{
								Mode:            "PAGE",
								DefaultPageSize: 50,
								OrderBy:         []string{"id"},
							},
						},
					},
				},
			},
		},
	}

	got, err := CollectRpcProtos(fds)
	if err != nil {
		t.Fatalf("CollectRpcProtos returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("CollectRpcProtos returned %d RPCs, want 1", len(got))
	}
	rpc := got[0]
	if rpc.RpcVerb != "List" {
		t.Fatalf("rpc_verb = %q, want List", rpc.RpcVerb)
	}
	if rpc.RpcNoun != "EventsPaged" {
		t.Fatalf("rpc_noun = %q, want EventsPaged", rpc.RpcNoun)
	}
	if rpc.MethodPattern != "LIST" {
		t.Fatalf("method_pattern = %q, want LIST", rpc.MethodPattern)
	}
	if rpc.AccessKind != "READ" {
		t.Fatalf("access_kind = %q, want READ", rpc.AccessKind)
	}
	if rpc.VerbClass != "core" {
		t.Fatalf("verb_class = %q, want core", rpc.VerbClass)
	}
	if rpc.VerbStatus != "classified" {
		t.Fatalf("verb_status = %q, want classified", rpc.VerbStatus)
	}
	// PAGE is its own grant class — a PAGE read is admitted by ANY/PAGE, NOT READ
	// (conservative default: READ_ANY is bounded reads only).
	wantScopes := []string{"ANY", "PAGE"}
	if !reflect.DeepEqual(rpc.EntitlementScopes, wantScopes) {
		t.Fatalf("entitlement_scopes = %#v, want %#v", rpc.EntitlementScopes, wantScopes)
	}
	if rpc.RetryPolicy != "READ_ONLY" {
		t.Fatalf("retry_policy = %q, want READ_ONLY", rpc.RetryPolicy)
	}
	if rpc.CapabilityScope != "PAGE" {
		t.Fatalf("capability_scope = %q, want PAGE", rpc.CapabilityScope)
	}
	if rpc.HandlerShape != "UNARY" {
		t.Fatalf("handler_shape = %q, want UNARY", rpc.HandlerShape)
	}
	if rpc.VerbDefinition == "" || rpc.VerbBoundary == "" || rpc.AuditMeaning == "" {
		t.Fatalf("governed dictionary metadata must be populated, got definition=%q boundary=%q audit=%q",
			rpc.VerbDefinition, rpc.VerbBoundary, rpc.AuditMeaning)
	}
	wantRejected := []string{"Filter", "Find", "Query", "Search"}
	if !reflect.DeepEqual(rpc.RejectedVerbs, wantRejected) {
		t.Fatalf("rejected_verbs = %#v, want %#v", rpc.RejectedVerbs, wantRejected)
	}
	wantAllowedPatterns := []string{"LIST"}
	if !reflect.DeepEqual(rpc.AllowedPatterns, wantAllowedPatterns) {
		t.Fatalf("allowed_patterns = %#v, want %#v", rpc.AllowedPatterns, wantAllowedPatterns)
	}
}

func TestCollectRpcProtosRejectsVerbClassificationFailure(t *testing.T) {
	fds := []*parser.FileDescriptor{
		{
			Package: "contract",
			Services: []parser.ServiceDescriptor{
				{
					Name:  "ContractApi",
					IsC2S: true,
					Methods: []parser.MethodDescriptor{
						{
							Name:       "SearchEvents",
							InputType:  "ContractPageRequest",
							OutputType: "ContractPageResponse",
							Collection: &parser.CollectionDescriptor{
								Mode:            "PAGE",
								DefaultPageSize: 50,
								OrderBy:         []string{"id"},
							},
						},
					},
				},
			},
		},
	}

	got, err := CollectRpcProtos(fds)
	if err == nil {
		t.Fatalf("CollectRpcProtos returned %#v, want classification error", got)
	}
	if !strings.Contains(err.Error(), "contract.ContractApi/SearchEvents") || !strings.Contains(err.Error(), "folded synonym") {
		t.Fatalf("CollectRpcProtos error = %v, want method context and classifier reason", err)
	}
}

func TestCollectRpcProtosIncludesVerbStatusForNonFatalFailures(t *testing.T) {
	fds := []*parser.FileDescriptor{
		{
			Package: "events",
			Services: []parser.ServiceDescriptor{
				{
					Name:  "EventBus",
					IsS2S: true,
					Methods: []parser.MethodDescriptor{
						{
							Name:       "SearchEvents",
							InputType:  "SearchEventsRequest",
							OutputType: "SearchEventsResponse",
							Collection: &parser.CollectionDescriptor{Mode: "PAGE"},
						},
					},
				},
				{
					Name: "InternalDebug",
					Methods: []parser.MethodDescriptor{
						{
							Name:       "frobnicateEvents",
							InputType:  "DebugRequest",
							OutputType: "DebugResponse",
						},
					},
				},
			},
		},
	}

	got, err := CollectRpcProtos(fds)
	if err != nil {
		t.Fatalf("CollectRpcProtos returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("CollectRpcProtos returned %d RPCs, want 2", len(got))
	}
	byName := map[string]*spec.RpcInfo{}
	for _, rpc := range got {
		byName[rpc.Rpc] = rpc
	}
	if byName["SearchEvents"].VerbStatus != "unclassified" || byName["SearchEvents"].FoldedToVerb != "List" {
		t.Fatalf("SearchEvents status/fold = (%q, %q), want (unclassified, List)",
			byName["SearchEvents"].VerbStatus, byName["SearchEvents"].FoldedToVerb)
	}
	if byName["frobnicateEvents"].VerbStatus != "exempt" || byName["frobnicateEvents"].FoldedToVerb != "" {
		t.Fatalf("frobnicateEvents status/fold = (%q, %q), want (exempt, empty)",
			byName["frobnicateEvents"].VerbStatus, byName["frobnicateEvents"].FoldedToVerb)
	}
}

// PC-C-7: the C2S (external) surface is STRICT — a generated-auth C2S method
// that cannot classify HARD-FAILS generation, the complement of the lenient
// S2S/C2D path above. Documented in docs/API.md ("A C2S generated-auth method
// that cannot classify fails generation").
func TestCollectRpcProtosHardFailsUnclassifiableC2SMethod(t *testing.T) {
	fds := []*parser.FileDescriptor{
		{
			Package: "events",
			Services: []parser.ServiceDescriptor{
				{
					Name:  "EventBus",
					IsC2S: true,
					Methods: []parser.MethodDescriptor{
						{
							// Same non-canonical verb that is merely "unclassified"
							// on an S2S service — on C2S it must fail generation.
							Name:       "SearchEvents",
							InputType:  "SearchEventsRequest",
							OutputType: "SearchEventsResponse",
							Collection: &parser.CollectionDescriptor{Mode: "PAGE"},
						},
					},
				},
			},
		},
	}
	if _, err := CollectRpcProtos(fds); err == nil {
		t.Fatal("an unclassifiable C2S generated-auth method must fail generation (strict external surface)")
	}
}

func TestCollectRpcProtosIncludesNamespaceAuthMetadata(t *testing.T) {
	fds := []*parser.FileDescriptor{
		{
			Package: "identity",
			Services: []parser.ServiceDescriptor{
				{
					Name:              "TenantApi",
					IsC2S:             true,
					AuthEntitledUsers: true,
					Methods: []parser.MethodDescriptor{
						{
							Name:                 "GetTenant",
							InputType:            "TenantRequest",
							OutputType:           "TenantResponse",
							TargetNamespaceField: "tenant_namespace",
							HttpPath:             "/api/v1/identity/GetTenant",
							HttpMethods:          []string{"POST", "GET"},
							HttpAliasPath:        "/api/v1/tenants/current",
						},
					},
				},
				{
					Name:  "EventStream",
					IsC2S: true,
					Methods: []parser.MethodDescriptor{
						{
							Name:                 "PingTenantEvents",
							InputType:            "PingTenantEventsRequest",
							OutputType:           "PingTenantEventsResponse",
							AuthAnyUser:          true,
							TargetNamespaceField: "/io/example/tenant",
						},
						{
							Name:        "PingClusterEvents",
							InputType:   "PingClusterEventsRequest",
							OutputType:  "PingClusterEventsResponse",
							AuthAnyUser: true,
						},
					},
				},
				{
					Name:  "DaemonApi",
					IsC2D: true,
					Methods: []parser.MethodDescriptor{
						{
							Name:                 "GetDaemonStatus",
							InputType:            "DaemonStatusRequest",
							OutputType:           "DaemonStatusResponse",
							TargetNamespaceField: "tenant_namespace",
						},
					},
				},
			},
		},
	}

	got, err := CollectRpcProtos(fds)
	if err != nil {
		t.Fatalf("CollectRpcProtos returned error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("CollectRpcProtos returned %d RPCs, want 4", len(got))
	}
	byName := map[string]*spec.RpcInfo{}
	for _, rpc := range got {
		byName[rpc.Rpc] = rpc
	}

	entitled := byName["GetTenant"]
	if entitled.SubjectNamespaceSource != "token.namespace" {
		t.Fatalf("entitled subject_namespace_source = %q, want token.namespace", entitled.SubjectNamespaceSource)
	}
	if entitled.TargetNamespaceField != "tenant_namespace" || entitled.TargetNamespaceSource != "request.field" || entitled.TargetNamespace != "" {
		t.Fatalf("entitled namespace metadata = field %q source %q fixed %q, want request field tenant_namespace",
			entitled.TargetNamespaceField, entitled.TargetNamespaceSource, entitled.TargetNamespace)
	}
	entitledSurfaces := map[string]*spec.RpcSurfaceInfo{}
	for _, surface := range entitled.Surfaces {
		entitledSurfaces[surface.SurfaceKind] = surface
	}
	if len(entitledSurfaces) != 2 {
		t.Fatalf("entitled surfaces = %#v, want c2s and w2s", entitled.Surfaces)
	}
	if c2s := entitledSurfaces["c2s"]; c2s == nil || c2s.Transport != "grpc" || c2s.AuthModel != "c2s" {
		t.Fatalf("c2s surface = %#v, want grpc/c2s", c2s)
	}
	w2s := entitledSurfaces["w2s"]
	if w2s == nil {
		t.Fatalf("missing w2s surface in %#v", entitled.Surfaces)
	}
	if w2s.Transport != "grpc_over_rest" || w2s.AuthModel != "w2s" || w2s.HttpPath != "/api/v1/identity/GetTenant" {
		t.Fatalf("w2s surface transport/auth/path = (%q, %q, %q), want grpc_over_rest/w2s/route",
			w2s.Transport, w2s.AuthModel, w2s.HttpPath)
	}
	if !reflect.DeepEqual(w2s.HttpMethods, []string{"POST", "GET"}) || w2s.HttpAliasPath != "/api/v1/tenants/current" {
		t.Fatalf("w2s http metadata = (%#v, %q), want POST+GET and alias", w2s.HttpMethods, w2s.HttpAliasPath)
	}
	if !w2s.AuthEntitledUsers || w2s.SubjectNamespaceSource != "token.namespace" || w2s.TargetNamespaceSource != "request.field" || w2s.TargetNamespaceField != "tenant_namespace" {
		t.Fatalf("w2s auth metadata = entitled=%v subject=%q target_source=%q target_field=%q, want entitled token namespace/request field",
			w2s.AuthEntitledUsers, w2s.SubjectNamespaceSource, w2s.TargetNamespaceSource, w2s.TargetNamespaceField)
	}

	fixed := byName["PingTenantEvents"]
	if fixed.SubjectNamespaceSource != "token.namespace" {
		t.Fatalf("fixed subject_namespace_source = %q, want token.namespace", fixed.SubjectNamespaceSource)
	}
	if fixed.TargetNamespaceField != "/io/example/tenant" || fixed.TargetNamespaceSource != "literal" || fixed.TargetNamespace != "/io/example/tenant" {
		t.Fatalf("fixed namespace metadata = field %q source %q fixed %q, want literal tenant namespace",
			fixed.TargetNamespaceField, fixed.TargetNamespaceSource, fixed.TargetNamespace)
	}

	cluster := byName["PingClusterEvents"]
	if cluster.SubjectNamespaceSource != "token.namespace" || cluster.TargetNamespaceSource != "literal" || cluster.TargetNamespace != "/" {
		t.Fatalf("cluster auth_any_user metadata = subject %q source %q fixed %q, want token namespace + literal /",
			cluster.SubjectNamespaceSource, cluster.TargetNamespaceSource, cluster.TargetNamespace)
	}

	c2d := byName["GetDaemonStatus"]
	if c2d.AuthModel != "c2d" {
		t.Fatalf("c2d auth_model = %q, want c2d", c2d.AuthModel)
	}
	if c2d.SubjectNamespaceSource != "" || c2d.TargetNamespaceSource != "" || c2d.TargetNamespace != "" {
		t.Fatalf("c2d namespace metadata = subject %q source %q fixed %q, want empty local IPC metadata",
			c2d.SubjectNamespaceSource, c2d.TargetNamespaceSource, c2d.TargetNamespace)
	}
}

func TestBuildFieldInfoProtoIncludesOneofMetadata(t *testing.T) {
	got := buildFieldInfoProto(llmdesc.FieldDescriptor{
		Name:      "delta",
		Type:      "message EventDelta",
		OneOf:     true,
		OneOfName: "event",
	})

	if !got.GetOneof() || got.GetOneofName() != "event" {
		t.Fatalf("oneof metadata = (%v, %q), want (true, event)", got.GetOneof(), got.GetOneofName())
	}
}

func TestBuildFieldInfoProtoIncludesConstraintInfos(t *testing.T) {
	maxSize := int64(8)
	got := buildFieldInfoProto(llmdesc.FieldDescriptor{
		Name: "name",
		Type: "string",
		ConstraintInfos: []llmdesc.ConstraintDescriptor{
			{
				Rule:      "assert_max_size",
				Target:    "field",
				ValueKind: "int",
				IntValue:  &maxSize,
				Unit:      "bytes",
				Source:    "annotation",
			},
		},
	})
	if len(got.ConstraintInfos) != 1 {
		t.Fatalf("constraint_infos = %#v, want one", got.ConstraintInfos)
	}
	info := got.ConstraintInfos[0]
	if info.Rule != "assert_max_size" || info.IntValue == nil || *info.IntValue != 8 || info.Unit != "bytes" || info.Source != "annotation" {
		t.Fatalf("constraint info = %#v, want assert_max_size int 8 bytes annotation", info)
	}
}

// The registry must carry the per-field data-entitlement mode (from spec.redact)
// so downstream consumers and the sanitize generator can read the effective
// transform without re-parsing the proto.
func TestBuildFieldInfoProtoIncludesDataEntitlementMode(t *testing.T) {
	cases := map[string]spec.RedactMode{
		"drop":          spec.RedactMode_REDACT_MODE_DROP,
		"pseudonymize":  spec.RedactMode_REDACT_MODE_PSEUDONYMIZE,
		"may_redact":    spec.RedactMode_REDACT_MODE_MAY_REDACT,
		"may_anonymize": spec.RedactMode_REDACT_MODE_MAY_ANONYMIZE,
		"":              spec.RedactMode_REDACT_MODE_UNSPECIFIED,
		"bogus":         spec.RedactMode_REDACT_MODE_UNSPECIFIED,
	}
	for mode, want := range cases {
		got := buildFieldInfoProto(llmdesc.FieldDescriptor{
			Name:                "payload",
			Type:                "string",
			DataEntitlementMode: mode,
		})
		if got.GetDataEntitlementMode() != want {
			t.Errorf("mode %q -> %v, want %v", mode, got.GetDataEntitlementMode(), want)
		}
	}
}

// The registry must carry the per-field RecordMap filterability (from
// spec.query_field) so a consumer reading registry.json at runtime — with no
// compiled record type to inspect — knows whether a field can be filtered on and
// whether the generated <T>Filter criterion is EQ or a _from/_to RANGE pair.
// Cross-checked against the injector's actual output in
// pkg/internal/proto/record_filter_registry_test.go.
func TestBuildFieldInfoProtoIncludesFilterMode(t *testing.T) {
	cases := map[string]spec.QueryOp{
		"eq":    spec.QueryOp_QUERY_OP_EQ,
		"range": spec.QueryOp_QUERY_OP_RANGE,
		"":      spec.QueryOp_QUERY_OP_UNSPECIFIED,
		"bogus": spec.QueryOp_QUERY_OP_UNSPECIFIED,
	}
	for mode, want := range cases {
		got := buildFieldInfoProto(llmdesc.FieldDescriptor{
			Name:       "timestamp_ms",
			Type:       "int64",
			FilterMode: mode,
		})
		if got.GetFilterMode() != want {
			t.Errorf("mode %q -> %v, want %v", mode, got.GetFilterMode(), want)
		}
	}
}

// The message-level default (spec.data_entitlement) is recorded on TypeInfo.
func TestBuildTypeInfoProtoIncludesDataEntitlementDefault(t *testing.T) {
	got := BuildTypeInfoProto(llmdesc.RegistryTypeInfo{
		FQN: "demo.Event",
		Descriptor: llmdesc.TypeDescriptor{
			Name:                   "Event",
			Package:                "demo",
			DataEntitlementDefault: "may_redact",
			Fields: []llmdesc.FieldDescriptor{
				{Name: "email", Type: "string", DataEntitlementMode: "may_anonymize"},
			},
		},
	})
	if got.GetDataEntitlementDefault() != spec.RedactMode_REDACT_MODE_MAY_REDACT {
		t.Fatalf("message default = %v, want MAY_REDACT", got.GetDataEntitlementDefault())
	}
	if len(got.Fields) != 1 || got.Fields[0].GetDataEntitlementMode() != spec.RedactMode_REDACT_MODE_MAY_ANONYMIZE {
		t.Fatalf("field mode not recorded: %#v", got.Fields)
	}
}

func TestRegistryProtoSchemaDefsIncludeTacplusFields(t *testing.T) {
	defs := registryProtoSchemaDefs()
	fieldInfo := schemaDefProperties(t, defs, "FieldInfo")
	if _, ok := fieldInfo["constraint_infos"]; !ok {
		t.Fatalf("FieldInfo schema missing descriptor-derived constraint_infos field: %#v", fieldInfo)
	}
	packageNode := schemaDefProperties(t, defs, "PackageNode")
	for _, want := range []string{"event_publishes", "event_watches", "entitlement_grants"} {
		if _, ok := packageNode[want]; !ok {
			t.Fatalf("PackageNode schema missing descriptor-derived %s field: %#v", want, packageNode)
		}
	}
}

func TestRenderAgentMDIncludesGeneratedFieldReference(t *testing.T) {
	got, err := renderAgentMD(&spec.PackageNode{})
	if err != nil {
		t.Fatalf("renderAgentMD: %v", err)
	}
	for _, want := range []string{
		"## Generated Field Reference",
		"### FieldInfo",
		"`constraint_infos`",
		"### PackageNode",
		"`event_publishes`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("AGENT.md missing %q:\n%s", want, got)
		}
	}
}

func schemaDefProperties(t *testing.T, defs map[string]any, name string) map[string]any {
	t.Helper()
	def, ok := defs[name].(map[string]any)
	if !ok {
		t.Fatalf("schema def %s missing or wrong type: %#v", name, defs[name])
	}
	props, ok := def["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema def %s properties missing or wrong type: %#v", name, def)
	}
	return props
}

func TestBuildTypeInfoProtoIncludesServiceStoreMetadata(t *testing.T) {
	got := BuildTypeInfoProto(llmdesc.RegistryTypeInfo{
		ServiceStore:       true,
		ServiceStoreName:   "CredPair",
		ServiceStoreLabels: []string{"auth"},
		Descriptor: llmdesc.TypeDescriptor{
			Name:    "CredPair",
			Package: "auth",
			Fields: []llmdesc.FieldDescriptor{
				{Name: "client_id", Type: "string", StoreKey: true},
				{Name: "public_key", Type: "string", QuorumWrite: true},
			},
		},
	})

	if !got.GetServiceStore() || got.GetServiceStoreName() != "CredPair" || len(got.GetServiceStoreLabel()) != 1 || got.GetServiceStoreLabel()[0] != "auth" {
		t.Fatalf("service_store metadata = (%v, %q, %q), want (true, CredPair, auth)",
			got.GetServiceStore(), got.GetServiceStoreName(), got.GetServiceStoreLabel())
	}
	fields := map[string]*spec.FieldInfo{}
	for _, field := range got.GetFields() {
		fields[field.GetName()] = field
	}
	if !fields["client_id"].GetStoreKey() {
		t.Fatalf("client_id store_key = false, want true")
	}
	if !fields["public_key"].GetQuorumWrite() {
		t.Fatalf("public_key quorum_write = false, want true")
	}
}

func TestDeclaredClientDependenciesIncludeSurfaceKind(t *testing.T) {
	got := declaredClientDependencies([]*parser.FileDescriptor{
		{
			Package:           "frontend",
			AllGrpcS2SClients: true,
			GrpcS2SClients:    []string{"identity.Mint"},
			GrpcC2SClients:    []string{"gateway.TokenMint"},
			GrpcW2SClients:    []string{"dashboard.Dashboard"},
		},
	})

	want := []*spec.ClientDependencyInfo{
		{
			SourcePackage: "frontend",
			SurfaceKind:   "c2s",
			Service:       "gateway.TokenMint",
			TargetPackage: "gateway",
			TargetService: "TokenMint",
		},
		{
			SourcePackage: "frontend",
			SurfaceKind:   "s2s",
			Service:       "*",
			Wildcard:      true,
		},
		{
			SourcePackage: "frontend",
			SurfaceKind:   "s2s",
			Service:       "identity.Mint",
			TargetPackage: "identity",
			TargetService: "Mint",
		},
		{
			SourcePackage: "frontend",
			SurfaceKind:   "w2s",
			Service:       "dashboard.Dashboard",
			TargetPackage: "dashboard",
			TargetService: "Dashboard",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("client dependencies = %#v, want %#v", got, want)
	}
}

func TestCollectEventTopologyProtos(t *testing.T) {
	topicFd := &parser.FileDescriptor{
		Path:    "events/eventbus.proto",
		Package: "events",
		Services: []parser.ServiceDescriptor{
			{Name: "EventBus", GrpcName: "eventbus", IsS2S: true},
		},
		Messages: []parser.MessageDescriptor{
			{
				Name: "TraceEventBus",
				Annotations: map[string]model.AnnotationValue{
					"service_event_label": {Type: model.AnnotationTypeString, StringValue: "eventbus"},
				},
				Fields: []parser.FieldDescriptor{
					{
						Name: "trace_events",
						Type: "trace.TraceEvent",
						Annotations: map[string]model.AnnotationValue{
							"topic": {Type: model.AnnotationTypeBool, BoolValue: true},
						},
					},
				},
			},
		},
	}
	actorFd := &parser.FileDescriptor{
		Path:    "services/audit.proto",
		Package: "audit",
		Messages: []parser.MessageDescriptor{
			{
				Name: "AuditActor",
				Annotations: map[string]model.AnnotationValue{
					"event_publish": {
						EventPublishEntries: []model.EventBusBindingEntry{
							{
								TopicRef:  "events.TraceEventBus.trace_events",
								MethodS2S: &model.EventBusMethodS2S{AuthServices: []string{"audit"}},
							},
						},
					},
					"event_watch": {
						EventWatchEntries: []model.EventBusBindingEntry{
							{
								TopicRef: "events.TraceEventBus.trace_events",
								MethodC2S: &model.EventBusMethodC2S{
									AuthAnyUser:          true,
									TargetNamespaceField: "/tenant",
								},
							},
						},
					},
				},
			},
		},
	}
	allFds := []*parser.FileDescriptor{topicFd, actorFd}

	publishes, err := CollectEventPublishProtos([]*parser.FileDescriptor{actorFd}, allFds)
	if err != nil {
		t.Fatalf("CollectEventPublishProtos: %v", err)
	}
	if len(publishes) != 1 {
		t.Fatalf("publishes = %#v, want one", publishes)
	}
	pub := publishes[0]
	if pub.ActorPackage != "audit" || pub.ActorMessage != "AuditActor" || pub.TopicRef != "events.TraceEventBus.trace_events" {
		t.Fatalf("publish actor/topic = (%q, %q, %q), want audit/AuditActor/topic", pub.ActorPackage, pub.ActorMessage, pub.TopicRef)
	}
	if pub.EventTypeFqn != "trace.TraceEvent" || pub.BrokerLabel != "eventbus" || pub.RpcStem != "EventsTraceEventBusTraceEvents" {
		t.Fatalf("publish topic metadata = (%q, %q, %q), want trace.TraceEvent/eventbus/stem", pub.EventTypeFqn, pub.BrokerLabel, pub.RpcStem)
	}
	if pub.AuthModel != "s2s" || !reflect.DeepEqual(pub.AuthServices, []string{"audit"}) {
		t.Fatalf("publish auth = (%q, %#v), want s2s/audit", pub.AuthModel, pub.AuthServices)
	}

	watches, err := CollectEventWatchProtos([]*parser.FileDescriptor{actorFd}, allFds)
	if err != nil {
		t.Fatalf("CollectEventWatchProtos: %v", err)
	}
	if len(watches) != 1 {
		t.Fatalf("watches = %#v, want one", watches)
	}
	watch := watches[0]
	if watch.AuthModel != "c2s" || !watch.AuthAnyUser || watch.TargetNamespaceField != "/tenant" {
		t.Fatalf("watch auth = (%q, any=%v, target=%q), want c2s/any /tenant", watch.AuthModel, watch.AuthAnyUser, watch.TargetNamespaceField)
	}
	if watch.EventTypeFqn != "trace.TraceEvent" || watch.TopicRef != "events.TraceEventBus.trace_events" {
		t.Fatalf("watch topic metadata = (%q, %q), want trace.TraceEvent/topic", watch.EventTypeFqn, watch.TopicRef)
	}
}

func TestGenerateEmitsSmbRootProfiles(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping descriptor-backed registry test")
	}

	dir := t.TempDir()
	sysDir := filepath.Join(dir, "sys")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatalf("mkdir sys: %v", err)
	}
	specProto, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "sys", "spec.proto"))
	if err != nil {
		t.Fatalf("read spec.proto: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "spec.proto"), specProto, 0o644); err != nil {
		t.Fatalf("write spec.proto: %v", err)
	}
	svcDir := filepath.Join(dir, "svc")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatalf("mkdir svc: %v", err)
	}
	protoText := `syntax = "proto3";

package mysmb;

import "sys/spec.proto";

message Ping { string m = 1; }

service CacheService {
  option (spec.service_s2s) = {auth_services: ["*"], label: "CacheService"};
  option (spec.smb) = {};
  rpc Get(Ping) returns (Ping);
}

service KernelCacheService {
  option (spec.service_s2s) = {auth_services: ["*"], label: "KernelCacheService"};
  option (spec.smb) = {root: SMB_ROOT_CLUSTER};
  rpc Get(Ping) returns (Ping);
}

service AuthorityService {
  option (spec.service_s2s) = {auth_services: ["*"], label: "AuthorityService"};
  option (spec.smb_authority) = {};
  rpc Get(Ping) returns (Ping);
}
`
	if err := os.WriteFile(filepath.Join(svcDir, "cache.proto"), []byte(protoText), 0o644); err != nil {
		t.Fatalf("write proto: %v", err)
	}

	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	err = Generate(Config{
		OutputDir: out,
		InputDir:  dir,
		Language:  "none",
	}, []model.ProtoFile{{
		Package: "mysmb",
		RelPath: "svc/cache.proto",
	}}, dp)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(out, "registry.json"))
	if err != nil {
		t.Fatalf("read registry.json: %v", err)
	}
	registryJSON := string(data)
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`"smb_root":\s*"SMB_ROOT_ISOLATED"`),
		regexp.MustCompile(`"smb_root":\s*"SMB_ROOT_CLUSTER"`),
		regexp.MustCompile(`"smb_authority":\s*true`),
	} {
		if !want.MatchString(registryJSON) {
			t.Fatalf("registry.json missing %s:\n%s", want, registryJSON)
		}
	}
}

// TestGenerateEmitsTenantOwned proves registry.json carries the tenant_owned
// declaration from option (spec.service).tenant_owned, the single fact the
// operator (per-tenant Deployments), the gateway (route by token namespace),
// and the record-plane store wrapper all read to know a service is
// per-tenant instead of shared. Default false (omitted) for every other
// service, matching every other service today.
func TestGenerateEmitsTenantOwned(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping descriptor-backed registry test")
	}

	dir := t.TempDir()
	sysDir := filepath.Join(dir, "sys")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatalf("mkdir sys: %v", err)
	}
	specProto, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "sys", "spec.proto"))
	if err != nil {
		t.Fatalf("read spec.proto: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "spec.proto"), specProto, 0o644); err != nil {
		t.Fatalf("write spec.proto: %v", err)
	}
	svcDir := filepath.Join(dir, "svc")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatalf("mkdir svc: %v", err)
	}
	protoText := `syntax = "proto3";

package mytenantsvc;

import "sys/spec.proto";

option (spec.service) = {name: "mytenantsvc", tenant_owned: true};

message Ping { string m = 1; }

service PingService {
  option (spec.service_s2s) = {auth_services: ["*"], label: "PingService"};
  rpc Get(Ping) returns (Ping);
}
`
	if err := os.WriteFile(filepath.Join(svcDir, "ping.proto"), []byte(protoText), 0o644); err != nil {
		t.Fatalf("write proto: %v", err)
	}

	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	err = Generate(Config{
		OutputDir: out,
		InputDir:  dir,
		Language:  "none",
	}, []model.ProtoFile{{
		Package: "mytenantsvc",
		RelPath: "svc/ping.proto",
	}}, dp)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(out, "registry.json"))
	if err != nil {
		t.Fatalf("read registry.json: %v", err)
	}
	registryJSON := string(data)
	if !regexp.MustCompile(`"tenant_owned":\s*true`).MatchString(registryJSON) {
		t.Fatalf("registry.json missing tenant_owned: true:\n%s", registryJSON)
	}
}

// TestGenerateRegistryIsDeterministic proves registry.json is a function of the
// semantic inputs only: generating the same proto graph, service identity,
// language, and version to two different output directories produces
// byte-identical registry.json. This guards against non-semantic build
// provenance (wall-clock timestamp, source git SHA) leaking back into the
// contract.
func TestGenerateRegistryIsDeterministic(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping descriptor-backed registry test")
	}

	dir := t.TempDir()
	sysDir := filepath.Join(dir, "sys")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatalf("mkdir sys: %v", err)
	}
	specProto, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "sys", "spec.proto"))
	if err != nil {
		t.Fatalf("read spec.proto: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "spec.proto"), specProto, 0o644); err != nil {
		t.Fatalf("write spec.proto: %v", err)
	}
	svcDir := filepath.Join(dir, "svc")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatalf("mkdir svc: %v", err)
	}
	protoText := `syntax = "proto3";

package mysvc;

import "sys/spec.proto";

message Ping { string m = 1; }

service CacheService {
  option (spec.service_s2s) = {auth_services: ["*"], label: "CacheService"};
  rpc Get(Ping) returns (Ping);
}
`
	if err := os.WriteFile(filepath.Join(svcDir, "cache.proto"), []byte(protoText), 0o644); err != nil {
		t.Fatalf("write proto: %v", err)
	}

	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}

	// Two builds of the same semantic inputs, differing only in output directory.
	genOnce := func(outName string) []byte {
		out := filepath.Join(dir, outName)
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", outName, err)
		}
		if err := Generate(Config{
			OutputDir:  out,
			InputDir:   dir,
			Language:   "none",
			VersionTag: "1.2.3",
		}, []model.ProtoFile{{
			Package: "mysvc",
			RelPath: "svc/cache.proto",
		}}, dp); err != nil {
			t.Fatalf("Generate(%s): %v", outName, err)
		}
		data, err := os.ReadFile(filepath.Join(out, "registry.json"))
		if err != nil {
			t.Fatalf("read registry.json (%s): %v", outName, err)
		}
		return data
	}

	first := genOnce("out-a")
	second := genOnce("out-b")
	if !bytes.Equal(first, second) {
		t.Fatalf("registry.json is not deterministic across output dirs:\n--- out-a ---\n%s\n--- out-b ---\n%s", first, second)
	}
}

func TestGenerateUsesDeclaredProtoPackageAttribution(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping descriptor-backed registry test")
	}

	dir := t.TempDir()
	svcDir := filepath.Join(dir, "svc", "router")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatalf("mkdir svc/router: %v", err)
	}
	alphaProto := `syntax = "proto3";

package alpha;

message AlphaRequest { string id = 1; }
message AlphaResponse { string id = 1; }

service SharedService {
  rpc GetAlpha(AlphaRequest) returns (AlphaResponse);
}
`
	if err := os.WriteFile(filepath.Join(svcDir, "alpha.proto"), []byte(alphaProto), 0o644); err != nil {
		t.Fatalf("write alpha proto: %v", err)
	}
	betaProto := `syntax = "proto3";

package beta;

message BetaRequest { string id = 1; }
message BetaResponse { string id = 1; }

service SharedService {
  rpc GetBeta(BetaRequest) returns (BetaResponse);
}
`
	if err := os.WriteFile(filepath.Join(svcDir, "beta.proto"), []byte(betaProto), 0o644); err != nil {
		t.Fatalf("write beta proto: %v", err)
	}

	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	err = Generate(Config{
		OutputDir: out,
		InputDir:  dir,
		Language:  "none",
	}, []model.ProtoFile{
		{Package: "alpha", RelPath: "svc/router/alpha.proto"},
		{Package: "beta", RelPath: "svc/router/beta.proto"},
	}, dp)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(out, "registry.json"))
	if err != nil {
		t.Fatalf("read registry.json: %v", err)
	}
	var root spec.PackageNode
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal registry.json: %v", err)
	}
	leaf := findPackageNode(&root, "svc/router/router")
	if leaf == nil {
		t.Fatalf("missing svc/router/router leaf in registry: %s", data)
	}
	wantPackages := []string{"alpha", "beta"}
	if !reflect.DeepEqual(leaf.ProtoPackages, wantPackages) {
		t.Fatalf("proto_packages = %#v, want %#v", leaf.ProtoPackages, wantPackages)
	}

	services := map[string]*spec.ServiceInfo{}
	for _, svc := range leaf.Services {
		services[svc.GrpcService] = svc
	}
	alphaSvc := services["alpha.SharedService"]
	if alphaSvc == nil {
		t.Fatalf("missing alpha.SharedService; services=%#v", services)
	}
	if alphaSvc.Package != "alpha" || len(alphaSvc.Rpcs) != 1 || alphaSvc.Rpcs[0].Rpc != "GetAlpha" {
		t.Fatalf("alpha service attribution = package %q rpcs %#v, want alpha/GetAlpha", alphaSvc.Package, alphaSvc.Rpcs)
	}
	betaSvc := services["beta.SharedService"]
	if betaSvc == nil {
		t.Fatalf("missing beta.SharedService; services=%#v", services)
	}
	if betaSvc.Package != "beta" || len(betaSvc.Rpcs) != 1 || betaSvc.Rpcs[0].Rpc != "GetBeta" {
		t.Fatalf("beta service attribution = package %q rpcs %#v, want beta/GetBeta", betaSvc.Package, betaSvc.Rpcs)
	}
}

func TestGenerateEmitsVServiceIdentityAndHostTag(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping descriptor-backed registry test")
	}

	dir := t.TempDir()
	sysDir := filepath.Join(dir, "sys")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatalf("mkdir sys: %v", err)
	}
	specProto, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "sys", "spec.proto"))
	if err != nil {
		t.Fatalf("read spec.proto: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "spec.proto"), specProto, 0o644); err != nil {
		t.Fatalf("write spec.proto: %v", err)
	}
	vsvcDir := filepath.Join(dir, "vservices")
	if err := os.MkdirAll(vsvcDir, 0o755); err != nil {
		t.Fatalf("mkdir vservices: %v", err)
	}
	protoText := `syntax = "proto3";

package claudesensor;

import "sys/spec.proto";

option (spec.vservice) = {id: 30, name: "claudesensor"};

message Ping { string m = 1; }

service ClaudeSensorApi {
  option (spec.service_s2s) = {auth_services: ["*"], label: "sensorhost"};
  rpc GetPing(Ping) returns (Ping);
}
`
	if err := os.WriteFile(filepath.Join(vsvcDir, "claudesensor.proto"), []byte(protoText), 0o644); err != nil {
		t.Fatalf("write vservice proto: %v", err)
	}

	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	err = Generate(Config{
		OutputDir: out,
		InputDir:  dir,
		Language:  "none",
	}, []model.ProtoFile{{
		Package: "claudesensor",
		RelPath: "vservices/claudesensor.proto",
	}}, dp)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(out, "registry.json"))
	if err != nil {
		t.Fatalf("read registry.json: %v", err)
	}
	var root spec.PackageNode
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal registry.json: %v", err)
	}
	leaf := findPackageNode(&root, "vservices/claudesensor")
	if leaf == nil {
		t.Fatalf("missing vservices/claudesensor leaf in registry: %s", data)
	}
	if len(leaf.Vservices) != 1 {
		t.Fatalf("vservices = %#v, want one claudesensor entry", leaf.Vservices)
	}
	vsvc := leaf.Vservices[0]
	if vsvc.Name != "claudesensor" || vsvc.Id != 30 || vsvc.Package != "claudesensor" {
		t.Fatalf("vservice = (%q, %d, %q), want (claudesensor, 30, claudesensor)",
			vsvc.Name, vsvc.Id, vsvc.Package)
	}
	if len(leaf.Services) != 1 {
		t.Fatalf("services = %#v, want one service", leaf.Services)
	}
	if leaf.Services[0].HostLabel != "sensorhost" {
		t.Fatalf("host_label = %q, want sensorhost", leaf.Services[0].HostLabel)
	}
}

func TestGenerateFailsWhenDescriptorLookupIsMissing(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping descriptor-backed registry test")
	}

	dir := t.TempDir()
	protoText := `syntax = "proto3";

package registrytest;

message Present {
  string id = 1;
}
`
	if err := os.WriteFile(filepath.Join(dir, "present.proto"), []byte(protoText), 0o644); err != nil {
		t.Fatalf("write proto: %v", err)
	}
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	err = Generate(Config{
		OutputDir: out,
		InputDir:  dir,
		Language:  "none",
	}, []model.ProtoFile{{
		Package: "registrytest",
		RelPath: "missing.proto",
	}}, dp)
	if err == nil {
		t.Fatal("Generate succeeded with a missing file descriptor")
	}
	if !strings.Contains(err.Error(), "registry descriptor lookup for missing.proto") {
		t.Fatalf("Generate error = %v, want registry descriptor lookup context", err)
	}
	if _, statErr := os.Stat(filepath.Join(out, "registry.json")); !os.IsNotExist(statErr) {
		t.Fatalf("registry.json should not be written after descriptor lookup failure, stat err=%v", statErr)
	}
}

func TestCollectEntitlementGrantsEmitsExactAndCapabilityWildcards(t *testing.T) {
	root := &spec.PackageNode{
		Name: "types",
		Services: []*spec.ServiceInfo{
			{
				Name:    "OpenAIService",
				Package: "openai",
				Rpcs: []*spec.RpcInfo{
					{
						Rpc:               "GetAPIKey",
						AuthModel:         "c2s",
						EntitlementScopes: []string{"ANY", "READ"},
						CapabilityScope:   "READ",
						ClientStreaming:   false,
						ServerStreaming:   false,
						AuthEntitledUsers: true,
					},
					{
						Rpc:               "WatchAPIKeys",
						AuthModel:         "c2s",
						EntitlementScopes: []string{"ANY", "WATCH"},
						CapabilityScope:   "WATCH",
					},
					{
						Rpc:               "RotateAPIKey",
						AuthModel:         "s2s",
						EntitlementScopes: []string{"ANY", "WRITE"},
						CapabilityScope:   "WRITE",
					},
					{
						Rpc:          "GetPublicInfo",
						AuthModel:    "c2s",
						AuthDisabled: true,
					},
					{
						Rpc:         "GetUserInfo",
						AuthModel:   "c2s",
						AuthAnyUser: true,
					},
				},
			},
		},
	}

	byToken := entitlementGrantMap(collectEntitlementGrants(root))
	for _, want := range []string{
		"OPENAI_OPEN_AI_SERVICE_GET_API_KEY",
		"OPENAI_OPEN_AI_SERVICE_WATCH_API_KEYS",
		"OPENAI_ANY",
		"OPENAI_READ_ANY",
		"OPENAI_WATCH_ANY",
	} {
		if byToken[want] == nil {
			t.Fatalf("collectEntitlementGrants missing %s; got %#v", want, byToken)
		}
	}
	for _, denied := range []string{
		"OPENAI_OPEN_AI_SERVICE_ROTATE_API_KEY",
		"OPENAI_OPEN_AI_SERVICE_GET_PUBLIC_INFO",
		"OPENAI_OPEN_AI_SERVICE_GET_USER_INFO",
		"OPENAI_WRITE_ANY",
	} {
		if byToken[denied] != nil {
			t.Fatalf("non-entitled RPC must not produce C2S entitlement grant %s; got %#v", denied, byToken)
		}
	}
	wantAny := []string{"OPENAI_OPEN_AI_SERVICE_GET_API_KEY", "OPENAI_OPEN_AI_SERVICE_WATCH_API_KEYS"}
	if !reflect.DeepEqual(byToken["OPENAI_ANY"].ExpandsTo, wantAny) {
		t.Fatalf("OPENAI_ANY expands_to = %#v, want %#v", byToken["OPENAI_ANY"].ExpandsTo, wantAny)
	}
	wantRead := []string{"OPENAI_OPEN_AI_SERVICE_GET_API_KEY"}
	if !reflect.DeepEqual(byToken["OPENAI_READ_ANY"].ExpandsTo, wantRead) {
		t.Fatalf("OPENAI_READ_ANY expands_to = %#v, want %#v", byToken["OPENAI_READ_ANY"].ExpandsTo, wantRead)
	}
}

func TestCollectEntitlementGrantsDoesNotDefaultUnclassifiedC2SToPackageAny(t *testing.T) {
	root := &spec.PackageNode{
		Name: "types",
		Services: []*spec.ServiceInfo{
			{
				Name:    "Store",
				Package: "kv",
				Rpcs: []*spec.RpcInfo{
					{
						Rpc:       "SearchItems",
						AuthModel: "c2s",
					},
				},
			},
		},
	}

	byToken := entitlementGrantMap(collectEntitlementGrants(root))
	if byToken["KV_STORE_SEARCH_ITEMS"] == nil {
		t.Fatalf("collectEntitlementGrants missing exact unclassified C2S grant; got %#v", byToken)
	}
	if byToken["KV_ANY"] != nil || byToken["KV_READ_ANY"] != nil || byToken["KV_WRITE_ANY"] != nil {
		t.Fatalf("unclassified C2S method must not produce wildcard grants; got %#v", byToken)
	}
}

func TestCollectEntitlementGrantsIncludesGeneratedEventBusTopicMethods(t *testing.T) {
	root := &spec.PackageNode{Name: "types"}
	methods := []registryGrantMethod{
		// Per-topic service+rpc, matching the wire. A grant naming a service the
		// caller never invokes cannot authorize it.
		eventTopicGrantMethod("eventbus", "AgenticCodeEventBusStateService", "PublishToEventStream", []verbs.Scope{verbs.ScopeAny, verbs.ScopeWrite}, verbs.ScopeWrite),
		eventTopicGrantMethod("eventbus", "AgenticCodeEventBusStateService", "SubscribeToEventStream", []verbs.Scope{verbs.ScopeAny, verbs.ScopeWatch}, verbs.ScopeWatch),
		eventTopicGrantMethod("claudesensor", "AgenticCodeEventBusStateService", "PublishToEventStream", []verbs.Scope{verbs.ScopeAny, verbs.ScopeWrite}, verbs.ScopeWrite),
		eventTopicGrantMethod("claudesensor", "AgenticCodeEventBusStateService", "SubscribeToEventStream", []verbs.Scope{verbs.ScopeAny, verbs.ScopeWatch}, verbs.ScopeWatch),
	}

	byToken := entitlementGrantMap(collectEntitlementGrants(root, methods...))
	for _, want := range []string{
		"EVENTBUS_AGENTIC_CODE_EVENT_BUS_STATE_SERVICE_PUBLISH_TO_EVENT_STREAM",
		"EVENTBUS_AGENTIC_CODE_EVENT_BUS_STATE_SERVICE_SUBSCRIBE_TO_EVENT_STREAM",
		"EVENTBUS_ANY",
		"EVENTBUS_WRITE_ANY",
		"EVENTBUS_WATCH_ANY",
		"CLAUDESENSOR_AGENTIC_CODE_EVENT_BUS_STATE_SERVICE_PUBLISH_TO_EVENT_STREAM",
		"CLAUDESENSOR_AGENTIC_CODE_EVENT_BUS_STATE_SERVICE_SUBSCRIBE_TO_EVENT_STREAM",
		"CLAUDESENSOR_ANY",
		"CLAUDESENSOR_WRITE_ANY",
		"CLAUDESENSOR_WATCH_ANY",
	} {
		if byToken[want] == nil {
			t.Fatalf("collectEntitlementGrants missing generated EventBus grant %s; got %#v", want, byToken)
		}
	}

	wantEventBusAny := []string{
		"EVENTBUS_AGENTIC_CODE_EVENT_BUS_STATE_SERVICE_PUBLISH_TO_EVENT_STREAM",
		"EVENTBUS_AGENTIC_CODE_EVENT_BUS_STATE_SERVICE_SUBSCRIBE_TO_EVENT_STREAM",
	}
	if !reflect.DeepEqual(byToken["EVENTBUS_ANY"].ExpandsTo, wantEventBusAny) {
		t.Fatalf("EVENTBUS_ANY expands_to = %#v, want %#v", byToken["EVENTBUS_ANY"].ExpandsTo, wantEventBusAny)
	}
	wantClaudeWrite := []string{"CLAUDESENSOR_AGENTIC_CODE_EVENT_BUS_STATE_SERVICE_PUBLISH_TO_EVENT_STREAM"}
	if !reflect.DeepEqual(byToken["CLAUDESENSOR_WRITE_ANY"].ExpandsTo, wantClaudeWrite) {
		t.Fatalf("CLAUDESENSOR_WRITE_ANY expands_to = %#v, want %#v", byToken["CLAUDESENSOR_WRITE_ANY"].ExpandsTo, wantClaudeWrite)
	}
}

// A routing-alias grant (physical dial package, e.g. "eventbus") must attach
// its package to the contract grant's RoutingAliasPackages so a Java consumer
// scoping a token to the physical audience can still keep the contract grant.
// Mirrors the real DelegationProbe fleet shape: TASKS_DELEGATION_PROBE_BUS_
// STATE_SERVICE_PUBLISH_TO_STREAM (contract) / EVENTBUS_DELEGATION_PROBE_BUS_
// STATE_SERVICE_PUBLISH_TO_STREAM (alias) share service+rpc but not package.
func TestCollectEntitlementGrantsAttachesRoutingAliasPackageToContractGrant(t *testing.T) {
	root := &spec.PackageNode{
		Name: "types",
		Services: []*spec.ServiceInfo{
			{
				Name:    "DelegationProbeBusStateService",
				Package: "tasks",
				Rpcs: []*spec.RpcInfo{
					{
						Rpc:       "PublishToStream",
						AuthModel: "c2s",
					},
				},
			},
		},
	}
	alias := eventTopicGrantMethod("eventbus", "DelegationProbeBusStateService", "PublishToStream",
		[]verbs.Scope{verbs.ScopeAny, verbs.ScopeWrite}, verbs.ScopeWrite)
	alias.isRoutingAlias = true
	alias.routingAliasContractPackage = "tasks"

	byToken := entitlementGrantMap(collectEntitlementGrants(root, alias))

	contract := byToken["TASKS_DELEGATION_PROBE_BUS_STATE_SERVICE_PUBLISH_TO_STREAM"]
	if contract == nil {
		t.Fatalf("missing contract grant; got %#v", byToken)
	}
	if !reflect.DeepEqual(contract.RoutingAliasPackages, []string{"eventbus"}) {
		t.Fatalf("contract grant RoutingAliasPackages = %#v, want [eventbus]", contract.RoutingAliasPackages)
	}

	aliasGrant := byToken["EVENTBUS_DELEGATION_PROBE_BUS_STATE_SERVICE_PUBLISH_TO_STREAM"]
	if aliasGrant == nil {
		t.Fatalf("missing alias grant; got %#v", byToken)
	}
	if len(aliasGrant.RoutingAliasPackages) != 0 {
		t.Fatalf("alias grant must not carry its own RoutingAliasPackages, got %#v", aliasGrant.RoutingAliasPackages)
	}
}

func TestValidateEntitlementGrantTokenCollisionsRejectsDuplicates(t *testing.T) {
	err := validateEntitlementGrantTokenCollisions([]*spec.EntitlementGrantInfo{
		{
			Token:       "OPENAI_READ_ANY",
			Package:     "openai",
			ServiceName: "Read",
			Rpc:         "Any",
		},
		{
			Token:           "OPENAI_READ_ANY",
			Package:         "openai",
			CapabilityScope: "READ",
			ExpandsTo:       []string{"OPENAI_OPEN_AI_SERVICE_GET_API_KEY"},
		},
	})
	if err == nil {
		t.Fatal("validateEntitlementGrantTokenCollisions succeeded with duplicate tokens")
	}
	for _, want := range []string{
		`entitlement grant token collision "OPENAI_READ_ANY"`,
		"exact openai.Read.Any",
		"wildcard openai.READ",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("collision error = %v, want %q", err, want)
		}
	}
}

func entitlementGrantMap(grants []*spec.EntitlementGrantInfo) map[string]*spec.EntitlementGrantInfo {
	out := make(map[string]*spec.EntitlementGrantInfo, len(grants))
	for _, grant := range grants {
		out[grant.Token] = grant
	}
	return out
}

func findPackageNode(root *spec.PackageNode, fullPath string) *spec.PackageNode {
	if root.GetFullPath() == fullPath {
		return root
	}
	for _, child := range root.GetChildren() {
		if found := findPackageNode(child, fullPath); found != nil {
			return found
		}
	}
	return nil
}

// recordPlaneFds is the shape the record query plane really has in a build: the
// trait's own file (package "query") carries BOTH synthesized services and names
// the host through (spec.service_*).label, while option (spec.service).tenant_owned
// lives on the HOST's file, in a different package and a different leaf directory.
// The host's flag is still varied across these cases, to prove tenant_scope no
// longer depends on it.
func recordPlaneFds(hostTenantOwned bool) []*parser.FileDescriptor {
	readRpcs := []parser.MethodDescriptor{
		{Name: "CreateWidgetQuery", InputType: "OpenWidgetQueryRequest", OutputType: "QueryHandle", AuthServices: []string{"argus"}},
		{Name: "ListWidget", InputType: "PullWidgetRequest", OutputType: "PullWidgetResponse", AuthServices: []string{"argus"},
			Collection: &parser.CollectionDescriptor{Mode: "PAGE", DefaultPageSize: 100, OrderBy: []string{"widget_id"}}},
	}
	return []*parser.FileDescriptor{
		{
			Package:            "stratum",
			ServiceTenantOwned: hostTenantOwned,
			Services: []parser.ServiceDescriptor{
				{
					Name:         "StratumAdmin",
					IsS2S:        true,
					AuthServices: []string{"argus"},
					Methods:      []parser.MethodDescriptor{{Name: "GetStratumStatus", InputType: "StatusRequest", OutputType: "StatusResponse"}},
				},
			},
		},
		{
			Package: "query",
			Services: []parser.ServiceDescriptor{
				{
					Name:            "RecordMap",
					IsS2S:           true,
					AuthServices:    []string{"argus"},
					Synthesized:     true,
					SynthesizedKind: parser.SynthesizedKindRecordMap,
					HostLabel:       "stratum",
					Methods:         readRpcs,
				},
				{
					Name:              "RecordQuery",
					IsC2S:             true,
					AuthEntitledUsers: true,
					Synthesized:       true,
					SynthesizedKind:   parser.SynthesizedKindRecordMap,
					HostLabel:         "stratum",
					Methods:           readRpcs,
				},
			},
		},
	}
}

func rpcByServiceAndName(t *testing.T, rpcs []*spec.RpcInfo, service, name string) *spec.RpcInfo {
	t.Helper()
	for _, r := range rpcs {
		if r.ServiceName == service && r.Rpc == name {
			return r
		}
	}
	t.Fatalf("no rpc %s/%s in %d collected rpcs", service, name, len(rpcs))
	return nil
}

func surfaceByKind(t *testing.T, rpc *spec.RpcInfo, kind string) *spec.RpcSurfaceInfo {
	t.Helper()
	for _, s := range rpc.Surfaces {
		if s.SurfaceKind == kind {
			return s
		}
	}
	t.Fatalf("rpc %s has no %q surface", rpc.Rpc, kind)
	return nil
}

// tenant_scope reports the s2s record plane as caller_pinned, and does so
// WHETHER OR NOT the plane's host is tenant_owned.
//
// The host-keyed version of this gate reported "fleet" for a shared host, which
// matched a generator that emitted no pin there at all. Both halves were wrong
// the same way: the tenant boundary is the CALLER's token namespace, so a
// /tenants/<t> peer reading through shared stratum is pinned like any other, and
// the registry has to report the plane the way the wrapper actually generates it.
func TestTenantScopeOnRecordPlaneS2SReads(t *testing.T) {
	for _, tc := range []struct {
		name            string
		hostTenantOwned bool
	}{
		{name: "shared host still pins on the caller", hostTenantOwned: false},
		{name: "tenant-owned host pins on the caller", hostTenantOwned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fds := recordPlaneFds(tc.hostTenantOwned)
			rpcs, err := CollectRpcProtos(fds)
			if err != nil {
				t.Fatalf("CollectRpcProtos returned error: %v", err)
			}

			for _, name := range []string{"CreateWidgetQuery", "ListWidget"} {
				rpc := rpcByServiceAndName(t, rpcs, "RecordMap", name)
				if rpc.TenantScope != tenantScopeCallerPinned {
					t.Fatalf("RecordMap/%s tenant_scope = %q, want %q", name, rpc.TenantScope, tenantScopeCallerPinned)
				}
				if got := surfaceByKind(t, rpc, "s2s").TenantScope; got != tenantScopeCallerPinned {
					t.Fatalf("RecordMap/%s s2s surface tenant_scope = %q, want %q", name, got, tenantScopeCallerPinned)
				}
				// The honesty fix is ADDITIVE: the namespace-source fields keep
				// reporting exactly what they reported before.
				if rpc.SubjectNamespaceSource != "token.namespace" {
					t.Fatalf("RecordMap/%s subject_namespace_source = %q, want token.namespace", name, rpc.SubjectNamespaceSource)
				}
			}

			// The c2s sibling is the same synthesized plane but not an S2S read
			// surface: its namespace filter is mandatory and unconditional, so it
			// has no tenant scope to report. Neither does a hand-authored S2S rpc.
			c2s := rpcByServiceAndName(t, rpcs, "RecordQuery", "ListWidget")
			if c2s.TenantScope != "" {
				t.Fatalf("RecordQuery/ListWidget tenant_scope = %q, want empty", c2s.TenantScope)
			}
			if got := surfaceByKind(t, c2s, "c2s").TenantScope; got != "" {
				t.Fatalf("RecordQuery/ListWidget c2s surface tenant_scope = %q, want empty", got)
			}
			plain := rpcByServiceAndName(t, rpcs, "StratumAdmin", "GetStratumStatus")
			if plain.TenantScope != "" {
				t.Fatalf("StratumAdmin/GetStratumStatus tenant_scope = %q, want empty", plain.TenantScope)
			}
			if plain.SubjectNamespaceSource != "token.namespace" {
				t.Fatalf("StratumAdmin/GetStratumStatus subject_namespace_source = %q, want token.namespace", plain.SubjectNamespaceSource)
			}
		})
	}
}

// The two host shapes are not merely both caller_pinned — they produce a
// BYTE-IDENTICAL registry. The host's tenant_owned is no longer an input to any
// rpc-level field, so nothing downstream can key a tenant boundary on deployment
// shape by reading this file.
func TestTenantScopeIgnoresHostTenantOwned(t *testing.T) {
	shared, err := CollectRpcProtos(recordPlaneFds(false))
	if err != nil {
		t.Fatalf("CollectRpcProtos(shared host) returned error: %v", err)
	}
	owned, err := CollectRpcProtos(recordPlaneFds(true))
	if err != nil {
		t.Fatalf("CollectRpcProtos(tenant-owned host) returned error: %v", err)
	}
	if len(shared) != len(owned) {
		t.Fatalf("rpc counts differ: shared %d, tenant-owned %d", len(shared), len(owned))
	}
	for i := range shared {
		if !proto.Equal(shared[i], owned[i]) {
			t.Fatalf("rpc %s/%s differs on host tenant_owned:\n shared: %v\n  owned: %v",
				shared[i].ServiceName, shared[i].Rpc, shared[i], owned[i])
		}
	}
}
