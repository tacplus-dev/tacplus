// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
	"text/template"

	tacproto "gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

func readJavaTemplate(t *testing.T, path string) string {
	t.Helper()
	src, err := templates.FS.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(src)
}

// F-7: the ServiceManager shutdown hook must block until the graceful
// shutdown sequence on the run() thread completes (bounded by the configured
// shutdown timeout), instead of returning immediately and racing JVM halt.
func TestJavaServiceManagerShutdownHookBlocksUntilDrainCompletes(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	for _, want := range []string{
		"Thread runThread = Thread.currentThread();",
		"config.getShutdownTimeoutSeconds()",
		"runThread.join((shutdownTimeoutSeconds + 10) * 1000L);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("ServiceManager shutdown hook must join the run thread with a bounded timeout; missing %q", want)
		}
	}
	hook := strings.Index(rendered, `"sm-shutdown-hook"`)
	join := strings.Index(rendered, "runThread.join(")
	if hook < 0 || join < 0 || join > hook {
		t.Fatalf("runThread.join must happen inside the sm-shutdown-hook body")
	}
}

// F-4a: the event-bus subscription target must be re-resolved on every
// (re)connect attempt — a supplier is passed to the subscriber instead of a
// channel resolved once at startup.
func TestJavaServiceManagerSubscriptionsReResolveTarget(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	if strings.Contains(rendered, `io.grpc.Channel subConn = resolveService("{{.TargetService}}");`) {
		t.Fatalf("subscription target must not be resolved once at startup")
	}
	if !strings.Contains(rendered, `() -> resolveService("{{.TargetService}}"),`) {
		t.Fatalf("subscription must pass a channel supplier that re-resolves the target per attempt")
	}
	if !strings.Contains(rendered, `return channelFactory.create(serviceName, target);`) {
		t.Fatalf("resolveService must use the ServiceConnection.ChannelFactory contract")
	}
}

func TestJavaAuthorizerUsesSharedBearerVerifier(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/grpcserver/Authorizer.java.tmpl")
	for _, want := range []string{
		"private TokenWrapper verifyBearer(String authHeader) throws ProtoException",
		"private static void rejectApiTokenClass(TokenWrapper tw) throws ProtoException",
		"rejectApiTokenClass(tw);",
		"TokenWrapper tw = verifyBearer(authHeader);",
		"verifyBearer(authHeader);\n        // auth_any_user",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java Authorizer must share verified bearer/token-class rejection path; missing %q", want)
		}
	}
	if got := strings.Count(rendered, "refresh tokens cannot be used for API access"); got != 1 {
		t.Fatalf("Java Authorizer must define refresh-token rejection in one shared helper, got %d copies", got)
	}
	if strings.Contains(rendered, "String authTokenUse =") {
		t.Fatalf("Java auth_any_user path must not keep a divergent token-class rejection branch")
	}
}

func TestJavaAuthorizerNormalizesGeneratedGrantTokens(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/grpcserver/Authorizer.java.tmpl")
	for _, want := range []string{
		"import {{.PackageBase}}.grants.Grants;",
		"Grants.normalize(raw)",
		"Grants.unknownTokens(raw)",
		"dropping unknown service_entitlements grant(s); token grants remain fail-closed",
		"for (String grant : te.getGrantsList())",
		"static String methodGrantToken(String fallbackService, String fullMethod)",
		"int serviceSlash = serviceFqn.lastIndexOf('/')",
		"static String grantTokenPart(String value)",
		"replace('/', '_')",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java Authorizer must authorize via generated grant normalization; missing %q", want)
		}
	}
	if strings.Contains(rendered, "Entitlements.EntitlementGrant") || strings.Contains(rendered, "grant.getToken()") {
		t.Fatal("Java authorizer must consume service_entitlements.grants as flat strings, not token objects")
	}
}

func TestJavaScopeToEntitlementsWarnsOnUnknownGeneratedGrants(t *testing.T) {
	var rendered strings.Builder
	writeScopeToEntitlements(&rendered, []tacproto.ServiceRPCEntry{
		{GrpcName: "anthropic", EnumName: "Anthropic", ProtoName: "AnthropicService", RPCs: []string{"CreateMessage"}},
	}, nil)
	src := rendered.String()

	for _, want := range []string{
		"private static final java.util.logging.Logger SCOPE_LOG",
		"Grants.unknownTokens(entitlements.getGrantsList())",
		"dropping unknown service_entitlements grant(s)",
		"Grants.normalize(java.util.List.of(grant))",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("scopeToEntitlements must warn on generated grant vocabulary drift; missing %q in:\n%s", want, src)
		}
	}
}

