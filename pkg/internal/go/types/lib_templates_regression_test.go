// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

func TestWriteLibTemplatesAlwaysWritesGrpcServer(t *testing.T) {
	out := t.TempDir()
	cfg := &tactype.Config{
		OutputDir:      out,
		ModuleBasePath: "example.com/consumer/tacplus",
	}

	if err := writeLibTemplates(cfg, []string{cfg.ServiceLabel()}, nil); err != nil {
		t.Fatalf("writeLibTemplates: %v", err)
	}

	path := filepath.Join(out, "chassis", "grpcserver", "grpcserver.go")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected grpcserver lib template to be written: %v", err)
	}

	tagsPath := filepath.Join(out, "chassis", "tags", "tags.go")
	if _, err := os.Stat(tagsPath); err != nil {
		t.Fatalf("expected tags lib template to be written: %v", err)
	}
}

func TestGoTagsRuntimeTemplateMatchesKubernetesSemantics(t *testing.T) {
	src, err := templates.FS.ReadFile("go/chassis/tags/tags.go.tmpl")
	if err != nil {
		t.Fatalf("read tags template: %v", err)
	}
	rendered := string(src)
	for _, want := range []string{
		"func ContainsAll(have, want *tagspb.TagSet) bool",
		"func Matches(selector *tagspb.TagSelector, tags *tagspb.TagSet) bool",
		"TagSelectorOperator_TAG_SELECTOR_OPERATOR_IN",
		"TagSelectorOperator_TAG_SELECTOR_OPERATOR_NOT_IN",
		"return !exists || !contains(req.GetValues(), value)",
		"TagSelectorOperator_TAG_SELECTOR_OPERATOR_EXISTS",
		"TagSelectorOperator_TAG_SELECTOR_OPERATOR_DOES_NOT_EXIST",
		"func ValidateKey(key string) error",
		"func ValidateSelector(selector *tagspb.TagSelector) error",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go tags runtime template missing %q", want)
		}
	}
}

func TestGoHealthReporterWrapsCheckerAndClassifiesFirstPublish(t *testing.T) {
	src, err := templates.FS.ReadFile("go/chassis/healthreporter/healthreporter.go.tmpl")
	if err != nil {
		t.Fatalf("read healthreporter template: %v", err)
	}
	rendered := string(src)
	for _, want := range []string{
		"panic in CheckHealth",
		"StatusUnhealthy",
		"isPermanentHealthPublishError",
		"codes.PermissionDenied",
		"first health publish failed after",
		"maxFirstPublishAttempts",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go health reporter must wrap/check/classify health publishing; missing %q", want)
		}
	}
}

// The HealthReporter must expose a StatusSink callback that bridges effective
// health status to the gRPC health endpoint. UNHEALTHY flips to NOT_SERVING,
// recovery flips back to SERVING. The sink is not called during DRAINING.
func TestGoHealthReporterStatusSinkBridgesHealthToGrpc(t *testing.T) {
	src, err := templates.FS.ReadFile("go/chassis/healthreporter/healthreporter.go.tmpl")
	if err != nil {
		t.Fatalf("read healthreporter template: %v", err)
	}
	rendered := string(src)

	for _, want := range []string{
		"statusSink",
		"func (hr *HealthReporter) SetStatusSink(sink func(InstanceStatus))",
		"statusSink(status)",
		"StatusDraining",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go HealthReporter must support StatusSink; missing %q", want)
		}
	}

	// The service_manager must wire the StatusSink to the gRPC health endpoint.
	smSrc, err := templates.FS.ReadFile("go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("read service_manager template: %v", err)
	}
	sm := string(smSrc)
	for _, want := range []string{
		"hr.SetStatusSink(",
		"StatusUnhealthy",
		"SetReady(",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("Go service_manager must wire StatusSink to gRPC health; missing %q", want)
		}
	}

	// The sink wiring must come AFTER the bootstrap SetReady(true).
	idxBootstrap := strings.Index(sm, "healthCtl.SetReady(true)")
	idxSink := strings.Index(sm, "hr.SetStatusSink(")
	if idxBootstrap < 0 || idxSink < 0 || idxSink < idxBootstrap {
		t.Fatalf("StatusSink must be wired after bootstrap SetReady(true) (bootstrap=%d sink=%d)", idxBootstrap, idxSink)
	}
}

