// Copyright (c) 2026 Second Star Cavalier LLC
//
// F11: identity claims must come from a VERIFIED token, never from a raw
// base64 decode of whatever bearer the caller sent.
//
// Everything below is real: the generated ContractApi wrapper, the generated
// Authorizer, the generated ContractApiGrpc client stub, real Ed25519-signed
// tokens, and a real gRPC socket on localhost. Nothing under test is mocked.
//
// The server is wired the way a real deployment wires one: the chassis
// GrpcServer installs Authorizer.interceptor(), and a service that wants
// x-request-id tagging additionally installs Auth.requestIdInterceptor() (this
// is exactly what platform/services/kernel's ClusterServer does).
//
// GetPublicEvent is RPC_FLAG_PUBLIC_NO_AUTH and returns a record carrying a
// (spec.redact) MAY_REDACT field, so the generated wrapper runs the RED-1 egress
// sanitizer on a method where nothing ever verifies a token. Before the fix the
// sanitizer sourced its data_grants from the unverified decode, so an unsigned
// token with a hand-written data_grants claim unlocked the raw payload.

package tacplus.crosstest.claimsprovenance;

import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.grpcserver.Authorizer;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.key.ImmutableKey;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.key.KeyHelpers;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.servicemanager.Auth;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.servicemanager.Caller;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.token.TokenWrapper;
import com.gitlab.example.tacplus.consumer.contractclient.model.s2sm.internal.grpc.servers.contract.contractclient.ContractApi;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.Contractclient;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.contract.contractclient.ContractApiGrpc;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.errors.ProtoException;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.jwk.JwkOuterClass;
import com.gitlab.example.tacplus.consumer.contractclient.model.types.sys.key.KeyOuterClass;
import com.gitlab.example.tacplus.consumer.contractclient.model.chassis.collection.CollectionRuntime;

import io.grpc.ClientInterceptors;
import io.grpc.ManagedChannel;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.Server;
import io.grpc.ServerBuilder;
import io.grpc.Status;
import io.grpc.StatusRuntimeException;
import io.grpc.stub.MetadataUtils;

import java.util.Base64;
import java.util.HashMap;
import java.util.Iterator;
import java.util.List;
import java.util.Map;
import java.util.concurrent.TimeUnit;

public final class ClaimsProvenanceTest {

    private static int failures = 0;

    /** The secret the handler produces; RED-1 decides whether it egresses. */
    private static final String SECRET_PAYLOAD = "PAYLOAD-THAT-NEEDS-A-GRANT";

    private static final String PAYLOAD_GRANT = "CONTRACTCLIENT_CONTRACT_EVENT_PAYLOAD";

    /** The generated Authorizer's audience for this fixture service. */
    private static final String SERVICE_AUDIENCE = "contractclient";

    private static final Metadata.Key<String> AUTHORIZATION =
            Metadata.Key.of("authorization", Metadata.ASCII_STRING_MARSHALLER);

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

    /** A real, correctly-signed user token carrying the payload data grant. */
    private static String signedUserTokenWithGrant(ImmutableKey key) throws Exception {
        TokenWrapper tok = TokenWrapper.create()
                .setIssuer("test-issuer")
                .setAudience(SERVICE_AUDIENCE)
                .setExpiration(System.currentTimeMillis() / 1000 + 3600)
                .setSubType("user")
                .setSubject("entitled@example.com")
                .setNamespace("/")
                .setServiceEntitlements(
                        com.gitlab.example.tacplus.consumer.contractclient.model.types.generated.entitlements
                                .Entitlements.TokenEntitlements.newBuilder()
                                .addDataGrants(PAYLOAD_GRANT)
                                .build());
        tok.sign(key);
        return "Bearer " + tok.getBase64();
    }

    /**
     * A FORGED bearer: three base64url segments, a hand-written payload claiming
     * the data grant, and a signature that is not a signature. Nothing signed it;
     * no verifying key exists for it.
     */
    private static String forgedTokenWithGrant() {
        Base64.Encoder b64 = Base64.getUrlEncoder().withoutPadding();
        String header = b64.encodeToString(
                "{\"alg\":\"EdDSA\",\"kid\":\"no-such-key\"}".getBytes());
        String payload = b64.encodeToString(
                ("{\"sub\":\"attacker@example.com\",\"aud\":\"contractclient\","
                        + "\"service_entitlements\":{\"data_grants\":[\"" + PAYLOAD_GRANT + "\"]}}").getBytes());
        String signature = b64.encodeToString("not-a-signature".getBytes());
        return "Bearer " + header + "." + payload + "." + signature;
    }