func TestJavaServiceManagerTreatsMissingTokenMinterAsPermanent(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	for _, want := range []string{
		"private static boolean isPermanentIdentityInitError(Throwable t)",
		"if (cur instanceof IdentityManager.TokenMinterNotConfiguredException) {",
		"if (isPermanentIdentityInitError(e)) {",
		`throw new RuntimeException("permanent identity startup failure", e);`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("ServiceManager must fail missing-token-minter startup without retrying; missing %q", want)
		}
	}
}

func TestJavaServiceManagerCreatesActiveStateStoreBeforeAutoTLS(t *testing.T) {
	serviceManager := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")
	staticIdx := strings.Index(serviceManager, "ServiceConnection stateStoreConn = ServiceConnection.newStatic(")
	tlsIdx := strings.Index(serviceManager, "tlsCertStreamLoop(sm, stateStoreConn, identity, tlsReady, tlsError)")
	if staticIdx < 0 {
		t.Fatalf("Java ServiceManager must create the static state-store connection before startup RPCs")
	}
	if tlsIdx < 0 {
		t.Fatalf("Java ServiceManager must start auto-TLS from the state-store connection")
	}
	if staticIdx > tlsIdx {
		t.Fatalf("Java ServiceManager must create the active state-store connection before auto-TLS startup")
	}

	serviceConnection := readJavaTemplate(t, "java/chassis/serviceconnection/ServiceConnection.java.tmpl")
	if !strings.Contains(serviceConnection, "sc.selectActive();\n\n        // Background loops") {
		t.Fatalf("Java ServiceConnection.newStatic must select an active endpoint before returning")
	}
}

func TestJavaIdentityMintersUseScopedRefreshLocks(t *testing.T) {
	// The static cred_pair TokenMinter has been removed; the Kubernetes
	// projected-token minter now carries the per-audience refresh-lock shape.
	k8sMinter := readJavaTemplate(t, "java/chassis/service_im/KubernetesMinter.java.tmpl")
	for _, want := range []string{
		"private final ConcurrentHashMap<String, Object> refreshLocks = new ConcurrentHashMap<>();",
		"public String getTokenFor(String audience) throws Exception",
		"refreshLocks.computeIfAbsent(audience, ignored -> new Object())",
		"synchronized (lock)",
		"private TokenEntry cachedToken(String audience)",
	} {
		if !strings.Contains(k8sMinter, want) {
			t.Fatalf("Java KubernetesMinter must use per-audience refresh locks; missing %q", want)
		}
	}
	if strings.Contains(k8sMinter, "public synchronized String getTokenFor") ||
		strings.Contains(k8sMinter, "public synchronized String getToken()") {
		t.Fatalf("Java KubernetesMinter must not synchronize the whole token lookup path")
	}

	cloudMinter := readJavaTemplate(t, "java/chassis/service_im/CloudMinter.java.tmpl")
	for _, want := range []string{
		"private final ConcurrentHashMap<String, Object> refreshLocks = new ConcurrentHashMap<>();",
		"private final Object sharedRefreshLock = new Object();",
		"Object audienceLock = refreshLocks.computeIfAbsent(audience, ignored -> new Object());",
		"synchronized (audienceLock)",
		"synchronized (sharedRefreshLock)",
		"private TokenEntry cachedToken(String audience)",
		"private String freshGenericAccess()",
		"private String validRefreshToken()",
		"private void doGenericExchange(String refresh) throws Exception",
		"private String doServiceExchange(String audience, String accessToken) throws Exception",
	} {
		if !strings.Contains(cloudMinter, want) {
			t.Fatalf("Java CloudMinter must split audience refresh from shared refresh; missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"public synchronized String getTokenFor",
		"public synchronized String getToken()",
		"private void doGenericExchange() throws Exception",
		"private String doServiceExchange(String audience) throws Exception",
		"legacyDeleted",
	} {
		if strings.Contains(cloudMinter, forbidden) {
			t.Fatalf("Java CloudMinter retained global/legacy token refresh shape %q", forbidden)
		}
	}
}

func TestJavaIdentityManagerReResolvesTokenAuthority(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/service_im/IdentityManager.java.tmpl")
	for _, want := range []string{
		"private final List<String> authorityTargets;",
		"private final SslContext sslCtx;",
		"private final Object channelLock = new Object();",
		"private static List<String> authorityTargets(Config config)",
		"private static ManagedChannel openBareChannel(String target, SslContext sslCtx)",
		"private boolean rotateAuthorityChannel(Exception cause)",
		"k8sMinter.setChannel(next);",
		"cloudMinter.setChannel(next);",
		"initialExchangeWithRetry(im::getToken);",
		"return getTokenForOnce(audience);",
		"retry.addSuppressed(first);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java IdentityManager must re-resolve token authority channels; missing %q", want)
		}
	}
	if strings.Contains(rendered, "Connect bare channel to first available endpoint") ||
		strings.Contains(rendered, "initialExchangeWithRetry(minter, null)") ||
		strings.Contains(rendered, "initialExchangeWithRetry(null, cloudMinter)") {
		t.Fatalf("Java IdentityManager retained the first-endpoint-only token authority path")
	}
}