func TestGoAuthorizerUsesSharedBearerVerifier(t *testing.T) {
	src, err := templates.FS.ReadFile("go/chassis/grpcserver/authorizer.go.tmpl")
	if err != nil {
		t.Fatalf("read authorizer template: %v", err)
	}
	rendered := string(src)
	for _, want := range []string{
		"func rejectAPITokenClass(tw *tokenlib.TokenWrapper) error",
		"func (a *authorizer) verifyBearer(ctx context.Context) (*tokenlib.TokenWrapper, error)",
		"if err := rejectAPITokenClass(tw); err != nil",
		"tw, err := a.verifyBearer(ctx)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go authorizer must share verified bearer/token-class rejection path; missing %q", want)
		}
	}
	// All three entry points (Authorize, Authenticate, AuthenticateAtOrBelow)
	// must go through that one verifier and keep the token it returns.
	if got := strings.Count(rendered, "tw, err := a.verifyBearer(ctx)"); got != 3 {
		t.Fatalf("Go authorizer must verify the bearer through one shared helper in all three entry points, got %d", got)
	}
	if got := strings.Count(rendered, "refresh tokens cannot be used for API access"); got != 1 {
		t.Fatalf("Go authorizer must define refresh-token rejection in one shared helper, got %d copies", got)
	}
	if strings.Contains(rendered, "authTokenUse :=") {
		t.Fatalf("Go auth_any_user path must not keep a divergent token-class rejection branch")
	}
}

func TestGoAuthorizerNormalizesGeneratedGrantTokens(t *testing.T) {
	src, err := templates.FS.ReadFile("go/chassis/grpcserver/authorizer.go.tmpl")
	if err != nil {
		t.Fatalf("read authorizer template: %v", err)
	}
	rendered := string(src)
	for _, want := range []string{
		`granthelper "{{.ModuleBasePath}}/grants"`,
		"granthelper.UnknownTokens(raw)",
		`a.logger.Warn("dropping unknown service_entitlements grant(s); token grants remain fail-closed"`,
		`zap.Strings("unknown_grants", unknown)`,
		"granthelper.Normalize(raw)",
		"raw = append(raw, grant)",
		"func methodGrantToken(fullMethod string) string",
		"strings.LastIndex(serviceFQN, \"/\")",
		"func grantTokenPart(s string) string",
		`strings.NewReplacer("-", "_", ".", "_", "/", "_")`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go authorizer must authorize via generated grant normalization; missing %q", want)
		}
	}
	if strings.Contains(rendered, "grant.GetToken()") {
		t.Fatal("Go authorizer must consume service_entitlements.grants as flat strings, not token objects")
	}
}

func TestGoServiceManagerBoundsSubscriptionJoin(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	for _, want := range []string{
		"func (s *serviceTaskScope) WaitContext(ctx context.Context) bool",
		"sm.tasks.WaitContext(waitCtx)",
		"subDone := make(chan struct{})",
		"subWg.Wait()",
		"case <-shutdownCtx.Done():",
		"Subscription shutdown timeout expired; continuing graceful shutdown",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Go ServiceManager must bound subscription join by shutdown timeout; missing %q", want)
		}
	}
}

func TestGoServiceManagerTreatsMissingTokenMinterAsPermanent(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	for _, want := range []string{
		"func isPermanentIdentityInitError(err error) bool",
		"errors.Is(err, service_im.ErrTokenMinterNotConfigured)",
		"if isPermanentIdentityInitError(idErr) {",
		`return nil, fmt.Errorf("identity permanent startup failure: %w", idErr)`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Go ServiceManager must fail missing-token-minter startup without retrying; missing %q", want)
		}
	}
}

