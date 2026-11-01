package com.example.streamexpiry.model.chassis.grpcserver;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.junit.jupiter.api.Assertions.fail;

import com.example.streamexpiry.model.chassis.key.ImmutableKey;
import com.example.streamexpiry.model.chassis.key.KeyHelpers;
import com.example.streamexpiry.model.chassis.servicemanager.Caller;
import com.example.streamexpiry.model.chassis.token.TokenWrapper;
import com.example.streamexpiry.model.s2sm.internal.grpc.servers.svc.streamexpiry.StreamerService;
import com.example.streamexpiry.model.types.svc.streamexpiry.Streamexpiry.Tick;
import com.example.streamexpiry.model.types.svc.streamexpiry.StreamerServiceGrpc;
import com.example.streamexpiry.model.types.sys.grpcconfig.Grpcconfig;
import com.example.streamexpiry.model.types.sys.host.HostOuterClass;
import com.example.streamexpiry.model.types.sys.jwk.JwkOuterClass;
import com.example.streamexpiry.model.types.sys.key.KeyOuterClass;
import com.example.streamexpiry.model.types.sys.x509.X509;
import io.grpc.Context;
import io.grpc.ManagedChannel;
import io.grpc.Metadata;
import io.grpc.ServerBuilder;
import io.grpc.Status;
import io.grpc.netty.shaded.io.grpc.netty.GrpcSslContexts;
import io.grpc.netty.shaded.io.grpc.netty.NettyChannelBuilder;
import io.grpc.stub.ClientCallStreamObserver;
import io.grpc.stub.ClientResponseObserver;
import io.grpc.stub.MetadataUtils;
import io.grpc.stub.StreamObserver;
import io.javalin.Javalin;
import java.math.BigInteger;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.security.KeyPair;
import java.security.KeyPairGenerator;
import java.security.PrivateKey;
import java.security.cert.X509Certificate;
import java.security.spec.ECGenParameterSpec;
import java.util.ArrayList;
import java.util.Base64;
import java.util.Date;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.concurrent.atomic.AtomicInteger;
import org.bouncycastle.asn1.x500.X500Name;
import org.bouncycastle.asn1.x509.Extension;
import org.bouncycastle.asn1.x509.GeneralName;
import org.bouncycastle.asn1.x509.GeneralNames;
import org.bouncycastle.cert.jcajce.JcaX509CertificateConverter;
import org.bouncycastle.cert.jcajce.JcaX509v3CertificateBuilder;
import org.bouncycastle.operator.jcajce.JcaContentSignerBuilder;
import org.junit.jupiter.api.Test;

/**
 * Issue #260, Java leg. API.md's Client Send Contract: a stream carries exactly
 * the token it opened with, and the server terminates a stream whose credential
 * has expired.
 *
 * <p>Everything on the server side is the REAL generated code: the generated
 * StreamerService wrapper (which authorizes at stream open), the generated
 * Authorizer seeded with the verifying JWK of the key the test signs with and
 * registered the way ServiceManager registers it (registerMethodAuth, wrapper
 * constructed with it), served by the generated GrpcServer itself (listen() on
 * TLS, with every interceptor it installs) on a free 127.0.0.1 port with a
 * self-signed certificate generated here. The handler keeps every stream open.
 *
 * <p>A client opens one stream of each shape with a service token whose exp is
 * ~3 s out, never rotates it, and keeps sending. Each stream must be ended by
 * the server with UNAUTHENTICATED at that exp. A control stream opened with a
 * long-lived token must stay open past the same window.
 */
class StreamExpiryBehaviorTest {
    private static final Metadata.Key<String> AUTHORIZATION =
            Metadata.Key.of("authorization", Metadata.ASCII_STRING_MARSHALLER);
    private static final long TICK_MS = 200;

    /** Server-side record that a handler learned its call ended. */
    private static final class HandlerSignals {
        final CountDownLatch sendTicksEnded = new CountDownLatch(1);
        final CountDownLatch generateTicksEnded = new CountDownLatch(1);
        final CountDownLatch assistTicksEnded = new CountDownLatch(1);
    }

    /**
     * Keeps every stream open: client-stream and bidi only ever receive;
     * server-stream sends a tick every ~200ms until its call is cancelled. The
     * short-token streams are tagged "short" so their handlers report when they
     * learn the call ended (Context cancellation).
     */
    private static final class OpenForeverHandler implements StreamerService.StreamerServiceHandler {
        final HandlerSignals signals;

        OpenForeverHandler(HandlerSignals signals) {
            this.signals = signals;
        }

        private static Tick tick(String m) {
            return Tick.newBuilder().setM(m).build();
        }