    // ---- server ------------------------------------------------------------

    /** Returns the one record the RED-1 sanitizer then decides how much of to reveal. */
    private static Contractclient.ContractEvent record() {
        return Contractclient.ContractEvent.newBuilder()
                .setEventId("event-1")
                .setTenantNamespace("/tenant")
                .setPayload(SECRET_PAYLOAD)
                .build();
    }

    private static final class Handler implements ContractApi.ContractApiHandler {
        @Override
        public Contractclient.ContractResponse getContract(Caller caller, Contractclient.ContractRequest request) {
            return Contractclient.ContractResponse.newBuilder().setResultId("r").setAccepted(true).build();
        }

        @Override
        public Contractclient.ContractResponse getPublicContract(Caller caller, Contractclient.ContractRequest request) {
            return Contractclient.ContractResponse.newBuilder().setResultId("r").setAccepted(true).build();
        }

        @Override
        public Contractclient.ContractEvent getPublicEvent(Caller caller, Contractclient.ContractRequest request) {
            return record();
        }

        @Override
        public Iterator<Contractclient.ContractEvent> listEvents(Caller caller, Contractclient.ContractRequest request) {
            return List.of(record()).iterator();
        }

        @Override
        public CollectionRuntime.WatchSource<Contractclient.ContractEvent, Contractclient.ContractWatchChange>
                watch(Caller caller, Contractclient.ContractWatchRequest request) throws ProtoException {
            throw ProtoException.newErrGeneral(null, "not part of this test");
        }

        @Override
        public CollectionRuntime.Page<Contractclient.ContractEvent> listEventsPaged(
                Caller caller, Contractclient.ContractPageRequest request, CollectionRuntime.PageParams page)
                throws ProtoException {
            throw ProtoException.newErrGeneral(null, "not part of this test");
        }

        @Override
        public io.grpc.stub.StreamObserver<Contractclient.ContractEvent> sendEvents(
                Caller caller, io.grpc.stub.StreamObserver<Contractclient.ContractResponse> responseObserver)
                throws ProtoException {
            throw ProtoException.newErrGeneral(null, "not part of this test");
        }

        @Override
        public io.grpc.stub.StreamObserver<Contractclient.ContractEvent> assistEvents(
                Caller caller, io.grpc.stub.StreamObserver<Contractclient.ContractEvent> responseObserver)
                throws ProtoException {
            throw ProtoException.newErrGeneral(null, "not part of this test");
        }
    }

    /**
     * The production wiring: Authorizer.interceptor() (what the chassis GrpcServer
     * installs) plus Auth.requestIdInterceptor() (what kernel's ClusterServer
     * additionally installs), and the authenticate seam wired to the real
     * Authorizer exactly as the generated ServiceManager wires it.
     */
    private static Server startServer(Authorizer authorizer) throws Exception {
        ContractApi wrapper = new ContractApi(new Handler(), authorizer);
        wrapper.setAuthenticateFunc((ContractApi.Authenticator) targetNamespace -> {
            String header = Authorizer.AUTH_HEADER_CTX_KEY.get();
            if (targetNamespace == null || targetNamespace.isEmpty()) {
                authorizer.authenticateWithToken(header);
            } else {
                authorizer.authenticateAtOrBelowWithToken(header, targetNamespace);
            }
        });
        ServerBuilder<?> builder = ServerBuilder.forPort(0)
                .intercept(Auth.requestIdInterceptor())
                .intercept(Authorizer.interceptor());
        wrapper.registerServices(builder);
        return builder.build().start();
    }

    private static Contractclient.ContractRequest request() {
        return Contractclient.ContractRequest.newBuilder()
                .setTenantNamespace("/tenant")
                .setItemId("item-1")
                .setMode(Contractclient.ContractMode.CONTRACT_MODE_READ)
                .build();
    }

    private interface WithStub<T> {
        T apply(ContractApiGrpc.ContractApiBlockingStub stub) throws Exception;
    }