func TestGoIdentityMintersUseScopedRefreshLocks(t *testing.T) {
	tmpl, err := loadChassisTemplate("service_im/service_im.go.tmpl")
	if err != nil {
		t.Fatalf("loadChassisTemplate service_im/service_im.go.tmpl: %v", err)
	}
	for _, want := range []string{
		"refreshLocks map[string]*sync.Mutex",
		"func (tm *k8sTokenMinter) refreshLockFor(audience string) *sync.Mutex",
		"func (tm *k8sTokenMinter) cachedToken(audience string) (string, bool)",
		"refreshMu      sync.Mutex",
		"func (cm *cloudTokenMinter) refreshLockFor(audience string) *sync.Mutex",
		"func (cm *cloudTokenMinter) freshGenericAccess() (string, bool)",
		"func (cm *cloudTokenMinter) validRefreshToken() (string, bool)",
		"func (cm *cloudTokenMinter) doGenericExchange(refreshToken string) error",
		"func (cm *cloudTokenMinter) doServiceExchange(audience, accessToken string) (string, error)",
		"cm.scheduleRenewal(nextRefreshIat, nextRefreshExp)",
		"cm.refreshMu.Lock()",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Go identity minters must scope refresh locking; missing %q", want)
		}
	}
	if strings.Contains(tmpl, "func (cm *cloudTokenMinter) getTokenFor(audience string) (string, error) {\n\tcm.mu.Lock()") ||
		strings.Contains(tmpl, "func (tm *k8sTokenMinter) getTokenFor(audience string) (string, error) {\n\ttm.mu.Lock()") {
		t.Fatalf("identity minters must not hold the state mutex across token-exchange RPCs")
	}
	if strings.Contains(tmpl, "Must be called with cm.mu held") || strings.Contains(tmpl, "Must be called with tm.mu held") {
		t.Fatalf("token exchange helpers must not require the state mutex while performing RPCs")
	}
	// The static cred_pair bootstrap minter has been removed; the generated IM
	// must no longer contain it.
	if strings.Contains(tmpl, "type tokenMinter struct") || strings.Contains(tmpl, "func newTokenMinter(") {
		t.Fatalf("Go IM must not contain the removed cred_pair tokenMinter")
	}
}

func TestGoIdentityManagerReResolvesTokenAuthority(t *testing.T) {
	tmpl, err := loadChassisTemplate("service_im/service_im.go.tmpl")
	if err != nil {
		t.Fatalf("loadChassisTemplate service_im/service_im.go.tmpl: %v", err)
	}
	for _, want := range []string{
		"authorityTargets   []string",
		"bareOpts           []grpc.DialOption",
		"func authorityTargets(cfg *grpcconfig_pb.Config) []string",
		"func (im *IdentityManager) setAuthorityConnLocked(start int) error",
		"func (im *IdentityManager) rotateAuthorityConn() error",
		"return im.setAuthorityConnLocked(im.activeAuthorityIdx + 1)",
		"if rotateErr := im.rotateAuthorityConn(); rotateErr != nil",
		"imInitialExchangeWithRetry(imCtx, im.GetToken)",
		"im.k8sMinter.setConn(conn)",
		"im.cloudMinter.setConn(conn)",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Go IdentityManager must re-resolve token authority connections; missing %q", want)
		}
	}
	if strings.Contains(tmpl, "Dial the first available endpoint with no interceptors.") {
		t.Fatalf("IdentityManager must not document or preserve the first-endpoint-only token authority dial")
	}
}

func TestGoServiceManagerTLSRenewalRetriesAndEscalates(t *testing.T) {
	tmpl, err := loadTemplate("servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicemanager_lib.go.tmpl: %v", err)
	}
	for _, want := range []string{
		"const tlsCertRenewalLeadTime = 4 * time.Hour",
		"const tlsCertRenewalRetryInitialBackoff = 2 * time.Second",
		"const tlsCertRenewalRetryMaxBackoff = 30 * time.Second",
		"const tlsCertRenewalEscalationWindow = 30 * time.Minute",
		"func (bc *ServiceManager) tlsCertExpiry() (time.Time, error)",
		"wrapperX509.NewImmutableX509Cert(chain.GetChain()[0])",
		"func tlsRenewalRetryWait(ctx context.Context, backoff *time.Duration) bool",
		"if err := bc.obtainTLSCert(); err != nil",
		`"TLS certificate renewal failed inside escalation window; shutting down ServiceManager"`,
		"bc.cancel()",
		`"Failed to renew TLS cert; retrying"`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Go ServiceManager TLS renewal must retry with backoff and escalate near expiry; missing %q", want)
		}
	}
	if strings.Contains(tmpl, "time.NewTicker(tlsCertRenewalInterval)") ||
		strings.Contains(tmpl, "const tlsCertRenewalInterval = 20 * time.Hour") ||
		strings.Contains(tmpl, `"Failed to renew TLS cert", zap.Error(err)`) {
		t.Fatalf("Go ServiceManager must not keep the fixed-interval warn-only TLS renewal loop")
	}
}

