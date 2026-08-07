// Command soundness is a read-only checker that builds the constraint × language
// coverage matrix described in docs/DEVELOPMENT.md ("fix A"). It derives the canonical
// constraint set from pkg/proto/sys/spec.proto (so it auto-tracks new
// constraints) and reports, for each constraint, which language validation
// emitters reference it. A constraint referenced in some languages but not all
// is a parity gap — the exact failure mode behind M18 and the oneof gap.
//
// It does NOT modify anything. Run: go run ./cmd/soundness [-root .]
//
// Caveat: presence of a constraint string in an emitter proves it is referenced,
// not that enforcement is correct; absence is a definitive gap. This is the same
// signal that surfaces uniform/partial omissions.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// validation emitter source file per language, relative to repo root.
// Standard location (decision D-3): every language generator keeps its
// validation emitter in pkg/internal/<lang>/types/validation.go.
var emitterFiles = map[string]string{
	"go":         "pkg/internal/go/types/validation.go",
	"java":       "pkg/internal/java/types/validation.go",
	"python":     "pkg/internal/python/types/validation.go",
	"rust":       "pkg/internal/rust/types/validation.go",
	"typescript": "pkg/internal/typescript/types/validation.go",
}

var langOrder = []string{"go", "java", "python", "rust", "typescript"}

// Auxiliary files that contribute validation semantics but are not the primary
// constraint emitter for a language.
var emitterAuxFiles = map[string][]string{
	"python": {"pkg/templates/python/types/validation.py.tmpl"},
	"rust":   {"pkg/templates/rust/types/validation.rs.tmpl"},
}

// Control-plane loci: the canonical generated file where a pipeline/outbound
// guarantee must be wired, per language. The control-plane matrix probes these
// (not the validation emitters).
var wrapperFiles = map[string]string{
	"go":         "pkg/templates/go/types/servicewrapper.go.tmpl",
	"java":       "pkg/templates/java/types/servicewrapper.java.tmpl",
	"python":     "pkg/templates/python/types/servicewrapper.py.tmpl",
	"rust":       "pkg/templates/rust/types/servicewrapper.rs.tmpl",
	"typescript": "pkg/templates/typescript/types/service_wrapper.ts.tmpl",
}

var clientFiles = map[string]string{
	"go":         "pkg/templates/go/types/grpcclient.go.tmpl",
	"java":       "pkg/templates/java/types/grpcclient.java.tmpl",
	"python":     "pkg/templates/python/types/grpcclient.py.tmpl",
	"rust":       "pkg/templates/rust/types/grpcclient.rs.tmpl",
	"typescript": "pkg/templates/typescript/types/grpcclient.ts.tmpl",
}

var clientEmitterFiles = map[string]string{
	"go":         "pkg/internal/go/types/grpcclient_gen.go",
	"java":       "pkg/internal/java/types/grpcclient_gen.go",
	"python":     "pkg/internal/python/types/grpcclient_gen.go",
	"rust":       "pkg/internal/rust/types/grpcclient_gen.go",
	"typescript": "pkg/internal/typescript/types/grpcclient_gen.go",
}

var serviceManagerFiles = map[string]string{
	"go":         "pkg/templates/go/types/service_manager.go.tmpl",
	"java":       "pkg/templates/java/types/ServiceManager.java.tmpl",
	"python":     "pkg/templates/python/types/service_manager.py.tmpl",
	"rust":       "pkg/templates/rust/types/service_manager.rs.tmpl",
	"typescript": "pkg/templates/typescript/types/service_manager.ts.tmpl",
}

var collectionRenderTestFiles = map[string]string{
	"go":         "pkg/internal/go/types/collection_render_test.go",
	"java":       "pkg/internal/java/types/collection_render_test.go",
	"python":     "pkg/internal/python/types/collection_render_test.go",
	"rust":       "pkg/internal/rust/types/collection_render_test.go",
	"typescript": "pkg/internal/typescript/types/collection_render_test.go",
}

var languageGeneratorFiles = map[string]string{
	"go":         "pkg/internal/go/types/generator.go",
	"java":       "pkg/internal/java/types/generator.go",
	"python":     "pkg/internal/python/types/generator.go",
	"rust":       "pkg/internal/rust/types/generator.go",
	"typescript": "pkg/internal/typescript/types/generator.go",
}

// dataEntitlementEmitterFiles locates each FULL-SERVICE language's Sanitize<T>
// emitter. Client-only python/typescript are intentionally absent (they host no
// store egress and emit no sanitize surface) — the probe leaves them without a
// pattern so they pass vacuously.
var dataEntitlementEmitterFiles = map[string]string{
	"go":   "pkg/internal/go/types/sanitize_gen.go",
	"java": "pkg/internal/java/types/release.go",
	"rust": "pkg/internal/rust/types/release_gen.go",
}

// dataEntitlementBuilderFiles locates each full-service language's caller →
// <T>Entitlement builder emitter (RED-1: token data grants → RAW levels,
// fail-closed).
var dataEntitlementBuilderFiles = map[string]string{
	"go":   "pkg/internal/go/types/entitlement_builder_gen.go",
	"java": "pkg/internal/java/types/release.go",
	"rust": "pkg/internal/rust/types/release_gen.go",
}

// dataEntitlementWrapperFiles locates each full-service language's wrapper-side
// egress sanitize wiring (RED-1: the generated server — never the handler —
// sanitizes every record-bearing egress item).
var dataEntitlementWrapperFiles = map[string]string{
	"go":   "pkg/internal/go/types/servicewrapper.go",
	"java": "pkg/internal/java/types/servicewrapper_gen.go",
	"rust": "pkg/internal/rust/types/servicewrapper_gen.go",
}

// controlPlaneGuarantee is one server-pipeline or outbound-call guarantee that
// docs/DEVELOPMENT.md Part II expects in every language. `present if any token`.
//
// ENFORCED (v1): this matrix fails the build when a calibrated control-plane
// marker is missing from any full-language backend. Token presence is necessary,
// not sufficient; runtime tests still prove semantics, but absence is a
// definitive parity gap. Calibrated against the wrapper/client templates
// 2026-06-06.
type controlPlaneGuarantee struct {
	name   string
	langs  []string
	locus  map[string]string   // canonical file per language
	tokens map[string][]string // present if any of these appears in that file
}

// semanticSurfaceProbe is a generated-surface marker that must appear in every
// full-language backend. Unlike controlPlaneGuarantee, every token listed for a
// language must be present because these rows prove specific coverage seams.
type semanticSurfaceProbe struct {
	name     string
	locus    map[string]string
	patterns map[string][]string
}

func uniform(tokens ...string) map[string][]string {
	m := map[string][]string{}
	for _, l := range langOrder {
		m[l] = tokens
	}
	return m
}

