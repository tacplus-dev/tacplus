// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/keepaliveconst"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/registry"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// Note: uses loadTemplate from servicewrapper.go

// singletonBoundary describes a non-repeated message field in the state root.
type singletonBoundary struct {
	FieldName      string // PascalCase proto field name: "UserProfile", "Roster"
	TypeName       string // PascalCase message type: "UserProfile", "TeamRoster"
	GoType         string // Go type: "TeamRoster" or "ballsports_pb.EmployeeSalary"
	IsCrossPackage bool   // true if type is from another package (don't prefix with types.)
}

// collectionBoundary describes a repeated message field in the state root.
type collectionBoundary struct {
	FieldName       string // PascalCase proto field name: "Players"
	TypeName        string // PascalCase item message type: "Player"
	GoType          string // Go type: "Player" or "ballsports_pb.Player"
	KeyFieldName    string // PascalCase Go field name for indexed key: "PlayerId"
	KeyGoType       string // Go type for key: "string"
	IsCrossPackage  bool   // true if type is from another package
	EvictionMaxSize int64  // max entries (from spec.eviction_max_size), 0 = use default
}

// topicBoundary describes a pub/sub topic field in the state root.
type topicBoundary struct {
	FieldName      string // PascalCase: "EventStream"
	TypeName       string // PascalCase message type: "EventData"
	GoType         string // "EventData" or "pkg_pb.EventData"
	IsCrossPackage bool
}

// queueBoundary describes a FIFO queue field in the state root.
type queueBoundary struct {
	FieldName      string // PascalCase: "TaskQueue"
	TypeName       string // PascalCase message type: "TaskItem"
	GoType         string
	IsCrossPackage bool
}

// priorityQueueBoundary describes a priority-ordered queue field in the state root.
type priorityQueueBoundary struct {
	FieldName      string // PascalCase: "AlertQueue"
	TypeName       string // PascalCase message type: "AlertItem"
	GoType         string
	PriorityField  string // field used for ordering: "severity"
	IsCrossPackage bool
}

// commandBoundary describes a command RPC field in the state root.
type commandBoundary struct {
	FieldName      string // PascalCase: "ReloadConfig"
	TypeName       string // PascalCase message type: "ReloadConfigRequest"
	GoType         string // "ReloadConfigRequest" or "pkg_pb.ReloadConfigRequest"
	IsCrossPackage bool
}

// viewBoundary describes a read-only view over multiple source collections.
type viewBoundary struct {
	FieldName      string   // PascalCase: "AllKeys"
	TypeName       string   // PascalCase item message type: "ManagedJwk"
	GoType         string   // Go type: "ManagedJwk" or "jwk_pb.ManagedJwk"
	KeyFieldName   string   // PascalCase Go field name for indexed key: "Kid"
	KeyGoType      string   // Go type for key: "string"
	IsCrossPackage bool     // true if type is from another package
	ViewSources    []string // source collection field names: ["ConfigKeys", "DynamicKeys", "ExternalKeys"]
}

// crossImportEntry represents a cross-package Go import for the state wrapper.
type crossImportEntry struct {
	Alias string
	Path  string
}

// stateImportField describes a field with a (spec.state_import) annotation —
// a read-only reference to a boundary owned by another service's state root.
type stateImportField struct {
	FieldName      string // PascalCase proto field name: "TenantConfig"
	TypeName       string // PascalCase message type: "TenantConfig"
	GoType         string // Go type: "TenantConfig" or "config_pb.TenantConfig"
	IsCrossPackage bool   // true if type is from another package
}

// stateClientData is the template data for generating state_client.go.
type stateClientData struct {
	PackageName           string
	ClientTypeName        string // e.g. "StateClient"
	StateServiceName      string // gRPC state service: "BaseballStateService"
	StateRootName         string // PascalCase state root message: "BaseballState" (from service_state annotation)
	TypesImportPath       string // import path for types (types/services/baseball)
	ServiceManagerLibPath string // import path for servicemanager lib (chassis/servicemanager)
	ServiceIMLibPath      string // import path for the identity manager (chassis/service_im)
	SpecTypesPath         string // import path for Lock types (lock)
	Singletons            []singletonBoundary
	Collections           []collectionBoundary
	Topics                []topicBoundary
	Queues                []queueBoundary
	PriorityQueues        []priorityQueueBoundary
	Commands              []commandBoundary
	Views                 []viewBoundary
	StateImports          []stateImportField
	CrossImports          []crossImportEntry
	TraceTypesPath        string // import path for trace types (types/sys/trace)
	ReadOnly              bool   // true for :ro (accessors only, no mutators)
	WriteOnce             bool   // true for :worm (write-once read-many: set/add/publish but no delete)
}

// boundaryInfo describes a state boundary for registry generation.
type boundaryInfo struct {
	FieldName    string // "Atomic", "Converged", "Roster"
	IsCollection bool   // true if repeated (collection boundary)
}

// generatedStateClient tracks info about a generated state client for the root StateClients.
type generatedStateClient struct {
	PackageName    string // proto package: "ironmaiden" (used for lock keys)
	DirName        string // snake_case directory name: "iron_maiden_internal" (unique per state root)
	StateRootName  string // PascalCase state root: "IronMaidenInternal"
	ClientTypeName string // e.g. "StateClient"
	ImportPath     string // e.g. "example.com/myproj/tacplus/sm/internal/state/services/iron_maiden"
	Group          string // top-level proto dir: "services"
	Boundaries     []boundaryInfo
}