func TestGoServiceManagerSelectsActiveEndpointBeforeInitialX509Mint(t *testing.T) {
	tmpl, err := loadTemplate("servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicemanager_lib.go.tmpl: %v", err)
	}
	selectIdx := strings.Index(tmpl, "bc.selectActive()")
	mintIdx := strings.Index(tmpl, "bc.obtainTLSCert()")
	if selectIdx < 0 {
		t.Fatalf("Go ServiceManager must select an active endpoint before startup RPCs")
	}
	if mintIdx < 0 {
		t.Fatalf("Go ServiceManager must obtain an initial TLS certificate")
	}
	if selectIdx > mintIdx {
		t.Fatalf("Go ServiceManager must select an active endpoint before initial X509Mint")
	}
}

func TestGoServiceManagerInitialX509MintFailureIsFatalBeforeServing(t *testing.T) {
	tmpl, err := loadTemplate("servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicemanager_lib.go.tmpl: %v", err)
	}
	mintIdx := strings.Index(tmpl, "if err := bc.obtainTLSCert(); err != nil {")
	startIdx := strings.Index(tmpl, "// Start background goroutines")
	if mintIdx < 0 || startIdx < 0 || mintIdx > startIdx {
		t.Fatalf("Go ServiceManager initial X509Mint block must appear before background goroutines")
	}
	afterMint := tmpl[mintIdx:startIdx]
	cancelIdx := strings.Index(afterMint, "bcCancel()")
	returnIdx := strings.Index(afterMint, `return nil, fmt.Errorf("failed to obtain TLS cert via X509Mint: %w", err)`)
	if cancelIdx < 0 || returnIdx < 0 {
		t.Fatalf("Go ServiceManager initial X509Mint fail-closed path must cancel and return")
	}
	if cancelIdx > returnIdx {
		t.Fatalf("Go ServiceManager must cancel and return on initial X509Mint failure before starting background goroutines")
	}
}

func TestGoServiceManagerUsesDiscoveredX509MintMethod(t *testing.T) {
	tmpl, err := loadTemplate("servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicemanager_lib.go.tmpl: %v", err)
	}
	if !strings.Contains(tmpl, "mintClient.{{.X509MintMethod}}(mintCtx, mintReq)") {
		t.Fatalf("Go ServiceManager must call the discovered X509 mint method")
	}
	if strings.Contains(tmpl, "mintClient.X509Mint(") {
		t.Fatalf("Go ServiceManager must not hardcode the legacy X509Mint method")
	}
}

func TestGoServiceManagerBindsX509MintAudienceToServiceLabel(t *testing.T) {
	src, err := os.ReadFile("stateclient_gen.go")
	if err != nil {
		t.Fatalf("read stateclient generator: %v", err)
	}
	rendered := string(src)
	compactRendered := strings.NewReplacer(" ", "", "\t", "", "\n", "").Replace(rendered)
	if !strings.Contains(compactRendered, "ServiceAudience:config.ServiceLabel(),") {
		t.Fatalf("Go ServiceManager X509Mint audience must be the generated service label")
	}
	if strings.Contains(compactRendered, "ServiceAudience:stateSvcPkg,") {
		t.Fatalf("Go ServiceManager X509Mint audience must not be derived from the token-exchanger package")
	}
	if !strings.Contains(compactRendered, "ServiceAudience:texInfo.Package,") {
		t.Fatalf("Go service_im default token audience must still target the discovered token exchanger")
	}

	tmpl, err := loadTemplate("servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicemanager_lib.go.tmpl: %v", err)
	}
	for _, want := range []string{
		`const serviceAudience = "{{.ServiceAudience}}"`,
		`Id:     serviceAudience + "-tls"`,
		"wrapperSpiffe.ServiceSpiffeID(bc.config.GetSpiffeTrustDomain(), bc.config.GetLocationPath(), serviceAudience)",
		"Issuer:      serviceAudience,",
		"names := []string{serviceAudience, \"*.\" + serviceAudience}",
		"Aud:         serviceAudience,",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Go ServiceManager X509Mint path must use serviceAudience for cert identity; missing %q", want)
		}
	}
}