// F-4b: UNAUTHENTICATED / PERMISSION_DENIED on the subscription stream are
// retryable (usually transient for S2S); only CANCELLED stays non-retryable.
func TestJavaSubscriberRetriesAuthErrors(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/Subscriber.java.tmpl")

	nonRetryable := strings.Index(rendered, "Non-retryable error on")
	if nonRetryable < 0 {
		t.Fatalf("subscriber missing non-retryable classification")
	}
	guard := rendered[:nonRetryable]
	guardTail := guard[strings.LastIndex(guard, "if ("):]
	if strings.Contains(guardTail, "PERMISSION_DENIED") || strings.Contains(guardTail, "UNAUTHENTICATED") {
		t.Fatalf("UNAUTHENTICATED/PERMISSION_DENIED must be retryable, not terminate the subscription:\n%s", guardTail)
	}
	if !strings.Contains(guardTail, "CANCELLED") {
		t.Fatalf("CANCELLED must remain non-retryable:\n%s", guardTail)
	}
	if !strings.Contains(rendered, "code == Status.Code.UNAUTHENTICATED || code == Status.Code.PERMISSION_DENIED") {
		t.Fatalf("auth failures must keep severe-level logging on the retry path")
	}
}

// F-18a: inflow must be handler-paced — onMessage only enqueues; the next
// message is requested after the handler finishes processing the previous one.
func TestJavaSubscriberBackpressure(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/Subscriber.java.tmpl")

	onMessage := strings.Index(rendered, "public void onMessage(")
	onClose := strings.Index(rendered, "public void onClose(")
	if onMessage < 0 || onClose < 0 || onClose < onMessage {
		t.Fatalf("subscriber missing onMessage/onClose")
	}
	if strings.Contains(rendered[onMessage:onClose], "call.request(1);") {
		t.Fatalf("onMessage must not request the next message — inflow would run at network speed")
	}
	handleIdx := strings.Index(rendered, "DecodedRecord decoded = decodeSubscribeRecord(response);")
	if handleIdx < 0 {
		t.Fatalf("the processing loop must request the next message after the handler finishes")
	}
	requestIdx := strings.Index(rendered[handleIdx:], "call.request(1);")
	if requestIdx < 0 {
		t.Fatalf("the processing loop must request the next message after the handler finishes")
	}
}

func TestJavaSubscriberDecodesRecordMetaEnvelope(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/Subscriber.java.tmpl")
	for _, want := range []string{
		"MethodDescriptor<byte[], byte[]>",
		"DecodedRecord decoded = decodeSubscribeRecord(response);",
		"{{.TypeOuterClass}}.{{.TypeName}}.parseFrom(envelope.payload)",
		// The parsed message is handed to DecodedRecord directly, or through
		// projectRoutingEnvelope when the subscriber is routing-only (see
		// spec.proto's routing_only_services). Either way it is parsed here.
		"decodeRecordMeta(envelope.meta)",
		"public final String topic;",
		"public final int partition;",
		"topic = new String(value, java.nio.charset.StandardCharsets.UTF_8);",
		"partition = (int) readVarint(data, cursor);",
		"timestamp = decodeTimestampMillis(value);",
		"{{.TypeOuterClass}}.{{.TypeName}}.parseFrom(data){{end}},",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java subscriber must decode metadata-bearing EventBus subscribe envelopes; missing %q", want)
		}
	}
	if strings.Contains(rendered, "handler.handle(event, new RecordMeta(0, 0, new byte[0]));") {
		t.Fatalf("Java subscriber must not hard-code zero RecordMeta for every message")
	}
}

