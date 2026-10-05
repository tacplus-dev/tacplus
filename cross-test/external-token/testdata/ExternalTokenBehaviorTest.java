package com.example.externaltoken.model.chassis.service_im;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.example.externaltoken.model.types.services.authority.TokenMintServiceGrpc;
import com.example.externaltoken.model.types.sys.token.TokenOuterClass.Token;
import com.example.externaltoken.model.types.sys.token.TokenOuterClass.TokenPurpose;
import com.example.externaltoken.model.types.sys.tokenrequest.Tokenrequest.MintExternalServiceTokenRequest;
import com.example.externaltoken.model.types.sys.tokenrequest.Tokenrequest.MintServiceTokenRequest;
import com.example.externaltoken.model.types.sys.tokenrequest.Tokenrequest.TokenMintResponse;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import io.grpc.ManagedChannel;
import io.grpc.Server;
import io.grpc.Status;
import io.grpc.netty.shaded.io.grpc.netty.NettyChannelBuilder;
import io.grpc.netty.shaded.io.grpc.netty.NettyServerBuilder;
import io.grpc.netty.shaded.io.netty.handler.ssl.SslContext;
import io.grpc.stub.StreamObserver;
import java.lang.reflect.Constructor;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.SecureRandom;
import java.security.Signature;
import java.security.spec.ECGenParameterSpec;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Base64;
import java.util.HexFormat;
import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;
import org.junit.jupiter.api.Test;

class ExternalTokenBehaviorTest {
    private static final String SUBJECT = "spiffe://td.test/system/services/caller";
    private static final String ISSUER = "https://fleet.test";
    private static final String SERVICE_TOKEN = "service-token-for-authority";
    private static final String AUDIENCE = "https://api.anthropic.com";
    private static final long MAX_LIFETIME_SECS = 600;
    private static final Base64.Encoder B64 = Base64.getUrlEncoder().withoutPadding();
    private static final Base64.Decoder B64D = Base64.getUrlDecoder();

    /**
     * Kernel stand-in. The bootstrap exchange hands out a fixed service token;
     * the external mint enforces the contract the real kernel RPC must meet (the
     * presented token is the caller's own, exp no more than 600 s out) and signs
     * a fresh ES256 JWT with a new jti per call.
     */
    private static final class MockKernel extends TokenMintServiceGrpc.TokenMintServiceImplBase {
        final KeyPair key;
        final List<MintExternalServiceTokenRequest> requests = new CopyOnWriteArrayList<>();

        MockKernel(KeyPair key) {
            this.key = key;
        }

        @Override
        public void mintServiceToken(MintServiceTokenRequest req, StreamObserver<TokenMintResponse> out) {
            String exp = Long.toString(Instant.now().getEpochSecond() + 3600);
            String base64;
            if (req.getTokenPurpose() == TokenPurpose.TOKEN_PURPOSE_REFRESH) {
                base64 = "access-token";
            } else if (req.getInputToken().equals("access-token")) {
                base64 = SERVICE_TOKEN;
            } else {
                out.onError(Status.UNAUTHENTICATED.withDescription("phase 2 must present the phase-1 access token").asRuntimeException());
                return;
            }
            out.onNext(TokenMintResponse.newBuilder()
                    .setAccessToken(Token.newBuilder().setBase64(base64).putPayload("exp", exp))
                    .build());
            out.onCompleted();
        }

        @Override
        public void mintExternalServiceToken(MintExternalServiceTokenRequest req, StreamObserver<TokenMintResponse> out) {
            requests.add(req);
            if (!req.getInputToken().equals(SERVICE_TOKEN)) {
                out.onError(Status.UNAUTHENTICATED.withDescription("input_token is not the caller's kernel token").asRuntimeException());
                return;
            }
            long now = Instant.now().getEpochSecond();
            if (req.getExp() <= now || req.getExp() > now + MAX_LIFETIME_SECS) {
                out.onError(Status.INVALID_ARGUMENT.withDescription("exp must be within 600 seconds").asRuntimeException());
                return;
            }
            byte[] jtiBytes = new byte[16];
            new SecureRandom().nextBytes(jtiBytes);
            JsonObject claims = new JsonObject();
            claims.addProperty("iss", ISSUER);
            claims.addProperty("sub", SUBJECT);
            claims.addProperty("aud", req.getAud());
            claims.addProperty("iat", now);
            claims.addProperty("exp", req.getExp());
            claims.addProperty("jti", HexFormat.of().formatHex(jtiBytes));
            try {
                String input = B64.encodeToString("{\"alg\":\"ES256\",\"typ\":\"JWT\"}".getBytes(StandardCharsets.UTF_8))
                        + "." + B64.encodeToString(claims.toString().getBytes(StandardCharsets.UTF_8));
                Signature signer = Signature.getInstance("SHA256withECDSAinP1363Format");
                signer.initSign(key.getPrivate());
                signer.update(input.getBytes(StandardCharsets.US_ASCII));
                String signed = input + "." + B64.encodeToString(signer.sign());
                out.onNext(TokenMintResponse.newBuilder()
                        .setAccessToken(Token.newBuilder().setBase64(signed))
                        .build());
                out.onCompleted();
            } catch (Exception e) {
                out.onError(Status.INTERNAL.withCause(e).asRuntimeException());
            }
        }
    }