    /** Drains a server-streaming response so the call ends cleanly rather than cancelled. */
    private static Contractclient.ContractEvent onlyEvent(Iterator<Contractclient.ContractEvent> it) {
        Contractclient.ContractEvent first = it.next();
        while (it.hasNext()) {
            it.next();
        }
        return first;
    }

    private static <T> T call(Server server, String bearer, WithStub<T> body) throws Exception {
        ManagedChannel channel = ManagedChannelBuilder
                .forAddress("localhost", server.getPort())
                .usePlaintext()
                .build();
        try {
            Metadata headers = new Metadata();
            if (bearer != null) {
                headers.put(AUTHORIZATION, bearer);
            }
            io.grpc.Channel authed = ClientInterceptors.intercept(
                    channel, MetadataUtils.newAttachHeadersInterceptor(headers));
            return body.apply(ContractApiGrpc.newBlockingStub(authed)
                    .withDeadlineAfter(15, TimeUnit.SECONDS));
        } finally {
            channel.shutdown();
            channel.awaitTermination(5, TimeUnit.SECONDS);
        }
    }

    private static void shutdown(Server server) throws Exception {
        server.shutdownNow();
        server.awaitTermination(5, TimeUnit.SECONDS);
    }

    // ---- assertions --------------------------------------------------------

    private static void check(String name, boolean ok, String detail) {
        if (ok) {
            System.out.println("PASS " + name);
        } else {
            System.out.println("FAIL " + name + ": " + detail);
            failures++;
        }
    }

    public static void main(String[] args) throws Exception {
        ImmutableKey key = newTestKey();
        Authorizer authorizer = authorizerWithKey(key);
        Server server = startServer(authorizer);
        try {
            // 1. THE HOLE. GetPublicEvent is PUBLIC_NO_AUTH: nothing on that path
            //    ever verifies a signature. A forged bearer whose payload simply
            //    asserts the data grant must NOT unlock the raw payload.
            Contractclient.ContractEvent forged = call(server, forgedTokenWithGrant(),
                    stub -> stub.getPublicEvent(request()));
            check("no-auth RPC ignores data_grants from an unverified token",
                    forged.getPayload().isEmpty() && forged.getPayloadRedacted(),
                    "payload=\"" + forged.getPayload() + "\" payload_redacted=" + forged.getPayloadRedacted());

            // 2. Same RPC, no token at all: the fail-closed baseline. Case 1 must
            //    reveal no more than this does.
            Contractclient.ContractEvent anon = call(server, null,
                    stub -> stub.getPublicEvent(request()));
            check("no-auth RPC with no token stays redacted",
                    anon.getPayload().isEmpty() && anon.getPayloadRedacted(),
                    "payload=\"" + anon.getPayload() + "\"");

            // 3. The forged bearer is rejected outright on a method that DOES
            //    verify — the fix must not be masking a broken verify path.
            Status forgedOnVerified = statusOf(() -> call(server, forgedTokenWithGrant(),
                    stub -> onlyEvent(stub.listEvents(request()))));
            check("forged token is rejected on a verifying RPC",
                    forgedOnVerified != null && forgedOnVerified.getCode() == Status.Code.UNAUTHENTICATED,
                    "status=" + forgedOnVerified);

            // 4. A real signed token carrying the grant still gets the raw view on
            //    a verifying RPC. This is the half that must NOT regress: the fix
            //    changes where the claims come from, not what an entitled caller sees.
            Contractclient.ContractEvent entitled = call(server, signedUserTokenWithGrant(key),
                    stub -> onlyEvent(stub.listEvents(request())));
            check("verified token with the data grant still egresses the payload",
                    SECRET_PAYLOAD.equals(entitled.getPayload()) && !entitled.getPayloadRedacted(),
                    "payload=\"" + entitled.getPayload() + "\" payload_redacted=" + entitled.getPayloadRedacted());
        } finally {
            shutdown(server);
        }

        if (failures > 0) {
            System.out.println(failures + " check(s) failed");
            System.exit(1);
        }
        System.out.println("all checks passed");
    }

    private interface Body {
        void run() throws Exception;
    }

    /** Runs body and returns the gRPC Status it failed with, or null if it succeeded. */
    private static Status statusOf(Body body) throws Exception {
        try {
            body.run();
            return null;
        } catch (StatusRuntimeException e) {
            return e.getStatus();
        }
    }
}