func TestJavaSubscriptionMetaCarriesFullEventBusRecordMeta(t *testing.T) {
	handler := readJavaTemplate(t, "java/types/Handler.java.tmpl")
	serviceManager := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")
	for _, want := range []string{
		"public final byte[] key;",
		"public final String topic;",
		"public final int partition;",
		"public final long offset;",
		"public final long timestamp;",
		"public SubscriptionMeta(long offset, long timestamp, byte[] key, String topic, int partition)",
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("Java Handler.SubscriptionMeta must carry full EventBus RecordMeta; missing %q", want)
		}
	}
	if !strings.Contains(serviceManager, "new Handler.SubscriptionMeta(meta.offset, meta.timestamp, meta.key, meta.topic, meta.partition)") {
		t.Fatalf("Java ServiceManager must pass full EventBus RecordMeta into Handler.SubscriptionMeta")
	}
}

func TestJavaSubscriberHandlerErrorTearsDownStream(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/Subscriber.java.tmpl")
	for _, want := range []string{
		`call.cancel("handler error", e);`,
		"throw e;",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("handler errors must tear down the Java subscription stream for broker redelivery; missing %q", want)
		}
	}
	if strings.Contains(rendered, `LOG.log(Level.WARNING, "{{.SourcePackage}}.{{.SourceField}} handler error", e);`) {
		t.Fatalf("Java subscriber must not log-and-continue after handler errors")
	}
}

// F-5: releasing the last watcher of a scope must stop the drain thread
// (interrupt + closed-ring check) so it does not leak per connect/disconnect
// generation.
func TestJavaCollectionRuntimeStopsDrainOnRelease(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/collection/CollectionRuntime.java.tmpl")

	for _, want := range []string{
		"private final Map<String, Thread> drainThreads = new HashMap<>();",
		"drain.interrupt();",
		"!Thread.currentThread().isInterrupted()",
		"!drainRing.isClosed()",
		"drainThreads.remove(key, Thread.currentThread());",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("CollectionRuntime drain must stop on last-watcher release; missing %q", want)
		}
	}
}

// F-25: page tokens are strict no-padding base64url on every backend; Java
// must reject '=' (and any other non-base64url character) before decoding.
func TestJavaCollectionRuntimeRejectsPaddedPageTokens(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/collection/CollectionRuntime.java.tmpl")

	decode := strings.Index(rendered, "public static Cursor decodePageToken(String token)")
	b64 := strings.Index(rendered, "java.util.Base64.getUrlDecoder().decode(token);")
	check := strings.Index(rendered, "|| (c >= '0' && c <= '9') || c == '-' || c == '_';")
	if decode < 0 || b64 < 0 || check < 0 {
		t.Fatalf("decodePageToken must validate the base64url alphabet before decoding")
	}
	if !(decode < check && check < b64) {
		t.Fatalf("the alphabet check must run before Base64 decoding (decode=%d check=%d b64=%d)", decode, check, b64)
	}
}

func TestJavaTagsRuntimeTemplateMatchesKubernetesSemantics(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/tags/Tags.java.tmpl")
	for _, want := range []string{
		"public static boolean containsAll(",
		"public static boolean matches(",
		"case TAG_SELECTOR_OPERATOR_IN:",
		"case TAG_SELECTOR_OPERATOR_NOT_IN:",
		"return !exists || !contains(req.getValuesList(), value);",
		"case TAG_SELECTOR_OPERATOR_EXISTS:",
		"case TAG_SELECTOR_OPERATOR_DOES_NOT_EXIST:",
		"public static void validateKey(String key)",
		"public static void validateSelector(",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java tags runtime template missing %q", want)
		}
	}
}