func controlPlaneGuarantees() []controlPlaneGuarantee {
	return []controlPlaneGuarantee{
		{
			name:  "auth (server pipeline)",
			locus: wrapperFiles,
			tokens: map[string][]string{
				"go": {"Authorizer", "authorize"}, "java": {"authorizer"},
				"python": {"authorize"}, "rust": {"authorizer", "Authorizer"},
				"typescript": {"authorize", "Authorizer"},
			},
		},
		{
			name:  "request/response validation (server pipeline)",
			locus: wrapperFiles,
			tokens: map[string][]string{
				"go": {"validateReq", "validateRes"}, "java": {".validate("},
				"python": {"validate_request"}, "rust": {"ValidationWrapper"},
				"typescript": {"validate("},
			},
		},
		{
			name:  "server egress validation (full-service send paths)",
			langs: []string{"go", "java", "rust"},
			locus: map[string]string{
				"go":   "pkg/templates/go/types/servicewrapper.go.tmpl",
				"java": "pkg/templates/java/types/servicewrapper.java.tmpl",
				"rust": "pkg/internal/rust/types/servicewrapper_gen.go",
			},
			tokens: map[string][]string{
				"go":   {"ValidatingServerStreamingServer"},
				"java": {"validatingResponseObserver"},
				"rust": {"rustServerResponseValidationBlock", "rustValidatedResponseStreamReturn"},
			},
		},
		{
			name:  "rust W2S REST/SSE response validation (server egress)",
			langs: []string{"rust"},
			locus: map[string]string{
				"rust": "pkg/templates/rust/types/rest_dispatch.rs.tmpl",
			},
			tokens: map[string][]string{
				"rust": {"resp.validate()", "msg.validate()"},
			},
		},
		{name: "trace emission (server pipeline)", locus: wrapperFiles, tokens: uniform("TraceEvent", "Trace(")},
		{
			name:  "accounting (server pipeline)",
			locus: wrapperFiles,
			tokens: map[string][]string{
				"go": {"accounting", "Accounting"}, "java": {"Accounting", "accounting"},
				"python": {"emitter", "accounting"}, "rust": {"Accounting", "accounting"},
				"typescript": {"Accounting", "accounting"},
			},
		},
		{name: "per-call outbound timeout (client)", locus: clientFiles, tokens: uniform("timeout", "Timeout", "deadline", "Deadline")},
		{
			// REST-based W2S clients (typescript, swift, csharp via lite-client) set
			// their own HTTP timeouts. The gRPC-based W2S clients (go, java, rust) get
			// timeouts from the general gRPC client row above, so they are not
			// duplicated here. PAR-7: this row verifies the TS fix exists.
			name:  "W2S REST default timeout (HTTP-based clients)",
			langs: []string{"typescript"},
			locus: map[string]string{
				"typescript": "pkg/templates/typescript/types/w2sm.ts.tmpl",
			},
			tokens: map[string][]string{
				"typescript": {"AbortSignal.timeout", "connectTimeoutMs"},
			},
		},
		{name: "bounded outbound retry/backoff (client)", locus: clientFiles, tokens: uniform("retry", "Retry", "backoff", "Backoff")},
		{
			name:  "request validation (client outbound)",
			locus: clientEmitterFiles,
			tokens: map[string][]string{
				"go": {"Validate request before sending"}, "java": {"inputValidator"},
				"python": {"inputValidation"}, "rust": {"rustGrpcRequestValidation"},
				"typescript": {"requestValidation"},
			},
		},
		{
			name:  "unary response validation (client outbound)",
			locus: clientEmitterFiles,
			tokens: map[string][]string{
				"go": {"Validate response after receiving"}, "java": {"outputValidator"},
				"python": {"outputValidation"}, "rust": {"rustGrpcResponseValidation"},
				"typescript": {"responseValidation"},
			},
		},
		{
			name:  "unified readiness (service manager)",
			locus: serviceManagerFiles,
			tokens: map[string][]string{
				"go":         {"StartWithReady", "first health publish"},
				"java":       {"firstHealthReady", "healthReporter.start(firstHealthReady)"},
				"python":     {"self._ready", "_GrpcHealthHandler(lambda: self._ready)", "await health_reporter.start()"},
				"rust":       {"new_with_ready", "first_health_rx"},
				"typescript": {"this._ready", "startUntilFirstPublish"},
			},
		},
		{
			name:  "SM-owned task teardown (service manager)",
			locus: serviceManagerFiles,
			tokens: map[string][]string{
				"go":         {"serviceTaskScope", "sm.tasks.Go", "sm.tasks.Wait"},
				"java":       {"managedThreads", "startManagedThread", "stopManagedThreads"},
				"python":     {"self._tasks", "_create_task", "_cancel_tasks"},
				"rust":       {"tasks: Arc<std::sync::Mutex", "spawn_owned", "shutdown_owned_tasks"},
				"typescript": {"tasks = new Set", "trackTask", "shutdownTasks"},
			},
		},
	}
}

func semanticSurfaceProbes() []semanticSurfaceProbe {
	return []semanticSurfaceProbe{
		{
			name:  "PAGE collection seam coverage",
			locus: collectionRenderTestFiles,
			patterns: map[string][]string{
				"go":         {"--- PAGE", "PageParams", "TestPage_Client_ExplicitPageAndAutoPager"},
				"java":       {"--- PAGE", "PageParams", "TestJavaPage_Client_ExplicitPageAndAutoPager"},
				"python":     {"--- PAGE", "PageParams", "TestPyPage_Client_ExplicitPageAndAutoPager"},
				"rust":       {"--- PAGE", "PageParams", "TestRustPage_Client_ExplicitPageAndAutoPager"},
				"typescript": {"--- PAGE", "PageParams", "TestTsPage_Client_ExplicitPageAndAutoPager"},
			},
		},
		{
			name:  "shared registry generation",
			locus: languageGeneratorFiles,
			patterns: map[string][]string{
				"go":         {"generateRegistry("},
				"java":       {"registry.Generate", "registry.Config"},
				"python":     {"registry.Generate", "registry.Config"},
				"rust":       {"registry.Generate", "registry.Config"},
				"typescript": {"registry.Generate", "registry.Config"},
			},
		},
		{
			// Data-entitlement egress transform. Full-service languages MUST emit the
			// recursive Sanitize<T> that consumes the generated <T>Entitlement and the
			// keyed-HMAC primitive; client-only python/typescript pass vacuously (no
			// store egress to sanitize). A missing token here is a build-failing gap.
			name:  "data-entitlement Sanitize<T> transform",
			locus: dataEntitlementEmitterFiles,
			patterns: map[string][]string{
				"go":   {"func Sanitize", "tacplusPseudonymize"},
				"java": {"sanitize%[2]s", "tacplusPseudonymize"},
				"rust": {"sanitize_", "tacplus_pseudonymize"},
			},
		},
		{
			// RED-1 builder: full-service languages MUST emit the caller →
			// <T>Entitlement builder that maps token data grants onto RAW levels
			// (fail-closed; absent grant = most-protective default).
			name:  "data-entitlement caller->entitlement builder",
			locus: dataEntitlementBuilderFiles,
			patterns: map[string][]string{
				"go":   {"Build%[1]sEntitlement", "ENTITLEMENT_LEVEL_RAW"},
				"java": {"build%[1]sEntitlement", "ENTITLEMENT_LEVEL_RAW"},
				"rust": {`"_entitlement"`, "ENTITLEMENT_LEVEL_RAW"},
			},
		},
		{
			// RED-1 wrapper wiring: the generated service wrapper MUST sanitize
			// every record-bearing egress item AFTER the handler returns. A missing
			// token here means an egress path can emit records without the sanitize
			// call — a build-failing gap.
			name:  "data-entitlement wrapper egress wiring",
			locus: dataEntitlementWrapperFiles,
			patterns: map[string][]string{
				"go":   {"Egress data entitlement", "egressSanitizePreamble", "sanitizableType"},
				"java": {"Egress data entitlement", "javaEgressSanitizePreamble", "sanitizableType"},
				"rust": {"Egress data entitlement", "rustEgressSanitizePreamble", "sanitizableType"},
			},
		},
		{
			// Config defaults parity: all full-service languages MUST iterate the
			// same annotation keys (default_int, default_string, etc.) and emit a
			// defaults function. The health_probe_port incident proved that a
			// single-language canary (go) passing hides a fleet-wide failure in
			// the others when they silently omit a default.
			name:  "config defaults annotation coverage",
			locus: configDefaultsEmitterFiles,
			patterns: map[string][]string{
				"go":   {`"default_int"`, `"default_string"`, `"default_float"`, `"default_bool"`, `"default_host"`},
				"java": {`"default_int"`, `"default_string"`, `"default_float"`, `"default_bool"`, `"default_host"`},
				"rust": {`"default_int"`, `"default_string"`, `"default_float"`, `"default_bool"`, `"default_host"`},
			},
		},
		{
			// Defense-in-depth: ServiceManager.create() MUST call configDefaults
			// immediately after loadConfig, before any field is read. The state
			// authority (kernel) never traverses the typed-config call sites that
			// re-apply defaults in run(); without this call in create(),
			// health_probe_port stays 0 and the probe listener never starts.
			name:  "configDefaults before first use in ServiceManager",
			locus: serviceManagerFiles,
			patterns: map[string][]string{
				"go":   {"ConfigDefaults(cfg)"},
				"java": {"configDefaults(cfg).build()"},
				"rust": {"config_defaults(&mut cfg)"},
			},
		},
	}
}