func TestGoSpiffeFixtureVectorsAreOptionalForGeneratedConsumers(t *testing.T) {
	src, err := templates.FS.ReadFile("go/chassis/spiffe/spiffe_test.go.tmpl")
	if err != nil {
		t.Fatalf("read spiffe test template: %v", err)
	}
	if !strings.Contains(string(src), `t.Skipf("shared SPIFFE vector fixture not found; set SPIFFE_VECTOR_FIXTURE")`) {
		t.Fatalf("Go SPIFFE vector fixture test must skip when the optional shared fixture is absent")
	}
}

func TestGoCertStreamsFollowSharedContractMarkers(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	for _, want := range []string{
		"tlsCertStreamLoop(taskCtx, func() grpc.ClientConnInterface { return sm.sc.Conn() },",
		`smbAuthorityConn := serviceconnection.NewDynamic("{{.SmbAuthorityServiceID}}", sc.ServiceDialOpts("{{.SmbAuthorityServiceID}}"))`,
		`smbAuthorityConn.ReconcileFromHealth(byService["{{.SmbAuthorityServiceID}}"])`,
		"smbCertStreamLoop(taskCtx, func() grpc.ClientConnInterface { return sm.smbAuthorityConn.Conn() },",
		"func tlsCertStreamLoop(ctx context.Context, connProvider func() grpc.ClientConnInterface",
		"func smbCertStreamLoop(ctx context.Context, connProvider func() grpc.ClientConnInterface",
		"TLS cert stream failed before first delivery after %d attempts",
		"SMB cert stream failed before first delivery after %d attempts",
		"defer func() { _ = stream.CloseSend() }()",
		"activePrivateKeyDER := privateKeyDER",
		"activePublicKeyDER := publicKeyDER",
		"var pendingPrivateKeyDER []byte",
		"var pendingPublicKeyDER []byte",
		"pendingPrivateKeyDER = privateKeyDER",
		"pendingPublicKeyDER = publicKeyDER",
		"certLeafMatchesPublicKey",
		"activePrivateKeyDER = certKeyDER",
		"activePublicKeyDER = certPublicKeyDER",
		`return fmt.Errorf("missing X509Mint TLS certificate")`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Go cert-stream template must follow shared contract; missing %q", want)
		}
	}
	if strings.Contains(tmpl, "tlsCertStreamLoop(taskCtx, sm.sc.Conn(),") ||
		strings.Contains(tmpl, "smbCertStreamLoop(taskCtx, sm.sc.Conn(),") {
		t.Fatalf("cert streams must resolve the active connection per attempt, not capture sm.sc.Conn() once")
	}
	startIdx := strings.Index(tmpl, "tlsCertStreamLoop(taskCtx, func() grpc.ClientConnInterface { return sm.sc.Conn() },")
	listenIdx := strings.Index(tmpl, "errCh := make(chan error, 1)")
	if startIdx < 0 || listenIdx < 0 {
		t.Fatalf("Go service manager template must start TLS stream and then prepare listener")
	}
	// PC-L-1 pin: the first-delivery outcome of the renewal stream must be drained
	// in a BACKGROUND task (log-only), never silently dropped. The drain is the
	// one permitted receive on tlsReadyCh before Listen.
	backgroundDrain := "sm.tasks.Go(func(taskCtx context.Context) {\nselect {\ncase err := <-tlsReadyCh:"
	if !strings.Contains(tmpl[startIdx:listenIdx], backgroundDrain) {
		t.Fatalf("Go service manager must drain tlsReadyCh in a background task so a terminal renewal failure is logged, not dropped (PC-L-1)")
	}
	// With the background drain excised, NO other receive on tlsReadyCh may appear
	// before listener prep: a receive in Run's synchronous path would block initial
	// Listen on ManageCert even though X509Mint already supplied the serving cert.
	// (Broader than the old exact-literal check: any `<-tlsReadyCh` spelling fails.)
	region := strings.ReplaceAll(tmpl[startIdx:listenIdx], backgroundDrain, "")
	if strings.Contains(region, "<-tlsReadyCh") {
		t.Fatalf("Go service manager must not block initial Listen on ManageCert when X509Mint already supplied the serving cert")
	}
}