// F-16: a handler failure on the FIRST TLS cert delivery is fatal — startup
// fails loudly instead of proceeding as if the cert had been applied. Renewal
// failures stay log-and-continue.
func TestJavaServiceManagerFirstTlsCertHandlerFailureIsFatal(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	for _, want := range []string{
		`throw new RuntimeException("handler newTlsCert failed on initial delivery", e);`,
		"private static final class FatalTlsHandlerException extends Exception",
		`new FatalTlsHandlerException(`,
		"if (e instanceof FatalTlsHandlerException) {",
		`LOG.warning("Handler newTlsCert failed: " + cbErr.getMessage());`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("first TLS cert handler failure must be fatal at startup; missing %q", want)
		}
	}
}

func TestJavaCertStreamsFollowSharedContractMarkers(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	for _, want := range []string{
		"ManageCert stream completed before first cert delivery",
		"ManageCert stream completed after first cert delivery",
		`throw new IllegalStateException("no active connection for ManageCert");`,
		"ManageSmbCert stream completed before first member cert delivery",
		"ManageSmbCert stream completed after first member cert delivery",
		`throw new IllegalStateException("no active connection for ManageSmbCert");`,
		"private final ServiceConnection smbAuthorityConn;",
		`new ServiceConnection("{{.SmbAuthorityServiceID}}", channelFactory)`,
		"smbAuthorityConn.reconcileFromHealth(filtered)",
		"smbCertStreamLoop(this, smbAuthorityConn, identity, smbReady, smbError)",
		"try { outbound.onCompleted(); } catch (Exception ignored) {}",
		"byte[] pending = pendingPrivateKeyDer.get();",
		"AtomicReference<byte[]> activePublicKeyDer",
		"AtomicReference<byte[]> pendingPublicKeyDer",
		"verifyLeafMatchesPublicKey",
		"pendingPrivateKeyDer.set(null);",
		"pendingPublicKeyDer.set(null);",
		"activePrivateKeyDer.set(pending);",
		"activePublicKeyDer.set(publicKeyDer);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java cert-stream template must follow shared contract; missing %q", want)
		}
	}

	tlsHandler := strings.Index(rendered, "activeHandler.newTlsCert(chain, keyDer);")
	tlsPromote := strings.Index(rendered, "activePrivateKeyDer.set(pending);")
	if tlsHandler < 0 || tlsPromote < 0 || tlsPromote < tlsHandler {
		t.Fatalf("TLS pending key must promote only after newTlsCert succeeds")
	}

	smbHandler := strings.Index(rendered, "activeHandler.newSmbCert(memberChain, keyDer, ca);")
	smbPromote := strings.LastIndex(rendered, "activePrivateKeyDer.set(pending);")
	if smbHandler < 0 || smbPromote < 0 || smbPromote < smbHandler {
		t.Fatalf("SMB pending key must promote only after newSmbCert succeeds")
	}
}

// PC-L-6: a renewal keypair is staged as pending BEFORE the CSR is sent, and
// the send can throw. A CSR that never went out has no in-flight request to
// pair with, so the catch must drop the staged key on both legs — otherwise
// the key material is retained indefinitely and the next cert delivery is
// verified against a public key the authority was never asked to sign.
func TestJavaRenewalCsrSendFailureClearsPendingKey(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	blocks := renewHandlerBlocks(t, rendered)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 renewal handlers (TLS + SMB), found %d", len(blocks))
	}
	for i, block := range blocks {
		for j, body := range renewHandlerCatches(t, block, i) {
			for _, want := range []string{
				"pendingPrivateKeyDer.set(null);",
				"pendingPublicKeyDer.set(null);",
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("renewal handler %d catch %d must clear the staged key when the CSR never went out; missing %q in:\n%s",
						i, j, want, body)
				}
			}
		}
	}
}