// generateStateClientLib generates sys/servicemanager/servicemanager.go.
// This is the shared State that owns the config and gRPC connection.
func generateStateClientLib(config *tactype.Config) error {
	grpcconfigTypesPath := fmt.Sprintf("%s/types/sys/grpcconfig", config.ModuleBasePath)

	stateClientLibTmplStr, err := loadTemplate("servicemanager_lib.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load stateclient lib template: %w", err)
	}
	tmpl, err := template.New("stateclientlib").Parse(stateClientLibTmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse stateclient lib template: %w", err)
	}

	tokenLibPath := fmt.Sprintf("%s/chassis/token", config.ModuleBasePath)
	keyLibPath := fmt.Sprintf("%s/chassis/key", config.ModuleBasePath)
	x509LibPath := fmt.Sprintf("%s/chassis/x509", config.ModuleBasePath)
	x509TypesPath := fmt.Sprintf("%s/types/sys/x509", config.ModuleBasePath)
	keyTypesPath := fmt.Sprintf("%s/types/sys/key", config.ModuleBasePath)
	loggerTypesPath := fmt.Sprintf("%s/types/sys/logger", config.ModuleBasePath)

	// Dynamically discover the proto package that defines the token exchange service.
	texInfo, err := parser.DiscoverTokenExchangerPackage(config.AnnotatedProtoDir)
	if err != nil {
		return err
	}
	if texInfo == nil {
		return generateStateClientLibWithoutTokenExchanger(config)
	}
	if texInfo.X509MintMethod == "" {
		return fmt.Errorf("token minter %s.%s must define an RPC accepting X509MintRequest and returning X509CertChain", texInfo.Package, texInfo.ServiceName)
	}
	stateServiceTypesPath := fmt.Sprintf("%s/types/%s", config.ModuleBasePath, texInfo.Dir)

	jwkTypesPath := fmt.Sprintf("%s/types/sys/jwk", config.ModuleBasePath)
	jwkLibPath := fmt.Sprintf("%s/chassis/jwk", config.ModuleBasePath)
	authTypesPath := fmt.Sprintf("%s/types/sys/auth", config.ModuleBasePath)
	commonTypesPath := fmt.Sprintf("%s/types/sys/tokenrequest", config.ModuleBasePath)

	hostTypesPath := fmt.Sprintf("%s/types/sys/host", config.ModuleBasePath)

	imLibPath := fmt.Sprintf("%s/chassis/service_im", config.ModuleBasePath)

	data := struct {
		GrpcconfigTypesPath        string
		StateServiceTypesPath      string
		HostTypesPath              string
		LoggerTypesPath            string
		TokenLibPath               string
		KeyLibPath                 string
		X509LibPath                string
		X509TypesPath              string
		KeyTypesPath               string
		JwkTypesPath               string
		JwkLibPath                 string
		SpiffeLibPath              string
		AuthTypesPath              string
		CommonTypesPath            string
		ServiceAudience            string
		MintServiceName            string
		X509MintMethod             string
		ImLibPath                  string
		TraceTypesPath             string
		AuthServices               []string
		KeepaliveTimeSecs          int
		KeepaliveTimeoutSecs       int
		KeepalivePermitMinTimeSecs int
	}{
		GrpcconfigTypesPath:   grpcconfigTypesPath,
		StateServiceTypesPath: stateServiceTypesPath,
		HostTypesPath:         hostTypesPath,
		LoggerTypesPath:       loggerTypesPath,
		TokenLibPath:          tokenLibPath,
		KeyLibPath:            keyLibPath,
		X509LibPath:           x509LibPath,
		X509TypesPath:         x509TypesPath,
		KeyTypesPath:          keyTypesPath,
		JwkTypesPath:          jwkTypesPath,
		JwkLibPath:            jwkLibPath,
		SpiffeLibPath:         fmt.Sprintf("%s/chassis/spiffe", config.ModuleBasePath),
		AuthTypesPath:         authTypesPath,
		CommonTypesPath:       commonTypesPath,
		// The serving TLS certificate must identify the generated service
		// itself, not the token exchanger it asks to mint the certificate.
		ServiceAudience:            config.ServiceLabel(),
		MintServiceName:            texInfo.ServiceName,
		X509MintMethod:             texInfo.X509MintMethod,
		ImLibPath:                  imLibPath,
		TraceTypesPath:             fmt.Sprintf("%s/types/sys/trace", config.ModuleBasePath),
		KeepaliveTimeSecs:          keepaliveconst.TimeSecs,
		KeepaliveTimeoutSecs:       keepaliveconst.TimeoutSecs,
		KeepalivePermitMinTimeSecs: keepaliveconst.PermitMinTimeSecs,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute stateclient lib template: %w", err)
	}

	formatted, fmtErr := format.Source(buf.Bytes())
	if fmtErr != nil {
		zap.L().Warn("failed to format stateclient.go, writing unformatted", zap.Error(fmtErr))
		formatted = buf.Bytes()
	}

	outputDir := filepath.Join(config.OutputDir, "chassis", "servicemanager")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create stateclient lib directory: %w", err)
	}

	outputPath := filepath.Join(outputDir, "servicemanager.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write servicemanager.go: %w", err)
	}

	zap.L().Info("Generated servicemanager lib", zap.String("path", outputPath))
	return nil
}

func generateStateClientLibWithoutTokenExchanger(config *tactype.Config) error {
	source := fmt.Sprintf(`// Copyright (c) 2026 Second Star Cavalier LLC
// Generated by state wrapper generator
//
// CODE GENERATED - DO NOT EDIT

package servicemanager

import (
	"context"
	gocrypto "crypto"
	"errors"

	service_im "%[1]s/chassis/service_im"
	wrapperToken "%[1]s/chassis/token"
	grpcconfig_pb "%[1]s/types/sys/grpcconfig"
	host_pb "%[1]s/types/sys/host"
	x509_pb "%[1]s/types/sys/x509"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ErrMissingTrustDomain mirrors the sentinel of the same name in the
// token-exchanger variant of this package. This degraded variant never builds a
// service-mesh channel, so it never returns it — but the generated ServiceManager
// classifies permanent identity-init failures by errors.Is against
// servicemanager.ErrMissingTrustDomain in EVERY build (NEW-21), so both variants
// must expose the same package API or a no-minter build does not compile.
var ErrMissingTrustDomain = errors.New("spiffe_trust_domain is required for service-mesh TLS; refusing CA-only trust with no service-identity pin (fail-closed)")

type ServiceManager struct {
	config   *grpcconfig_pb.Config
	identity *service_im.IdentityManager
}

func NewServiceManager(ctx context.Context, cfg *grpcconfig_pb.Config, identity *service_im.IdentityManager) (*ServiceManager, error) {
	_ = ctx
	return nil, service_im.ErrTokenMinterNotConfigured
}

func (bc *ServiceManager) Config() *grpcconfig_pb.Config {
	if bc == nil {
		return nil
	}
	return bc.config
}

func (bc *ServiceManager) DialOpts() []grpc.DialOption { return nil }

func (bc *ServiceManager) TLSCertChain() *x509_pb.X509CertChain { return nil }

func (bc *ServiceManager) TLSPrivateKey() gocrypto.PrivateKey { return nil }

func (bc *ServiceManager) TLSCertMaterial() (*x509_pb.X509CertChain, gocrypto.PrivateKey) {
	return nil, nil
}

func (bc *ServiceManager) ActiveConn() *grpc.ClientConn { return nil }

func (bc *ServiceManager) Conn() *grpc.ClientConn { return nil }

func (bc *ServiceManager) CreateChannel(target string) (*grpc.ClientConn, error) {
	_ = target
	return nil, service_im.ErrTokenMinterNotConfigured
}

func (bc *ServiceManager) InstanceID() string {
	if bc == nil || bc.config == nil {
		return ""
	}
	return bc.config.GetInstanceId()
}

func (bc *ServiceManager) Close() error { return nil }

func (bc *ServiceManager) ReconcileEndpoints(newHosts []*host_pb.Host) {
	_ = newHosts
}

const GrpcRequestIDHeader = "x-request-id"

type Caller struct {
	token     *wrapperToken.TokenWrapper
	requestID string
}

func NewCaller(token *wrapperToken.TokenWrapper, requestID string) *Caller {
	if token != nil && token.Proto() != nil {
		token.Proto().Signature = ""
	}
	return &Caller{token: token, requestID: requestID}
}

func (c *Caller) Token() *wrapperToken.TokenWrapper {
	if c == nil {
		return nil
	}
	return c.token
}

func (c *Caller) RequestID() string {
	if c == nil {
		return ""
	}
	return c.requestID
}

func RequestIDFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(GrpcRequestIDHeader)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
`, config.ModuleBasePath)

	formatted, fmtErr := format.Source([]byte(source))
	if fmtErr != nil {
		zap.L().Warn("failed to format no-minter servicemanager.go, writing unformatted", zap.Error(fmtErr))
		formatted = []byte(source)
	}

	outputDir := filepath.Join(config.OutputDir, "chassis", "servicemanager")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create servicemanager lib directory: %w", err)
	}
	outputPath := filepath.Join(outputDir, "servicemanager.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write servicemanager.go: %w", err)
	}
	zap.L().Info("Generated no-minter servicemanager lib", zap.String("path", outputPath))
	return nil
}