    private static JsonObject decodeSegment(String segment) {
        return JsonParser.parseString(new String(B64D.decode(segment), StandardCharsets.UTF_8)).getAsJsonObject();
    }

    @Test
    void tokenForExternalMintsAFreshEs256TokenOnEveryCall() throws Exception {
        Path tokenFile = Files.createTempFile("external-token", ".jwt");
        Files.writeString(tokenFile, "k8s.projected.token");

        KeyPairGenerator gen = KeyPairGenerator.getInstance("EC");
        gen.initialize(new ECGenParameterSpec("secp256r1"));
        MockKernel kernel = new MockKernel(gen.generateKeyPair());
        Server server = NettyServerBuilder.forAddress(new InetSocketAddress("127.0.0.1", 0))
                .addService(kernel)
                .build()
                .start();
        ManagedChannel channel = NettyChannelBuilder.forAddress("127.0.0.1", server.getPort()).usePlaintext().build();
        try {
            KubernetesMinter minter = new KubernetesMinter(tokenFile.toString(), "authority");
            minter.setChannel(channel);
            // create() pins bootstrap TLS to a SPIFFE trust domain; this test
            // exercises what happens after bootstrap, so it wires the same
            // pieces over a plaintext channel to the mock kernel.
            Constructor<IdentityManager> ctor = IdentityManager.class.getDeclaredConstructor(
                    KubernetesMinter.class, CloudMinter.class, ManagedChannel.class, List.class, SslContext.class, int.class);
            ctor.setAccessible(true);
            IdentityManager identity = ctor.newInstance(minter, null, channel, new ArrayList<String>(), null, 0);

            long before = Instant.now().getEpochSecond();
            String first = identity.tokenForExternal(AUDIENCE);
            String second = identity.tokenForExternal(AUDIENCE);
            long after = Instant.now().getEpochSecond();

            assertNotEquals(first, second, "an external token must never be reused");
            List<String> jtis = new ArrayList<>();
            for (String jwt : List.of(first, second)) {
                String[] parts = jwt.split("\\.");
                assertEquals(3, parts.length, "compact JWT");
                assertEquals("ES256", decodeSegment(parts[0]).get("alg").getAsString(), "external tokens are ES256, never EdDSA");
                Signature verifier = Signature.getInstance("SHA256withECDSAinP1363Format");
                verifier.initVerify(kernel.key.getPublic());
                verifier.update((parts[0] + "." + parts[1]).getBytes(StandardCharsets.US_ASCII));
                assertTrue(verifier.verify(B64D.decode(parts[2])), "token is signed by the kernel's P-256 key");
                JsonObject claims = decodeSegment(parts[1]);
                assertEquals(SUBJECT, claims.get("sub").getAsString());
                assertEquals(AUDIENCE, claims.get("aud").getAsString());
                assertEquals(ISSUER, claims.get("iss").getAsString());
                long exp = claims.get("exp").getAsLong();
                assertTrue(exp > before && exp <= after + MAX_LIFETIME_SECS, "exp " + exp + " must be within 600 s");
                jtis.add(claims.get("jti").getAsString());
            }
            assertNotEquals(jtis.get(0), jtis.get(1), "every call must carry a fresh jti");

            assertEquals(2, kernel.requests.size(), "each call must reach the kernel; nothing is cached");
            for (MintExternalServiceTokenRequest req : kernel.requests) {
                assertEquals(SERVICE_TOKEN, req.getInputToken(), "the caller's own kernel token is the credential");
                assertEquals(AUDIENCE, req.getAud());
                assertTrue(req.getExp() > before && req.getExp() <= after + MAX_LIFETIME_SECS);
            }
        } finally {
            channel.shutdownNow();
            server.shutdownNow();
            Files.deleteIfExists(tokenFile);
        }
    }
}