// SEC-1/NEW-29/H1 pin: the client dial VerifyConnection must (1) validate the
// peer chain against the trusted CA pool AND (2) pin the peer's canonical SPIFFE
// identity, with a typed fail-fast on an empty trust domain. Chain-only
// verification is CA-only trust (any CA-issued cert accepted as any peer) — the
// unwired-verifier hole 4c921bfb closed. Two pin levels: the state-store/
// bootstrap connection pins the trust domain (authority label unknown here),
// while S2S mesh channels pin the exact target service (parity with rust's
// expect_service and java's ServiceIdentityTrustManager). This test makes
// reverting either to CA-only fail the suite.
func TestGoMeshDialVerifyConnectionPinsSpiffeIdentity(t *testing.T) {
	tmpl, err := loadTemplate("servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicemanager_lib.go.tmpl: %v", err)
	}
	for _, want := range []string{
		// typed fail-fast at construction (NEW-21)
		"var ErrMissingTrustDomain = errors.New(",
		"trustDomain := cfg.GetSpiffeTrustDomain()",
		"return nil, ErrMissingTrustDomain",
		// chain validation stays (in the shared spiffeTLSDialOpt helper)
		"if _, err := cs.PeerCertificates[0].Verify(opts); err != nil {",
		// bootstrap/state-store connection: trust-domain pin
		"return wrapperX509.VerifyPeerInTrustDomain(cert, bc.trustDomain)",
		// S2S mesh channels: exact-target-service pin (parity with rust/java)
		"func (bc *ServiceManager) ServiceDialOpts(serviceID string) []grpc.DialOption {",
		"return wrapperX509.VerifyPeerServiceIdentity(cert, serviceID, bc.trustDomain)",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("dial TLS must fail-fast on empty trust domain and pin peer SPIFFE identity after chain validation; missing %q", want)
		}
	}
	// The old chain-only ending (return the chain-verify error with no identity
	// pin after it) must not reappear.
	if strings.Contains(tmpl, "_, err := cs.PeerCertificates[0].Verify(opts)\nreturn err") {
		t.Fatalf("dial VerifyConnection must not end at chain validation (CA-only trust)")
	}
}

// H1 parity: S2S mesh ServiceConnections must dial with per-target
// ServiceDialOpts (exact-service pin), not the trust-domain-pinned DialOpts, so
// Go matches rust/java mesh pinning.
func TestGoMeshS2SConnectionsPinExactService(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	if !strings.Contains(tmpl, `serviceconnection.NewDynamic("{{.}}", sc.ServiceDialOpts("{{.}}"))`) {
		t.Fatalf("S2S mesh connections must pin the exact target via ServiceDialOpts, not the trust-domain DialOpts")
	}
	if strings.Contains(tmpl, `s2sConnections["{{.}}"] = serviceconnection.NewDynamic("{{.}}", sc.DialOpts())`) {
		t.Fatalf("S2S mesh connections must not use the trust-domain-pinned DialOpts (weaker than rust/java)")
	}
}