var sharedRegistryProbes = []struct {
	name     string
	file     string
	patterns []string
}{
	{
		name: "RpcInfo governed verb metadata",
		file: "pkg/proto/sys/tacplus.proto",
		patterns: []string{
			"string rpc_verb",
			"string rpc_noun",
			"string method_pattern",
			"string access_kind",
			"string verb_class",
			"repeated string entitlement_scopes",
			"string retry_policy",
			"string capability_scope",
			"string handler_shape",
			"string target_namespace_field",
			"string subject_namespace_source",
			"string target_namespace_source",
			"string target_namespace",
		},
	},
	{
		name: "registry.schema descriptor-driven metadata",
		file: "pkg/internal/common/registry/registry.go",
		patterns: []string{
			"registryProtoSchemaDefs()",
			"spec.File_sys_tacplus_proto.Messages()",
			"registryProtoFieldSchema(field)",
		},
	},
	{
		name: "PAGE registry metadata",
		file: "pkg/proto/sys/tacplus.proto",
		patterns: []string{
			"\"PAGE\"",
			"string collection_mode",
			"uint32 collection_default_page_size",
		},
	},
	{
		name: "entitlement grant catalog metadata",
		file: "pkg/proto/sys/tacplus.proto",
		patterns: []string{
			"message EntitlementGrantInfo",
			"repeated EntitlementGrantInfo entitlement_grants",
			"repeated string expands_to",
		},
	},
	{
		name: "entitlement grant token collision gate",
		file: "pkg/internal/common/registry/registry.go",
		patterns: []string{
			"validateEntitlementGrantTokenCollisions(rootNode.EntitlementGrants)",
			"entitlement grant token collision",
		},
	},
}

var spiffeGrammarProbes = []struct {
	name     string
	file     string
	patterns []string
}{
	{
		name: "canonical SPIFFE grammar source",
		file: "pkg/internal/common/spiffe/grammar.go",
		patterns: []string{
			"SegmentPattern = `^` + SegmentGrammar + `$`",
			"func TemplateData() TemplateGrammar",
			"func ReservedTagSet() map[string]bool",
		},
	},
	{
		name: "parser SPIFFE grammar source",
		file: "pkg/internal/common/parser/spiffe_validate.go",
		patterns: []string{
			"commonspiffe.SegmentPattern",
			"commonspiffe.SegmentGrammar",
			"commonspiffe.ReservedTagSet()",
		},
	},
	{
		name: "go sys/spiffe template grammar source",
		file: "pkg/templates/go/chassis/spiffe/spiffe.go.tmpl",
		patterns: []string{
			"{{.Spiffe.SegmentGrammar}}",
			"{{printf \"%q\" .Spiffe.SegmentPattern}}",
		},
	},
	{
		name: "java chassis/spiffe template grammar source",
		file: "pkg/templates/java/chassis/spiffe/Spiffe.java.tmpl",
		patterns: []string{
			"{{.Spiffe.SegmentGrammar}}",
			"{{printf \"%q\" .Spiffe.SegmentPattern}}",
		},
	},
	{
		name: "rust chassis/spiffe template grammar source",
		file: "pkg/templates/rust/chassis/spiffe/src/spiffe.rs.tmpl",
		patterns: []string{
			"{{.Spiffe.SegmentGrammar}}",
			"{{printf \"%q\" .Spiffe.SegmentPattern}}",
		},
	},
	{
		name: "python chassis/spiffe template grammar source",
		file: "pkg/templates/python/chassis/spiffe/spiffe.py.tmpl",
		patterns: []string{
			"{{.Spiffe.SegmentGrammar}}",
			"{{printf \"%q\" .Spiffe.SegmentPattern}}",
		},
	},
	{
		name: "typescript chassis/spiffe template grammar source",
		file: "pkg/templates/typescript/chassis/spiffe/SpiffeId.ts.tmpl",
		patterns: []string{
			"{{.Spiffe.SegmentGrammar}}",
			"{{printf \"%q\" .Spiffe.SegmentPattern}}",
		},
	},
}