// generateIdentityManager generates sys/service_im/service_im.go.
// This is the standalone service identity manager that owns token exchange on a bare connection.
func generateIdentityManager(config *tactype.Config) error {
	tmplStr, err := loadChassisTemplate("service_im/service_im.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load service_im template: %w", err)
	}
	tmpl, err := template.New("service_im").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse service_im template: %w", err)
	}

	// Dynamically discover the proto package that defines the token exchange service.
	texInfo, err := parser.DiscoverTokenExchangerPackage(config.AnnotatedProtoDir)
	if err != nil {
		return err
	}
	if texInfo == nil {
		return generateIdentityManagerWithoutTokenExchanger(config)
	}

	// Build virtual→physical audience mappings for audienceFromMethod.
	// Virtual service packages (opa_eval, graph_mutation, timeseries_mutation)
	// need to map to their physical host service for correct token audience.
	type audienceMapping struct {
		Package  string // virtual service proto package (e.g. "opa_eval")
		Audience string // physical service name (e.g. "opa")
	}
	var audienceMappings []audienceMapping
	mappingSeen := make(map[string]bool) // dedup: package already mapped
	for _, entry := range config.GrpcS2SClients {
		if entry.TargetService != "" {
			// Use explicit ProtoPackage when set (e.g. "userentitlements" for vservices/entitlements.proto).
			// Fall back to deriving the package from the filename (works when package == basename).
			pkg := entry.ProtoPackage
			if pkg == "" {
				pkg = strings.TrimSuffix(filepath.Base(entry.ProtoFile), ".proto")
			}
			if pkg != entry.TargetService && !mappingSeen[pkg] {
				audienceMappings = append(audienceMappings, audienceMapping{
					Package:  pkg,
					Audience: entry.TargetService,
				})
				mappingSeen[pkg] = true
			}
		}
	}

	// Map state-service proto packages to the state store's service identity.
	// State RPCs physically go to the state store (kernel), but their gRPC
	// method names carry the owning service's proto package (e.g.
	// "oauth.OauthConfigStateService/WatchConfig"). Without this mapping,
	// audienceFromMethod resolves "oauth" as the audience, the caller tries
	// to mint a token for "oauth", and kernel rejects it because the caller's
	// delegate is only authorized for [self, kernel]. The correct audience is
	// the state store service — the caller IS authorized for that.
	stateStoreServiceID := ""
	for _, b := range config.States {
		if b.IsHealthState && b.AccessMode != tactype.AccessRO {
			if stateInfo, err := proto.DiscoverStateRoot(config.FlatProtoDir, b.MessageName); err == nil && stateInfo != nil {
				stateStoreServiceID = stateInfo.Package
			}
		}
	}
	if stateStoreServiceID != "" {
		for _, b := range config.States {
			var pkg string
			if stateInfo, err := proto.DiscoverStateRoot(config.FlatProtoDir, b.MessageName); err == nil && stateInfo != nil {
				pkg = stateInfo.Package
			} else {
				pkg = strings.TrimSuffix(filepath.Base(b.ProtoFile), ".proto")
			}
			if pkg != "" && pkg != stateStoreServiceID && !mappingSeen[pkg] {
				audienceMappings = append(audienceMappings, audienceMapping{
					Package:  pkg,
					Audience: stateStoreServiceID,
				})
				mappingSeen[pkg] = true
			}
		}
	}

	data := struct {
		GrpcconfigTypesPath        string
		StateServiceTypesPath      string
		CommonTypesPath            string
		CloudIdentityTypesPath     string
		CloudVendorTypesPath       string
		KeyTypesPath               string
		KeyLibPath                 string
		TokenLibPath               string
		TokenTypesPath             string
		X509LibPath                string
		ServiceAudience            string
		MintServiceName            string
		AudienceMappings           []audienceMapping
		KeepaliveTimeSecs          int
		KeepaliveTimeoutSecs       int
		KeepalivePermitMinTimeSecs int
	}{
		GrpcconfigTypesPath:        fmt.Sprintf("%s/types/sys/grpcconfig", config.ModuleBasePath),
		StateServiceTypesPath:      fmt.Sprintf("%s/types/%s", config.ModuleBasePath, texInfo.Dir),
		CommonTypesPath:            fmt.Sprintf("%s/types/sys/tokenrequest", config.ModuleBasePath),
		CloudIdentityTypesPath:     fmt.Sprintf("%s/types/sys/cloudidentity", config.ModuleBasePath),
		CloudVendorTypesPath:       fmt.Sprintf("%s/types/sys/cloudvendor", config.ModuleBasePath),
		KeyTypesPath:               fmt.Sprintf("%s/types/sys/key", config.ModuleBasePath),
		KeyLibPath:                 fmt.Sprintf("%s/chassis/key", config.ModuleBasePath),
		TokenLibPath:               fmt.Sprintf("%s/chassis/token", config.ModuleBasePath),
		TokenTypesPath:             fmt.Sprintf("%s/types/sys/token", config.ModuleBasePath),
		X509LibPath:                fmt.Sprintf("%s/chassis/x509", config.ModuleBasePath),
		ServiceAudience:            texInfo.Package,
		MintServiceName:            texInfo.ServiceName,
		AudienceMappings:           audienceMappings,
		KeepaliveTimeSecs:          keepaliveconst.TimeSecs,
		KeepaliveTimeoutSecs:       keepaliveconst.TimeoutSecs,
		KeepalivePermitMinTimeSecs: keepaliveconst.PermitMinTimeSecs,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute service_im template: %w", err)
	}

	formatted, fmtErr := format.Source(buf.Bytes())
	if fmtErr != nil {
		zap.L().Warn("failed to format service_im.go, writing unformatted", zap.Error(fmtErr))
		formatted = buf.Bytes()
	}

	outputDir := filepath.Join(config.OutputDir, "chassis", "service_im")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create service_im directory: %w", err)
	}

	outputPath := filepath.Join(outputDir, "service_im.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write service_im.go: %w", err)
	}

	zap.L().Info("Generated service identity manager", zap.String("path", outputPath))
	return nil
}