// The ServiceManager must expose audience-scoped token acquisition and service
// resolution so handlers can build custom subscribers without duplicating the
// identity/connection plane.
func TestGoServiceManagerExposesSubscriberPlumbing(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	for _, want := range []string{
		"func (sm *ServiceManager) TokenForService(audience string) (string, error)",
		"func (sm *ServiceManager) ConnToService(name string) (grpc.ClientConnInterface, error)",
		"func (sm *ServiceManager) ConnToServiceCtx(ctx context.Context, name string) (grpc.ClientConnInterface, error)",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("Go ServiceManager must expose subscriber plumbing; missing %q", want)
		}
	}
}

// PC-L-1 pin: a renewed serving certificate that cannot be rebuilt was LOG-ONLY.
// The server keeps serving the previous certificate — correct — but nothing
// outside the log said the renewal was not taking effect, so the instance stayed
// HEALTHY in the fleet plane right up to expiry. It now reports the same
// degradation seam the ServiceManager uses for its own renewal failures, and
// every server the SM starts installs the reporter.
func TestGoServingCertRebuildFailureReportsDegraded(t *testing.T) {
	grpcsrv, err := templates.FS.ReadFile("go/chassis/grpcserver/grpcserver.go.tmpl")
	if err != nil {
		t.Fatalf("read grpcserver template: %v", err)
	}
	for _, want := range []string{
		"func (ps *GrpcServer) SetCertDegradeReporter(fn func(reason string)) {",
		`ps.reportCertDegraded("renewed TLS serving certificate could not be built: " + err.Error())`,
	} {
		if !strings.Contains(string(grpcsrv), want) {
			t.Fatalf("a failed serving-cert rebuild must reach the degradation seam, not just the log "+
				"(PC-L-1); missing %q", want)
		}
	}

	sm, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	providers := strings.Count(sm, ".SetCertProvider(sm.sc.TLSCertMaterial)")
	reporters := strings.Count(sm, ".SetCertDegradeReporter(sm.reportTLSDegraded)")
	if providers == 0 || reporters != providers {
		t.Fatalf("every server given live cert providers must also report rebuild failures: "+
			"%d SetCertProvider call(s) but %d SetCertDegradeReporter call(s) (PC-L-1)", providers, reporters)
	}
}

// PC-L-10 pin: the go serving certificate must HOT-RELOAD on ManageCert
// renewal — java (X509ExtendedKeyManager) and rust (reloading resolver) both
// rotate; a static tls.Config serves an expiring cert until restart. Three
// legs: the server resolves the cert per handshake (GetCertificate), the SM
// publishes accepted renewals (SetTLSCert via the stream-loop store hook), and
// the servers are wired to live providers (SetCertProvider).
func TestGoServingCertHotReloads(t *testing.T) {
	grpcsrv, err := templates.FS.ReadFile("go/chassis/grpcserver/grpcserver.go.tmpl")
	if err != nil {
		t.Fatalf("read grpcserver template: %v", err)
	}
	for _, want := range []string{
		"GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {",
		"func (ps *GrpcServer) SetCertProvider(",
		"func buildTLSCertificate(",
	} {
		if !strings.Contains(string(grpcsrv), want) {
			t.Fatalf("grpcserver must resolve the serving cert per handshake (PC-L-10); missing %q", want)
		}
	}
	if strings.Contains(string(grpcsrv), "Certificates: []tls.Certificate{tlsCertObj}") {
		t.Fatalf("grpcserver must not pin a static Certificates list (PC-L-10)")
	}

	sm, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	for _, want := range []string{
		"storeTlsCert := func(chain *x509_pb.X509CertChain, keyDER []byte) {",
		"sm.sc.SetTLSCert(chain, key)",
		"srv.SetCertProvider(sm.sc.TLSCertMaterial)",
		"store func(*x509_pb.X509CertChain, []byte)",
		"store(m.Cert.Chain, certKeyDER)",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("ServiceManager must publish renewals to live cert providers (PC-L-10); missing %q", want)
		}
	}

	smlib, err := loadTemplate("servicemanager_lib.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicemanager_lib.go.tmpl: %v", err)
	}
	if !strings.Contains(smlib, "func (bc *ServiceManager) SetTLSCert(") {
		t.Fatalf("servicemanager lib must expose SetTLSCert for renewal publication (PC-L-10)")
	}
}
