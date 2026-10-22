// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// F10: an accounting record must name the principal the platform VERIFIED.
//
// Before the fix the generated Java wrapper emitted every RpcCompleted and
// RpcError with an empty identity map, so the only thing tying a record to a
// caller was x-request-id — a request header the caller writes. Pick your own
// x-request-id and the audit trail files a privileged action under it; reuse
// someone else's and the action reads as theirs.
//
// Everything below is real: the generated ContractAuditApi wrapper, the
// generated Authorizer, the generated ContractAuditApiGrpc client stub, a real
// AccountingManager.Emitter, real Ed25519-signed tokens, and a real gRPC socket
// on localhost. Nothing under test is mocked.
//
// The server is wired the way a real deployment wires one: the chassis
// GrpcServer installs Authorizer.interceptor(), and a service that wants
// x-request-id tagging additionally installs Auth.requestIdInterceptor() (this
// is exactly what platform/services/kernel's ClusterServer does). The
// authenticate seam is wired the way the generated ServiceManager wires it.

package tacplus.crosstest.accountingidentity;

import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.am.AccountingManager;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.grpcserver.Authorizer;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.key.ImmutableKey;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.key.KeyHelpers;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.servicemanager.Auth;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.servicemanager.Caller;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.token.TokenWrapper;
import com.gitlab.example.tacplus.consumer.contractclient.model.s2sm.internal.grpc.servers.services.contractclient.ContractAuditApi;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.generated.entitlements.Entitlements;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.ContractAuditApiGrpc;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.services.contractclient.Contractclient;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.accounting.Accounting;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.errors.ProtoException;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.jwk.JwkOuterClass;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.key.KeyOuterClass;

import io.grpc.ClientInterceptors;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.Server;
import io.grpc.ServerBuilder;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;
import io.grpc.stub.StreamObserver;

import java.util.ArrayList;
import java.util.Base64;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.TimeUnit;

public final class AccountingIdentityTest {

    private static int failures = 0;

    private static final String SERVICE_AUDIENCE = "contractclient";
    private static final String ISSUER = "test-issuer";
    private static final String ENTITLED_GRANT = "CONTRACTCLIENT_CONTRACT_AUDIT_API_EXECUTE_ENTITLED_AUDIT";

    /** The item id the handler throws on, to reach the RpcError accounting path. */
    private static final String BOOM = "boom";

    private static final Metadata.Key<String> AUTHORIZATION =
            Metadata.Key.of("authorization", Metadata.ASCII_STRING_MARSHALLER);
    private static final Metadata.Key<String> REQUEST_ID =
            Metadata.Key.of("x-request-id", Metadata.ASCII_STRING_MARSHALLER);

    /** Every accounting event the wrapper emitted, in order. */
    private static final List<Accounting.Event> EVENTS = new CopyOnWriteArrayList<>();

    // ---- tokens ------------------------------------------------------------

    private static ImmutableKey newTestKey() throws Exception {
        KeyOuterClass.Key proto = KeyHelpers.newEd25519Key();
        return ImmutableKey.from(proto);
    }

    /** The verifying JWK for a signing key; the raw Ed25519 key is the SPKI tail. */
    private static JwkOuterClass.Jwk jwkForKey(ImmutableKey key) {
        byte[] spki = key.getPublicKey().getEncoded();
        byte[] raw = new byte[32];
        System.arraycopy(spki, spki.length - 32, raw, 0, 32);
        return JwkOuterClass.Jwk.newBuilder()
                .setKid(key.getProto().getId())
                .setKty(KeyOuterClass.KeyType.KEY_TYPE_OKP)
                .setCrv("Ed25519")
                .setX(Base64.getUrlEncoder().withoutPadding().encodeToString(raw))
                .build();
    }

    private static Authorizer authorizerWithKey(ImmutableKey key) {
        Map<String, JwkOuterClass.Jwk> entries = new HashMap<>();
        JwkOuterClass.Jwk jwk = jwkForKey(key);
        entries.put(jwk.getKid(), jwk);
        return new Authorizer(sink -> sink.update(entries));
    }

    /** A real, correctly-signed user token for the given subject. */
    private static String signedUserToken(ImmutableKey key, String subject) throws Exception {
        TokenWrapper tok = TokenWrapper.create()
                .setIssuer(ISSUER)
                .setAudience(SERVICE_AUDIENCE)
                .setExpiration(System.currentTimeMillis() / 1000 + 3600)
                .setSubType("user")
                .setSubject(subject)
                .setNamespace("/")
                .setServiceEntitlements(Entitlements.TokenEntitlements.newBuilder()
                        .addGrants(ENTITLED_GRANT)
                        .build());
        tok.sign(key);
        return "Bearer " + tok.getBase64();
    }