func generateIdentityManagerWithoutTokenExchanger(config *tactype.Config) error {
	source := fmt.Sprintf(`// Copyright (c) 2026 Second Star Cavalier LLC
// Generated by service generator
//
// CODE GENERATED - DO NOT EDIT

package service_im

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	grpcconfig_pb "%[1]s/types/sys/grpcconfig"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"go.uber.org/zap"
)

// ErrMissingTrustDomain mirrors the sentinel of the same name in the
// token-exchanger variant of this package. This degraded variant builds no
// bootstrap TLS context, so it never returns it — but the generated
// ServiceManager classifies against service_im.ErrMissingTrustDomain in EVERY
// build (NEW-21), so both variants must expose the same package API or a
// no-minter build does not compile.
var ErrMissingTrustDomain = errors.New("im: spiffe_trust_domain is required for bootstrap TLS; refusing CA-only trust with no service-identity pin (fail-closed)")

// ErrTokenMinterNotConfigured mirrors the same sentinel in the token-exchanger
// variant of this package: this build has no exchanger, so no service token can
// ever be minted. TYPED so the ServiceManager startup loop classifies it as a
// PERMANENT failure with errors.Is instead of matching the message (NEW-21).
var ErrTokenMinterNotConfigured = errors.New("im: service token minter is not configured")
var errNoTokenSource = errors.New("no token source configured")

type IdentityManager struct {
	cfg *grpcconfig_pb.Config
}

func New(ctx context.Context, cfg *grpcconfig_pb.Config) (*IdentityManager, error) {
	_ = ctx
	if cfg != nil && cfg.GetPrivateKey() != nil {
		return nil, ErrTokenMinterNotConfigured
	}
	return &IdentityManager{cfg: cfg}, nil
}

func (im *IdentityManager) GetToken() (string, error) {
	return "", errNoTokenSource
}

func (im *IdentityManager) GetTokenFor(audience string) (string, error) {
	_ = audience
	return "", errNoTokenSource
}

func (im *IdentityManager) RefreshToken() error {
	return errNoTokenSource
}

func (im *IdentityManager) InstanceId() string {
	if im == nil || im.cfg == nil {
		return ""
	}
	return im.cfg.GetInstanceId()
}

func (im *IdentityManager) HasAuth() bool { return false }

func (im *IdentityManager) UnaryInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func (im *IdentityManager) StreamInterceptor() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(ctx, desc, cc, method, opts...)
	}
}

func (im *IdentityManager) Close() {}

// -----------------------------------------------------------------------------
// Credential rejection recovery
// -----------------------------------------------------------------------------
//
// Mirrors the surface of the same section in the token-exchanger variant of this
// package. The generated state wrappers, subscribers and ServiceManager call
// NoteIfCredentialRejection / NoteCredentialAccepted / CredentialsDegraded
// UNCONDITIONALLY, so both variants must expose them or a no-minter build does
// not compile (NEW-47).
//
// The re-mint half of the token-exchanger implementation
// (RegisterCredentialInvalidator, which drops the cached token) is deliberately
// absent: this variant mints nothing and caches nothing, so there is no token to
// invalidate. The COUNTING half is kept as-is, because it is what the health
// floor reads: a build whose calls are rejected over and over still has to stop
// reporting itself Ready.

var consecutiveRejects atomic.Int64

// degradedAfter is the number of rejections in a row before the service reports
// itself unhealthy. Same threshold and same reasoning as the token-exchanger
// variant: a single rejection is a normal event, three in a row is not.
const degradedAfter = 3

// IsCredentialRejection reports whether an error means our credential was
// REJECTED, as opposed to the peer being unreachable or the call being refused
// on authorization grounds.
//
// Deliberately excludes PermissionDenied: that means the credential was
// understood and refused, so re-presenting the same identity would loop forever.
func IsCredentialRejection(err error) bool {
	if err == nil {
		return false
	}
	if status.Code(err) == codes.Unauthenticated {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unauthenticated") ||
		strings.Contains(msg, "does not have valid authentication credentials") ||
		strings.Contains(msg, "token validation failed")
}

// NoteCredentialAccepted clears the degraded state after an accepted call.
func NoteCredentialAccepted() {
	consecutiveRejects.Store(0)
}

// NoteIfCredentialRejection records a rejection when the error looks like one.
// Returns true when it did. There is no cached credential to drop in this
// variant, so unlike the token-exchanger one it only counts.
func NoteIfCredentialRejection(err error) bool {
	if IsCredentialRejection(err) {
		consecutiveRejects.Add(1)
		zap.L().Warn("credential was rejected and this build has no token minter — nothing can re-mint it")
		return true
	}
	return false
}

// CredentialsDegraded reports whether calls keep coming back rejected. Wired to
// the health floor so readiness stops reporting Ready for a service that cannot
// authenticate at all.
func CredentialsDegraded() bool {
	return consecutiveRejects.Load() >= degradedAfter
}
`, config.ModuleBasePath)

	formatted, fmtErr := format.Source([]byte(source))
	if fmtErr != nil {
		zap.L().Warn("failed to format no-minter service_im.go, writing unformatted", zap.Error(fmtErr))
		formatted = []byte(source)
	}

	outputDir := filepath.Join(config.OutputDir, "chassis", "service_im")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create service_im directory: %w", err)
	}
	outputPath := filepath.Join(outputDir, "service_im.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write service_im.go: %w", err)
	}
	zap.L().Info("Generated no-minter service identity manager", zap.String("path", outputPath))
	return nil
}

// generateClientIdentityManager generates chassis/client_im/{source,manager}.go —
// the client identity manager for CLI tools/daemons/webapps that authenticate as
// users (OAuth) or via credential pairs. Clients have no S2S identity, so the
// manager mints via the client-reachable (C2S) exchanger (a {proxy: true} minter
// front, or the authoritative minter as a fallback), NOT the authoritative S2S
// mint that service_im uses.
//
// The mint-independent source.go is emitted as-is; the mint-dependent manager.go
// is templated with the discovered C2S exchanger so it targets the real
// TokenMint RPC. When no client-reachable minter is discovered, the manager
// still compiles but every mint call returns "token minter is not configured".
func generateClientIdentityManager(config *tactype.Config) error {
	outputDir := filepath.Join(config.OutputDir, "chassis", "client_im")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create client_im directory: %w", err)
	}

	// source.go — the TokenSource seam. Mint-independent; no template data.
	if err := renderClientIMFile(
		"client_im/source.go.tmpl", "source", struct{}{},
		filepath.Join(outputDir, "source.go"),
	); err != nil {
		return err
	}

	// Discover the client-reachable (C2S) mint exchanger — the {proxy: true}
	// front the client mints through. Falls back to the authoritative minter if
	// no proxy is declared. Kept SEPARATE from the authoritative S2S exchanger
	// that service_im discovers (mirrors rust's libs.go two-exchanger split).
	clientInfo, err := parser.DiscoverClientTokenExchangerPackage(config.AnnotatedProtoDir)
	if err != nil {
		return fmt.Errorf("client token exchanger discovery: %w", err)
	}

	hasClientTokenExchanger := false
	clientMintTypesPath := ""
	clientMintServiceName := ""
	clientTokenMintMethod := ""
	if clientInfo != nil {
		if clientInfo.TokenMintMethod == "" {
			return fmt.Errorf("client token minter %s.%s must define an RPC accepting TokenMintRequest and returning TokenMintResponse", clientInfo.Package, clientInfo.ServiceName)
		}
		hasClientTokenExchanger = true
		clientMintTypesPath = fmt.Sprintf("%s/types/%s", config.ModuleBasePath, clientInfo.Dir)
		clientMintServiceName = clientInfo.ServiceName
		clientTokenMintMethod = clientInfo.TokenMintMethod
	}

	data := struct {
		TokenRequestTypesPath   string
		TokenTypesPath          string
		HasClientTokenExchanger bool
		ClientMintTypesPath     string // Go import path for the C2S mint proto stub
		ClientMintServiceName   string // C2S mint proto service name (client = New<Name>Client)
		ClientTokenMintMethod   string // C2S TokenMint RPC name
	}{
		TokenRequestTypesPath:   fmt.Sprintf("%s/types/sys/tokenrequest", config.ModuleBasePath),
		TokenTypesPath:          fmt.Sprintf("%s/types/sys/token", config.ModuleBasePath),
		HasClientTokenExchanger: hasClientTokenExchanger,
		ClientMintTypesPath:     clientMintTypesPath,
		ClientMintServiceName:   clientMintServiceName,
		ClientTokenMintMethod:   clientTokenMintMethod,
	}

	if err := renderClientIMFile(
		"client_im/manager.go.tmpl", "client_im_manager", data,
		filepath.Join(outputDir, "manager.go"),
	); err != nil {
		return err
	}

	zap.L().Info("Generated client identity manager", zap.String("dir", outputDir))
	return nil
}