var emittedManifestProbes = []struct {
	name      string
	file      string
	patterns  []string
	forbidden []string
}{
	{
		name: "emitted proto manifest feature source",
		file: "pkg/internal/proto/manifest.go",
		patterns: []string{
			"FeatureRuntimeConfigState",
			"FeatureFilesystemHelpers",
			"FeatureEntitlementHelpers",
			"emittedProtoManifest",
		},
	},
	{
		name: "service-state source classification facts",
		file: "pkg/internal/proto/classification.go",
		patterns: []string{
			"DeclaresServiceStateModel",
			"DeclaresServiceStateSource",
			"serviceStateSourceOptionRe",
		},
		forbidden: []string{
			"func IsStateFileName",
		},
	},
	{
		name: "trace publisher declared event type routing",
		file: "pkg/internal/common/boundaries/boundaries.go",
		patterns: []string{
			"TraceEventTypeFQN",
			"IsTraceEventPublisherData",
			"publisher.EventTypeFQN == TraceEventTypeFQN",
		},
		forbidden: []string{
			`EventTypeName == "TraceEvent"`,
		},
	},
	{
		name: "rust trace publisher declared event type routing",
		file: "pkg/internal/rust/types/handler_gen.go",
		patterns: []string{
			"boundaries.TraceEventTypeFQN",
		},
		forbidden: []string{
			`EventTypeName == "TraceEvent"`,
		},
	},
	{
		name: "trace skipped type descriptor package routing",
		file: "pkg/internal/proto/trace.go",
		patterns: []string{
			"fqn := fileDesc.Package + \".\" + msg.Name",
		},
		forbidden: []string{
			`strings.TrimSuffix(filepath.Base(relPath), ".proto")`,
		},
	},
	{
		name: "config state roots from grpc_config declarations",
		file: "pkg/internal/proto/configholder.go",
		patterns: []string{
			"declaredConfigStateRoots",
			`msg.Annotations["grpc_config"]`,
			"ConfigStateRootName(serviceName)",
		},
		forbidden: []string{
			"messageHasConfigBoundary",
			`strings.HasSuffix(msg.Name, "ConfigState")`,
		},
	},
	{
		name: "go state wrapper descriptor/manifest gates",
		file: "pkg/internal/go/types/stateclient_gen.go",
		patterns: []string{
			"proto.IsRuntimeConfigStateProto(relPath)",
			"proto.IsSyntheticServiceOnlyFile(fileDesc)",
			"strutil.ToSnakeCase(expectedState)",
		},
		forbidden: []string{
			`strings.TrimSuffix(strutil.ToSnakeCase(expectedState), "_state")`,
		},
	},
	{
		name: "java runtime config state manifest gate",
		file: "pkg/internal/java/types/statewrapper_gen.go",
		patterns: []string{
			"proto.IsRuntimeConfigStateProto(svc.ProtoFile)",
		},
	},
	{
		name: "rust runtime config state manifest gate",
		file: "pkg/internal/rust/types/statewrapper_gen.go",
		patterns: []string{
			"proto.IsRuntimeConfigStateProto(relPath)",
			"strutil.ToSnakeCase(stateRootAnnotation)",
		},
		forbidden: []string{
			`strings.TrimSuffix(strutil.ToSnakeCase(stateRootAnnotation), "_state")`,
		},
	},
	{
		name: "python runtime config state manifest gate",
		file: "pkg/internal/python/types/statewrapper_gen.go",
		patterns: []string{
			"proto.IsRuntimeConfigStateProto(relPath)",
			"strutil.ToSnakeCase(stateRootAnnotation)",
		},
		forbidden: []string{
			`strings.TrimSuffix(strutil.ToSnakeCase(stateRootAnnotation), "_state")`,
		},
	},
	{
		name: "typescript runtime config state manifest gate",
		file: "pkg/internal/typescript/types/statewrapper.go",
		patterns: []string{
			"proto.IsRuntimeConfigStateProto(relPath)",
			"strutil.ToSnakeCase(stateRootAnnotation)",
		},
		forbidden: []string{
			`strings.TrimSuffix(strutil.ToSnakeCase(stateRootAnnotation), "_state")`,
		},
	},
	{
		name: "go declared-package output routing",
		file: "pkg/internal/go/types/generator.go",
		patterns: []string{
			"dirPackageName := packageName",
		},
		forbidden: []string{
			`strings.TrimSuffix(packageName, "_watch")`,
			`strings.TrimSuffix(pfPkg, "_watch")`,
		},
	},
	{
		name: "java service-state source routing",
		file: "pkg/internal/java/types/generator.go",
		patterns: []string{
			"proto.DeclaresServiceStateSource(source)",
			"case declaresServiceState:",
		},
		forbidden: []string{
			"proto.IsStateFileName",
		},
	},
	{
		name: "typescript service-state source routing",
		file: "pkg/internal/typescript/types/generator.go",
		patterns: []string{
			"typeScriptServiceStateFileMap",
			"proto.DeclaresServiceStateModel(pf)",
			"typeScriptServiceStateFile(serviceStateFiles, protoFile)",
		},
		forbidden: []string{
			"proto.IsStateFileName",
		},
	},
	{
		name: "typescript declared service package helper",
		file: "pkg/internal/typescript/types/client_symbol.go",
		patterns: []string{
			"return svc.PackageName",
		},
		forbidden: []string{
			`svc.PackageName == "services"`,
		},
	},
	{
		name: "typescript grpc client declared package routing",
		file: "pkg/internal/typescript/types/grpcclient_gen.go",
		patterns: []string{
			"tsActualServicePackage(svc)",
		},
		forbidden: []string{
			`svc.PackageName == "services"`,
		},
	},
	{
		name: "typescript rest client declared package routing",
		file: "pkg/internal/typescript/types/restclient_gen.go",
		patterns: []string{
			"tsActualServicePackage(svc)",
		},
		forbidden: []string{
			`svc.PackageName == "services"`,
		},
	},
	{
		name: "typescript grpc-over-rest declared package routing",
		file: "pkg/internal/typescript/types/grpc_over_rest_gen.go",
		patterns: []string{
			"tsActualServicePackage(svc)",
		},
		forbidden: []string{
			`svc.PackageName == "services"`,
		},
	},
	{
		name: "typescript server wrapper declared package routing",
		file: "pkg/internal/typescript/types/service_wrapper.go",
		patterns: []string{
			"tsActualServicePackage(svc)",
		},
		forbidden: []string{
			`svc.PackageName == "services"`,
		},
	},
	{
		name: "typescript state wrapper declared package routing",
		file: "pkg/internal/typescript/types/statewrapper.go",
		patterns: []string{
			"tsActualServicePackage(svc)",
		},
		forbidden: []string{
			`svc.PackageName == "services"`,
		},
	},
	{
		name: "go filesystem helper manifest gate",
		file: "pkg/internal/go/types/helpers.go",
		patterns: []string{
			"proto.EmitsFilesystemHelpers(pf.RelPath, pf.Package)",
		},
	},
	{
		name: "java filesystem helper manifest gate",
		file: "pkg/internal/java/types/helpers.go",
		patterns: []string{
			"proto.EmitsFilesystemHelpers(pf.RelPath, pf.Package)",
		},
	},
	{
		name: "rust filesystem helper manifest gate",
		file: "pkg/internal/rust/types/builder.go",
		patterns: []string{
			"internalproto.EmitsFilesystemHelpers(pf.RelPath, pf.Package)",
		},
	},
	{
		name: "python filesystem helper manifest gate",
		file: "pkg/internal/python/types/helpers_gen.go",
		patterns: []string{
			"proto.EmitsFilesystemHelpers(pf.RelPath, pf.Package)",
		},
	},
	{
		name: "typescript filesystem helper manifest gate",
		file: "pkg/internal/typescript/types/helpers_gen.go",
		patterns: []string{
			"proto.EmitsFilesystemHelpers(pf.RelPath, pf.Package)",
		},
	},
	{
		name: "java entitlement helper manifest gate",
		file: "pkg/internal/java/types/helpers.go",
		patterns: []string{
			"proto.EmitsEntitlementHelpers(pf.RelPath, pf.Package)",
		},
	},
}

// configDefaultsEmitterFiles locates each FULL-SERVICE language's config defaults
// generator. All three must iterate the same annotation keys and emit defaults
// for every field — a divergence is the health_probe_port class of bug.
var configDefaultsEmitterFiles = map[string]string{
	"go":   "pkg/internal/go/types/constants.go",
	"java": "pkg/internal/java/types/helpers.go",
	"rust": "pkg/internal/rust/types/constants.go",
}

// field kinds whose default-bound coverage we additionally probe (the M18/oneof
// class). For each language, every listed source marker must be present.
var fieldKindProbes = []struct {
	name     string
	patterns map[string][]string
}{
	{
		name: "oneof-scalar-size-bounds",
		patterns: map[string][]string{
			"go":         {"field.IsOneOf", "m.Get%s()", "assert_max_size"},
			"java":       {"field.IsOneOf", "msg.%s().getBytes(java.nio.charset.StandardCharsets.UTF_8).length", "assert_max_size"},
			"python":     {"field.IsOneOf", "msg.%s.encode('utf-8')", "assert_max_size"},
			"rust":       {"generateRustOneofScalarMaxSize", "writeRustOneofScalarMaxSize"},
			"typescript": {"writeTypeScriptOneofScalarMaxSize", "m.%s.case === '%s'"},
		},
	},
	{
		name: "message-nesting-depth-guard",
		patterns: map[string][]string{
			"go":         {"Validate%sWithDepth", "depth > 24", "depth+1"},
			"java":       {"validate%sDepth", "depth > 24", "depth + 1"},
			"python":     {"_depth > 24", "_depth + 1"},
			"rust":       {"validate_with_depth", "depth > 24", "depth + 1"},
			"typescript": {"depth = 0", "depth > 24", "depth + 1"},
		},
	},
	{
		name: "unknown-enum-vocabulary-guard",
		patterns: map[string][]string{
			"go":         {"enum values are known", "%s[int32"},
			"java":       {"enum values are known", "UNRECOGNIZED"},
			"python":     {"enum values are known", "DESCRIPTOR.values_by_number"},
			"rust":       {"enum value is known", "try_from"},
			"typescript": {"enum value is known", "Object.values"},
		},
	},
}