func TestJavaTypedHealthCollectorRendered(t *testing.T) {
	handler := renderJavaTemplateForTest(t, "Handler.java.tmpl", javaSmTemplateData{
		PackageBase:         "com.example",
		SmPrefix:            "s2sm",
		TypesBase:           "com.example.types",
		HasTypedHealth:      true,
		TypedHealthImport:   "com.example.types.services.demo.Demo",
		TypedHealthJavaType: "Demo.DemoHealth",
	})
	for _, want := range []string{
		"import com.example.types.services.demo.Demo;",
		"default Demo.DemoHealth collectHealth()",
		"return Demo.DemoHealth.getDefaultInstance();",
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("Handler typed health seam missing %q:\n%s", want, handler)
		}
	}

	sm := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", javaSmTemplateData{
		PackageBase:           "com.example",
		ChassisBase:              "com.example.sys",
		TypesBase:             "com.example.types",
		SmPrefix:              "s2sm",
		ServiceLabel:          "demo",
		HasServiceHealthState: true,
		HealthStateJavaPkg:    "services.health",
		HealthStateOuterClass: "Health",
		HasTypedHealth:        true,
		TypedHealthImport:     "com.example.types.services.demo.Demo",
		TypedHealthJavaType:   "Demo.DemoHealth",
	})
	for _, want := range []string{
		"healthReporter.setTypedHealthCollector(base -> handler.collectHealth().toBuilder()",
		".setBase(base)",
		"healthReporter.start(firstHealthReady, MAX_INIT_RETRIES);",
		"healthReporter.firstPublishFailure()",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("ServiceManager typed health/first publish wiring missing %q:\n%s", want, sm)
		}
	}
}

func TestJavaTypesImportForProtoUsesGeneratedTypeDir(t *testing.T) {
	tests := []struct {
		name         string
		protoFile    string
		protoPackage string
		outerClass   string
		serviceState bool
		want         string
	}{
		{
			name:         "flat service proto",
			protoFile:    "services/stratum.proto",
			protoPackage: "stratum",
			outerClass:   "Stratum",
			want:         "com.example.types.services.stratum.Stratum",
		},
		{
			name:         "nested service proto",
			protoFile:    "services/gitlab-connector/types.proto",
			protoPackage: "gitlabconnector",
			outerClass:   "Gitlabconnector",
			want:         "com.example.types.services.gitlabconnector.gitlabconnector.Gitlabconnector",
		},
		{
			name:         "nested service state proto",
			protoFile:    "services/identity/state.proto",
			protoPackage: "identity",
			outerClass:   "IdentityState",
			serviceState: true,
			want:         "com.example.types.services.identity.services.IdentityState",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := javaTypesImportForProto("com.example", tt.protoFile, tt.protoPackage, tt.outerClass, tt.serviceState)
			if got != tt.want {
				t.Fatalf("javaTypesImportForProto() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestJavaServiceManagerRendersDiskStoreRollSchedulers(t *testing.T) {
	rendered := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", javaSmTemplateData{
		PackageBase:  "com.example",
		ChassisBase:     "com.example.sys",
		TypesBase:    "com.example.types",
		SmPrefix:     "s2sm",
		ServiceLabel: "demo",
		DiskStores: []javaDiskStoreRuntimeEntry{{
			StoreName:     "cred_pair",
			MethodName:    "CredPair",
			LogField:      "credPairLog",
			HandlerGetter: "getCredPairStoreGrpcImpl",
		}},
		HasDiskStores: true,
	})

	for _, want := range []string{
		"public static final class DiskStoreLogs",
		"private final Duration rollInterval",
		"DiskStores.openCredPairLog(base, StorePath.ROOT_TENANT, archiveCount, codec)",
		"private java.util.List<ScheduledExecutorService> startDiskStoreRollSchedulers(Handler handler)",
		"RollSchedule.runRollScheduler(",
		"diskStoreLogs.rollInterval",
		"nodeId + \"/cred_pair\"",
		// The baseline is narrowed by LocalRows before it is written (see
		// diskstore_locality_gen_test.go), so the list call is now two lines.
		"var store = handler.getCredPairStoreGrpcImpl();",
		"var response = store.list(null);",
		"DiskStores.snapshotCredPair(response.getItemsList())",
		"java.util.List<ScheduledExecutorService> diskStoreSchedulers = startDiskStoreRollSchedulers(handler);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Java ServiceManager missing diskstore scheduler marker %q:\n%s", want, rendered)
		}
	}
}