// renderClientIMFile loads a chassis client_im template, executes it with data,
// gofmt-formats the result, and writes it to outputPath.
func renderClientIMFile(tmplName, tmplLabel string, data any, outputPath string) error {
	tmplStr, err := loadChassisTemplate(tmplName)
	if err != nil {
		return fmt.Errorf("failed to load %s template: %w", tmplName, err)
	}
	tmpl, err := template.New(tmplLabel).Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse %s template: %w", tmplName, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute %s template: %w", tmplName, err)
	}
	formatted, fmtErr := format.Source(buf.Bytes())
	if fmtErr != nil {
		zap.L().Warn("failed to format client_im file, writing unformatted", zap.String("path", outputPath), zap.Error(fmtErr))
		formatted = buf.Bytes()
	}
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", outputPath, err)
	}
	return nil
}

// generateStateWrappers generates state_client.go for each state service.
// Uses a message-first approach: walks proto files and finds service_state
// annotations on messages directly, generating a wrapper per state root.
// This correctly handles multi-state-root protos (e.g. a service proto with
// 6 state roots) without fallback matching or dedup collisions.
func generateStateWrappers(config *tactype.Config, inputDir string) ([]generatedStateClient, error) {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	generated := make(map[string]bool) // key: "pkg.stateRootName"
	var clients []generatedStateClient

	err = filepath.Walk(inputDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		relPath, _ := filepath.Rel(inputDir, path)
		fileDesc, err := dp.GetFileDescriptor(path)
		if err != nil {
			return nil
		}
		// Skip infrastructure protos except the manifest-declared runtime config
		// state proto, which has ServiceConfigState.
		if proto.IsInfraProtoFile(relPath, fileDesc) && !proto.IsRuntimeConfigStateProto(relPath) {
			return nil
		}
		if proto.IsSyntheticServiceOnlyFile(fileDesc) {
			return nil
		}

		packageName := strings.SplitN(fileDesc.Package, ".", 2)[0]
		parentDir := filepath.Dir(relPath)

		for i := range fileDesc.Messages {
			ann, ok := fileDesc.Messages[i].Annotations["service_state"]
			if !ok || ann.StringValue == "" {
				continue
			}

			stateRootAnnotation := ann.StringValue
			msgAccessMode, shouldGenerate := config.AccessModeForState(relPath, stateRootAnnotation)
			if !shouldGenerate {
				continue
			}
			key := fileDesc.Package + "." + stateRootAnnotation
			if generated[key] {
				continue
			}
			generated[key] = true

			svc := ServiceInfo{
				ProtoFile:   relPath,
				PackageName: packageName,
				ParentDir:   parentDir,
				ServiceName: proto.StateServiceName(stateRootAnnotation),
				Config:      config,
				ReadOnly:    msgAccessMode == tactype.AccessRO || (config.HealthStateName != "" && stateRootAnnotation == config.HealthStateName),
				WriteOnce:   msgAccessMode == tactype.AccessWORM,
			}

			zap.L().Info("Generating state client",
				zap.String("stateRoot", stateRootAnnotation),
				zap.String("proto", relPath))

			client, err := generateStateWrapper(config, svc, &fileDesc.Messages[i], stateRootAnnotation, fileDesc, dp)
			if err != nil {
				return fmt.Errorf("failed to generate state wrapper for %s: %w", stateRootAnnotation, err)
			}
			clients = append(clients, *client)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return clients, nil
}

// generateStateWrapper creates the state_client.go file for a single state service.
// The generated state wrapper is a single root wrapper that provides:
// - A Proto() method that reconstructs the full proto message tree
// - Child wrappers per boundary with Proto() methods
// - A bidirectional Watch stream for state subscriptions and commits
// - Get/Set for singleton boundaries
// - Add/Delete for collection boundaries
// - Read-only Get for cross-package state (reads_state)
func generateStateWrapper(config *tactype.Config, svc ServiceInfo, stateRoot *parser.MessageDescriptor, expectedState string, fileDesc *parser.FileDescriptor, dp *parser.DescriptorParser) (*generatedStateClient, error) {

	// Classify state root fields as singletons, collections, topics, queues, priority queues, views, commands, or state imports.
	var singletons []singletonBoundary
	var collections []collectionBoundary
	var topics []topicBoundary
	var queues []queueBoundary
	var priorityQueues []priorityQueueBoundary
	var commands []commandBoundary
	var views []viewBoundary
	var stateImports []stateImportField
	seenPkgs := make(map[string]bool)

	fieldsToClassify := stateRoot.Fields

	for _, field := range fieldsToClassify {
		if field.TypeKind != "message" {
			continue
		}

		// state_import: read-only reference to a boundary owned by another service.
		if importAnn, ok := field.Annotations["state_import"]; ok && importAnn.StringValue != "" {
			typeName := extractSimpleTypeName(field.Type)
			fieldPascal := strutil.ToPascalCase(field.Name)
			isCross := strings.Contains(field.Type, ".")
			var goType string
			if isCross {
				pkg := strings.SplitN(field.Type, ".", 2)[0]
				if pkg != svc.PackageName {
					goType = pkg + "_pb." + typeName
					seenPkgs[pkg] = true
				} else {
					goType = typeName
					isCross = false
				}
			} else {
				goType = typeName
			}
			stateImports = append(stateImports, stateImportField{
				FieldName:      fieldPascal,
				TypeName:       typeName,
				GoType:         goType,
				IsCrossPackage: isCross,
			})
			continue
		}

		// Skip private boundaries
		if privAnn, ok := field.Annotations["private"]; ok && privAnn.BoolValue {
			continue
		}

		typeName := extractSimpleTypeName(field.Type)
		fieldPascal := strutil.ToPascalCase(field.Name)

		// Determine Go type: cross-package types use pkg_pb prefix, same-package types are bare.
		// field.Type may contain "." for qualified names (e.g. "key.KeyType") depending on
		// which parser path was used; TypePackage is not consistently set across both parsers.
		var goType string
		var crossPkg string
		isCrossPackage := false
		if strings.Contains(field.Type, ".") {
			crossPkg = strings.SplitN(field.Type, ".", 2)[0]
			if crossPkg == svc.PackageName {
				goType = typeName
				crossPkg = ""
			} else {
				goType = crossPkg + "_pb." + typeName
				isCrossPackage = true
			}
		} else {
			goType = typeName
		}
		// markCross is a helper to register this field's cross-package import
		// only after the field has been successfully classified as a boundary.
		markCross := func() {
			if crossPkg != "" {
				seenPkgs[crossPkg] = true
			}
		}

		// View: read-only union of multiple source collections (skip normal classification)
		if vsAnn, ok := field.Annotations["view_sources"]; ok && len(vsAnn.StringSliceValue) > 0 {
			// Find the item message to determine the indexed key field
			boundaryMsg := findMessageInFile(fileDesc, field.Type)
			if boundaryMsg == nil {
				// Try cross-package lookup
				if strings.Contains(field.Type, ".") {
					crossPkg := strings.SplitN(field.Type, ".", 2)[0]
					simpleType := extractSimpleTypeName(field.Type)
					boundaryMsg, _ = findMessageAcrossFiles(dp, crossPkg, simpleType)
				}
			}
			keyFieldName, keyGoType := "", ""
			if boundaryMsg != nil {
				keyFieldName, keyGoType = findIndexedKey(boundaryMsg)
			}
			// Convert source field names to PascalCase
			var sourcePascal []string
			for _, src := range vsAnn.StringSliceValue {
				sourcePascal = append(sourcePascal, strutil.ToPascalCase(src))
			}
			views = append(views, viewBoundary{
				FieldName:      fieldPascal,
				TypeName:       typeName,
				GoType:         goType,
				KeyFieldName:   keyFieldName,
				KeyGoType:      keyGoType,
				IsCrossPackage: isCrossPackage,
				ViewSources:    sourcePascal,
			})
			markCross()
			continue
		}

		// Command: explicit command RPC (skip normal boundary classification)
		if cmdAnn, ok := field.Annotations["command"]; ok && cmdAnn.BoolValue {
			commands = append(commands, commandBoundary{
				FieldName:      fieldPascal,
				TypeName:       typeName,
				GoType:         goType,
				IsCrossPackage: isCrossPackage,
			})
			markCross()
			continue
		}

		// Topic: pub/sub broadcast boundary
		if _, ok := field.Annotations["topic"]; ok {
			topics = append(topics, topicBoundary{
				FieldName:      fieldPascal,
				TypeName:       typeName,
				GoType:         goType,
				IsCrossPackage: isCrossPackage,
			})
			// Only import cross-package types for writable clients (Publish uses the type).
			// Read-only clients only have Subscribe which returns a stream type from the
			// state service package — no direct reference to the topic's message type.
			if !svc.ReadOnly {
				markCross()
			}
			continue
		}

		// Queue: FIFO queue boundary
		if _, ok := field.Annotations["queue"]; ok {
			queues = append(queues, queueBoundary{
				FieldName:      fieldPascal,
				TypeName:       typeName,
				GoType:         goType,
				IsCrossPackage: isCrossPackage,
			})
			markCross()
			continue
		}

		// Priority queue: priority-ordered queue boundary
		if pqAnn, ok := field.Annotations["priority_queue"]; ok {
			priorityQueues = append(priorityQueues, priorityQueueBoundary{
				FieldName:      fieldPascal,
				TypeName:       typeName,
				GoType:         goType,
				PriorityField:  pqAnn.StringValue,
				IsCrossPackage: isCrossPackage,
			})
			markCross()
			continue
		}

		// Boundary classification by type: message-typed fields are singleton or
		// collection boundaries (all convergent).
		if field.IsMap {
			// Proto map fields (map<K, V>) cannot be used directly as state boundaries.
			// ValidateStateConsistency already rejects this loudly; if we reach here
			// something bypassed validation — fail closed rather than silently skip.
			return nil, fmt.Errorf("proto map field %q on state root %s used directly as boundary — not supported; wrap in a named message type",
				field.Name, svc.ServiceName)
		}

		if field.IsRepeated {
			// Collection: repeated message field
			boundaryMsg := findMessageInFile(fileDesc, field.Type)
			if boundaryMsg == nil {
				// Try cross-package lookup (e.g. jwk.Jwk defined in sys/jwk.proto)
				if strings.Contains(field.Type, ".") {
					xpkg := strings.SplitN(field.Type, ".", 2)[0]
					simpleType := extractSimpleTypeName(field.Type)
					boundaryMsg, _ = findMessageAcrossFiles(dp, xpkg, simpleType)
				}
			}
			// Fail closed, as the map case above does. Warn-and-continue here
			// dropped a DECLARED collection boundary from the generated state
			// client: generation reported success, no accessor was emitted, and
			// the only trace was a warning on a log line nobody reads. The
			// service author sees a boundary they declared simply not exist.
			if boundaryMsg == nil {
				return nil, fmt.Errorf("collection boundary %q on state root %s references message type %q that could not be resolved in this file or across packages",
					field.Name, svc.ServiceName, field.Type)
			}
			keyFieldName, keyGoType := findIndexedKey(boundaryMsg)
			if keyFieldName == "" {
				return nil, fmt.Errorf("collection boundary %q on state root %s has type %q with no indexed key field — a collection boundary requires one indexed key",
					field.Name, svc.ServiceName, field.Type)
			}
			collections = append(collections, collectionBoundary{
				FieldName:       fieldPascal,
				TypeName:        typeName,
				GoType:          goType,
				KeyFieldName:    keyFieldName,
				KeyGoType:       keyGoType,
				IsCrossPackage:  isCrossPackage,
				EvictionMaxSize: field.Annotations["eviction_max_size"].IntValue,
			})
			markCross()
		} else {
			// Singleton: non-repeated message field
			singletons = append(singletons, singletonBoundary{
				FieldName:      fieldPascal,
				TypeName:       typeName,
				GoType:         goType,
				IsCrossPackage: isCrossPackage,
			})
			markCross()
		}
	}

	// Build cross-package imports (skip own package)
	var crossImports []crossImportEntry
	for pkg := range seenPkgs {
		if pkg == svc.PackageName {
			continue // same package — no import needed
		}
		dir := findPackageDir(dp, pkg)
		if dir == "" {
			// Fail closed. Skipping the import emits a file that references an
			// undeclared package, so the failure surfaced as a confusing compile
			// error in generated code rather than at the point the resolution
			// actually failed.
			return nil, fmt.Errorf("cross-package type in state root %s references package %q whose directory could not be resolved",
				svc.ServiceName, pkg)
		}
		var importPath string
		if filepath.Base(dir) == pkg {
			importPath = fmt.Sprintf("%s/types/%s", config.ModuleBasePath, dir)
		} else {
			importPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, dir, pkg)
		}
		if strings.HasPrefix(dir, "services") {
			importPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, dir, pkg)
		}
		crossImports = append(crossImports, crossImportEntry{
			Alias: pkg + "_pb",
			Path:  importPath,
		})
	}

	// Build import paths
	typesImportPath := fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, svc.ParentDir, svc.ImportDirName())
	specTypesPath := fmt.Sprintf("%s/types/sys/lock", config.ModuleBasePath)
	serviceManagerLibPath := fmt.Sprintf("%s/chassis/servicemanager", config.ModuleBasePath)
	serviceIMLibPath := fmt.Sprintf("%s/chassis/service_im", config.ModuleBasePath)
	traceTypesPath := fmt.Sprintf("%s/types/sys/trace", config.ModuleBasePath)

	// Derive unique directory name from the declared state root name.
	// This ensures each state service gets its own output directory even when
	// multiple states share the same proto package (e.g. IronMaidenInternal
	// and IronMaidenShared both have PackageName "ironmaiden").
	dirName := strutil.ToSnakeCase(expectedState)

	// Use the annotation value directly as the service prefix — no suffix stripping
	data := stateClientData{
		PackageName:           dirName,
		ClientTypeName:        "StateClient",
		StateServiceName:      proto.StateServiceName(stateRoot.Name),
		StateRootName:         stateRoot.Name,
		TypesImportPath:       typesImportPath,
		ServiceManagerLibPath: serviceManagerLibPath,
		ServiceIMLibPath:      serviceIMLibPath,
		SpecTypesPath:         specTypesPath,
		Singletons:            singletons,
		Collections:           collections,
		Topics:                topics,
		Queues:                queues,
		PriorityQueues:        priorityQueues,
		Commands:              commands,
		Views:                 views,
		StateImports:          stateImports,
		CrossImports:          crossImports,
		TraceTypesPath:        traceTypesPath,
		ReadOnly:              svc.ReadOnly,
		WriteOnce:             svc.WriteOnce,
	}

	// Execute template
	funcMap := template.FuncMap{
		// TypeRef prefixes same-package types with "types." but leaves cross-package types bare
		// (cross-package types already have their own import alias like "ballsports_pb.")
		"TypeRef": func(goType string, isCross bool) string {
			if isCross {
				return goType
			}
			return "types." + goType
		},
		// ValidateRef returns the validate function call for a type.
		// Same-package: types.ValidateTeamRoster, cross-package: ballsports_pb.ValidateTeamRoster
		"ValidateRef": func(goType string, isCross bool) string {
			if isCross {
				// goType is like "ballsports_pb.TeamRoster" — insert "Validate" after the dot
				if idx := strings.LastIndex(goType, "."); idx >= 0 {
					return goType[:idx+1] + "Validate" + goType[idx+1:]
				}
				return "Validate" + goType
			}
			return "types.Validate" + goType
		},
		// DefaultsRef returns the proto-defaults function call for a type.
		// Same-package: types.TeamRosterDefaults, cross-package: ballsports_pb.TeamRosterDefaults.
		// writeDefaults emits one per message unconditionally, so this is always callable.
		"DefaultsRef": func(goType string, isCross bool) string {
			if isCross {
				return goType + "Defaults"
			}
			return "types." + goType + "Defaults"
		},
		// LowerFirst lowercases the first letter of a string (PascalCase -> camelCase)
		"LowerFirst": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToLower(s[:1]) + s[1:]
		},
	}
	stateWrapperTmplStr, err := loadTemplate("state_wrapper.go.tmpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load state wrapper template: %w", err)
	}
	tmpl, err := template.New("stateclient").Funcs(funcMap).Parse(stateWrapperTmplStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse state wrapper template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute state wrapper template: %w", err)
	}

	// Format the generated code
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		zap.L().Warn("failed to format state.go, writing unformatted", zap.Error(err))
		formatted = buf.Bytes()
	}

	// Write to sm/internal/state/{parentDir}/{dirName}/
	stateDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "state", svc.ParentDir, dirName)
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state wrapper directory: %w", err)
	}

	clientFileName := "state.go"
	outputPath := filepath.Join(stateDir, clientFileName)
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return nil, fmt.Errorf("failed to write %s: %w", clientFileName, err)
	}

	zap.L().Debug("Generated state client", zap.String("path", outputPath))

	// Build import path for the root StateWrappers
	wrapperImportPath := config.ModuleBasePath + "/" + config.GetSmPrefix() + "/state/" + svc.ParentDir + "/" + dirName

	// Collect boundary info for registry generation
	var boundaries []boundaryInfo
	for _, s := range singletons {
		boundaries = append(boundaries, boundaryInfo{
			FieldName:    s.FieldName,
			IsCollection: false,
		})
	}
	for _, c := range collections {
		boundaries = append(boundaries, boundaryInfo{
			FieldName:    c.FieldName,
			IsCollection: true,
		})
	}
	for _, t := range topics {
		boundaries = append(boundaries, boundaryInfo{FieldName: t.FieldName})
	}
	for _, q := range queues {
		boundaries = append(boundaries, boundaryInfo{FieldName: q.FieldName})
	}
	for _, pq := range priorityQueues {
		boundaries = append(boundaries, boundaryInfo{FieldName: pq.FieldName})
	}

	return &generatedStateClient{
		PackageName:    svc.PackageName,
		DirName:        dirName,
		StateRootName:  expectedState,
		ClientTypeName: data.ClientTypeName,
		ImportPath:     wrapperImportPath,
		Group:          svc.ParentDir,
		Boundaries:     boundaries,
	}, nil
}