    /**
     * A FORGED bearer: three base64url segments, a hand-written payload naming
     * whatever subject the attacker likes, and a signature that is not a
     * signature. Nothing signed it; no verifying key exists for it.
     */
    private static String forgedTokenFor(String subject) {
        Base64.Encoder b64 = Base64.getUrlEncoder().withoutPadding();
        String header = b64.encodeToString("{\"alg\":\"EdDSA\",\"kid\":\"no-such-key\"}".getBytes());
        String payload = b64.encodeToString(
                ("{\"sub\":\"" + subject + "\",\"aud\":\"" + SERVICE_AUDIENCE + "\",\"iss\":\"" + ISSUER + "\"}").getBytes());
        String signature = b64.encodeToString("not-a-signature".getBytes());
        return "Bearer " + header + "." + payload + "." + signature;
    }

    // ---- server ------------------------------------------------------------

    private static Contractclient.AuditResponse ok() {
        return Contractclient.AuditResponse.newBuilder().setResultId("audit-1").build();
    }

    private static final class Handler implements ContractAuditApi.ContractAuditApiHandler {
        @Override
        public Contractclient.AuditResponse getAudit(Caller caller, Contractclient.AuditRequest request)
                throws ProtoException {
            if (BOOM.equals(request.getItemId())) {
                throw ProtoException.newErrGeneral(null, "handler failed on purpose");
            }
            return ok();
        }

        @Override
        public Contractclient.AuditResponse executeEntitledAudit(Caller caller, Contractclient.AuditRequest request) {
            return ok();
        }

        @Override
        public Contractclient.AuditResponse executeAudit(Caller caller, Contractclient.AuditRequest request) {
            return ok();
        }

        @Override
        public void generateAuditEvents(Caller caller, Contractclient.AuditRequest request,
                StreamObserver<Contractclient.AuditEvent> responseObserver) throws ProtoException {
            throw ProtoException.newErrGeneral(null, "not part of this test");
        }

        @Override
        public StreamObserver<Contractclient.AuditEvent> sendAuditEvents(
                Caller caller, StreamObserver<Contractclient.AuditResponse> responseObserver) throws ProtoException {
            throw ProtoException.newErrGeneral(null, "not part of this test");
        }

        @Override
        public StreamObserver<Contractclient.AuditEvent> assistAuditEvents(
                Caller caller, StreamObserver<Contractclient.AuditEvent> responseObserver) throws ProtoException {
            throw ProtoException.newErrGeneral(null, "not part of this test");
        }
    }

    private static Server startServer(Authorizer authorizer) throws Exception {
        ContractAuditApi wrapper = new ContractAuditApi(new Handler(), authorizer);
        // Exactly the seam the generated ServiceManager wires.
        wrapper.setAuthenticateFunc((ContractAuditApi.Authenticator) targetNamespace -> {
            String header = Authorizer.AUTH_HEADER_CTX_KEY.get();
            if (targetNamespace == null || targetNamespace.isEmpty()) {
                return Authorizer.identityClaims(authorizer.authenticateWithToken(header));
            }
            return Authorizer.identityClaims(authorizer.authenticateAtOrBelowWithToken(header, targetNamespace));
        });
        wrapper.setEmitter(new AccountingManager.Emitter("contractclient-test", EVENTS::add));
        ServerBuilder<?> builder = ServerBuilder.forPort(0)
                .intercept(Auth.requestIdInterceptor())
                .intercept(Authorizer.headerCaptureInterceptor());
        wrapper.registerServices(builder);
        return builder.build().start();
    }

    private static Contractclient.AuditRequest request(String itemId) {
        return Contractclient.AuditRequest.newBuilder().setItemId(itemId).build();
    }

    private interface WithStub<T> {
        T apply(ContractAuditApiGrpc.ContractAuditApiBlockingStub stub) throws Exception;
    }

    private static <T> T call(Server server, String bearer, String requestId, WithStub<T> body) throws Exception {
        ManagedChannel channel = ManagedChannelBuilder
                .forAddress("localhost", server.getPort())
                .usePlaintext()
                .build();
        try {
            Metadata headers = new Metadata();
            if (bearer != null) {
                headers.put(AUTHORIZATION, bearer);
            }
            if (requestId != null) {
                headers.put(REQUEST_ID, requestId);
            }
            io.grpc.Channel authed = ClientInterceptors.intercept(
                    channel, MetadataUtils.newAttachHeadersInterceptor(headers));
            return body.apply(ContractAuditApiGrpc.newBlockingStub(authed)
                    .withDeadlineAfter(15, TimeUnit.SECONDS));
        } finally {
            channel.shutdown();
            channel.awaitTermination(5, TimeUnit.SECONDS);
        }
    }

    // ---- event helpers -----------------------------------------------------

    /** Runs body with a fresh event log and returns everything the wrapper emitted. */
    private static List<Accounting.Event> record(Body body) throws Exception {
        EVENTS.clear();
        try {
            body.run();
        } catch (StatusRuntimeException ignored) {
            // Rejections are a legitimate outcome here; the events are the subject.
        }
        // The client returns before the server-side emit is guaranteed visible.
        for (int i = 0; i < 100 && EVENTS.isEmpty(); i++) {
            Thread.sleep(20);
        }
        return new ArrayList<>(EVENTS);
    }

    private static String describe(List<Accounting.Event> events) {
        StringBuilder sb = new StringBuilder();
        for (Accounting.Event e : events) {
            sb.append("\n    ").append(e.getEventCase())
              .append(" request_id=\"").append(e.getRequestId()).append('"')
              .append(" identity=").append(e.getIdentityMap());
        }
        return sb.length() == 0 ? "\n    (no events)" : sb.toString();
    }