func main() {
	root := flag.String("root", ".", "repo root")
	flag.Parse()

	specPath := filepath.Join(*root, "pkg/proto/sys/spec.proto")
	constraints, err := parseConstraints(specPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Load each emitter's source once.
	src := map[string]string{}
	for lang, rel := range emitterFiles {
		b, err := os.ReadFile(filepath.Join(*root, rel))
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot read %s emitter %s: %v\n", lang, rel, err)
			src[lang] = ""
			continue
		}
		src[lang] = string(b)
		for _, auxRel := range emitterAuxFiles[lang] {
			aux, err := os.ReadFile(filepath.Join(*root, auxRel))
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: cannot read %s auxiliary emitter %s: %v\n", lang, auxRel, err)
				continue
			}
			src[lang] += "\n" + string(aux)
		}
	}

	// Build the matrix: constraint -> lang -> referenced?
	type row struct {
		name    string
		present map[string]bool
		count   int
	}
	rows := make([]row, 0, len(constraints))
	for _, c := range constraints {
		r := row{name: c, present: map[string]bool{}}
		for _, lang := range langOrder {
			if constraintReferenced(src[lang], c) {
				r.present[lang] = true
				r.count++
			}
		}
		rows = append(rows, r)
	}

	// Render matrix.
	fmt.Println("# Constraint × language coverage matrix")
	fmt.Printf("\nDerived from %s. ✓ = constraint referenced in that language's validation emitter; · = absent.\n\n", specPath)
	fmt.Printf("| constraint | %s | langs |\n", strings.Join(langOrder, " | "))
	separators := make([]string, len(langOrder)+2)
	for i := range separators {
		separators[i] = "---"
	}
	fmt.Printf("| %s |\n", strings.Join(separators, " | "))
	var gaps []row
	failures := 0
	full := 0
	for _, r := range rows {
		cells := make([]string, len(langOrder))
		for i, lang := range langOrder {
			if r.present[lang] {
				cells[i] = "✓"
			} else {
				cells[i] = "·"
			}
		}
		flag := ""
		if r.count == len(langOrder) {
			full++
		} else if r.count > 0 {
			flag = "  ⚠ PARTIAL"
			gaps = append(gaps, r)
			failures++
		} else {
			flag = "  ✗ NONE"
			failures++
		}
		fmt.Printf("| `%s` | %s | %d/%d%s |\n", r.name, strings.Join(cells, " | "), r.count, len(langOrder), flag)
	}

	// Gaps summary.
	fmt.Printf("\n## Summary\n\n%d constraints, %d enforced in all %d languages, %d partial-parity gaps.\n",
		len(rows), full, len(langOrder), len(gaps))
	if len(gaps) > 0 {
		fmt.Println("\n### Partial-parity gaps (referenced in some languages, missing in others)")
		for _, r := range gaps {
			var missing []string
			for _, lang := range langOrder {
				if !r.present[lang] {
					missing = append(missing, lang)
				}
			}
			fmt.Printf("- `%s` — missing in: %s\n", r.name, strings.Join(missing, ", "))
		}
	}

	// Field-kind probes.
	fmt.Println("\n## Field-kind default-bound probes")
	for _, p := range fieldKindProbes {
		var hits []string
		for _, lang := range langOrder {
			langOK := true
			for _, pattern := range p.patterns[lang] {
				if !strings.Contains(src[lang], pattern) {
					langOK = false
					break
				}
			}
			if langOK {
				hits = append(hits, lang)
			}
		}
		status := ""
		if len(hits) != len(langOrder) {
			status = "  ⚠ PARTIAL"
			failures++
		}
		fmt.Printf("- `%s` present in: %s%s\n", p.name, strings.Join(hits, ", "), status)
	}
	fmt.Println("\n(Note: presence proves a reference, not correct enforcement; absence is a definitive gap.)")

	failures += renderSemanticSurfaces(*root)
	failures += renderSharedRegistryProbes(*root)
	failures += renderSpiffeGrammarProbes(*root)
	failures += renderEmittedManifestProbes(*root)

	// Control-plane coverage matrix (docs/DEVELOPMENT.md Part II).
	failures += renderControlPlane(*root)
	failures += renderLiteClientSoundness(*root)
	failures += renderSpiffeSoundness(*root)
	failures += renderStoreVendorSoundness(*root)

	if failures > 0 {
		os.Exit(1)
	}
}

// renderSemanticSurfaces prints generated-surface probes that must hold across
// every full-language backend. These are calibrated source/test markers, not
// runtime proofs.
func renderSemanticSurfaces(root string) int {
	cache := map[string]string{}
	read := func(rel string) string {
		if rel == "" {
			return ""
		}
		if v, ok := cache[rel]; ok {
			return v
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			cache[rel] = ""
			return ""
		}
		cache[rel] = string(b)
		return string(b)
	}

	fmt.Println("\n## Semantic generated-surface probes (ENFORCED)")
	fmt.Println("\nMarkers for generated semantic surfaces that must be present in every full-language backend.")
	fmt.Println("A `·` is a build-failing gap; runtime and consumer-contract tests still prove behavior.")
	fmt.Println()
	fmt.Printf("| probe | %s | langs |\n", strings.Join(langOrder, " | "))
	seps := make([]string, len(langOrder)+2)
	for i := range seps {
		seps[i] = "---"
	}
	fmt.Printf("| %s |\n", strings.Join(seps, " | "))

	type semanticGap struct {
		name    string
		missing []string
	}
	var gaps []semanticGap
	for _, p := range semanticSurfaceProbes() {
		cells := make([]string, len(langOrder))
		count := 0
		var missing []string
		for i, lang := range langOrder {
			src := read(p.locus[lang])
			ok := true
			for _, pattern := range p.patterns[lang] {
				if pattern == "" || !strings.Contains(src, pattern) {
					ok = false
					break
				}
			}
			if ok {
				cells[i] = "✓"
				count++
			} else {
				cells[i] = "·"
				missing = append(missing, lang)
			}
		}
		flag := ""
		if count != len(langOrder) {
			if count == 0 {
				flag = "  ✗ NONE (uniform gap)"
			} else {
				flag = "  ⚠ PARTIAL"
			}
			gaps = append(gaps, semanticGap{p.name, missing})
		}
		fmt.Printf("| %s | %s | %d/%d%s |\n", p.name, strings.Join(cells, " | "), count, len(langOrder), flag)
	}

	if len(gaps) > 0 {
		fmt.Println("\n### Semantic generated-surface gaps")
		for _, g := range gaps {
			where := "all languages"
			if len(g.missing) > 0 && len(g.missing) < len(langOrder) {
				where = strings.Join(g.missing, ", ")
			}
			fmt.Printf("- %s — missing in: %s\n", g.name, where)
		}
	}
	return len(gaps)
}