// stateClientGroupInfo represents a top-level proto directory group for the root StateClients.
type stateClientGroupInfo struct {
	Name        string                 // e.g. "services"
	ImportAlias string                 // e.g. "services_stateclients"
	ImportPath  string                 // e.g. "example.com/myproj/tacplus/sm/internal/state/services"
	Clients     []generatedStateClient // clients in this group
}

// generateGroupStateClients generates sm/internal/state/{group}/states.go
// that aggregates all state clients in that group.
func generateGroupStateClients(config *tactype.Config, group string, clients []generatedStateClient) error {
	funcMap := template.FuncMap{
		"Title": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"LowerFirst": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToLower(s[:1]) + s[1:]
		},
	}

	groupStateTmplStr, err := loadTemplate("group_state_clients.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load group state clients template: %w", err)
	}
	tmpl, err := template.New("groupstate").Funcs(funcMap).Parse(groupStateTmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse group state clients template: %w", err)
	}

	data := struct {
		Group          string
		ModuleBasePath string
		Clients        []generatedStateClient
	}{
		Group:          group,
		ModuleBasePath: config.ModuleBasePath,
		Clients:        clients,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute group state clients template: %w", err)
	}

	formatted, fmtErr := format.Source(buf.Bytes())
	if fmtErr != nil {
		zap.L().Warn("failed to format states.go, writing unformatted", zap.Error(fmtErr))
		formatted = buf.Bytes()
	}

	groupDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "state", group)
	if err := os.MkdirAll(groupDir, 0755); err != nil {
		return fmt.Errorf("failed to create group directory: %w", err)
	}

	outputPath := filepath.Join(groupDir, "states.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write states.go: %w", err)
	}

	zap.L().Info("Generated group StateClients", zap.String("group", group), zap.String("path", outputPath))
	return nil
}