    // ---- assertions --------------------------------------------------------

    private static void check(String name, boolean ok, String detail) {
        if (ok) {
            System.out.println("PASS " + name + detail);
        } else {
            System.out.println("FAIL " + name + ": " + detail);
            failures++;
        }
    }

    private interface Body {
        void run() throws Exception;
    }

    public static void main(String[] args) throws Exception {
        ImmutableKey key = newTestKey();
        Authorizer authorizer = authorizerWithKey(key);
        Server server = startServer(authorizer);
        String alice = signedUserToken(key, "alice@example.com");
        String mallory = signedUserToken(key, "mallory@example.com");

        try {
            // 1. THE FIX. An entitled-user RPC (checkAuth path) driven with a real
            //    verified token: the record names the verified subject, plus the
            //    audience and issuer that were checked. Before the fix this map
            //    was empty on every single record.
            List<Accounting.Event> e1 = record(() ->
                    call(server, alice, "req-1", stub -> stub.executeEntitledAudit(request("item-1"))));
            Map<String, String> id1 = e1.isEmpty() ? Map.of() : e1.get(0).getIdentityMap();
            check("entitled RPC records the verified subject",
                    e1.size() == 1
                            && e1.get(0).getEventCase() == Accounting.Event.EventCase.RPC_COMPLETED
                            && "alice@example.com".equals(id1.get("sub"))
                            && SERVICE_AUDIENCE.equals(id1.get("aud"))
                            && ISSUER.equals(id1.get("iss")),
                    describe(e1));

            // 2. The auth_any_user path (checkAuthenticate) records identity too.
            List<Accounting.Event> e2 = record(() ->
                    call(server, alice, "req-2", stub -> stub.getAudit(request("item-1"))));
            check("auth_any_user RPC records the verified subject",
                    e2.size() == 1
                            && "alice@example.com".equals(e2.get(0).getIdentityMap().get("sub")),
                    describe(e2));

            // 3. The failure path is attributed too — an RpcError is an audit record.
            List<Accounting.Event> e3 = record(() ->
                    call(server, alice, "req-3", stub -> stub.getAudit(request(BOOM))));
            check("RpcError records the verified subject",
                    e3.size() == 1
                            && e3.get(0).getEventCase() == Accounting.Event.EventCase.RPC_ERROR
                            && "alice@example.com".equals(e3.get(0).getIdentityMap().get("sub")),
                    describe(e3));

            // 4. ATTRIBUTION UNDER A FORGED CORRELATION KEY. Mallory performs the
            //    privileged action while writing Alice's x-request-id. The record
            //    still says mallory: the header is a correlation hint, the identity
            //    is what the platform verified.
            List<Accounting.Event> e4 = record(() ->
                    call(server, mallory, "req-1", stub -> stub.executeEntitledAudit(request("item-1"))));
            Map<String, String> id4 = e4.isEmpty() ? Map.of() : e4.get(0).getIdentityMap();
            check("a caller-chosen x-request-id cannot misattribute the action",
                    e4.size() == 1
                            && "req-1".equals(e4.get(0).getRequestId())
                            && "mallory@example.com".equals(id4.get("sub")),
                    describe(e4));

            // 5. A forged bearer naming another subject is rejected on any RPC that
            //    verifies — the forged sub never reaches the audit trail at all.
            List<Accounting.Event> e5 = record(() ->
                    call(server, forgedTokenFor("alice@example.com"), "req-5",
                            stub -> stub.executeEntitledAudit(request("item-1"))));
            check("forged token on a verifying RPC produces no record",
                    e5.isEmpty(), describe(e5));

            // 6. THE FAIL-CLOSED HALF. ExecuteAudit is RPC_FLAG_PUBLIC_NO_AUTH:
            //    nothing on that path verifies anything. The record must say "no
            //    authenticated caller" rather than repeat the sub the attacker
            //    wrote into an unsigned bearer.
            List<Accounting.Event> e6 = record(() ->
                    call(server, forgedTokenFor("alice@example.com"), "req-6",
                            stub -> stub.executeAudit(request("item-1"))));
            check("no-auth RPC records no identity rather than an unverified one",
                    e6.size() == 1
                            && e6.get(0).getIdentityMap().isEmpty()
                            && "req-6".equals(e6.get(0).getRequestId()),
                    describe(e6));

            // 7. Same RPC with no token at all — case 6 must reveal no more than this.
            List<Accounting.Event> e7 = record(() ->
                    call(server, null, "req-7", stub -> stub.executeAudit(request("item-1"))));
            check("no-auth RPC with no token records no identity",
                    e7.size() == 1 && e7.get(0).getIdentityMap().isEmpty(),
                    describe(e7));
        } finally {
            server.shutdownNow();
            server.awaitTermination(5, TimeUnit.SECONDS);
        }

        if (failures > 0) {
            System.out.println(failures + " check(s) failed");
            System.exit(1);
        }
        System.out.println("all checks passed");
    }
}