// renderSharedRegistryProbes checks language-neutral registry metadata emitted
// through the shared registry path used by every full-language generator.
func renderSharedRegistryProbes(root string) int {
	fmt.Println("\n## Shared registry metadata probes (ENFORCED)")
	fmt.Println("\nThese probes cover language-neutral registry fields emitted by the shared registry generator.")

	failures := 0
	for _, p := range sharedRegistryProbes {
		b, err := os.ReadFile(filepath.Join(root, p.file))
		if err != nil {
			fmt.Printf("- `%s` · cannot read %s: %v\n", p.name, p.file, err)
			failures++
			continue
		}
		src := string(b)
		var missing []string
		for _, pattern := range p.patterns {
			if !strings.Contains(src, pattern) {
				missing = append(missing, pattern)
			}
		}
		if len(missing) > 0 {
			fmt.Printf("- `%s` · missing in %s: %s\n", p.name, p.file, strings.Join(missing, ", "))
			failures++
			continue
		}
		fmt.Printf("- `%s` ✓ (%s)\n", p.name, p.file)
	}
	return failures
}

func renderSpiffeGrammarProbes(root string) int {
	fmt.Println("\n## SPIFFE grammar source probes (ENFORCED)")
	fmt.Println("\nParser validation and generated sys/spiffe templates must consume the same generator-side grammar source.")

	failures := 0
	for _, p := range spiffeGrammarProbes {
		b, err := os.ReadFile(filepath.Join(root, p.file))
		if err != nil {
			fmt.Printf("- `%s` · cannot read %s: %v\n", p.name, p.file, err)
			failures++
			continue
		}
		src := string(b)
		var missing []string
		for _, pattern := range p.patterns {
			if !strings.Contains(src, pattern) {
				missing = append(missing, pattern)
			}
		}
		if len(missing) > 0 {
			fmt.Printf("- `%s` · missing in %s: %s\n", p.name, p.file, strings.Join(missing, ", "))
			failures++
			continue
		}
		fmt.Printf("- `%s` ✓ (%s)\n", p.name, p.file)
	}
	return failures
}

func renderEmittedManifestProbes(root string) int {
	fmt.Println("\n## Emitted proto manifest probes (ENFORCED)")
	fmt.Println("\nGenerated infrastructure/package feature decisions must flow through the emitted-proto manifest, not package-name or path-substring literals.")

	failures := 0
	for _, p := range emittedManifestProbes {
		b, err := os.ReadFile(filepath.Join(root, p.file))
		if err != nil {
			fmt.Printf("- `%s` · cannot read %s: %v\n", p.name, p.file, err)
			failures++
			continue
		}
		src := string(b)
		var missing []string
		for _, pattern := range p.patterns {
			if !strings.Contains(src, pattern) {
				missing = append(missing, pattern)
			}
		}
		if len(missing) > 0 {
			fmt.Printf("- `%s` · missing in %s: %s\n", p.name, p.file, strings.Join(missing, ", "))
			failures++
			continue
		}
		var present []string
		for _, pattern := range p.forbidden {
			if strings.Contains(src, pattern) {
				present = append(present, pattern)
			}
		}
		if len(present) > 0 {
			fmt.Printf("- `%s` · forbidden in %s: %s\n", p.name, p.file, strings.Join(present, ", "))
			failures++
			continue
		}
		fmt.Printf("- `%s` ✓ (%s)\n", p.name, p.file)
	}
	return failures
}

// liteClientProbes verifies that swift/csharp lite-client generators emit the
// expected client surfaces. These languages are not in langOrder (no validation
// emitters) but they DO generate C2D and W2S clients that must not silently
// regress.
var liteClientProbes = []struct {
	name     string
	file     string
	patterns []string
}{
	{
		name: "swift C2D client template",
		file: "pkg/templates/swift/types/C2DClient.swift.tmpl",
		patterns: []string{
			"public struct C2DClient",
			"socketPath",
			"precondition(!socketPath.isEmpty",
		},
	},
	{
		name: "swift W2S client template",
		file: "pkg/templates/swift/types/W2SClient.swift.tmpl",
		patterns: []string{
			"public struct W2SClient",
			"requestTimeout",
		},
	},
	{
		name: "swift W2S streaming method stub",
		file: "pkg/templates/swift/types/W2SClient.swift.tmpl",
		patterns: []string{
			"@available(*, unavailable",
			"fatalError",
		},
	},
	{
		name: "csharp C2D client template",
		file: "pkg/templates/csharp/types/C2DClient.cs.tmpl",
		patterns: []string{
			"public sealed class C2DClient",
			"PipeName",
			"ArgumentException",
		},
	},
	{
		name: "csharp W2S client template",
		file: "pkg/templates/csharp/types/W2SClient.cs.tmpl",
		patterns: []string{
			"public sealed class W2SClient",
			"TimeSpan.FromSeconds(30)",
		},
	},
	{
		name: "csharp W2S streaming method stub",
		file: "pkg/templates/csharp/types/W2SClient.cs.tmpl",
		patterns: []string{
			"Obsolete",
			"NotSupportedException",
		},
	},
	{
		name: "swift W2S structured errors",
		file: "pkg/templates/swift/types/W2SClient.swift.tmpl",
		patterns: []string{
			"W2SClientError",
			"missingTokenProvider",
			"emptyToken",
			"httpStatus",
		},
	},
	{
		name: "csharp W2S structured errors",
		file: "pkg/templates/csharp/types/W2SClient.cs.tmpl",
		patterns: []string{
			"W2SClientException",
			"StatusCode",
		},
	},
}

func renderLiteClientSoundness(root string) int {
	fmt.Println("\n## Lite-client (swift/csharp) soundness probes (ENFORCED)")
	fmt.Println("\nSwift and C# are client-lite targets (no validation emitters, not in langOrder).")
	fmt.Println("These probes verify that the client surfaces they DO generate are present and")
	fmt.Println("include timeouts, structured errors, and streaming-method stubs.")

	failures := 0
	for _, p := range liteClientProbes {
		b, err := os.ReadFile(filepath.Join(root, p.file))
		if err != nil {
			fmt.Printf("- `%s` · cannot read %s: %v\n", p.name, p.file, err)
			failures++
			continue
		}
		src := string(b)
		var missing []string
		for _, pattern := range p.patterns {
			if !strings.Contains(src, pattern) {
				missing = append(missing, pattern)
			}
		}
		if len(missing) > 0 {
			fmt.Printf("- `%s` · missing in %s: %s\n", p.name, p.file, strings.Join(missing, ", "))
			failures++
			continue
		}
		fmt.Printf("- `%s` ✓ (%s)\n", p.name, p.file)
	}
	return failures
}