// generateRootStateWrappers generates sm/internal/state/state.go that only exposes
// group accessors (e.g. Services()).
func generateRootStateWrappers(config *tactype.Config, clients []generatedStateClient) error {
	// Group clients by top-level proto directory
	groups := make(map[string][]generatedStateClient)
	for _, c := range clients {
		groups[c.Group] = append(groups[c.Group], c)
	}

	// Generate per-group aggregator files
	for group, groupClients := range groups {
		if err := generateGroupStateClients(config, group, groupClients); err != nil {
			return fmt.Errorf("failed to generate %s state clients aggregator: %w", group, err)
		}
	}

	// Build sorted group list
	var groupList []stateClientGroupInfo
	for group, groupClients := range groups {
		groupList = append(groupList, stateClientGroupInfo{
			Name:        group,
			ImportAlias: group + "_stateclients",
			ImportPath:  config.ModuleBasePath + "/" + config.GetSmPrefix() + "/state/" + group,
			Clients:     groupClients,
		})
	}
	// Actually sort it (the comment above always promised this, but the range over
	// the `groups` map is nondeterministic): Groups renders struct fields, init
	// statements, Close() calls and getter methods that gofmt does NOT reorder, so
	// without this the root state-clients file byte-diffs across identical runs.
	// The Java twin (statewrapper_gen.go) sorts its groupList the same way.
	sort.Slice(groupList, func(i, j int) bool { return groupList[i].Name < groupList[j].Name })

	funcMap := template.FuncMap{
		"Title": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"LowerFirst": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToLower(s[:1]) + s[1:]
		},
	}

	rootStateTmplStr, err := loadTemplate("root_state_wrappers.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load root state wrappers template: %w", err)
	}
	tmpl, err := template.New("rootstate").Funcs(funcMap).Parse(rootStateTmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse root state wrappers template: %w", err)
	}

	data := struct {
		ModuleBasePath string
		Groups         []stateClientGroupInfo
	}{
		ModuleBasePath: config.ModuleBasePath,
		Groups:         groupList,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute root state wrappers template: %w", err)
	}

	formatted, fmtErr := format.Source(buf.Bytes())
	if fmtErr != nil {
		zap.L().Warn("failed to format states.go, writing unformatted", zap.Error(fmtErr))
		formatted = buf.Bytes()
	}

	stateClientsDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "state")
	if err := os.MkdirAll(stateClientsDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	outputPath := filepath.Join(stateClientsDir, "state.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write states.go: %w", err)
	}

	zap.L().Info("Generated root StateClients", zap.String("path", outputPath), zap.Int("groups", len(groupList)))

	return nil
}