        private StreamObserver<Tick> receiveForever(CountDownLatch ended) {
            // The handler is invoked with the call's Context attached; once the
            // call ends that Context is cancelled.
            Context.current().addListener(ctx -> ended.countDown(), Runnable::run);
            return new StreamObserver<Tick>() {
                @Override public void onNext(Tick value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }

        @Override
        public StreamObserver<Tick> sendTicks(Caller caller, StreamObserver<Tick> responseObserver) {
            return receiveForever(signals.sendTicksEnded);
        }

        @Override
        public void generateTicks(Caller caller, Tick request, StreamObserver<Tick> responseObserver) {
            // Blocks the call's executor, as a naive server-streaming handler
            // does: it learns the call ended only through its Context.
            long giveUp = System.currentTimeMillis() + 60_000;
            int n = 0;
            while (!Context.current().isCancelled() && System.currentTimeMillis() < giveUp) {
                responseObserver.onNext(tick("tick-" + n++));
                try {
                    Thread.sleep(TICK_MS);
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    return;
                }
            }
            if ("short".equals(request.getM()) && Context.current().isCancelled()) {
                signals.generateTicksEnded.countDown();
            }
        }

        @Override
        public StreamObserver<Tick> assistTicks(Caller caller, StreamObserver<Tick> responseObserver) {
            return receiveForever(signals.assistTicksEnded);
        }
    }

    /** How a client stream ended. */
    private static final class Ending {
        final Status status;
        final long atMs;

        Ending(Status status, long atMs) {
            this.status = status;
            this.atMs = atMs;
        }
    }

    /** Client response observer recording how (and when) the stream ended. */
    private static final class Recorder implements ClientResponseObserver<Tick, Tick> {
        final CompletableFuture<Ending> ended = new CompletableFuture<>();
        final AtomicInteger received = new AtomicInteger();
        volatile ClientCallStreamObserver<Tick> requestStream;

        @Override
        public void beforeStart(ClientCallStreamObserver<Tick> requestStream) {
            this.requestStream = requestStream;
        }

        @Override
        public void onNext(Tick value) {
            received.incrementAndGet();
        }

        @Override
        public void onError(Throwable t) {
            ended.complete(new Ending(Status.fromThrowable(t), System.currentTimeMillis()));
        }

        @Override
        public void onCompleted() {
            ended.complete(new Ending(Status.OK, System.currentTimeMillis()));
        }

        void cancel() {
            ClientCallStreamObserver<Tick> rs = requestStream;
            if (rs != null && !ended.isDone()) {
                rs.cancel("test done", null);
            }
        }
    }

    // ---- tokens: the generated TokenWrapper, signed with a key the Authorizer trusts ----

    private static JwkOuterClass.Jwk jwkForKey(ImmutableKey key) {
        byte[] spki = key.getPublicKey().getEncoded();
        byte[] raw = new byte[32];
        System.arraycopy(spki, spki.length - 32, raw, 0, 32);
        return JwkOuterClass.Jwk.newBuilder()
                .setKid(key.getProto().getId())
                .setKty(KeyOuterClass.KeyType.KEY_TYPE_OKP)
                .setAlg(key.getAlg())
                .setCrv("Ed25519")
                .setX(Base64.getUrlEncoder().withoutPadding().encodeToString(raw))
                .build();
    }

    private static String serviceToken(ImmutableKey key, long expSecs) {
        TokenWrapper tok = TokenWrapper.create()
                .setIssuer("test-issuer")
                .setAudience(Authorizer.ACCEPTED_AUDIENCES.get(0))
                .setExpiration(expSecs)
                .setNamespace("/system")
                .setSubType("service")
                .setSubject("caller-svc");
        tok.sign(key);
        return tok.getBase64();
    }

    // ---- TLS: a self-signed serving certificate for 127.0.0.1 / localhost ----

    private static final class SelfSigned {
        final KeyPair keyPair;
        final X509Certificate cert;

        SelfSigned() throws Exception {
            KeyPairGenerator gen = KeyPairGenerator.getInstance("EC");
            gen.initialize(new ECGenParameterSpec("secp256r1"));
            keyPair = gen.generateKeyPair();
            X500Name name = new X500Name("CN=localhost");
            long now = System.currentTimeMillis();
            JcaX509v3CertificateBuilder b = new JcaX509v3CertificateBuilder(
                    name, BigInteger.valueOf(now), new Date(now - 60_000), new Date(now + 3_600_000),
                    name, keyPair.getPublic());
            b.addExtension(Extension.subjectAlternativeName, false, new GeneralNames(new GeneralName[] {
                    new GeneralName(GeneralName.dNSName, "localhost"),
                    new GeneralName(GeneralName.iPAddress, "127.0.0.1"),
            }));
            cert = new JcaX509CertificateConverter().getCertificate(
                    b.build(new JcaContentSignerBuilder("SHA256withECDSA").build(keyPair.getPrivate())));
        }

        X509.X509CertChain chain() throws Exception {
            return X509.X509CertChain.newBuilder()
                    .addChain(X509.X509Cert.newBuilder()
                            .setBase64(Base64.getEncoder().encodeToString(cert.getEncoded())))
                    .build();
        }
    }

    private static int freePort() throws Exception {
        try (ServerSocket s = new ServerSocket(0)) {
            s.setReuseAddress(true);
            return s.getLocalPort();
        }
    }

    private static void awaitListening(int port, Thread serverThread) throws Exception {
        long deadline = System.currentTimeMillis() + 20_000;
        while (System.currentTimeMillis() < deadline) {
            try (Socket s = new Socket()) {
                s.connect(new InetSocketAddress("127.0.0.1", port), 200);
                return;
            } catch (Exception notYet) {
                if (!serverThread.isAlive()) {
                    fail("generated GrpcServer.listen() exited before serving");
                }
                Thread.sleep(50);
            }
        }
        fail("generated GrpcServer did not start listening on port " + port);
    }

    /** Sends a tick every TICK_MS on a client stream until it ends. */
    private static void keepSending(ScheduledExecutorService sched, StreamObserver<Tick> requests, Recorder rec) {
        sched.scheduleAtFixedRate(() -> {
            if (rec.ended.isDone()) {
                throw new RuntimeException("stream ended; stop sending");
            }
            try {
                synchronized (requests) {
                    requests.onNext(Tick.newBuilder().setM("c").build());
                }
            } catch (RuntimeException ignored) {
                // A send racing the server's close is not this test's concern.
            }
        }, 0, TICK_MS, TimeUnit.MILLISECONDS);
    }

    @Test
    void streamsAreEndedAtTheOpeningTokensExpiry() throws Exception {
        ImmutableKey key = ImmutableKey.from(KeyHelpers.newEd25519Key());
        JwkOuterClass.Jwk jwk = jwkForKey(key);

        // ServiceManager.run's wiring, with the JWK feed seeded locally.
        Authorizer authorizer = new Authorizer(sink -> sink.update(Map.of(jwk.getKid(), jwk)));
        StreamerService.registerMethodAuth(authorizer);
        HandlerSignals signals = new HandlerSignals();
        StreamerService wrapper = new StreamerService(new OpenForeverHandler(signals), authorizer);
        ServiceImplementation impl = new ServiceImplementation() {
            @Override
            public void registerServices(ServerBuilder<?> grpcServerBuilder) {
                grpcServerBuilder.addService(wrapper);
            }

            @Override
            public void registerRESTEndpoints(Javalin app, String apiPrefix) {}
        };

        SelfSigned tls = new SelfSigned();
        X509.X509CertChain chain = tls.chain();
        int port = freePort();
        Grpcconfig.Config config = Grpcconfig.Config.newBuilder()
                .setListenAddress(HostOuterClass.Host.newBuilder().setHost("127.0.0.1").setPort(port))
                .setShutdownTimeoutSeconds(2)
                .build();
        GrpcServer srv = new GrpcServer(config, new GrpcServer.CertProvider() {
            @Override public X509.X509CertChain getChain() { return chain; }
            @Override public PrivateKey getPrivateKey() { return tls.keyPair.getPrivate(); }
        }, impl, null);
        srv.setReady(true);
        Thread serverThread = new Thread(() -> {
            try {
                srv.listen();
            } catch (Exception e) {
                e.printStackTrace();
            }
        }, "stream-expiry-grpc-server");
        serverThread.setDaemon(true);
        serverThread.start();

        ManagedChannel channel = null;
        ScheduledExecutorService sched = Executors.newScheduledThreadPool(2, r -> {
            Thread t = new Thread(r, "stream-expiry-client-sender");
            t.setDaemon(true);
            return t;
        });
        List<Recorder> all = new ArrayList<>();
        try {
            awaitListening(port, serverThread);
            channel = NettyChannelBuilder.forAddress("127.0.0.1", port)
                    .overrideAuthority("localhost")
                    .sslContext(GrpcSslContexts.forClient().trustManager(tls.cert).build())
                    .build();

            long expSecs = System.currentTimeMillis() / 1000 + 3;
            long expMs = expSecs * 1000;
            Metadata shortAuth = new Metadata();
            shortAuth.put(AUTHORIZATION, "Bearer " + serviceToken(key, expSecs));
            Metadata longAuth = new Metadata();
            longAuth.put(AUTHORIZATION, "Bearer " + serviceToken(key, expSecs + 3600));
            StreamerServiceGrpc.StreamerServiceStub shortStub = StreamerServiceGrpc.newStub(channel)
                    .withInterceptors(MetadataUtils.newAttachHeadersInterceptor(shortAuth));
            StreamerServiceGrpc.StreamerServiceStub longStub = StreamerServiceGrpc.newStub(channel)
                    .withInterceptors(MetadataUtils.newAttachHeadersInterceptor(longAuth));

            Recorder send = new Recorder();
            Recorder generate = new Recorder();
            Recorder assist = new Recorder();
            Recorder control = new Recorder();
            all.add(send);
            all.add(generate);
            all.add(assist);
            all.add(control);

            keepSending(sched, shortStub.sendTicks(send), send);
            shortStub.generateTicks(Tick.newBuilder().setM("short").build(), generate);
            keepSending(sched, shortStub.assistTicks(assist), assist);
            longStub.generateTicks(Tick.newBuilder().setM("control").build(), control);

            // Bound: exp + 5 s, plus a little slack for the wait itself. A
            // server that never ends the streams fails here instead of hanging.
            long waitUntil = expMs + 6_000;
            String[] names = {"SendTicks (client-stream)", "GenerateTicks (server-stream)", "AssistTicks (bidi)"};
            Recorder[] streams = {send, generate, assist};
            List<String> problems = new ArrayList<>();
            for (int i = 0; i < streams.length; i++) {
                Ending e;
                try {
                    e = streams[i].ended.get(Math.max(1, waitUntil - System.currentTimeMillis()), TimeUnit.MILLISECONDS);
                } catch (TimeoutException stillOpen) {
                    problems.add(names[i] + " still open " + (System.currentTimeMillis() - expMs)
                            + " ms after its token's exp; the server did not end it");
                    continue;
                }
                if (e.status.getCode() != Status.Code.UNAUTHENTICATED) {
                    problems.add(names[i] + " ended with " + e.status + ", want UNAUTHENTICATED");
                }
                if (e.atMs < expMs - 1_000) {
                    problems.add(names[i] + " ended " + (expMs - e.atMs) + " ms BEFORE its token's exp");
                }
                if (e.atMs > expMs + 5_000) {
                    problems.add(names[i] + " ended " + (e.atMs - expMs) + " ms after its token's exp (bound 5000)");
                }
                System.out.println(names[i] + ": " + e.status.getCode() + " (" + e.status.getDescription()
                        + ") at exp" + (e.atMs >= expMs ? "+" : "") + (e.atMs - expMs) + "ms");
            }
            if (!problems.isEmpty()) {
                fail(String.join("\n", problems));
            }

            // The handlers learned the calls ended (their Context was cancelled),
            // including the server-stream handler blocking its call executor.
            assertTrue(signals.sendTicksEnded.await(3, TimeUnit.SECONDS),
                    "SendTicks handler's Context was not cancelled after the expiry close");
            assertTrue(signals.assistTicksEnded.await(3, TimeUnit.SECONDS),
                    "AssistTicks handler's Context was not cancelled after the expiry close");
            assertTrue(signals.generateTicksEnded.await(3, TimeUnit.SECONDS),
                    "GenerateTicks handler did not learn its call ended (Context not cancelled)");

            // Control: the long-lived-token stream is still open past the window
            // and still receiving ticks.
            long pastExp = expMs + 1_500 - System.currentTimeMillis();
            if (pastExp > 0) {
                Thread.sleep(pastExp);
            }
            int before = control.received.get();
            Thread.sleep(3 * TICK_MS);
            assertFalse(control.ended.isDone(), "control stream (long-lived token) ended: "
                    + (control.ended.isDone() ? control.ended.getNow(null).status : null));
            assertTrue(control.received.get() > before,
                    "control stream stopped receiving ticks after the short tokens expired");
            control.cancel();
            Ending ce = control.ended.get(5, TimeUnit.SECONDS);
            assertEquals(Status.Code.CANCELLED, ce.status.getCode());
        } finally {
            for (Recorder r : all) {
                r.cancel();
            }
            sched.shutdownNow();
            if (channel != null) {
                channel.shutdownNow();
                channel.awaitTermination(5, TimeUnit.SECONDS);
            }
            srv.shutdown();
            serverThread.join(15_000);
            authorizer.close();
        }
    }
}