// renderControlPlane prints the control-plane guarantee × language matrix and
// returns one failure for each control-plane row that is not present in every
// language. Runtime tests still prove behavior; this gate prevents known wiring
// from silently regressing.
func renderControlPlane(root string) int {
	// Cache file reads.
	cache := map[string]string{}
	read := func(rel string) string {
		if rel == "" {
			return ""
		}
		if v, ok := cache[rel]; ok {
			return v
		}
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			cache[rel] = ""
			return ""
		}
		cache[rel] = string(b)
		return string(b)
	}

	fmt.Println("\n## Control-plane coverage matrix (ENFORCED — Part II)")
	fmt.Println("\nWhich server-pipeline / outbound-call guarantee is wired in each language's")
	fmt.Println("canonical generated file. ✓ = a calibrated marker is present; · = absent.")
	fmt.Print("A `·` is a build-failing gap. Runtime tests still prove behavior; this gate checks wiring markers.\n\n")
	fmt.Printf("| guarantee | %s | langs |\n", strings.Join(langOrder, " | "))
	seps := make([]string, len(langOrder)+2)
	for i := range seps {
		seps[i] = "---"
	}
	fmt.Printf("| %s |\n", strings.Join(seps, " | "))

	type cpGap struct {
		name    string
		missing []string
	}
	var gaps []cpGap
	for _, g := range controlPlaneGuarantees() {
		requiredLangs := g.langs
		if len(requiredLangs) == 0 {
			requiredLangs = langOrder
		}
		required := map[string]bool{}
		for _, lang := range requiredLangs {
			required[lang] = true
		}
		cells := make([]string, len(langOrder))
		count := 0
		var missing []string
		for i, lang := range langOrder {
			if !required[lang] {
				cells[i] = "n/a"
				continue
			}
			src := read(g.locus[lang])
			ok := false
			for _, tok := range g.tokens[lang] {
				if tok != "" && strings.Contains(src, tok) {
					ok = true
					break
				}
			}
			if ok {
				cells[i] = "✓"
				count++
			} else {
				cells[i] = "·"
				missing = append(missing, lang)
			}
		}
		flag := ""
		if count != len(requiredLangs) {
			if count == 0 {
				flag = "  ✗ NONE (uniform gap)"
			} else {
				flag = "  ⚠ PARTIAL"
			}
			gaps = append(gaps, cpGap{g.name, missing})
		}
		fmt.Printf("| %s | %s | %d/%d%s |\n", g.name, strings.Join(cells, " | "), count, len(requiredLangs), flag)
	}

	if len(gaps) > 0 {
		fmt.Println("\n### Control-plane gaps")
		for _, g := range gaps {
			where := "all languages"
			if len(g.missing) > 0 && len(g.missing) < len(langOrder) {
				where = strings.Join(g.missing, ", ")
			}
			fmt.Printf("- %s — missing in: %s\n", g.name, where)
		}
	}

	fmt.Println("\nRuntime-backed checks:")
	fmt.Println("- streaming/error-path trace and accounting completeness is covered by consumer-contract runtime cases.")
	return len(gaps)
}

// constraintReferenced reports whether src references the constraint name as a
// whole token. Word-boundary matching so a prefix-named constraint
// (assert_duration) cannot false-pass on a longer sibling (assert_duration_min):
// `_` and digits are word characters, so `\b` rejects partial-name hits.
func constraintReferenced(src, constraint string) bool {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(constraint) + `\b`)
	return re.MatchString(src)
}

// parseConstraints extracts the assert_* extension names from spec.proto.
func parseConstraints(specPath string) ([]string, error) {
	b, err := os.ReadFile(specPath)
	if err != nil {
		return nil, err
	}
	// Constraint names may contain digits (assert_base64); `\b` keeps the name
	// from bleeding into the field number on the other side of the `=`.
	re := regexp.MustCompile(`\b(assert_[a-z0-9_]+)\b\s*=\s*\d+`)
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out, nil
}

type spiffeSoundnessViolation struct {
	RelPath string
	Line    int
	Rule    string
	Text    string
}

var (
	spiffeSchemeInterpolationRe = regexp.MustCompile(`(?i)(?:\bSpiffe|\bwrapperSpiffe|\bwrapper_spiffe|\bspiffe)(?:(?:::)|\.)(?:SCHEME|Scheme)\s*\+\s*["']://`)
	spiffeRawInterpolationRe    = regexp.MustCompile(`f["']spiffe:|spiffe://%|spiffe://\{|spiffe://\$|spiffe:[^"'` + "`" + `]*["'` + "`" + `][[:space:]]*\+|\+[[:space:]]*["'` + "`" + `]spiffe:`)
	prefixCompareRe             = regexp.MustCompile(`\b(?:strings\.)?(?:HasPrefix|startsWith|starts_with)\s*\(`)
	uriPathExtractionRe         = regexp.MustCompile(`\b(?:URI\.create|url\.Parse|Url::parse|urlparse|new URL)\b|\.getPath\(\)|\.path\(\)`)
	pathSplitRe                 = regexp.MustCompile(`\.split\(["']/["']\)|\.split\('/'\)|strings\.Split\([^,]+,\s*"/"\)|trim_start_matches\('/'\)\.split\('/'\)`)
	bareNameExtractionRe        = regexp.MustCompile(`strings\.LastIndex\([^,]+,\s*"\."\)|strings\.Split\([^,]+,\s*"\."\)`)
	bareNameComparisonRe        = regexp.MustCompile(`\b(?:msg|svc|service|enum|stateRoot)\.Name\s*==\s*(?:simpleName|name|typ|outerClass|svcName|keyEnumType)\b|\b(?:simpleName|name|typ|outerClass|svcName|keyEnumType)\s*==\s*(?:msg|svc|service|enum|stateRoot)\.Name\b`)
)

// renderSpiffeSoundness reports SPIFFE verifier/construction smell shapes that
// must stay centralized in sys/spiffe, plus literal-name lookup shapes that
// must stay package-qualified. This is intentionally a small static gate: it
// catches the exact OF-12 family (scheme interpolation, URI path splitting, and
// prefix comparisons) and the OF-19 bare-name descriptor match shape while
// runtime tests prove semantics.
func renderSpiffeSoundness(root string) int {
	violations, err := spiffeSoundnessViolations(root)
	fmt.Println("\n## SPIFFE/literal soundness probes (ENFORCED)")
	fmt.Println("\nGenerated code outside sys/spiffe must not hand-roll SPIFFE URI parsing/construction, and descriptor lookups must not strip package context before matching names.")
	if err != nil {
		fmt.Printf("- · scan failed: %v\n", err)
		return 1
	}
	if len(violations) == 0 {
		fmt.Println("- ✓ no hand-rolled SPIFFE parsing/construction or bare-name descriptor-match shapes found")
		return 0
	}
	for _, v := range violations {
		fmt.Printf("- · %s:%d [%s] %s\n", v.RelPath, v.Line, v.Rule, strings.TrimSpace(v.Text))
	}
	return len(violations)
}

func spiffeSoundnessViolations(root string) ([]spiffeSoundnessViolation, error) {
	var files []string
	for _, rel := range []string{
		"pkg/templates",
		"pkg/internal",
	} {
		path := filepath.Join(root, rel)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, path)
			continue
		}
		if err := filepath.WalkDir(path, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			files = append(files, path)
			return nil
		}); err != nil {
			return nil, err
		}
	}

	var out []spiffeSoundnessViolation
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		if skipSpiffeSoundnessPath(rel) {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if skipSpiffeSoundnessLine(line) {
				continue
			}
			window := spiffeSoundnessWindow(lines, i)
			add := func(rule string) {
				out = append(out, spiffeSoundnessViolation{
					RelPath: rel,
					Line:    i + 1,
					Rule:    rule,
					Text:    line,
				})
			}
			switch {
			case spiffeSchemeInterpolationRe.MatchString(line):
				add("scheme-constant interpolation")
			case spiffeRawInterpolationRe.MatchString(line):
				add("raw SPIFFE interpolation")
			case prefixCompareRe.MatchString(line) && strings.Contains(strings.ToLower(window), "spiffe"):
				add("SPIFFE prefix comparison")
			case strings.Contains(strings.ToLower(window), "spiffe") &&
				(uriPathExtractionRe.MatchString(line) || pathSplitRe.MatchString(line)) &&
				uriPathExtractionRe.MatchString(window) && pathSplitRe.MatchString(window):
				add("URI path splitting")
			case bareNameComparisonRe.MatchString(line) &&
				bareNameExtractionRe.MatchString(bareNameSoundnessWindow(lines, i)) &&
				!strings.Contains(bareNameSoundnessWindow(lines, i), "ProtoInfo.Package"):
				add("bare-name descriptor match")
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RelPath != out[j].RelPath {
			return out[i].RelPath < out[j].RelPath
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Rule < out[j].Rule
	})
	return out, nil
}

func spiffeSoundnessWindow(lines []string, idx int) string {
	start := idx - 2
	if start < 0 {
		start = 0
	}
	end := idx + 3
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start:end], "\n")
}