// registryServiceInfo is the template data for a state root in registry.go.
type registryServiceInfo struct {
	ServiceEnum         string
	ServiceGoName       string // PascalCase method name, e.g. "Identity"
	ServiceProtoPackage string // proto package name, e.g. "identity"
	ServicePath         string // slash-separated path, e.g. "services/identity"
	StateRoot           string
	Boundaries          []boundaryInfo
}

// registryTreeNode represents a node in the directory tree used to build the
// hierarchical registry. Leaf nodes correspond to proto packages with generated
// code; intermediate nodes are pure directory groupings.
type registryTreeNode struct {
	name       string // directory basename
	fullPath   string // slash-separated from types root
	goPackage  string // Go package name (leaf) or dir name (intermediate)
	isLeaf     bool   // has proto-generated registry.go
	children   map[string]*registryTreeNode
	childOrder []string
}

// generateRegistry generates per-leaf registry.go files with enum helpers and
// a single registry.json containing the full PackageNode tree.
func generateRegistry(config *tactype.Config, _servicesInfo []ServiceInfo, inputDir, _typesOutputDir string, allParsedFiles []model.ProtoFile) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Collect all leaf subdirs.
	leafDirs := map[string]string{} // subdir → goPackage
	for i := range allParsedFiles {
		pf := allParsedFiles[i]
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		parentDir := filepath.Dir(pf.RelPath)
		var subdir string
		if serviceName, _ := proto.ServiceProtoParts(pf.RelPath); serviceName != "" {
			subdir = filepath.Join(parentDir, serviceName)
		} else if filepath.Base(parentDir) == pf.Package {
			subdir = parentDir
		} else {
			subdir = filepath.Join(parentDir, pf.Package)
		}
		if _, ok := leafDirs[subdir]; !ok {
			goPackage := pf.Package
			if goPackage == "" {
				goPackage = filepath.Base(subdir)
			}
			goPackage = strings.ReplaceAll(strings.ReplaceAll(goPackage, "-", "_"), ".", "_")
			leafDirs[subdir] = goPackage
		}
	}

	// Build tree from leaf dirs. Each intermediate directory becomes a node.
	root := &registryTreeNode{name: "types", fullPath: "", children: map[string]*registryTreeNode{}}

	for subdir, goPackage := range leafDirs {
		parts := strings.Split(filepath.ToSlash(subdir), "/")
		cur := root
		for i, part := range parts {
			if _, ok := cur.children[part]; !ok {
				partPath := strings.Join(parts[:i+1], "/")
				cur.children[part] = &registryTreeNode{
					name:      part,
					fullPath:  partPath,
					goPackage: part,
					children:  map[string]*registryTreeNode{},
				}
				cur.childOrder = append(cur.childOrder, part)
			}
			cur = cur.children[part]
		}
		cur.isLeaf = true
		cur.goPackage = goPackage
	}

	// Sort childOrder at every level for deterministic output.
	var sortChildren func(node *registryTreeNode)
	sortChildren = func(node *registryTreeNode) {
		sort.Strings(node.childOrder)
		for _, child := range node.children {
			sortChildren(child)
		}
	}
	sortChildren(root)

	// Generate registry.json, registry.schema.json, AGENT.md via shared package.
	return registry.Generate(registry.Config{
		OutputDir:   config.OutputDir,
		VersionTag:  config.VersionTag,
		InputDir:    inputDir,
		Language:    "go",
		PackageBase: config.ModuleBasePath,
	}, allParsedFiles, dp)
}

// findIndexedKey finds the indexed key field in a message and returns its Go name and type.
func findIndexedKey(msg *parser.MessageDescriptor) (string, string) {
	for _, f := range msg.Fields {
		if ann, ok := f.Annotations["indexed"]; ok && ann.BoolValue {
			return strutil.ToPascalCase(f.Name), protoScalarGoType(f.Type)
		}
	}
	return "", ""
}

// protoScalarGoType maps a proto scalar type to its Go equivalent.
func protoScalarGoType(protoType string) string {
	switch protoType {
	case "string":
		return "string"
	case "int32", "sint32", "sfixed32":
		return "int32"
	case "int64", "sint64", "sfixed64":
		return "int64"
	case "uint32", "fixed32":
		return "uint32"
	case "uint64", "fixed64":
		return "uint64"
	case "bool":
		return "bool"
	case "float":
		return "float32"
	case "double":
		return "float64"
	case "bytes":
		return "[]byte"
	}
	return protoType
}

// findMessageInFile searches for a message by type name within a single file descriptor.
func findMessageInFile(fileDesc *parser.FileDescriptor, typeName string) *parser.MessageDescriptor {
	simple := extractSimpleTypeName(typeName)
	for i := range fileDesc.Messages {
		if fileDesc.Messages[i].Name == simple {
			return &fileDesc.Messages[i]
		}
	}
	return nil
}

// findMessageAcrossFiles searches for a message by package and type name across all parsed files.
func findMessageAcrossFiles(dp *parser.DescriptorParser, pkg, typeName string) (*parser.MessageDescriptor, string) {
	for _, fileDesc := range dp.GetAllFiles() {
		if fileDesc.Package == pkg {
			for i := range fileDesc.Messages {
				if fileDesc.Messages[i].Name == typeName {
					return &fileDesc.Messages[i], pkg
				}
			}
		}
	}
	return nil, ""
}

// findPackageDir finds the directory containing a proto package by searching parsed descriptors.
func findPackageDir(dp *parser.DescriptorParser, pkg string) string {
	for descPath, fileDesc := range dp.GetAllFiles() {
		if fileDesc.Package == pkg {
			return filepath.Dir(descPath)
		}
	}
	return ""
}

// extractSimpleTypeName extracts the simple type name from a potentially fully-qualified name.
// e.g. "foobar.UserProfile" -> "UserProfile", "UserProfile" -> "UserProfile"
func extractSimpleTypeName(fullType string) string {
	parts := strings.Split(fullType, ".")
	return parts[len(parts)-1]
}