func TestJavaHealthReporterWrapsCheckerAndClassifiesFirstPublish(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/healthreporter/HealthReporter.java.tmpl")

	for _, want := range []string{
		"public interface TypedHealthCollector",
		"HealthChecker failed; publishing UNHEALTHY",
		"firstPublishFailure()",
		"isPermanentPublishFailure",
		"io.grpc.Status.Code.PERMISSION_DENIED",
		"first health publish failed after",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("HealthReporter must wrap/check/classify health publishing; missing %q", want)
		}
	}
}

func TestJavaServiceConnectionVerifiesSpiffePeerIdentity(t *testing.T) {
	serviceConnection := readJavaTemplate(t, "java/chassis/serviceconnection/ServiceConnection.java.tmpl")
	for _, want := range []string{
		"public interface ChannelFactory",
		"ManagedChannel create(String serviceId, String target) throws Exception",
		"buildClientSslContext(trustedCas, serviceId, trustDomain)",
		"ServiceIdentityTrustManager",
		"Spiffe.normalizeTrustDomain(trustDomain)",
		"Spiffe.parseSpiffeId",
		"parsed.subjectKind == Spiffe.SubjectKind.SERVICE",
		"serviceId.equals(parsed.subjectPath)",
	} {
		if !strings.Contains(serviceConnection, want) {
			t.Fatalf("ServiceConnection must verify peer SPIFFE identity with the logical service id; missing %q", want)
		}
	}

	serviceManager := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")
	for _, want := range []string{
		"private final ServiceConnection.ChannelFactory channelFactory;",
		"ServiceConnection.ChannelFactory channelFactory =",
		"ServiceConnection.channelFactory(cfg)",
	} {
		if !strings.Contains(serviceManager, want) {
			t.Fatalf("ServiceManager must use the service-aware channel factory; missing %q", want)
		}
	}
}

// The ServiceManager must expose audience-scoped credentials, the channel
// factory, and service resolution so handlers can build custom subscribers
// without standing up a second IdentityManager.
func TestJavaServiceManagerExposesSubscriberPlumbing(t *testing.T) {
	rendered := readJavaTemplate(t, "java/types/ServiceManager.java.tmpl")

	for _, want := range []string{
		"public io.grpc.CallCredentials callCredentialsForAudience(String audience)",
		"return identity.callCredentialsForAudience(audience);",
		"public ServiceConnection.ChannelFactory getChannelFactory()",
		"return channelFactory;",
		"public io.grpc.ManagedChannel resolveService(String serviceName)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("ServiceManager must expose subscriber plumbing; missing %q", want)
		}
	}
}

// An already origin_-prefixed claim must survive setClaimsFromStruct. origin_iss is
// both an origin_-prefixed IdP claim and a declared JwtPayloadType, so it lands in the
// caller's skip set (kernel builds it from Helpers.jwtPayloadTypeValidValues()); with
// the skip check first it was dropped on every re-mint and OPA saw zero entitlements.
func TestJavaTokenWrapperKeepsAlreadyOriginPrefixedClaims(t *testing.T) {
	rendered := readJavaTemplate(t, "java/chassis/token/TokenWrapper.java.tmpl")

	for _, want := range []string{
		`boolean alreadyPrefixed = key.startsWith("origin_")`,
		`&& originPrefixKeys.contains(key.substring("origin_".length()));`,
		"if (!alreadyPrefixed && skipKeys.contains(key)) continue;",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("setClaimsFromStruct must exempt already origin_-prefixed claims from skipKeys; missing %q", want)
		}
	}
	if strings.Contains(rendered, "if (skipKeys.contains(key)) continue;") {
		t.Fatal("setClaimsFromStruct must not skip on skipKeys before the already-origin_-prefixed check")
	}
	guard := strings.Index(rendered, "boolean alreadyPrefixed")
	skip := strings.Index(rendered, "skipKeys.contains(key)) continue;")
	if guard < 0 || skip < 0 || guard > skip {
		t.Fatal("the already-origin_-prefixed guard must be computed before the skipKeys check")
	}
}

func renderJavaTemplateForTest(t *testing.T, name string, data javaSmTemplateData) string {
	t.Helper()
	tmplStr, err := loadTemplate(name)
	if err != nil {
		t.Fatalf("loadTemplate %s: %v", name, err)
	}
	tmpl, err := template.New(name).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute %s: %v", name, err)
	}
	return out.String()
}