func bareNameSoundnessWindow(lines []string, idx int) string {
	start := idx - 8
	if start < 0 {
		start = 0
	}
	end := idx + 3
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start:end], "\n")
}

func skipSpiffeSoundnessPath(rel string) bool {
	// The spiffe lib IS the canonical SPIFFE parser, so it is exempt. The runtime
	// libs live under sys/ for most languages and chassis/ for those already moved
	// off the declarative namespace (SYS_RENAME Phase 2).
	if strings.Contains(rel, "/sys/spiffe/") || strings.Contains(rel, "/chassis/spiffe/") {
		return true
	}
	for _, suffix := range []string{
		".md", ".json", ".proto", "_test.go", "_test.go.tmpl", "test_spiffe.py.tmpl",
		".test.ts.tmpl", "Test.java.tmpl",
	} {
		if strings.HasSuffix(rel, suffix) {
			return true
		}
	}
	return false
}

func skipSpiffeSoundnessLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" ||
		strings.HasPrefix(trimmed, "//") ||
		strings.HasPrefix(trimmed, "#") ||
		strings.HasPrefix(trimmed, "*") ||
		strings.HasPrefix(trimmed, "/*")
}

type storeVendorViolation struct {
	RelPath string
	Line    int
	Token   string
	Text    string
}

// storeVendorForbidden lists backend-store product/technology names (and the two
// store-specific query directives) that must not appear anywhere in the tacplus
// source tree. tacplus decoupled store materialization: it no longer generates
// ClickHouse DDL or Dgraph DQL, so any reference to a concrete store vendor is a
// regression that re-couples the contract to a backend. Matched case-insensitively
// on a word boundary (see storeVendorPattern), so "graphstore"/"columnarstore"
// and similar store-neutral names are unaffected.
var storeVendorForbidden = []string{
	"clickhouse", "dgraph", "dql", "nquad", "mergetree", "replacingmergetree",
	"iceberg", "ceph", "cassandra", "scylla", "redis", "postgresql", "postgres",
	"mysql", "sqlite", "elasticsearch", "rabbitmq", "pulsar", "snowflake",
	"bigquery", "dynamodb", "rocksdb", "badger", "minio",
	// The event transport is neutral too: tacplus's generated EventBus contract and
	// code speak a generic gRPC "broker" streaming protocol, never a vendor's API.
	// The concrete Kafka binding lives in the platform's EventBus service, outside
	// tacplus — so no transport vendor name belongs in this tree either.
	"kafka",
	// Store-query directive strings that leaked from DQL/SQL generation.
	"@reverse", "@upsert", "CREATE TABLE",
}

// storeVendorAllow lists tokens that are legitimate, accepted architectural
// couplings and therefore must NOT be flagged. Each entry needs a real reason —
// do not add a token here just to make the gate pass. Currently empty: the
// former "kafka" exception was retired once tacplus was confirmed transport-
// neutral (the generated EventBus code speaks a generic gRPC broker protocol).
var storeVendorAllow = map[string]string{}

// storeVendorScanRoots are the source roots under -root walked by the gate.
// EXCLUDED entirely: target/ (build artifacts), archive/ (historical docs),
// roadmap/ (plans legitimately discuss vendors), todo/ (git-ignored ledgers),
// and .git/. The gate's own source file is skipped by path (it necessarily
// contains the forbidden list).
var storeVendorScanRoots = []string{"pkg", "cmd", "docs"}

// storeVendorPattern matches any forbidden token case-insensitively. Word tokens
// use \b boundaries so store-neutral names (graphstore, columnarstore) don't
// false-positive; the directive strings (@reverse, @upsert, CREATE TABLE) are
// matched literally since \b does not apply cleanly to a leading '@' or a space.
var storeVendorPattern = buildStoreVendorPattern()

func buildStoreVendorPattern() *regexp.Regexp {
	var alts []string
	for _, tok := range storeVendorForbidden {
		q := regexp.QuoteMeta(tok)
		// Apply a word boundary only where the token edge is a word char, so
		// "@reverse"/"@upsert"/"CREATE TABLE" match literally.
		left, right := "", ""
		if isWordChar(tok[0]) {
			left = `\b`
		}
		if isWordChar(tok[len(tok)-1]) {
			right = `\b`
		}
		alts = append(alts, left+q+right)
	}
	return regexp.MustCompile(`(?i)(` + strings.Join(alts, "|") + `)`)
}

func isWordChar(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// renderStoreVendorSoundness fails the build if any backend-store or transport
// vendor name or store-query directive leaked into the tacplus source tree, so the
// store-materialization decoupling and transport-neutrality can't silently regress.
// This is a read-only scan; it modifies nothing.
func renderStoreVendorSoundness(root string) int {
	violations, err := storeVendorViolations(root)
	fmt.Println("\n## Store-vendor soundness probe (ENFORCED)")
	fmt.Println("\ntacplus no longer generates any store DDL/DQL, and its EventBus contract/code are")
	fmt.Println("transport-neutral, so no concrete backend-store or transport vendor name (or store-")
	fmt.Println("query directive) may appear in the source tree. A hit is a build-failing regression.")
	if err != nil {
		fmt.Printf("- · scan failed: %v\n", err)
		return 1
	}
	if len(violations) == 0 {
		fmt.Println("- ✓ no forbidden backend-store vendor names or store-query directives found")
		return 0
	}
	for _, v := range violations {
		fmt.Printf("- · %s:%d [%s] %s\n", v.RelPath, v.Line, v.Token, strings.TrimSpace(v.Text))
	}
	return len(violations)
}

// storeVendorGuardSelf are the gate's own source files, skipped by path because
// they necessarily contain the forbidden list (main.go) or seed forbidden tokens
// to test the gate (main_test.go).
var storeVendorGuardSelf = map[string]bool{
	filepath.ToSlash(filepath.Join("cmd", "soundness", "main.go")):      true,
	filepath.ToSlash(filepath.Join("cmd", "soundness", "main_test.go")): true,
}

func storeVendorViolations(root string) ([]storeVendorViolation, error) {

	var out []storeVendorViolation
	for _, rel := range storeVendorScanRoots {
		base := filepath.Join(root, rel)
		info, err := os.Stat(base)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		if err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if skipStoreVendorDir(rel) {
					return filepath.SkipDir
				}
				return nil
			}
			// The gate's own source necessarily contains the forbidden list.
			if storeVendorGuardSelf[rel] {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			lines := strings.Split(string(b), "\n")
			for i, line := range lines {
				for _, m := range storeVendorPattern.FindAllString(line, -1) {
					if _, ok := storeVendorAllow[strings.ToLower(m)]; ok {
						continue
					}
					out = append(out, storeVendorViolation{
						RelPath: rel,
						Line:    i + 1,
						Token:   m,
						Text:    line,
					})
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RelPath != out[j].RelPath {
			return out[i].RelPath < out[j].RelPath
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Token < out[j].Token
	})
	return out, nil
}

// skipStoreVendorDir excludes build artifacts, historical docs, vendor-discussing
// plans, git-ignored ledgers, and the git dir from the store-vendor scan.
func skipStoreVendorDir(rel string) bool {
	switch rel {
	case "target", "archive", "roadmap", "todo", ".git":
		return true
	}
	for _, seg := range []string{"target", "archive", "roadmap", "todo", ".git"} {
		if strings.HasPrefix(rel, seg+"/") ||
			strings.Contains(rel, "/"+seg+"/") ||
			strings.HasSuffix(rel, "/"+seg) {
			return true
		}
	}
	return false
}
